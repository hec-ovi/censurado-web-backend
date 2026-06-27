// Package adminweb is the private, server-rendered operator console for the
// censurado archive. It is an observability tool, not a CMS: it is read-only over
// article content (browse with combinable filters, inspect one article) behind a
// single-operator signed-cookie session, and is meant to run bound to localhost
// off the public internet. Every page is plain Go html/template with a small
// vendored htmx for the filter/results swap; the only trusted HTML is the
// markdown body, rendered through internal/content's sanitizer.
package adminweb

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hec-ovi/censurado-web-backend/content"
	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/media"
	"github.com/hec-ovi/censurado-web-backend/store"
)

// Config wires a Handler. Repo and the auth secrets are required; the rest take
// documented defaults.
type Config struct {
	Repo          store.Repository
	Now           func() time.Time // injectable clock; default time.Now
	TokenHash     string           // hex sha256 of the operator login token
	LoginToken    string           // optional local-rig convenience default for the login field
	SessionKey    []byte           // HMAC signing key for session+csrf (>=32 bytes)
	SessionTTL    time.Duration    // default 12h if zero
	SecureCookies bool             // set Secure on cookies (true in prod)
	PageSize      int              // default 50

	// Log backs the operator audit view (/admin/audit). When nil, that route
	// returns a friendly "not configured" page instead of an error.
	Log store.SubmissionLog
	// Regenerate triggers a static-site rebuild from /admin/regenerate. It is an
	// injected closure so adminweb stays decoupled from internal/generate: the
	// binary supplies it. When nil, the route shows a disabled state and the POST
	// returns a friendly "not configured" response (never a 500/panic).
	Regenerate func(ctx context.Context) (RegenResult, error)

	// Publish creates one article from /admin/create. It is an injected closure so
	// the admin stays a NON-WRITER of the database: the closure POSTs to the
	// publish service (the single writer) exactly as an author agent would, instead
	// of opening the store for writing. This keeps the single-writer invariant
	// intact and dogfoods the real write boundary. The binary supplies it from an
	// operator publish key. When nil, the create route shows a disabled state and
	// the POST returns a friendly "not configured" response (never a 500/panic).
	Publish func(ctx context.Context, in CreateArticleInput) (CreateArticleResult, error)

	// UploadMedia stores one uploaded image via the publish service's media endpoint
	// and returns its site-relative URL (e.g. /media/<hash>.jpg). Injected so the
	// admin stays a NON-WRITER: it proxies the bytes to the writer service rather than
	// touching disk. When nil, the create form hides the file-upload control; an
	// operator can still reference an image by URL.
	UploadMedia func(ctx context.Context, filename, contentType string, data []byte) (string, error)

	// Authors and Topics back the operator registry pages (the lists and the edit
	// prefills). They are READ directly off the store, like the article browse, since
	// a read never breaks the single-writer invariant; the concrete *sqlite.Store
	// satisfies both. When nil, the corresponding registry page shows a disabled note.
	Authors store.AuthorStore
	Topics  store.TopicStore

	// Operator performs one mutation against the operator API (the admin:write lane:
	// authors/topics CRUD and article edit/delete/restore). It is an injected closure
	// so the admin stays a NON-WRITER: it POSTs to the publish service exactly as the
	// create form does, never opening the store for writing. It returns the response
	// body on a 2xx, or an *OperatorError on a 4xx/5xx so the handler renders a
	// friendly message. When nil, the mutation controls show a disabled note and a
	// POST returns a friendly response (never a 500/panic).
	Operator func(ctx context.Context, method, path string, body any) ([]byte, error)
}

// CreateArticleInput is one operator-entered article handed to the Publish
// closure. It mirrors the publish contract's input fields; the closure marshals
// it to the write API. PublishedAt is nil when the operator left it blank (the
// server then stamps the receipt time); Metadata is nil when none was given.
type CreateArticleInput struct {
	Title       string
	Body        string
	Author      string
	Section     string
	Topics      []string
	PublishedAt *time.Time
	Metadata    map[string]any
}

// CreateArticleResult is the outcome of a successful create. Created is true for a
// brand-new article (HTTP 201) and false when the write API deduplicated or
// idempotently replayed an identical one (HTTP 200).
type CreateArticleResult struct {
	ID      string
	Slug    string
	Created bool
}

// CreateValidationError is returned by the Publish closure when the write API
// rejects the article with field-level problems (HTTP 422/400). The handler
// renders Fields inline against each input without losing the operator's typed
// values, so it is recoverable user error rather than a server fault. Any other
// error from the closure (auth, network, 5xx) is treated as a server-side problem.
type CreateValidationError struct {
	Message string
	Fields  map[string]string
}

func (e *CreateValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message != "" {
		return e.Message
	}
	return "validation failed"
}

// RegenResult is the outcome of one Regenerate call, surfaced on the regenerate
// page. It mirrors the generator's Result counts and purge list without importing
// internal/generate, so the two packages stay decoupled.
type RegenResult struct {
	Written    int
	Unchanged  int
	Deleted    int
	ScopeCount int
	Purge      []string // every root-relative URL to invalidate; never truncated
}

