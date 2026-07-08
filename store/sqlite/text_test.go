package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

func openTextStore(t *testing.T) *sqlite.Store {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "text.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// TestTextStore walks the UI-string catalog through one store: create, replace,
// language separation, tombstone/restore, scope isolation, and the error paths.
// The subtests run in order and build on each other, like the store's own lifecycle.
func TestTextStore(t *testing.T) {
	ctx := context.Background()
	s := openTextStore(t)

	t.Run("upsert creates and Text bulk-loads it", func(t *testing.T) {
		if _, err := s.UpsertText(ctx, store.ScopeFrontend, store.TextEntry{Key: "nav.about", Lang: "en", Value: "About"}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		m, err := s.Text(ctx, store.ScopeFrontend, "en")
		if err != nil {
			t.Fatalf("text: %v", err)
		}
		if m["nav.about"] != "About" {
			t.Errorf("Text[nav.about] = %q, want About", m["nav.about"])
		}
	})

	t.Run("re-upsert replaces the value and preserves created_at", func(t *testing.T) {
		first, _ := s.UpsertText(ctx, store.ScopeFrontend, store.TextEntry{Key: "nav.latest", Lang: "en", Value: "Latest"})
		second, err := s.UpsertText(ctx, store.ScopeFrontend, store.TextEntry{Key: "nav.latest", Lang: "en", Value: "Newest"})
		if err != nil {
			t.Fatalf("re-upsert: %v", err)
		}
		if second.Value != "Newest" {
			t.Errorf("value = %q, want Newest", second.Value)
		}
		if !second.CreatedAt.Equal(first.CreatedAt) {
			t.Errorf("created_at moved: %v -> %v", first.CreatedAt, second.CreatedAt)
		}
	})

	t.Run("Text keeps languages separate and drops tombstones", func(t *testing.T) {
		if _, err := s.UpsertText(ctx, store.ScopeFrontend, store.TextEntry{Key: "nav.about", Lang: "es", Value: "Nosotros"}); err != nil {
			t.Fatalf("upsert es: %v", err)
		}
		en, _ := s.Text(ctx, store.ScopeFrontend, "en")
		if en["nav.about"] != "About" {
			t.Errorf("en[nav.about] = %q, want About (no language bleed)", en["nav.about"])
		}
		es, _ := s.Text(ctx, store.ScopeFrontend, "es")
		if es["nav.about"] != "Nosotros" {
			t.Errorf("es[nav.about] = %q, want Nosotros", es["nav.about"])
		}
		if err := s.DeleteText(ctx, store.ScopeFrontend, "nav.about", "es"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		es2, _ := s.Text(ctx, store.ScopeFrontend, "es")
		if _, ok := es2["nav.about"]; ok {
			t.Error("tombstoned es row still in Text bulk load")
		}
	})

	t.Run("re-upsert clears the tombstone", func(t *testing.T) {
		if _, err := s.UpsertText(ctx, store.ScopeFrontend, store.TextEntry{Key: "nav.about", Lang: "es", Value: "Nosotros"}); err != nil {
			t.Fatalf("re-upsert es: %v", err)
		}
		es, _ := s.Text(ctx, store.ScopeFrontend, "es")
		if es["nav.about"] != "Nosotros" {
			t.Errorf("restored es[nav.about] = %q, want Nosotros", es["nav.about"])
		}
	})

	t.Run("Text returns a non-nil empty map for an unknown language", func(t *testing.T) {
		m, err := s.Text(ctx, store.ScopeFrontend, "zz")
		if err != nil {
			t.Fatalf("text: %v", err)
		}
		if m == nil || len(m) != 0 {
			t.Errorf("Text(zz) = %v, want empty non-nil map", m)
		}
	})

	t.Run("ListText is key-ordered and honors includeDeleted", func(t *testing.T) {
		if _, err := s.UpsertText(ctx, store.ScopeFrontend, store.TextEntry{Key: "zzz.last", Lang: "en", Value: "Z"}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := s.DeleteText(ctx, store.ScopeFrontend, "zzz.last", "en"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		live, _ := s.ListText(ctx, store.ScopeFrontend, "en", false)
		var keys []string
		for _, e := range live {
			if e.Key == "zzz.last" {
				t.Error("ListText(includeDeleted=false) returned a tombstoned row")
			}
			keys = append(keys, e.Key)
		}
		if !sort.StringsAreSorted(keys) {
			t.Errorf("ListText keys not ascending: %v", keys)
		}
		all, _ := s.ListText(ctx, store.ScopeFrontend, "en", true)
		var sawTomb bool
		for _, e := range all {
			if e.Key == "zzz.last" && e.Deleted {
				sawTomb = true
			}
		}
		if !sawTomb {
			t.Error("ListText(includeDeleted=true) missing the tombstoned row")
		}
	})

	t.Run("frontend and panel scopes are isolated", func(t *testing.T) {
		if _, err := s.UpsertText(ctx, store.ScopePanel, store.TextEntry{Key: "nav.articles", Lang: "en", Value: "Articles"}); err != nil {
			t.Fatalf("upsert panel: %v", err)
		}
		panel, _ := s.Text(ctx, store.ScopePanel, "en")
		if panel["nav.articles"] != "Articles" {
			t.Errorf("panel[nav.articles] = %q, want Articles", panel["nav.articles"])
		}
		front, _ := s.Text(ctx, store.ScopeFrontend, "en")
		if _, ok := front["nav.articles"]; ok {
			t.Error("panel key leaked into frontend scope")
		}
	})

	t.Run("DeleteText on an absent entry is ErrNotFound", func(t *testing.T) {
		if err := s.DeleteText(ctx, store.ScopeFrontend, "does.not.exist", "en"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("delete absent = %v, want ErrNotFound", err)
		}
	})

	t.Run("an unknown scope is an error, not a panic", func(t *testing.T) {
		if _, err := s.Text(ctx, store.TextScope("bogus"), "en"); err == nil {
			t.Error("Text(bogus scope) = nil error, want error")
		}
		if _, err := s.UpsertText(ctx, store.TextScope("bogus"), store.TextEntry{Key: "k", Lang: "en"}); err == nil {
			t.Error("UpsertText(bogus scope) = nil error, want error")
		}
	})
}
