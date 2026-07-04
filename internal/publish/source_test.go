package publish_test

import (
	"net/http"
	"testing"
)

// These tests drive the source registry and the author-source join through the real
// HTTP surface wired by newOperatorServer (read API + operator mutation lane over a
// sqlite store), the same harness the author/topic/portada operator tests use.

type sourcesResp struct {
	Sources []struct {
		Slug     string   `json:"slug"`
		Domain   string   `json:"domain"`
		Lean     string   `json:"lean"`
		FeedType string   `json:"feed_type"`
		FeedURLs []string `json:"feed_urls"`
		Enabled  bool     `json:"enabled"`
		Deleted  bool     `json:"deleted"`
	} `json:"sources"`
}

type authorSourcesResp struct {
	Handle  string   `json:"handle"`
	Sources []string `json:"sources"`
}

type authorsFullResp struct {
	Authors []struct {
		Handle  string   `json:"handle"`
		Gender  string   `json:"gender"`
		About   string   `json:"about"`
		Style   string   `json:"style"`
		Topics  []string `json:"topics"`
		Sources []string `json:"sources"`
	} `json:"authors"`
}

func sourceBySlug(r sourcesResp, slug string) (int, bool) {
	for i, s := range r.Sources {
		if s.Slug == slug {
			return i, true
		}
	}
	return 0, false
}

