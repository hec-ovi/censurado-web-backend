// Package postgres implements the store.Repository contract on PostgreSQL via
// the pgx stdlib driver. It is not the default store; it exists so the same
// conformance suite (storetest) runs against both SQLite and Postgres, proving
// the source of truth is swappable behind the repository interface rather than
// the swap being merely documented.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
)

//go:embed schema.sql
var schema string

// Store is a Postgres-backed Repository.
type Store struct {
	db *sql.DB
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type scanner interface {
	Scan(dest ...any) error
}

// execer is satisfied by both *sql.DB and *sql.Tx so a write helper runs the
// same statement in or out of a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// argBuilder assigns positional placeholders ($1, $2, ...) as args are added.
type argBuilder struct{ args []any }

func (a *argBuilder) next(v any) string {
	a.args = append(a.args, v)
	return "$" + strconv.Itoa(len(a.args))
}

// Open connects to Postgres using a pgx DSN (postgres://...), applies the
// schema, and verifies connectivity with a short retry to tolerate a database
// that is still starting up.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres open: %w", err)
	}
	var pingErr error
	for i := 0; i < 20; i++ {
		if pingErr = db.PingContext(context.Background()); pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pingErr != nil {
		db.Close()
		return nil, fmt.Errorf("postgres ping: %w", pingErr)
	}
	// Single-writer model, matching the SQLite adapter: one connection serializes
	// writes so two concurrent batches commit one after the other rather than
	// racing to a unique-constraint violation. The stated write volume (tens per
	// day, bursts to ~100/24h) is far below where one writer connection is a
	// bottleneck, and batch ingest is what keeps that cheap.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Upsert inserts the article, or returns the existing one when its content hash
// already exists (idempotent, deduplicated). Topics are written only on create.
func (s *Store) Upsert(ctx context.Context, a domain.Article) (store.UpsertResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.UpsertResult{}, err
	}
	defer tx.Rollback()

	stored, created, err := upsertTx(ctx, tx, a)
	if err != nil {
		return store.UpsertResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.UpsertResult{}, err
	}
	return store.UpsertResult{Article: stored, Created: created}, nil
}

// UpsertMany stores a batch of articles and their submission-ledger records in
// one transaction, mirroring the single path's Upsert + RecordSubmission per item
// but committing them together so the batch is atomic. Behavior is identical to
// the SQLite adapter (proven by the shared conformance suite): per item it reads
// the ledger first (a replayed key returns the prior article with no write), then
// upserts the article (content-hash deduplicated) and appends the submission
// record. A key reused for a different article rolls the whole batch back and
// returns *store.BatchConflictError.
func (s *Store) UpsertMany(ctx context.Context, items []store.UpsertItem) ([]store.UpsertResult, error) {
	results := make([]store.UpsertResult, len(items))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	for i, item := range items {
		prev, found, err := findSubmissionTx(ctx, tx, item.IdempotencyKey)
		if err != nil {
			return nil, fmt.Errorf("batch item %d: lookup: %w", i, err)
		}
		if found {
			if prev.ContentHash != item.Article.ContentHash {
				return nil, &store.BatchConflictError{Index: i, IdempotencyKey: item.IdempotencyKey}
			}
			results[i] = store.UpsertResult{
				Article: domain.Article{ID: prev.ArticleID, Slug: prev.Slug, ContentHash: prev.ContentHash},
				Created: false,
			}
			continue
		}

		stored, created, err := upsertTx(ctx, tx, item.Article)
		if err != nil {
			return nil, fmt.Errorf("batch item %d: %w", i, err)
		}
		if err := recordSubmissionTx(ctx, tx, store.Submission{
			IdempotencyKey: item.IdempotencyKey,
			ContentHash:    stored.ContentHash,
			ArticleID:      stored.ID,
			Slug:           stored.Slug,
			Author:         stored.Author,
			Scopes:         item.Scopes,
			CreatedAt:      stored.CreatedAt,
		}); err != nil {
			return nil, fmt.Errorf("batch item %d: audit: %w", i, err)
		}
		results[i] = store.UpsertResult{Article: stored, Created: created}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}

// upsertTx inserts the article and (on create) its topics within tx, returning
// the stored article and whether it was newly created. It is the per-item core
// shared by Upsert and UpsertMany so the SQL, dedup, and timestamp handling never
// drift between the single and batch paths.
func upsertTx(ctx context.Context, tx *sql.Tx, a domain.Article) (domain.Article, bool, error) {
	meta, err := marshalMeta(a.Metadata)
	if err != nil {
		return domain.Article{}, false, err
	}
	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO articles (slug,title,body,author,section,published_at,content_hash,metadata,created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)
		 ON CONFLICT (content_hash) DO NOTHING
		 RETURNING id`,
		a.Slug, a.Title, a.Body, a.Author, a.Section,
		// Whole-second resolution so both adapters return the identical instant
		// through the Repository interface. TIMESTAMPTZ would otherwise keep
		// microseconds while SQLite's RFC3339 drops them, diverging on the
		// sub-second inputs production feeds (domain.NewArticle uses time.Now()).
		a.PublishedAt.UTC().Truncate(time.Second), a.ContentHash, meta, a.CreatedAt.UTC().Truncate(time.Second),
	).Scan(&id)

	created := true
	if errors.Is(err, sql.ErrNoRows) {
		created = false
		if err = tx.QueryRowContext(ctx,
			`SELECT id FROM articles WHERE content_hash = $1`, a.ContentHash).Scan(&id); err != nil {
			return domain.Article{}, false, fmt.Errorf("lookup existing: %w", err)
		}
	} else if err != nil {
		return domain.Article{}, false, fmt.Errorf("insert article: %w", err)
	}

	if created {
		for _, topic := range a.Topics {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO article_topics (article_id,topic) VALUES ($1,$2)`, id, topic); err != nil {
				return domain.Article{}, false, fmt.Errorf("insert topic: %w", err)
			}
		}
	}

	stored, err := getByID(ctx, tx, id)
	if err != nil {
		return domain.Article{}, false, err
	}
	return stored, created, nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint violation
