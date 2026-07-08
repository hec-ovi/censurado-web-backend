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

func TestSeedPanelText_InsertsLivePreservesEditsDropsDead(t *testing.T) {
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()

	w1, s1, err := seedPanelText(ctx, repo, false)
	if err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	if s1 != 0 {
		t.Errorf("first seed skipped %d rows, want 0", s1)
	}
	if w1 == 0 {
		t.Fatal("first seed wrote nothing")
	}

	en, err := repo.Text(ctx, store.ScopePanel, "en")
	if err != nil {
		t.Fatalf("read en: %v", err)
	}
	if en["Save portada"] != "Save portada" {
		t.Errorf("live key not seeded: %q", en["Save portada"])
	}
	if _, ok := en["Runs"]; ok {
		t.Error("dead key Runs was seeded")
	}

	es, err := repo.Text(ctx, store.ScopePanel, "es")
	if err != nil {
		t.Fatalf("read es: %v", err)
	}
	// A key whose former ES overlay is preserved carries its Spanish row.
	if es["Save"] != "Guardar" {
		t.Errorf("preserved Spanish overlay missing: %q", es["Save"])
	}
	// A key that never had a Spanish overlay (an English-only leak) gets no es row.
	if _, ok := es["Active on this author"]; ok {
		t.Error("an English-only key was given an es row")
	}

	// An operator edit must survive a re-seed (insert-if-absent).
	if _, err := repo.UpsertText(ctx, store.ScopePanel, store.TextEntry{Key: "Save portada", Lang: "en", Value: "Publish front page"}); err != nil {
		t.Fatalf("operator edit: %v", err)
	}
	w2, s2, err := seedPanelText(ctx, repo, false)
	if err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	if w2 != 0 {
		t.Errorf("re-seed wrote %d rows, want 0 (all present)", w2)
	}
	if s2 == 0 {
		t.Error("re-seed skipped 0 rows, expected the full existing set")
	}
	en2, _ := repo.Text(ctx, store.ScopePanel, "en")
	if en2["Save portada"] != "Publish front page" {
		t.Errorf("re-seed clobbered the operator edit: %q", en2["Save portada"])
	}

	// -force resets every seeded row back to the shipped value.
	if _, _, err := seedPanelText(ctx, repo, true); err != nil {
		t.Fatalf("force seed: %v", err)
	}
	en3, _ := repo.Text(ctx, store.ScopePanel, "en")
	if en3["Save portada"] != "Save portada" {
		t.Errorf("-force did not reset the operator edit: %q", en3["Save portada"])
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
