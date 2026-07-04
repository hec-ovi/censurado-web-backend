package publish_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/media"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

// Seam B freeze: the whole HTTP surface Layer 3 (brain / CLI) and the admin panel
// consume. This test stands up the full server with every lane mounted (liveness,
// publish, read, media, operator) and walks each frozen route, asserting it is
// registered, dispatched to the right handler by method, and answers with the frozen
// status. Removing, renaming, or changing the method of any route breaks it, so
// contracts/CONTRACT.md and this test move together. It does NOT re-assert deep
// behavior (the per-handler tests own that); it pins the ROUTE TABLE and the read
// wrapper shapes.

// contractServer wires the complete surface over a real sqlite store and a real
// media store. It deliberately does not attach a regenerate trigger (that machinery
// is being removed) so the freeze does not depend on it.
func contractServer(t *testing.T) http.Handler {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "contract.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	mstore, err := media.NewStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatalf("media store: %v", err)
	}

	auth := publish.NewStaticKeyAuth()
	auth.Add("ak_ada", publish.HashSecret(adaSecret), "ada", publish.ScopeWrite)
	auth.Add("ak_op", publish.HashSecret(opSecret), "editor",
		publish.ScopeWrite, publish.ScopePublishAny, publish.ScopeAdminWrite)

	now := func() time.Time { return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC) }
	h := publish.NewHandler(repo, repo, auth, now)
	rh := publish.NewReadHandler(repo, auth)
	oh := publish.NewOperatorHandler(repo, auth, now)
	mh := publish.NewMediaHandler(mstore, auth)
	limiter := publish.NewRateLimiter(10000, 10000, now)
	return publish.NewServerHandler(h, limiter, mh, rh, oh)
}

func TestContract_FrozenHTTPSurface(t *testing.T) {
	srv := contractServer(t)
	op := "ak_op." + opSecret

	// Lane 1: liveness, unauthenticated.
	if rec := doReq(t, srv, http.MethodGet, "", "/healthz", ""); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("GET /healthz: status=%d body=%q, want 200 ok", rec.Code, rec.Body.String())
	}

	// Lane 2: publish / write.
	rec := post(t, srv, op, "idem-1", `{"title":"Contract One","body":"# H\n\nthe body","author":"ada","section":"tech","topics":["go"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /articles: status=%d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	_, slug := decodeCreate(t, rec)

	if rec := doReq(t, srv, http.MethodPost, op, "/articles:batch",
		`{"articles":[{"title":"Batch A","body":"# H\n\nbatch body","author":"ada","section":"tech","idempotency_key":"b-1"}]}`); rec.Code != http.StatusCreated {
		t.Fatalf("POST /articles:batch: status=%d, want 201 (%s)", rec.Code, rec.Body.String())
	}

	up := postMedia(t, srv, op, pngBytes, "image/png")
	if up.Code != http.StatusCreated {
		t.Fatalf("POST /media: status=%d, want 201 (%s)", up.Code, up.Body.String())
	}
	var stored struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(up.Body.Bytes(), &stored); err != nil || stored.Name == "" {
		t.Fatalf("POST /media response = %q (err %v)", up.Body.String(), err)
	}

	// Method dispatch freeze on the method-less /articles pattern: POST reaches the
	// append-only publish handler (which demands an idempotency key), GET reaches the
	// read handler, not the other way round.
	if rec := doReq(t, srv, http.MethodPost, op, "/articles", `{"title":"x","body":"# y\n\nz","author":"ada","section":"tech"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("POST /articles without Idempotency-Key: status=%d, want 400 (proves POST routes to publish)", rec.Code)
	}

	// Lane 3: read. Assert routing + the frozen top-level wrapper keys.
	reads := []struct {
		path string
		keys []string
	}{
		{"/authors", []string{`"authors"`}},
		{"/topics", []string{`"topics"`}},
		{"/portadas", []string{`"portadas"`}},
		{"/sources", []string{`"sources"`}},
		{"/articles", []string{`"articles"`, `"total"`}},
	}
	for _, r := range reads {
		rec := getAuth(t, srv, op, r.path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status=%d, want 200 (%s)", r.path, rec.Code, rec.Body.String())
			continue
		}
		body := rec.Body.String()
		for _, k := range r.keys {
			if !strings.Contains(body, k) {
				t.Errorf("GET %s: response missing frozen wrapper key %s (%s)", r.path, k, body)
			}
		}
	}
	// Single article carries the full body (distinguishes it from a list item).
	if rec := getAuth(t, srv, op, "/articles/"+slug); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"body"`) {
		t.Errorf("GET /articles/{slug}: status=%d, body-key present=%v", rec.Code, strings.Contains(rec.Body.String(), `"body"`))
	}
	// Public media read of the content-addressed name.
	if rec := doReq(t, srv, http.MethodGet, "", "/media/"+stored.Name, ""); rec.Code != http.StatusOK {
		t.Errorf("GET /media/{name}: status=%d, want 200", rec.Code)
	}

	// Lane 4: operator / admin write. Walk each registry's lifecycle in dependency
	// order and pin the frozen status of every verb.
	adminWalk := []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPost, "/authors", `{"handle":"contract-a","name":"C"}`, http.StatusOK},
		{http.MethodDelete, "/authors/contract-a", "", http.StatusNoContent},
		{http.MethodPost, "/authors/contract-a/restore", "", http.StatusOK},

		{http.MethodPost, "/topics", `{"slug":"contract-t","label":"T"}`, http.StatusOK},
		{http.MethodDelete, "/topics/contract-t", "", http.StatusNoContent},
		{http.MethodPost, "/topics/contract-t/restore", "", http.StatusOK},

		{http.MethodPost, "/portadas", `{"date":"2026-06-22","entries":[{"slug":"lead","role":"important"}],"recomendado":["r1"]}`, http.StatusOK},
		{http.MethodDelete, "/portadas/2026-06-22", "", http.StatusNoContent},
		{http.MethodPost, "/portadas/2026-06-22/restore", "", http.StatusOK},

		// sources + the author-source join (contract-a exists again after its restore
		// above, so the PUT/GET below bind to a real author).
		{http.MethodPost, "/sources", `{"domain":"contract.example"}`, http.StatusOK},
		{http.MethodPut, "/authors/contract-a/sources", `{"sources":["contract-example"]}`, http.StatusOK},
		{http.MethodGet, "/authors/contract-a/sources", "", http.StatusOK},
		{http.MethodDelete, "/sources/contract-example", "", http.StatusNoContent},
		{http.MethodPost, "/sources/contract-example/restore", "", http.StatusOK},

		{http.MethodPut, "/articles/" + slug, `{"title":"Edited","body":"# H\n\nedited body here","author":"ada","section":"tech"}`, http.StatusOK},
		{http.MethodDelete, "/articles/" + slug, "", http.StatusNoContent},
		{http.MethodPost, "/articles/" + slug + "/restore", "", http.StatusNoContent},
	}
	for _, c := range adminWalk {
		rec := doReq(t, srv, c.method, op, c.path, c.body)
		if rec.Code != c.want {
			t.Errorf("%s %s: status=%d, frozen=%d (%s)", c.method, c.path, rec.Code, c.want, rec.Body.String())
		}
	}

	// An unregistered path is a 404 from the mux, confirming the surface is exactly
	// the frozen set and nothing wildcards it.
	if rec := doReq(t, srv, http.MethodGet, op, "/does-not-exist", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET /does-not-exist: status=%d, want 404", rec.Code)
	}
}
