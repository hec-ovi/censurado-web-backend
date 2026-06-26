package publish

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hec-ovi/censurado-web-backend/store"
)

// ReadStore is the read surface the JSON read API needs: the article Repository
// plus the operator-owned author and topic registries. Both *sqlite.Store and
// *postgres.Store satisfy it, so the same concrete store backs the write path and
// these reads.
type ReadStore interface {
	store.Repository
	store.AuthorStore
	store.TopicStore
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
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		writeProblem(w, problem{Status: http.StatusUnauthorized, Code: "missing_token"})
		return false
	}
	if _, err := rh.auth.Authenticate(token); err != nil {
		writeProblem(w, problem{Status: http.StatusUnauthorized, Code: "invalid_token"})
		return false
	}
	return true
}

type authorJSON struct {
	Handle    string         `json:"handle"`
	Name      string         `json:"name"`
	Bio       string         `json:"bio"`
	Avatar    string         `json:"avatar"`
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
		out.Authors = append(out.Authors, authorJSON{
			Handle: a.Handle, Name: a.Name, Bio: a.Bio, Avatar: a.Avatar,
			Metadata: coalesceMeta(a.Metadata), Deleted: a.Deleted,
			CreatedAt: rfc3339(a.CreatedAt), UpdatedAt: rfc3339(a.UpdatedAt),
		})
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

// articleListItem is the light list shape: everything an index needs EXCEPT the
// body, so a listing of many articles stays small.
type articleListItem struct {
	Slug        string         `json:"slug"`
	Title       string         `json:"title"`
	Section     string         `json:"section"`
	Author      string         `json:"author"`
	PublishedAt string         `json:"published_at"`
	Topics      []string       `json:"topics"`
	Metadata    map[string]any `json:"metadata"`
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
			Metadata: coalesceMeta(a.Metadata), Deleted: a.Deleted, ContentHash: a.ContentHash,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// articleResponse is the full single-article shape, including the body.
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
		Metadata: coalesceMeta(a.Metadata), Deleted: a.Deleted, ContentHash: a.ContentHash,
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