// (SQLSTATE 23505). In UpdateArticle the only UNIQUE column the update touches is
// content_hash (the slug is preserved), so a unique violation there is a
// content-hash collision.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// UpdateArticle mutates the article identified by a.Slug in place: id, slug, and
// created_at (and the tombstone) are preserved, the mutable fields are replaced,
// and the topic set is rewritten, all in one transaction. Behavior is identical to
// the SQLite adapter (proven by the shared conformance suite). Returns
// store.ErrNotFound when no row has the slug and *store.EditConflictError when the
// new content hash collides with a different article.
func (s *Store) UpdateArticle(ctx context.Context, a domain.Article) (domain.Article, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Article{}, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM articles WHERE slug = $1`, a.Slug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Article{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Article{}, err
	}

	meta, err := marshalMeta(a.Metadata)
	if err != nil {
		return domain.Article{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE articles SET title=$1, body=$2, author=$3, section=$4, published_at=$5, content_hash=$6, metadata=$7::jsonb WHERE id=$8`,
		a.Title, a.Body, a.Author, a.Section,
		a.PublishedAt.UTC().Truncate(time.Second), a.ContentHash, meta, id,
	); err != nil {
		if isUniqueViolation(err) {
			return domain.Article{}, &store.EditConflictError{Slug: a.Slug, ContentHash: a.ContentHash}
		}
		return domain.Article{}, fmt.Errorf("update article: %w", err)
	}

	// Replace the topic set so an edit that adds or removes a topic is reflected
	// exactly, within the same transaction.
	if _, err := tx.ExecContext(ctx, `DELETE FROM article_topics WHERE article_id = $1`, id); err != nil {
		return domain.Article{}, fmt.Errorf("clear topics: %w", err)
	}
	for _, topic := range a.Topics {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO article_topics (article_id,topic) VALUES ($1,$2)`, id, topic); err != nil {
			return domain.Article{}, fmt.Errorf("insert topic: %w", err)
		}
	}

	stored, err := getByID(ctx, tx, id)
	if err != nil {
		return domain.Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Article{}, err
	}
	return stored, nil
}

// DeleteArticle soft-deletes the article by stamping a whole-second RFC3339
// tombstone (TEXT, matching the SQLite adapter), so it is hidden from the default
// Find/Count but kept for audit and restore. Returns store.ErrNotFound for an
// absent slug; idempotent on an already-deleted row.
func (s *Store) DeleteArticle(ctx context.Context, slug string) error {
	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `UPDATE articles SET deleted_at = $1 WHERE slug = $2`, now, slug)
	if err != nil {
		return fmt.Errorf("delete article: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// RestoreArticle clears the tombstone so a soft-deleted article is active again.
// Returns store.ErrNotFound for an absent slug.
func (s *Store) RestoreArticle(ctx context.Context, slug string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE articles SET deleted_at = '' WHERE slug = $1`, slug)
	if err != nil {
		return fmt.Errorf("restore article: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// BySlug returns the article with the given slug, or store.ErrNotFound.
func (s *Store) BySlug(ctx context.Context, slug string) (domain.Article, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM articles WHERE slug = $1`, slug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Article{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Article{}, err
	}
	return getByID(ctx, s.db, id)
}

// Find returns articles matching the filter, ordered and paged.
func (s *Store) Find(ctx context.Context, f store.Filter) ([]domain.Article, error) {
	query, args := buildSelect(f, false)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var arts []domain.Article
	var ids []int64
	for rows.Next() {
		a, id, err := scanArticle(rows)
		if err != nil {
			return nil, err
		}
		arts = append(arts, a)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	topics, err := loadTopics(ctx, s.db, ids)
	if err != nil {
		return nil, err
	}
	for i := range arts {
		if tp := topics[ids[i]]; tp != nil {
			arts[i].Topics = tp
		}
	}
	return arts, nil
}

// Count returns how many articles match the filter, ignoring paging.
func (s *Store) Count(ctx context.Context, f store.Filter) (int, error) {
	query, args := buildSelect(f, true)
	var n int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Facets returns the distinct section, author, and topic values present with
// their article counts, ordered Count DESC then Value ASC so the bytes are
// deterministic and identical to the SQLite adapter.
//
// The value tie-break is COLLATE "C" (raw byte order) on purpose: SQLite's TEXT
// columns sort BINARY by default, while Postgres would otherwise use the
// database's locale collation, which can order mixed-case or accented values
// differently. Pinning "C" makes the ordering byte-identical across engines for
// ANY value set, not just the ASCII lowercase ones the conformance suite seeds.
func (s *Store) Facets(ctx context.Context) (store.Facets, error) {
	// Tombstoned articles are excluded so the facet counts match what Find returns:
	// a soft-deleted article is hidden from the public site, so it must not inflate
	// the admin filter counts either.
	sections, err := facetRows(ctx, s.db,
		`SELECT section, COUNT(*) FROM articles WHERE deleted_at = '' GROUP BY section ORDER BY COUNT(*) DESC, section COLLATE "C" ASC`)
	if err != nil {
		return store.Facets{}, fmt.Errorf("facets sections: %w", err)
	}
	authors, err := facetRows(ctx, s.db,
		`SELECT author, COUNT(*) FROM articles WHERE deleted_at = '' GROUP BY author ORDER BY COUNT(*) DESC, author COLLATE "C" ASC`)
	if err != nil {
		return store.Facets{}, fmt.Errorf("facets authors: %w", err)
	}
	// Topic counts are per-article (one row per article carrying the topic); the
	// join table's (article_id, topic) primary key already forbids duplicates, so
	// COUNT(*) cannot double-count a single article on one topic. The join to
	// articles drops a tombstoned article's topics from the counts.
	topics, err := facetRows(ctx, s.db,
		`SELECT topic, COUNT(*) FROM article_topics t JOIN articles a ON a.id = t.article_id WHERE a.deleted_at = '' GROUP BY topic ORDER BY COUNT(*) DESC, topic COLLATE "C" ASC`)
	if err != nil {
		return store.Facets{}, fmt.Errorf("facets topics: %w", err)
	}
	return store.Facets{Sections: sections, Authors: authors, Topics: topics}, nil
}

func facetRows(ctx context.Context, q querier, query string) ([]store.Facet, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Facet
	for rows.Next() {
		var f store.Facet
		if err := rows.Scan(&f.Value, &f.Count); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func buildSelect(f store.Filter, count bool) (string, []any) {
	var b strings.Builder
	ab := &argBuilder{}
	if count {
		b.WriteString("SELECT COUNT(*) FROM articles a")
	} else {
		b.WriteString("SELECT a.id,a.slug,a.title,a.body,a.author,a.section,a.published_at,a.content_hash,a.metadata,a.created_at,a.deleted_at FROM articles a")
	}
	if f.Topic != "" {
		fmt.Fprintf(&b, " JOIN article_topics t ON t.article_id = a.id AND t.topic = %s", ab.next(f.Topic))
	}
	b.WriteString(" WHERE 1=1")
	// Soft-deleted rows are hidden by default; the admin opts into them. The public
	// generator passes the zero Filter and so never sees a tombstoned article.
	if !f.IncludeDeleted {
		b.WriteString(" AND a.deleted_at = ''")
	}
	if f.Section != "" {
		fmt.Fprintf(&b, " AND a.section = %s", ab.next(f.Section))
	}
	if f.Author != "" {
		fmt.Fprintf(&b, " AND a.author = %s", ab.next(f.Author))
	}
	// Date bounds are second-resolution: stored timestamps are whole-second and the
	// SQLite adapter binds them as RFC3339 (which drops sub-second), so truncate the
	// bounds here too so a fractional bound returns the identical rows on both engines.
	if !f.From.IsZero() {
		fmt.Fprintf(&b, " AND a.published_at >= %s", ab.next(f.From.UTC().Truncate(time.Second)))
	}
	if !f.To.IsZero() {
		fmt.Fprintf(&b, " AND a.published_at < %s", ab.next(f.To.UTC().Truncate(time.Second)))
	}
	// Multi-value axes (admin only). Each ANDs with the scalar fields above and
	// ORs within itself via IN/EXISTS. Blank entries are dropped first; an
	// all-blank slice imposes no constraint. All values are bound via $n.
	if vals := nonBlank(f.Sections); len(vals) > 0 {
		fmt.Fprintf(&b, " AND a.section IN (%s)", inList(ab, vals))
	}
	if vals := nonBlank(f.Authors); len(vals) > 0 {
		fmt.Fprintf(&b, " AND a.author IN (%s)", inList(ab, vals))
	}
	if vals := nonBlank(f.Topics); len(vals) > 0 {
		// EXISTS (not a JOIN) so an article matching several of the values is
		// returned once; coexists with the scalar Topic JOIN above (both AND).
		fmt.Fprintf(&b, " AND EXISTS (SELECT 1 FROM article_topics att WHERE att.article_id = a.id AND att.topic IN (%s))", inList(ab, vals))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		// Full-text substring over title OR body. LIKE wildcards in the user value
		// are escaped in Go (likeEscape) and the backslash is declared as the
		// ESCAPE char, so '%'/'_' are matched literally, never as wildcards. We use
		// lower()+LIKE (NOT ILIKE) so the case-folding is symmetric with SQLite.
		// standard_conforming_strings is on by default (pg17), so the '\' literal
		// in ESCAPE '\' is a single backslash, matching the bound pattern.
		//
		// ACCEPTED BOUNDARY: this is ASCII-case-insensitive only. Postgres's lower()
		// is unicode-aware while SQLite's folds ASCII only, so a non-ASCII case fold
		// (e.g. 'É' vs 'é') would DIVERGE between the engines. The contract is
		// therefore: case-insensitive for ASCII; non-ASCII (accented) letters match
		// case-sensitively. An exact non-ASCII substring in the same case DOES match
		// and is safe to rely on across both engines.
		pat := "%" + likeEscape(q) + "%"
		fmt.Fprintf(&b, " AND (lower(a.title) LIKE lower(%s) ESCAPE '\\' OR lower(a.body) LIKE lower(%s) ESCAPE '\\')", ab.next(pat), ab.next(pat))
	}
	if count {
		return b.String(), ab.args
	}
	dir := "DESC"
	if f.Order == store.OldestFirst {
		dir = "ASC"
	}
	fmt.Fprintf(&b, " ORDER BY a.published_at %s, a.id %s", dir, dir)
	if f.Limit > 0 {
		fmt.Fprintf(&b, " LIMIT %s", ab.next(f.Limit))
	}
	if f.Offset > 0 {
		fmt.Fprintf(&b, " OFFSET %s", ab.next(f.Offset))
	}
	return b.String(), ab.args
}

// nonBlank returns the entries of in that are not empty or whitespace-only,
// preserving their original (untrimmed) value so membership matches the exact
// stored column value, consistent with the scalar equality predicates.
func nonBlank(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// inList binds each value through the argBuilder and returns the comma-joined
// "$n,$m,..." placeholder list for an IN clause.
func inList(ab *argBuilder, vals []string) string {
	ph := make([]string, len(vals))
	for i, v := range vals {
		ph[i] = ab.next(v)
	}
	return strings.Join(ph, ",")
}

// likeEscape escapes the LIKE wildcards in a user value so it is matched as a
// literal substring under "ESCAPE '\'". The escape char is escaped first, then
// the '%' and '_' metacharacters. Identical to the SQLite adapter's escaping so
// the two engines bind byte-identical patterns.
func likeEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

func getByID(ctx context.Context, q querier, id int64) (domain.Article, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id,slug,title,body,author,section,published_at,content_hash,metadata,created_at,deleted_at
		 FROM articles WHERE id = $1`, id)
	a, _, err := scanArticle(row)
	if err != nil {
		return domain.Article{}, err
	}
	topics, err := loadTopics(ctx, q, []int64{id})
	if err != nil {
		return domain.Article{}, err
	}
	if tp := topics[id]; tp != nil {
		a.Topics = tp
	}
	return a, nil
}

func scanArticle(sc scanner) (domain.Article, int64, error) {
	var (
		id                                       int64
		slug, title, body, author, section, hash string
		deleted                                  string
		pub, created                             time.Time
		meta                                     []byte
	)
	if err := sc.Scan(&id, &slug, &title, &body, &author, &section, &pub, &hash, &meta, &created, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Article{}, 0, store.ErrNotFound
		}
		return domain.Article{}, 0, err
	}
	m, err := unmarshalMeta(meta)
	if err != nil {
		return domain.Article{}, 0, err
	}
	return domain.Article{
		ID:          strconv.FormatInt(id, 10),
		Slug:        slug,
		Title:       title,
		Body:        body,
		Author:      author,
		Section:     section,
		PublishedAt: pub.UTC(),
		ContentHash: hash,
		Metadata:    m,
		Deleted:     deleted != "",
		CreatedAt:   created.UTC(),
		Topics:      []string{},
	}, id, nil
}

