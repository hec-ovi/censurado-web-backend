package adminweb

import (
	"embed"
	"io/fs"
	"net/http"
)

// staticFS holds the buildless admin SPA: index.html plus the ES modules and
// stylesheet, served same-origin so no external host is ever contacted.
//
//go:embed static
var staticFS embed.FS

// contentSecurityPolicy is emitted on the SPA shell and mirrors the app's own meta
// CSP: same-origin scripts/styles only, images may be same-origin, data:, or https:.
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data: https:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

type handler struct {
	cfg Config
}

// Handler wraps the backend API with the operator admin panel. It serves /login,
// /logout, and the embedded SPA at /, gating them and the API behind the
// signed-cookie session, and maps a valid session to the operator identity for the
// wrapped API. api is the backend's route mux (publish.NewServerHandler). When the
// panel is not configured (no session key / login hash) Handler returns api
// unwrapped and ok=false, so a pure-API deployment is byte-identical.
func Handler(cfg Config, api http.Handler) (http.Handler, bool) {
	if !cfg.Enabled() {
		return api, false
	}
	h := &handler{cfg: cfg}
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("adminweb: embedded static tree missing: " + err.Error())
	}
	assets := http.FileServerFS(sub)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", h.loginGET)
	mux.HandleFunc("POST /login", h.loginPOST)
	mux.HandleFunc("POST /logout", h.logout)

	// SPA assets at root. These explicit patterns beat the "/" catch-all, so the bare
	// API paths (/authors, /articles, /sources, /media/..., /healthz, ...) still reach
	// the API handler.
	mux.Handle("GET /{$}", spaIndex(sub))
	mux.Handle("GET /app.js", assets)
	mux.Handle("GET /api.js", assets)
	mux.Handle("GET /articles.css", assets)
	mux.Handle("GET /brand.css", assets)
	mux.Handle("GET /controls.css", assets)
	mux.Handle("GET /login.css", assets)
	mux.Handle("GET /login.js", assets)
	mux.Handle("GET /styles.css", assets)
	mux.Handle("GET /slugify.js", assets)
	mux.Handle("GET /components/", assets)

	// Everything else is the JSON API + liveness + media.
	mux.Handle("/", api)

	return gate(cfg, mux), true
}

// spaIndex serves the SPA shell for the exact root path, stamping the same-origin
// CSP the app expects.
func spaIndex(fsys fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		_, _ = w.Write(data)
	}
}