// Handler serves the whole /admin surface. It is safe for concurrent use.
type Handler struct {
	repo          store.Repository
	now           func() time.Time
	tokenHash     string
	loginToken    string
	sessionKey    []byte
	sessionTTL    time.Duration
	secureCookies bool
	pageSize      int
	log           store.SubmissionLog
	regenerate    func(ctx context.Context) (RegenResult, error)
	publish       func(ctx context.Context, in CreateArticleInput) (CreateArticleResult, error)
	uploadMedia   func(ctx context.Context, filename, contentType string, data []byte) (string, error)
	authors       store.AuthorStore
	topics        store.TopicStore
	operator      func(ctx context.Context, method, path string, body any) ([]byte, error)
	mux           *http.ServeMux
}

const (
	defaultSessionTTL = 12 * time.Hour
	defaultPageSize   = 50
	minSessionKeyLen  = 32
)

// New validates the config, fills defaults, and builds the route mux.
func New(cfg Config) (*Handler, error) {
	if cfg.Repo == nil {
		return nil, errors.New("adminweb: Repo is required")
	}
	if cfg.TokenHash == "" {
		return nil, errors.New("adminweb: TokenHash is required")
	}
	if len(cfg.SessionKey) < minSessionKeyLen {
		return nil, fmt.Errorf("adminweb: SessionKey must be at least %d bytes, got %d", minSessionKeyLen, len(cfg.SessionKey))
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	pageSize := cfg.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	h := &Handler{
		repo:          cfg.Repo,
		now:           now,
		tokenHash:     cfg.TokenHash,
		loginToken:    cfg.LoginToken,
		sessionKey:    append([]byte(nil), cfg.SessionKey...),
		sessionTTL:    ttl,
		secureCookies: cfg.SecureCookies,
		pageSize:      pageSize,
		log:           cfg.Log,         // optional; nil disables the audit view
		regenerate:    cfg.Regenerate,  // optional; nil disables the regenerate action
		publish:       cfg.Publish,     // optional; nil disables the create action
		uploadMedia:   cfg.UploadMedia, // optional; nil hides the upload control
		authors:       cfg.Authors,     // optional; nil disables the authors registry
		topics:        cfg.Topics,      // optional; nil disables the topics registry
		operator:      cfg.Operator,    // optional; nil disables all operator mutations
	}
	h.routes()
	return h, nil
}

// ServeHTTP dispatches through the route mux.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// routes registers the Go 1.22 method+pattern routes. Registering POST-only
// patterns (logout) makes a GET to the same path return 405 automatically.
func (h *Handler) routes() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/login", h.handleLoginForm)
	mux.HandleFunc("POST /admin/login", h.handleLoginSubmit)
	mux.HandleFunc("POST /admin/logout", h.requireSession(h.requireCSRF(h.handleLogout)))
	mux.HandleFunc("GET /admin/{$}", h.handleRoot)
	mux.HandleFunc("GET /admin/create", h.requireSession(h.handleCreateForm))
	mux.HandleFunc("POST /admin/create", h.requireSession(h.requireCSRF(h.handleCreateSubmit)))
	mux.HandleFunc("GET /admin/articles", h.requireSession(h.handleArticles))
	mux.HandleFunc("GET /admin/articles/{slug}", h.requireSession(h.handleDetail))
	// Article edit/delete: the operator mutation lane. Browse/detail stay read-only;
	// these go over the operator API so the admin never writes the store directly.
	mux.HandleFunc("GET /admin/articles/{slug}/edit", h.requireSession(h.handleArticleEdit))
	mux.HandleFunc("POST /admin/articles/{slug}", h.requireSession(h.requireCSRF(h.handleArticleUpdate)))
	mux.HandleFunc("POST /admin/articles/{slug}/delete", h.requireSession(h.requireCSRF(h.handleArticleDelete)))
	mux.HandleFunc("POST /admin/articles/{slug}/restore", h.requireSession(h.requireCSRF(h.handleArticleRestore)))
	// Managed authors registry (CRUD via the operator API).
	mux.HandleFunc("GET /admin/authors", h.requireSession(h.handleAuthors))
	mux.HandleFunc("GET /admin/authors/new", h.requireSession(h.handleAuthorNew))
	mux.HandleFunc("POST /admin/authors", h.requireSession(h.requireCSRF(h.handleAuthorCreate)))
	mux.HandleFunc("GET /admin/authors/{handle}/edit", h.requireSession(h.handleAuthorEdit))
	mux.HandleFunc("POST /admin/authors/{handle}", h.requireSession(h.requireCSRF(h.handleAuthorUpdate)))
	mux.HandleFunc("POST /admin/authors/{handle}/delete", h.requireSession(h.requireCSRF(h.handleAuthorDelete)))
	mux.HandleFunc("POST /admin/authors/{handle}/restore", h.requireSession(h.requireCSRF(h.handleAuthorRestore)))
	// Managed topics registry (CRUD via the operator API).
	mux.HandleFunc("GET /admin/topics", h.requireSession(h.handleTopics))
	mux.HandleFunc("GET /admin/topics/new", h.requireSession(h.handleTopicNew))
	mux.HandleFunc("POST /admin/topics", h.requireSession(h.requireCSRF(h.handleTopicCreate)))
	mux.HandleFunc("GET /admin/topics/{slug}/edit", h.requireSession(h.handleTopicEdit))
	mux.HandleFunc("POST /admin/topics/{slug}", h.requireSession(h.requireCSRF(h.handleTopicUpdate)))
	mux.HandleFunc("POST /admin/topics/{slug}/delete", h.requireSession(h.requireCSRF(h.handleTopicDelete)))
	mux.HandleFunc("POST /admin/topics/{slug}/restore", h.requireSession(h.requireCSRF(h.handleTopicRestore)))
	mux.HandleFunc("GET /admin/audit", h.requireSession(h.handleAudit))
	mux.HandleFunc("GET /admin/regenerate", h.requireSession(h.handleRegenerateForm))
	mux.HandleFunc("POST /admin/regenerate", h.requireSession(h.requireCSRF(h.handleRegenerateSubmit)))
	mux.HandleFunc("GET /admin/assets/{file}", h.handleAsset)
	mux.HandleFunc("GET /admin/healthz", h.handleHealthz)
	h.mux = mux
}

