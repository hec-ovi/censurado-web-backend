// Package store defines the source-of-truth contract for articles. The interface
// is expressed purely in domain terms; no SQL, dialect, or storage detail leaks
// through it, which is what keeps the store swappable. SQLite is the default
// adapter; a Postgres adapter satisfies the same interface and is exercised by
// the same conformance suite (see storetest) so the swap is proven, not assumed.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hec-ovi/censurado-web-backend/domain"
)

// ErrNotFound is returned when a lookup matches no article.
var ErrNotFound = errors.New("store: article not found")

// Order controls result ordering by publish time.
type Order int

const (
	// NewestFirst orders by descending publish time (the default).
	NewestFirst Order = iota
	// OldestFirst orders by ascending publish time.
	OldestFirst
)

// Filter selects articles by the stable hot axes. Zero-valued fields are
// ignored, so the empty Filter matches everything.
//
// Axes AND together. The scalar Section/Author/Topic fields are the stable public
// surface the generator relies on. The plural Sections/Authors/Topics slices and
// the Query field are an admin-only widening (Phase 5): within a single plural
// axis the values OR (membership), and a scalar field and its plural counterpart
// are independent constraints that AND together (the plural never overrides the
// scalar). Blank or whitespace-only entries inside a slice are ignored, and a
// slice of only blanks is treated as no constraint.
type Filter struct {
	// Scalar hot axes. The public generator uses ONLY these (plus From/To/Order/
	// paging); they must keep their exact meaning.
	Section string
	Author  string
	Topic   string

	// Multi-value axes (admin only). Each is an OR-within-axis membership test:
	//   Sections -> article.section IN (Sections)
	//   Authors  -> article.author  IN (Authors)
	//   Topics   -> article has AT LEAST ONE topic IN (Topics)
	// They AND with each other and with the scalar fields above. Blank entries are
	// ignored; an all-blank (or empty) slice imposes no constraint.
	Sections []string
	Authors  []string
	Topics   []string

	// Query is an admin-only full-text filter: a case-insensitive substring match
	// over title OR body. Blank/whitespace-only imposes no constraint. The match is
	// ASCII case-insensitive only; non-ASCII (accented) letters match
	// case-sensitively. See the adapters' buildSelect for why (sqlite vs postgres
	// lower() differ on unicode folding) and the exact, parity-safe contract.
	Query string

	From   time.Time // inclusive lower bound on PublishedAt; zero = open
	To     time.Time // exclusive upper bound on PublishedAt; zero = open
	Order  Order
	Limit  int // 0 = no limit
	Offset int

	// IncludeDeleted is an admin-only flag. By default Find and Count EXCLUDE
	// soft-deleted (tombstoned) articles, so the public generator (which passes the
	// zero Filter) never renders a removed article. The admin sets this true to see
	// tombstoned rows, e.g. to restore one.
	IncludeDeleted bool
}

// Facet is one distinct filter value and how many articles carry it. It feeds the
// admin filter UI's faceted controls.
type Facet struct {
	Value string
	Count int
}

// Facets is the distinct set of filter values present in the store, per axis,
// each with its article count. It is a read-only aggregate for the admin filter
// UI. Each slice is ordered Count DESC, then Value ASC, so the bytes are
// deterministic and identical across the SQLite and Postgres adapters.
type Facets struct {
	Sections []Facet
	Authors  []Facet
	Topics   []Facet
}

// UpsertResult reports the stored article and whether it was newly created.
// Created is false when an article with the same content hash already existed,
// which is what makes publishing both idempotent and deduplicated.
type UpsertResult struct {
	Article domain.Article
	Created bool
}

// UpsertItem is one article to store together with the idempotency-ledger
// metadata that UpsertMany writes in the SAME transaction. Bundling them is what
// makes a batch publish atomic: the article, its topics, and its submission
// record commit together or not at all. The article's CreatedAt is reused as the
// submission timestamp, exactly as the single-publish path does in publish.Apply.
type UpsertItem struct {
	Article        domain.Article
	IdempotencyKey string
	Scopes         []string
}

// BatchConflictError reports that the item at Index reused an idempotency key
// that already maps to a different article (a content-hash mismatch), the same
// condition the single path answers with idempotency_key_reused. UpsertMany rolls
// back the entire batch and returns this so the caller can answer with a per-item
// error and write nothing.
type BatchConflictError struct {
	Index          int
	IdempotencyKey string
}

