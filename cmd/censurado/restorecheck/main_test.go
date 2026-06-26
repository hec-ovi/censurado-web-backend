package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

// seedArt is one article to write into a test database.
type seedArt struct {
	title string
	body  string
	when  time.Time
}

// writeDB seeds a fresh WAL-mode SQLite DB with the given articles, then closes it
// so the data is checkpointed into the main file (what a restored DB looks like).
func writeDB(t *testing.T, path string, arts []seedArt) {
	t.Helper()
	repo, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	ctx := context.Background()
	for i, sa := range arts {
		art, err := domain.NewArticle(domain.PublishInput{
			Title:   sa.title,
			Body:    sa.body,
			Author:  "tester",
			Section: "news",
		}, sa.when)
		if err != nil {
			t.Fatalf("new article %d: %v", i, err)
		}
		if _, err := repo.Upsert(ctx, art); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// writeSubs appends n submission records to an existing DB at path, each with a
// distinct idempotency key so none are deduplicated, then closes it. tag keeps the
// keys unique across the live and restored sides when both get submissions.
func writeSubs(t *testing.T, path, tag string, n int) {
	t.Helper()
	repo, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	ctx := context.Background()
	for i := 0; i < n; i++ {
		sub := store.Submission{
			IdempotencyKey: fmt.Sprintf("idem-%s-%d", tag, i),
			ContentHash:    fmt.Sprintf("hash-%s-%d", tag, i),
			ArticleID:      strconv.Itoa(i + 1),
			Slug:           fmt.Sprintf("slug-%s-%d", tag, i),
			Author:         "tester",
			Scopes:         []string{"section:news"},
			CreatedAt:      t0.Add(time.Duration(i) * time.Minute),
		}
		if err := repo.RecordSubmission(ctx, sub); err != nil {
			t.Fatalf("record submission %d: %v", i, err)
		}
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func checkNamed(t *testing.T, r Result, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, r.Checks)
	return Check{}
}

var t0 = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

// threeArticles is a fixed set whose newest (by published_at) is "Gamma latest".
func threeArticles() []seedArt {
	return []seedArt{
		{"Alpha first", "body alpha", t0},
		{"Beta second", "body beta", t0.Add(1 * time.Hour)},
		{"Gamma latest", "body gamma", t0.Add(2 * time.Hour)},
	}
}

func TestComparePass(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	restored := filepath.Join(dir, "restored.db")
	writeDB(t, live, threeArticles())
	writeDB(t, restored, threeArticles())

	r := compare(live, restored)
	if !r.Pass() {
		t.Fatalf("expected PASS, got FAIL: %+v", r.Checks)
	}
	if r.Integrity != "ok" {
		t.Fatalf("integrity = %q, want ok", r.Integrity)
	}
	if r.LiveArticles != 3 || r.RestoredArticles != 3 {
		t.Fatalf("article counts live=%d restored=%d, want 3/3", r.LiveArticles, r.RestoredArticles)
	}
	if r.LiveLatestSlug != "gamma-latest" || r.RestoredLatestSlug != "gamma-latest" {
		t.Fatalf("latest slugs live=%q restored=%q, want gamma-latest", r.LiveLatestSlug, r.RestoredLatestSlug)
	}
}

func TestCompareRowCountMismatch(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	restored := filepath.Join(dir, "restored.db")
	// Restored is missing the OLDEST row, so the newest slug still matches but the
	// count diverges. That isolates the articles-count check.
	all := threeArticles()
	writeDB(t, live, all)
	writeDB(t, restored, all[1:]) // drop the oldest

	r := compare(live, restored)
	if r.Pass() {
		t.Fatalf("expected FAIL on row-count mismatch, got PASS: %+v", r.Checks)
	}
	if c := checkNamed(t, r, "articles-count"); c.OK {
		t.Fatalf("articles-count should FAIL, got %+v", c)
	}
	// Latest slug still matches because only the oldest was dropped.
	if c := checkNamed(t, r, "latest-slug"); !c.OK {
		t.Fatalf("latest-slug should still PASS, got %+v", c)
	}
	if r.LiveArticles != 3 || r.RestoredArticles != 2 {
		t.Fatalf("counts live=%d restored=%d, want 3/2", r.LiveArticles, r.RestoredArticles)
	}
}

func TestCompareLatestSlugMismatch(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	restored := filepath.Join(dir, "restored.db")
	// Same count, but a different newest article. The first two rows are identical;
	// only the newest differs, isolating the latest-slug check.
	common := []seedArt{
		{"Alpha first", "body alpha", t0},
		{"Beta second", "body beta", t0.Add(1 * time.Hour)},
	}
	liveArts := append(append([]seedArt{}, common...), seedArt{"Live newest", "body live", t0.Add(2 * time.Hour)})
	restoredArts := append(append([]seedArt{}, common...), seedArt{"Restored newest", "body restored", t0.Add(2 * time.Hour)})
	writeDB(t, live, liveArts)
	writeDB(t, restored, restoredArts)

	r := compare(live, restored)
	if r.Pass() {
		t.Fatalf("expected FAIL on latest-slug mismatch, got PASS: %+v", r.Checks)
	}
	if c := checkNamed(t, r, "latest-slug"); c.OK {
		t.Fatalf("latest-slug should FAIL, got %+v", c)
	}
	// Counts match (3 vs 3), so that check passes.
	if c := checkNamed(t, r, "articles-count"); !c.OK {
		t.Fatalf("articles-count should PASS, got %+v", c)
	}
	if r.LiveLatestSlug != "live-newest" || r.RestoredLatestSlug != "restored-newest" {
		t.Fatalf("latest slugs live=%q restored=%q", r.LiveLatestSlug, r.RestoredLatestSlug)
	}
}

func TestCompareSubmissionsCountMismatch(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	restored := filepath.Join(dir, "restored.db")
	// Identical articles on both sides, so articles-count and latest-slug both pass;
	// only the submissions count diverges. That isolates the submissions-count check
	// the same way TestCompareRowCountMismatch isolates the articles one.
	writeDB(t, live, threeArticles())
	writeDB(t, restored, threeArticles())
	writeSubs(t, live, "live", 3)
	writeSubs(t, restored, "restored", 1)

	r := compare(live, restored)
	if r.Pass() {
		t.Fatalf("expected FAIL on submissions-count mismatch, got PASS: %+v", r.Checks)
	}
	if c := checkNamed(t, r, "submissions-count"); c.OK {
		t.Fatalf("submissions-count should FAIL, got %+v", c)
	}
	// The articles and latest-slug checks are unaffected by the submissions divergence.
	if c := checkNamed(t, r, "articles-count"); !c.OK {
		t.Fatalf("articles-count should PASS, got %+v", c)
	}
	if c := checkNamed(t, r, "latest-slug"); !c.OK {
		t.Fatalf("latest-slug should PASS, got %+v", c)
	}
	// The report fields carry the actual (non-zero) per-side counts.
	if r.LiveSubmissions != 3 || r.RestoredSubmissions != 1 {
		t.Fatalf("submission counts live=%d restored=%d, want 3/1", r.LiveSubmissions, r.RestoredSubmissions)
	}
}

func TestCompareGarbageRestored(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	restored := filepath.Join(dir, "restored.db")
	writeDB(t, live, threeArticles())
	if err := os.WriteFile(restored, []byte("this is not a sqlite database at all\n"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	r := compare(live, restored)
	if r.Pass() {
		t.Fatalf("expected FAIL on garbage restored, got PASS: %+v", r.Checks)
	}
	if c := checkNamed(t, r, "integrity_check"); c.OK {
		t.Fatalf("integrity_check should FAIL on garbage, got %+v", c)
	}
}

func TestCompareEmptyRestored(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	restored := filepath.Join(dir, "restored.db")
	writeDB(t, live, threeArticles())
	if err := os.WriteFile(restored, []byte{}, 0o600); err != nil {
		t.Fatalf("write empty: %v", err)
	}

	r := compare(live, restored)
	if r.Pass() {
		t.Fatalf("expected FAIL on empty restored, got PASS: %+v", r.Checks)
	}
}

func TestCompareMissingRestored(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	restored := filepath.Join(dir, "does-not-exist.db")
	writeDB(t, live, threeArticles())

	r := compare(live, restored)
	if r.Pass() {
		t.Fatalf("expected FAIL on missing restored, got PASS: %+v", r.Checks)
	}
}

// TestSeedAndAppendRoundTrip exercises the drill helper modes: a seeded DB plus one
// append is internally consistent, and the appended article becomes the latest.
func TestSeedAndAppendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "src.db")
	if err := seedDB(db); err != nil {
		t.Fatalf("seedDB: %v", err)
	}
	slug, err := appendArticle(db)
	if err != nil {
		t.Fatalf("appendArticle: %v", err)
	}

	// A seeded+appended DB compared against itself passes, and the latest slug is
	// the appended one (proving published_at-now sorts newest).
	r := compare(db, db)
	if !r.Pass() {
		t.Fatalf("self-compare should PASS: %+v", r.Checks)
	}
	if r.LiveArticles != len(seedBase)+1 {
		t.Fatalf("articles = %d, want %d", r.LiveArticles, len(seedBase)+1)
	}
	if r.LiveLatestSlug != slug {
		t.Fatalf("latest slug = %q, want appended %q", r.LiveLatestSlug, slug)
	}
}