// requireSession guards a handler with the signed-cookie session. On failure it
// answers HX-Request callers with an HX-Redirect header (htmx swaps would
// otherwise inject the login page into a fragment) and everyone else with a 303
// to the login page. On success it stashes the session expiry in the request
// context so downstream handlers can derive the CSRF token without re-parsing.
func (h *Handler) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := h.now()
		exp, ok := h.verifySession(r, now)
		if !ok {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/admin/login")
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		// Sliding renewal: once more than half the TTL has elapsed, mint a fresh
		// cookie so an active operator is not logged out mid-session. The renewed
		// cookie carries a NEW expiry, so adopt it as the context expiry too:
		// layoutFor derives the CSRF token from the context expiry, and that token
		// must match the cookie the browser now holds, or the next POST 403s.
		if time.Unix(exp, 0).Sub(now) < h.sessionTTL/2 {
			exp = h.issueSession(w, now)
		}
		ctx := context.WithValue(r.Context(), sessionExpKey, exp)
		next(w, r.WithContext(ctx))
	}
}

// requireCSRF guards a mutating POST. It must run inside requireSession so the
// session expiry (and thus the expected token) is in context. A missing or
// mismatched csrf_token is a 403.
func (h *Handler) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		exp, ok := sessionExpFromContext(r)
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// A multipart POST (the create form's image upload) must be parsed as
		// multipart, not urlencoded: r.ParseForm does not read a multipart body, so the
		// csrf_token field would be missing and every upload would 403. Cap the body
		// first so an oversize upload is rejected before it is buffered, and the file
		// part is then available to the handler via r.FormFile.
		if isMultipartForm(r) {
			r.Body = http.MaxBytesReader(w, r.Body, maxCreateUpload)
			if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		} else if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !h.validCSRF(exp, r.PostFormValue("csrf_token")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// isMultipartForm reports whether the request carries a multipart/form-data body.
func isMultipartForm(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data")
}

const (
	// maxCreateUpload bounds the whole multipart create POST (image plus form fields).
	// It sits above the publish media endpoint's own per-image limit.
	maxCreateUpload = 12 << 20 // 12 MiB
	// maxMultipartMemory is how much of a multipart form is buffered in memory before
	// parts spill to temp files (the container's tmpfs /tmp).
	maxMultipartMemory = 1 << 20 // 1 MiB
)

func (h *Handler) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/articles", http.StatusSeeOther)
}

func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.verifySession(r, h.now()); ok {
		http.Redirect(w, r, "/admin/articles", http.StatusSeeOther)
		return
	}
	h.renderTemplate(w, http.StatusOK, loginTmpl, "login", loginView{Title: "Sign in", Token: h.loginToken})
}

func (h *Handler) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token := r.PostFormValue("token")
	// Both sides are hex sha256 (fixed 64 bytes), so the constant-time compare
	// neither leaks length nor short-circuits on the first differing byte.
	if subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(h.tokenHash)) == 1 {
		h.issueSession(w, h.now())
		http.Redirect(w, r, "/admin/articles", http.StatusSeeOther)
		return
	}
	// Generic error; never reflect the submitted token. No session cookie is set.
	h.renderTemplate(w, http.StatusOK, loginTmpl, "login", loginView{
		Title: "Sign in",
		Error: "Invalid token.",
		Token: h.loginToken,
	})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	h.clearSession(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *Handler) handleArticles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sel := h.parseSelection(r)

	total, err := h.repo.Count(ctx, h.buildFilter(sel, true))
	if err != nil {
		h.serverError(w, "count", err)
		return
	}
	arts, err := h.repo.Find(ctx, h.buildFilter(sel, false))
	if err != nil {
		h.serverError(w, "find", err)
		return
	}
	results := h.buildResults(sel, arts, total)

	// HX-Request -> swap only the results region, so hx-push-url'd filtered URLs
	// stay shareable: a full reload of the same URL hits the else branch below and
	// reproduces the identical state.
	if r.Header.Get("HX-Request") == "true" {
		h.renderPartial(w, "results", results)
		return
	}

	facets, err := h.repo.Facets(ctx)
	if err != nil {
		h.serverError(w, "facets", err)
		return
	}
	view := browseView{
		layoutData: h.layoutFor(r, "Browse articles"),
		Sections:   facetOptionsFor(facets.Sections, sel.Sections),
		Authors:    facetOptionsFor(facets.Authors, sel.Authors),
		Topics:     facetOptionsFor(facets.Topics, sel.Topics),
		Query:      sel.Query,
		From:       sel.From,
		To:         sel.To,
		Order:      sel.Order,
		Chips:      h.chipsFor(sel),
		Results:    results,
	}
	h.renderPage(w, browseTmpl, view)
}

