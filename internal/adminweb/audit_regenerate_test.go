package adminweb_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/adminweb"
	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

// newOpsHandler builds a handler with no seeded articles but an injectable audit
// Log and Regenerate closure, so the audit and regenerate routes can be driven in
// isolation. It returns the backing store too, so a test can seed submissions via
// the real RecordSubmission write path (Config.Log).
func newOpsHandler(t *testing.T, withLog bool, regen func(context.Context) (adminweb.RegenResult, error)) (*adminweb.Handler, *sqlite.Store) {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	cfg := adminweb.Config{
		Repo:          repo,
		Now:           fixedNow,
		TokenHash:     hashTok(fixtureToken),
		SessionKey:    testSessionKey,
		SecureCookies: false,
		PageSize:      pageSize,
		Regenerate:    regen,
	}
	if withLog {
		cfg.Log = repo
	}
	h, err := adminweb.New(cfg)
	if err != nil {
		t.Fatalf("adminweb.New: %v", err)
	}
	return h, repo
}

func seedSubmissions(t *testing.T, log store.SubmissionLog, subs []store.Submission) {
	t.Helper()
	ctx := context.Background()
	for _, s := range subs {
		if err := log.RecordSubmission(ctx, s); err != nil {
			t.Fatalf("RecordSubmission %q: %v", s.IdempotencyKey, err)
		}
	}
}

func getAudit(h http.Handler, q url.Values, cookie *http.Cookie, hx bool) *httptest.ResponseRecorder {
	target := "/admin/audit"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	return do(h, req)
}

// auditBase is the audit-seed reference instant. Distinct hours keep the
// newest-first order and the date-range window unambiguous.
var auditBase = time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

func threeSubmissions() []store.Submission {
	return []store.Submission{
		{IdempotencyKey: "idem-one", ContentHash: "hashAAAA", ArticleID: "1", Slug: "go-1-26-ships", Author: "ada", Scopes: []string{"section:tech", "topic:go"}, CreatedAt: auditBase},
		{IdempotencyKey: "idem-two", ContentHash: "hashBBBB", ArticleID: "2", Slug: "markets-dip", Author: "bo", Scopes: []string{"section:economics"}, CreatedAt: auditBase.Add(24 * time.Hour)},
		{IdempotencyKey: "idem-three", ContentHash: "hashCCCC", ArticleID: "3", Slug: "ai-policy-debate", Author: "ada", Scopes: []string{"section:politics", "topic:ai"}, CreatedAt: auditBase.Add(48 * time.Hour)},
	}
}

