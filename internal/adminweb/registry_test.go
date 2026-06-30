package adminweb_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/internal/adminweb"
	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
	"github.com/hec-ovi/censurado-web-backend/store/storetest"
)

// operatorCall records one invocation of the injected Operator closure. body is the
// JSON the handler built (the operator body type is unexported, so the test asserts
// against its marshaled form).
type operatorCall struct {
	method string
	path   string
	body   string
}

// operatorStub is a recording fake for the Operator closure: it captures each call
// and returns a programmed body/error, so a test asserts both what reached the
// operator boundary and the rendered outcome without a real publish service.
type operatorStub struct {
	calls    []operatorCall
	respBody []byte
	err      error
}

func (o *operatorStub) fn(_ context.Context, method, path string, body any) ([]byte, error) {
	raw, _ := json.Marshal(body)
	o.calls = append(o.calls, operatorCall{method: method, path: path, body: string(raw)})
	return o.respBody, o.err
}

// newRegistryHandler builds an admin handler with a configurable Operator closure
// (nil disables the mutation lane) over a small seed corpus of authors, topics, and
// one article. It returns the handler and the seeded article's slug.
func newRegistryHandler(t *testing.T, op func(context.Context, string, string, any) ([]byte, error)) (*adminweb.Handler, string) {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	for _, a := range []store.Author{
		storetest.SampleAuthor("author-a"),
		storetest.SampleAuthor("author-b"),
	} {
		if _, err := repo.UpsertAuthor(ctx, a); err != nil {
			t.Fatalf("seed author %q: %v", a.Handle, err)
		}
	}
	for _, tp := range []store.Topic{
		{Slug: "economia", Label: "Economia", Description: "Money and markets."},
	} {
		if _, err := repo.UpsertTopic(ctx, tp); err != nil {
			t.Fatalf("seed topic %q: %v", tp.Slug, err)
		}
	}
	art, err := domain.NewArticle(domain.PublishInput{
		Title: "Edit Me Please", Body: "original body text", Author: "author-a",
		Section: "politics", Topics: []string{"economia"},
	}, fixedNow())
	if err != nil {
		t.Fatalf("NewArticle: %v", err)
	}
	if _, err := repo.Upsert(ctx, art); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	h, err := adminweb.New(adminweb.Config{
		Repo:          repo,
		Now:           fixedNow,
		TokenHash:     hashTok(fixtureToken),
		SessionKey:    testSessionKey,
		SecureCookies: false,
		PageSize:      pageSize,
		Authors:       repo,
		Topics:        repo,
		Operator:      op,
	})
	if err != nil {
		t.Fatalf("adminweb.New: %v", err)
	}
	return h, art.Slug
}

// pageCSRF fetches an authed GET page and returns its CSRF token.
func pageCSRF(t *testing.T, h http.Handler, c *http.Cookie, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(c)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (%s)", path, rec.Code, rec.Body.String())
	}
	return metaCSRF(t, rec.Body.String())
}

