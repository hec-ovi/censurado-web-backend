package adminweb

import (
	"bytes"
	"embed"
	"html/template"
	"log"
	"net/http"
)

//go:embed templates/*.tmpl
var tmplFS embed.FS

//go:embed assets/*
var assetFS embed.FS

// Template sets are parsed once at init. Each page kind that defines "content"
// gets its own set paired with the shared layout, because two templates cannot
// both define "content" in one set (same reason the generator splits its sets).
// login renders its own standalone shell (no nav/logout), so it is parsed alone.
// results is parsed both inside the browse set (the full page embeds it) and on
// its own, so the HX-Request partial can render the fragment in isolation.
var (
	loginTmpl   = template.Must(template.New("login").ParseFS(tmplFS, "templates/login.tmpl"))
	browseTmpl  = template.Must(template.New("browse").ParseFS(tmplFS, "templates/layout.tmpl", "templates/browse.tmpl", "templates/results.tmpl"))
	detailTmpl  = template.Must(template.New("detail").ParseFS(tmplFS, "templates/layout.tmpl", "templates/detail.tmpl"))
	resultsTmpl = template.Must(template.New("results").ParseFS(tmplFS, "templates/results.tmpl"))
	// audit embeds its results fragment, which is also parsed standalone so the
	// HX-Request partial renders in isolation (same split as browse/results).
	auditTmpl        = template.Must(template.New("audit").ParseFS(tmplFS, "templates/layout.tmpl", "templates/audit.tmpl", "templates/audit_results.tmpl"))
	auditResultsTmpl = template.Must(template.New("audit_results").ParseFS(tmplFS, "templates/audit_results.tmpl"))
	// regenerate embeds its result fragment, also parsed standalone for the HX
	// POST swap that updates only the result region.
	regenerateTmpl  = template.Must(template.New("regenerate").ParseFS(tmplFS, "templates/layout.tmpl", "templates/regenerate.tmpl"))
	regenResultTmpl = template.Must(template.New("regen_result").ParseFS(tmplFS, "templates/regenerate.tmpl"))
	// create is a plain full-page form: the POST re-renders the whole page so field
	// errors can repopulate inputs, so no standalone fragment is needed.
	createTmpl = template.Must(template.New("create").ParseFS(tmplFS, "templates/layout.tmpl", "templates/create.tmpl"))
	// The operator registry pages (managed authors/topics) and the article edit form.
	// Each is a plain full-page form like create: the POST redirects on success and
	// re-renders with an inline error on failure.
	authorsTmpl     = template.Must(template.New("authors").ParseFS(tmplFS, "templates/layout.tmpl", "templates/authors.tmpl"))
	authorFormTmpl  = template.Must(template.New("author_form").ParseFS(tmplFS, "templates/layout.tmpl", "templates/author_form.tmpl"))
	topicsTmpl      = template.Must(template.New("topics").ParseFS(tmplFS, "templates/layout.tmpl", "templates/topics.tmpl"))
	topicFormTmpl   = template.Must(template.New("topic_form").ParseFS(tmplFS, "templates/layout.tmpl", "templates/topic_form.tmpl"))
	articleEditTmpl = template.Must(template.New("article_edit").ParseFS(tmplFS, "templates/layout.tmpl", "templates/article_edit.tmpl"))
)

// authorRow is one managed author in the registry list.
type authorRow struct {
	Handle  string
	Name    string
	Bio     string
	Avatar  string
	Deleted bool
	EditURL string
}

// authorsView is the managed-authors list. Configured gates the page vs a disabled
// note; ShowDeleted toggles tombstoned rows; ToggleURL flips that view.
type authorsView struct {
	layoutData
	Configured  bool
	ShowDeleted bool
	ToggleURL   string
	Rows        []authorRow
}

// authorFormView is the new/edit author form. Editing makes the handle read-only
// (the key never changes on edit); ActionURL is the POST target; Error carries an
// operator-API failure shown inline without losing the typed values.
type authorFormView struct {
	layoutData
	Configured bool
	Editing    bool
	ActionURL  string
	Handle     string
	Name       string
	Bio        string
	Avatar     string
	Error      string
}

// topicRow is one managed topic in the registry list.
type topicRow struct {
	Slug        string
	Label       string
	Description string
	Deleted     bool
	EditURL     string
}

// topicsView is the managed-topics list.
type topicsView struct {
	layoutData
	Configured  bool
	ShowDeleted bool
	ToggleURL   string
	Rows        []topicRow
}

// topicFormView is the new/edit topic form (slug read-only on edit).
type topicFormView struct {
	layoutData
	Configured  bool
	Editing     bool
	ActionURL   string
	Slug        string
	Label       string
	Description string
	Error       string
}

// articleEditView is the article edit form. The slug is fixed (the permalink never
// moves); Fields carries per-input validation errors and Error a top-level message
// (e.g. an edit_conflict), both shown inline while preserving the typed values.
type articleEditView struct {
	layoutData
	Configured  bool
	Slug        string
	Title       string
	Body        string
	Author      string
	Section     string
	Topics      string
	PublishedAt string
	Metadata    string
	Deleted     bool
	Error       string
	Fields      map[string]string
}

// layoutData is embedded by every full-page view so the shared layout can read
// the page title and the per-session CSRF token off the same dot.
type layoutData struct {
	Title     string
	CSRFToken string
}

// loginView models the standalone login page.
type loginView struct {
	Title string
	Error string // generic message; never echoes the submitted token
}

