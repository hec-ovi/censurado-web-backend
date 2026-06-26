package publish

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// PayloadArchive is an append-only sink for accepted raw publish payloads. The
// handler records each newly created publish so an operator can replay payloads
// received after a restore point and recover writes that the asynchronous
// replication window dropped on an ungraceful crash. Record must be safe for
// concurrent callers.
type PayloadArchive interface {
	Record(ctx context.Context, p ArchivedPayload) error
}

// ArchivedPayload is one archived publish: the exact request JSON plus the
// metadata needed to replay it. IdempotencyKey and Author are UNTRUSTED client
// input and must never influence a filesystem path.
type ArchivedPayload struct {
	IdempotencyKey string
	Author         string
	ReceivedAt     time.Time
	Body           []byte // the raw request JSON the agent sent
}

// NoopArchive discards every payload. It is the explicit "archiving off" sink for
// callers that prefer a non-nil value; the handler also treats a nil archive as off.
type NoopArchive struct{}

// Record reports success without storing anything.
func (NoopArchive) Record(context.Context, ArchivedPayload) error { return nil }

// archiveEnvelope is the on-disk JSON shape of one archived payload. The untrusted
// idempotency key and author live INSIDE the envelope, never in the file name. The
// body is stored as a raw JSON value when it is valid JSON (the publish path only
// archives already-decoded payloads, so this is the normal case) and falls back to
// base64 for any non-JSON bytes, so Record/List round-trips the exact body.
type archiveEnvelope struct {
	IdempotencyKey string          `json:"idempotency_key"`
	Author         string          `json:"author"`
	ReceivedAt     string          `json:"received_at"` // RFC3339 (nanosecond precision)
	Body           json.RawMessage `json:"body,omitempty"`
	BodyBase64     string          `json:"body_base64,omitempty"`
}

func (e archiveEnvelope) decodeBody() ([]byte, error) {
	if len(bytes.TrimSpace(e.Body)) > 0 {
		return append([]byte(nil), e.Body...), nil
	}
	if e.BodyBase64 != "" {
		return base64.StdEncoding.DecodeString(e.BodyBase64)
	}
	return nil, errors.New("archive envelope has no body")
}

// DirArchive is an append-only filesystem sink: one JSON envelope file per payload,
// written atomically (temp file + fsync + rename). File names are entirely
// server-controlled (received-time nanoseconds, a process-local sequence number, and
// random bytes) so untrusted input can never traverse out of the archive directory.
//
// The directory holds article payloads (content, not secrets) but should still be
// access-controlled and shipped to durable storage: it is the recovery source after
// a restore. Files are written 0600 and the directory 0700.
type DirArchive struct {
	dir  string
	seq  atomic.Uint64
	logf func(format string, args ...any)
}

// NewDirArchive opens (creating if needed) an archive directory.
func NewDirArchive(dir string) (*DirArchive, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("archive: empty directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("archive: create dir %q: %w", dir, err)
	}
	return &DirArchive{dir: dir}, nil
}

// WithLogger sets where List warnings (malformed files) go. Defaults to log.Printf.
func (a *DirArchive) WithLogger(logf func(format string, args ...any)) *DirArchive {
	a.logf = logf
	return a
}

func (a *DirArchive) warnf(format string, args ...any) {
	if a.logf != nil {
		a.logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Record appends one payload as an atomically written envelope file. The file name
// derives only from server-controlled values (never the idempotency key or author),
// so a hostile key like "../../etc/x" cannot escape the directory.
func (a *DirArchive) Record(_ context.Context, p ArchivedPayload) error {
	env := archiveEnvelope{
		IdempotencyKey: p.IdempotencyKey,
		Author:         p.Author,
		ReceivedAt:     p.ReceivedAt.UTC().Format(time.RFC3339Nano),
	}
	if json.Valid(p.Body) && len(bytes.TrimSpace(p.Body)) > 0 {
		env.Body = append(json.RawMessage(nil), p.Body...)
	} else {
		env.BodyBase64 = base64.StdEncoding.EncodeToString(p.Body)
	}

	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("archive: marshal envelope: %w", err)
	}
	data = append(data, '\n')

	name, err := a.fileName(p.ReceivedAt)
	if err != nil {
		return err
	}
	return a.atomicWrite(filepath.Join(a.dir, name), data)
}

// fileName builds a unique, server-controlled file name: zero-padded received-time
// nanoseconds (so a lexical sort approximates time order), a process-local sequence
// number, and random bytes to stay unique across restarts and instances. It contains
// only digits, hyphens, and hex, so it can never be a path-traversal sequence.
func (a *DirArchive) fileName(received time.Time) (string, error) {
	rb := make([]byte, 6)
	if _, err := rand.Read(rb); err != nil {
		return "", fmt.Errorf("archive: entropy: %w", err)
	}
	return fmt.Sprintf("%020d-%012d-%s.json",
		received.UTC().UnixNano(), a.seq.Add(1), hex.EncodeToString(rb)), nil
}

// atomicWrite writes data to a temp file in the same directory, fsyncs it, then
// renames it into place. os.CreateTemp creates the temp file 0600. A best-effort
// directory fsync makes the rename itself durable where the platform supports it.
func (a *DirArchive) atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(a.dir, ".tmp-archive-*")
	if err != nil {
		return fmt.Errorf("archive: temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("archive: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("archive: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("archive: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("archive: rename: %w", err)
	}
	if d, err := os.Open(a.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// List returns the archived payloads, decoded and sorted by ReceivedAt ascending
// (stable). When since is non-zero only payloads with ReceivedAt strictly after it
// are returned. A malformed or unreadable file is skipped with a logged warning and
// counted in skipped, so one bad file never aborts a whole replay.
func (a *DirArchive) List(_ context.Context, since time.Time) (payloads []ArchivedPayload, skipped int, err error) {
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return nil, 0, fmt.Errorf("archive: read dir %q: %w", a.dir, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".tmp-") {
			continue // not an envelope (e.g. a leftover temp file)
		}
		path := filepath.Join(a.dir, name)
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			skipped++
			a.warnf("archive: skipping unreadable file %q: %v", name, rerr)
			continue
		}
		var env archiveEnvelope
		if uerr := json.Unmarshal(data, &env); uerr != nil {
			skipped++
			a.warnf("archive: skipping malformed file %q: %v", name, uerr)
			continue
		}
		received, perr := parseArchiveTime(env.ReceivedAt)
		if perr != nil {
			skipped++
			a.warnf("archive: skipping file %q with bad received_at %q: %v", name, env.ReceivedAt, perr)
			continue
		}
		body, berr := env.decodeBody()
		if berr != nil {
			skipped++
			a.warnf("archive: skipping file %q with bad body: %v", name, berr)
			continue
		}
		if !since.IsZero() && !received.After(since) {
			continue // before or at the restore point: not in scope
		}
		payloads = append(payloads, ArchivedPayload{
			IdempotencyKey: env.IdempotencyKey,
			Author:         env.Author,
			ReceivedAt:     received,
			Body:           body,
		})
	}

	sort.SliceStable(payloads, func(i, j int) bool {
		return payloads[i].ReceivedAt.Before(payloads[j].ReceivedAt)
	})
	return payloads, skipped, nil
}

// parseArchiveTime accepts the RFC3339 forms Record writes (with or without
// fractional seconds).
func parseArchiveTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
