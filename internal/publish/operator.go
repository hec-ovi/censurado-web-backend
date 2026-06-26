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
// goes through the same single store writer and fires the SAME off-request
// regenerate+purge after a change so the public site reflects it.
type OperatorHandler struct {
	store ReadStore // Repository + AuthorStore + TopicStore (the concrete *Store)
	auth  Authenticator
	now   func() time.Time
	regen regenTrigger // optional; nil = no auto-regenerate
}

// NewOperatorHandler wires the operator mutation handler over a store and the same
// authenticator the write path uses. now may be nil (defaults to time.Now).
func NewOperatorHandler(s ReadStore, auth Authenticator, now func() time.Time) *OperatorHandler {
	if now == nil {
		now = time.Now
	}
	return &OperatorHandler{store: s, auth: auth, now: now}
}

// WithRegenerator attaches the SAME off-request regenerate trigger the publish
// handler uses, so an author/topic/article mutation rebuilds and purges the static
// site exactly like a create. nil leaves auto-regenerate off. Returns the handler
// for chaining.
func (oh *OperatorHandler) WithRegenerator(r regenTrigger) *OperatorHandler {
	oh.regen = r
	return oh
}

func (oh *OperatorHandler) triggerRegen() {
	if oh.regen != nil {
		oh.regen.Trigger()
	}
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

type authorInput struct {
	Handle   string         `json:"handle"`
	Name     string         `json:"name"`
	Bio      string         `json:"bio"`
	Avatar   string         `json:"avatar"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ServeUpsertAuthor answers POST /authors: create or update an author keyed on
// handle. handle is required; a missing one is a 400.
func (oh *OperatorHandler) ServeUpsertAuthor(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	var in authorInput
	if !decodeStrict(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Handle) == "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "handle is required"})
		return
	}
	a, err := oh.store.UpsertAuthor(r.Context(), store.Author{
		Handle: strings.TrimSpace(in.Handle), Name: in.Name, Bio: in.Bio,
		Avatar: in.Avatar, Metadata: in.Metadata,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	oh.triggerRegen()
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
	oh.triggerRegen()
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
		Handle: a.Handle, Name: a.Name, Bio: a.Bio, Avatar: a.Avatar, Metadata: a.Metadata,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	oh.triggerRegen()
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
	oh.triggerRegen()
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
	oh.triggerRegen()
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
	oh.triggerRegen()
	writeJSON(w, http.StatusOK, toTopicJSON(restored))
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
	oh.triggerRegen()
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
	oh.triggerRegen()
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
	oh.triggerRegen()
	w.WriteHeader(http.StatusNoContent)
}

// ----- JSON shaping (reuses the read API's authorJSON/topicJSON/articleResponse) -----

func toAuthorJSON(a store.Author) authorJSON {
	return authorJSON{
		Handle: a.Handle, Name: a.Name, Bio: a.Bio, Avatar: a.Avatar,
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
