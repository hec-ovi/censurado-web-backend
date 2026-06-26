package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

func openStore(t *testing.T) *sqlite.Store {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "replay.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func openArchive(t *testing.T) (*publish.DirArchive, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "archive")
	arch, err := publish.NewDirArchive(dir)
	if err != nil {
		t.Fatalf("new archive: %v", err)
	}
	return arch, dir
}

func bodyJSON(title string) string {
	return fmt.Sprintf(`{"title":%q,"body":"# H\n\nbody text","author":"ada","section":"tech"}`, title)
}

func seed(t *testing.T, arch *publish.DirArchive, key, body string, received time.Time) {
	t.Helper()
	if err := arch.Record(context.Background(), publish.ArchivedPayload{
		IdempotencyKey: key,
		Author:         "ada",
		ReceivedAt:     received,
		Body:           []byte(body),
	}); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func count(t *testing.T, repo store.Repository) int {
	t.Helper()
	n, err := repo.Count(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// The core exactly-once proof: replay into an empty store recovers everything, and a
// second replay is a pure no-op (nothing double-published, article count unchanged).
func TestReplay_ExactlyOnce(t *testing.T) {
	repo := openStore(t)
	arch, _ := openArchive(t)
	base := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	seed(t, arch, "k1", bodyJSON("Alpha launch"), base.Add(1*time.Minute))
	seed(t, arch, "k2", bodyJSON("Beta release"), base.Add(2*time.Minute))
	seed(t, arch, "k3", bodyJSON("Gamma ships"), base.Add(3*time.Minute))

	ctx := context.Background()
	var out bytes.Buffer
	first, err := replay(ctx, repo, repo, arch, time.Time{}, false, &out)
	if err != nil {
		t.Fatalf("first replay: %v", err)
	}
	if first.Considered != 3 || first.Recovered != 3 || first.AlreadyPresent != 0 || first.Failed != 0 {
		t.Fatalf("first replay tally = %+v, want considered/recovered=3, rest 0", first)
	}
	if got := count(t, repo); got != 3 {
		t.Fatalf("article count after first replay = %d, want 3", got)
	}

	// Second replay: every payload is already present; no double-publish.
	out.Reset()
	second, err := replay(ctx, repo, repo, arch, time.Time{}, false, &out)
	if err != nil {
		t.Fatalf("second replay: %v", err)
	}
	if second.Considered != 3 || second.Recovered != 0 || second.AlreadyPresent != 3 || second.Failed != 0 {
		t.Fatalf("second replay tally = %+v, want considered=3 present=3, rest 0", second)
	}
	if got := count(t, repo); got != 3 {
		t.Fatalf("article count after second replay = %d, want still 3 (exactly-once)", got)
	}
}

// -since replays only payloads strictly after the restore point.
func TestReplay_SinceFiltersByRestorePoint(t *testing.T) {
	repo := openStore(t)
	arch, _ := openArchive(t)
	base := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	seed(t, arch, "before-1", bodyJSON("Old one"), base.Add(-2*time.Minute))
	seed(t, arch, "before-2", bodyJSON("Old two"), base) // exactly at the point: excluded
	seed(t, arch, "after-1", bodyJSON("New one"), base.Add(1*time.Minute))
	seed(t, arch, "after-2", bodyJSON("New two"), base.Add(2*time.Minute))

	ctx := context.Background()
	var out bytes.Buffer
	tl, err := replay(ctx, repo, repo, arch, base, false, &out)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if tl.Considered != 2 || tl.Recovered != 2 {
		t.Fatalf("since tally = %+v, want considered/recovered=2 (only strictly-after)", tl)
	}
	if got := count(t, repo); got != 2 {
		t.Fatalf("article count = %d, want 2 (pre-restore payloads skipped)", got)
	}
}

// A payload whose article survived the restore is a no-op on replay; the count does
// not change.
func TestReplay_ExistingArticleIsNoOp(t *testing.T) {
	repo := openStore(t)
	arch, _ := openArchive(t)
	base := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	seed(t, arch, "k1", bodyJSON("Survivor"), base.Add(1*time.Minute))
	seed(t, arch, "k2", bodyJSON("Lost write"), base.Add(2*time.Minute))

	// Simulate a restore where k1's write survived: apply it directly first.
	in, err := decodePayload([]byte(bodyJSON("Survivor")))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res, aerr := publish.Apply(context.Background(), repo, repo, in, "k1", nil, base.Add(1*time.Minute)); aerr != nil || !res.Created {
		t.Fatalf("seed survivor: created=%v err=%v", res.Created, aerr)
	}
	if got := count(t, repo); got != 1 {
		t.Fatalf("precondition count = %d, want 1", got)
	}

	var out bytes.Buffer
	tl, err := replay(context.Background(), repo, repo, arch, time.Time{}, false, &out)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if tl.Considered != 2 || tl.Recovered != 1 || tl.AlreadyPresent != 1 || tl.Failed != 0 {
		t.Fatalf("tally = %+v, want considered=2 recovered=1 present=1", tl)
	}
	if got := count(t, repo); got != 2 {
		t.Fatalf("article count = %d, want 2 (survivor untouched, lost write recovered)", got)
	}
}

// -dry-run writes nothing but still projects the outcome.
func TestReplay_DryRunWritesNothing(t *testing.T) {
	repo := openStore(t)
	arch, _ := openArchive(t)
	base := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	seed(t, arch, "k1", bodyJSON("One"), base.Add(1*time.Minute))
	seed(t, arch, "k2", bodyJSON("Two"), base.Add(2*time.Minute))

	var out bytes.Buffer
	tl, err := replay(context.Background(), repo, repo, arch, time.Time{}, true, &out)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if tl.Considered != 2 || tl.Recovered != 2 {
		t.Fatalf("dry-run tally = %+v, want considered/recovered=2 projected", tl)
	}
	if got := count(t, repo); got != 0 {
		t.Fatalf("dry-run wrote %d articles, want 0", got)
	}
}

// A malformed file in the archive is skipped, not fatal; a bad body inside a valid
// envelope is a failure that makes the tool exit nonzero.
func TestReplay_SkipsMalformedAndFailsOnBadBody(t *testing.T) {
	repo := openStore(t)
	arch, dir := openArchive(t)
	base := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	seed(t, arch, "ok", bodyJSON("Good"), base.Add(1*time.Minute))
	// A valid envelope whose body is valid JSON but an invalid article (missing fields).
	seed(t, arch, "bad", `{"author":"ada"}`, base.Add(2*time.Minute))
	// A corrupt file is skipped by List.
	writeFile(t, filepath.Join(dir, "00000000000000000000-1-corrupt.json"), "{ not json")

	var out bytes.Buffer
	tl, err := replay(context.Background(), repo, repo, arch, time.Time{}, false, &out)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if tl.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (corrupt file)", tl.Skipped)
	}
	if tl.Recovered != 1 || tl.Failed != 1 {
		t.Fatalf("tally = %+v, want recovered=1 failed=1", tl)
	}
}

// run() is the real entry point: it opens the store, lists the archive, replays, and
// returns the exit code (1 when any payload failed).
func TestRun_ExitCodes(t *testing.T) {
	arch, dir := openArchive(t)
	base := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	seed(t, arch, "k1", bodyJSON("Recoverable"), base.Add(1*time.Minute))
	dbPath := filepath.Join(t.TempDir(), "run.db")

	t.Run("clean replay -> exit 0", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"-db", dbPath, "-archive", dir}, func(string) string { return "" }, &stdout, &stderr)
		if code != exitOK {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitOK, stderr.String())
		}
		if !bytes.Contains(stdout.Bytes(), []byte("recovered  : 1")) {
			t.Errorf("stdout missing recovered count: %s", stdout.String())
		}
	})

	t.Run("missing -archive -> usage error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"-db", dbPath}, func(string) string { return "" }, &stdout, &stderr)
		if code != exitUsage {
			t.Fatalf("exit = %d, want %d", code, exitUsage)
		}
	})

	t.Run("failing payload -> exit 1", func(t *testing.T) {
		fa, fdir := openArchive(t)
		seed(t, fa, "bad", `{"author":"ada"}`, base.Add(1*time.Minute))
		var stdout, stderr bytes.Buffer
		code := run([]string{"-db", filepath.Join(t.TempDir(), "f.db"), "-archive", fdir},
			func(string) string { return "" }, &stdout, &stderr)
		if code != exitFailed {
			t.Fatalf("exit = %d, want %d (a payload failed)", code, exitFailed)
		}
	})
}

