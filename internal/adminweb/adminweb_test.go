package adminweb_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/adminweb"
	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

const (
	opSecret   = "op-secret-key-value"
	loginToken = "letmein"
)

var (
	sessionKey = []byte("0123456789abcdef0123456789abcdef") // 32 bytes
	fixedTime  = time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
)

func loginHash() string {
	sum := sha256.Sum256([]byte(loginToken))
	return hex.EncodeToString(sum[:])
}

func fixedNow() time.Time { return fixedTime }

// newAdminServer wires the full API over a real sqlite store, then folds in the
// admin panel with a deterministic clock and a known login token. ak_op is the only
// key: it holds the full operator scope set (the bearer lane), so a bearer request
// exercises the unchanged API path beside the browser session.
func newAdminServer(t *testing.T) (http.Handler, adminweb.Config) {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	auth := publish.NewStaticKeyAuth()
	auth.Add("ak_op", publish.HashSecret(opSecret), "editor",
		publish.ScopeWrite, publish.ScopePublishAny, publish.ScopeAdminWrite)

	h := publish.NewHandler(repo, repo, auth, fixedNow)
	rh := publish.NewReadHandler(repo, auth)
	oh := publish.NewOperatorHandler(repo, auth, fixedNow)
	limiter := publish.NewRateLimiter(1000, 1000, fixedNow)
	api := publish.NewServerHandler(h, limiter, nil, rh, oh)

	cfg := adminweb.Config{
		SessionKey:     sessionKey,
		LoginTokenHash: loginHash(),
		SecureCookies:  false,
		SessionTTL:     time.Hour,
		Now:            fixedNow,
	}
	handler, ok := adminweb.Handler(cfg, api)
	if !ok {
		t.Fatal("panel not enabled with a session key + login hash")
	}
	return handler, cfg
}

// sessionCookie mints a valid session cookie value for the fixed clock.
func sessionCookie() string {
	exp := fixedTime.Add(time.Hour).Unix()
	return adminweb.SessionValue(sessionKey, exp)
}

func csrfHeader() string {
	exp := fixedTime.Add(time.Hour).Unix()
	return adminweb.CSRFValue(sessionKey, exp)
}

// req is a small request builder: method, path, optional JSON body, then options.
type opt func(*http.Request)

func withBearer(tok string) opt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}
func withSession() opt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: adminweb.SessionCookie, Value: sessionCookie()}) }
}
func withCSRF() opt   { return func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrfHeader()) } }
func acceptHTML() opt { return func(r *http.Request) { r.Header.Set("Accept", "text/html") } }
func asXHR() opt      { return func(r *http.Request) { r.Header.Set("X-Requested-With", "XMLHttpRequest") } }

func do(t *testing.T, h http.Handler, method, path, body string, opts ...opt) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rdr)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(r)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// --- session + CSRF math ---------------------------------------------------

func TestSessionRoundTripTamperAndExpiry(t *testing.T) {
	exp := fixedTime.Add(time.Hour).Unix()
	val := adminweb.SessionValue(sessionKey, exp)

	if got, ok := adminweb.VerifySession(sessionKey, val, fixedTime.Unix()); !ok || got != exp {
		t.Fatalf("round-trip failed: got=%d ok=%v want=%d", got, ok, exp)
	}
	if _, ok := adminweb.VerifySession(sessionKey, val, exp); ok {
		t.Fatal("expired session accepted (now >= exp must fail)")
	}
	tampered := val[:len(val)-1] + flipLast(val)
	if _, ok := adminweb.VerifySession(sessionKey, tampered, fixedTime.Unix()); ok {
		t.Fatal("tampered session accepted")
	}
	wrongKey := []byte("ffffffffffffffffffffffffffffffff")
	if _, ok := adminweb.VerifySession(wrongKey, val, fixedTime.Unix()); ok {
		t.Fatal("session verified under the wrong key")
	}
	if _, ok := adminweb.VerifySession(sessionKey, "", fixedTime.Unix()); ok {
		t.Fatal("empty session accepted")
	}
}

