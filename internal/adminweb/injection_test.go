package adminweb_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/adminweb"
	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

// newAdminServerWithText wires the panel with PanelText/SectionLabels readers so the
// SPA shell carries an injected catalog, mirroring newAdminServer otherwise.
func newAdminServerWithText(t *testing.T, cfg adminweb.Config) http.Handler {
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

	cfg.SessionKey = sessionKey
	cfg.LoginTokenHash = loginHash()
	cfg.SessionTTL = time.Hour
	cfg.Now = fixedNow
	handler, ok := adminweb.Handler(cfg, api)
	if !ok {
		t.Fatal("panel not enabled")
	}
	return handler
}

// panelI18NBlob pulls the JSON out of the injected #panel-i18n data block.
func panelI18NBlob(t *testing.T, body string) string {
	t.Helper()
	if strings.Contains(body, "__PANEL_I18N__") {
		t.Fatal("injection marker was not substituted")
	}
	const open = `<script type="application/json" id="panel-i18n">`
	i := strings.Index(body, open)
	if i < 0 {
		t.Fatal("panel-i18n data block missing")
	}
	rest := body[i+len(open):]
	j := strings.Index(rest, "</script>")
	if j < 0 {
		t.Fatal("panel-i18n data block not terminated")
	}
	return rest[:j]
}

func TestSPAInjectsPanelTextCatalog(t *testing.T) {
	handler := newAdminServerWithText(t, adminweb.Config{
		PanelText: func(_ context.Context, lang string) (map[string]string, error) {
			if lang != "en" {
				t.Errorf("panel_text read with lang %q, want en", lang)
			}
			return map[string]string{"Save portada": "Save portada", "Admin Panel": "Admin Panel"}, nil
		},
		SectionLabels: func(_ context.Context, lang string) (map[string]string, error) {
			if lang != "en" {
				t.Errorf("section labels read with lang %q, want en", lang)
			}
			return map[string]string{"world": "World", "tech": "Technology"}, nil
		},
	})

	page := do(t, handler, "GET", "/", "", withSession(), acceptHTML())
	if page.Code != http.StatusOK {
		t.Fatalf("GET / = %d want 200", page.Code)
	}
	body := page.Body.String()

	var blob struct {
		Strings  map[string]string `json:"strings"`
		Sections map[string]string `json:"sections"`
	}
	if err := json.Unmarshal([]byte(panelI18NBlob(t, body)), &blob); err != nil {
		t.Fatalf("injected blob is not valid JSON: %v", err)
	}
	if blob.Strings["Save portada"] != "Save portada" {
		t.Errorf("injected strings missing the seeded key: %v", blob.Strings)
	}
	if blob.Sections["world"] != "World" || blob.Sections["tech"] != "Technology" {
		t.Errorf("injected sections missing labels: %v", blob.Sections)
	}
	// The shell is otherwise untouched: strict same-origin CSP and #app root.
	assertStrictCSP(t, page.Header().Get("Content-Security-Policy"))
	if !strings.Contains(body, `id="app"`) {
		t.Error("SPA root #app missing after injection")
	}
}

// assertStrictCSP pins the whole inline-script-blocking policy, not just a substring, so
// a regression that adds unsafe-inline (which would defeat the JSON-data-block design)
// fails loudly.
func assertStrictCSP(t *testing.T, csp string) {
	t.Helper()
	for _, want := range []string{"default-src 'self'", "object-src 'none'", "base-uri 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %q", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP must never allow unsafe-inline (breaks the data-block model): %q", csp)
	}
}

// A panel_text (or section-label) value containing a </script> sequence must be escaped
// so it cannot terminate the inert <script type="application/json"> data block. The
// defense is json.Marshal's default HTML escaping (< -> <); this pins it as tested.
func TestSPAInjectionEscapesScriptBreakout(t *testing.T) {
	const payload = `</script><script>alert(1)</script>`
	handler := newAdminServerWithText(t, adminweb.Config{
		PanelText: func(_ context.Context, _ string) (map[string]string, error) {
			return map[string]string{"x": payload}, nil
		},
		SectionLabels: func(_ context.Context, _ string) (map[string]string, error) {
			return map[string]string{"world": payload}, nil
		},
	})

	page := do(t, handler, "GET", "/", "", withSession(), acceptHTML())
	if page.Code != http.StatusOK {
		t.Fatalf("GET / = %d want 200", page.Code)
	}
	body := page.Body.String()

	// The raw payload must not appear literally, and no early </script> can close the
	// block. A SetEscapeHTML(false) regression would put the literal payload in the body.
	if strings.Contains(body, payload) {
		t.Fatalf("breakout payload appears unescaped in the served HTML")
	}
	if strings.Contains(body, "</script><script>") {
		t.Fatalf("an injected </script> terminated the data block early")
	}
	// The escaped form still decodes back to the original value inside the data block.
	var blob struct {
		Strings  map[string]string `json:"strings"`
		Sections map[string]string `json:"sections"`
	}
	if err := json.Unmarshal([]byte(panelI18NBlob(t, body)), &blob); err != nil {
		t.Fatalf("injected blob is not valid JSON: %v", err)
	}
	if blob.Strings["x"] != payload || blob.Sections["world"] != payload {
		t.Errorf("payload did not round-trip: strings[x]=%q sections[world]=%q", blob.Strings["x"], blob.Sections["world"])
	}
}

func TestSPAInjectsEmptyCatalogWithoutReaders(t *testing.T) {
	// newAdminServer builds a Config with no PanelText/SectionLabels readers.
	h, _ := newAdminServer(t)
	page := do(t, h, "GET", "/", "", withSession(), acceptHTML())
	if page.Code != http.StatusOK {
		t.Fatalf("GET / = %d want 200", page.Code)
	}
	var blob struct {
		Strings  map[string]string `json:"strings"`
		Sections map[string]string `json:"sections"`
	}
	if err := json.Unmarshal([]byte(panelI18NBlob(t, page.Body.String())), &blob); err != nil {
		t.Fatalf("blob not valid JSON with nil readers: %v", err)
	}
	if len(blob.Strings) != 0 || len(blob.Sections) != 0 {
		t.Errorf("expected empty catalog with nil readers, got strings=%v sections=%v", blob.Strings, blob.Sections)
	}
}