func (e *BatchConflictError) Error() string {
	return fmt.Sprintf("store: batch item %d reused idempotency key %q for a different article", e.Index, e.IdempotencyKey)
}

// EditConflictError reports that an UpdateArticle would set a content hash already
// held by a DIFFERENT article (the content_hash UNIQUE constraint). The edit is
// rolled back and nothing is written, so the caller can answer with a 409 instead
// of leaking a raw store error. Editing a row to its own existing content hash is
// a no-op-safe success and never produces this.
type EditConflictError struct {
	Slug        string
	ContentHash string
}

func (e *EditConflictError) Error() string {
	return fmt.Sprintf("store: editing %q would collide with an existing article on content hash %s", e.Slug, e.ContentHash)
}

// Repository is the article source of truth. Implementations must be safe for a
// single writer with concurrent readers.
type Repository interface {
	// Upsert stores an article, deduplicating on content hash: a later call with
	// the same hash returns the existing row with Created=false.
	Upsert(ctx context.Context, a domain.Article) (UpsertResult, error)
	// UpsertMany stores a batch of articles and their submission-ledger records in
	// a single transaction: either every item commits or none does. Each item is
	// deduplicated on content hash and idempotent on its key exactly like Upsert
	// followed by RecordSubmission, so a replayed key returns the prior article
	// with Created=false and writes nothing. It returns one UpsertResult per input
	// item, in order. If any item reuses an idempotency key for a different article
	// it writes nothing and returns a *BatchConflictError naming the offending
	// index. Callers validate every item (NewArticle, the safety gate, author
	// binding, intra-batch duplicate keys/slugs) before calling this; UpsertMany
	// owns only the atomic write.
	UpsertMany(ctx context.Context, items []UpsertItem) ([]UpsertResult, error)
	// BySlug returns the article with the given slug, or ErrNotFound.
	BySlug(ctx context.Context, slug string) (domain.Article, error)
	// Find returns articles matching the filter, ordered and paged.
	Find(ctx context.Context, f Filter) ([]domain.Article, error)
	// Count returns how many articles match the filter, ignoring Limit/Offset.
	Count(ctx context.Context, f Filter) (int, error)
	// Facets returns the distinct filter values present with their article counts,
	// for the admin filter UI. Read-only aggregate. Deterministic ordering.
	Facets(ctx context.Context) (Facets, error)
	// UpdateArticle mutates the existing article identified by a.Slug in place,
	// preserving its id, slug, and created_at (the permalink stays stable) while
	// replacing the mutable fields (title/body/author/section/published_at/
	// content_hash/metadata) and its full topic set. The caller supplies the
	// recomputed a.ContentHash; the store does not hash. Returns ErrNotFound when no
	// row has the slug, and *EditConflictError when the new content hash collides
	// with a different article. This is the operator edit lane, distinct from the
	// append-only publish path.
	UpdateArticle(ctx context.Context, a domain.Article) (domain.Article, error)
	// DeleteArticle soft-deletes the article (sets a tombstone), so it is hidden
	// from the default Find/Count but kept for audit and restore. ErrNotFound on an
	// absent slug; idempotent on an already-deleted row. A later idempotent publish
	// replay does NOT resurrect a deleted article.
	DeleteArticle(ctx context.Context, slug string) error
	// RestoreArticle clears the tombstone so a soft-deleted article is active again.
	// ErrNotFound on an absent slug.
	RestoreArticle(ctx context.Context, slug string) error
	// Close releases resources held by the store.
	Close() error
}

// Submission is the append-only record of one publish attempt. It is both the
// audit log entry and the idempotency ledger entry, so one write covers both.
type Submission struct {
	IdempotencyKey string
	ContentHash    string
	ArticleID      string
	Slug           string
	Author         string
	Scopes         []string
	CreatedAt      time.Time
}

// ListSubmissionsFilter selects audit-log entries. Zero-valued fields impose no
// constraint, so the empty filter lists every submission. The bounds match the
// Filter date semantics: From is an inclusive lower bound, To an exclusive upper
// bound, both on CreatedAt.
type ListSubmissionsFilter struct {
	Author string    // optional exact author; "" = all
	From   time.Time // inclusive lower bound on CreatedAt; zero = open
	To     time.Time // exclusive upper bound; zero = open
	Limit  int       // 0 = no limit
	Offset int
}

