package publish

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
)

// ScopeAdminWrite is the scope a key must hold to use the operator mutation lane:
// authors/topics CRUD and article edit/delete/restore. It is distinct from
// ScopeWrite (the append-only agent publish path), so an agent key can never reach
// the operator console's edit/delete surface and only the trusted operator key
// carries it.
const ScopeAdminWrite = "admin:write"

// OperatorHandler serves the operator mutation lane: the registry CRUD and the
// article edit/delete/restore endpoints behind ScopeAdminWrite. It is the second,
// audited write lane alongside the append-only publish path, and like that path it
// goes through the same single store writer.
type OperatorHandler struct {
	store ReadStore // Repository + AuthorStore + TopicStore (the concrete *Store)
	auth  Authenticator
	now   func() time.Time
}

// NewOperatorHandler wires the operator mutation handler over a store and the same
// authenticator the write path uses. now may be nil (defaults to time.Now).
func NewOperatorHandler(s ReadStore, auth Authenticator, now func() time.Time) *OperatorHandler {
	if now == nil {
		now = time.Now
	}
	return &OperatorHandler{store: s, auth: auth, now: now}
}

// authz requires a valid bearer token AND the admin:write scope, writing the
// problem response and returning false on failure. A key with a good token but
// without admin:write gets 403 insufficient_scope, mirroring the write path.
func (oh *OperatorHandler) authz(w http.ResponseWriter, r *http.Request) bool {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		writeProblem(w, problem{Status: http.StatusUnauthorized, Code: "missing_token"})
		return false
	}
	id, err := oh.auth.Authenticate(token)
	if err != nil {
		writeProblem(w, problem{Status: http.StatusUnauthorized, Code: "invalid_token"})
		return false
	}
	if !id.HasScope(ScopeAdminWrite) {
		writeProblem(w, problem{Status: http.StatusForbidden, Code: "insufficient_scope", Detail: "requires " + ScopeAdminWrite})
		return false
	}
	return true
}

// decodeStrict decodes a JSON request body with unknown fields rejected, writing a
// 400 invalid_json problem and returning false on failure, the same strict contract
// the publish path uses.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_json", Detail: err.Error()})
		return false
	}
	return true
}

// ----- authors -----

