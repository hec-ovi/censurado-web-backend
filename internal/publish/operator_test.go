package publish_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

// newOperatorServer wires the full server with the read API AND the operator
// mutation lane over a real sqlite store. ak_op holds admin:write (the operator
// console key); ak_ada holds only articles:write (an agent key) so it is rejected by
// the mutation lane.
func newOperatorServer(t *testing.T) http.Handler {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "op.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	auth := publish.NewStaticKeyAuth()
	auth.Add("ak_ada", publish.HashSecret(adaSecret), "ada", publish.ScopeWrite)
	auth.Add("ak_op", publish.HashSecret(opSecret), "editor",
		publish.ScopeWrite, publish.ScopePublishAny, publish.ScopeAdminWrite)

	now := func() time.Time { return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC) }
	h := publish.NewHandler(repo, repo, auth, now)
	rh := publish.NewReadHandler(repo, auth)
	oh := publish.NewOperatorHandler(repo, auth, now)
	limiter := publish.NewRateLimiter(1000, 1000, now)
	return publish.NewServerHandler(h, limiter, nil, rh, oh)
}

// doReq drives one request with an optional bearer token and JSON body against the
// real server (the operator endpoints take no Idempotency-Key, unlike the write path).
func doReq(t *testing.T, h http.Handler, method, token, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOperator_RequiresAdminWriteScope(t *testing.T) {
	srv := newOperatorServer(t)
	slug := seedArticle(t, srv, "ak_op."+opSecret, "seed1", "Target", "ada", "tech", []string{"go"})
	ada := "ak_ada." + adaSecret // articles:write only, no admin:write

	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/authors", `{"handle":"x","name":"X"}`},
		{http.MethodDelete, "/authors/x", ""},
		{http.MethodPost, "/authors/x/restore", ""},
		{http.MethodPost, "/topics", `{"slug":"t","label":"T"}`},
		{http.MethodDelete, "/topics/t", ""},
		{http.MethodPut, "/articles/" + slug, `{"title":"New","body":"# B\n\nbody","author":"ada","section":"tech"}`},
		{http.MethodDelete, "/articles/" + slug, ""},
		{http.MethodPost, "/articles/" + slug + "/restore", ""},
	}
	for _, c := range cases {
		rec := doReq(t, srv, c.method, ada, c.path, c.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with a write-only key: status = %d, want 403 (%s)", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if rec := doReq(t, srv, http.MethodPost, "", "/authors", `{"handle":"x"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}
}

func TestOperator_AuthorLifecycle(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret

	if rec := doReq(t, srv, http.MethodPost, op, "/authors", `{"handle":"author-a","name":"Sample Author","bio":"general"}`); rec.Code != http.StatusOK {
		t.Fatalf("create: %d (%s)", rec.Code, rec.Body.String())
	}
	var got authorsResp
	decodeBody(t, getAuth(t, srv, op, "/authors"), &got)
	if !authorHandles(got)["author-a"] {
		t.Fatal("created author missing from the listing")
	}

	// Upsert again updates in place (no duplicate; name changes).
	if rec := doReq(t, srv, http.MethodPost, op, "/authors", `{"handle":"author-a","name":"Sample Author (edited)"}`); rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}
	decodeBody(t, getAuth(t, srv, op, "/authors"), &got)
	count, name := 0, ""
	for _, a := range got.Authors {
		if a.Handle == "author-a" {
			count++
			name = a.Name
		}
	}
	if count != 1 || name != "Sample Author (edited)" {
		t.Errorf("after update: count=%d name=%q, want 1 / Sample Author (edited)", count, name)
	}

	// Delete hides it from the default listing but include_deleted shows it flagged.
	if rec := doReq(t, srv, http.MethodDelete, op, "/authors/author-a", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	decodeBody(t, getAuth(t, srv, op, "/authors"), &got)
	if authorHandles(got)["author-a"] {
		t.Error("deleted author still in the default listing")
	}
	decodeBody(t, getAuth(t, srv, op, "/authors?include_deleted=true"), &got)
	sawDeleted := false
	for _, a := range got.Authors {
		if a.Handle == "author-a" && a.Deleted {
			sawDeleted = true
		}
	}
	if !sawDeleted {
		t.Error("deleted author missing or not flagged with include_deleted=true")
	}

	// Restore brings it back.
	if rec := doReq(t, srv, http.MethodPost, op, "/authors/author-a/restore", ""); rec.Code != http.StatusOK {
		t.Fatalf("restore: %d", rec.Code)
	}
	decodeBody(t, getAuth(t, srv, op, "/authors"), &got)
	if !authorHandles(got)["author-a"] {
		t.Error("restored author missing from the default listing")
	}

	if rec := doReq(t, srv, http.MethodDelete, op, "/authors/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete missing handle: status = %d, want 404", rec.Code)
	}
	if rec := doReq(t, srv, http.MethodPost, op, "/authors/nope/restore", ""); rec.Code != http.StatusNotFound {
		t.Errorf("restore missing handle: status = %d, want 404", rec.Code)
	}
}

func TestOperator_TopicLifecycle(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret

	if rec := doReq(t, srv, http.MethodPost, op, "/topics", `{"slug":"economia","label":"Economia","description":"plata"}`); rec.Code != http.StatusOK {
		t.Fatalf("create topic: %d (%s)", rec.Code, rec.Body.String())
	}
	var got topicsResp
	decodeBody(t, getAuth(t, srv, op, "/topics"), &got)
	if !topicSlugs(got)["economia"] {
		t.Fatal("created topic missing from the listing")
	}

	if rec := doReq(t, srv, http.MethodDelete, op, "/topics/economia", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete topic: %d", rec.Code)
	}
	decodeBody(t, getAuth(t, srv, op, "/topics"), &got)
	if topicSlugs(got)["economia"] {
		t.Error("deleted topic still in the default listing")
	}

	if rec := doReq(t, srv, http.MethodPost, op, "/topics/economia/restore", ""); rec.Code != http.StatusOK {
		t.Fatalf("restore topic: %d", rec.Code)
	}
	decodeBody(t, getAuth(t, srv, op, "/topics"), &got)
	if !topicSlugs(got)["economia"] {
		t.Error("restored topic missing from the default listing")
	}

	if rec := doReq(t, srv, http.MethodDelete, op, "/topics/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete missing topic: status = %d, want 404", rec.Code)
	}
}

