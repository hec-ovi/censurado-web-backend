// Package store defines the source-of-truth contract for articles. The interface
// is expressed purely in domain terms; no SQL, dialect, or storage detail leaks
// through it, so its callers (the generator in Layer 1, the HTTP layer in Layer 2)
// depend on behavior, not on storage internals. SQLite backs it; the shared
// conformance suite (see storetest) is the executable spec the implementation must
// pass.
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
// the Query and TitleSubtitleQuery fields are an admin-only widening (Phase 5):
// within a single plural axis the values OR (membership), and a scalar field and
// its plural counterpart are independent constraints that AND together (the
// plural never overrides the scalar). Blank or whitespace-only entries inside a
// slice are ignored, and a slice of only blanks is treated as no constraint.
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
	// case-sensitively. See buildSelect for why (SQLite's lower() folds ASCII only,
	// not unicode) and the exact contract.
	Query string

	// TitleSubtitleQuery is an admin-only substring filter over title OR
	// metadata.subtitle. It intentionally does not search the body, so the operator
	// article-search box can return only visible headline/deck matches.
	TitleSubtitleQuery string

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

// ArticleDay is one UTC publication date and the number of matching articles on
// that date. It feeds admin pagination without loading whole article pages.
type ArticleDay struct {
	Date  string
	Count int
}

// ArticleDayStore is the lightweight date-index read surface used by the admin
// panel. It is kept separate from Repository so the public article contract stays
// stable.
type ArticleDayStore interface {
	// ArticleDays returns distinct UTC publication dates for articles matching the
	// filter, ordered by Filter.Order. Paging fields on the filter are ignored.
	ArticleDays(ctx context.Context, f Filter) ([]ArticleDay, error)
}

// Facets is the distinct set of filter values present in the store, per axis,
// each with its article count. It is a read-only aggregate for the admin filter
// UI. Each slice is ordered Count DESC, then Value ASC, so the bytes are
// deterministic and stable run to run.
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
// stays focused, even though one store implements both.
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
// public site renders. Handle is the stable key and matches the articles.author
// column as a soft string reference (not a foreign key), so existing articles whose
// author predates this registry are unaffected. Gender, About, Style, and Topics are
// first-class profile columns an operator (or a self-serve human author) edits over
// the admin panel: About is the long public about-page text, Style is the author's
// private voice/writing prompt (a plain field the public site never renders), and
// Topics is the author's curated profile beats. Sources is the set of source slugs
// this author reads, held in the author_sources join and hydrated on read (it is not
// written by UpsertAuthor; use SetAuthorSources). Deleted reports whether the author
// is soft-deleted (tombstoned); a tombstoned author is hidden from the default listing
// but kept for audit and re-activation.
type Author struct {
	ID        string
	Handle    string
	Name      string
	Bio       string
	Avatar    string
	Gender    string
	About     string
	Style     string
	Topics    []string
	Sources   []string // attached source slugs; read-hydrated from the join, not written by UpsertAuthor
	Metadata  map[string]any
	Deleted   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AuthorStore is the managed-author registry. It is a separate concern from the
// article Repository (like SubmissionLog) so the article contract stays focused
// even though one store implements both. Writes go through the same single writer
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
	// the ordering is deterministic. Tombstoned authors are excluded unless includeDeleted.
	// Each returned Author has its Sources hydrated from the author_sources join.
	ListAuthors(ctx context.Context, includeDeleted bool) ([]Author, error)
	// DeleteAuthor soft-deletes the author by setting a tombstone. Deleting an
	// absent handle returns ErrNotFound.
	DeleteAuthor(ctx context.Context, handle string) error
	// SetAuthorSources replaces the author's attached-source set with sourceSlugs
	// (deduplicated, blanks dropped) in one transaction. Returns ErrNotFound when no
	// author has the handle, so a set never leaves orphan links. The slugs are soft
	// references to sources.slug (not a foreign key), so a source may be attached
	// before it is registered.
	SetAuthorSources(ctx context.Context, handle string, sourceSlugs []string) error
	// AuthorSources returns the author's attached-source slugs, ordered by slug in
	// byte order. An author with no links returns an empty slice; a missing handle
	// also returns an empty slice (the join carries no rows for it).
	AuthorSources(ctx context.Context, handle string) ([]string, error)
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
// stays focused even though one store implements both. Writes go through the same
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
	// ListTopics returns topics ordered by slug ascending in byte order, so the
	// ordering is deterministic. Tombstoned topics are excluded unless includeDeleted.
	ListTopics(ctx context.Context, includeDeleted bool) ([]Topic, error)
	// DeleteTopic soft-deletes the topic by setting a tombstone. Deleting an absent
	// slug returns ErrNotFound.
	DeleteTopic(ctx context.Context, slug string) error
}

