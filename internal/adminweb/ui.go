package adminweb

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
)

// staticFS holds the buildless admin SPA: index.html plus the ES modules and
// stylesheet, served same-origin so no external host is ever contacted.
//
//go:embed static
var staticFS embed.FS

// contentSecurityPolicy is emitted on the SPA shell and mirrors the app's own meta
// CSP: same-origin scripts/styles only, images may be same-origin, data:, or https:.
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data: https:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// panelLang is the single language the operator panel renders. English is the base
// every panel_text key is authored in; the panel has no language switch.
const panelLang = "en"

// panelI18NMarker is the literal in index.html that spaIndex replaces with the injected
// catalog JSON. Left in place (e.g. index.html served without substitution) it is
// invalid JSON, so the client's JSON.parse falls back to the English identity.
const panelI18NMarker = "__PANEL_I18N__"

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
	mux.Handle("GET /{$}", h.spaIndex(sub))
	mux.Handle("GET /app.js", assets)
	mux.Handle("GET /api.js", assets)
	mux.Handle("GET /articles.css", assets)
	mux.Handle("GET /brand.css", assets)
	mux.Handle("GET /controls.css", assets)
	mux.Handle("GET /login.css", assets)
	mux.Handle("GET /login.js", assets)
	mux.Handle("GET /styles.css", assets)
	mux.Handle("GET /automation.css", assets)
	mux.Handle("GET /slugify.js", assets)
	mux.Handle("GET /schedule.js", assets)
	mux.Handle("GET /components/", assets)

	// Everything else is the JSON API + liveness + media.
	mux.Handle("/", api)

	return gate(cfg, mux), true
}

// spaIndex serves the SPA shell for the exact root path, stamping the same-origin CSP
// the app expects and injecting the panel_text catalog (see panelI18N) into the
// #panel-i18n data block before first render.
func (h *handler) spaIndex(fsys fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		page := strings.Replace(string(data), panelI18NMarker, string(h.panelI18N(r.Context())), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		_, _ = w.Write([]byte(page))
	}
}

// panelI18N builds the JSON the SPA reads at boot: the panel_text strings for the panel
// language plus the shared section labels. A nil reader or a read error leaves that half
// empty, so the panel always renders (English identity for strings, slug for sections).
// json.Marshal keeps its default HTML escaping, so "<" becomes "<" and the blob can
// never terminate the surrounding <script> element.
func (h *handler) panelI18N(ctx context.Context) []byte {
	payload := struct {
		Strings  map[string]string `json:"strings"`
		Sections map[string]string `json:"sections"`
	}{Strings: map[string]string{}, Sections: map[string]string{}}
	if h.cfg.PanelText != nil {
		if m, err := h.cfg.PanelText(ctx, panelLang); err == nil && m != nil {
			payload.Strings = m
		}
	}
	if h.cfg.SectionLabels != nil {
		if m, err := h.cfg.SectionLabels(ctx, panelLang); err == nil && m != nil {
			payload.Sections = m
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// SectionLabelsFromFrontend projects the shared frontend_text catalog (rows keyed
// "section.<slug>.label") down to the slug -> label map the panel's section pickers
// show. It is the single source of record that keeps the panel's section names from
// drifting from the site's. Non-section keys are ignored.
func SectionLabelsFromFrontend(front map[string]string) map[string]string {
	out := make(map[string]string, len(front))
	for k, v := range front {
		s, ok := strings.CutPrefix(k, "section.")
		if !ok {
			continue
		}
		slug, ok := strings.CutSuffix(s, ".label")
		if !ok {
			continue
		}
		out[slug] = v
	}
	return out
}
