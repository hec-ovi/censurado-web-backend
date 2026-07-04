// Package publish implements the authenticated write path: the only way articles
// enter the system. It authenticates the caller, binds the article to that
// author, validates and safety-checks untrusted input, stores it (deduplicated
// and idempotent), and appends an audit/idempotency record per submission.
package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
)

// ScopeWrite is the scope a key must hold to publish.
const ScopeWrite = "articles:write"

// ScopePublishAny is a privileged operator scope: a key that holds it may publish
// as ANY author, bypassing the author-binding check below. It is meant only for
// the trusted operator console key (the human newsroom), never for agent keys.
// Agent keys carry ScopeWrite alone and stay locked to their own persona, so a
// leaked agent key can never impersonate another author.
const ScopePublishAny = "articles:publish-any"

// defaultMaxBody caps the request entity as a transport safeguard against abuse.
// It is generous and is not a content limit on the article body. It is the default
// for both POST /articles and POST /articles:batch; raise it with WithLimits (env
// CENSURADO_PUBLISH_MAX_BODY) when a batch of long articles needs more headroom.
const defaultMaxBody = 8 << 20 // 8 MiB

// defaultMaxBatchItems caps how many articles one POST /articles:batch may carry.
// It bounds the batch transaction and the validated working set; it is not a limit
// on any single article's length. Raise it with WithLimits (env
// CENSURADO_PUBLISH_BATCH_MAX_ITEMS).
const defaultMaxBatchItems = 500

// Handler serves POST /articles and POST /articles:batch.
type Handler struct {
	repo          store.Repository
	log           store.SubmissionLog
	auth          Authenticator
	now           func() time.Time
	archive       PayloadArchive                   // optional; nil = no payload archiving
	logf          func(format string, args ...any) // optional; nil = log.Printf
	maxBody       int                              // request entity cap (bytes); defaultMaxBody if zero
	maxBatchItems int                              // max items per batch; defaultMaxBatchItems if zero
}

// NewHandler wires the publish handler. now may be nil (defaults to time.Now).
// Payload archiving is off by default; attach it with WithArchive. The body and
// batch-size limits default to the generous safeguards above; override with
// WithLimits.
func NewHandler(repo store.Repository, log store.SubmissionLog, auth Authenticator, now func() time.Time) *Handler {
	if now == nil {
		now = time.Now
	}
	return &Handler{repo: repo, log: log, auth: auth, now: now, maxBody: defaultMaxBody, maxBatchItems: defaultMaxBatchItems}
}

// WithLimits overrides the request body cap and the per-batch item cap, returning
// the handler for chaining. A non-positive value leaves that limit at its default.
// These are transport safeguards, never content-length caps: a single article body
// has no maximum, and raising maxBody is how a batch of long articles is admitted.
func (h *Handler) WithLimits(maxBody, maxBatchItems int) *Handler {
	if maxBody > 0 {
		h.maxBody = maxBody
	}
	if maxBatchItems > 0 {
		h.maxBatchItems = maxBatchItems
	}
	return h
}

// WithArchive attaches an append-only payload archive and returns the handler for
// chaining. Every NEWLY created publish (not an idempotent replay) is recorded to
// it after the write commits, so an operator can replay payloads received after a
// restore point and recover writes lost in the asynchronous replication window.
// Archiving is best-effort: a Record failure is logged and never fails the publish
// that already succeeded. A nil archive leaves behavior exactly as without it.
func (h *Handler) WithArchive(a PayloadArchive) *Handler {
	h.archive = a
	return h
}

// WithLogger sets where archive warnings go. Defaults to log.Printf.
func (h *Handler) WithLogger(logf func(format string, args ...any)) *Handler {
	h.logf = logf
	return h
}