func loadTopics(ctx context.Context, q querier, ids []int64) (map[int64][]string, error) {
	out := make(map[int64][]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ab := &argBuilder{}
	ph := make([]string, len(ids))
	for i, id := range ids {
		ph[i] = ab.next(id)
	}
	rows, err := q.QueryContext(ctx,
		`SELECT article_id, topic FROM article_topics WHERE article_id IN (`+strings.Join(ph, ",")+`) ORDER BY article_id, topic`,
		ab.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var aid int64
		var topic string
		if err := rows.Scan(&aid, &topic); err != nil {
			return nil, err
		}
		out[aid] = append(out[aid], topic)
	}
	return out, rows.Err()
}

const submissionCols = "idempotency_key,content_hash,article_id,slug,author,scopes,created_at"

// scanSubmission decodes one submissions row. Scopes are space-joined on write
// (RecordSubmission), so they split back on the same separator; created_at is a
// TIMESTAMPTZ normalized to UTC. Shared by FindSubmission and ListSubmissions so
// every read path round-trips a submission identically to the SQLite adapter.
func scanSubmission(sc scanner) (store.Submission, error) {
	var sub store.Submission
	var scopes string
	var created time.Time
	if err := sc.Scan(&sub.IdempotencyKey, &sub.ContentHash, &sub.ArticleID, &sub.Slug, &sub.Author, &scopes, &created); err != nil {
		return store.Submission{}, err
	}
	if scopes != "" {
		sub.Scopes = strings.Split(scopes, " ")
	}
	sub.CreatedAt = created.UTC()
	return sub, nil
}

// FindSubmission returns a prior submission for the idempotency key, or found=false.
func (s *Store) FindSubmission(ctx context.Context, key string) (store.Submission, bool, error) {
	return findSubmissionTx(ctx, s.db, key)
}

// findSubmissionTx looks up a submission by key using q, which may be the *sql.DB
// or an open *sql.Tx, so the batch path can read the ledger inside its own
// transaction exactly as FindSubmission reads it outside one.
func findSubmissionTx(ctx context.Context, q querier, key string) (store.Submission, bool, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+submissionCols+` FROM submissions WHERE idempotency_key = $1`, key)
	sub, err := scanSubmission(row)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Submission{}, false, nil
	}
	if err != nil {
		return store.Submission{}, false, err
	}
	return sub, true, nil
}

