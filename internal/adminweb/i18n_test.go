package adminweb_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/internal/adminweb"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
	"github.com/hec-ovi/censurado-web-backend/store/storetest"
)

// newLocaleHandler builds an admin handler with a configurable DefaultLocale over a
// tiny seed (one author + one article) with the operator lane wired, so the locale
// tests can render both a browse page and an author edit form (which carries the
// "Save changes" button). defaultLocale "" leaves the served default at English.
func newLocaleHandler(t *testing.T, defaultLocale string) *adminweb.Handler {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "i18n.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	if _, err := repo.UpsertAuthor(ctx, storetest.SampleAuthor("author-a")); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	art, err := domain.NewArticle(domain.PublishInput{
		Title: "Edit Me Please", Body: "original body text", Author: "author-a", Section: "politics",
	}, fixedNow())
	if err != nil {
		t.Fatalf("NewArticle: %v", err)
	}
	if _, err := repo.Upsert(ctx, art); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	op := func(context.Context, string, string, any) ([]byte, error) { return nil, nil }
	h, err := adminweb.New(adminweb.Config{
		Repo:          repo,
		Now:           fixedNow,
		TokenHash:     hashTok(fixtureToken),
		SessionKey:    testSessionKey,
		SecureCookies: false,
		PageSize:      pageSize,
		DefaultLocale: defaultLocale,
		Authors:       repo,
		Operator:      op,
	})
	if err != nil {
		t.Fatalf("adminweb.New: %v", err)
	}
	return h
}

// getPath fetches an authed GET page, optionally pinning a locale via the
// admin_lang cookie, and fails on a non-200.
func getPath(t *testing.T, h http.Handler, session *http.Cookie, lang, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(session)
	if lang != "" {
		req.AddCookie(&http.Cookie{Name: "admin_lang", Value: lang})
	}
	rec := do(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s (lang=%q) = %d, want 200 (%s)", path, lang, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func mustContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("body missing %q", w)
		}
	}
}

func mustNotContain(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(body, u) {
			t.Errorf("body unexpectedly contains %q", u)
		}
	}
}

// TestDefaultLocaleEnglishUnchanged proves the test-default (no DefaultLocale, no
// cookie) renders English byte-for-byte: {{T}} is the identity, so the page reads
// exactly as before i18n was added.
func TestDefaultLocaleEnglishUnchanged(t *testing.T) {
	h := newTestHandler(t)
	c := loginCookie(t, h)
	body := getPath(t, h, c, "", "/admin/articles")
	mustContain(t, body, `<html lang="en">`, "Browse articles", "Filter", "Log out", "Newest first")
	mustNotContain(t, body, "Explorar artículos", "Cerrar sesión")
}

// TestSpanishCookieRendersSpanish proves a request carrying admin_lang=es renders
// the catalog overlay on a representative page (the browse page).
func TestSpanishCookieRendersSpanish(t *testing.T) {
	h := newTestHandler(t)
	c := loginCookie(t, h)
	body := getPath(t, h, c, "es", "/admin/articles")
	mustContain(t, body,
		`<html lang="es">`,
		"Explorar artículos", // page title + h1
		"Filtro",             // filter heading
		"Cerrar sesión",      // logout
		"Aplicar",            // apply button
		"Restablecer",        // reset
		"Más recientes primero",
	)
	// The nav links translate too.
	mustContain(t, body, `>Autores<`, `>Artículos<`, `>Auditoría<`)
	// English source strings must not leak on the translated page.
	mustNotContain(t, body, "Browse articles", "Log out", ">Dashboard<")
}

// TestSpanishFormRendersSpanish proves a configured form (the author edit form)
// renders its labels and the Save-changes button in Spanish under admin_lang=es.
func TestSpanishFormRendersSpanish(t *testing.T) {
	h := newLocaleHandler(t, "")
	c := loginCookie(t, h)
	body := getPath(t, h, c, "es", "/admin/authors/author-a/edit")
	mustContain(t, body,
		"Editar autor",    // h1 + title
		"Guardar cambios", // submit button
		"Identificador",   // Handle label
		"Volver a autores",
		"URL del avatar",
		"Biografía",
	)
	mustNotContain(t, body, "Save changes", "Back to authors")
}

// TestDefaultLocaleSpanishServed proves New(Config{DefaultLocale:"es"}) serves
// Spanish when no admin_lang cookie is present (the production default).
func TestDefaultLocaleSpanishServed(t *testing.T) {
	h := newLocaleHandler(t, "es")
	c := loginCookie(t, h)
	body := getPath(t, h, c, "", "/admin/articles")
	mustContain(t, body, `<html lang="es">`, "Explorar artículos", "Filtro", "Cerrar sesión")
	mustNotContain(t, body, "Browse articles")
}

// TestCookieOverridesDefaultLocale proves the admin_lang cookie wins over a
// Spanish-default handler, so an operator can pin English on a Spanish-first rig.
func TestCookieOverridesDefaultLocale(t *testing.T) {
	h := newLocaleHandler(t, "es")
	c := loginCookie(t, h)
	body := getPath(t, h, c, "en", "/admin/articles")
	mustContain(t, body, `<html lang="en">`, "Browse articles", "Filter")
	mustNotContain(t, body, "Explorar artículos")
}

// TestLangSwitchPresentAndPressed proves the EN/ES switch is rendered with
// aria-pressed reflecting the active locale, on both the chrome layout and the
// standalone login page.
func TestLangSwitchPressed(t *testing.T) {
	h := newTestHandler(t)
	c := loginCookie(t, h)

	en := getPath(t, h, c, "en", "/admin/articles")
	mustContain(t, en,
		`<button type="button" class="admin-lang-option" data-lang-choice="en" aria-pressed="true">EN</button>`,
		`<button type="button" class="admin-lang-option" data-lang-choice="es" aria-pressed="false">ES</button>`,
	)

	es := getPath(t, h, c, "es", "/admin/articles")
	mustContain(t, es,
		`<button type="button" class="admin-lang-option" data-lang-choice="en" aria-pressed="false">EN</button>`,
		`<button type="button" class="admin-lang-option" data-lang-choice="es" aria-pressed="true">ES</button>`,
	)

	// The login page (standalone shell, no session) also carries the switch and
	// translates its labels under es.
	loginReq := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	loginReq.AddCookie(&http.Cookie{Name: "admin_lang", Value: "es"})
	loginRec := do(h, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("GET /admin/login = %d, want 200", loginRec.Code)
	}
	loginBody := loginRec.Body.String()
	mustContain(t, loginBody,
		`<html lang="es">`,
		`data-lang-choice="es" aria-pressed="true"`,
		"Iniciar sesión",    // Sign in button
		"Token de operador", // Operator token label
		"Panel de administración",
	)
}