func (h *Handler) handleDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := r.PathValue("slug")
	a, err := h.repo.BySlug(ctx, slug)
	if errors.Is(err, store.ErrNotFound) {
		h.notFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, "byslug", err)
		return
	}
	bodyHTML, err := content.RenderMarkdown(a.Body)
	if err != nil {
		h.serverError(w, "render markdown", err)
		return
	}
	view := detailView{
		layoutData:   h.layoutFor(r, a.Title),
		Slug:         a.Slug,
		ArticleTitle: a.Title,
		Author:       a.Author,
		Section:      a.Section,
		Published:    a.PublishedAt.UTC().Format("2006-01-02"),
		Topics:       a.Topics,
		BodyHTML:     template.HTML(bodyHTML),
		Meta:         metaRows(a),
		ContentHash:  a.ContentHash,
		CreatedAt:    a.CreatedAt.UTC().Format(time.RFC3339),
		EditEnabled:  h.operator != nil,
		Deleted:      a.Deleted,
	}
	h.renderPage(w, detailTmpl, view)
}

// handleAudit renders the operator audit log: recorded submissions newest first,
// with optional author and date-range filters and pagination. When no Log is
// configured it returns a friendly 503 page rather than an error. Mirrors the
// browse dual-render: an HX-Request gets only the results fragment.
func (h *Handler) handleAudit(w http.ResponseWriter, r *http.Request) {
	if h.log == nil {
		view := auditView{
			layoutData:    h.layoutFor(r, "Audit log"),
			NotConfigured: true,
		}
		h.renderTemplate(w, http.StatusServiceUnavailable, auditTmpl, "layout", view)
		return
	}
	ctx := r.Context()
	sel := h.parseAuditSelection(r)
	f := store.ListSubmissionsFilter{
		Author: sel.Author,
		From:   sel.FromTime,
		To:     sel.ToTime,
		// Over-fetch one row past the page so we can report a next page without a
		// separate count query (SubmissionLog has no Count). buildAuditResults trims.
		Limit:  h.pageSize + 1,
		Offset: (sel.Page - 1) * h.pageSize,
	}
	subs, err := h.log.ListSubmissions(ctx, f)
	if err != nil {
		h.serverError(w, "list submissions", err)
		return
	}
	results := h.buildAuditResults(sel, subs)

	if r.Header.Get("HX-Request") == "true" {
		h.renderTemplate(w, http.StatusOK, auditResultsTmpl, "audit_results", results)
		return
	}
	view := auditView{
		layoutData: h.layoutFor(r, "Audit log"),
		Author:     sel.Author,
		From:       sel.From,
		To:         sel.To,
		Results:    results,
	}
	h.renderPage(w, auditTmpl, view)
}

// handleRegenerateForm renders the regenerate page: a POST form with the CSRF
// token and a submit button when Regenerate is configured, or a disabled note
// when it is nil. The action runs on POST only.
func (h *Handler) handleRegenerateForm(w http.ResponseWriter, r *http.Request) {
	view := regenerateView{
		layoutData: h.layoutFor(r, "Regenerate"),
		Configured: h.regenerate != nil,
	}
	h.renderPage(w, regenerateTmpl, view)
}

// handleRegenerateSubmit runs the regenerate closure (under requireSession +
// requireCSRF) and renders the summary. A nil closure yields a friendly
// "not configured" page rather than a 500/panic. On error it shows the error; on
// success it shows the counts and the FULL purge URL list (never truncated).
func (h *Handler) handleRegenerateSubmit(w http.ResponseWriter, r *http.Request) {
	view := regenerateView{
		layoutData: h.layoutFor(r, "Regenerate"),
		Configured: h.regenerate != nil,
	}
	if h.regenerate == nil {
		// Feature disabled: render the full page (with its disabled note) so the
		// operator sees a clear, friendly state. Not an error.
		h.renderPage(w, regenerateTmpl, view)
		return
	}
	view.Ran = true
	res, err := h.regenerate(r.Context())
	if err != nil {
		view.Error = err.Error()
	} else {
		view.Result = &regenResultView{
			Written:    res.Written,
			Unchanged:  res.Unchanged,
			Deleted:    res.Deleted,
			ScopeCount: res.ScopeCount,
			Purge:      res.Purge,
		}
	}
	if r.Header.Get("HX-Request") == "true" {
		h.renderTemplate(w, http.StatusOK, regenResultTmpl, "regen_result", view)
		return
	}
	h.renderPage(w, regenerateTmpl, view)
}

// handleCreateForm renders the manual "new article" form. The form posts to the
// publish service (the single db writer); the admin never writes the store. When
// no Publish closure is wired the page shows a friendly disabled note instead.
func (h *Handler) handleCreateForm(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, createTmpl, h.newCreateView(r.Context(), r))
}

