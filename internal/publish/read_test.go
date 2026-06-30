package publish_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
	"github.com/hec-ovi/censurado-web-backend/store/storetest"
)

// newReadServer wires the full server WITH the JSON read API over a real sqlite
// store, returning the handler and the concrete store so a test can seed authors,
// topics, and articles (and soft-delete them) directly. The same three keys as the
// write tests: ak_ada (write), ak_ro (read-only scope), ak_op (publish-any).
func newReadServer(t *testing.T) (http.Handler, *sqlite.Store) {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "read.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	auth := publish.NewStaticKeyAuth()
	auth.Add("ak_ada", publish.HashSecret(adaSecret), "ada", publish.ScopeWrite)
	auth.Add("ak_ro", publish.HashSecret(roSecret), "ro", "articles:read")
	auth.Add("ak_op", publish.HashSecret(opSecret), "editor", publish.ScopeWrite, publish.ScopePublishAny)

	now := func() time.Time { return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC) }
	h := publish.NewHandler(repo, repo, auth, now)
	rh := publish.NewReadHandler(repo, auth)
	limiter := publish.NewRateLimiter(1000, 1000, now)
	return publish.NewServerHandler(h, limiter, nil, rh, nil), repo
}

func getAuth(t *testing.T, h http.Handler, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// seedArticle publishes one article through the real write path (as the operator
// key, so it can author as anyone) and returns its slug.
func seedArticle(t *testing.T, srv http.Handler, opToken, key, title, author, section string, topics []string) string {
	t.Helper()
	payload := map[string]any{
		"title": title, "body": "# H\n\nbody for " + title,
		"author": author, "section": section, "topics": topics,
	}
	b, _ := json.Marshal(payload)
	rec := post(t, srv, opToken, key, string(b))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed %q: status = %d (%s)", title, rec.Code, rec.Body.String())
	}
	_, slug := decodeCreate(t, rec)
	return slug
}

type authorsResp struct {
	Authors []struct {
		Handle   string         `json:"handle"`
		Name     string         `json:"name"`
		Bio      string         `json:"bio"`
		Metadata map[string]any `json:"metadata"`
		Deleted  bool           `json:"deleted"`
	} `json:"authors"`
}

type topicsResp struct {
	Topics []struct {
		Slug    string `json:"slug"`
		Label   string `json:"label"`
		Deleted bool   `json:"deleted"`
	} `json:"topics"`
}

type articlesResp struct {
	Articles []struct {
		Slug    string   `json:"slug"`
		Title   string   `json:"title"`
		Section string   `json:"section"`
		Author  string   `json:"author"`
		Topics  []string `json:"topics"`
		Deleted bool     `json:"deleted"`
	} `json:"articles"`
	Total int `json:"total"`
}

