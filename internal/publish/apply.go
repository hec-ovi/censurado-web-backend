package publish

import (
	"context"
	"errors"
	"time"

	"github.com/hec-ovi/censurado-web-backend/content"
	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
)

// FailureKind classifies why Apply rejected a submission. The HTTP handler maps
// each kind to the exact status and problem code it has always returned, and the
// replay tool uses it to tally and explain failures. FailureNone means success.
type FailureKind int

const (
	// FailureNone is the zero value: Apply succeeded (Created tells 201 vs 200).
	FailureNone FailureKind = iota
	// FailureValidation: NewArticle returned a *domain.ValidationError. The HTTP
	// layer answers 422 validation_failed with the field map (ApplyResult.Fields).
	FailureValidation
	// FailureInvalidArticle: NewArticle failed for a non-field reason. 422 invalid_article.
	FailureInvalidArticle
	// FailureUnrenderableBody: the Markdown safety gate rejected the body. 422 unrenderable_body.
	FailureUnrenderableBody
	// FailureIdempotencyConflict: the key was already used for a different content
	// hash. 422 idempotency_key_reused.
	FailureIdempotencyConflict
	// FailureLookup: the idempotency lookup (FindSubmission) errored. 500 internal.
	FailureLookup
	// FailureStore: the article Upsert errored. 500 store_error.
	FailureStore
	// FailureAudit: recording the submission errored. 500 audit_error.
	FailureAudit
)

// String gives a stable, log-friendly name for each failure kind. It mirrors the
// problem code the HTTP layer emits where one exists.
func (k FailureKind) String() string {
	switch k {
	case FailureNone:
		return "ok"
	case FailureValidation:
		return "validation_failed"
	case FailureInvalidArticle:
		return "invalid_article"
	case FailureUnrenderableBody:
		return "unrenderable_body"
	case FailureIdempotencyConflict:
		return "idempotency_key_reused"
	case FailureLookup:
		return "lookup_error"
	case FailureStore:
		return "store_error"
	case FailureAudit:
		return "audit_error"
	default:
		return "unknown"
	}
}

// ErrIdempotencyKeyReused is the error Apply attaches when an idempotency key was
// already used for a different article (a content-hash mismatch).
var ErrIdempotencyKeyReused = errors.New("idempotency key reused for a different article")

// validateInput is the validation half of Apply: it builds the canonical article
// (NewArticle) and runs the Markdown safety gate (RenderMarkdown), writing
// nothing. On success it returns the article and FailureNone. On failure it
// returns the FailureKind, the validation field map (FailureValidation only), and
// the underlying error. Both the single path (Apply) and the batch path use it so
// validation is identical, and the batch can validate every item before any
// write.
func validateInput(in domain.PublishInput, now time.Time) (domain.Article, FailureKind, map[string]string, error) {
	article, err := domain.NewArticle(in, now)
	if err != nil {
		var ve *domain.ValidationError
		if errors.As(err, &ve) {
			return domain.Article{}, FailureValidation, ve.Fields, err
		}
		return domain.Article{}, FailureInvalidArticle, nil, err
	}
	// Safety gate: the body must render to sanitized HTML without error.
	if _, err := content.RenderMarkdown(article.Body); err != nil {
		return domain.Article{}, FailureUnrenderableBody, nil, err
	}
	return article, FailureNone, nil, nil
}

// ApplyResult is the outcome of the accept core. On success Failure is FailureNone
// and Article + Created describe the stored article (Created=true => a brand-new
// write that maps to 201; false => an idempotent replay or content-hash dedup that
// maps to 200). On failure Failure says why, Fields carries validation field errors
// (FailureValidation only), and Err is the underlying error for detail and logging.
type ApplyResult struct {
	Article domain.Article
	Created bool
	Failure FailureKind
	Fields  map[string]string
	Err     error
}

// Apply is the post-auth accept core shared by the HTTP handler and the replay
// tool. It does exactly what ServeHTTP does after authentication and author
// binding: build the canonical Article (NewArticle), run the Markdown safety gate
// (RenderMarkdown), check the idempotency ledger (FindSubmission) for a prior use
// of the key, Upsert the article (content-hash deduplicated), and append the
// submission record. now is used both as the article clock and the submission
// CreatedAt; scopes are recorded on the submission (the handler passes the caller's
// scopes; replay has none and passes nil).
//
// Exactly-once is a property of this path: a key already in the ledger with the
// same content hash is a no-op replay (Created=false), and a content hash already
// stored under a different key is deduplicated by Upsert (Created=false). Neither
// writes a second article, which is what makes archived-payload replay safe to run
// repeatedly.
func Apply(ctx context.Context, repo store.Repository, log store.SubmissionLog, in domain.PublishInput, idemKey string, scopes []string, now time.Time) (ApplyResult, error) {
	article, fk, fields, err := validateInput(in, now)
	if fk != FailureNone {
		return ApplyResult{Failure: fk, Fields: fields, Err: err}, err
	}

	if prev, found, err := log.FindSubmission(ctx, idemKey); err != nil {
		return ApplyResult{Failure: FailureLookup, Err: err}, err
	} else if found {
		if prev.ContentHash != article.ContentHash {
			return ApplyResult{Failure: FailureIdempotencyConflict, Err: ErrIdempotencyKeyReused}, ErrIdempotencyKeyReused
		}
		// Idempotent replay: the key already minted this article. Return its stored
		// identity without writing again.
		return ApplyResult{
			Article: domain.Article{ID: prev.ArticleID, Slug: prev.Slug, ContentHash: prev.ContentHash},
			Created: false,
		}, nil
	}

	res, err := repo.Upsert(ctx, article)
	if err != nil {
		return ApplyResult{Failure: FailureStore, Err: err}, err
	}

	if err := log.RecordSubmission(ctx, store.Submission{
		IdempotencyKey: idemKey,
		ContentHash:    res.Article.ContentHash,
		ArticleID:      res.Article.ID,
		Slug:           res.Article.Slug,
		Author:         res.Article.Author,
		Scopes:         scopes,
		CreatedAt:      now.UTC(),
	}); err != nil {
		return ApplyResult{Failure: FailureAudit, Err: err}, err
	}

	return ApplyResult{Article: res.Article, Created: res.Created}, nil
}