// PortadaEntry is one slot on a day's front page: a reference to an article by
// slug plus its layout role. Role is "" for a normal entry and "important" for a
// full-row emphasis; the lead is entries[0]. Slug is a soft string reference to
// articles.slug (not a foreign key), so a front page can be planned before or
// independently of the article it points at.
type PortadaEntry struct {
	Slug string
	Role string // "" = normal, "important" = full-row emphasis; the lead is entries[0]
}

// PortadaDay is the operator-owned front-page plan for one calendar day: the
// ordered list of entries the site renders plus a Recomendado sidebar list. Date
// ("YYYY-MM-DD") is the stable, natural unique key, like Author.Handle and
// Topic.Slug. Deleted reports whether the day is soft-deleted (tombstoned); a
// tombstoned day is hidden from the default listing but kept for audit and
// re-activation.
type PortadaDay struct {
	Date        string // "YYYY-MM-DD", the natural unique key (like Author.Handle / Topic.Slug)
	Entries     []PortadaEntry
	Recomendado []string
	Deleted     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PortadaStore is the operator-owned front-page registry. It is a separate concern
// from the article Repository (like SubmissionLog, AuthorStore, and TopicStore) so
// the article contract stays focused even though one store implements them all.
// Writes go through the same single writer as the publish path.
type PortadaStore interface {
	// UpsertPortada creates or updates a day plan keyed on Date. On create it sets
	// CreatedAt and UpdatedAt from the supplied values (defaulting to now when
	// zero); on an existing date it replaces the entries and recomendado, advances
	// UpdatedAt, preserves the stored CreatedAt, and clears any tombstone
	// (re-activating a previously deleted date). It returns the stored row.
	UpsertPortada(ctx context.Context, p PortadaDay) (PortadaDay, error)
	// PortadaByDate returns the day plan for the date, or found=false. A soft-deleted
	// day is still returned (found=true, Deleted=true).
	PortadaByDate(ctx context.Context, date string) (PortadaDay, bool, error)
	// ListPortadas returns day plans ordered by date ascending in byte order, so the
	// ordering is deterministic. Tombstoned days are excluded unless includeDeleted.
	ListPortadas(ctx context.Context, includeDeleted bool) ([]PortadaDay, error)
	// DeletePortada soft-deletes the day plan by setting a tombstone. Deleting an
	// absent date returns ErrNotFound.
	DeletePortada(ctx context.Context, date string) error
}

// Source is a managed news source: an outlet the newsroom reads for grounding and
// cross-checking, and the political-lean axis the sourcing floor balances across.
// Slug is the stable key (the domain slugified, e.g. "example-com"), like
// Author.Handle and Topic.Slug; Domain is the bare registrable host and is unique.
// Lean is the cross-spectrum label ("right"|"neutral"|"left") and FeedType the
// discovery hint ("auto"|"native_rss"|"atom"|"news_sitemap"|"site_search"); the HTTP
// layer validates both. Enabled is the operator's discovery toggle (a disabled source
// stays registered but is skipped by discovery), distinct from Deleted, the tombstone
// that hides a removed source from the default listing while keeping it for audit and
// re-activation. Status/LastChecked/LastOK carry the health-checker's last result.
type Source struct {
	ID             string
	Slug           string
	Domain         string
	Homepage       string
	Description    string
	FeedURLs       []string
	FeedType       string
	Language       string
	OwnershipGroup string
	Lean           string
	Enabled        bool
	Status         string
	LastChecked    string
	LastOK         string
	Metadata       map[string]any
	Deleted        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SourceStore is the managed-source registry. It is a separate concern from the
// article Repository (like the author, topic, and portada registries) so the article
// contract stays focused even though one store implements them all. Writes go through
// the same single writer as the publish path.
type SourceStore interface {
	// UpsertSource creates or updates a source keyed on Slug. On create it sets
	// CreatedAt and UpdatedAt from the supplied values (defaulting to now when zero);
	// on an existing slug it updates the mutable fields, advances UpdatedAt, preserves
	// the stored CreatedAt, and clears any tombstone (re-activating a previously
	// deleted slug). It returns the stored row.
	UpsertSource(ctx context.Context, s Source) (Source, error)
	// SourceBySlug returns the source for the slug, or found=false. A soft-deleted
	// source is still returned (found=true, Deleted=true).
	SourceBySlug(ctx context.Context, slug string) (Source, bool, error)
	// ListSources returns sources ordered by slug ascending in byte order, so the
	// ordering is deterministic. Tombstoned sources are excluded unless includeDeleted.
	ListSources(ctx context.Context, includeDeleted bool) ([]Source, error)
	// DeleteSource soft-deletes the source by setting a tombstone AND detaches it from
	// every author (removing its author_sources rows) in one transaction, so no author
	// keeps reading a removed source. Deleting an absent slug returns ErrNotFound.
	DeleteSource(ctx context.Context, slug string) error
}