// authorInput is the POST /authors body. Sources is a pointer so the caller can
// distinguish "omitted" (leave the attached-source set untouched) from an explicit
// empty array (detach everything): a nil Sources never touches the author_sources join.
type authorInput struct {
	Handle   string         `json:"handle"`
	Name     string         `json:"name"`
	Bio      string         `json:"bio"`
	Avatar   string         `json:"avatar"`
	Gender   string         `json:"gender"`
	About    string         `json:"about"`
	Style    string         `json:"style"`
	Topics   []string       `json:"topics,omitempty"`
	Sources  *[]string      `json:"sources,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ServeUpsertAuthor answers POST /authors: create or update an author keyed on
// handle. handle is required; a missing one is a 400. When sources is present it
// replaces the author's attached-source set in the same request; when it is omitted
// the join is left untouched.
func (oh *OperatorHandler) ServeUpsertAuthor(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	var in authorInput
	if !decodeStrict(w, r, &in) {
		return
	}
	handle := strings.TrimSpace(in.Handle)
	if handle == "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "handle is required"})
		return
	}
	a, err := oh.store.UpsertAuthor(r.Context(), store.Author{
		Handle: handle, Name: in.Name, Bio: in.Bio, Avatar: in.Avatar,
		Gender: in.Gender, About: in.About, Style: in.Style, Topics: in.Topics,
		Metadata: in.Metadata,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	if in.Sources != nil {
		if err := oh.store.SetAuthorSources(r.Context(), a.Handle, *in.Sources); err != nil {
			writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
			return
		}
		// Re-read so the response carries the freshly hydrated Sources.
		a, _, err = oh.store.AuthorByHandle(r.Context(), a.Handle)
		if err != nil {
			writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
			return
		}
	}
	writeJSON(w, http.StatusOK, toAuthorJSON(a))
}

// ServeDeleteAuthor answers DELETE /authors/{handle}: soft-delete (tombstone) the
// author. A missing handle is a 404.
func (oh *OperatorHandler) ServeDeleteAuthor(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	err := oh.store.DeleteAuthor(r.Context(), r.PathValue("handle"))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeRestoreAuthor answers POST /authors/{handle}/restore: clear the tombstone by
// re-upserting the stored row. A missing handle is a 404.
func (oh *OperatorHandler) ServeRestoreAuthor(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	a, found, err := oh.store.AuthorByHandle(r.Context(), r.PathValue("handle"))
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	if !found {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	restored, err := oh.store.UpsertAuthor(r.Context(), store.Author{
		Handle: a.Handle, Name: a.Name, Bio: a.Bio, Avatar: a.Avatar,
		Gender: a.Gender, About: a.About, Style: a.Style, Topics: a.Topics,
		Metadata: a.Metadata,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	// The author's source links survive a delete/restore (DeleteAuthor never touches
	// the join), so the re-upserted row hydrates them back into the response.
	writeJSON(w, http.StatusOK, toAuthorJSON(restored))
}

// ----- topics -----

type topicInput struct {
	Slug        string         `json:"slug"`
	Label       string         `json:"label"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ServeUpsertTopic answers POST /topics: create or update a topic keyed on slug.
// slug is required; a missing one is a 400.
func (oh *OperatorHandler) ServeUpsertTopic(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	var in topicInput
	if !decodeStrict(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Slug) == "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "slug is required"})
		return
	}
	tp, err := oh.store.UpsertTopic(r.Context(), store.Topic{
		Slug: strings.TrimSpace(in.Slug), Label: in.Label, Description: in.Description, Metadata: in.Metadata,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, toTopicJSON(tp))
}

// ServeDeleteTopic answers DELETE /topics/{slug}: soft-delete the topic. 404 on a
// missing slug.
func (oh *OperatorHandler) ServeDeleteTopic(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	err := oh.store.DeleteTopic(r.Context(), r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeRestoreTopic answers POST /topics/{slug}/restore: clear the tombstone. 404 on
// a missing slug.
func (oh *OperatorHandler) ServeRestoreTopic(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	tp, found, err := oh.store.TopicBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	if !found {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	restored, err := oh.store.UpsertTopic(r.Context(), store.Topic{
		Slug: tp.Slug, Label: tp.Label, Description: tp.Description, Metadata: tp.Metadata,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, toTopicJSON(restored))
}

// ----- portadas -----

type portadaEntryInput struct {
	Slug string `json:"slug"`
	Role string `json:"role"`
}

type portadaInput struct {
	Date        string              `json:"date"`
	Entries     []portadaEntryInput `json:"entries"`
	Recomendado []string            `json:"recomendado"`
}

// ServeUpsertPortada answers POST /portadas: create or update a day plan keyed on
// date. date is required; a missing one is a 400. Entries and recomendado are
// replaced wholesale.
func (oh *OperatorHandler) ServeUpsertPortada(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	var in portadaInput
	if !decodeStrict(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Date) == "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "date is required"})
		return
	}
	entries := make([]store.PortadaEntry, 0, len(in.Entries))
	for _, e := range in.Entries {
		entries = append(entries, store.PortadaEntry{Slug: e.Slug, Role: e.Role})
	}
	p, err := oh.store.UpsertPortada(r.Context(), store.PortadaDay{
		Date: strings.TrimSpace(in.Date), Entries: entries, Recomendado: in.Recomendado,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, toPortadaJSON(p))
}

// ServeDeletePortada answers DELETE /portadas/{date}: soft-delete the day plan. 404
// on a missing date.
func (oh *OperatorHandler) ServeDeletePortada(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	err := oh.store.DeletePortada(r.Context(), r.PathValue("date"))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeRestorePortada answers POST /portadas/{date}/restore: clear the tombstone by
// re-upserting the stored row. 404 on a missing date.
func (oh *OperatorHandler) ServeRestorePortada(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	p, found, err := oh.store.PortadaByDate(r.Context(), r.PathValue("date"))
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	if !found {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	restored, err := oh.store.UpsertPortada(r.Context(), store.PortadaDay{
		Date: p.Date, Entries: p.Entries, Recomendado: p.Recomendado,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, toPortadaJSON(restored))
}

// ----- sources -----

// validLeans and validFeedTypes are the closed vocabularies the source registry
// enforces at the HTTP edge (the schema keeps them as plain TEXT for dialect and
// ALTER neutrality, exactly like articles.section). They mirror the brain's portal
// enums so a migrated source keeps the same lean and feed_type.
var validLeans = map[string]bool{"right": true, "neutral": true, "left": true}
var validFeedTypes = map[string]bool{"auto": true, "native_rss": true, "atom": true, "news_sitemap": true, "site_search": true}

// sourceInput is the POST /sources body. slug is optional: when blank it is derived
// from the (normalized) domain, so a panel or agent can add a source by URL alone.
// enabled is a pointer so an omitted flag defaults to true (registered and active)
// rather than false.
type sourceInput struct {
	Slug           string         `json:"slug"`
	Domain         string         `json:"domain"`
	Homepage       string         `json:"homepage"`
	Description    string         `json:"description"`
	FeedURLs       []string       `json:"feed_urls,omitempty"`
	FeedType       string         `json:"feed_type"`
	Language       string         `json:"language"`
	OwnershipGroup string         `json:"ownership_group"`
	Lean           string         `json:"lean"`
	Enabled        *bool          `json:"enabled,omitempty"`
	Status         string         `json:"status"`
	LastChecked    string         `json:"last_checked"`
	LastOK         string         `json:"last_ok"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// normalizeDomain reduces a URL or host to a bare registrable host: lowercase, no
// scheme, no leading "www.", no path or query. It mirrors the brain's portal
// normalization so a source added here keys to the same slug the newsroom used.
func normalizeDomain(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "www.")
	return strings.Trim(s, ".")
}

// ServeUpsertSource answers POST /sources: create or update a source keyed on slug.
// domain is required; slug defaults to the slugified domain. lean and feed_type must
// be in their closed vocabularies (a bad value is a 400), and the other operational
// fields default sensibly when blank.
func (oh *OperatorHandler) ServeUpsertSource(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	var in sourceInput
	if !decodeStrict(w, r, &in) {
		return
	}
	domainName := normalizeDomain(in.Domain)
	if domainName == "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "domain is required"})
		return
	}
	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		slug = domain.Slugify(domainName)
	}
	if slug == "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "could not derive a slug from the domain"})
		return
	}
	feedType := in.FeedType
	if feedType == "" {
		feedType = "auto"
	}
	if !validFeedTypes[feedType] {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "feed_type must be one of auto, native_rss, atom, news_sitemap, site_search"})
		return
	}
	lean := in.Lean
	if lean == "" {
		lean = "neutral"
	}
	if !validLeans[lean] {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "lean must be one of right, neutral, left"})
		return
	}
	language := in.Language
	if language == "" {
		language = "es"
	}
	status := in.Status
	if status == "" {
		status = "unknown"
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	src, err := oh.store.UpsertSource(r.Context(), store.Source{
		Slug: slug, Domain: domainName, Homepage: in.Homepage, Description: in.Description,
		FeedURLs: in.FeedURLs, FeedType: feedType, Language: language,
		OwnershipGroup: in.OwnershipGroup, Lean: lean, Enabled: enabled, Status: status,
		LastChecked: in.LastChecked, LastOK: in.LastOK, Metadata: in.Metadata,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, toSourceJSON(src))
}

// ServeDeleteSource answers DELETE /sources/{slug}: soft-delete the source AND detach
// it from every author (see store.DeleteSource). 404 on a missing slug.
func (oh *OperatorHandler) ServeDeleteSource(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	err := oh.store.DeleteSource(r.Context(), r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeRestoreSource answers POST /sources/{slug}/restore: clear the tombstone by
// re-upserting the stored row. Its author links were severed at delete time and are
// NOT restored (re-attach is an explicit act). 404 on a missing slug.
func (oh *OperatorHandler) ServeRestoreSource(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	src, found, err := oh.store.SourceBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	if !found {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	restored, err := oh.store.UpsertSource(r.Context(), store.Source{
		Slug: src.Slug, Domain: src.Domain, Homepage: src.Homepage, Description: src.Description,
		FeedURLs: src.FeedURLs, FeedType: src.FeedType, Language: src.Language,
		OwnershipGroup: src.OwnershipGroup, Lean: src.Lean, Enabled: src.Enabled, Status: src.Status,
		LastChecked: src.LastChecked, LastOK: src.LastOK, Metadata: src.Metadata,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, toSourceJSON(restored))
}

// authorSourcesInput is the PUT /authors/{handle}/sources body: the full attached-
// source set, replaced wholesale.
type authorSourcesInput struct {
	Sources []string `json:"sources"`
}

// ServeSetAuthorSources answers PUT /authors/{handle}/sources: replace the author's
// attached-source set. A missing author is a 404. The response echoes the slug-sorted
// set the store now holds.
func (oh *OperatorHandler) ServeSetAuthorSources(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	var in authorSourcesInput
	if !decodeStrict(w, r, &in) {
		return
	}
	handle := r.PathValue("handle")
	err := oh.store.SetAuthorSources(r.Context(), handle, in.Sources)
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	slugs, err := oh.store.AuthorSources(r.Context(), handle)
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, authorSourcesResponse{Handle: handle, Sources: coalesceTopics(slugs)})
}

// ----- articles -----

// ServeUpdateArticle answers PUT /articles/{slug}: edit an article in place. The
// body is recomputed and safety-gated exactly like a create (NewArticle +
// RenderMarkdown), then UpdateArticle preserves the row's id, slug, and created_at.
// A content-hash collision with another article is a 409 edit_conflict; a missing
// slug is a 404.
func (oh *OperatorHandler) ServeUpdateArticle(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	var in domain.PublishInput
	if !decodeStrict(w, r, &in) {
		return
	}
	// The path slug is authoritative and the permalink is stable, so any slug in the
	// body is ignored: an edit never moves the article's URL.
	in.Slug = ""
	article, fk, fields, err := validateInput(in, oh.now())
	switch fk {
	case FailureNone:
	case FailureValidation:
		writeProblem(w, problem{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Fields: fields})
		return
	case FailureUnrenderableBody:
		writeProblem(w, problem{Status: http.StatusUnprocessableEntity, Code: "unrenderable_body", Detail: err.Error()})
		return
	default:
		writeProblem(w, problem{Status: http.StatusUnprocessableEntity, Code: "invalid_article", Detail: err.Error()})
		return
	}
	article.Slug = r.PathValue("slug") // identify the row + keep its permalink
	updated, err := oh.store.UpdateArticle(r.Context(), article)
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	var ec *store.EditConflictError
	if errors.As(err, &ec) {
		writeProblem(w, problem{Status: http.StatusConflict, Code: "edit_conflict", Detail: "the edited content collides with an existing article"})
		return
	}
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, toArticleResponse(updated))
}

// ServeDeleteArticle answers DELETE /articles/{slug}: soft-delete the article so the
// generator stops rendering it. 404 on a missing slug.
func (oh *OperatorHandler) ServeDeleteArticle(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	err := oh.store.DeleteArticle(r.Context(), r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeRestoreArticle answers POST /articles/{slug}/restore: clear the tombstone so
// the article is published again. 404 on a missing slug.
func (oh *OperatorHandler) ServeRestoreArticle(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	err := oh.store.RestoreArticle(r.Context(), r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ----- JSON shaping (reuses the read API's authorJSON/topicJSON/articleResponse) -----

func toAuthorJSON(a store.Author) authorJSON {
	return authorJSON{
		Handle: a.Handle, Name: a.Name, Bio: a.Bio, Avatar: a.Avatar,
		Gender: a.Gender, About: a.About, Style: a.Style,
		Topics: coalesceTopics(a.Topics), Sources: coalesceTopics(a.Sources),
		Metadata: coalesceMeta(a.Metadata), Deleted: a.Deleted,
		CreatedAt: rfc3339(a.CreatedAt), UpdatedAt: rfc3339(a.UpdatedAt),
	}
}

func toTopicJSON(tp store.Topic) topicJSON {
	return topicJSON{
		Slug: tp.Slug, Label: tp.Label, Description: tp.Description,
		Metadata: coalesceMeta(tp.Metadata), Deleted: tp.Deleted,
		CreatedAt: rfc3339(tp.CreatedAt), UpdatedAt: rfc3339(tp.UpdatedAt),
	}
}

func toArticleResponse(a domain.Article) articleResponse {
	return articleResponse{
		ID: a.ID, Slug: a.Slug, Title: a.Title, Body: a.Body, Section: a.Section,
		Author: a.Author, PublishedAt: rfc3339(a.PublishedAt), Topics: coalesceTopics(a.Topics),
		Metadata: coalesceMeta(a.Metadata), Deleted: a.Deleted, ContentHash: a.ContentHash,
		CreatedAt: rfc3339(a.CreatedAt),
	}
}