func TestCSRFBindingAndDomainSeparation(t *testing.T) {
	exp := fixedTime.Add(time.Hour).Unix()
	tok := adminweb.CSRFValue(sessionKey, exp)
	if !adminweb.ValidCSRF(sessionKey, exp, tok) {
		t.Fatal("matching CSRF token rejected")
	}
	if adminweb.ValidCSRF(sessionKey, exp, tok+"x") {
		t.Fatal("wrong CSRF token accepted")
	}
	// The session cookie value must NOT validate as a CSRF token (domain separation).
	if adminweb.ValidCSRF(sessionKey, exp, adminweb.SessionValue(sessionKey, exp)) {
		t.Fatal("session value accepted as CSRF token (no domain separation)")
	}
	// A CSRF token bound to a different expiry must not validate.
	if adminweb.ValidCSRF(sessionKey, exp+1, tok) {
		t.Fatal("CSRF token accepted for a different session expiry")
	}
}

func TestDecodeKey(t *testing.T) {
	if _, ok := adminweb.DecodeKey("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); !ok {
		t.Fatal("hex key rejected")
	}
	if _, ok := adminweb.DecodeKey("AAAAAAAAAAAAAAAAAAAAAAAA"); !ok {
		t.Fatal("base64 key rejected")
	}
	if _, ok := adminweb.DecodeKey("not a key !!!"); ok {
		t.Fatal("garbage key accepted")
	}
}

// --- login flow ------------------------------------------------------------

func TestLoginSetsSessionThenServesSPA(t *testing.T) {
	h, _ := newAdminServer(t)

	rec := do(t, h, "POST", "/login", "", func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Body = io.NopCloser(strings.NewReader("token=" + loginToken))
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login: got %d want 303\n%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("login redirect = %q want /", loc)
	}
	var gotSession, gotCSRF bool
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case adminweb.SessionCookie:
			gotSession = c.Value != ""
			if !c.HttpOnly {
				t.Error("session cookie must be HttpOnly")
			}
		case adminweb.CSRFCookie:
			gotCSRF = c.Value != ""
			if c.HttpOnly {
				t.Error("CSRF cookie must NOT be HttpOnly (the SPA reads it)")
			}
		}
	}
	if !gotSession || !gotCSRF {
		t.Fatalf("login cookies: session=%v csrf=%v", gotSession, gotCSRF)
	}

	// The session now serves the SPA shell at /.
	page := do(t, h, "GET", "/", "", withSession(), acceptHTML())
	if page.Code != http.StatusOK {
		t.Fatalf("GET / with session = %d want 200", page.Code)
	}
	if !strings.Contains(page.Body.String(), `id="app"`) {
		t.Fatalf("SPA shell not served:\n%s", page.Body.String())
	}
	if csp := page.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("missing same-origin CSP on the shell: %q", csp)
	}
}