// TestReplay_RecoversBatchArchive proves the full durability round-trip for a batch
// publish: drive the real POST /articles:batch handler with an archive attached, so
// it writes one envelope per created item, then recover those writes through the
// replay tool into a FRESH empty store (as after a restore that lost the batch).
// All items come back, multi-author attribution is preserved, and a second replay
// is exactly-once. No replay code is batch-aware: each envelope is a single article,
// which is exactly what makes a batch recoverable like single publishes.
func TestReplay_RecoversBatchArchive(t *testing.T) {
	base := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	// Produce a batch archive through the real handler.
	srcRepo, err := sqlite.Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatalf("src store: %v", err)
	}
	t.Cleanup(func() { _ = srcRepo.Close() })

	arch, _ := openArchive(t)
	const secret = "batch-replay-secret-1234567890"
	auth := publish.NewStaticKeyAuth()
	auth.Add("ak_ed", publish.HashSecret(secret), "editor", publish.ScopeWrite, publish.ScopePublishAny)
	h := publish.NewHandler(srcRepo, srcRepo, auth, func() time.Time { return base }).WithArchive(arch)
	srv := publish.NewServerHandler(h, nil, nil, nil, nil)

	body := `{"articles":[
		{"title":"Batch A","body":"b","author":"ada","section":"tech","idempotency_key":"ba"},
		{"title":"Batch B","body":"b","author":"bo","section":"world","idempotency_key":"bb"},
		{"title":"Batch C","body":"b","author":"cy","section":"politics","idempotency_key":"bc"},
		{"title":"Batch D","body":"b","author":"di","section":"economics","idempotency_key":"bd"}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/articles:batch", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer ak_ed."+secret)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("batch publish status = %d (%s)", rec.Code, rec.Body.String())
	}

	// Recover into a fresh, empty store.
	repo := openStore(t)
	var out bytes.Buffer
	tl, err := replay(context.Background(), repo, repo, arch, time.Time{}, false, &out)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if tl.Considered != 4 || tl.Recovered != 4 || tl.Failed != 0 {
		t.Fatalf("tally = %+v, want considered/recovered=4, failed 0", tl)
	}
	if got := count(t, repo); got != 4 {
		t.Fatalf("recovered article count = %d, want 4", got)
	}
	// Multi-author attribution survives the round-trip.
	for _, author := range []string{"ada", "bo", "cy", "di"} {
		n, err := repo.Count(context.Background(), store.Filter{Author: author})
		if err != nil {
			t.Fatalf("count author %s: %v", author, err)
		}
		if n != 1 {
			t.Errorf("recovered %d articles for author %s, want 1", n, author)
		}
	}

	// Second replay is exactly-once: everything is already present, nothing doubles.
	out.Reset()
	second, err := replay(context.Background(), repo, repo, arch, time.Time{}, false, &out)
	if err != nil {
		t.Fatalf("second replay: %v", err)
	}
	if second.Recovered != 0 || second.AlreadyPresent != 4 {
		t.Fatalf("second tally = %+v, want present=4 recovered=0 (exactly-once)", second)
	}
	if got := count(t, repo); got != 4 {
		t.Fatalf("count after second replay = %d, want 4", got)
	}
}

func TestParseSince(t *testing.T) {
	if ts, err := parseSince(""); err != nil || !ts.IsZero() {
		t.Errorf("empty: %v %v, want zero time", ts, err)
	}
	if ts, err := parseSince("1750593600"); err != nil || ts.Unix() != 1750593600 {
		t.Errorf("unix: %v %v", ts, err)
	}
	if ts, err := parseSince("2026-06-22T12:00:00Z"); err != nil || ts.Year() != 2026 {
		t.Errorf("rfc3339: %v %v", ts, err)
	}
	if _, err := parseSince("not-a-time"); err == nil {
		t.Error("want error for garbage -since")
	}
}
