// Package sqlite implements the store.Repository contract on SQLite using the
// pure-Go modernc.org/sqlite driver (no cgo). It is the default source-of-truth
// adapter: a single file in WAL mode, the stable hot axes indexed, and topics
// normalized into a join table.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
)

//go:embed schema.sql
var schema string

const timeLayout = time.RFC3339

// Store is a SQLite-backed Repository.
type Store struct {
	db *sql.DB
}

// querier is satisfied by both *sql.DB and *sql.Tx so helpers work in or out of
// a transaction.
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

// Open opens (creating if needed) a SQLite database at path and applies the
// schema. WAL mode, foreign keys, and a busy timeout are set via the DSN.
func Open(path string) (*Store, error) {
	q := url.Values{"_pragma": {"journal_mode(WAL)", "foreign_keys(1)", "busy_timeout(5000)"}}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	// Single-writer model: one connection serializes writes and sidesteps lock
	// churn, which matches the batch publish workload.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite schema: %w", err)
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
// but committing them together so the batch is atomic. Per item it reads the
// ledger first (a replayed key returns the prior article with no write), then
// upserts the article (content-hash deduplicated) and appends the submission
// record. A key reused for a different article rolls the whole batch back and
// returns *store.BatchConflictError. The single SQLite connection serializes
// concurrent batches at the writer.
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
			// Idempotent replay: the key already minted this article. Return its
			// stored identity without writing again, exactly like Apply.
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
	res, err := tx.ExecContext(ctx,
		`INSERT INTO articles (slug,title,body,author,section,published_at,content_hash,metadata,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(content_hash) DO NOTHING`,
		a.Slug, a.Title, a.Body, a.Author, a.Section,
		// Whole-second resolution so both adapters return the identical instant
		// through the Repository interface. RFC3339 already drops sub-second
		// precision, so the Truncate is documentary here, but it keeps the
		// whole-second contract explicit and symmetric with the Postgres adapter.
		a.PublishedAt.UTC().Truncate(time.Second).Format(timeLayout), a.ContentHash, meta, a.CreatedAt.UTC().Truncate(time.Second).Format(timeLayout),
	)
	if err != nil {
		return domain.Article{}, false, fmt.Errorf("insert article: %w", err)
	}
	affected, _ := res.RowsAffected()
	created := affected == 1

	var id int64
	if created {
		if id, err = res.LastInsertId(); err != nil {
			return domain.Article{}, false, err
		}
		for _, topic := range a.Topics {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO article_topics (article_id,topic) VALUES (?,?)`, id, topic); err != nil {
				return domain.Article{}, false, fmt.Errorf("insert topic: %w", err)
			}
		}
	} else {
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM articles WHERE content_hash = ?`, a.ContentHash).Scan(&id); err != nil {
			return domain.Article{}, false, fmt.Errorf("lookup existing: %w", err)
		}
	}

	stored, err := getByID(ctx, tx, id)
	if err != nil {
		return domain.Article{}, false, err
	}
	return stored, created, nil
}

