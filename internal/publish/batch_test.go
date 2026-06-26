package publish_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/store"
)

// bItem is the test-side shape of one batch element: the article fields plus the
// per-item idempotency key.
type bItem struct {
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Author   string         `json:"author"`
	Section  string         `json:"section"`
	Topics   []string       `json:"topics,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Key      string         `json:"idempotency_key"`
}

type batchResult struct {
	Index  int    `json:"index"`
	ID     string `json:"id"`
	Slug   string `json:"slug"`
	Status string `json:"status"`
}

// batchServer wraps newHandler in the real server mux so POST /articles:batch is
// exercised through routing and the limiter. A nil limiter disables limiting.
func batchServer(t *testing.T, limiter *publish.RateLimiter) (http.Handler, store.Repository, *publish.Handler) {
	t.Helper()
	h, repo := newHandler(t)
	return publish.NewServerHandler(h, limiter, nil, nil, nil), repo, h
}

func batchBody(t *testing.T, items []bItem) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"articles": items})
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	return string(b)
}

func postBatch(t *testing.T, h http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/articles:batch", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBatch(t *testing.T, rec *httptest.ResponseRecorder) []batchResult {
	t.Helper()
	var got struct {
		Results []batchResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode batch response: %v (%s)", err, rec.Body.String())
	}
	return got.Results
}

func submissionCount(t *testing.T, repo store.Repository) int {
	t.Helper()
	subs, err := repo.(store.SubmissionLog).ListSubmissions(context.Background(), store.ListSubmissionsFilter{Limit: 100000})
	if err != nil {
		t.Fatalf("list submissions: %v", err)
	}
	return len(subs)
}

const opToken = "ak_op." + opSecret

// Case 1: a batch of 100 valid articles (varied authors, topics, long bodies,
// media references) commits in one transaction; all created; per-item ids/slugs
// returned; the db has 100 article rows and 100 audit rows.
func TestBatch_HundredValidCommitInOneTransaction(t *testing.T) {
	h, repo, _ := batchServer(t, nil)

	authors := []string{"ada", "bo", "zoe", "lin", "cy"}
	sections := []string{"tech", "world", "politics", "economics"}
	longBody := strings.Repeat("All the news that fits, we render. ", 4000) // ~140 KB, no content cap
	items := make([]bItem, 100)
	for i := range items {
		it := bItem{
			Title:   fmt.Sprintf("Dispatch number %d on the state of things", i),
			Body:    fmt.Sprintf("# Headline %d\n\nbody text for article %d", i, i),
			Author:  authors[i%len(authors)],
			Section: sections[i%len(sections)],
			Topics:  []string{"go", fmt.Sprintf("topic-%d", i%7)},
			Key:     fmt.Sprintf("bk-%d", i),
		}
		if i == 0 {
			it.Body = longBody // prove a single long body is admitted in a batch
		}
		if i%7 == 0 {
			it.Metadata = map[string]any{"image": fmt.Sprintf("/media/%064x.jpg", i), "image_alt": "a hero"}
		}
		if i%11 == 0 {
			it.Metadata = map[string]any{"youtube": "https://www.youtube.com/watch?v=dQw4w9WgXcQ"}
		}
		items[i] = it
	}

	rec := postBatch(t, h, opToken, batchBody(t, items))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, truncate(rec.Body.String()))
	}
	results := decodeBatch(t, rec)
	if len(results) != 100 {
		t.Fatalf("results = %d, want 100", len(results))
	}
	for i, r := range results {
		if r.Index != i {
			t.Errorf("results[%d].Index = %d, want %d (input order)", i, r.Index, i)
		}
		if r.Status != "created" {
			t.Errorf("results[%d].Status = %q, want created", i, r.Status)
		}
		if r.ID == "" || r.Slug == "" {
			t.Errorf("results[%d] empty id/slug: %+v", i, r)
		}
	}
	if n, err := repo.Count(context.Background(), store.Filter{}); err != nil || n != 100 {
		t.Errorf("article count = %d (err %v), want 100", n, err)
	}
	if n := submissionCount(t, repo); n != 100 {
		t.Errorf("audit rows = %d, want 100", n)
	}
}

// Case 2: one invalid item (empty title) returns 422 with the offending index and
// reason; zero rows written (atomic, validate-all-then-one-transaction).
func TestBatch_AtomicityOneInvalidWritesNothing(t *testing.T) {
	h, repo, _ := batchServer(t, nil)

	body := batchBody(t, []bItem{
		{Title: "Good one", Body: "b", Author: "ada", Section: "tech", Key: "a1"},
		{Title: "   ", Body: "b", Author: "ada", Section: "tech", Key: "a2"}, // empty title after trim
		{Title: "Good two", Body: "b", Author: "ada", Section: "tech", Key: "a3"},
	})
	rec := postBatch(t, h, "ak_ada."+adaSecret, body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (%s)", rec.Code, truncate(rec.Body.String()))
	}
	if !strings.Contains(rec.Body.String(), `"index":1`) {
		t.Errorf("response should name offending index 1: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "validation_failed") || !strings.Contains(rec.Body.String(), "title") {
		t.Errorf("response should explain the title failure: %s", rec.Body.String())
	}
	if n, _ := repo.Count(context.Background(), store.Filter{}); n != 0 {
		t.Errorf("article count = %d, want 0 (nothing written)", n)
	}
	if n := submissionCount(t, repo); n != 0 {
		t.Errorf("audit rows = %d, want 0", n)
	}
}

// Case 3: replaying the same batch returns all deduplicated with no duplicate rows;
// a mixed batch (some new, some replayed) returns the correct per-item statuses.
func TestBatch_IdempotentReplayAndMixed(t *testing.T) {
	h, repo, _ := batchServer(t, nil)
	tok := "ak_ada." + adaSecret

	first := []bItem{
		{Title: "Replay one", Body: "b1", Author: "ada", Section: "tech", Key: "r1"},
		{Title: "Replay two", Body: "b2", Author: "ada", Section: "tech", Key: "r2"},
	}
	if rec := postBatch(t, h, tok, batchBody(t, first)); rec.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want 201 (%s)", rec.Code, truncate(rec.Body.String()))
	}

	rec := postBatch(t, h, tok, batchBody(t, first))
	if rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", rec.Code)
	}
	for i, r := range decodeBatch(t, rec) {
		if r.Status != "deduplicated" {
			t.Errorf("replay results[%d].Status = %q, want deduplicated", i, r.Status)
		}
	}
	if n, _ := repo.Count(context.Background(), store.Filter{}); n != 2 {
		t.Errorf("count after replay = %d, want 2 (no new rows)", n)
	}

	mixed := []bItem{
		first[0], // replay
		{Title: "Brand new three", Body: "b3", Author: "ada", Section: "world", Key: "r3"},
	}
	rec = postBatch(t, h, tok, batchBody(t, mixed))
	if rec.Code != http.StatusCreated {
		t.Fatalf("mixed status = %d, want 201 (something created)", rec.Code)
	}
	got := decodeBatch(t, rec)
	if got[0].Status != "deduplicated" {
		t.Errorf("mixed[0].Status = %q, want deduplicated", got[0].Status)
	}
	if got[1].Status != "created" {
		t.Errorf("mixed[1].Status = %q, want created", got[1].Status)
	}
	if n, _ := repo.Count(context.Background(), store.Filter{}); n != 3 {
		t.Errorf("count after mixed = %d, want 3", n)
	}
}

// Case 4: auth. No token 401; a token without articles:write 403; a multi-author
// batch with a write-only key 422 listing the offending item; with publish-any it
// succeeds.
func TestBatch_Auth(t *testing.T) {
	h, repo, _ := batchServer(t, nil)

	single := batchBody(t, []bItem{{Title: "x", Body: "y", Author: "ada", Section: "tech", Key: "k1"}})

	t.Run("no token -> 401", func(t *testing.T) {
		if rec := postBatch(t, h, "", single); rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
	t.Run("read-only scope -> 403", func(t *testing.T) {
		if rec := postBatch(t, h, "ak_ro."+roSecret, single); rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
	})

	multi := batchBody(t, []bItem{
		{Title: "Mine", Body: "b", Author: "ada", Section: "tech", Key: "m1"},
		{Title: "Not mine", Body: "b", Author: "bo", Section: "tech", Key: "m2"},
	})
	t.Run("write-only key cannot author for others -> 422 naming the item", func(t *testing.T) {
		rec := postBatch(t, h, "ak_ada."+adaSecret, multi)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422 (%s)", rec.Code, truncate(rec.Body.String()))
		}
		if !strings.Contains(rec.Body.String(), "author_mismatch") || !strings.Contains(rec.Body.String(), `"index":1`) {
			t.Errorf("response should name the offending author item: %s", rec.Body.String())
		}
		if n, _ := repo.Count(context.Background(), store.Filter{}); n != 0 {
			t.Errorf("count = %d, want 0 (nothing written)", n)
		}
	})
	t.Run("publish-any key authors for anyone -> 201", func(t *testing.T) {
		if rec := postBatch(t, h, opToken, multi); rec.Code != http.StatusCreated {
			t.Errorf("status = %d, want 201 (%s)", rec.Code, truncate(rec.Body.String()))
		}
	})
}

// Case 5: limits and memory. A batch at the configured item cap succeeds; exceeding
// the item cap or the body byte cap returns a clear 422/413; no content-length cap
// on an individual article body.
func TestBatch_LimitsAndNoContentCap(t *testing.T) {
	t.Run("at the item cap succeeds, over it is 422", func(t *testing.T) {
		h, _, handler := batchServer(t, nil)
		handler.WithLimits(0, 3) // cap at 3 items

		mk := func(n int) string {
			items := make([]bItem, n)
			for i := range items {
				items[i] = bItem{Title: fmt.Sprintf("Cap item %d", i), Body: "b", Author: "ada", Section: "tech", Key: fmt.Sprintf("c%d", i)}
			}
			return batchBody(t, items)
		}
		if rec := postBatch(t, h, "ak_ada."+adaSecret, mk(3)); rec.Code != http.StatusCreated {
			t.Errorf("3 items status = %d, want 201 (%s)", rec.Code, truncate(rec.Body.String()))
		}
		rec := postBatch(t, h, "ak_ada."+adaSecret, mk(4))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("4 items status = %d, want 422", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "too_many_items") {
			t.Errorf("want too_many_items: %s", rec.Body.String())
		}
	})

	t.Run("body over the byte cap is 413", func(t *testing.T) {
		h, _, handler := batchServer(t, nil)
		handler.WithLimits(200, 0) // 200-byte cap
		body := batchBody(t, []bItem{
			{Title: "Over the byte cap", Body: strings.Repeat("padding ", 50), Author: "ada", Section: "tech", Key: "big"},
		})
		if len(body) <= 200 {
			t.Fatalf("test body is %d bytes, expected > 200 to trip the cap", len(body))
		}
		rec := postBatch(t, h, "ak_ada."+adaSecret, body)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413 (%s)", rec.Code, truncate(rec.Body.String()))
		}
	})

	t.Run("no content-length cap on a single article body", func(t *testing.T) {
		h, repo, _ := batchServer(t, nil)      // default 8 MiB transport cap
		huge := strings.Repeat("word ", 60000) // ~300 KB, far over any small cap, under 8 MiB
		body := batchBody(t, []bItem{{Title: "A very long read", Body: huge, Author: "ada", Section: "tech", Key: "long"}})
		rec := postBatch(t, h, "ak_ada."+adaSecret, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 for a long body (%s)", rec.Code, truncate(rec.Body.String()))
		}
		if n, _ := repo.Count(context.Background(), store.Filter{}); n != 1 {
			t.Errorf("count = %d, want 1", n)
		}
	})
}

// Case 6: a batch counts as one request against the limiter, so a single large
// batch is not throttled item by item.
func TestBatch_CountsAsOneRateLimitToken(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC) }
	limiter := publish.NewRateLimiter(0, 1, clock) // one token, never refills
	h, repo, _ := batchServer(t, limiter)

	items := make([]bItem, 5)
	for i := range items {
		items[i] = bItem{Title: fmt.Sprintf("Token item %d", i), Body: "b", Author: "ada", Section: "tech", Key: fmt.Sprintf("t%d", i)}
	}
	// A 5-item batch consumes exactly one token: if it were charged per item it
	// would need 5 and fail at burst 1.
	if rec := postBatch(t, h, "ak_ada."+adaSecret, batchBody(t, items)); rec.Code != http.StatusCreated {
		t.Fatalf("first batch status = %d, want 201 (%s)", rec.Code, truncate(rec.Body.String()))
	}
	if n, _ := repo.Count(context.Background(), store.Filter{}); n != 5 {
		t.Fatalf("count = %d, want 5 (whole batch admitted on one token)", n)
	}
	// The single token is now spent, so an immediate second batch is rate-limited.
	more := batchBody(t, []bItem{{Title: "After the limit", Body: "b", Author: "ada", Section: "tech", Key: "t-late"}})
	if rec := postBatch(t, h, "ak_ada."+adaSecret, more); rec.Code != http.StatusTooManyRequests {
		t.Errorf("second batch status = %d, want 429 (token spent)", rec.Code)
	}
}

// Case 7: two concurrent batches serialize at the single writer; both land
// consistently with no corruption.
func TestBatch_ConcurrentBatchesSerialize(t *testing.T) {
	h, repo, _ := batchServer(t, nil)

	mkBatch := func(prefix string) string {
		items := make([]bItem, 20)
		for i := range items {
			items[i] = bItem{
				Title:   fmt.Sprintf("%s story %d", prefix, i),
				Body:    "b",
				Author:  "ada",
				Section: "tech",
				Key:     fmt.Sprintf("%s-%d", prefix, i),
			}
		}
		return batchBody(t, items)
	}

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for g, prefix := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(idx int, p string) {
			defer wg.Done()
			rec := postBatch(t, h, opToken, mkBatch(p))
			codes[idx] = rec.Code
		}(g, prefix)
	}
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusCreated {
			t.Errorf("batch %d status = %d, want 201", i, c)
		}
	}
	if n, err := repo.Count(context.Background(), store.Filter{}); err != nil || n != 40 {
		t.Errorf("count = %d (err %v), want 40 (both batches landed)", n, err)
	}
}

// TestBatch_ArchivesEachCreatedItem proves a batch writes one replay-shaped archive
// envelope per NEWLY created item (and re-archives nothing on a deduplicated
// replay), so a batch is recoverable exactly like single publishes. The replay
// round-trip itself is proven end to end in cmd/censurado/replay.
func TestBatch_ArchivesEachCreatedItem(t *testing.T) {
	h, _ := newHandler(t)
	dir := t.TempDir()
	arch, err := publish.NewDirArchive(dir)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	h.WithArchive(arch)
	srv := publish.NewServerHandler(h, nil, nil, nil, nil)

	body := batchBody(t, []bItem{
		{Title: "Arch one", Body: "b1", Author: "ada", Section: "tech", Key: "arc-1"},
		{Title: "Arch two", Body: "b2", Author: "ada", Section: "tech", Key: "arc-2"},
	})
	if rec := postBatch(t, srv, "ak_ada."+adaSecret, body); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, truncate(rec.Body.String()))
	}

	payloads, skipped, err := arch.List(context.Background(), time.Time{})
	if err != nil || skipped != 0 {
		t.Fatalf("List: err=%v skipped=%d", err, skipped)
	}
	if len(payloads) != 2 {
		t.Fatalf("archived %d envelopes, want 2 (one per created item)", len(payloads))
	}
	keys := map[string]bool{}
	for _, p := range payloads {
		keys[p.IdempotencyKey] = true
		var one map[string]any
		if err := json.Unmarshal(p.Body, &one); err != nil {
			t.Errorf("envelope body is not a JSON object: %v", err)
		}
		if _, ok := one["title"]; !ok {
			t.Errorf("envelope body missing title (must be a single article): %s", p.Body)
		}
		if _, ok := one["articles"]; ok {
			t.Errorf("envelope must be a single article, not the batch wrapper: %s", p.Body)
		}
	}
	if !keys["arc-1"] || !keys["arc-2"] {
		t.Errorf("archived keys = %v, want arc-1 and arc-2", keys)
	}

	// Replaying the same batch creates nothing, so nothing new is archived.
	if rec := postBatch(t, srv, "ak_ada."+adaSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", rec.Code)
	}
	again, _, _ := arch.List(context.Background(), time.Time{})
	if len(again) != 2 {
		t.Errorf("after replay archived %d, want still 2 (deduplicated items not re-archived)", len(again))
	}
}

// truncate keeps failure output readable when a body is large.
func truncate(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
