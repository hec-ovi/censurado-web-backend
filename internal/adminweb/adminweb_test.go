package adminweb_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/hec-ovi/censurado-web-backend/internal/adminweb"
	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

const (
	fixtureToken = "operator-token-fixture"
	pageSize     = 5
)

// 32-byte HMAC key shared by the handler and the tests that forge/expire cookies.
var testSessionKey = []byte("0123456789abcdef0123456789abcdef")

func fixedNow() time.Time { return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC) }

func hashTok(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

type seedSpec struct {
	title   string
	author  string
	section string
	topics  []string
	body    string
	day     time.Time
}

func newTestHandler(t *testing.T) *adminweb.Handler {
	return newTestHandlerWith(t, fixedNow, 0)
}

// newTestHandlerWith builds a handler over the same seed corpus but with an
// injectable clock and session TTL, so the sliding-renewal window can actually
// be crossed in a test (the default 12h TTL with a fixed clock never renews).
func newTestHandlerWith(t *testing.T, now func() time.Time, ttl time.Duration) *adminweb.Handler {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	d := func(y int, m time.Month, day int) time.Time {
		return time.Date(y, m, day, 9, 0, 0, 0, time.UTC)
	}
	// 12 articles: 3 sections x 4, 3 authors x 4, overlapping topics, one article
	// (Senate Vote Recap) carrying two of the queried topics (go + election) to
	// prove union dedup, and one accented title+body (Cafe Society).
	seeds := []seedSpec{
		{"Go Generics Land", "ada", "tech", []string{"go", "release"},
			"## Generics\n\nGo **finally** ships generics.\n\n<script>alert('xss')</script>\n\n<img src=x onerror=\"alert(1)\">", d(2026, 5, 1)},
		{"Compiler Speedups", "bo", "tech", []string{"go"}, "Builds are faster now.", d(2026, 5, 5)},
		{"Election Night Results", "cy", "politics", []string{"election"}, "Ballots counted overnight.", d(2026, 5, 10)},
		{"Senate Vote Recap", "ada", "politics", []string{"election", "go"}, "A recap of the vote.", d(2026, 5, 15)},
		{"Markets Tumble", "bo", "economics", []string{"markets"}, "Indices fell sharply.", d(2026, 5, 20)},
		{"Inflation Watch", "cy", "economics", []string{"markets", "economy"}, "Prices keep climbing.", d(2026, 5, 25)},
		{"Quantum Chips Arrive", "ada", "tech", []string{"ai"}, "New silicon ships.", d(2026, 6, 1)},
		{"Trade Talks Stall", "bo", "politics", []string{"election", "trade"}, "Negotiations paused.", d(2026, 6, 5)},
		{"Cafe Society Reborn", "cy", "economics", []string{"economy"}, "La cultura del café vuelve a las calles de la ciudad.", d(2026, 6, 10)},
		{"Startup Funding Dips", "ada", "economics", []string{"markets"}, "Seed rounds shrink.", d(2026, 6, 12)},
		{"AI Policy Debate", "bo", "politics", []string{"ai", "election"}, "Lawmakers spar over models.", d(2026, 6, 15)},
		{"Open Source Wins", "cy", "tech", []string{"go", "ai"}, "Contributors celebrate.", d(2026, 6, 18)},
	}
	ctx := context.Background()
	for _, s := range seeds {
		pub := s.day
		art, err := domain.NewArticle(domain.PublishInput{
			Title: s.title, Body: s.body, Author: s.author, Section: s.section,
			Topics: s.topics, PublishedAt: &pub,
		}, fixedNow())
		if err != nil {
			t.Fatalf("NewArticle %q: %v", s.title, err)
		}
		if _, err := repo.Upsert(ctx, art); err != nil {
			t.Fatalf("Upsert %q: %v", s.title, err)
		}
	}

	h, err := adminweb.New(adminweb.Config{
		Repo:          repo,
		Now:           now,
		TokenHash:     hashTok(fixtureToken),
		SessionKey:    testSessionKey,
		SessionTTL:    ttl,
		SecureCookies: false,
		PageSize:      pageSize,
	})
	if err != nil {
		t.Fatalf("adminweb.New: %v", err)
	}
	return h
}

// ---- request helpers -----------------------------------------------------

func do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getArticles(h http.Handler, q url.Values, cookie *http.Cookie, hx bool) *httptest.ResponseRecorder {
	target := "/admin/articles"
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

func loginCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	form := url.Values{"token": {fixtureToken}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := do(h, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login: status = %d, want 303 (%s)", rec.Code, rec.Body.String())
	}
	c := findCookie(rec, "cnz_admin")
	if c == nil {
		t.Fatalf("login: no cnz_admin cookie set")
	}
	return c
}

func findCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// signSession mirrors the handler's cookie format so tests can mint expired or
// forged cookies without reaching into the package internals.
func signSession(key []byte, expUnix int64, goodMAC bool) string {
	payload := []byte("v1|" + strconv.FormatInt(expUnix, 10))
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	sum := mac.Sum(nil)
	if !goodMAC {
		sum[0] ^= 0xff // corrupt the signature
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(sum)
}

// ---- HTML helpers --------------------------------------------------------

func parseHTML(t *testing.T, s string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	return doc
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func findAll(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == tag {
			out = append(out, node)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// metaCSRF extracts the csrf-token meta content from a full page.
func metaCSRF(t *testing.T, body string) string {
	t.Helper()
	doc := parseHTML(t, body)
	for _, m := range findAll(doc, "meta") {
		if attr(m, "name") == "csrf-token" {
			return attr(m, "content")
		}
	}
	t.Fatalf("no csrf-token meta found")
	return ""
}

func rowCount(body string) int { return strings.Count(body, "admin-title-link") }

// ---- tests ---------------------------------------------------------------

func TestLoginForm(t *testing.T) {
	h := newTestHandler(t)
	rec := do(h, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	doc := parseHTML(t, rec.Body.String())
	forms := findAll(doc, "form")
	var found bool
	for _, f := range forms {
		if attr(f, "action") == "/admin/login" && strings.EqualFold(attr(f, "method"), "post") {
			for _, in := range findAll(f, "input") {
				if attr(in, "name") == "token" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("login form with token input not found: %s", rec.Body.String())
	}
}

func TestLoginWrongToken(t *testing.T) {
	h := newTestHandler(t)
	form := url.Values{"token": {"not-the-token"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-render)", rec.Code)
	}
	if c := findCookie(rec, "cnz_admin"); c != nil {
		t.Errorf("a session cookie was set on a failed login: %+v", c)
	}
	if !strings.Contains(rec.Body.String(), "Invalid token") {
		t.Errorf("expected a generic error message, got: %s", rec.Body.String())
	}
}

func TestLoginCorrectToken(t *testing.T) {
	h := newTestHandler(t)
	form := url.Values{"token": {fixtureToken}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := do(h, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/articles" {
		t.Errorf("Location = %q, want /admin/articles", loc)
	}
	c := findCookie(rec, "cnz_admin")
	if c == nil {
		t.Fatal("no cnz_admin cookie")
	}
	if !c.HttpOnly {
		t.Errorf("cookie not HttpOnly")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}
}

func TestArticlesRequiresSession(t *testing.T) {
	h := newTestHandler(t)

	rec := getArticles(h, nil, nil, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("no-cookie status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/login" {
		t.Errorf("Location = %q, want /admin/login", loc)
	}

	rec = getArticles(h, nil, nil, true) // HX-Request, no cookie
	if rec.Code != http.StatusOK {
		t.Fatalf("hx no-cookie status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/admin/login" {
		t.Errorf("HX-Redirect = %q, want /admin/login", got)
	}
}

func TestArticlesListAndFacets(t *testing.T) {
	h := newTestHandler(t)
	c := loginCookie(t, h)
	rec := getArticles(h, nil, c, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// A seeded title is shown (page 1, newest first -> Open Source Wins).
	if !strings.Contains(body, "Open Source Wins") {
		t.Errorf("expected a seeded title on the page")
	}
	// Facet options with counts: each section appears 4 times.
	doc := parseHTML(t, body)
	var techCount string
	for _, in := range findAll(doc, "input") {
		if attr(in, "name") == "section" && attr(in, "value") == "tech" {
			// the count span is a sibling within the same label; find it via parent
			if lbl := in.Parent; lbl != nil {
				for _, sp := range findAll(lbl, "span") {
					if strings.Contains(attr(sp, "class"), "admin-count") {
						techCount = strings.TrimSpace(textOf(sp))
					}
				}
			}
		}
	}
	if techCount != "4" {
		t.Errorf("tech facet count = %q, want 4", techCount)
	}
}

func TestMultiValueFilters(t *testing.T) {
	h := newTestHandler(t)
	c := loginCookie(t, h)

	t.Run("sections OR within axis", func(t *testing.T) {
		q := url.Values{"section": {"tech", "politics"}}
		body := getArticles(h, q, c, false).Body.String()
		if strings.Contains(body, "Markets Tumble") {
			t.Errorf("economics row leaked into section=tech,politics result")
		}
		if !strings.Contains(body, "Open Source Wins") || !strings.Contains(body, "AI Policy Debate") {
			t.Errorf("expected tech and politics rows present")
		}
	})

	t.Run("topics union dedups the two-topic article", func(t *testing.T) {
		q := url.Values{"topic": {"go", "election"}}
		body := getArticles(h, q, c, false).Body.String()
		// Senate Vote Recap has BOTH go and election; it must appear exactly once.
		if n := strings.Count(body, "Senate Vote Recap"); n != 1 {
			t.Errorf("Senate Vote Recap appears %d times, want exactly 1 (union dedup)", n)
		}
	})

	t.Run("author filter excludes others", func(t *testing.T) {
		q := url.Values{"author": {"ada"}}
		body := getArticles(h, q, c, false).Body.String()
		// ada wrote 4 (<= page size), all should appear; bo's must not.
		for _, want := range []string{"Go Generics Land", "Senate Vote Recap", "Quantum Chips Arrive", "Startup Funding Dips"} {
			if !strings.Contains(body, want) {
				t.Errorf("author=ada missing %q", want)
			}
		}
		if strings.Contains(body, "Compiler Speedups") {
			t.Errorf("author=ada leaked bo's article")
		}
	})

	t.Run("accented full-text query", func(t *testing.T) {
		q := url.Values{"q": {"Café"}}
		body := getArticles(h, q, c, false).Body.String()
		if !strings.Contains(body, "Cafe Society Reborn") {
			t.Errorf("q=Café did not match the accented article")
		}
		if strings.Contains(body, "Open Source Wins") {
			t.Errorf("q=Café leaked an unrelated article")
		}
	})

	t.Run("inclusive single-day date window", func(t *testing.T) {
		// Election Night Results is published 2026-05-10 09:00 UTC. from=to=that
		// day must include it (parseSelection maps 'to' onto the named day +24h,
		// the store's exclusive upper bound) and exclude the adjacent seeded days
		// (Compiler Speedups 05-05, Senate Vote Recap 05-15).
		q := url.Values{"from": {"2026-05-10"}, "to": {"2026-05-10"}}
		body := getArticles(h, q, c, false).Body.String()
		if !strings.Contains(body, "Election Night Results") {
			t.Errorf("from=to=2026-05-10 dropped that day's article (inclusive-day +24h not applied)")
		}
		if strings.Contains(body, "Compiler Speedups") {
			t.Errorf("date window leaked the prior day (2026-05-05)")
		}
		if strings.Contains(body, "Senate Vote Recap") {
			t.Errorf("date window leaked the next day (2026-05-15)")
		}
		if n := rowCount(body); n != 1 {
			t.Errorf("from=to=2026-05-10 rows = %d, want exactly 1", n)
		}
	})

	t.Run("malformed date param is ignored gracefully", func(t *testing.T) {
		// A garbage 'from' is swallowed per-field (no error page); the result is
		// the unconstrained full first page.
		q := url.Values{"from": {"not-a-date"}}
		rec := getArticles(h, q, c, false)
		if rec.Code != http.StatusOK {
			t.Fatalf("malformed from status = %d, want 200 (ignored, not a 500)", rec.Code)
		}
		if n := rowCount(rec.Body.String()); n != pageSize {
			t.Errorf("malformed from rows = %d, want a full unconstrained page (%d)", n, pageSize)
		}
	})
}

func TestPartialVsFull(t *testing.T) {
	h := newTestHandler(t)
	c := loginCookie(t, h)

	partial := getArticles(h, nil, c, true)
	if partial.Code != http.StatusOK {
		t.Fatalf("partial status = %d, want 200", partial.Code)
	}
	pb := partial.Body.String()
	if strings.Contains(pb, "<html") {
		t.Errorf("HX partial must not contain a full <html wrapper")
	}
	if !strings.Contains(pb, "Open Source Wins") {
		t.Errorf("HX partial missing the expected row")
	}

	full := getArticles(h, nil, c, false)
	fb := full.Body.String()
	if !strings.Contains(fb, "<html") {
		t.Errorf("full page should contain <html wrapper")
	}
	if !strings.Contains(fb, "Open Source Wins") {
		t.Errorf("full page missing the expected row (shareable URL must reproduce state)")
	}
}

func TestPagination(t *testing.T) {
	h := newTestHandler(t)
	c := loginCookie(t, h)

	if n := rowCount(getArticles(h, nil, c, false).Body.String()); n != 5 {
		t.Errorf("page 1 rows = %d, want 5", n)
	}
	if n := rowCount(getArticles(h, url.Values{"page": {"2"}}, c, false).Body.String()); n != 5 {
		t.Errorf("page 2 rows = %d, want 5", n)
	}
	if n := rowCount(getArticles(h, url.Values{"page": {"3"}}, c, false).Body.String()); n != 2 {
		t.Errorf("page 3 rows = %d, want 2", n)
	}
	// Out-of-range page: empty, no crash, still 200.
	rec := getArticles(h, url.Values{"page": {"99"}}, c, false)
	if rec.Code != http.StatusOK {
		t.Errorf("page 99 status = %d, want 200", rec.Code)
	}
	if n := rowCount(rec.Body.String()); n != 0 {
		t.Errorf("page 99 rows = %d, want 0", n)
	}

	// Filtered pagination: section=tech,politics -> 8 results -> page 1 has Next
	// whose URL carries both section values.
	q := url.Values{"section": {"tech", "politics"}}
	doc := parseHTML(t, getArticles(h, q, c, false).Body.String())
	var nextHref string
	for _, a := range findAll(doc, "a") {
		if strings.TrimSpace(textOf(a)) == "Next" {
			nextHref = attr(a, "href")
		}
	}
	if nextHref == "" {
		t.Fatalf("no Next link on filtered page 1")
	}
	if !strings.Contains(nextHref, "section=tech") || !strings.Contains(nextHref, "section=politics") {
		t.Errorf("Next href = %q, want it to carry both section filters", nextHref)
	}
	if !strings.Contains(nextHref, "page=2") {
		t.Errorf("Next href = %q, want page=2", nextHref)
	}
}

func TestDetail(t *testing.T) {
	h := newTestHandler(t)
	c := loginCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/admin/articles/go-generics-land", nil)
	req.AddCookie(c)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Go Generics Land") {
		t.Errorf("detail missing the title")
	}
	// Markdown rendered to sanitized elements.
	bodyRegion := between(body, `<div class="admin-article-body">`, "</div>")
	if !strings.Contains(bodyRegion, "<strong>") {
		t.Errorf("rendered body missing <strong> from **finally**: %q", bodyRegion)
	}
	if !strings.Contains(bodyRegion, "<h2") {
		t.Errorf("rendered body missing <h2 from ## Generics: %q", bodyRegion)
	}
	// XSS gate: the raw <script>/<img onerror> must NOT survive as live tags in
	// the body region (content.RenderMarkdown escapes + sanitizes).
	if strings.Contains(bodyRegion, "<script") {
		t.Errorf("unsanitized <script tag in rendered body: %q", bodyRegion)
	}
	if strings.Contains(bodyRegion, "<img") {
		t.Errorf("unsanitized <img tag (onerror) in rendered body: %q", bodyRegion)
	}

	// Unknown slug -> 404 HTML.
	req = httptest.NewRequest(http.MethodGet, "/admin/articles/no-such-slug", nil)
	req.AddCookie(c)
	rec = do(h, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug status = %d, want 404", rec.Code)
	}
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return rest
	}
	return rest[:j]
}

func TestLogout(t *testing.T) {
	h := newTestHandler(t)
	c := loginCookie(t, h)

	// Need a valid CSRF token: read it from a rendered page.
	page := getArticles(h, nil, c, false).Body.String()
	csrf := metaCSRF(t, page)

	t.Run("GET logout -> 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/logout", nil)
		req.AddCookie(c)
		rec := do(h, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET logout status = %d, want 405", rec.Code)
		}
	})

	t.Run("POST without csrf -> 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/admin/logout", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(c)
		rec := do(h, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("no-csrf status = %d, want 403", rec.Code)
		}
	})

	t.Run("POST wrong csrf -> 403", func(t *testing.T) {
		form := url.Values{"csrf_token": {"wrong"}}
		req := httptest.NewRequest(http.MethodPost, "/admin/logout", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(c)
		rec := do(h, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("wrong-csrf status = %d, want 403", rec.Code)
		}
	})

	t.Run("POST valid csrf -> 303 and cookie cleared", func(t *testing.T) {
		form := url.Values{"csrf_token": {csrf}}
		req := httptest.NewRequest(http.MethodPost, "/admin/logout", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(c)
		rec := do(h, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/admin/login" {
			t.Errorf("Location = %q, want /admin/login", loc)
		}
		cleared := findCookie(rec, "cnz_admin")
		if cleared == nil || cleared.MaxAge >= 0 {
			t.Errorf("session cookie not cleared (MaxAge<0 expected): %+v", cleared)
		}
	})
}

func TestSessionIntegrity(t *testing.T) {
	h := newTestHandler(t)

	t.Run("forged HMAC -> unauthenticated", func(t *testing.T) {
		future := fixedNow().Add(time.Hour).Unix()
		forged := &http.Cookie{Name: "cnz_admin", Value: signSession(testSessionKey, future, false)}
		req := httptest.NewRequest(http.MethodGet, "/admin/articles", nil)
		req.AddCookie(forged)
		rec := do(h, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("forged cookie status = %d, want 303 (redirect to login)", rec.Code)
		}
	})

	t.Run("expired but correctly signed -> unauthenticated", func(t *testing.T) {
		past := fixedNow().Add(-time.Hour).Unix()
		expired := &http.Cookie{Name: "cnz_admin", Value: signSession(testSessionKey, past, true)}
		req := httptest.NewRequest(http.MethodGet, "/admin/articles", nil)
		req.AddCookie(expired)
		rec := do(h, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("expired cookie status = %d, want 303", rec.Code)
		}
	})

	t.Run("valid signed cookie -> authenticated", func(t *testing.T) {
		future := fixedNow().Add(time.Hour).Unix()
		good := &http.Cookie{Name: "cnz_admin", Value: signSession(testSessionKey, future, true)}
		req := httptest.NewRequest(http.MethodGet, "/admin/articles", nil)
		req.AddCookie(good)
		rec := do(h, req)
		if rec.Code != http.StatusOK {
			t.Errorf("valid cookie status = %d, want 200", rec.Code)
		}
	})
}

// TestSlidingRenewal crosses the renewal boundary (more than half the TTL
// elapsed) and proves: a fresh cookie is minted, the renewed cookie itself
// authenticates, the CSRF token on the renewed page validates the next POST
// (regression guard for binding the token to the OLD expiry), and an already
// dead cookie is never resurrected.
func TestSlidingRenewal(t *testing.T) {
	base := fixedNow() // 2026-06-22 12:00 UTC
	const ttl = 10 * time.Minute
	now := base.Add(6 * time.Minute) // >TTL/2 (5m) elapsed vs a base-time cookie
	h := newTestHandlerWith(t, func() time.Time { return now }, ttl)

	// Cookie minted at base -> exp = base+10m. At now=base+6m only 4m remain
	// (<5m = TTL/2), so requireSession must renew.
	oldExp := base.Add(ttl).Unix()
	oldCookie := &http.Cookie{Name: "cnz_admin", Value: signSession(testSessionKey, oldExp, true)}

	req := httptest.NewRequest(http.MethodGet, "/admin/articles", nil)
	req.AddCookie(oldCookie)
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("renewal GET status = %d, want 200", rec.Code)
	}
	renewed := findCookie(rec, "cnz_admin")
	if renewed == nil {
		t.Fatalf("renewal GET set no cnz_admin cookie; sliding renewal did not fire")
	}
	if renewed.Value == oldCookie.Value {
		t.Fatalf("renewed cookie value equals the old one; no fresh expiry was minted")
	}

	// (2) The renewed cookie authenticates a follow-up request.
	req2 := httptest.NewRequest(http.MethodGet, "/admin/articles", nil)
	req2.AddCookie(renewed)
	if rec2 := do(h, req2); rec2.Code != http.StatusOK {
		t.Fatalf("renewed cookie auth status = %d, want 200", rec2.Code)
	}

	// (3) The CSRF token rendered on the renewed page must validate a logout POST
	// sent with the renewed cookie. Before the fix the page carried csrf(oldExp)
	// while the cookie encoded newExp, so this POST 403'd.
	csrf := metaCSRF(t, rec.Body.String())
	form := url.Values{"csrf_token": {csrf}}
	lreq := httptest.NewRequest(http.MethodPost, "/admin/logout", strings.NewReader(form.Encode()))
	lreq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lreq.AddCookie(renewed)
	lrec := do(h, lreq)
	if lrec.Code != http.StatusSeeOther {
		t.Fatalf("logout after renewal status = %d, want 303 (csrf must bind to the renewed cookie); body=%s",
			lrec.Code, lrec.Body.String())
	}

	// (4) A cookie already expired is never resurrected: redirect, no Set-Cookie.
	deadExp := base.Add(-time.Minute).Unix()
	dead := &http.Cookie{Name: "cnz_admin", Value: signSession(testSessionKey, deadExp, true)}
	dreq := httptest.NewRequest(http.MethodGet, "/admin/articles", nil)
	dreq.AddCookie(dead)
	drec := do(h, dreq)
	if drec.Code != http.StatusSeeOther {
		t.Errorf("dead cookie status = %d, want 303 (no renewal)", drec.Code)
	}
	if c := findCookie(drec, "cnz_admin"); c != nil {
		t.Errorf("dead cookie was resurrected with a Set-Cookie: %+v", c)
	}
}

func TestAssets(t *testing.T) {
	h := newTestHandler(t)

	t.Run("htmx served as javascript", func(t *testing.T) {
		rec := do(h, httptest.NewRequest(http.MethodGet, "/admin/assets/htmx.min.js", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "javascript") {
			t.Errorf("Content-Type = %q, want a javascript type", ct)
		}
	})

	t.Run("css served as text/css", func(t *testing.T) {
		rec := do(h, httptest.NewRequest(http.MethodGet, "/admin/assets/admin.css", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
			t.Errorf("Content-Type = %q, want text/css", ct)
		}
	})

	t.Run("unknown asset -> 404 (no embedded escape)", func(t *testing.T) {
		rec := do(h, httptest.NewRequest(http.MethodGet, "/admin/assets/adminweb.go", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("unknown asset status = %d, want 404", rec.Code)
		}
	})
}

func TestHealthz(t *testing.T) {
	h := newTestHandler(t)
	rec := do(h, httptest.NewRequest(http.MethodGet, "/admin/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("healthz = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}

func TestRootRedirect(t *testing.T) {
	h := newTestHandler(t)
	rec := do(h, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/articles" {
		t.Errorf("Location = %q, want /admin/articles", loc)
	}
}

// Ensure the Handler satisfies http.Handler (compile-time).
var _ http.Handler = (*adminweb.Handler)(nil)

// Ensure store.Repository is what we pass (compile-time hygiene).
var _ store.Repository = (*sqlite.Store)(nil)