func TestReadAPI_AuthRequired(t *testing.T) {
	srv, _ := newReadServer(t)

	t.Run("no token -> 401", func(t *testing.T) {
		if rec := getAuth(t, srv, "", "/authors"); rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
	t.Run("bad token -> 401", func(t *testing.T) {
		if rec := getAuth(t, srv, "ak_ada.wrong", "/topics"); rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
	t.Run("read-only key may read (no scope required)", func(t *testing.T) {
		if rec := getAuth(t, srv, "ak_ro."+roSecret, "/articles"); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
	})
}

func TestReadAuthors(t *testing.T) {
	srv, repo := newReadServer(t)
	ctx := context.Background()
	roToken := "ak_ro." + roSecret

	if _, err := repo.UpsertAuthor(ctx, storetest.SampleAuthor("author-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertAuthor(ctx, storetest.SampleAuthor("author-b")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertAuthor(ctx, storetest.SampleAuthor("gone")); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAuthor(ctx, "gone"); err != nil {
		t.Fatal(err)
	}

	t.Run("default excludes the tombstoned author", func(t *testing.T) {
		rec := getAuth(t, srv, roToken, "/authors")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
		}
		var got authorsResp
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		handles := authorHandles(got)
		if !handles["author-a"] || !handles["author-b"] {
			t.Errorf("missing seeded authors: %v", handles)
		}
		if handles["gone"] {
			t.Error("tombstoned author leaked into the default listing")
		}
		for _, a := range got.Authors {
			if a.Handle == "author-a" && a.Metadata["beat"] != "general" {
				t.Errorf("metadata did not round-trip: %+v", a.Metadata)
			}
		}
	})

	t.Run("include_deleted surfaces the tombstoned author", func(t *testing.T) {
		rec := getAuth(t, srv, roToken, "/authors?include_deleted=true")
		var got authorsResp
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var gone *struct {
			Handle   string         `json:"handle"`
			Name     string         `json:"name"`
			Bio      string         `json:"bio"`
			Metadata map[string]any `json:"metadata"`
			Deleted  bool           `json:"deleted"`
		}
		for i := range got.Authors {
			if got.Authors[i].Handle == "gone" {
				gone = &got.Authors[i]
			}
		}
		if gone == nil {
			t.Fatal("tombstoned author missing with include_deleted=true")
		}
		if !gone.Deleted {
			t.Error("tombstoned author should carry deleted=true")
		}
	})
}

func authorHandles(r authorsResp) map[string]bool {
	out := map[string]bool{}
	for _, a := range r.Authors {
		out[a.Handle] = true
	}
	return out
}

func TestReadTopics(t *testing.T) {
	srv, repo := newReadServer(t)
	ctx := context.Background()
	roToken := "ak_ro." + roSecret

	if _, err := repo.UpsertTopic(ctx, store.Topic{Slug: "go", Label: "Go", Description: "the language"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertTopic(ctx, store.Topic{Slug: "ai", Label: "AI"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertTopic(ctx, store.Topic{Slug: "old", Label: "Old"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteTopic(ctx, "old"); err != nil {
		t.Fatal(err)
	}

	rec := getAuth(t, srv, roToken, "/topics")
	var got topicsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, tp := range got.Topics {
		slugs[tp.Slug] = true
	}
	if !slugs["go"] || !slugs["ai"] {
		t.Errorf("missing seeded topics: %v", slugs)
	}
	if slugs["old"] {
		t.Error("tombstoned topic leaked into the default listing")
	}

	rec = getAuth(t, srv, roToken, "/topics?include_deleted=true")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tp := range got.Topics {
		if tp.Slug == "old" && tp.Deleted {
			found = true
		}
	}
	if !found {
		t.Error("tombstoned topic missing or not flagged deleted with include_deleted=true")
	}
}

func TestReadArticles_FilterPagingAndBodyOmitted(t *testing.T) {
	srv, _ := newReadServer(t)
	opToken := "ak_op." + opSecret

	seedArticle(t, srv, opToken, "a1", "Chip ships", "ada", "tech", []string{"go", "hardware"})
	seedArticle(t, srv, opToken, "a2", "Summit opens", "bo", "world", []string{"diplomacy"})
	seedArticle(t, srv, opToken, "a3", "Markets dip", "ada", "economics", []string{"go"})

	t.Run("all -> total and body omitted", func(t *testing.T) {
		rec := getAuth(t, srv, opToken, "/articles")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "body for ") {
			t.Error("list response leaked the article body")
		}
		var got articlesResp
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Total != 3 || len(got.Articles) != 3 {
			t.Errorf("total=%d len=%d, want 3/3", got.Total, len(got.Articles))
		}
	})

	t.Run("filter by author", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?author=ada"), &got)
		if got.Total != 2 {
			t.Errorf("total = %d, want 2", got.Total)
		}
		for _, a := range got.Articles {
			if a.Author != "ada" {
				t.Errorf("author = %q, want ada", a.Author)
			}
		}
	})

	t.Run("filter by section", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?section=world"), &got)
		if got.Total != 1 || (len(got.Articles) == 1 && got.Articles[0].Section != "world") {
			t.Errorf("section filter wrong: %+v", got)
		}
	})

	t.Run("filter by topic", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?topic=go"), &got)
		if got.Total != 2 {
			t.Errorf("topic=go total = %d, want 2", got.Total)
		}
	})

	t.Run("paging keeps total but pages items", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?limit=1"), &got)
		if got.Total != 3 {
			t.Errorf("total = %d, want 3 (paging must not shrink total)", got.Total)
		}
		if len(got.Articles) != 1 {
			t.Errorf("len = %d, want 1 (limit=1)", len(got.Articles))
		}
	})
}

func TestReadArticles_SoftDeleteVisibility(t *testing.T) {
	srv, repo := newReadServer(t)
	ctx := context.Background()
	opToken := "ak_op." + opSecret

	keep := seedArticle(t, srv, opToken, "k1", "Keep me", "ada", "tech", []string{"go"})
	gone := seedArticle(t, srv, opToken, "k2", "Delete me", "ada", "tech", []string{"go"})
	if err := repo.DeleteArticle(ctx, gone); err != nil {
		t.Fatal(err)
	}

	t.Run("default list excludes the deleted article", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles"), &got)
		if got.Total != 1 {
			t.Errorf("total = %d, want 1", got.Total)
		}
		for _, a := range got.Articles {
			if a.Slug == gone {
				t.Error("deleted article appeared in the default list")
			}
		}
	})

	t.Run("include_deleted lists the deleted article flagged", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?include_deleted=true"), &got)
		if got.Total != 2 {
			t.Errorf("total = %d, want 2", got.Total)
		}
		var saw bool
		for _, a := range got.Articles {
			if a.Slug == gone {
				saw = a.Deleted
			}
		}
		if !saw {
			t.Error("deleted article missing or not flagged deleted")
		}
	})

	t.Run("GET by slug returns body and the deleted one with deleted=true", func(t *testing.T) {
		rec := getAuth(t, srv, opToken, "/articles/"+keep)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "body for Keep me") {
			t.Fatalf("keep: status=%d body=%s", rec.Code, rec.Body.String())
		}
		var single struct {
			Slug, Body string
			Deleted    bool
		}
		rec = getAuth(t, srv, opToken, "/articles/"+gone)
		if rec.Code != http.StatusOK {
			t.Fatalf("deleted by slug status = %d", rec.Code)
		}
		decodeBody(t, rec, &single)
		if !single.Deleted || !strings.Contains(single.Body, "body for Delete me") {
			t.Errorf("deleted-by-slug = %+v, want deleted=true with body", single)
		}
	})

	t.Run("missing slug -> 404", func(t *testing.T) {
		if rec := getAuth(t, srv, opToken, "/articles/no-such-slug"); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

func TestReadArticles_InvalidQuery(t *testing.T) {
	srv, _ := newReadServer(t)
	opToken := "ak_op." + opSecret

	for _, path := range []string{"/articles?limit=abc", "/articles?offset=-1", "/articles?from=not-a-time"} {
		if rec := getAuth(t, srv, opToken, path); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
}
