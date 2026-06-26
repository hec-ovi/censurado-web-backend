package postgres_test

import (
	"database/sql"
	"os"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/store/postgres"
	"github.com/hec-ovi/censurado-web-backend/store/storetest"
)

// resetPostgres truncates every table the conformance suites touch so the tests
// are re-runnable against a persistent Postgres. The pgx driver is registered
// by importing the postgres package above; postgres.Open has already applied
// the CREATE TABLE IF NOT EXISTS schema by the time callers invoke this, so the
// tables exist. RESTART IDENTITY resets serial IDs and CASCADE clears the
// article_topics rows that reference articles.
func resetPostgres(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("reset open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`TRUNCATE articles, article_topics, submissions, authors, topics RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("reset truncate: %v", err)
	}
}

// TestPostgresRepository runs the shared conformance suite against a real
// Postgres when CENSURADO_TEST_POSTGRES_DSN is set (CI and the local docker
// harness provide it), and skips otherwise so the default `go test ./...` stays
// dependency-free. A green run here is the proof that the store is swappable.
func TestPostgresRepository(t *testing.T) {
	dsn := os.Getenv("CENSURADO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CENSURADO_TEST_POSTGRES_DSN to run the Postgres conformance suite")
	}
	repo, err := postgres.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	resetPostgres(t, dsn)

	storetest.Run(t, repo)
}

// TestPostgresFilters runs the multi-value + full-text Filter conformance suite
// against a real Postgres when CENSURADO_TEST_POSTGRES_DSN is set, proving the
// widened Filter (multi-section/author/topic membership and the LIKE-escaped,
// ASCII-case-folded Query) behaves identically to SQLite.
func TestPostgresFilters(t *testing.T) {
	dsn := os.Getenv("CENSURADO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CENSURADO_TEST_POSTGRES_DSN to run the Postgres conformance suite")
	}
	repo, err := postgres.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	resetPostgres(t, dsn)

	storetest.RunFilters(t, repo)
}

// TestPostgresFacets runs the shared Facets conformance suite against a real
// Postgres when CENSURADO_TEST_POSTGRES_DSN is set, proving the aggregate
// values, counts, and the deterministic Count-DESC-then-Value-ASC ordering are
// byte-identical to SQLite.
func TestPostgresFacets(t *testing.T) {
	dsn := os.Getenv("CENSURADO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CENSURADO_TEST_POSTGRES_DSN to run the Postgres conformance suite")
	}
	repo, err := postgres.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	resetPostgres(t, dsn)

	storetest.RunFacets(t, repo)
}

// TestPostgresUpsertMany runs the shared UpsertMany conformance suite against a
// real Postgres when CENSURADO_TEST_POSTGRES_DSN is set, proving the atomic batch
// write (article + topics + ledger in one transaction), the per-item
// created/deduplicated classification, idempotent replay, and the all-or-nothing
// rollback behave identically to SQLite. This is the proof that batch ingest is
// safe to run on the cloud Postgres target, not just on the local SQLite file.
func TestPostgresUpsertMany(t *testing.T) {
	dsn := os.Getenv("CENSURADO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CENSURADO_TEST_POSTGRES_DSN to run the Postgres conformance suite")
	}
	repo, err := postgres.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	resetPostgres(t, dsn)

	storetest.RunUpsertMany(t, repo)
}

// TestPostgresSubmissionLog runs the shared SubmissionLog conformance suite
// against a real Postgres when CENSURADO_TEST_POSTGRES_DSN is set, proving the
// Postgres adapter records and roundtrips submissions identically to SQLite.
func TestPostgresSubmissionLog(t *testing.T) {
	dsn := os.Getenv("CENSURADO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CENSURADO_TEST_POSTGRES_DSN to run the Postgres conformance suite")
	}
	repo, err := postgres.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	resetPostgres(t, dsn)

	storetest.RunSubmissionLog(t, repo)
}

// TestPostgresListSubmissions runs the shared ListSubmissions conformance suite
// against a real Postgres when CENSURADO_TEST_POSTGRES_DSN is set, proving the
// audit-log ordering (created_at DESC, idempotency_key COLLATE "C" DESC), the
// author and date-range filters, paging, and the full field+scopes round-trip are
// byte-identical to SQLite.
func TestPostgresListSubmissions(t *testing.T) {
	dsn := os.Getenv("CENSURADO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CENSURADO_TEST_POSTGRES_DSN to run the Postgres conformance suite")
	}
	repo, err := postgres.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	resetPostgres(t, dsn)

	storetest.RunListSubmissions(t, repo)
}

// TestPostgresAuthorStore runs the shared AuthorStore conformance suite against a
// real Postgres when CENSURADO_TEST_POSTGRES_DSN is set, proving the managed-author
// registry round-trips, orders (handle COLLATE "C"), and tombstones/re-activates
// byte-identically to SQLite.
func TestPostgresAuthorStore(t *testing.T) {
	dsn := os.Getenv("CENSURADO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CENSURADO_TEST_POSTGRES_DSN to run the Postgres conformance suite")
	}
	repo, err := postgres.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	resetPostgres(t, dsn)

	storetest.RunAuthorStore(t, repo)
}

// TestPostgresTopicStore runs the shared TopicStore conformance suite against a
// real Postgres when CENSURADO_TEST_POSTGRES_DSN is set, proving the managed-topic
// registry round-trips, orders (slug COLLATE "C"), and tombstones/re-activates
// byte-identically to SQLite.
func TestPostgresTopicStore(t *testing.T) {
	dsn := os.Getenv("CENSURADO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CENSURADO_TEST_POSTGRES_DSN to run the Postgres conformance suite")
	}
	repo, err := postgres.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	resetPostgres(t, dsn)

	storetest.RunTopicStore(t, repo)
}

// TestPostgresArticleMutations runs the shared article soft-delete + edit
// conformance suite against a real Postgres when CENSURADO_TEST_POSTGRES_DSN is
// set, proving DeleteArticle/RestoreArticle/UpdateArticle and the
// replay-after-delete invariant behave identically to SQLite.
func TestPostgresArticleMutations(t *testing.T) {
	dsn := os.Getenv("CENSURADO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CENSURADO_TEST_POSTGRES_DSN to run the Postgres conformance suite")
	}
	repo, err := postgres.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	resetPostgres(t, dsn)

	storetest.RunArticleMutations(t, repo)
}
