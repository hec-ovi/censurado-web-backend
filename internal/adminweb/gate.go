package adminweb

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
)

// openPath reports whether a path is reachable without a session: liveness, the
// login/logout endpoints, the login page's stylesheet, and the public keyless media
// reads. These bypass the session gate and go straight to the wrapped handlers,
// which apply their own auth (media upload still needs a bearer; the login POST sets
// the session).
func openPath(path string) bool {
	switch path {
	case "/healthz", "/login", "/logout", "/styles.css", "/brand.css", "/controls.css", "/login.css", "/login.js", "/components/i18n.js":
		return true
	}
	return path == "/media" || strings.HasPrefix(path, "/media/")
}

// hasBearer reports whether the request carries an Authorization: Bearer header. A
// bearer request is a CLI/API caller and is passed through untouched, so the
// existing token lanes keep working byte-identically beside the browser session.
func hasBearer(r *http.Request) bool {
	return bearerPrefix(r.Header.Get("Authorization"))
}

func bearerPrefix(header string) bool {
	const p = "Bearer "
	return len(header) > len(p) && strings.EqualFold(header[:len(p)], p)
}

// isBrowserNav reports whether the request is a top-level browser navigation, so an
// unauthenticated one gets a 303 to the login page instead of a 401. It is a GET that
// explicitly accepts HTML and is not flagged as an XHR. A fetch() from the SPA sends
// Accept: */* (or sets X-Requested-With), so it falls through to a 401 the client can
// handle rather than silently following a redirect to the login HTML.
func isBrowserNav(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func isWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// noCache keeps the buildless admin from showing stale HTML/CSS/JS after a redeploy.
func noCache(w http.ResponseWriter, path string) {
	if path == "/" || strings.HasSuffix(path, ".html") ||
		strings.HasSuffix(path, ".css") || strings.HasSuffix(path, ".js") {
		w.Header().Set("Cache-Control", "no-store")
	}
}

// gate wraps next with the signed-cookie session. It branches on credential type:
//   - a bearer request passes through untouched (the CLI/API lane is unchanged);
//   - an open path passes through (liveness, login, public media);
//   - a valid session maps to the operator identity in-process (no bearer), and any
//     state-changing method must carry the matching CSRF double-submit token;
//   - no/invalid session gets a 303 to /login for a browser navigation, else a 401.
func gate(cfg Config, next http.Handler) http.Handler {
	id := operatorIdentity()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if hasBearer(r) || openPath(path) {
			noCache(w, path)
			next.ServeHTTP(w, r)
			return
		}
		cookie, _ := r.Cookie(SessionCookie)
		value := ""
		if cookie != nil {
			value = cookie.Value
		}
		exp, ok := VerifySession(cfg.SessionKey, value, cfg.now().Unix())
		if !ok {
			if isBrowserNav(r) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if isWrite(r.Method) && !ValidCSRF(cfg.SessionKey, exp, r.Header.Get("X-CSRF-Token")) {
			writeJSONError(w, http.StatusForbidden, "invalid_csrf")
			return
		}
		noCache(w, path)
		next.ServeHTTP(w, r.WithContext(publish.ContextWithIdentity(r.Context(), id)))
	})
}

func writeJSONError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
