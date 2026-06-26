package publish_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

func jsonFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestDirArchive_RecordListRoundTrip(t *testing.T) {
	dir := t.TempDir()
	arch, err := publish.NewDirArchive(dir)
	if err != nil {
		t.Fatalf("new dir archive: %v", err)
	}
	ctx := context.Background()

	base := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"title":"Go 1.26 ships","body":"# Hello\n\nbody","author":"ada","section":"tech"}`)
	if err := arch.Record(ctx, publish.ArchivedPayload{
		IdempotencyKey: "key-1",
		Author:         "ada",
		ReceivedAt:     base,
		Body:           body,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Files are written 0600 (content, access-controlled).
	for _, name := range jsonFiles(t, dir) {
		fi, statErr := os.Stat(filepath.Join(dir, name))
		if statErr != nil {
			t.Fatalf("stat %s: %v", name, statErr)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("file %s perm = %o, want 600", name, perm)
		}
	}

	got, skipped, err := arch.List(ctx, time.Time{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(got) != 1 {
		t.Fatalf("got %d payloads, want 1", len(got))
	}
	p := got[0]
	if p.IdempotencyKey != "key-1" || p.Author != "ada" {
		t.Errorf("metadata = %q/%q, want key-1/ada", p.IdempotencyKey, p.Author)
	}
	if !p.ReceivedAt.Equal(base) {
		t.Errorf("received_at = %v, want %v", p.ReceivedAt, base)
	}
	if string(p.Body) != string(body) {
		t.Errorf("body round-trip = %s, want %s", p.Body, body)
	}
}

func TestDirArchive_NonJSONBodyRoundTripsViaBase64(t *testing.T) {
	dir := t.TempDir()
	arch, err := publish.NewDirArchive(dir)
	if err != nil {
		t.Fatalf("new dir archive: %v", err)
	}
	ctx := context.Background()

	raw := []byte("\x00\x01 not json at all \xff")
	if err := arch.Record(ctx, publish.ArchivedPayload{
		IdempotencyKey: "k", Author: "ada", ReceivedAt: time.Now(), Body: raw,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	// The on-disk envelope must carry the body as base64, not a broken JSON value.
	names := jsonFiles(t, dir)
	if len(names) != 1 {
		t.Fatalf("got %d files, want 1", len(names))
	}
	data, _ := os.ReadFile(filepath.Join(dir, names[0]))
	if !strings.Contains(string(data), "body_base64") {
		t.Errorf("non-JSON body should be stored as body_base64, got %s", data)
	}

	got, _, err := arch.List(ctx, time.Time{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || string(got[0].Body) != string(raw) {
		t.Fatalf("base64 round-trip failed: %q", got[0].Body)
	}
}

func TestDirArchive_OrdersByReceivedAtAndFiltersSince(t *testing.T) {
	dir := t.TempDir()
	arch, _ := publish.NewDirArchive(dir)
	ctx := context.Background()

	base := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	// Record out of time order; List must return ascending by ReceivedAt.
	for _, off := range []int{2, 0, 1} {
		body := []byte(`{"n":` + string(rune('0'+off)) + `}`)
		if err := arch.Record(ctx, publish.ArchivedPayload{
			IdempotencyKey: "k" + string(rune('0'+off)),
			Author:         "ada",
			ReceivedAt:     base.Add(time.Duration(off) * time.Minute),
			Body:           body,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	all, _, err := arch.List(ctx, time.Time{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d, want 3", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].ReceivedAt.Before(all[i-1].ReceivedAt) {
			t.Errorf("not ascending at %d: %v before %v", i, all[i].ReceivedAt, all[i-1].ReceivedAt)
		}
	}

	// since strictly after the first record drops it, keeps the two later ones.
	after, _, err := arch.List(ctx, base)
	if err != nil {
		t.Fatalf("list since: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("since base+0: got %d, want 2 (strictly after)", len(after))
	}
	if !after[0].ReceivedAt.Equal(base.Add(time.Minute)) {
		t.Errorf("first kept = %v, want base+1m", after[0].ReceivedAt)
	}
}

func TestDirArchive_SkipsMalformedFile(t *testing.T) {
	dir := t.TempDir()
	arch, _ := publish.NewDirArchive(dir)
	ctx := context.Background()

	if err := arch.Record(ctx, publish.ArchivedPayload{
		IdempotencyKey: "good", Author: "ada", ReceivedAt: time.Now(), Body: []byte(`{"ok":true}`),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Drop a corrupt envelope file alongside the good one.
	if err := os.WriteFile(filepath.Join(dir, "99999-1-deadbeef.json"), []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("write malformed: %v", err)
	}

	got, skipped, err := arch.List(ctx, time.Time{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(got) != 1 || got[0].IdempotencyKey != "good" {
		t.Errorf("got %d good payloads, want the one good record", len(got))
	}
}

// A hostile idempotency key must never escape the archive directory: filenames are
// server-controlled and the key lives only inside the envelope.
func TestDirArchive_MaliciousKeyDoesNotTraverse(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "archive")
	arch, err := publish.NewDirArchive(dir)
	if err != nil {
		t.Fatalf("new dir archive: %v", err)
	}
	ctx := context.Background()

	const evil = "../../escape"
	if err := arch.Record(ctx, publish.ArchivedPayload{
		IdempotencyKey: evil, Author: "ada", ReceivedAt: time.Now(), Body: []byte(`{"x":1}`),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Nothing was created outside the archive dir (the parent holds only "archive").
	parentEntries, _ := os.ReadDir(root)
	for _, e := range parentEntries {
		if e.Name() != "archive" {
			t.Fatalf("traversal: unexpected entry %q in parent dir", e.Name())
		}
	}
	// The one file inside the archive has a server-controlled name (no traversal).
	names := jsonFiles(t, dir)
	if len(names) != 1 {
		t.Fatalf("got %d files in archive, want 1", len(names))
	}
	if strings.Contains(names[0], "..") || strings.Contains(names[0], "escape") || strings.ContainsAny(names[0], `/\`) {
		t.Errorf("file name %q leaks the untrusted key", names[0])
	}
	// The key survived, but only inside the envelope.
	got, _, _ := arch.List(ctx, time.Time{})
	if len(got) != 1 || got[0].IdempotencyKey != evil {
		t.Fatalf("envelope did not preserve the key inside: %+v", got)
	}
}

// Apply is the post-auth accept core. This exercises it directly against a real
// sqlite store to pin its created/replay/conflict/validation classification.
func TestApply_CreateReplayConflictValidation(t *testing.T) {
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "apply.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	in := domain.PublishInput{Title: "Go 1.26 ships", Body: "# Hello\n\nbody", Author: "ada", Section: "tech"}

	res, err := publish.Apply(ctx, repo, repo, in, "k1", []string{publish.ScopeWrite}, now)
	if err != nil {
		t.Fatalf("apply create: %v", err)
	}
	if res.Failure != publish.FailureNone || !res.Created {
		t.Fatalf("create: failure=%v created=%v, want none/true", res.Failure, res.Created)
	}
	if res.Article.ID == "" || res.Article.Slug == "" {
		t.Fatalf("create returned empty id/slug: %+v", res.Article)
	}

	// Same key + same content -> idempotent replay (no new write).
	res2, err := publish.Apply(ctx, repo, repo, in, "k1", []string{publish.ScopeWrite}, now)
	if err != nil {
		t.Fatalf("apply replay: %v", err)
	}
	if res2.Failure != publish.FailureNone || res2.Created {
		t.Fatalf("replay: failure=%v created=%v, want none/false", res2.Failure, res2.Created)
	}
	if res2.Article.ID != res.Article.ID {
		t.Errorf("replay id = %q, want %q", res2.Article.ID, res.Article.ID)
	}

	// Same key + different content -> conflict.
	other := domain.PublishInput{Title: "Totally different", Body: "x", Author: "ada", Section: "tech"}
	resC, err := publish.Apply(ctx, repo, repo, other, "k1", nil, now)
	if resC.Failure != publish.FailureIdempotencyConflict {
		t.Errorf("conflict: failure=%v err=%v, want FailureIdempotencyConflict", resC.Failure, err)
	}

	// Invalid input -> validation failure with field map.
	resV, _ := publish.Apply(ctx, repo, repo, domain.PublishInput{Author: "ada"}, "k2", nil, now)
	if resV.Failure != publish.FailureValidation {
		t.Fatalf("validation: failure=%v, want FailureValidation", resV.Failure)
	}
	if resV.Fields["title"] == "" {
		t.Errorf("validation fields missing title: %+v", resV.Fields)
	}
}

// NoopArchive and a nil archive are both no-ops the publish path tolerates.
func TestNoopArchive(t *testing.T) {
	if err := (publish.NoopArchive{}).Record(context.Background(), publish.ArchivedPayload{}); err != nil {
		t.Fatalf("noop record: %v", err)
	}
}

// guard ensures the envelope is plain JSON an operator can read.
func TestDirArchive_EnvelopeIsReadableJSON(t *testing.T) {
	dir := t.TempDir()
	arch, _ := publish.NewDirArchive(dir)
	body := []byte(`{"title":"t","body":"b","author":"ada","section":"tech"}`)
	if err := arch.Record(context.Background(), publish.ArchivedPayload{
		IdempotencyKey: "key-1", Author: "ada", ReceivedAt: time.Now(), Body: body,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	names := jsonFiles(t, dir)
	data, _ := os.ReadFile(filepath.Join(dir, names[0]))
	var env map[string]any
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("envelope is not valid JSON: %v", err)
	}
	if env["idempotency_key"] != "key-1" || env["author"] != "ada" {
		t.Errorf("envelope fields = %+v, want key-1/ada", env)
	}
	if _, ok := env["received_at"].(string); !ok {
		t.Errorf("received_at missing or not a string: %+v", env["received_at"])
	}
}