// handleCreateSubmit validates the operator's input, calls the Publish closure
// (which POSTs to the write API), and re-renders the page with the result. Field
// errors (admin-side parse failures or the write API's 422) are shown inline
// while preserving every typed value, so nothing is retyped on a fix. A nil
// closure yields the friendly "not configured" page rather than a 500/panic.
func (h *Handler) handleCreateSubmit(w http.ResponseWriter, r *http.Request) {
	view := h.newCreateView(r.Context(), r)
	if h.publish == nil {
		h.renderPage(w, createTmpl, view)
		return
	}
	view.Ran = true

	// requireCSRF already parsed the form. Body is preserved verbatim (markdown
	// whitespace matters); only validation trims it.
	form := createForm{
		Title:       r.PostFormValue("title"),
		Body:        r.PostFormValue("body"),
		Author:      r.PostFormValue("author"),
		Section:     r.PostFormValue("section"),
		Topics:      r.PostFormValue("topics"),
		PublishedAt: r.PostFormValue("published_at"),
		Metadata:    r.PostFormValue("metadata"),
		ImageURL:    r.PostFormValue("image_url"),
		ImageAlt:    r.PostFormValue("image_alt"),
		YouTube:     r.PostFormValue("youtube"),
	}
	view.Form = form

	// An optional uploaded image is proxied to the publish service's media endpoint,
	// which stores the bytes and returns a /media/... URL; the admin never writes disk
	// itself. An upload problem is shown inline like any other field error.
	uploadedImageURL, uploadErr := h.uploadFormImage(r)
	if uploadErr != "" {
		view.Fields = map[string]string{"image": uploadErr}
		h.renderTemplate(w, http.StatusUnprocessableEntity, createTmpl, "layout", view)
		return
	}
	// Preserve a successful upload across a field-error re-render: echo the stored URL
	// into the form's image field so a validation failure on an unrelated field does
	// not discard it (a file input cannot be repopulated by the browser, so without
	// this the operator would have to re-select and re-upload the image).
	if uploadedImageURL != "" {
		form.ImageURL = uploadedImageURL
		view.Form = form
	}

	in, fields := buildCreateInput(form, uploadedImageURL)
	if len(fields) > 0 {
		view.Fields = fields
		h.renderTemplate(w, http.StatusUnprocessableEntity, createTmpl, "layout", view)
		return
	}

	res, err := h.publish(r.Context(), in)
	if err != nil {
		var ve *CreateValidationError
		if errors.As(err, &ve) {
			view.Fields = ve.Fields
			// A field-less validation error still needs a visible message.
			if len(ve.Fields) == 0 {
				view.Error = ve.Error()
			}
			h.renderTemplate(w, http.StatusUnprocessableEntity, createTmpl, "layout", view)
			return
		}
		// Auth / network / 5xx: a server-side problem, not the operator's input.
		view.Error = err.Error()
		h.renderTemplate(w, http.StatusBadGateway, createTmpl, "layout", view)
		return
	}

	view.Result = &createResultView{
		ID:        res.ID,
		Slug:      res.Slug,
		Created:   res.Created,
		DetailURL: "/admin/articles/" + res.Slug,
	}
	// Clear the form on success so the next entry starts blank.
	view.Form = createForm{}
	h.renderPage(w, createTmpl, view)
}

// newCreateView seeds the create view with the layout, the configured flag, and
// datalist suggestions (existing sections/authors/topics) for typeahead. The
// suggestions are a convenience: a Facets error degrades to no suggestions rather
// than failing the form.
func (h *Handler) newCreateView(ctx context.Context, r *http.Request) createView {
	view := createView{
		layoutData:    h.layoutFor(r, "New article"),
		Configured:    h.publish != nil,
		UploadEnabled: h.uploadMedia != nil,
	}
	if facets, err := h.repo.Facets(ctx); err == nil {
		view.Sections = facetValues(facets.Sections)
		view.Authors = facetValues(facets.Authors)
		view.Topics = facetValues(facets.Topics)
	}
	return view
}

// maxImageBytes is the per-image cap the admin enforces before proxying an upload.
// It matches the publish media store's default limit, so a too-large image is caught
// here with a friendly field error rather than a generic gateway failure.
const maxImageBytes = 10 << 20 // 10 MiB

// uploadFormImage reads an optional uploaded image from the create form and stores it
// via the injected UploadMedia closure (which POSTs to the publish service). It
// returns the stored /media/... URL, or a field-error message for an
// oversize/unreadable/failed upload. When no file was submitted, or uploads are not
// configured, it returns empty/empty (no image, no error), so the form still works
// without an upload.
func (h *Handler) uploadFormImage(r *http.Request) (url string, fieldErr string) {
	if h.uploadMedia == nil || r.MultipartForm == nil {
		return "", ""
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		return "", "" // no file part submitted: not an error
	}
	defer file.Close()
	if header.Size > maxImageBytes {
		return "", "image is too large (max 10 MiB)"
	}
	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil {
		return "", "could not read the uploaded image"
	}
	if int64(len(data)) > maxImageBytes {
		return "", "image is too large (max 10 MiB)"
	}
	stored, err := h.uploadMedia(r.Context(), header.Filename, header.Header.Get("Content-Type"), data)
	if err != nil {
		return "", "upload failed: " + err.Error()
	}
	return stored, ""
}