// SubmissionLog records publish attempts and looks them up by idempotency key.
// It is a separate concern from the article Repository so the article contract
// stays focused, even when one adapter implements both.
type SubmissionLog interface {
	// FindSubmission returns a prior submission for the key, or found=false.
	FindSubmission(ctx context.Context, idempotencyKey string) (Submission, bool, error)
	// RecordSubmission appends a submission record.
	RecordSubmission(ctx context.Context, s Submission) error
	// ListSubmissions returns recorded submissions newest first (CreatedAt DESC,
	// then a stable tiebreak), for the admin audit log. Read-only.
	ListSubmissions(ctx context.Context, f ListSubmissionsFilter) ([]Submission, error)
}

// Author is a managed author profile: the canonical, operator-owned identity the
// public site renders and the brain mirrors. Handle is the stable key and matches
// the articles.author column as a soft string reference (not a foreign key), so
// existing articles whose author predates this registry are unaffected. Deleted
// reports whether the author is soft-deleted (tombstoned); a tombstoned author is
// hidden from the default listing but kept for audit and re-activation.
type Author struct {
	ID        string
	Handle    string
	Name      string
	Bio       string
	Avatar    string
	Metadata  map[string]any
	Deleted   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AuthorStore is the managed-author registry. It is a separate concern from the
// article Repository (like SubmissionLog) so the article contract stays focused
// even when one adapter implements both. Writes go through the same single writer
// as the publish path.
type AuthorStore interface {
	// UpsertAuthor creates or updates an author keyed on Handle. On create it sets
	// CreatedAt and UpdatedAt from the supplied values (defaulting to now when
	// zero); on an existing handle it updates the mutable fields, advances
	// UpdatedAt, preserves the stored CreatedAt, and clears any tombstone
	// (re-activating a previously deleted handle). It returns the stored row.
	UpsertAuthor(ctx context.Context, a Author) (Author, error)
	// AuthorByHandle returns the author for the handle, or found=false. A
	// soft-deleted author is still returned (found=true, Deleted=true).
	AuthorByHandle(ctx context.Context, handle string) (Author, bool, error)
	// ListAuthors returns authors ordered by handle ascending in byte order, so
	// the two adapters agree. Tombstoned authors are excluded unless includeDeleted.
	ListAuthors(ctx context.Context, includeDeleted bool) ([]Author, error)
	// DeleteAuthor soft-deletes the author by setting a tombstone. Deleting an
	// absent handle returns ErrNotFound.
	DeleteAuthor(ctx context.Context, handle string) error
}

// Topic is a managed topic: the canonical, operator-owned topic the public site
// renders and the brain mirrors. Slug is the stable key and matches the
// article_topics.topic column as a soft string reference (not a foreign key), so
// existing topic memberships are unaffected. Deleted reports whether the topic is
// soft-deleted (tombstoned); a tombstoned topic is hidden from the default listing
// but kept for audit and re-activation.
type Topic struct {
	ID          string
	Slug        string
	Label       string
	Description string
	Metadata    map[string]any
	Deleted     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TopicStore is the managed-topic registry. It is a separate concern from the
// article Repository (like SubmissionLog and AuthorStore) so the article contract
// stays focused even when one adapter implements both. Writes go through the same
// single writer as the publish path.
type TopicStore interface {
	// UpsertTopic creates or updates a topic keyed on Slug. On create it sets
	// CreatedAt and UpdatedAt from the supplied values (defaulting to now when
	// zero); on an existing slug it updates the mutable fields, advances UpdatedAt,
	// preserves the stored CreatedAt, and clears any tombstone (re-activating a
	// previously deleted slug). It returns the stored row.
	UpsertTopic(ctx context.Context, t Topic) (Topic, error)
	// TopicBySlug returns the topic for the slug, or found=false. A soft-deleted
	// topic is still returned (found=true, Deleted=true).
	TopicBySlug(ctx context.Context, slug string) (Topic, bool, error)
	// ListTopics returns topics ordered by slug ascending in byte order, so the two
	// adapters agree. Tombstoned topics are excluded unless includeDeleted.
	ListTopics(ctx context.Context, includeDeleted bool) ([]Topic, error)
	// DeleteTopic soft-deletes the topic by setting a tombstone. Deleting an absent
	// slug returns ErrNotFound.
	DeleteTopic(ctx context.Context, slug string) error
}