// isUniqueViolation reports whether err is a SQLite UNIQUE-constraint failure.
// In UpdateArticle the only UNIQUE column the update touches is content_hash (the
// slug is preserved), so a unique violation there is a content-hash collision.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// UpdateArticle mutates the article identified by a.Slug in place: id, slug, and
// created_at (and the tombstone) are preserved, the mutable fields are replaced,
// and the topic set is rewritten, all in one transaction. The caller supplies the
// recomputed a.ContentHash. Returns store.ErrNotFound when no row has the slug and
// *store.EditConflictError when the new content hash collides with a different
// article.
func (s *Store) UpdateArticle(ctx context.Context, a domain.Article) (domain.Article, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Article{}, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM articles WHERE slug = ?`, a.Slug).Scan(&id)
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
		`UPDATE articles SET title=?, body=?, author=?, section=?, published_at=?, content_hash=?, metadata=? WHERE id=?`,
		a.Title, a.Body, a.Author, a.Section,
		a.PublishedAt.UTC().Truncate(time.Second).Format(timeLayout), a.ContentHash, meta, id,
	); err != nil {
		if isUniqueViolation(err) {
			return domain.Article{}, &store.EditConflictError{Slug: a.Slug, ContentHash: a.ContentHash}
		}
		return domain.Article{}, fmt.Errorf("update article: %w", err)
	}

	// Replace the topic set so an edit that adds or removes a topic is reflected
	// exactly, within the same transaction.
	if _, err := tx.ExecContext(ctx, `DELETE FROM article_topics WHERE article_id = ?`, id); err != nil {
		return domain.Article{}, fmt.Errorf("clear topics: %w", err)
	}
	for _, topic := range a.Topics {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO article_topics (article_id,topic) VALUES (?,?)`, id, topic); err != nil {
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
// tombstone, so it is hidden from the default Find/Count but kept for audit and
// restore. Returns store.ErrNotFound for an absent slug; idempotent on an
// already-deleted row (the tombstone is re-stamped).
func (s *Store) DeleteArticle(ctx context.Context, slug string) error {
	now := time.Now().UTC().Truncate(time.Second).Format(timeLayout)
	res, err := s.db.ExecContext(ctx, `UPDATE articles SET deleted_at = ? WHERE slug = ?`, now, slug)
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
	res, err := s.db.ExecContext(ctx, `UPDATE articles SET deleted_at = '' WHERE slug = ?`, slug)
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
	err := s.db.QueryRowContext(ctx, `SELECT id FROM articles WHERE slug = ?`, slug).Scan(&id)
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
// deterministic and identical to the Postgres adapter. The value tie-break sorts
// on the TEXT columns' default BINARY (byte) collation; the Postgres adapter
// pins COLLATE "C" to match this exactly for any value set.
func (s *Store) Facets(ctx context.Context) (store.Facets, error) {
	// Tombstoned articles are excluded so the facet counts match what Find returns:
	// a soft-deleted article is hidden from the public site, so it must not inflate
	// the admin filter counts either.
	sections, err := facetRows(ctx, s.db,
		`SELECT section, COUNT(*) FROM articles WHERE deleted_at = '' GROUP BY section ORDER BY COUNT(*) DESC, section ASC`)
	if err != nil {
		return store.Facets{}, fmt.Errorf("facets sections: %w", err)
	}
	authors, err := facetRows(ctx, s.db,
		`SELECT author, COUNT(*) FROM articles WHERE deleted_at = '' GROUP BY author ORDER BY COUNT(*) DESC, author ASC`)
	if err != nil {
		return store.Facets{}, fmt.Errorf("facets authors: %w", err)
	}
	// Topic counts are per-article (one row per article carrying the topic); the
	// join table's (article_id, topic) primary key already forbids duplicates, so
	// COUNT(*) cannot double-count a single article on one topic. The join to
	// articles drops a tombstoned article's topics from the counts.
	topics, err := facetRows(ctx, s.db,
		`SELECT topic, COUNT(*) FROM article_topics t JOIN articles a ON a.id = t.article_id WHERE a.deleted_at = '' GROUP BY topic ORDER BY COUNT(*) DESC, topic ASC`)
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
	var args []any
	if count {
		b.WriteString("SELECT COUNT(*) FROM articles a")
	} else {
		b.WriteString("SELECT a.id,a.slug,a.title,a.body,a.author,a.section,a.published_at,a.content_hash,a.metadata,a.created_at,a.deleted_at FROM articles a")
	}
	if f.Topic != "" {
		b.WriteString(" JOIN article_topics t ON t.article_id = a.id AND t.topic = ?")
		args = append(args, f.Topic)
	}
	b.WriteString(" WHERE 1=1")
	// Soft-deleted rows are hidden by default; the admin opts into them. The public
	// generator passes the zero Filter and so never sees a tombstoned article.
	if !f.IncludeDeleted {
		b.WriteString(" AND a.deleted_at = ''")
	}
	if f.Section != "" {
		b.WriteString(" AND a.section = ?")
		args = append(args, f.Section)
	}
	if f.Author != "" {
		b.WriteString(" AND a.author = ?")
		args = append(args, f.Author)
	}
	if !f.From.IsZero() {
		b.WriteString(" AND a.published_at >= ?")
		args = append(args, f.From.UTC().Format(timeLayout))
	}
	if !f.To.IsZero() {
		b.WriteString(" AND a.published_at < ?")
		args = append(args, f.To.UTC().Format(timeLayout))
	}
	// Multi-value axes (admin only). Each ANDs with the scalar fields above and
	// ORs within itself via IN/EXISTS. Blank entries are dropped first; an
	// all-blank slice imposes no constraint. All values are bound as parameters.
	if vals := nonBlank(f.Sections); len(vals) > 0 {
		b.WriteString(" AND a.section IN (" + placeholders(len(vals)) + ")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	if vals := nonBlank(f.Authors); len(vals) > 0 {
		b.WriteString(" AND a.author IN (" + placeholders(len(vals)) + ")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	if vals := nonBlank(f.Topics); len(vals) > 0 {
		// EXISTS (not a JOIN) so an article matching several of the values is
		// returned once; coexists with the scalar Topic JOIN above (both AND).
		b.WriteString(" AND EXISTS (SELECT 1 FROM article_topics att WHERE att.article_id = a.id AND att.topic IN (" + placeholders(len(vals)) + "))")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		// Full-text substring over title OR body. LIKE wildcards in the user value
		// are escaped in Go (likeEscape) and the backslash is declared as the
		// ESCAPE char, so '%'/'_' are matched literally, never as wildcards.
		//
		// ACCEPTED BOUNDARY: this is ASCII-case-insensitive only. SQLite's lower()
		// folds ASCII only, while Postgres's lower() is unicode-aware, so a
		// non-ASCII case fold (e.g. 'É' vs 'é') would DIVERGE between the engines.
		// The contract is therefore: case-insensitive for ASCII; non-ASCII
		// (accented) letters match case-sensitively. An exact non-ASCII substring
		// in the same case DOES match and is safe to rely on across both engines.
		pat := "%" + likeEscape(q) + "%"
		b.WriteString(" AND (lower(a.title) LIKE lower(?) ESCAPE '\\' OR lower(a.body) LIKE lower(?) ESCAPE '\\')")
		args = append(args, pat, pat)
	}
	if count {
		return b.String(), args
	}
	dir := "DESC"
	if f.Order == store.OldestFirst {
		dir = "ASC"
	}
	fmt.Fprintf(&b, " ORDER BY a.published_at %s, a.id %s", dir, dir)
	if f.Limit > 0 {
		b.WriteString(" LIMIT ?")
		args = append(args, f.Limit)
	}
	if f.Offset > 0 {
		if f.Limit <= 0 {
			b.WriteString(" LIMIT -1") // SQLite requires LIMIT before OFFSET
		}
		b.WriteString(" OFFSET ?")
		args = append(args, f.Offset)
	}
	return b.String(), args
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

// placeholders returns "?,?,..." with n placeholders for an IN clause.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// likeEscape escapes the LIKE wildcards in a user value so it is matched as a
// literal substring under "ESCAPE '\'". The escape char is escaped first, then
// the '%' and '_' metacharacters. Identical to the Postgres adapter's escaping
// so the two engines bind byte-identical patterns.
func likeEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

func getByID(ctx context.Context, q querier, id int64) (domain.Article, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id,slug,title,body,author,section,published_at,content_hash,metadata,created_at,deleted_at
		 FROM articles WHERE id = ?`, id)
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
		id                                                                    int64
		slug, title, body, author, section, pub, hash, meta, created, deleted string
	)
	if err := sc.Scan(&id, &slug, &title, &body, &author, &section, &pub, &hash, &meta, &created, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Article{}, 0, store.ErrNotFound
		}
		return domain.Article{}, 0, err
	}
	pubT, err := time.Parse(timeLayout, pub)
	if err != nil {
		return domain.Article{}, 0, fmt.Errorf("parse published_at: %w", err)
	}
	createdT, err := time.Parse(timeLayout, created)
	if err != nil {
		return domain.Article{}, 0, fmt.Errorf("parse created_at: %w", err)
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
		PublishedAt: pubT,
		ContentHash: hash,
		Metadata:    m,
		Deleted:     deleted != "",
		CreatedAt:   createdT,
		Topics:      []string{},
	}, id, nil
}

func loadTopics(ctx context.Context, q querier, ids []int64) (map[int64][]string, error) {
	out := make(map[int64][]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	rows, err := q.QueryContext(ctx,
		`SELECT article_id, topic FROM article_topics WHERE article_id IN (`+strings.Join(ph, ",")+`) ORDER BY article_id, topic`,
		args...)
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
// (RecordSubmission), so they split back on the same separator; created_at is the
// whole-second RFC3339 string both adapters store. Shared by FindSubmission and
// ListSubmissions so every read path round-trips a submission identically.
func scanSubmission(sc scanner) (store.Submission, error) {
	var sub store.Submission
	var scopes, created string
	if err := sc.Scan(&sub.IdempotencyKey, &sub.ContentHash, &sub.ArticleID, &sub.Slug, &sub.Author, &scopes, &created); err != nil {
		return store.Submission{}, err
	}
	if scopes != "" {
		sub.Scopes = strings.Split(scopes, " ")
	}
	t, err := time.Parse(timeLayout, created)
	if err != nil {
		return store.Submission{}, fmt.Errorf("parse submission created_at: %w", err)
	}
	sub.CreatedAt = t
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
		`SELECT `+submissionCols+` FROM submissions WHERE idempotency_key = ?`, key)
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
// key) so it is deterministic, and identical to the Postgres adapter: the
// idempotency_key tiebreak sorts on SQLite's default BINARY (byte) collation,
// which the Postgres adapter pins with COLLATE "C". The Author filter is exact
// equality; From/To bound created_at (>= From, < To). All values are bound as
// parameters.
func (s *Store) ListSubmissions(ctx context.Context, f store.ListSubmissionsFilter) ([]store.Submission, error) {
	var b strings.Builder
	var args []any
	b.WriteString("SELECT " + submissionCols + " FROM submissions WHERE 1=1")
	if f.Author != "" {
		b.WriteString(" AND author = ?")
		args = append(args, f.Author)
	}
	if !f.From.IsZero() {
		b.WriteString(" AND created_at >= ?")
		args = append(args, f.From.UTC().Format(timeLayout))
	}
	if !f.To.IsZero() {
		b.WriteString(" AND created_at < ?")
		args = append(args, f.To.UTC().Format(timeLayout))
	}
	b.WriteString(" ORDER BY created_at DESC, idempotency_key DESC")
	if f.Limit > 0 {
		b.WriteString(" LIMIT ?")
		args = append(args, f.Limit)
	}
	if f.Offset > 0 {
		if f.Limit <= 0 {
			b.WriteString(" LIMIT -1") // SQLite requires LIMIT before OFFSET
		}
		b.WriteString(" OFFSET ?")
		args = append(args, f.Offset)
	}
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
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
		 VALUES (?,?,?,?,?,?,?)`,
		sub.IdempotencyKey, sub.ContentHash, sub.ArticleID, sub.Slug, sub.Author,
		// Truncate to whole seconds, the shared second-granularity contract with
		// the Postgres adapter. RFC3339 already drops sub-second precision, so
		// this is explicit rather than load-bearing here, but it keeps both
		// adapters visibly identical and guards against a future layout change.
		strings.Join(sub.Scopes, " "), sub.CreatedAt.UTC().Truncate(time.Second).Format(timeLayout))
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

func unmarshalMeta(s string) (map[string]any, error) {
	if s == "" || s == "{}" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	return m, nil
}

const authorCols = "id,handle,name,bio,avatar,metadata,deleted_at,created_at,updated_at"

// scanAuthor decodes one authors row. metadata round-trips through the shared
// marshal/unmarshal helpers; created_at/updated_at/deleted_at are whole-second
// RFC3339 TEXT (deleted_at "" means active), so Deleted is derived from the
// tombstone being non-empty. Shared by AuthorByHandle and ListAuthors so every
// read path round-trips an author identically.
func scanAuthor(sc scanner) (store.Author, error) {
	var (
		id                                       int64
		handle, name, bio, avatar, meta, deleted string
		created, updated                         string
	)
	if err := sc.Scan(&id, &handle, &name, &bio, &avatar, &meta, &deleted, &created, &updated); err != nil {
		return store.Author{}, err
	}
	m, err := unmarshalMeta(meta)
	if err != nil {
		return store.Author{}, err
	}
	createdT, err := time.Parse(timeLayout, created)
	if err != nil {
		return store.Author{}, fmt.Errorf("parse author created_at: %w", err)
	}
	updatedT, err := time.Parse(timeLayout, updated)
	if err != nil {
		return store.Author{}, fmt.Errorf("parse author updated_at: %w", err)
	}
	return store.Author{
		ID:        strconv.FormatInt(id, 10),
		Handle:    handle,
		Name:      name,
		Bio:       bio,
		Avatar:    avatar,
		Metadata:  m,
		Deleted:   deleted != "",
		CreatedAt: createdT,
		UpdatedAt: updatedT,
	}, nil
}

// UpsertAuthor creates or updates an author keyed on handle. On create it writes
// the supplied CreatedAt/UpdatedAt (defaulting to now when zero); on an existing
// handle it updates the mutable fields, advances updated_at, preserves created_at,
// and clears the tombstone so a previously deleted handle is re-activated.
func (s *Store) UpsertAuthor(ctx context.Context, a store.Author) (store.Author, error) {
	meta, err := marshalMeta(a.Metadata)
	if err != nil {
		return store.Author{}, err
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
		`INSERT INTO authors (handle,name,bio,avatar,metadata,deleted_at,created_at,updated_at)
		 VALUES (?,?,?,?,?,'',?,?)
		 ON CONFLICT(handle) DO UPDATE SET
		   name=excluded.name, bio=excluded.bio, avatar=excluded.avatar,
		   metadata=excluded.metadata, deleted_at='', updated_at=excluded.updated_at`,
		a.Handle, a.Name, a.Bio, a.Avatar, meta,
		created.UTC().Truncate(time.Second).Format(timeLayout),
		updated.UTC().Truncate(time.Second).Format(timeLayout),
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
func (s *Store) AuthorByHandle(ctx context.Context, handle string) (store.Author, bool, error) {
	a, err := scanAuthor(s.db.QueryRowContext(ctx,
		`SELECT `+authorCols+` FROM authors WHERE handle = ?`, handle))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Author{}, false, nil
	}
	if err != nil {
		return store.Author{}, false, err
	}
	return a, true, nil
}

// ListAuthors returns authors ordered by handle ascending. The tie-break sorts on
// SQLite's default BINARY (byte) collation; the Postgres adapter pins COLLATE "C"
// to match. Tombstoned authors are excluded unless includeDeleted.
func (s *Store) ListAuthors(ctx context.Context, includeDeleted bool) ([]store.Author, error) {
	q := `SELECT ` + authorCols + ` FROM authors`
	if !includeDeleted {
		q += ` WHERE deleted_at = ''`
	}
	q += ` ORDER BY handle ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Author
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAuthor soft-deletes the author by setting a whole-second RFC3339 tombstone.
// Deleting an absent handle returns store.ErrNotFound.
func (s *Store) DeleteAuthor(ctx context.Context, handle string) error {
	now := time.Now().UTC().Truncate(time.Second).Format(timeLayout)
	res, err := s.db.ExecContext(ctx,
		`UPDATE authors SET deleted_at = ?, updated_at = ? WHERE handle = ?`, now, now, handle)
	if err != nil {
		return fmt.Errorf("delete author: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

const topicCols = "id,slug,label,description,metadata,deleted_at,created_at,updated_at"

// scanTopic decodes one topics row, mirroring scanAuthor: metadata round-trips
// through the shared marshal/unmarshal helpers; the timestamps are whole-second
// RFC3339 TEXT (deleted_at "" means active), so Deleted is the tombstone being
// non-empty. Shared by TopicBySlug and ListTopics.
func scanTopic(sc scanner) (store.Topic, error) {
	var (
		id                               int64
		slug, label, desc, meta, deleted string
		created, updated                 string
	)
	if err := sc.Scan(&id, &slug, &label, &desc, &meta, &deleted, &created, &updated); err != nil {
		return store.Topic{}, err
	}
	m, err := unmarshalMeta(meta)
	if err != nil {
		return store.Topic{}, err
	}
	createdT, err := time.Parse(timeLayout, created)
	if err != nil {
		return store.Topic{}, fmt.Errorf("parse topic created_at: %w", err)
	}
	updatedT, err := time.Parse(timeLayout, updated)
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

// UpsertTopic creates or updates a topic keyed on slug, mirroring UpsertAuthor: on
// an existing slug it updates the mutable fields, advances updated_at, preserves
// created_at, and clears the tombstone (re-activating a previously deleted slug).
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
		 VALUES (?,?,?,?,'',?,?)
		 ON CONFLICT(slug) DO UPDATE SET
		   label=excluded.label, description=excluded.description,
		   metadata=excluded.metadata, deleted_at='', updated_at=excluded.updated_at`,
		t.Slug, t.Label, t.Description, meta,
		created.UTC().Truncate(time.Second).Format(timeLayout),
		updated.UTC().Truncate(time.Second).Format(timeLayout),
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
		`SELECT `+topicCols+` FROM topics WHERE slug = ?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Topic{}, false, nil
	}
	if err != nil {
		return store.Topic{}, false, err
	}
	return tp, true, nil
}

// ListTopics returns topics ordered by slug ascending (SQLite BINARY default; the
// Postgres adapter pins COLLATE "C" to match). Tombstoned topics are excluded
// unless includeDeleted.
func (s *Store) ListTopics(ctx context.Context, includeDeleted bool) ([]store.Topic, error) {
	q := `SELECT ` + topicCols + ` FROM topics`
	if !includeDeleted {
		q += ` WHERE deleted_at = ''`
	}
	q += ` ORDER BY slug ASC`
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
	now := time.Now().UTC().Truncate(time.Second).Format(timeLayout)
	res, err := s.db.ExecContext(ctx,
		`UPDATE topics SET deleted_at = ?, updated_at = ? WHERE slug = ?`, now, now, slug)
	if err != nil {
		return fmt.Errorf("delete topic: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}
