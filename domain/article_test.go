package domain

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC) }

func validInput() PublishInput {
	return PublishInput{
		Title:   "OpenTofu 1.12 Lands Client-Side State Encryption",
		Body:    "# Heading\n\nBody text.",
		Author:  "ada",
		Section: "tech",
		Topics:  []string{"infrastructure", "Infrastructure", "  ", "iac"},
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNewArticle_DerivesSlugHashTimeAndTopics(t *testing.T) {
	got, err := NewArticle(validInput(), fixedNow())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "opentofu-1-12-lands-client-side-state-encryption"; got.Slug != want {
		t.Errorf("slug = %q, want %q", got.Slug, want)
	}
	if len(got.ContentHash) != 64 {
		t.Errorf("content hash = %q, want 64 hex chars", got.ContentHash)
	}
	if !got.PublishedAt.Equal(fixedNow()) {
		t.Errorf("published_at = %v, want %v", got.PublishedAt, fixedNow())
	}
	if !got.CreatedAt.Equal(fixedNow()) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, fixedNow())
	}
	if want := []string{"infrastructure", "iac"}; !equalStrings(got.Topics, want) {
		t.Errorf("topics = %v, want %v (deduped case-insensitively, blanks dropped, order kept)", got.Topics, want)
	}
}

func TestNewArticle_RespectsProvidedSlugAndPublishedAt(t *testing.T) {
	in := validInput()
	in.Slug = "custom-slug"
	pub := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	in.PublishedAt = &pub

	got, err := NewArticle(in, fixedNow())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Slug != "custom-slug" {
		t.Errorf("slug = %q, want provided %q", got.Slug, "custom-slug")
	}
	if !got.PublishedAt.Equal(pub) {
		t.Errorf("published_at = %v, want provided %v", got.PublishedAt, pub)
	}
}

func TestNewArticle_MissingRequiredFields(t *testing.T) {
	_, err := NewArticle(PublishInput{}, fixedNow())
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	for _, f := range []string{"title", "body", "author", "section"} {
		if _, ok := ve.Fields[f]; !ok {
			t.Errorf("missing validation entry for required field %q", f)
		}
	}
}

func TestNewArticle_FallbackSlugWhenTitleHasNoAlnum(t *testing.T) {
	in := validInput()
	in.Title = "***"
	got, err := NewArticle(in, fixedNow())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Slug) != 12 {
		t.Errorf("slug = %q, want 12-char content-hash fallback", got.Slug)
	}
}

func TestContentHash_DeterministicAndCollisionResistant(t *testing.T) {
	base := ContentHash("t", "b", "au", "s")
	if base != ContentHash("t", "b", "au", "s") {
		t.Error("content hash is not deterministic")
	}
	if ContentHash("t", "b2", "au", "s") == base {
		t.Error("content hash insensitive to body change")
	}
	if ContentHash("ab", "c", "x", "y") == ContentHash("a", "bc", "x", "y") {
		t.Error("field-boundary collision: length prefixing is not working")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Hello, World!":  "hello-world",
		"  spaced  out ": "spaced-out",
		"AI & ML":        "ai-ml",
		"---":            "",
		// Common Latin accents transliterate to ASCII (so "política" and
		// "politica" slugify the same); other scripts still drop.
		"café münchen":      "cafe-munchen",
		"Análisis Político": "analisis-politico",
		"política":          "politica",
		"日本語":               "",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// The golden fixture is the contract sample; it must build into a valid Article.
func TestGoldenArticleInputBuildsArticle(t *testing.T) {
	data, err := os.ReadFile("../testdata/golden_article.json")
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var in PublishInput
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatalf("unmarshal golden fixture: %v", err)
	}
	got, err := NewArticle(in, fixedNow())
	if err != nil {
		t.Fatalf("golden fixture did not build: %v", err)
	}
	if got.Slug == "" || got.ContentHash == "" {
		t.Errorf("golden article missing derived slug/hash: %+v", got)
	}
}

// Guard the contract artifact itself: it must be valid JSON, declare the 2020-12
// dialect, and require the four core fields.
func TestArticleSchemaContract(t *testing.T) {
	data, err := os.ReadFile("../contracts/article.schema.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if m["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %v, want the 2020-12 dialect", m["$schema"])
	}
	req, ok := m["required"].([]any)
	if !ok {
		t.Fatalf("schema has no required array")
	}
	want := map[string]bool{"title": true, "body": true, "author": true, "section": true}
	for _, r := range req {
		delete(want, r.(string))
	}
	if len(want) != 0 {
		t.Errorf("schema missing required fields: %v", want)
	}
}