func (h *Handler) warnf(format string, args ...any) {
	if h.logf != nil {
		h.logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

type problem struct {
	Status int               `json:"status"`
	Code   string            `json:"code"`
	Detail string            `json:"detail,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
}

type createResponse struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeProblem(w, problem{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed"})
		return
	}

	id, ok := authenticateWrite(h.auth, w, r)
	if !ok {
		return
	}

	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "missing_idempotency_key", Detail: "Idempotency-Key header is required"})
		return
	}

	// Read the raw body first so the exact bytes can be archived, then decode from
	// them with the same strict (additionalProperties:false) contract as before.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(h.maxBody)))
	if err != nil {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_json", Detail: err.Error()})
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in domain.PublishInput
	if err := dec.Decode(&in); err != nil {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_json", Detail: err.Error()})
		return
	}

	// The key determines the author: an article may only be published as the
	// authenticated persona. The sole exception is a key holding ScopePublishAny
	// (the operator console key), which may publish as any author. A non-empty
	// author is still required and enforced by NewArticle inside Apply.
	if !authorAllowed(id, in.Author) {
		writeProblem(w, problem{Status: http.StatusForbidden, Code: "author_mismatch", Detail: "author must match the authenticated key"})
		return
	}

	// The accept core (NewArticle -> safety gate -> idempotency -> Upsert -> audit)
	// lives in Apply so the replay tool runs the identical, exactly-once path. now is
	// read once and used as both the article clock and the submission CreatedAt.
	ctx := r.Context()
	now := h.now()
	res, _ := Apply(ctx, h.repo, h.log, in, key, id.Scopes, now)
	switch res.Failure {
	case FailureNone:
		// Archive every newly created publish exactly once, after the write commits.
		// A 200 replay/dedup is not re-archived, so retries do not bloat the sink.
		if res.Created {
			h.recordArchive(ctx, key, id.Author, now, raw)
		}
		status := http.StatusOK // article already existed (deduplicated)
		if res.Created {
			status = http.StatusCreated
		}
		writeJSON(w, status, createResponse{ID: res.Article.ID, Slug: res.Article.Slug})
	case FailureValidation:
		writeProblem(w, problem{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Fields: res.Fields})
	case FailureInvalidArticle:
		writeProblem(w, problem{Status: http.StatusUnprocessableEntity, Code: "invalid_article", Detail: res.Err.Error()})
	case FailureUnrenderableBody:
		writeProblem(w, problem{Status: http.StatusUnprocessableEntity, Code: "unrenderable_body", Detail: res.Err.Error()})
	case FailureIdempotencyConflict:
		writeProblem(w, problem{Status: http.StatusUnprocessableEntity, Code: "idempotency_key_reused", Detail: "this key was used for a different article"})
	case FailureLookup:
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "internal"})
	case FailureStore:
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
	case FailureAudit:
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "audit_error"})
	}
}

// recordArchive writes the raw payload to the configured archive, if any. It is
// best-effort: a failure is logged but never propagated, because the publish it
// describes has already committed. The raw bytes are copied so the archive never
// aliases a reused request buffer.
func (h *Handler) recordArchive(ctx context.Context, key, author string, now time.Time, raw []byte) {
	if h.archive == nil {
		return
	}
	body := append([]byte(nil), raw...)
	if err := h.archive.Record(ctx, ArchivedPayload{
		IdempotencyKey: key,
		Author:         author,
		ReceivedAt:     now,
		Body:           body,
	}); err != nil {
		h.warnf("publish: archive record failed (idempotency_key=%q author=%q): %v", key, author, err)
	}
}

// authenticateWrite checks the bearer token and the write scope, writing the
// appropriate problem response on failure. It is shared by the article handler and
// the media handler so both gate on the same articles:write credential.
func authenticateWrite(auth Authenticator, w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, ok := resolveIdentity(auth, w, r)
	if !ok {
		return Identity{}, false
	}
	if !id.HasScope(ScopeWrite) {
		writeProblem(w, problem{Status: http.StatusForbidden, Code: "insufficient_scope", Detail: "requires " + ScopeWrite})
		return Identity{}, false
	}
	return id, true
}

// authorAllowed reports whether identity id may publish as the given author. Any
// key may publish as its own author; only a key holding ScopePublishAny (the
// operator console key) may publish as a different one. A blank author is left for
// NewArticle to reject, so this only governs the author-binding rule. Shared by
// the single handler and the batch handler so per-item binding is identical.
func authorAllowed(id Identity, author string) bool {
	return id.HasScope(ScopePublishAny) || strings.TrimSpace(author) == id.Author
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, p problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
