package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
	"github.com/hec-ovi/censurado-web-backend/store/storetest"
)

func TestSQLiteRepository(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	repo, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	storetest.Run(t, repo)
}

func TestSQLiteFilters(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "filters.db")
	repo, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	storetest.RunFilters(t, repo)
}

func TestSQLiteFacets(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "facets.db")
	repo, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	storetest.RunFacets(t, repo)
}

func TestSQLiteUpsertMany(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "batch.db")
	repo, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	storetest.RunUpsertMany(t, repo)
}

func TestSQLiteSubmissionLog(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sub.db")
	repo, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	storetest.RunSubmissionLog(t, repo)
}

func TestSQLiteListSubmissions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "list-sub.db")
	repo, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	storetest.RunListSubmissions(t, repo)
}

func TestSQLiteAuthorStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "authors.db")
	repo, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	storetest.RunAuthorStore(t, repo)
}

func TestSQLiteTopicStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "topics.db")
	repo, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	storetest.RunTopicStore(t, repo)
}

func TestSQLiteArticleMutations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mutations.db")
	repo, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	storetest.RunArticleMutations(t, repo)
}

// TestSQLiteOpenMigratesLegacyArticles guards the additive column migration: a
// database created before soft-delete (no articles.deleted_at) must keep working,
// because every read path now selects and filters on that column. Open adds it.
func TestSQLiteOpenMigratesLegacyArticles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Build the articles table the way an old release did: no deleted_at column.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE articles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		slug TEXT NOT NULL UNIQUE, title TEXT NOT NULL, body TEXT NOT NULL,
		author TEXT NOT NULL, section TEXT NOT NULL, published_at TEXT NOT NULL,
		content_hash TEXT NOT NULL UNIQUE, metadata TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO articles
		(slug,title,body,author,section,published_at,content_hash,metadata,created_at)
		VALUES ('hola','Hola','Cuerpo','ada','tech','2026-06-01T00:00:00Z','hash1','{}','2026-06-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	_ = raw.Close()

	repo, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	// Find selects a.deleted_at and filters on it; it must succeed and see the row.
	arts, err := repo.Find(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Find after migrate: %v", err)
	}
	if len(arts) != 1 || arts[0].Slug != "hola" {
		t.Fatalf("want 1 article slug hola, got %+v", arts)
	}

	// Re-opening is a no-op: the migration is idempotent.
	repo2, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	_ = repo2.Close()
}