// ListSubmissions returns recorded submissions newest first for the admin audit
// log. Ordering is created_at DESC then idempotency_key DESC (the table's primary
// key) so it is deterministic, and identical to the SQLite adapter. The
// idempotency_key tiebreak is pinned to COLLATE "C" (raw byte order) so it
// matches SQLite's default BINARY collation for ANY key set, not just the ASCII
// ones the conformance suite seeds. The Author filter is exact equality; From/To
// bound created_at (>= From, < To). All values are bound via $n.
func (s *Store) ListSubmissions(ctx context.Context, f store.ListSubmissionsFilter) ([]store.Submission, error) {
	var b strings.Builder
	ab := &argBuilder{}
	b.WriteString("SELECT " + submissionCols + " FROM submissions WHERE 1=1")
	if f.Author != "" {
		fmt.Fprintf(&b, " AND author = %s", ab.next(f.Author))
	}
	if !f.From.IsZero() {
		fmt.Fprintf(&b, " AND created_at >= %s", ab.next(f.From.UTC().Truncate(time.Second)))
	}
	if !f.To.IsZero() {
		fmt.Fprintf(&b, " AND created_at < %s", ab.next(f.To.UTC().Truncate(time.Second)))
	}
	b.WriteString(` ORDER BY created_at DESC, idempotency_key COLLATE "C" DESC`)
	if f.Limit > 0 {
		fmt.Fprintf(&b, " LIMIT %s", ab.next(f.Limit))
	}
	if f.Offset > 0 {
		fmt.Fprintf(&b, " OFFSET %s", ab.next(f.Offset))
	}
	rows, err := s.db.QueryContext(ctx, b.String(), ab.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Submission
	for rows.Next() {
		sub, err := scanSubmission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// RecordSubmission appends a submission record (audit log + idempotency ledger).
func (s *Store) RecordSubmission(ctx context.Context, sub store.Submission) error {
	return recordSubmissionTx(ctx, s.db, sub)
}

// recordSubmissionTx appends a submission record using ex, which may be the
// *sql.DB or an open *sql.Tx, so the batch path writes the ledger inside the same
// transaction as the article and topics.
func recordSubmissionTx(ctx context.Context, ex execer, sub store.Submission) error {
	_, err := ex.ExecContext(ctx,
		`INSERT INTO submissions (idempotency_key,content_hash,article_id,slug,author,scopes,created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		sub.IdempotencyKey, sub.ContentHash, sub.ArticleID, sub.Slug, sub.Author,
		// Truncate to whole seconds so both adapters return the identical instant
		// through the SubmissionLog interface. The caller writes time.Now().UTC()
		// (sub-second), and TIMESTAMPTZ would otherwise keep microseconds while
		// SQLite's RFC3339 drops them, diverging on the exact inputs production feeds.
		strings.Join(sub.Scopes, " "), sub.CreatedAt.UTC().Truncate(time.Second))
	if err != nil {
		return fmt.Errorf("record submission: %w", err)
	}
	return nil
}

func marshalMeta(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal metadata: %w", err)
	}
	return string(b), nil
}

func unmarshalMeta(b []byte) (map[string]any, error) {
	if len(b) == 0 || string(b) == "{}" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	return m, nil
}

const authorCols = "id,handle,name,bio,avatar,gender,about,style,topics,metadata,deleted_at,created_at,updated_at"

// scanAuthor decodes one authors row. metadata and topics are JSONB (scanned as
// bytes) and round-trip through the shared marshal/unmarshal helpers; created_at/
// updated_at/deleted_at are whole-second RFC3339 TEXT (deleted_at "" means active), so
// Deleted is derived from the tombstone being non-empty. Sources is NOT scanned here
// (it lives in the author_sources join); AuthorByHandle and ListAuthors hydrate it
// after the row scan. Identical observable result to the SQLite adapter.
func scanAuthor(sc scanner) (store.Author, error) {
	var (
		id                                       int64
		handle, name, bio, avatar, gender, about string
		style                                    string
		deleted, created, updated                string
		topics, meta                             []byte
	)
	if err := sc.Scan(&id, &handle, &name, &bio, &avatar, &gender, &about, &style, &topics, &meta, &deleted, &created, &updated); err != nil {
		return store.Author{}, err
	}
	m, err := unmarshalMeta(meta)
	if err != nil {
		return store.Author{}, err
	}
	tp, err := unmarshalStrings(topics)
	if err != nil {
		return store.Author{}, fmt.Errorf("unmarshal author topics: %w", err)
	}
	createdT, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return store.Author{}, fmt.Errorf("parse author created_at: %w", err)
	}
	updatedT, err := time.Parse(time.RFC3339, updated)
	if err != nil {
		return store.Author{}, fmt.Errorf("parse author updated_at: %w", err)
	}
	return store.Author{
		ID:        strconv.FormatInt(id, 10),
		Handle:    handle,
		Name:      name,
		Bio:       bio,
		Avatar:    avatar,
		Gender:    gender,
		About:     about,
		Style:     style,
		Topics:    tp,
		Metadata:  m,
		Deleted:   deleted != "",
		CreatedAt: createdT,
		UpdatedAt: updatedT,
	}, nil
}

// UpsertAuthor creates or updates an author keyed on handle. On create it writes
// the supplied CreatedAt/UpdatedAt (defaulting to now when zero); on an existing
// handle it updates the mutable fields (including the gender/about/style/topics
// profile columns), advances updated_at, preserves created_at, and clears the
// tombstone so a previously deleted handle is re-activated. It does NOT touch the
// author_sources join; use SetAuthorSources. Behavior is identical to the SQLite
// adapter (proven by the shared conformance suite).
func (s *Store) UpsertAuthor(ctx context.Context, a store.Author) (store.Author, error) {
	meta, err := marshalMeta(a.Metadata)
	if err != nil {
		return store.Author{}, err
	}
	topics, err := marshalStrings(a.Topics)
	if err != nil {
		return store.Author{}, fmt.Errorf("marshal author topics: %w", err)
	}
	created := a.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	updated := a.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO authors (handle,name,bio,avatar,gender,about,style,topics,metadata,deleted_at,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,'',$10,$11)
		 ON CONFLICT (handle) DO UPDATE SET
		   name=excluded.name, bio=excluded.bio, avatar=excluded.avatar,
		   gender=excluded.gender, about=excluded.about, style=excluded.style, topics=excluded.topics,
		   metadata=excluded.metadata, deleted_at='', updated_at=excluded.updated_at`,
		a.Handle, a.Name, a.Bio, a.Avatar, a.Gender, a.About, a.Style, topics, meta,
		created.UTC().Truncate(time.Second).Format(time.RFC3339),
		updated.UTC().Truncate(time.Second).Format(time.RFC3339),
	); err != nil {
		return store.Author{}, fmt.Errorf("upsert author: %w", err)
	}
	got, _, err := s.AuthorByHandle(ctx, a.Handle)
	if err != nil {
		return store.Author{}, err
	}
	return got, nil
}

// AuthorByHandle returns the author for the handle, or found=false. A soft-deleted
// author is still returned (found=true, Deleted=true) so callers can re-activate it.
// The returned author has its Sources hydrated from the author_sources join.
func (s *Store) AuthorByHandle(ctx context.Context, handle string) (store.Author, bool, error) {
	a, err := scanAuthor(s.db.QueryRowContext(ctx,
		`SELECT `+authorCols+` FROM authors WHERE handle = $1`, handle))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Author{}, false, nil
	}
	if err != nil {
		return store.Author{}, false, err
	}
	srcs, err := loadAuthorSources(ctx, s.db, []string{a.Handle})
	if err != nil {
		return store.Author{}, false, err
	}
	a.Sources = srcs[a.Handle]
	return a, true, nil
}

// ListAuthors returns authors ordered by handle ascending. The tie-break is pinned
// to COLLATE "C" (raw byte order) so it matches SQLite's default BINARY collation
// for any handle set. Tombstoned authors are excluded unless includeDeleted. Each
// returned author has its Sources hydrated from the author_sources join.
func (s *Store) ListAuthors(ctx context.Context, includeDeleted bool) ([]store.Author, error) {
	q := `SELECT ` + authorCols + ` FROM authors`
	if !includeDeleted {
		q += ` WHERE deleted_at = ''`
	}
	q += ` ORDER BY handle COLLATE "C" ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Author
	var handles []string
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
		handles = append(handles, a.Handle)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	srcs, err := loadAuthorSources(ctx, s.db, handles)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Sources = srcs[out[i].Handle]
	}
	return out, nil
}

// DeleteAuthor soft-deletes the author by setting a whole-second RFC3339 tombstone.
// The author's source links are left intact so a restore (re-upsert) brings them
// back; DeleteSource is the path that severs a link. Deleting an absent handle
// returns store.ErrNotFound.
func (s *Store) DeleteAuthor(ctx context.Context, handle string) error {
	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE authors SET deleted_at = $1, updated_at = $2 WHERE handle = $3`, now, now, handle)
	if err != nil {
		return fmt.Errorf("delete author: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// loadAuthorSources returns, per author handle, the attached source slugs ordered by
// slug (COLLATE "C" to match SQLite's BINARY default). One IN-query hydrates many
// authors, mirroring loadTopics, so ListAuthors does not fan out a query per row. An
// empty handle set returns an empty map.
func loadAuthorSources(ctx context.Context, q querier, handles []string) (map[string][]string, error) {
	out := make(map[string][]string, len(handles))
	if len(handles) == 0 {
		return out, nil
	}
	ab := &argBuilder{}
	ph := make([]string, len(handles))
	for i, h := range handles {
		ph[i] = ab.next(h)
	}
	rows, err := q.QueryContext(ctx,
		`SELECT author_handle, source_slug FROM author_sources WHERE author_handle IN (`+strings.Join(ph, ",")+`) ORDER BY author_handle COLLATE "C", source_slug COLLATE "C"`,
		ab.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var handle, slug string
		if err := rows.Scan(&handle, &slug); err != nil {
			return nil, err
		}
		out[handle] = append(out[handle], slug)
	}
	return out, rows.Err()
}

// dedupeNonBlank returns the entries of in that are not blank, deduplicated with
// first-seen order preserved, so a repeated or blank source slug is dropped before it
// reaches the author_sources primary key. Identical to the SQLite adapter's helper.
func dedupeNonBlank(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// SetAuthorSources replaces the author's attached-source set in one transaction. It
// verifies the author exists (ErrNotFound otherwise), then clears and re-inserts the
// deduplicated, non-blank slugs. Behavior is identical to the SQLite adapter.
func (s *Store) SetAuthorSources(ctx context.Context, handle string, sourceSlugs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM authors WHERE handle = $1`, handle).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM author_sources WHERE author_handle = $1`, handle); err != nil {
		return fmt.Errorf("clear author sources: %w", err)
	}
	for _, slug := range dedupeNonBlank(sourceSlugs) {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO author_sources (author_handle,source_slug) VALUES ($1,$2)`, handle, slug); err != nil {
			return fmt.Errorf("insert author source: %w", err)
		}
	}
	return tx.Commit()
}

// AuthorSources returns the author's attached-source slugs ordered by slug. A missing
// handle or an author with no links returns an empty slice.
func (s *Store) AuthorSources(ctx context.Context, handle string) ([]string, error) {
	srcs, err := loadAuthorSources(ctx, s.db, []string{handle})
	if err != nil {
		return nil, err
	}
	if got := srcs[handle]; got != nil {
		return got, nil
	}
	return []string{}, nil
}

const topicCols = "id,slug,label,description,metadata,deleted_at,created_at,updated_at"

// scanTopic decodes one topics row. metadata is JSONB (scanned as bytes) and
// round-trips through the shared marshal/unmarshal helpers; the timestamps are
// whole-second RFC3339 TEXT (deleted_at "" means active). Identical observable
// result to the SQLite adapter.
func scanTopic(sc scanner) (store.Topic, error) {
	var (
		id                        int64
		slug, label, desc         string
		deleted, created, updated string
		meta                      []byte
	)
	if err := sc.Scan(&id, &slug, &label, &desc, &meta, &deleted, &created, &updated); err != nil {
		return store.Topic{}, err
	}
	m, err := unmarshalMeta(meta)
	if err != nil {
		return store.Topic{}, err
	}
	createdT, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return store.Topic{}, fmt.Errorf("parse topic created_at: %w", err)
	}
	updatedT, err := time.Parse(time.RFC3339, updated)
	if err != nil {
		return store.Topic{}, fmt.Errorf("parse topic updated_at: %w", err)
	}
	return store.Topic{
		ID:          strconv.FormatInt(id, 10),
		Slug:        slug,
		Label:       label,
		Description: desc,
		Metadata:    m,
		Deleted:     deleted != "",
		CreatedAt:   createdT,
		UpdatedAt:   updatedT,
	}, nil
}

// UpsertTopic creates or updates a topic keyed on slug. Behavior is identical to
// the SQLite adapter (proven by the shared conformance suite): an existing slug is
// updated in place, updated_at advances, created_at is preserved, and the tombstone
// is cleared so a previously deleted slug is re-activated.
func (s *Store) UpsertTopic(ctx context.Context, t store.Topic) (store.Topic, error) {
	meta, err := marshalMeta(t.Metadata)
	if err != nil {
		return store.Topic{}, err
	}
	created := t.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	updated := t.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO topics (slug,label,description,metadata,deleted_at,created_at,updated_at)
		 VALUES ($1,$2,$3,$4::jsonb,'',$5,$6)
		 ON CONFLICT (slug) DO UPDATE SET
		   label=excluded.label, description=excluded.description,
		   metadata=excluded.metadata, deleted_at='', updated_at=excluded.updated_at`,
		t.Slug, t.Label, t.Description, meta,
		created.UTC().Truncate(time.Second).Format(time.RFC3339),
		updated.UTC().Truncate(time.Second).Format(time.RFC3339),
	); err != nil {
		return store.Topic{}, fmt.Errorf("upsert topic: %w", err)
	}
	got, _, err := s.TopicBySlug(ctx, t.Slug)
	if err != nil {
		return store.Topic{}, err
	}
	return got, nil
}

// TopicBySlug returns the topic for the slug, or found=false. A soft-deleted topic
// is still returned (found=true, Deleted=true) so callers can re-activate it.
func (s *Store) TopicBySlug(ctx context.Context, slug string) (store.Topic, bool, error) {
	tp, err := scanTopic(s.db.QueryRowContext(ctx,
		`SELECT `+topicCols+` FROM topics WHERE slug = $1`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Topic{}, false, nil
	}
	if err != nil {
		return store.Topic{}, false, err
	}
	return tp, true, nil
}

// ListTopics returns topics ordered by slug ascending. The order is pinned to
// COLLATE "C" (raw byte order) so it matches SQLite's default BINARY collation for
// any slug set. Tombstoned topics are excluded unless includeDeleted.
func (s *Store) ListTopics(ctx context.Context, includeDeleted bool) ([]store.Topic, error) {
	q := `SELECT ` + topicCols + ` FROM topics`
	if !includeDeleted {
		q += ` WHERE deleted_at = ''`
	}
	q += ` ORDER BY slug COLLATE "C" ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Topic
	for rows.Next() {
		tp, err := scanTopic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tp)
	}
	return out, rows.Err()
}

// DeleteTopic soft-deletes the topic by setting a whole-second RFC3339 tombstone.
// Deleting an absent slug returns store.ErrNotFound.
func (s *Store) DeleteTopic(ctx context.Context, slug string) error {
	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE topics SET deleted_at = $1, updated_at = $2 WHERE slug = $3`, now, now, slug)
	if err != nil {
		return fmt.Errorf("delete topic: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

const portadaCols = "date,entries,recomendado,deleted_at,created_at,updated_at"

// portadaEntryRow is the on-disk JSON shape of a store.PortadaEntry, byte-identical
// to the SQLite adapter's. role is omitted when empty so a normal entry stays compact.
type portadaEntryRow struct {
	Slug string `json:"slug"`
	Role string `json:"role,omitempty"`
}

// marshalEntries encodes the ordered entries as a JSON array, defaulting to "[]"
// (never "null"). The {slug, role} shape matches the SQLite adapter so a row
// written by one reads the same in the other.
func marshalEntries(entries []store.PortadaEntry) (string, error) {
	if len(entries) == 0 {
		return "[]", nil
	}
	rows := make([]portadaEntryRow, len(entries))
	for i, e := range entries {
		rows[i] = portadaEntryRow{Slug: e.Slug, Role: e.Role}
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return "", fmt.Errorf("marshal entries: %w", err)
	}
	return string(b), nil
}

func unmarshalEntries(b []byte) ([]store.PortadaEntry, error) {
	if len(b) == 0 || string(b) == "[]" {
		return []store.PortadaEntry{}, nil
	}
	var rows []portadaEntryRow
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("unmarshal entries: %w", err)
	}
	out := make([]store.PortadaEntry, len(rows))
	for i, r := range rows {
		out[i] = store.PortadaEntry{Slug: r.Slug, Role: r.Role}
	}
	return out, nil
}

// marshalStrings encodes a slug list as a JSON array, defaulting to "[]".
func marshalStrings(ss []string) (string, error) {
	if len(ss) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return "", fmt.Errorf("marshal strings: %w", err)
	}
	return string(b), nil
}

func unmarshalStrings(b []byte) ([]string, error) {
	if len(b) == 0 || string(b) == "[]" {
		return []string{}, nil
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("unmarshal strings: %w", err)
	}
	return out, nil
}

// scanPortada decodes one portadas row. entries and recomendado are JSONB (scanned
// as bytes) and round-trip through the JSON helpers; the timestamps are whole-second
// RFC3339 TEXT (deleted_at "" means active). Identical observable result to the
// SQLite adapter. Shared by PortadaByDate and ListPortadas.
func scanPortada(sc scanner) (store.PortadaDay, error) {
	var (
		date                      string
		entries, recomendado      []byte
		deleted, created, updated string
	)
	if err := sc.Scan(&date, &entries, &recomendado, &deleted, &created, &updated); err != nil {
		return store.PortadaDay{}, err
	}
	es, err := unmarshalEntries(entries)
	if err != nil {
		return store.PortadaDay{}, err
	}
	rec, err := unmarshalStrings(recomendado)
	if err != nil {
		return store.PortadaDay{}, err
	}
	createdT, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return store.PortadaDay{}, fmt.Errorf("parse portada created_at: %w", err)
	}
	updatedT, err := time.Parse(time.RFC3339, updated)
	if err != nil {
		return store.PortadaDay{}, fmt.Errorf("parse portada updated_at: %w", err)
	}
	return store.PortadaDay{
		Date:        date,
		Entries:     es,
		Recomendado: rec,
		Deleted:     deleted != "",
		CreatedAt:   createdT,
		UpdatedAt:   updatedT,
	}, nil
}

// UpsertPortada creates or updates a day plan keyed on date. Behavior is identical
// to the SQLite adapter (proven by the shared conformance suite): an existing date
// has its entries and recomendado replaced wholesale, updated_at advances,
// created_at is preserved, and the tombstone is cleared so a previously deleted
// date is re-activated.
func (s *Store) UpsertPortada(ctx context.Context, p store.PortadaDay) (store.PortadaDay, error) {
	entries, err := marshalEntries(p.Entries)
	if err != nil {
		return store.PortadaDay{}, err
	}
	rec, err := marshalStrings(p.Recomendado)
	if err != nil {
		return store.PortadaDay{}, err
	}
	created := p.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	updated := p.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO portadas (date,entries,recomendado,deleted_at,created_at,updated_at)
		 VALUES ($1,$2::jsonb,$3::jsonb,'',$4,$5)
		 ON CONFLICT (date) DO UPDATE SET
		   entries=excluded.entries, recomendado=excluded.recomendado,
		   deleted_at='', updated_at=excluded.updated_at`,
		p.Date, entries, rec,
		created.UTC().Truncate(time.Second).Format(time.RFC3339),
		updated.UTC().Truncate(time.Second).Format(time.RFC3339),
	); err != nil {
		return store.PortadaDay{}, fmt.Errorf("upsert portada: %w", err)
	}
	got, _, err := s.PortadaByDate(ctx, p.Date)
	if err != nil {
		return store.PortadaDay{}, err
	}
	return got, nil
}

// PortadaByDate returns the day plan for the date, or found=false. A soft-deleted
// day is still returned (found=true, Deleted=true) so callers can re-activate it.
func (s *Store) PortadaByDate(ctx context.Context, date string) (store.PortadaDay, bool, error) {
	p, err := scanPortada(s.db.QueryRowContext(ctx,
		`SELECT `+portadaCols+` FROM portadas WHERE date = $1`, date))
	if errors.Is(err, sql.ErrNoRows) {
		return store.PortadaDay{}, false, nil
	}
	if err != nil {
		return store.PortadaDay{}, false, err
	}
	return p, true, nil
}

// ListPortadas returns day plans ordered by date ascending. The order is pinned to
// COLLATE "C" (raw byte order) so it matches SQLite's default BINARY collation for
// any date set. Tombstoned days are excluded unless includeDeleted.
func (s *Store) ListPortadas(ctx context.Context, includeDeleted bool) ([]store.PortadaDay, error) {
	q := `SELECT ` + portadaCols + ` FROM portadas`
	if !includeDeleted {
		q += ` WHERE deleted_at = ''`
	}
	q += ` ORDER BY date COLLATE "C" ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.PortadaDay
	for rows.Next() {
		p, err := scanPortada(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePortada soft-deletes the day plan by setting a whole-second RFC3339
// tombstone. Deleting an absent date returns store.ErrNotFound.
func (s *Store) DeletePortada(ctx context.Context, date string) error {
	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE portadas SET deleted_at = $1, updated_at = $2 WHERE date = $3`, now, now, date)
	if err != nil {
		return fmt.Errorf("delete portada: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

const sourceCols = "id,slug,domain,homepage,description,feed_urls,feed_type,language,ownership_group,lean,enabled,status,last_checked,last_ok,metadata,deleted_at,created_at,updated_at"

// scanSource decodes one sources row. feed_urls and metadata are JSONB (scanned as
// bytes) and round-trip through the shared JSON helpers; enabled is a BOOLEAN; the
// timestamps are whole-second RFC3339 TEXT (deleted_at "" means active). Identical
// observable result to the SQLite adapter. Shared by SourceBySlug and ListSources.
func scanSource(sc scanner) (store.Source, error) {
	var (
		id                                                           int64
		slug, domain, homepage, desc, feedType, language, ownership  string
		lean, status, lastChecked, lastOK, deleted, created, updated string
		enabled                                                      bool
		feedURLs, meta                                               []byte
	)
	if err := sc.Scan(&id, &slug, &domain, &homepage, &desc, &feedURLs, &feedType, &language, &ownership, &lean, &enabled, &status, &lastChecked, &lastOK, &meta, &deleted, &created, &updated); err != nil {
		return store.Source{}, err
	}
	urls, err := unmarshalStrings(feedURLs)
	if err != nil {
		return store.Source{}, fmt.Errorf("unmarshal source feed_urls: %w", err)
	}
	m, err := unmarshalMeta(meta)
	if err != nil {
		return store.Source{}, err
	}
	createdT, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return store.Source{}, fmt.Errorf("parse source created_at: %w", err)
	}
	updatedT, err := time.Parse(time.RFC3339, updated)
	if err != nil {
		return store.Source{}, fmt.Errorf("parse source updated_at: %w", err)
	}
	return store.Source{
		ID:             strconv.FormatInt(id, 10),
		Slug:           slug,
		Domain:         domain,
		Homepage:       homepage,
		Description:    desc,
		FeedURLs:       urls,
		FeedType:       feedType,
		Language:       language,
		OwnershipGroup: ownership,
		Lean:           lean,
		Enabled:        enabled,
		Status:         status,
		LastChecked:    lastChecked,
		LastOK:         lastOK,
		Metadata:       m,
		Deleted:        deleted != "",
		CreatedAt:      createdT,
		UpdatedAt:      updatedT,
	}, nil
}

// UpsertSource creates or updates a source keyed on slug. Behavior is identical to
// the SQLite adapter (proven by the shared conformance suite): an existing slug is
// updated in place, updated_at advances, created_at is preserved, and the tombstone
// is cleared so a previously deleted slug is re-activated.
func (s *Store) UpsertSource(ctx context.Context, src store.Source) (store.Source, error) {
	urls, err := marshalStrings(src.FeedURLs)
	if err != nil {
		return store.Source{}, fmt.Errorf("marshal source feed_urls: %w", err)
	}
	meta, err := marshalMeta(src.Metadata)
	if err != nil {
		return store.Source{}, err
	}
	created := src.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	updated := src.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sources (slug,domain,homepage,description,feed_urls,feed_type,language,ownership_group,lean,enabled,status,last_checked,last_ok,metadata,deleted_at,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb,'',$15,$16)
		 ON CONFLICT (slug) DO UPDATE SET
		   domain=excluded.domain, homepage=excluded.homepage, description=excluded.description,
		   feed_urls=excluded.feed_urls, feed_type=excluded.feed_type, language=excluded.language,
		   ownership_group=excluded.ownership_group, lean=excluded.lean, enabled=excluded.enabled,
		   status=excluded.status, last_checked=excluded.last_checked, last_ok=excluded.last_ok,
		   metadata=excluded.metadata, deleted_at='', updated_at=excluded.updated_at`,
		src.Slug, src.Domain, src.Homepage, src.Description, urls, src.FeedType, src.Language,
		src.OwnershipGroup, src.Lean, src.Enabled, src.Status, src.LastChecked, src.LastOK, meta,
		created.UTC().Truncate(time.Second).Format(time.RFC3339),
		updated.UTC().Truncate(time.Second).Format(time.RFC3339),
	); err != nil {
		return store.Source{}, fmt.Errorf("upsert source: %w", err)
	}
	got, _, err := s.SourceBySlug(ctx, src.Slug)
	if err != nil {
		return store.Source{}, err
	}
	return got, nil
}

// SourceBySlug returns the source for the slug, or found=false. A soft-deleted source
// is still returned (found=true, Deleted=true) so callers can re-activate it.
func (s *Store) SourceBySlug(ctx context.Context, slug string) (store.Source, bool, error) {
	src, err := scanSource(s.db.QueryRowContext(ctx,
		`SELECT `+sourceCols+` FROM sources WHERE slug = $1`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Source{}, false, nil
	}
	if err != nil {
		return store.Source{}, false, err
	}
	return src, true, nil
}

// ListSources returns sources ordered by slug ascending. The order is pinned to
// COLLATE "C" (raw byte order) so it matches SQLite's default BINARY collation for
// any slug set. Tombstoned sources are excluded unless includeDeleted.
func (s *Store) ListSources(ctx context.Context, includeDeleted bool) ([]store.Source, error) {
	q := `SELECT ` + sourceCols + ` FROM sources`
	if !includeDeleted {
		q += ` WHERE deleted_at = ''`
	}
	q += ` ORDER BY slug COLLATE "C" ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// DeleteSource soft-deletes the source by setting a whole-second RFC3339 tombstone
// AND detaches it from every author (removing its author_sources rows), in one
// transaction. Behavior is identical to the SQLite adapter. Deleting an absent slug
// returns store.ErrNotFound.
func (s *Store) DeleteSource(ctx context.Context, slug string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	res, err := tx.ExecContext(ctx,
		`UPDATE sources SET deleted_at = $1, updated_at = $2 WHERE slug = $3`, now, now, slug)
	if err != nil {
		return fmt.Errorf("delete source: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM author_sources WHERE source_slug = $1`, slug); err != nil {
		return fmt.Errorf("detach source from authors: %w", err)
	}
	return tx.Commit()
}
