package adminweb

import (
	"crypto/subtle"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const loginPage = `<!doctype html>
<html lang="es"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Censurado | Acceso</title>
<link rel="stylesheet" href="/styles.css">
</head>
<body class="login-page">
<main class="login-card">
<div class="login-brand"><span class="brand-mark">CN</span><span>Censurado</span></div>
<h1>Panel de administraci&oacute;n</h1>
<form class="login-form" method="post" action="/login">
<label class="field">Token de operador
<input type="password" name="token" autocomplete="current-password" value="%s" autofocus>
</label>
<button type="submit">Entrar</button>
</form>
%s
</main>
</body></html>
`

// setSessionCookies writes the signed session cookie (HttpOnly) and the CSRF cookie
// (readable by the SPA), both bound to expUnix and pure functions of the key, so the
// server holds no per-session state.
func setSessionCookies(w http.ResponseWriter, cfg Config, expUnix int64) {
	maxAge := int(cfg.ttl().Seconds())
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    SessionValue(cfg.SessionKey, expUnix),
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   cfg.SecureCookies,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookie,
		Value:    CSRFValue(cfg.SessionKey, expUnix),
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: false,
		Secure:   cfg.SecureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookies(w http.ResponseWriter, cfg Config) {
	for _, name := range []struct {
		n        string
		httpOnly bool
	}{{SessionCookie, true}, {CSRFCookie, false}} {
		http.SetCookie(w, &http.Cookie{
			Name:     name.n,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name.httpOnly,
			Secure:   cfg.SecureCookies,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

// loginTokenPrefill returns the cleartext login token to prefill only when it
// matches the configured hash (a local-box convenience). Any mismatch yields "".
func loginTokenPrefill(cfg Config) string {
	if cfg.LoginToken == "" || cfg.LoginTokenHash == "" {
		return ""
	}
	if subtle.ConstantTimeCompare([]byte(hashToken(cfg.LoginToken)), []byte(cfg.LoginTokenHash)) != 1 {
		return ""
	}
	return html.EscapeString(cfg.LoginToken)
}

func (h *handler) loginGET(w http.ResponseWriter, r *http.Request) {
	if cookie, _ := r.Cookie(SessionCookie); cookie != nil {
		if _, ok := VerifySession(h.cfg.SessionKey, cookie.Value, h.cfg.now().Unix()); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	renderLogin(w, h.cfg, http.StatusOK, "")
}

func (h *handler) loginPOST(w http.ResponseWriter, r *http.Request) {
	token := submittedToken(r)
	// Both sides are hex sha256 (fixed length), so the constant-time compare neither
	// leaks length nor short-circuits on the first differing byte.
	if h.cfg.LoginTokenHash != "" &&
		subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(h.cfg.LoginTokenHash)) == 1 {
		exp := h.cfg.now().Add(h.cfg.ttl()).Unix()
		setSessionCookies(w, h.cfg, exp)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// Generic error; never reflect the submitted token. No cookie is set.
	renderLogin(w, h.cfg, http.StatusOK, `<p class="login-error">Token inv&aacute;lido.</p>`)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookies(w, h.cfg)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func renderLogin(w http.ResponseWriter, cfg Config, status int, errHTML string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, sprintfLogin(loginTokenPrefill(cfg), errHTML))
}

// sprintfLogin fills the login template. Kept as a tiny helper so the format string
// stays close to the two substitution points (token prefill, error block).
func sprintfLogin(tokenValue, errHTML string) string {
	page := strings.Replace(loginPage, "%s", tokenValue, 1)
	return strings.Replace(page, "%s", errHTML, 1)
}

// submittedToken reads the login token from a JSON or urlencoded form body without
// depending on multipart parsing.
func submittedToken(r *http.Request) string {
	raw, _ := io.ReadAll(r.Body)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			Token string `json:"token"`
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		return body.Token
	}
	vals, err := url.ParseQuery(string(raw))
	if err != nil {
		return ""
	}
	return vals.Get("token")
}