func TestAuditRequiresSession(t *testing.T) {
	h, _ := newOpsHandler(t, true, nil)

	rec := getAudit(h, nil, nil, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("no-cookie status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/login" {
		t.Errorf("Location = %q, want /admin/login", loc)
	}

	rec = getAudit(h, nil, nil, true) // HX-Request, no cookie
	if rec.Code != http.StatusOK {
		t.Fatalf("hx no-cookie status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/admin/login" {
		t.Errorf("HX-Redirect = %q, want /admin/login", got)
	}
}

func TestAuditListing(t *testing.T) {
	h, repo := newOpsHandler(t, true, nil)
	seedSubmissions(t, repo, threeSubmissions())
	c := loginCookie(t, h)

	rec := getAudit(h, nil, c, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Every column for at least one seeded submission is present: author, slug,
	// content hash, a scope, and the idempotency key.
	for _, want := range []string{"ada", "go-1-26-ships", "hashAAAA", "section:tech", "idem-one",
		"markets-dip", "hashBBBB", "idem-three"} {
		if !strings.Contains(body, want) {
			t.Errorf("audit page missing %q", want)
		}
	}
	// Slug links point at the article detail route.
	if !strings.Contains(body, `href="/admin/articles/go-1-26-ships"`) {
		t.Errorf("slug not linked to the detail route")
	}
	// Newest first: idem-three (newest) appears before idem-one (oldest).
	if strings.Index(body, "idem-three") > strings.Index(body, "idem-one") {
		t.Errorf("audit not newest-first: idem-three should precede idem-one")
	}
	// Three submissions, all on page 1 (pageSize=5).
	if n := rowCount(body); n != 3 {
		t.Errorf("audit rows = %d, want 3", n)
	}
}

func TestAuditAuthorFilter(t *testing.T) {
	h, repo := newOpsHandler(t, true, nil)
	seedSubmissions(t, repo, threeSubmissions())
	c := loginCookie(t, h)

	body := getAudit(h, url.Values{"author": {"ada"}}, c, false).Body.String()
	if !strings.Contains(body, "idem-one") || !strings.Contains(body, "idem-three") {
		t.Errorf("author=ada missing ada's submissions")
	}
	if strings.Contains(body, "idem-two") {
		t.Errorf("author=ada leaked bo's submission (idem-two)")
	}
	if n := rowCount(body); n != 2 {
		t.Errorf("author=ada rows = %d, want 2", n)
	}
}

func TestAuditDateRange(t *testing.T) {
	h, repo := newOpsHandler(t, true, nil)
	seedSubmissions(t, repo, threeSubmissions())
	c := loginCookie(t, h)

	// idem-two is recorded 2026-06-02; a single-day window must include only it
	// (inclusive 'to' day = +24h), excluding 06-01 (idem-one) and 06-03 (idem-three).
	q := url.Values{"from": {"2026-06-02"}, "to": {"2026-06-02"}}
	body := getAudit(h, q, c, false).Body.String()
	if !strings.Contains(body, "idem-two") {
		t.Errorf("date window dropped that day's submission")
	}
	if strings.Contains(body, "idem-one") || strings.Contains(body, "idem-three") {
		t.Errorf("date window leaked an adjacent day")
	}
	if n := rowCount(body); n != 1 {
		t.Errorf("single-day window rows = %d, want 1", n)
	}
}

func TestAuditPaginationAndFragment(t *testing.T) {
	h, repo := newOpsHandler(t, true, nil)
	// Seven submissions across distinct hours so newest-first paging is stable.
	var subs []store.Submission
	for i := 0; i < 7; i++ {
		subs = append(subs, store.Submission{
			IdempotencyKey: "idem-p" + string(rune('0'+i)),
			ContentHash:    "hashp" + string(rune('0'+i)),
			ArticleID:      "100",
			Slug:           "slug-p" + string(rune('0'+i)),
			Author:         "ada",
			Scopes:         []string{"section:tech"},
			CreatedAt:      auditBase.Add(time.Duration(i) * time.Hour),
		})
	}
	seedSubmissions(t, repo, subs)
	c := loginCookie(t, h)

	// Page 1: a full page of 5 plus a Next link (over-fetch detected page 2).
	p1 := getAudit(h, nil, c, false)
	if n := rowCount(p1.Body.String()); n != 5 {
		t.Errorf("page 1 rows = %d, want 5 (pageSize)", n)
	}
	doc := parseHTML(t, p1.Body.String())
	var nextHref string
	for _, a := range findAll(doc, "a") {
		if strings.TrimSpace(textOf(a)) == "Next" {
			nextHref = attr(a, "href")
		}
	}
	if nextHref == "" {
		t.Fatalf("no Next link on audit page 1")
	}
	if !strings.Contains(nextHref, "page=2") {
		t.Errorf("Next href = %q, want page=2", nextHref)
	}

	// Page 2: the remaining 2 rows and a Prev link, no Next.
	p2 := getAudit(h, url.Values{"page": {"2"}}, c, false)
	if n := rowCount(p2.Body.String()); n != 2 {
		t.Errorf("page 2 rows = %d, want 2", n)
	}
	if !strings.Contains(p2.Body.String(), "Prev") {
		t.Errorf("page 2 missing a Prev link")
	}

	// HX-Request returns only the results fragment, never a full HTML shell.
	frag := getAudit(h, nil, c, true)
	if frag.Code != http.StatusOK {
		t.Fatalf("hx audit status = %d, want 200", frag.Code)
	}
	fb := frag.Body.String()
	if strings.Contains(fb, "<html") {
		t.Errorf("HX audit fragment must not contain a full <html wrapper")
	}
	if !strings.Contains(fb, "admin-title-link") {
		t.Errorf("HX audit fragment missing result rows")
	}
}

func TestAuditNotConfigured(t *testing.T) {
	h, _ := newOpsHandler(t, false, nil) // Log == nil
	c := loginCookie(t, h)

	rec := getAudit(h, nil, c, false)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (friendly not-configured)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "not configured") {
		t.Errorf("expected a friendly not-configured message, got: %s", body)
	}
	if strings.Contains(body, "admin-title-link") {
		t.Errorf("not-configured audit page should render no result rows")
	}
}

// regenForm finds the regenerate POST form (action="/admin/regenerate").
func regenForm(t *testing.T, body string) (form *htmlForm) {
	t.Helper()
	doc := parseHTML(t, body)
	for _, f := range findAll(doc, "form") {
		if attr(f, "action") == "/admin/regenerate" && strings.EqualFold(attr(f, "method"), "post") {
			hf := &htmlForm{}
			for _, b := range findAll(f, "button") {
				if strings.EqualFold(attr(b, "type"), "submit") {
					hf.hasSubmit = true
				}
			}
			for _, in := range findAll(f, "input") {
				if attr(in, "name") == "csrf_token" {
					hf.csrf = attr(in, "value")
				}
			}
			return hf
		}
	}
	return nil
}

type htmlForm struct {
	hasSubmit bool
	csrf      string
}

func TestRegenerateFormConfigured(t *testing.T) {
	regen := func(context.Context) (adminweb.RegenResult, error) { return adminweb.RegenResult{}, nil }
	h, _ := newOpsHandler(t, true, regen)
	c := loginCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/admin/regenerate", nil)
	req.AddCookie(c)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	f := regenForm(t, rec.Body.String())
	if f == nil {
		t.Fatalf("no regenerate POST form rendered when Regenerate is configured")
	}
	if !f.hasSubmit {
		t.Errorf("regenerate form has no submit button")
	}
	if f.csrf == "" {
		t.Errorf("regenerate form missing csrf_token input")
	}
}

func TestRegenerateFormDisabled(t *testing.T) {
	h, _ := newOpsHandler(t, true, nil) // Regenerate == nil
	c := loginCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/admin/regenerate", nil)
	req.AddCookie(c)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if f := regenForm(t, body); f != nil {
		t.Errorf("a regenerate POST form was rendered while disabled")
	}
	if !strings.Contains(body, "not configured") {
		t.Errorf("disabled regenerate page missing the not-configured note")
	}
}

func TestRegenerateSubmitSuccess(t *testing.T) {
	var called bool
	regen := func(context.Context) (adminweb.RegenResult, error) {
		called = true
		return adminweb.RegenResult{
			Written:    3,
			Unchanged:  2,
			Deleted:    1,
			ScopeCount: 4,
			Purge:      []string{"/", "/latest/", "/topic/ai/"},
		}, nil
	}
	h, _ := newOpsHandler(t, true, regen)
	c := loginCookie(t, h)

	// Read a valid CSRF token from the rendered page (bound to the session).
	page := getArticles(h, nil, c, false).Body.String()
	csrf := metaCSRF(t, page)

	form := url.Values{"csrf_token": {csrf}}
	req := httptest.NewRequest(http.MethodPost, "/admin/regenerate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if !called {
		t.Errorf("Regenerate closure was not invoked")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Regeneration complete") {
		t.Errorf("success summary missing")
	}
	// Counts are shown.
	if !strings.Contains(body, ">3<") {
		t.Errorf("Written=3 not shown in summary")
	}
	// The FULL purge list is shown: the count (3) and every URL, none truncated.
	if !strings.Contains(body, "Purge URLs (3)") {
		t.Errorf("purge URL count not shown as 3 (list may be capped)")
	}
	for _, u := range []string{"/latest/", "/topic/ai/"} {
		if !strings.Contains(body, u) {
			t.Errorf("purge list missing %q", u)
		}
	}
	// Exactly three list items in the purge list.
	if n := strings.Count(body, `<li class="admin-mono">`); n != 3 {
		t.Errorf("purge list items = %d, want 3 (no truncation)", n)
	}
}

func TestRegenerateSubmitError(t *testing.T) {
	regen := func(context.Context) (adminweb.RegenResult, error) {
		return adminweb.RegenResult{}, errors.New("disk full while writing artifacts")
	}
	h, _ := newOpsHandler(t, true, regen)
	c := loginCookie(t, h)

	page := getArticles(h, nil, c, false).Body.String()
	csrf := metaCSRF(t, page)

	form := url.Values{"csrf_token": {csrf}}
	req := httptest.NewRequest(http.MethodPost, "/admin/regenerate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (error rendered, no panic)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Regeneration failed") {
		t.Errorf("error state not shown")
	}
	if !strings.Contains(body, "disk full while writing artifacts") {
		t.Errorf("error detail not shown in the page")
	}
}

func TestRegenerateCSRF(t *testing.T) {
	regen := func(context.Context) (adminweb.RegenResult, error) { return adminweb.RegenResult{}, nil }
	h, _ := newOpsHandler(t, true, regen)
	c := loginCookie(t, h)

	t.Run("POST without csrf -> 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/admin/regenerate", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(c)
		rec := do(h, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("no-csrf status = %d, want 403", rec.Code)
		}
	})

	t.Run("POST wrong csrf -> 403", func(t *testing.T) {
		form := url.Values{"csrf_token": {"wrong"}}
		req := httptest.NewRequest(http.MethodPost, "/admin/regenerate", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(c)
		rec := do(h, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("wrong-csrf status = %d, want 403", rec.Code)
		}
	})

	t.Run("POST without a session -> 303 to login", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/admin/regenerate", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := do(h, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("no-session POST status = %d, want 303", rec.Code)
		}
	})
}

func TestRegenerateNotConfiguredPOST(t *testing.T) {
	h, _ := newOpsHandler(t, true, nil) // Regenerate == nil but Log set so csrf page renders
	c := loginCookie(t, h)

	page := getArticles(h, nil, c, false).Body.String()
	csrf := metaCSRF(t, page)

	form := url.Values{"csrf_token": {csrf}}
	req := httptest.NewRequest(http.MethodPost, "/admin/regenerate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := do(h, req)
	// Friendly, not a 500/panic.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (friendly not-configured)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Errorf("expected a friendly not-configured message")
	}
}

// The masthead exposes the Audit and Regenerate links on an authed page.
func TestMastheadOpsLinks(t *testing.T) {
	h, _ := newOpsHandler(t, true, nil)
	c := loginCookie(t, h)
	body := getArticles(h, nil, c, false).Body.String()
	if !strings.Contains(body, `href="/admin/audit"`) {
		t.Errorf("masthead missing the Audit link")
	}
	if !strings.Contains(body, `href="/admin/regenerate"`) {
		t.Errorf("masthead missing the Regenerate link")
	}
}