func facetValues(fs []store.Facet) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Value)
	}
	return out
}

// buildCreateInput maps the raw form into a CreateArticleInput, collecting
// field-level errors for anything the admin can check before the network call:
// the four required fields, the optional published_at timestamp, and the optional
// metadata JSON. The write API re-validates everything (it is the contract), but
// catching these here gives one-pass feedback without a round trip.
func buildCreateInput(f createForm, uploadedImageURL string) (CreateArticleInput, map[string]string) {
	fields := map[string]string{}
	title := strings.TrimSpace(f.Title)
	body := strings.TrimSpace(f.Body)
	author := strings.TrimSpace(f.Author)
	section := strings.TrimSpace(f.Section)
	if title == "" {
		fields["title"] = "required"
	}
	if body == "" {
		fields["body"] = "required"
	}
	if author == "" {
		fields["author"] = "required"
	}
	if section == "" {
		fields["section"] = "required"
	}

	in := CreateArticleInput{
		Title:   title,
		Body:    body,
		Author:  author,
		Section: section,
		Topics:  splitTopics(f.Topics),
	}
	if pa := strings.TrimSpace(f.PublishedAt); pa != "" {
		if t, err := parsePublishedAt(pa); err != nil {
			fields["published_at"] = "use YYYY-MM-DD, a datetime-local value, or RFC 3339"
		} else {
			in.PublishedAt = &t
		}
	}
	if md := strings.TrimSpace(f.Metadata); md != "" {
		if m, err := parseMetadataObject(md); err != nil {
			fields["metadata"] = `must be a JSON object, e.g. {"image":"https://example/x.jpg"}`
		} else {
			in.Metadata = m
		}
	}

	// Media: the dedicated fields and the metadata JSON converge on the same keys the
	// public renderer reads (image, image_alt, youtube), so media support needs no
	// contract change. An uploaded image wins over a typed URL; both are optional. A
	// dedicated field overrides the same key in the metadata JSON. YouTube takes
	// precedence over the image at render time.
	image := strings.TrimSpace(f.ImageURL)
	if uploadedImageURL != "" {
		image = uploadedImageURL
	}
	alt := strings.TrimSpace(f.ImageAlt)
	youtube := strings.TrimSpace(f.YouTube)
	if image != "" {
		if src, _ := media.SafeMediaURL("", image); src == "" {
			fields["image_url"] = "must be a https URL or a /media path"
		}
	}
	if youtube != "" && media.YouTubeEmbedURL(youtube) == "" {
		fields["youtube"] = "not a recognized YouTube URL or video id"
	}
	if image != "" || alt != "" || youtube != "" {
		meta := in.Metadata
		if meta == nil {
			meta = map[string]any{}
		}
		if image != "" {
			meta["image"] = image
		}
		if alt != "" {
			meta["image_alt"] = alt
		}
		if youtube != "" {
			meta["youtube"] = youtube
		}
		in.Metadata = meta
	}

	if len(fields) == 0 {
		return in, nil
	}
	return in, fields
}

// splitTopics splits a comma- or newline-separated topic string into trimmed,
// non-blank tags. The write API normalizes and de-duplicates them further.
func splitTopics(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parsePublishedAt accepts the common ways an operator might type a timestamp and
// normalizes to UTC. A bare datetime-local or date is read as UTC (no zone), which
// is deterministic and matches the "UTC" hint on the form.
func parsePublishedAt(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04", // HTML datetime-local
		"2006-01-02",       // date only
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp %q", s)
}

// parseMetadataObject parses the optional metadata textarea as a single JSON
// object. Anything else (an array, a scalar, trailing junk, null) is an error so
// the operator gets a clear message instead of a confusing server rejection.
func parseMetadataObject(s string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the JSON object")
	}
	if m == nil {
		return nil, errors.New("must be a JSON object, not null")
	}
	return m, nil
}

// assetTypes is the allowlist of servable embedded assets and their MIME types.
// Only these exact names are served; the {file} route segment cannot contain a
// slash, so path traversal is structurally impossible, and the allowlist is a
// second gate.
var assetTypes = map[string]string{
	"htmx.min.js": "application/javascript; charset=utf-8",
	"admin.css":   "text/css; charset=utf-8",
	"theme.js":    "application/javascript; charset=utf-8",
}

