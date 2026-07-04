package store_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/store"
)

// Seam A freeze: the store contract the generator (Layer 1) depends on. The method
// sets and the Filter/overlay shapes are pinned so an internal store change cannot
// silently break Layer 1's build. A deliberate change updates contracts/CONTRACT.md
// and these expectations together.

func TestContract_StoreInterfaces(t *testing.T) {
	cases := []struct {
		name string
		typ  reflect.Type
		want []string
	}{
		{"Repository", reflect.TypeOf((*store.Repository)(nil)).Elem(),
			[]string{"BySlug", "Close", "Count", "DeleteArticle", "Facets", "Find", "RestoreArticle", "UpdateArticle", "Upsert", "UpsertMany"}},
		{"SubmissionLog", reflect.TypeOf((*store.SubmissionLog)(nil)).Elem(),
			[]string{"FindSubmission", "ListSubmissions", "RecordSubmission"}},
		{"AuthorStore", reflect.TypeOf((*store.AuthorStore)(nil)).Elem(),
			[]string{"AuthorByHandle", "AuthorSources", "DeleteAuthor", "ListAuthors", "SetAuthorSources", "UpsertAuthor"}},
		{"TopicStore", reflect.TypeOf((*store.TopicStore)(nil)).Elem(),
			[]string{"DeleteTopic", "ListTopics", "TopicBySlug", "UpsertTopic"}},
		{"PortadaStore", reflect.TypeOf((*store.PortadaStore)(nil)).Elem(),
			[]string{"DeletePortada", "ListPortadas", "PortadaByDate", "UpsertPortada"}},
		{"SourceStore", reflect.TypeOf((*store.SourceStore)(nil)).Elem(),
			[]string{"DeleteSource", "ListSources", "SourceBySlug", "UpsertSource"}},
	}
	for _, c := range cases {
		got := methodSet(c.typ)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s method set = %v, frozen = %v (update CONTRACT.md on a deliberate change)", c.name, got, c.want)
		}
	}
}

func TestContract_FilterPublicAxes(t *testing.T) {
	want := []string{
		"Section", "Author", "Topic",
		"Sections", "Authors", "Topics",
		"Query", "From", "To", "Order", "Limit", "Offset", "IncludeDeleted",
	}
	assertFields(t, "store.Filter", reflect.TypeOf(store.Filter{}), want)
}

func TestContract_OverlayShapes(t *testing.T) {
	assertFields(t, "store.Author", reflect.TypeOf(store.Author{}),
		[]string{"ID", "Handle", "Name", "Bio", "Avatar", "Gender", "About", "Style", "Topics", "Sources", "Metadata", "Deleted", "CreatedAt", "UpdatedAt"})
	assertFields(t, "store.Topic", reflect.TypeOf(store.Topic{}),
		[]string{"ID", "Slug", "Label", "Description", "Metadata", "Deleted", "CreatedAt", "UpdatedAt"})
	assertFields(t, "store.PortadaDay", reflect.TypeOf(store.PortadaDay{}),
		[]string{"Date", "Entries", "Recomendado", "Deleted", "CreatedAt", "UpdatedAt"})
	assertFields(t, "store.PortadaEntry", reflect.TypeOf(store.PortadaEntry{}),
		[]string{"Slug", "Role"})
	assertFields(t, "store.Source", reflect.TypeOf(store.Source{}),
		[]string{"ID", "Slug", "Domain", "Homepage", "Description", "FeedURLs", "FeedType", "Language", "OwnershipGroup", "Lean", "Enabled", "Status", "LastChecked", "LastOK", "Metadata", "Deleted", "CreatedAt", "UpdatedAt"})
}

func methodSet(t reflect.Type) []string {
	names := make([]string, 0, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		names = append(names, t.Method(i).Name)
	}
	sort.Strings(names)
	return names
}

func assertFields(t *testing.T, name string, typ reflect.Type, want []string) {
	t.Helper()
	got := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		if f := typ.Field(i); f.IsExported() {
			got = append(got, f.Name)
		}
	}
	sort.Strings(got)
	w := append([]string{}, want...)
	sort.Strings(w)
	if !reflect.DeepEqual(got, w) {
		t.Errorf("%s fields = %v, frozen = %v (update CONTRACT.md on a deliberate change)", name, got, w)
	}
}