func TestSource_Lifecycle(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret

	// Create by URL alone: the slug derives from the normalized domain, and the
	// lean/feed_type/enabled/feed_urls round-trip.
	body := `{"domain":"https://www.Diario.example/seccion","description":"Diario local","feed_urls":["https://diario.example/rss"],"feed_type":"native_rss","lean":"left"}`
	if rec := doReq(t, srv, http.MethodPost, op, "/sources", body); rec.Code != http.StatusOK {
		t.Fatalf("create source: %d (%s)", rec.Code, rec.Body.String())
	}
	var got sourcesResp
	decodeBody(t, getAuth(t, srv, op, "/sources"), &got)
	i, ok := sourceBySlug(got, "diario-example")
	if !ok {
		t.Fatalf("source not found by derived slug; got %+v", got.Sources)
	}
	if got.Sources[i].Domain != "diario.example" {
		t.Errorf("domain = %q, want normalized diario.example", got.Sources[i].Domain)
	}
	if got.Sources[i].Lean != "left" || got.Sources[i].FeedType != "native_rss" || !got.Sources[i].Enabled {
		t.Errorf("fields did not round-trip: %+v", got.Sources[i])
	}
	if len(got.Sources[i].FeedURLs) != 1 || got.Sources[i].FeedURLs[0] != "https://diario.example/rss" {
		t.Errorf("feed_urls = %v, want [https://diario.example/rss]", got.Sources[i].FeedURLs)
	}

	// Upsert in place: same slug, disable it, no duplicate row.
	if rec := doReq(t, srv, http.MethodPost, op, "/sources", `{"domain":"diario.example","lean":"neutral","enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("update source: %d (%s)", rec.Code, rec.Body.String())
	}
	decodeBody(t, getAuth(t, srv, op, "/sources"), &got)
	n := 0
	for _, s := range got.Sources {
		if s.Slug == "diario-example" {
			n++
			if s.Enabled {
				t.Error("enabled=true after disabling")
			}
		}
	}
	if n != 1 {
		t.Errorf("rows for diario-example = %d, want 1 (update, not insert)", n)
	}

	// Delete tombstones it: gone from the default listing, present under include_deleted.
	if rec := doReq(t, srv, http.MethodDelete, op, "/sources/diario-example", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete source: %d", rec.Code)
	}
	decodeBody(t, getAuth(t, srv, op, "/sources"), &got)
	if _, ok := sourceBySlug(got, "diario-example"); ok {
		t.Error("deleted source still in the default listing")
	}
	decodeBody(t, getAuth(t, srv, op, "/sources?include_deleted=true"), &got)
	if i, ok := sourceBySlug(got, "diario-example"); !ok || !got.Sources[i].Deleted {
		t.Error("deleted source missing/unflagged under include_deleted")
	}

	// Restore brings it back active.
	if rec := doReq(t, srv, http.MethodPost, op, "/sources/diario-example/restore", ""); rec.Code != http.StatusOK {
		t.Fatalf("restore source: %d (%s)", rec.Code, rec.Body.String())
	}
	decodeBody(t, getAuth(t, srv, op, "/sources"), &got)
	if _, ok := sourceBySlug(got, "diario-example"); !ok {
		t.Error("restored source missing from the default listing")
	}

	// Deleting an absent slug is a 404.
	if rec := doReq(t, srv, http.MethodDelete, op, "/sources/no-such", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete missing source: %d, want 404", rec.Code)
	}
}

func TestSource_ValidatesInput(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret

	cases := []struct{ name, body string }{
		{"missing domain", `{"lean":"left"}`},
		{"invalid lean", `{"domain":"x.example","lean":"centrist"}`},
		{"invalid feed_type", `{"domain":"x.example","feed_type":"telepathy"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := doReq(t, srv, http.MethodPost, op, "/sources", c.body); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSource_RequiresAdminWriteScope(t *testing.T) {
	srv := newOperatorServer(t)
	ada := "ak_ada." + adaSecret // articles:write only, no admin:write

	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/sources", `{"domain":"x.example"}`},
		{http.MethodDelete, "/sources/x-example", ""},
		{http.MethodPost, "/sources/x-example/restore", ""},
		{http.MethodPut, "/authors/x/sources", `{"sources":["x-example"]}`},
	}
	for _, c := range cases {
		if rec := doReq(t, srv, c.method, ada, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with a write-only key: %d, want 403 (%s)", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

func TestAuthor_ProfileFieldsAndSources(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret

	// Register two sources to attach.
	for _, d := range []string{"alfa.example", "beta.example"} {
		if rec := doReq(t, srv, http.MethodPost, op, "/sources", `{"domain":"`+d+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("seed source %s: %d", d, rec.Code)
		}
	}

	// Create an author carrying every promoted profile field AND its sources in one
	// call (the sources array on POST /authors sets the join).
	body := `{"handle":"lara","name":"Lara","gender":"femenino","about":"Cronista politica.","style":"Voz sobria.","topics":["politica","congreso"],"sources":["beta-example","alfa-example"]}`
	if rec := doReq(t, srv, http.MethodPost, op, "/authors", body); rec.Code != http.StatusOK {
		t.Fatalf("create author: %d (%s)", rec.Code, rec.Body.String())
	}

	// GET /authors hydrates the profile fields AND the slug-sorted sources.
	var full authorsFullResp
	decodeBody(t, getAuth(t, srv, op, "/authors"), &full)
	var lara *struct {
		Handle  string   `json:"handle"`
		Gender  string   `json:"gender"`
		About   string   `json:"about"`
		Style   string   `json:"style"`
		Topics  []string `json:"topics"`
		Sources []string `json:"sources"`
	}
	for i := range full.Authors {
		if full.Authors[i].Handle == "lara" {
			lara = &full.Authors[i]
		}
	}
	if lara == nil {
		t.Fatal("author lara missing from the listing")
	}
	if lara.Gender != "femenino" || lara.About != "Cronista politica." || lara.Style != "Voz sobria." {
		t.Errorf("profile fields did not round-trip: %+v", lara)
	}
	if len(lara.Topics) != 2 || lara.Topics[0] != "politica" {
		t.Errorf("topics = %v, want [politica congreso]", lara.Topics)
	}
	if len(lara.Sources) != 2 || lara.Sources[0] != "alfa-example" || lara.Sources[1] != "beta-example" {
		t.Errorf("sources = %v, want slug-sorted [alfa-example beta-example]", lara.Sources)
	}

	// The focused GET returns the same slug-sorted set.
	var as authorSourcesResp
	decodeBody(t, getAuth(t, srv, op, "/authors/lara/sources"), &as)
	if as.Handle != "lara" || len(as.Sources) != 2 || as.Sources[0] != "alfa-example" {
		t.Errorf("GET author sources = %+v, want alfa,beta", as)
	}

	// PUT replaces the set wholesale.
	if rec := doReq(t, srv, http.MethodPut, op, "/authors/lara/sources", `{"sources":["beta-example"]}`); rec.Code != http.StatusOK {
		t.Fatalf("set sources: %d (%s)", rec.Code, rec.Body.String())
	}
	decodeBody(t, getAuth(t, srv, op, "/authors/lara/sources"), &as)
	if len(as.Sources) != 1 || as.Sources[0] != "beta-example" {
		t.Errorf("after PUT, sources = %v, want [beta-example]", as.Sources)
	}

	// PUT / GET on a missing author is a 404.
	if rec := doReq(t, srv, http.MethodPut, op, "/authors/ghost/sources", `{"sources":[]}`); rec.Code != http.StatusNotFound {
		t.Errorf("PUT missing-author sources: %d, want 404", rec.Code)
	}
	if rec := getAuth(t, srv, op, "/authors/ghost/sources"); rec.Code != http.StatusNotFound {
		t.Errorf("GET missing-author sources: %d, want 404", rec.Code)
	}
}

func TestSource_DeleteDetachesAuthors(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret

	for _, d := range []string{"uno.example", "dos.example"} {
		if rec := doReq(t, srv, http.MethodPost, op, "/sources", `{"domain":"`+d+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("seed source %s: %d", d, rec.Code)
		}
	}
	if rec := doReq(t, srv, http.MethodPost, op, "/authors", `{"handle":"nora","name":"Nora","sources":["uno-example","dos-example"]}`); rec.Code != http.StatusOK {
		t.Fatalf("create author: %d (%s)", rec.Code, rec.Body.String())
	}

	// Deleting a source detaches it from the author (but keeps the other link).
	if rec := doReq(t, srv, http.MethodDelete, op, "/sources/uno-example", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete source: %d", rec.Code)
	}
	var as authorSourcesResp
	decodeBody(t, getAuth(t, srv, op, "/authors/nora/sources"), &as)
	if len(as.Sources) != 1 || as.Sources[0] != "dos-example" {
		t.Errorf("after source delete, author sources = %v, want [dos-example] (uno detached)", as.Sources)
	}
}
