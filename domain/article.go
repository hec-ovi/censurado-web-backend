// Package domain defines the core article model shared across the portal's
// isolated layers. It depends on nothing else in the system; the store,
// publish, generate, and admin layers depend on it, never the reverse.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Article is the canonical record: the source of truth's row, read by the
// generator and admin. Stable, high-value dimensions are explicit fields; the
// open-ended tail lives in Metadata so new attributes need no schema migration.
type Article struct {
	ID          string         `json:"id"`
	Slug        string         `json:"slug"`
	Title       string         `json:"title"`
	Body        string         `json:"body"` // markdown; untrusted at ingress
	Author      string         `json:"author"`
	Section     string         `json:"section"`
	Topics      []string       `json:"topics"`
	PublishedAt time.Time      `json:"published_at"`
	ContentHash string         `json:"content_hash"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	// Deleted reports whether the article is soft-deleted (tombstoned in the store).
	// It is server-derived state, never part of ContentHash (which is over
	// title/body/author/section only), and is omitted from JSON when false so an
	// active article serializes exactly as before.
	Deleted   bool      `json:"deleted,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// PublishInput is what an author agent submits. It mirrors
// contracts/article.schema.json. Server-owned fields (id, content_hash,
// created_at) are never accepted from input.
type PublishInput struct {
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	Author      string         `json:"author"`
	Section     string         `json:"section"`
	Topics      []string       `json:"topics,omitempty"`
	Slug        string         `json:"slug,omitempty"`
	PublishedAt *time.Time     `json:"published_at,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ValidationError reports field-level problems; the publish layer maps it to a
// 422 response. The zero value is not usable; build it via NewArticle.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for f, m := range e.Fields {
		parts = append(parts, fmt.Sprintf("%s: %s", f, m))
	}
	sort.Strings(parts) // stable message regardless of map iteration order
	return "invalid article: " + strings.Join(parts, "; ")
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugTranslit folds common Latin (especially Spanish) accented letters to ASCII
// so "política", "politica" and "Política" all slugify to the same value. It runs
// after lowercasing. Scripts with no ASCII fold (e.g. CJK) are still dropped, and
// callers supply a fallback for an empty result.
var slugTranslit = strings.NewReplacer(
	"á", "a", "à", "a", "ä", "a", "â", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ë", "e", "ê", "e",
	"í", "i", "ì", "i", "ï", "i", "î", "i",
	"ó", "o", "ò", "o", "ö", "o", "ô", "o", "õ", "o",
	"ú", "u", "ù", "u", "ü", "u", "û", "u",
	"ñ", "n", "ç", "c", "ý", "y", "ÿ", "y",
)

// Slugify turns arbitrary text into a URL-safe slug: lowercase ASCII
// alphanumerics joined by single hyphens, with no leading or trailing hyphen.
// Common Latin accents are transliterated (á->a, ñ->n, ...); other non-ASCII
// characters are dropped. Returns "" when nothing usable remains; callers supply
// a fallback.
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugTranslit.Replace(s)
	s = slugNonAlnum.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// ContentHash is the stable dedup and idempotency identity of an article: a
// SHA-256 over its normalized title, body, author, and section. Two submissions
// with the same identity collapse to one article. Fields are length-prefixed so
// shifting a boundary (e.g. "ab"+"c" vs "a"+"bc") cannot collide.
func ContentHash(title, body, author, section string) string {
	h := sha256.New()
	for _, f := range []string{title, body, author, section} {
		fmt.Fprintf(h, "%d:%s", len(f), f)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// normalizeTopics trims, drops blanks, and removes case-insensitive duplicates
// while preserving first-seen order and original casing.
func normalizeTopics(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	return out
}

// NewArticle validates and normalizes a submission into a canonical Article.
// now is injected so time is deterministic for callers and tests. It derives the
// slug from the title when absent (falling back to a content-hash prefix when a
// title has no usable characters) and computes the content hash. The returned
// Article has no ID; the store assigns identity on insert.
func NewArticle(in PublishInput, now time.Time) (Article, error) {
	title := strings.TrimSpace(in.Title)
	body := strings.TrimSpace(in.Body)
	author := strings.TrimSpace(in.Author)
	section := strings.TrimSpace(in.Section)

	ve := &ValidationError{Fields: map[string]string{}}
	if title == "" {
		ve.Fields["title"] = "required"
	}
	if body == "" {
		ve.Fields["body"] = "required"
	}
	if author == "" {
		ve.Fields["author"] = "required"
	}
	if section == "" {
		ve.Fields["section"] = "required"
	}
	if len(ve.Fields) > 0 {
		return Article{}, ve
	}

	hash := ContentHash(title, body, author, section)

	slug := Slugify(in.Slug)
	if slug == "" {
		slug = Slugify(title)
	}
	if slug == "" {
		slug = hash[:12]
	}

	published := now.UTC()
	if in.PublishedAt != nil {
		published = in.PublishedAt.UTC()
	}

	return Article{
		Slug:        slug,
		Title:       title,
		Body:        body,
		Author:      author,
		Section:     section,
		Topics:      normalizeTopics(in.Topics),
		PublishedAt: published,
		ContentHash: hash,
		Metadata:    in.Metadata,
		CreatedAt:   now.UTC(),
	}, nil
}
