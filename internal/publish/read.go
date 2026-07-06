package publish

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hec-ovi/censurado-web-backend/store"
)

// ReadStore is the read surface the JSON read API needs: the article Repository
// plus the operator-owned author, topic, portada, and source registries. The
// SQLite store satisfies it, so the same concrete store backs the write path and
// these reads.
type ReadStore interface {
	store.Repository
	store.AuthorStore
	store.TopicStore
	store.PortadaStore
	store.SourceStore
}

// ReadHandler serves the authenticated JSON read API: GET /authors, /topics,
// /articles, and /articles/{slug}. It never writes. Any VALID key may read: a read
// needs a good bearer token but no particular scope, so an agent's write key and a
// read-only operator key both work. The list endpoints are deliberately light
// (no body) and a single article carries everything including the body.
type ReadHandler struct {
	store ReadStore
	auth  Authenticator
}

// NewReadHandler wires the read API over a store and the same authenticator the
// write path uses.
func NewReadHandler(s ReadStore, auth Authenticator) *ReadHandler {
	return &ReadHandler{store: s, auth: auth}
}

// authn requires a valid bearer token but NO scope (reads are open to any
// authenticated key). It writes the problem response and returns false on failure,
// mirroring the write path's missing_token/invalid_token shape.
func (rh *ReadHandler) authn(w http.ResponseWriter, r *http.Request) bool {
	_, ok := resolveIdentity(rh.auth, w, r)
	return ok
}