// postForm submits an urlencoded form, attaching the cookie and (unless the sentinel
// "<omit>") a csrf_token field.
func postForm(h http.Handler, c *http.Cookie, csrf, path string, fields url.Values) *httptest.ResponseRecorder {
	if fields == nil {
		fields = url.Values{}
	}
	if csrf != "<omit>" {
		fields.Set("csrf_token", csrf)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c != nil {
		req.AddCookie(c)
	}
	return do(h, req)
}

// ----- authors -----

func TestAuthorsListRequiresSession(t *testing.T) {
	h, _ := newRegistryHandler(t, (&operatorStub{}).fn)
	rec := do(h, httptest.NewRequest(http.MethodGet, "/admin/authors", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("no-cookie GET status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/login" {
		t.Errorf("Location = %q, want /admin/login", loc)
	}
}

func TestAuthorsListRenders(t *testing.T) {
	h, _ := newRegistryHandler(t, (&operatorStub{}).fn)
	c := loginCookie(t, h)
	req := httptest.NewRequest(http.MethodGet, "/admin/authors", nil)
	req.AddCookie(c)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"author-a", "Sample Author", "author-b", `href="/admin/authors/new"`, `href="/admin/authors/author-a/edit"`} {
		if !strings.Contains(body, want) {
			t.Errorf("authors list missing %q", want)
		}
	}
	// The masthead exposes the Authors and Topics links on an authed page.
	if !strings.Contains(body, `href="/admin/authors"`) || !strings.Contains(body, `href="/admin/topics"`) {
		t.Errorf("masthead missing the registry nav links")
	}
}

func TestAuthorsListNotConfigured(t *testing.T) {
	h, _ := newRegistryHandler(t, nil) // Operator disabled
	c := loginCookie(t, h)
	req := httptest.NewRequest(http.MethodGet, "/admin/authors", nil)
	req.AddCookie(c)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Errorf("disabled authors page missing the not-configured note")
	}
	if strings.Contains(rec.Body.String(), `href="/admin/authors/new"`) {
		t.Errorf("a New author link was rendered while the action is disabled")
	}
}

func TestAuthorCreateCallsOperator(t *testing.T) {
	op := &operatorStub{respBody: []byte(`{"handle":"author-c"}`)}
	h, _ := newRegistryHandler(t, op.fn)
	c := loginCookie(t, h)
	csrf := pageCSRF(t, h, c, "/admin/authors/new")

	rec := postForm(h, c, csrf, "/admin/authors", url.Values{
		"handle": {"author-c"}, "name": {"Sample Author"}, "bio": {"Sample author bio."}, "avatar": {"/media/avatar.png"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/authors" {
		t.Errorf("redirect Location = %q, want /admin/authors", loc)
	}
	if len(op.calls) != 1 {
		t.Fatalf("operator calls = %d, want 1", len(op.calls))
	}
	call := op.calls[0]
	if call.method != http.MethodPost || call.path != "/authors" {
		t.Errorf("operator called %s %s, want POST /authors", call.method, call.path)
	}
	for _, want := range []string{`"handle":"author-c"`, `"name":"Sample Author"`, `"bio":"Sample author bio."`, `"avatar":"/media/avatar.png"`} {
		if !strings.Contains(call.body, want) {
			t.Errorf("operator body %q missing %q", call.body, want)
		}
	}
}

func TestAuthorCreateMissingHandle(t *testing.T) {
	op := &operatorStub{}
	h, _ := newRegistryHandler(t, op.fn)
	c := loginCookie(t, h)
	csrf := pageCSRF(t, h, c, "/admin/authors/new")

	rec := postForm(h, c, csrf, "/admin/authors", url.Values{"handle": {"   "}, "name": {"No Handle"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if len(op.calls) != 0 {
		t.Errorf("operator was called despite a missing handle")
	}
	if !strings.Contains(rec.Body.String(), "handle is required") {
		t.Errorf("missing-handle error not shown")
	}
}

func TestAuthorEditPrefillsAndUpdates(t *testing.T) {
	op := &operatorStub{respBody: []byte(`{"handle":"author-a"}`)}
	h, _ := newRegistryHandler(t, op.fn)
	c := loginCookie(t, h)

	// The edit form prefills the stored author and locks the handle.
	editPage := httptest.NewRequest(http.MethodGet, "/admin/authors/author-a/edit", nil)
	editPage.AddCookie(c)
	rec := do(h, editPage)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET edit = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="Sample Author"`) {
		t.Errorf("edit form did not prefill the name")
	}
	if !strings.Contains(body, "readonly") {
		t.Errorf("edit form did not lock the handle")
	}
	csrf := metaCSRF(t, body)

	rec = postForm(h, c, csrf, "/admin/authors/author-a", url.Values{"name": {"Sample Author Updated"}, "bio": {"Updated."}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want 303 (%s)", rec.Code, rec.Body.String())
	}
	if len(op.calls) != 1 || op.calls[0].path != "/authors" {
		t.Fatalf("operator calls = %+v, want one POST /authors", op.calls)
	}
	if !strings.Contains(op.calls[0].body, `"handle":"author-a"`) || !strings.Contains(op.calls[0].body, `"name":"Sample Author Updated"`) {
		t.Errorf("update body %q missing the path handle or new name", op.calls[0].body)
	}
}

func TestAuthorDeleteAndRestoreCallOperator(t *testing.T) {
	op := &operatorStub{}
	h, _ := newRegistryHandler(t, op.fn)
	c := loginCookie(t, h)
	csrf := pageCSRF(t, h, c, "/admin/authors")

	rec := postForm(h, c, csrf, "/admin/authors/author-b/delete", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303", rec.Code)
	}
	rec = postForm(h, c, csrf, "/admin/authors/author-b/restore", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("restore status = %d, want 303", rec.Code)
	}
	if len(op.calls) != 2 {
		t.Fatalf("operator calls = %d, want 2", len(op.calls))
	}
	if op.calls[0].method != http.MethodDelete || op.calls[0].path != "/authors/author-b" {
		t.Errorf("delete called %s %s, want DELETE /authors/author-b", op.calls[0].method, op.calls[0].path)
	}
	if op.calls[1].method != http.MethodPost || op.calls[1].path != "/authors/author-b/restore" {
		t.Errorf("restore called %s %s, want POST /authors/author-b/restore", op.calls[1].method, op.calls[1].path)
	}
}

func TestRegistryCSRFRequired(t *testing.T) {
	op := &operatorStub{}
	h, _ := newRegistryHandler(t, op.fn)
	c := loginCookie(t, h)

	for _, csrf := range []string{"<omit>", "wrong-token"} {
		rec := postForm(h, c, csrf, "/admin/authors", url.Values{"handle": {"x"}, "name": {"X"}})
		if rec.Code != http.StatusForbidden {
			t.Errorf("csrf=%q: status = %d, want 403", csrf, rec.Code)
		}
	}
	// No session -> 303 to login.
	if rec := postForm(h, nil, "whatever", "/admin/authors", url.Values{"handle": {"x"}}); rec.Code != http.StatusSeeOther {
		t.Errorf("no session: status = %d, want 303", rec.Code)
	}
	if len(op.calls) != 0 {
		t.Errorf("operator reached despite csrf/session rejection (calls=%d)", len(op.calls))
	}
}

// ----- topics -----

func TestTopicCreateAndDeleteCallOperator(t *testing.T) {
	op := &operatorStub{respBody: []byte(`{"slug":"deportes"}`)}
	h, _ := newRegistryHandler(t, op.fn)
	c := loginCookie(t, h)
	csrf := pageCSRF(t, h, c, "/admin/topics/new")

	rec := postForm(h, c, csrf, "/admin/topics", url.Values{
		"slug": {"deportes"}, "label": {"Deportes"}, "description": {"Sports coverage."},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303 (%s)", rec.Code, rec.Body.String())
	}
	if len(op.calls) != 1 || op.calls[0].path != "/topics" {
		t.Fatalf("operator calls = %+v, want one POST /topics", op.calls)
	}
	for _, want := range []string{`"slug":"deportes"`, `"label":"Deportes"`, `"description":"Sports coverage."`} {
		if !strings.Contains(op.calls[0].body, want) {
			t.Errorf("topic body %q missing %q", op.calls[0].body, want)
		}
	}

	rec = postForm(h, c, csrf, "/admin/topics/economia/delete", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303", rec.Code)
	}
	if op.calls[1].method != http.MethodDelete || op.calls[1].path != "/topics/economia" {
		t.Errorf("delete called %s %s, want DELETE /topics/economia", op.calls[1].method, op.calls[1].path)
	}
}

func TestTopicsListRenders(t *testing.T) {
	h, _ := newRegistryHandler(t, (&operatorStub{}).fn)
	c := loginCookie(t, h)
	req := httptest.NewRequest(http.MethodGet, "/admin/topics", nil)
	req.AddCookie(c)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "economia") || !strings.Contains(rec.Body.String(), "Money and markets.") {
		t.Errorf("topics list missing the seeded topic")
	}
}

// ----- article edit/delete -----

func TestArticleEditPrefills(t *testing.T) {
	h, slug := newRegistryHandler(t, (&operatorStub{}).fn)
	c := loginCookie(t, h)
	req := httptest.NewRequest(http.MethodGet, "/admin/articles/"+slug+"/edit", nil)
	req.AddCookie(c)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET edit = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="Edit Me Please"`) {
		t.Errorf("edit form did not prefill the title")
	}
	if !strings.Contains(body, "original body text") {
		t.Errorf("edit form did not prefill the body")
	}
	if !strings.Contains(body, `action="/admin/articles/`+slug+`"`) {
		t.Errorf("edit form posts to the wrong action")
	}
}

func TestArticleUpdateCallsOperator(t *testing.T) {
	op := &operatorStub{respBody: []byte(`{"slug":"edit-me-please"}`)}
	h, slug := newRegistryHandler(t, op.fn)
	c := loginCookie(t, h)
	csrf := pageCSRF(t, h, c, "/admin/articles/"+slug+"/edit")

	rec := postForm(h, c, csrf, "/admin/articles/"+slug, url.Values{
		"title": {"New Title"}, "body": {"new body text"}, "author": {"author-a"}, "section": {"politics"},
		"topics": {"economia, ai"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want 303 (%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/articles/"+slug {
		t.Errorf("redirect = %q, want the article detail", loc)
	}
	if len(op.calls) != 1 || op.calls[0].method != http.MethodPut || op.calls[0].path != "/articles/"+slug {
		t.Fatalf("operator calls = %+v, want one PUT /articles/%s", op.calls, slug)
	}
	if !strings.Contains(op.calls[0].body, `"title":"New Title"`) || !strings.Contains(op.calls[0].body, `"body":"new body text"`) {
		t.Errorf("update body %q missing the edited fields", op.calls[0].body)
	}
}

func TestArticleUpdateRequiredField(t *testing.T) {
	op := &operatorStub{}
	h, slug := newRegistryHandler(t, op.fn)
	c := loginCookie(t, h)
	csrf := pageCSRF(t, h, c, "/admin/articles/"+slug+"/edit")

	rec := postForm(h, c, csrf, "/admin/articles/"+slug, url.Values{
		"title": {"   "}, "body": {"b"}, "author": {"author-a"}, "section": {"politics"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if len(op.calls) != 0 {
		t.Errorf("operator was called despite an admin-side validation failure")
	}
	if !strings.Contains(rec.Body.String(), "required") {
		t.Errorf("missing-title error not shown")
	}
}

func TestArticleUpdateConflictIsFriendly(t *testing.T) {
	op := &operatorStub{err: &adminweb.OperatorError{Status: http.StatusConflict, Code: "edit_conflict"}}
	h, slug := newRegistryHandler(t, op.fn)
	c := loginCookie(t, h)
	csrf := pageCSRF(t, h, c, "/admin/articles/"+slug+"/edit")

	rec := postForm(h, c, csrf, "/admin/articles/"+slug, url.Values{
		"title": {"Clashing Title"}, "body": {"clashing body"}, "author": {"author-a"}, "section": {"politics"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (recoverable, shown inline)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "collides") {
		t.Errorf("409 edit_conflict not surfaced as a friendly message")
	}
	// The typed values are preserved so the operator can fix and resubmit.
	if !strings.Contains(body, "clashing body") {
		t.Errorf("body not preserved after a conflict")
	}
}

func TestArticleDeleteAndRestoreCallOperator(t *testing.T) {
	op := &operatorStub{}
	h, slug := newRegistryHandler(t, op.fn)
	c := loginCookie(t, h)
	csrf := pageCSRF(t, h, c, "/admin/articles/"+slug)

	rec := postForm(h, c, csrf, "/admin/articles/"+slug+"/delete", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/articles" {
		t.Errorf("delete redirect = %q, want /admin/articles", loc)
	}
	rec = postForm(h, c, csrf, "/admin/articles/"+slug+"/restore", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("restore status = %d, want 303", rec.Code)
	}
	if len(op.calls) != 2 {
		t.Fatalf("operator calls = %d, want 2", len(op.calls))
	}
	if op.calls[0].method != http.MethodDelete || op.calls[0].path != "/articles/"+slug {
		t.Errorf("delete called %s %s", op.calls[0].method, op.calls[0].path)
	}
	if op.calls[1].method != http.MethodPost || op.calls[1].path != "/articles/"+slug+"/restore" {
		t.Errorf("restore called %s %s", op.calls[1].method, op.calls[1].path)
	}
}

func TestDetailShowsEditDeleteWhenOperatorWired(t *testing.T) {
	h, slug := newRegistryHandler(t, (&operatorStub{}).fn)
	c := loginCookie(t, h)
	req := httptest.NewRequest(http.MethodGet, "/admin/articles/"+slug, nil)
	req.AddCookie(c)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/admin/articles/`+slug+`/edit"`) {
		t.Errorf("detail page missing the Edit link")
	}
	if !strings.Contains(body, `action="/admin/articles/`+slug+`/delete"`) {
		t.Errorf("detail page missing the Delete control")
	}
}