func TestLoginRejectsWrongToken(t *testing.T) {
	h, _ := newAdminServer(t)
	rec := do(t, h, "POST", "/login", "", func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Body = io.NopCloser(strings.NewReader("token=wrong"))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("wrong login: got %d want 200 (re-rendered form)", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == adminweb.SessionCookie && c.Value != "" {
			t.Fatal("a session cookie was set for a wrong token")
		}
	}
	if !strings.Contains(rec.Body.String(), "login-error") {
		t.Fatal("wrong login did not render the error")
	}
}

// --- the gate --------------------------------------------------------------

func TestGateRedirectsBrowserNavAnd401sXHR(t *testing.T) {
	h, _ := newAdminServer(t)

	nav := do(t, h, "GET", "/", "", acceptHTML())
	if nav.Code != http.StatusSeeOther || nav.Header().Get("Location") != "/login" {
		t.Fatalf("unauth browser nav: got %d loc=%q want 303 /login", nav.Code, nav.Header().Get("Location"))
	}

	xhr := do(t, h, "GET", "/authors", "", asXHR())
	if xhr.Code != http.StatusUnauthorized {
		t.Fatalf("unauth XHR: got %d want 401", xhr.Code)
	}
}

func TestOpenPathsBypassTheGate(t *testing.T) {
	h, _ := newAdminServer(t)
	for _, tc := range []struct {
		path, wantSub string
		code          int
	}{
		{"/healthz", "ok", http.StatusOK},
		{"/login", "login-form", http.StatusOK},
		{"/styles.css", "", http.StatusOK},
	} {
		rec := do(t, h, "GET", tc.path, "")
		if rec.Code != tc.code {
			t.Errorf("GET %s (no session) = %d want %d", tc.path, rec.Code, tc.code)
		}
		if tc.wantSub != "" && !strings.Contains(rec.Body.String(), tc.wantSub) {
			t.Errorf("GET %s body missing %q", tc.path, tc.wantSub)
		}
	}
}

// --- CSRF on session writes ------------------------------------------------

func TestCSRFRequiredOnSessionWrites(t *testing.T) {
	h, _ := newAdminServer(t)
	body := `{"handle":"ada","name":"Ada"}`

	noCSRF := do(t, h, "POST", "/authors", body, withSession())
	if noCSRF.Code != http.StatusForbidden {
		t.Fatalf("session write without CSRF: got %d want 403", noCSRF.Code)
	}
	if code := errorCode(t, noCSRF); code != "invalid_csrf" {
		t.Fatalf("error code = %q want invalid_csrf", code)
	}
	// The store was not touched.
	if list := do(t, h, "GET", "/authors", "", withSession()); strings.Contains(list.Body.String(), `"ada"`) {
		t.Fatal("author created despite the CSRF rejection")
	}

	bad := do(t, h, "POST", "/authors", body, withSession(), func(r *http.Request) { r.Header.Set("X-CSRF-Token", "nope") })
	if bad.Code != http.StatusForbidden {
		t.Fatalf("session write with wrong CSRF: got %d want 403", bad.Code)
	}

	ok := do(t, h, "POST", "/authors", body, withSession(), withCSRF())
	if ok.Code != http.StatusOK {
		t.Fatalf("session write with valid CSRF: got %d want 200\n%s", ok.Code, ok.Body.String())
	}
}

// --- session maps to admin:write (no bearer) -------------------------------

func TestSessionMapsToAdminWriteAndEdits(t *testing.T) {
	h, _ := newAdminServer(t)
	body := `{"handle":"lara","name":"Lara","gender":"female","about":"desk","style":"terse","topics":["politica","economia"]}`

	create := do(t, h, "POST", "/authors", body, withSession(), withCSRF())
	if create.Code != http.StatusOK {
		t.Fatalf("session create author: got %d want 200\n%s", create.Code, create.Body.String())
	}

	list := do(t, h, "GET", "/authors", "", withSession())
	if list.Code != http.StatusOK {
		t.Fatalf("session list authors: got %d want 200", list.Code)
	}
	got := list.Body.String()
	for _, want := range []string{`"lara"`, `"female"`, `"terse"`, "politica"} {
		if !strings.Contains(got, want) {
			t.Fatalf("author round-trip missing %q in:\n%s", want, got)
		}
	}
}

// --- bearer lane is unchanged (the session layer is additive) --------------

func TestBearerLaneUnchangedByTheGate(t *testing.T) {
	h, _ := newAdminServer(t)

	// A bearer write needs NO cookie and NO CSRF: the API-key lane is untouched.
	create := do(t, h, "POST", "/authors", `{"handle":"nana","name":"Nana"}`, withBearer("ak_op."+opSecret))
	if create.Code != http.StatusOK {
		t.Fatalf("bearer create (no cookie/CSRF): got %d want 200\n%s", create.Code, create.Body.String())
	}
	read := do(t, h, "GET", "/authors", "", withBearer("ak_op."+opSecret))
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"nana"`) {
		t.Fatalf("bearer read: got %d body=%s", read.Code, read.Body.String())
	}
}

func flipLast(s string) string {
	if s == "" {
		return "x"
	}
	last := s[len(s)-1]
	if last == 'a' {
		return "b"
	}
	return "a"
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		return ""
	}
	if v, ok := m["error"].(string); ok {
		return v
	}
	if v, ok := m["code"].(string); ok {
		return v
	}
	return ""
}
