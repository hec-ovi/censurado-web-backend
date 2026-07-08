package publish

import (
	"net/http"
	"strings"

	"github.com/hec-ovi/censurado-web-backend/store"
)

// defaultTextLang is the base language every UI-string key is authored in; a read or
// write that omits lang targets it.
const defaultTextLang = "en"

// textEntryJSON is the wire shape of one UI-string catalog entry. Metadata coalesces
// to {} so the wire form never carries a null.
type textEntryJSON struct {
	Key       string         `json:"key"`
	Lang      string         `json:"lang"`
	Value     string         `json:"value"`
	Metadata  map[string]any `json:"metadata"`
	Deleted   bool           `json:"deleted"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
}

// textResponse is the GET wrapper: the scope and language read, and the entries.
type textResponse struct {
	Scope   string          `json:"scope"`
	Lang    string          `json:"lang"`
	Entries []textEntryJSON `json:"entries"`
}

func toTextJSON(e store.TextEntry) textEntryJSON {
	return textEntryJSON{
		Key: e.Key, Lang: e.Lang, Value: e.Value,
		Metadata: coalesceMeta(e.Metadata), Deleted: e.Deleted,
		CreatedAt: rfc3339(e.CreatedAt), UpdatedAt: rfc3339(e.UpdatedAt),
	}
}

// serveText answers a GET for a text scope: the catalog for ?lang (default the base
// language), ?include_deleted=true to include tombstones. Any valid key may read.
func (rh *ReadHandler) serveText(w http.ResponseWriter, r *http.Request, scope store.TextScope) {
	if !rh.authn(w, r) {
		return
	}
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if lang == "" {
		lang = defaultTextLang
	}
	entries, err := rh.store.ListText(r.Context(), scope, lang, includeDeleted(r))
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	out := textResponse{Scope: string(scope), Lang: lang, Entries: make([]textEntryJSON, 0, len(entries))}
	for _, e := range entries {
		out.Entries = append(out.Entries, toTextJSON(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// ServeFrontendText answers GET /frontend-text with the reader-facing site string
// catalog for a language.
func (rh *ReadHandler) ServeFrontendText(w http.ResponseWriter, r *http.Request) {
	rh.serveText(w, r, store.ScopeFrontend)
}

// ServePanelText answers GET /panel-text with the operator-facing admin string catalog
// for a language.
func (rh *ReadHandler) ServePanelText(w http.ResponseWriter, r *http.Request) {
	rh.serveText(w, r, store.ScopePanel)
}

// ServeEditorialText answers GET /editorial-text with the newsroom's per-language
// editorial config (lexicon, orthography, slop phrases, exemplars, bot directive) for a
// language. Unlike the UI catalogs the base authored language is Spanish, so a bare read
// still defaults to ?lang=en and simply returns an empty catalog until a language is set.
func (rh *ReadHandler) ServeEditorialText(w http.ResponseWriter, r *http.Request) {
	rh.serveText(w, r, store.ScopeEditorial)
}

// textInput is the POST body for a text upsert. lang defaults to the base language.
type textInput struct {
	Key      string         `json:"key"`
	Lang     string         `json:"lang"`
	Value    string         `json:"value"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// upsertText answers a POST for a text scope: create or replace one (key, lang) entry.
// key is required (400 otherwise); lang defaults to the base language. admin:write.
func (oh *OperatorHandler) upsertText(w http.ResponseWriter, r *http.Request, scope store.TextScope) {
	if !oh.authz(w, r) {
		return
	}
	var in textInput
	if !decodeStrict(w, r, &in) {
		return
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "key is required"})
		return
	}
	lang := strings.TrimSpace(in.Lang)
	if lang == "" {
		lang = defaultTextLang
	}
	e, err := oh.store.UpsertText(r.Context(), scope, store.TextEntry{
		Key: key, Lang: lang, Value: in.Value, Metadata: in.Metadata,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, toTextJSON(e))
}

// ServeUpsertFrontendText answers POST /frontend-text: upsert a reader-facing string.
func (oh *OperatorHandler) ServeUpsertFrontendText(w http.ResponseWriter, r *http.Request) {
	oh.upsertText(w, r, store.ScopeFrontend)
}

// ServeUpsertPanelText answers POST /panel-text: upsert an operator-facing string.
func (oh *OperatorHandler) ServeUpsertPanelText(w http.ResponseWriter, r *http.Request) {
	oh.upsertText(w, r, store.ScopePanel)
}

// ServeUpsertEditorialText answers POST /editorial-text: upsert one per-language
// editorial-config entry (an anchor row or the bot directive).
func (oh *OperatorHandler) ServeUpsertEditorialText(w http.ResponseWriter, r *http.Request) {
	oh.upsertText(w, r, store.ScopeEditorial)
}
