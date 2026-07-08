package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

func TestSeedEditorialText_InsertsPreservesEditsForceResets(t *testing.T) {
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()

	w1, s1, err := seedEditorialText(ctx, repo, false)
	if err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	if s1 != 0 {
		t.Errorf("first seed skipped %d rows, want 0", s1)
	}
	if w1 == 0 {
		t.Fatal("first seed wrote nothing")
	}

	es, err := repo.Text(ctx, store.ScopeEditorial, "es")
	if err != nil {
		t.Fatalf("read es: %v", err)
	}
	if es["attribution.example"] != "según X" {
		t.Errorf("anchor not seeded: %q", es["attribution.example"])
	}
	// The catalog is Spanish-only: the default 'en' read is empty.
	en, err := repo.Text(ctx, store.ScopeEditorial, "en")
	if err != nil {
		t.Fatalf("read en: %v", err)
	}
	if len(en) != 0 {
		t.Errorf("editorial catalog has an en row, want none: %v", en)
	}

	// An operator edit must survive a re-seed (insert-if-absent).
	if _, err := repo.UpsertText(ctx, store.ScopeEditorial, store.TextEntry{
		Key: "attribution.example", Lang: "es", Value: "conforme a X"}); err != nil {
		t.Fatalf("operator edit: %v", err)
	}
	w2, s2, err := seedEditorialText(ctx, repo, false)
	if err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	if w2 != 0 {
		t.Errorf("re-seed wrote %d rows, want 0 (all present)", w2)
	}
	if s2 == 0 {
		t.Error("re-seed skipped 0 rows, expected the full existing set")
	}
	es2, _ := repo.Text(ctx, store.ScopeEditorial, "es")
	if es2["attribution.example"] != "conforme a X" {
		t.Errorf("re-seed clobbered the operator edit: %q", es2["attribution.example"])
	}

	// -force resets every seeded row back to the shipped value.
	if _, _, err := seedEditorialText(ctx, repo, true); err != nil {
		t.Fatalf("force seed: %v", err)
	}
	es3, _ := repo.Text(ctx, store.ScopeEditorial, "es")
	if es3["attribution.example"] != "según X" {
		t.Errorf("-force did not reset the operator edit: %q", es3["attribution.example"])
	}
}

func TestRun_CLI(t *testing.T) {
	db := filepath.Join(t.TempDir(), "cli.db")
	var out, errb bytes.Buffer
	if code := run([]string{"-db", db}, &out, &errb); code != 0 {
		t.Fatalf("run -db = %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "seeded") {
		t.Errorf("unexpected stdout: %q", out.String())
	}

	// Missing -db (with no env fallback) is a usage error.
	t.Setenv("CENSURADO_DB", "")
	if code := run(nil, io.Discard, io.Discard); code != 2 {
		t.Errorf("missing -db = %d, want 2", code)
	}
}
