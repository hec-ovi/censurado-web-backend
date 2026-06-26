package sqlite_test

import (
	"path/filepath"
	"testing"

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
