package publish_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
)

func timeZero() time.Time { return time.Time{} }

// jsonEqual compares two JSON byte slices for semantic equality (ignoring
// formatting), so a re-encoded body matches the original payload.
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal a: %v", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal b: %v", err)
	}
	return reflect.DeepEqual(av, bv)
}

// archiveServer wires the full server handler over a real store with a DirArchive
// attached, and returns the handler plus the archive directory.
func archiveServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	h, _ := newHandler(t)
	dir := filepath.Join(t.TempDir(), "archive")
	arch, err := publish.NewDirArchive(dir)
	if err != nil {
		t.Fatalf("new dir archive: %v", err)
	}
	h = h.WithArchive(arch)
	// A nil limiter disables limiting so the test is not rate-bound.
	return publish.NewServerHandler(h, nil, nil, nil, nil), dir
}

// A new publish is archived exactly once; an idempotent replay (same key+body) is
// NOT re-archived, so client retries do not bloat the sink.
func TestServer_ArchivesNewPublishOnceNotOnReplay(t *testing.T) {
	srv, dir := archiveServer(t)
	adaToken := "ak_ada." + adaSecret

	if rec := post(t, srv, adaToken, "arch-key-1", validBody); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if names := jsonFiles(t, dir); len(names) != 1 {
		t.Fatalf("after create: %d archive files, want 1", len(names))
	}

	// The archived envelope carries the raw body, idempotency key, and author.
	arch, _ := publish.NewDirArchive(dir)
	got, _, err := arch.List(context.Background(), timeZero())
	if err != nil {
		t.Fatalf("list archive: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d payloads, want 1", len(got))
	}
	if got[0].IdempotencyKey != "arch-key-1" || got[0].Author != "ada" {
		t.Errorf("archived meta = %q/%q, want arch-key-1/ada", got[0].IdempotencyKey, got[0].Author)
	}
	if !jsonEqual(t, got[0].Body, []byte(validBody)) {
		t.Errorf("archived body = %s, want the raw request body", got[0].Body)
	}

	// Same key + same body -> 200 replay, no second archive file.
	if rec := post(t, srv, adaToken, "arch-key-1", validBody); rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if names := jsonFiles(t, dir); len(names) != 1 {
		t.Fatalf("after replay: %d archive files, want still 1", len(names))
	}

	// A genuinely new article (different content) -> 201 and a second archive file.
	other := `{"title":"Markets dip","body":"b","author":"ada","section":"economics"}`
	if rec := post(t, srv, adaToken, "arch-key-2", other); rec.Code != http.StatusCreated {
		t.Fatalf("second create status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if names := jsonFiles(t, dir); len(names) != 2 {
		t.Fatalf("after second create: %d archive files, want 2", len(names))
	}
}

// A malicious Idempotency-Key must never create a file outside the archive dir; the
// key is preserved only inside the envelope.
func TestServer_MaliciousKeyDoesNotEscapeArchive(t *testing.T) {
	h, _ := newHandler(t)
	root := t.TempDir()
	dir := filepath.Join(root, "archive")
	arch, err := publish.NewDirArchive(dir)
	if err != nil {
		t.Fatalf("new dir archive: %v", err)
	}
	srv := publish.NewServerHandler(h.WithArchive(arch), nil, nil, nil, nil)
	adaToken := "ak_ada." + adaSecret

	const evil = "../../escape"
	if rec := post(t, srv, adaToken, evil, validBody); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}

	// The archive dir is the only thing under root, and the file name is safe.
	for _, e := range mustReadDir(t, root) {
		if e != "archive" {
			t.Fatalf("traversal: unexpected %q outside the archive dir", e)
		}
	}
	names := jsonFiles(t, dir)
	if len(names) != 1 {
		t.Fatalf("got %d archive files, want 1", len(names))
	}
	if strings.Contains(names[0], "..") || strings.Contains(names[0], "escape") {
		t.Errorf("file name %q leaks the untrusted key", names[0])
	}
	got, _, _ := arch.List(context.Background(), timeZero())
	if len(got) != 1 || got[0].IdempotencyKey != evil {
		t.Fatalf("the key must survive inside the envelope: %+v", got)
	}
}

// failArchive always fails Record. It exists to prove archiving is best-effort:
// the publish path records the payload AFTER the write commits, so a Record error
// must be logged and swallowed, never propagated to a client whose article already
// exists.
type failArchive struct{}

func (failArchive) Record(context.Context, publish.ArchivedPayload) error {
	return errors.New("disk full")
}

// A failing archive Record must NOT fail a publish that already committed. The
// response stays 201 with a well-formed body, the article is actually stored, and
// the failure is LOGGED exactly once (swallowed best-effort), not dropped silently
// and not surfaced to the caller. This covers the warnf branch in recordArchive,
// which every DirArchive-backed test leaves green because its Record succeeds.
func TestServer_FailingArchiveDoesNotFailCommittedPublish(t *testing.T) {
	h, repo := newHandler(t)
	var logged []string
	h = h.WithArchive(failArchive{}).WithLogger(func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	srv := publish.NewServerHandler(h, nil, nil, nil, nil) // nil limiter: not rate-bound
	adaToken := "ak_ada." + adaSecret

	rec := post(t, srv, adaToken, "arch-fail-1", validBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 despite archive failure (%s)", rec.Code, rec.Body.String())
	}
	id, slug := decodeCreate(t, rec)
	if id == "" || slug == "" {
		t.Fatalf("create returned empty id/slug: %s", rec.Body.String())
	}

	// The write committed even though archiving failed: the article is retrievable.
	if got, err := repo.BySlug(context.Background(), slug); err != nil {
		t.Fatalf("article not stored after archive failure: %v", err)
	} else if got.ID != id {
		t.Errorf("stored article id = %q, want %q", got.ID, id)
	}

	// The failure was logged exactly once, mentioning the error and the untrusted
	// key, so it is observable to an operator rather than dropped silently.
	if len(logged) != 1 {
		t.Fatalf("archive failure logged %d times, want exactly 1: %v", len(logged), logged)
	}
	if !strings.Contains(logged[0], "disk full") || !strings.Contains(logged[0], "arch-fail-1") {
		t.Errorf("log line = %q, want it to mention the error and the idempotency key", logged[0])
	}
}

func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
