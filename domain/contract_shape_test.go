package domain_test

import (
	"reflect"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/domain"
)

// Seam A freeze: the Go data shape the static-site generator (Layer 1) renders.
// These tests pin the exact exported field set and JSON tags of the records that
// cross the layer boundary. Adding, removing, or retagging a field is a deliberate
// contract change: update contracts/CONTRACT.md and this test in the same commit.

func TestContract_ArticleShape(t *testing.T) {
	want := map[string]string{
		"ID":          "id",
		"Slug":        "slug",
		"Title":       "title",
		"Body":        "body",
		"Author":      "author",
		"Section":     "section",
		"Topics":      "topics",
		"PublishedAt": "published_at",
		"ContentHash": "content_hash",
		"Metadata":    "metadata,omitempty",
		"Deleted":     "deleted,omitempty",
		"CreatedAt":   "created_at",
	}
	assertShape(t, reflect.TypeOf(domain.Article{}), want)
}

func TestContract_PublishInputShape(t *testing.T) {
	want := map[string]string{
		"Title":       "title",
		"Body":        "body",
		"Author":      "author",
		"Section":     "section",
		"Topics":      "topics,omitempty",
		"Slug":        "slug,omitempty",
		"PublishedAt": "published_at,omitempty",
		"Metadata":    "metadata,omitempty",
	}
	assertShape(t, reflect.TypeOf(domain.PublishInput{}), want)
}

func assertShape(t *testing.T, typ reflect.Type, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		got[f.Name] = f.Tag.Get("json")
	}
	for name, tag := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s: frozen field %q is missing (a contract change must update CONTRACT.md)", typ.Name(), name)
			continue
		}
		if g != tag {
			t.Errorf("%s.%s: json tag = %q, frozen = %q", typ.Name(), name, g, tag)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s: unexpected field %q is not in the frozen contract (update CONTRACT.md if intended)", typ.Name(), name)
		}
	}
}