// facetOption is one selectable filter value with its article count and whether
// the current request has it selected.
type facetOption struct {
	Value    string
	Count    int
	Selected bool
}

// chip is an active filter shown as a removable tag. RemoveURL points back to
// /admin/articles with this one value dropped (and paging reset).
type chip struct {
	Axis      string // human label: "section" / "author" / "topic" / "search" / "from" / "to"
	Value     string
	RemoveURL string
}

// browseView is the full browse page: the filter controls (pre-filled from the
// request), the active-filter chips, and the embedded results region.
type browseView struct {
	layoutData
	Sections []facetOption
	Authors  []facetOption
	Topics   []facetOption
	Query    string
	From     string
	To       string
	Order    string // "newest" or "oldest"
	Chips    []chip
	Results  resultsView
}

// articleRow is one row in the results table.
type articleRow struct {
	Title     string
	Slug      string
	DetailURL string
	Author    string
	Section   string
	Topics    []string
	Published string // YYYY-MM-DD
}

// resultsView is the results table plus its count and pagination. It is rendered
// both inside the browse page and as the standalone HX fragment.
type resultsView struct {
	Rows       []articleRow
	Total      int // total matching articles (ignores paging)
	Shown      int // rows on this page
	Page       int
	PageSize   int
	TotalPages int
	FirstIndex int // 1-based index of the first row in the full result set
	LastIndex  int
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
}

// kv is one metadata key/value row on the detail page.
type kv struct {
	Key   string
	Value string
}

// detailView is a single article's full page.
type detailView struct {
	layoutData
	Slug         string
	ArticleTitle string
	Author       string
	Section      string
	Published    string
	Topics       []string
	BodyHTML     template.HTML // sanitized markdown render; the ONLY trusted HTML
	Meta         []kv
	ContentHash  string
	CreatedAt    string
	EditEnabled  bool // the operator mutation lane is wired (edit/delete buttons shown)
	Deleted      bool // soft-deleted: show Restore instead of Delete
}

// auditRow is one submission row in the audit-log table. Every field is a plain
// string or string slice rendered through html/template auto-escaping; none is
// trusted HTML.
type auditRow struct {
	CreatedAt      string // RFC3339 UTC
	Author         string
	Slug           string
	DetailURL      string
	ContentHash    string
	Scopes         []string
	IdempotencyKey string
}

// auditResultsView is the audit table plus pagination. It is rendered both inside
// the audit page and as the standalone HX fragment. There is no Total: paging is
// driven by an over-fetched extra row (HasNext), since SubmissionLog has no Count.
type auditResultsView struct {
	Rows     []auditRow
	Shown    int
	Page     int
	PageSize int
	HasPrev  bool
	HasNext  bool
	PrevURL  string
	NextURL  string
}

// auditView is the full audit page: the filter controls (pre-filled) and the
// embedded results region. NotConfigured swaps the whole body for a friendly note
// when no SubmissionLog is wired.
type auditView struct {
	layoutData
	NotConfigured bool
	Author        string
	From          string
	To            string
	Results       auditResultsView
}

// regenResultView is the summary of one regenerate run. Purge is shown in full
// (every URL); it is never truncated.
type regenResultView struct {
	Written    int
	Unchanged  int
	Deleted    int
	ScopeCount int
	Purge      []string
}

// regenerateView is the regenerate page. Configured gates the button vs the
// disabled note. Ran marks that a POST happened; Error/Result carry the outcome.
type regenerateView struct {
	layoutData
	Configured bool
	Ran        bool
	Error      string
	Result     *regenResultView
}

// createForm holds the raw, operator-typed values, echoed back verbatim so a
// validation error never makes the operator retype. Topics/PublishedAt/Metadata
// are the raw strings as entered; the handler parses them into CreateArticleInput.
type createForm struct {
	Title       string
	Body        string
	Author      string
	Section     string
	Topics      string
	PublishedAt string
	Metadata    string
	ImageURL    string
	ImageAlt    string
	YouTube     string
}

// createResultView is the success summary for one published article. Created is
// true for a brand-new write (201) and false for an idempotent/deduplicated 200.
type createResultView struct {
	ID        string
	Slug      string
	Created   bool
	DetailURL string
}

// createView is the manual create page. Configured gates the form vs the disabled
// note. Fields carries per-input validation messages (nil when clean); Error is a
// top-level message for a config/network/server failure; Result is set on success.
// Sections/Authors/Topics seed datalist typeahead from existing facet values.
type createView struct {
	layoutData
	Configured    bool
	UploadEnabled bool // whether the image-upload control is shown (UploadMedia wired)
	Sections      []string
	Authors       []string
	Topics        []string
	Form          createForm
	Ran           bool
	Error         string
	Fields        map[string]string
	Result        *createResultView
}

// renderTemplate executes name from t into a buffer first, so a template error
// becomes a clean 500 with a generic body rather than a half-written page. The
// detailed error is logged to stderr only.
func (h *Handler) renderTemplate(w http.ResponseWriter, status int, t *template.Template, name string, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("adminweb: render %s: %v", name, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// renderPage writes a full layout-wrapped page.
func (h *Handler) renderPage(w http.ResponseWriter, t *template.Template, data any) {
	h.renderTemplate(w, http.StatusOK, t, "layout", data)
}

// renderPartial writes only the named fragment (the HX-Request results region).
func (h *Handler) renderPartial(w http.ResponseWriter, name string, data any) {
	h.renderTemplate(w, http.StatusOK, resultsTmpl, name, data)
}