func topicSlugs(r topicsResp) map[string]bool {
	out := map[string]bool{}
	for _, tp := range r.Topics {
		out[tp.Slug] = true
	}
	return out
}

func TestOperator_ArticleEditDeleteRestore(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret
	slug := seedArticle(t, srv, op, "art1", "Original title", "ada", "tech", []string{"go"})

	var orig struct {
		Slug        string `json:"slug"`
		ContentHash string `json:"content_hash"`
		ID          string `json:"id"`
	}
	decodeBody(t, getAuth(t, srv, op, "/articles/"+slug), &orig)

	// Edit in place: slug + id stable, content hash changes, body persists.
	rec := doReq(t, srv, http.MethodPut, op, "/articles/"+slug,
		`{"title":"Edited title","body":"# H\n\nthe edited body here","author":"ada","section":"tech","topics":["go","news"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d (%s)", rec.Code, rec.Body.String())
	}
	var edited struct {
		Slug        string `json:"slug"`
		ContentHash string `json:"content_hash"`
		ID          string `json:"id"`
		Title       string `json:"title"`
		Body        string `json:"body"`
	}
	decodeBody(t, rec, &edited)
	if edited.Slug != slug {
		t.Errorf("edit changed the slug: %q != %q", edited.Slug, slug)
	}
	if edited.ID != orig.ID {
		t.Errorf("edit changed the id: %q != %q", edited.ID, orig.ID)
	}
	if edited.ContentHash == orig.ContentHash {
		t.Error("content hash did not change after an edit")
	}

	var after struct{ Body, Title string }
	decodeBody(t, getAuth(t, srv, op, "/articles/"+slug), &after)
	if !strings.Contains(after.Body, "the edited body here") || after.Title != "Edited title" {
		t.Errorf("edit not persisted: title=%q body=%q", after.Title, after.Body)
	}

	// Delete excludes it from the default list; restore re-includes it.
	if rec := doReq(t, srv, http.MethodDelete, op, "/articles/"+slug, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	var list articlesResp
	decodeBody(t, getAuth(t, srv, op, "/articles"), &list)
	for _, a := range list.Articles {
		if a.Slug == slug {
			t.Error("deleted article appeared in the default list")
		}
	}
	if rec := doReq(t, srv, http.MethodPost, op, "/articles/"+slug+"/restore", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("restore: %d", rec.Code)
	}
	decodeBody(t, getAuth(t, srv, op, "/articles"), &list)
	found := false
	for _, a := range list.Articles {
		if a.Slug == slug {
			found = true
		}
	}
	if !found {
		t.Error("restored article missing from the default list")
	}

	if rec := doReq(t, srv, http.MethodPut, op, "/articles/nope", `{"title":"x","body":"# y\n\nz","author":"ada","section":"tech"}`); rec.Code != http.StatusNotFound {
		t.Errorf("edit missing slug: status = %d, want 404", rec.Code)
	}
	if rec := doReq(t, srv, http.MethodDelete, op, "/articles/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete missing slug: status = %d, want 404", rec.Code)
	}
}

func TestOperator_EditConflictReturns409(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret
	a1 := seedArticle(t, srv, op, "c1", "First article", "ada", "tech", nil)
	seedArticle(t, srv, op, "c2", "Second article", "bo", "world", nil)

	// Edit a1 to be byte-identical to a2 (same title/body/author/section), so its new
	// content hash collides with a2's UNIQUE content_hash. seedArticle's body is
	// "# H\n\nbody for <title>", so reproduce a2's exactly.
	body := `{"title":"Second article","body":"# H\n\nbody for Second article","author":"bo","section":"world"}`
	rec := doReq(t, srv, http.MethodPut, op, "/articles/"+a1, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("colliding edit: status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}