func (h *Handler) handleAsset(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	ctype, ok := assetTypes[file]
	if !ok {
		h.notFound(w, r)
		return
	}
	body, err := assetFS.ReadFile("assets/" + file)
	if err != nil {
		h.notFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// layoutFor builds the shared layout data, deriving the CSRF token from the
// session expiry stashed by requireSession.
func (h *Handler) layoutFor(r *http.Request, title string) layoutData {
	ld := layoutData{Title: title}
	if exp, ok := sessionExpFromContext(r); ok {
		ld.CSRFToken = h.csrfToken(exp)
	}
	return ld
}

func (h *Handler) serverError(w http.ResponseWriter, stage string, err error) {
	// Detail goes to stderr only; the client gets a generic body.
	fmt.Println("adminweb:", stage, "error:", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<title>Not found</title><link rel="stylesheet" href="/admin/assets/admin.css"></head>` +
		`<body class="admin-body"><main class="admin-main"><h1 class="admin-h1">404</h1>` +
		`<p>No such article. <a href="/admin/articles">Back to browse</a>.</p></main></body></html>`))
}

// selection is the parsed, validated filter request. Raw date strings are kept
// for re-populating the form and chips; the parsed times drive the store filter.
type selection struct {
	Sections []string
	Authors  []string
	Topics   []string
	Query    string
	From     string // raw YYYY-MM-DD echoed into the input (only set when valid)
	To       string
	FromTime time.Time
	ToTime   time.Time
	Order    string // "newest" (default) or "oldest"
	Page     int    // 1-based
}

const dateLayout = "2006-01-02"

// parseSelection reads the filter query params. Invalid dates and pages are
// ignored gracefully (no error page; just no constraint / page 1).
func (h *Handler) parseSelection(r *http.Request) selection {
	q := r.URL.Query()
	sel := selection{
		Sections: nonBlankStrings(q["section"]),
		Authors:  nonBlankStrings(q["author"]),
		Topics:   nonBlankStrings(q["topic"]),
		Query:    strings.TrimSpace(q.Get("q")),
		Order:    "newest",
		Page:     1,
	}
	if q.Get("order") == "oldest" {
		sel.Order = "oldest"
	}
	if t, err := time.Parse(dateLayout, strings.TrimSpace(q.Get("from"))); err == nil {
		sel.FromTime = t.UTC()
		sel.From = t.UTC().Format(dateLayout)
	}
	if t, err := time.Parse(dateLayout, strings.TrimSpace(q.Get("to"))); err == nil {
		// 'to' is an INCLUSIVE day: the store's To is an exclusive upper bound, so
		// the whole of the named day is included by adding 24h.
		sel.ToTime = t.UTC().Add(24 * time.Hour)
		sel.To = t.UTC().Format(dateLayout)
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 1 {
		sel.Page = n
	}
	return sel
}

// buildFilter maps a selection onto a store.Filter. forCount omits paging so the
// total used for pagination ignores Limit/Offset.
func (h *Handler) buildFilter(sel selection, forCount bool) store.Filter {
	f := store.Filter{
		Sections: sel.Sections,
		Authors:  sel.Authors,
		Topics:   sel.Topics,
		Query:    sel.Query,
		From:     sel.FromTime,
		To:       sel.ToTime,
		Order:    store.NewestFirst,
	}
	if sel.Order == "oldest" {
		f.Order = store.OldestFirst
	}
	if !forCount {
		f.Limit = h.pageSize
		f.Offset = (sel.Page - 1) * h.pageSize
	}
	return f
}

// values renders the selection back into url.Values (without page) for building
// shareable links.
func (sel selection) values() url.Values {
	v := url.Values{}
	for _, s := range sel.Sections {
		v.Add("section", s)
	}
	for _, a := range sel.Authors {
		v.Add("author", a)
	}
	for _, t := range sel.Topics {
		v.Add("topic", t)
	}
	if sel.Query != "" {
		v.Set("q", sel.Query)
	}
	if sel.From != "" {
		v.Set("from", sel.From)
	}
	if sel.To != "" {
		v.Set("to", sel.To)
	}
	if sel.Order == "oldest" {
		v.Set("order", "oldest")
	}
	return v
}

func articlesURL(v url.Values) string {
	if len(v) == 0 {
		return "/admin/articles"
	}
	return "/admin/articles?" + v.Encode()
}

func (h *Handler) buildResults(sel selection, arts []domain.Article, total int) resultsView {
	rows := make([]articleRow, 0, len(arts))
	for _, a := range arts {
		rows = append(rows, articleRow{
			Title:     a.Title,
			Slug:      a.Slug,
			DetailURL: "/admin/articles/" + a.Slug,
			Author:    a.Author,
			Section:   a.Section,
			Topics:    a.Topics,
			Published: a.PublishedAt.UTC().Format(dateLayout),
		})
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + h.pageSize - 1) / h.pageSize
	}
	rv := resultsView{
		Rows:       rows,
		Total:      total,
		Shown:      len(rows),
		Page:       sel.Page,
		PageSize:   h.pageSize,
		TotalPages: totalPages,
		HasPrev:    sel.Page > 1,
		HasNext:    sel.Page < totalPages,
	}
	if len(rows) > 0 {
		rv.FirstIndex = (sel.Page-1)*h.pageSize + 1
		rv.LastIndex = rv.FirstIndex + len(rows) - 1
	}
	if rv.HasPrev {
		v := sel.values()
		v.Set("page", strconv.Itoa(sel.Page-1))
		rv.PrevURL = articlesURL(v)
	}
	if rv.HasNext {
		v := sel.values()
		v.Set("page", strconv.Itoa(sel.Page+1))
		rv.NextURL = articlesURL(v)
	}
	return rv
}

// chipsFor renders one removable chip per active constraint. Each RemoveURL is
// the current selection minus that single value, with paging reset.
func (h *Handler) chipsFor(sel selection) []chip {
	var chips []chip
	add := func(axis, value string, without url.Values) {
		chips = append(chips, chip{Axis: axis, Value: value, RemoveURL: articlesURL(without)})
	}
	for _, s := range sel.Sections {
		add("section", s, removeValue(sel.values(), "section", s))
	}
	for _, a := range sel.Authors {
		add("author", a, removeValue(sel.values(), "author", a))
	}
	for _, t := range sel.Topics {
		add("topic", t, removeValue(sel.values(), "topic", t))
	}
	if sel.Query != "" {
		v := sel.values()
		v.Del("q")
		add("search", sel.Query, v)
	}
	if !sel.FromTime.IsZero() {
		v := sel.values()
		v.Del("from")
		add("from", sel.From, v)
	}
	if !sel.ToTime.IsZero() {
		v := sel.values()
		v.Del("to")
		add("to", sel.To, v)
	}
	return chips
}

// removeValue returns a copy of v with one occurrence of val removed from key.
func removeValue(v url.Values, key, val string) url.Values {
	kept := v[key][:0:0]
	removed := false
	for _, x := range v[key] {
		if !removed && x == val {
			removed = true
			continue
		}
		kept = append(kept, x)
	}
	if len(kept) == 0 {
		v.Del(key)
	} else {
		v[key] = kept
	}
	return v
}

func facetOptionsFor(facets []store.Facet, selected []string) []facetOption {
	out := make([]facetOption, 0, len(facets))
	for _, f := range facets {
		out = append(out, facetOption{
			Value:    f.Value,
			Count:    f.Count,
			Selected: containsString(selected, f.Value),
		})
	}
	return out
}

// auditSelection is the parsed, validated audit-log filter request. Raw date
// strings are kept to re-populate the form; the parsed times drive the store
// filter. The date semantics match browse: 'to' is an inclusive day (the store's
// To is the exclusive upper bound, so the whole named day is included by +24h).
type auditSelection struct {
	Author   string
	From     string // raw YYYY-MM-DD echoed into the input (only set when valid)
	To       string
	FromTime time.Time
	ToTime   time.Time
	Page     int // 1-based
}

// parseAuditSelection reads the audit filter query params. Invalid dates and
// pages are ignored gracefully (no error page; just no constraint / page 1).
func (h *Handler) parseAuditSelection(r *http.Request) auditSelection {
	q := r.URL.Query()
	sel := auditSelection{
		Author: strings.TrimSpace(q.Get("author")),
		Page:   1,
	}
	if t, err := time.Parse(dateLayout, strings.TrimSpace(q.Get("from"))); err == nil {
		sel.FromTime = t.UTC()
		sel.From = t.UTC().Format(dateLayout)
	}
	if t, err := time.Parse(dateLayout, strings.TrimSpace(q.Get("to"))); err == nil {
		sel.ToTime = t.UTC().Add(24 * time.Hour)
		sel.To = t.UTC().Format(dateLayout)
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 1 {
		sel.Page = n
	}
	return sel
}

// values renders the audit selection back into url.Values (without page) for
// building shareable, filter-preserving pagination links.
func (sel auditSelection) values() url.Values {
	v := url.Values{}
	if sel.Author != "" {
		v.Set("author", sel.Author)
	}
	if sel.From != "" {
		v.Set("from", sel.From)
	}
	if sel.To != "" {
		v.Set("to", sel.To)
	}
	return v
}

func auditURL(v url.Values) string {
	if len(v) == 0 {
		return "/admin/audit"
	}
	return "/admin/audit?" + v.Encode()
}

// buildAuditResults projects the over-fetched submissions into the results view.
// The extra row past the page (if present) only signals a next page; it is
// trimmed off before rendering.
func (h *Handler) buildAuditResults(sel auditSelection, subs []store.Submission) auditResultsView {
	hasNext := len(subs) > h.pageSize
	if hasNext {
		subs = subs[:h.pageSize]
	}
	rows := make([]auditRow, 0, len(subs))
	for _, s := range subs {
		rows = append(rows, auditRow{
			CreatedAt:      s.CreatedAt.UTC().Format(time.RFC3339),
			Author:         s.Author,
			Slug:           s.Slug,
			DetailURL:      "/admin/articles/" + s.Slug,
			ContentHash:    s.ContentHash,
			Scopes:         s.Scopes,
			IdempotencyKey: s.IdempotencyKey,
		})
	}
	rv := auditResultsView{
		Rows:     rows,
		Shown:    len(rows),
		Page:     sel.Page,
		PageSize: h.pageSize,
		HasPrev:  sel.Page > 1,
		HasNext:  hasNext,
	}
	if rv.HasPrev {
		v := sel.values()
		v.Set("page", strconv.Itoa(sel.Page-1))
		rv.PrevURL = auditURL(v)
	}
	if rv.HasNext {
		v := sel.values()
		v.Set("page", strconv.Itoa(sel.Page+1))
		rv.NextURL = auditURL(v)
	}
	return rv
}

// metaRows projects an article's open-ended metadata into stable key/value rows,
// sorted by key for a deterministic table.
func metaRows(a domain.Article) []kv {
	if len(a.Metadata) == 0 {
		return nil
	}
	keys := make([]string, 0, len(a.Metadata))
	for k := range a.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([]kv, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, kv{Key: k, Value: fmt.Sprintf("%v", a.Metadata[k])})
	}
	return rows
}

func nonBlankStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