// authorJSON is the wire shape of a managed author. Style is the private voice/writing
// prompt: it is served here (the admin panel edits it) but the public site never
// renders it. Sources is the author's attached-source slug set, hydrated from the join.
type authorJSON struct {
	Handle    string         `json:"handle"`
	Name      string         `json:"name"`
	Bio       string         `json:"bio"`
	Avatar    string         `json:"avatar"`
	Gender    string         `json:"gender"`
	About     string         `json:"about"`
	Style     string         `json:"style"`
	Topics    []string       `json:"topics"`
	Sources   []string       `json:"sources"`
	Metadata  map[string]any `json:"metadata"`
	Deleted   bool           `json:"deleted"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
}

type authorsResponse struct {
	Authors []authorJSON `json:"authors"`
}

// ServeAuthors answers GET /authors with the managed author registry. ?include_deleted=true
// includes tombstoned authors (default excludes them).
func (rh *ReadHandler) ServeAuthors(w http.ResponseWriter, r *http.Request) {
	if !rh.authn(w, r) {
		return
	}
	authors, err := rh.store.ListAuthors(r.Context(), includeDeleted(r))
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	out := authorsResponse{Authors: make([]authorJSON, 0, len(authors))}
	for _, a := range authors {
		out.Authors = append(out.Authors, toAuthorJSON(a))
	}
	writeJSON(w, http.StatusOK, out)
}

type topicJSON struct {
	Slug        string         `json:"slug"`
	Label       string         `json:"label"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata"`
	Deleted     bool           `json:"deleted"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

type topicsResponse struct {
	Topics []topicJSON `json:"topics"`
}

// ServeTopics answers GET /topics with the managed topic registry. ?include_deleted=true
// includes tombstoned topics.
func (rh *ReadHandler) ServeTopics(w http.ResponseWriter, r *http.Request) {
	if !rh.authn(w, r) {
		return
	}
	topics, err := rh.store.ListTopics(r.Context(), includeDeleted(r))
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	out := topicsResponse{Topics: make([]topicJSON, 0, len(topics))}
	for _, tp := range topics {
		out.Topics = append(out.Topics, topicJSON{
			Slug: tp.Slug, Label: tp.Label, Description: tp.Description,
			Metadata: coalesceMeta(tp.Metadata), Deleted: tp.Deleted,
			CreatedAt: rfc3339(tp.CreatedAt), UpdatedAt: rfc3339(tp.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type portadaEntryJSON struct {
	Slug string `json:"slug"`
	Role string `json:"role"`
}

type portadaJSON struct {
	Date        string             `json:"date"`
	Entries     []portadaEntryJSON `json:"entries"`
	Recomendado []string           `json:"recomendado"`
	Deleted     bool               `json:"deleted"`
	CreatedAt   string             `json:"created_at"`
	UpdatedAt   string             `json:"updated_at"`
}

type portadasResponse struct {
	Portadas []portadaJSON `json:"portadas"`
}

// toPortadaJSON maps a stored day plan to its JSON shape, coalescing Entries and
// Recomendado to [] (never null) so the wire form is always a JSON array.
func toPortadaJSON(p store.PortadaDay) portadaJSON {
	entries := make([]portadaEntryJSON, 0, len(p.Entries))
	for _, e := range p.Entries {
		entries = append(entries, portadaEntryJSON{Slug: e.Slug, Role: e.Role})
	}
	return portadaJSON{
		Date: p.Date, Entries: entries, Recomendado: coalesceTopics(p.Recomendado),
		Deleted: p.Deleted, CreatedAt: rfc3339(p.CreatedAt), UpdatedAt: rfc3339(p.UpdatedAt),
	}
}

// ServePortadas answers GET /portadas with the managed front-page registry.
// ?include_deleted=true includes tombstoned days (default excludes them).
func (rh *ReadHandler) ServePortadas(w http.ResponseWriter, r *http.Request) {
	if !rh.authn(w, r) {
		return
	}
	portadas, err := rh.store.ListPortadas(r.Context(), includeDeleted(r))
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	out := portadasResponse{Portadas: make([]portadaJSON, 0, len(portadas))}
	for _, p := range portadas {
		out.Portadas = append(out.Portadas, toPortadaJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

type sourceJSON struct {
	Slug           string         `json:"slug"`
	Domain         string         `json:"domain"`
	Homepage       string         `json:"homepage"`
	Description    string         `json:"description"`
	FeedURLs       []string       `json:"feed_urls"`
	FeedType       string         `json:"feed_type"`
	Language       string         `json:"language"`
	OwnershipGroup string         `json:"ownership_group"`
	Lean           string         `json:"lean"`
	Enabled        bool           `json:"enabled"`
	Status         string         `json:"status"`
	LastChecked    string         `json:"last_checked"`
	LastOK         string         `json:"last_ok"`
	Metadata       map[string]any `json:"metadata"`
	Deleted        bool           `json:"deleted"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

type sourcesResponse struct {
	Sources []sourceJSON `json:"sources"`
}

// toSourceJSON maps a stored source to its JSON shape, coalescing FeedURLs to [] and
// Metadata to {} so the wire form never carries a null.
func toSourceJSON(s store.Source) sourceJSON {
	return sourceJSON{
		Slug: s.Slug, Domain: s.Domain, Homepage: s.Homepage, Description: s.Description,
		FeedURLs: coalesceTopics(s.FeedURLs), FeedType: s.FeedType, Language: s.Language,
		OwnershipGroup: s.OwnershipGroup, Lean: s.Lean, Enabled: s.Enabled, Status: s.Status,
		LastChecked: s.LastChecked, LastOK: s.LastOK, Metadata: coalesceMeta(s.Metadata),
		Deleted: s.Deleted, CreatedAt: rfc3339(s.CreatedAt), UpdatedAt: rfc3339(s.UpdatedAt),
	}
}

// ServeSources answers GET /sources with the managed source registry.
// ?include_deleted=true includes tombstoned sources (default excludes them).
func (rh *ReadHandler) ServeSources(w http.ResponseWriter, r *http.Request) {
	if !rh.authn(w, r) {
		return
	}
	sources, err := rh.store.ListSources(r.Context(), includeDeleted(r))
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	out := sourcesResponse{Sources: make([]sourceJSON, 0, len(sources))}
	for _, s := range sources {
		out.Sources = append(out.Sources, toSourceJSON(s))
	}
	writeJSON(w, http.StatusOK, out)
}

type authorSourcesResponse struct {
	Handle  string   `json:"handle"`
	Sources []string `json:"sources"`
}

// ServeAuthorSources answers GET /authors/{handle}/sources with the author's attached
// source slugs, slug-sorted. A missing author is a 404 (so a typo does not silently
// read as "no sources").
func (rh *ReadHandler) ServeAuthorSources(w http.ResponseWriter, r *http.Request) {
	if !rh.authn(w, r) {
		return
	}
	handle := r.PathValue("handle")
	_, found, err := rh.store.AuthorByHandle(r.Context(), handle)
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	if !found {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	slugs, err := rh.store.AuthorSources(r.Context(), handle)
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, authorSourcesResponse{Handle: handle, Sources: coalesceTopics(slugs)})
}

// articleListItem is the light list shape: everything an index needs EXCEPT the
// body, so a listing of many articles stays small. HasMedia is derived from the body
// and metadata (which the store loads even though the body is dropped from this
// shape), so a listing consumer knows whether a piece carries an image or video
// without pulling the full article.
type articleListItem struct {
	Slug        string         `json:"slug"`
	Title       string         `json:"title"`
	Section     string         `json:"section"`
	Author      string         `json:"author"`
	PublishedAt string         `json:"published_at"`
	Topics      []string       `json:"topics"`
	Metadata    map[string]any `json:"metadata"`
	HasMedia    bool           `json:"has_media"`
	CardType    string         `json:"card_type"`
	Deleted     bool           `json:"deleted"`
	ContentHash string         `json:"content_hash"`
}

type articlesResponse struct {
	Articles []articleListItem `json:"articles"`
	Total    int               `json:"total"`
}

// ServeArticles answers GET /articles with a filtered, paged, body-less list and a
// Total that counts all matches (ignoring paging). Query params map to store.Filter:
// section, author, topic, q, from, to (RFC3339), limit, offset, order (oldest|newest),
// include_deleted. A malformed from/to/limit/offset is a 400.
func (rh *ReadHandler) ServeArticles(w http.ResponseWriter, r *http.Request) {
	if !rh.authn(w, r) {
		return
	}
	f, ok := parseFilter(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	arts, err := rh.store.Find(ctx, f)
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	total, err := rh.store.Count(ctx, f)
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	out := articlesResponse{Articles: make([]articleListItem, 0, len(arts)), Total: total}
	for _, a := range arts {
		out.Articles = append(out.Articles, articleListItem{
			Slug: a.Slug, Title: a.Title, Section: a.Section, Author: a.Author,
			PublishedAt: rfc3339(a.PublishedAt), Topics: coalesceTopics(a.Topics),
			Metadata: coalesceMeta(a.Metadata), HasMedia: a.HasMedia(), CardType: a.CardType(),
			Deleted: a.Deleted, ContentHash: a.ContentHash,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// articleResponse is the full single-article shape, including the body. HasMedia is
// the same server-derived flag the light list carries, so a client reads it
// consistently whether it fetched the list or the single article.
type articleResponse struct {
	ID          string         `json:"id"`
	Slug        string         `json:"slug"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	Section     string         `json:"section"`
	Author      string         `json:"author"`
	PublishedAt string         `json:"published_at"`
	Topics      []string       `json:"topics"`
	Metadata    map[string]any `json:"metadata"`
	HasMedia    bool           `json:"has_media"`
	CardType    string         `json:"card_type"`
	Deleted     bool           `json:"deleted"`
	ContentHash string         `json:"content_hash"`
	CreatedAt   string         `json:"created_at"`
}

// ServeArticle answers GET /articles/{slug} with the full article including the
// body. A soft-deleted article is still returned (deleted=true); a missing slug is
// a 404.
func (rh *ReadHandler) ServeArticle(w http.ResponseWriter, r *http.Request) {
	if !rh.authn(w, r) {
		return
	}
	a, err := rh.store.BySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, articleResponse{
		ID: a.ID, Slug: a.Slug, Title: a.Title, Body: a.Body, Section: a.Section,
		Author: a.Author, PublishedAt: rfc3339(a.PublishedAt), Topics: coalesceTopics(a.Topics),
		Metadata: coalesceMeta(a.Metadata), HasMedia: a.HasMedia(), CardType: a.CardType(),
		Deleted: a.Deleted, ContentHash: a.ContentHash,
		CreatedAt: rfc3339(a.CreatedAt),
	})
}

// parseFilter maps the query string to a store.Filter, writing a 400 problem and
// returning ok=false on a malformed from/to/limit/offset.
func parseFilter(w http.ResponseWriter, r *http.Request) (store.Filter, bool) {
	q := r.URL.Query()
	f := store.Filter{
		Section:        q.Get("section"),
		Author:         q.Get("author"),
		Topic:          q.Get("topic"),
		Query:          q.Get("q"),
		IncludeDeleted: includeDeleted(r),
	}
	if q.Get("order") == "oldest" {
		f.Order = store.OldestFirst
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_query", Detail: "from must be an RFC3339 timestamp"})
			return store.Filter{}, false
		}
		f.From = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_query", Detail: "to must be an RFC3339 timestamp"})
			return store.Filter{}, false
		}
		f.To = t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_query", Detail: "limit must be a non-negative integer"})
			return store.Filter{}, false
		}
		f.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_query", Detail: "offset must be a non-negative integer"})
			return store.Filter{}, false
		}
		f.Offset = n
	}
	return f, true
}

func includeDeleted(r *http.Request) bool {
	return r.URL.Query().Get("include_deleted") == "true"
}

// rfc3339 formats a timestamp as a UTC RFC3339 string, or "" when zero, so the JSON
// is stable and a never-set time is an empty string rather than the Go zero date.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// coalesceMeta returns an empty object rather than nil so the JSON is "{}" not "null".
func coalesceMeta(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// coalesceTopics returns an empty slice rather than nil so the JSON is "[]" not "null".
func coalesceTopics(t []string) []string {
	if t == nil {
		return []string{}
	}
	return t
}
