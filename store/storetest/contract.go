// Package storetest provides the conformance suite for the store: the executable
// behavioral spec every store.Repository implementation must pass. It is the
// single place that pins what the store guarantees (faithful round-trips,
// deterministic byte-order sorting, whole-second timestamps, atomic batch writes,
// tombstone and restore), so the sqlite package tests assert behavior against the
// spec rather than re-deriving it.
package storetest

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
)

func mustArticle(t *testing.T, in domain.PublishInput, at time.Time) domain.Article {
	t.Helper()
	a, err := domain.NewArticle(in, at)
	if err != nil {
		t.Fatalf("build article: %v", err)
	}
	return a
}

func setEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func slugsOf(arts []domain.Article) []string {
	out := make([]string, len(arts))
	for i, a := range arts {
		out[i] = a.Slug
	}
	return out
}

func equalOrdered(a, b []string) bool {
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

// Run executes the full Repository conformance suite against repo, which must be
// empty. Subtests run sequentially and share the seeded state.
func Run(t *testing.T, repo store.Repository) {
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	seed := []domain.Article{
		mustArticle(t, domain.PublishInput{Title: "Go 1.26 ships", Body: "b1", Author: "ada", Section: "tech", Topics: []string{"go", "release"}}, base),
		mustArticle(t, domain.PublishInput{Title: "Election results", Body: "b2", Author: "bo", Section: "politics", Topics: []string{"election"}}, base.Add(24*time.Hour)),
		mustArticle(t, domain.PublishInput{Title: "Markets dip", Body: "b3", Author: "ada", Section: "economics", Topics: []string{"markets", "go"}}, base.Add(48*time.Hour)),
	}
	for _, a := range seed {
		res, err := repo.Upsert(ctx, a)
		if err != nil {
			t.Fatalf("seed upsert %q: %v", a.Slug, err)
		}
		if !res.Created {
			t.Fatalf("seed upsert %q: Created=false, want true", a.Slug)
		}
		if res.Article.ID == "" {
			t.Fatalf("seed upsert %q: empty ID, want store-assigned", a.Slug)
		}
	}

	t.Run("Upsert dedups on content hash (idempotent publish)", func(t *testing.T) {
		res, err := repo.Upsert(ctx, seed[0])
		if err != nil {
			t.Fatalf("re-upsert: %v", err)
		}
		if res.Created {
			t.Errorf("Created=true on duplicate content hash, want false")
		}
		n, err := repo.Count(ctx, store.Filter{})
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != len(seed) {
			t.Errorf("count = %d after duplicate upsert, want %d", n, len(seed))
		}
	})

	t.Run("BySlug returns the article, with topics, or ErrNotFound", func(t *testing.T) {
		got, err := repo.BySlug(ctx, seed[0].Slug)
		if err != nil {
			t.Fatalf("BySlug: %v", err)
		}
		if got.Title != seed[0].Title || got.ContentHash != seed[0].ContentHash {
			t.Errorf("BySlug returned wrong article: %+v", got)
		}
		if !setEqual(got.Topics, seed[0].Topics) {
			t.Errorf("topics roundtrip = %v, want %v", got.Topics, seed[0].Topics)
		}
		if _, err := repo.BySlug(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("BySlug(missing) err = %v, want ErrNotFound", err)
		}
	})

	t.Run("Find by section", func(t *testing.T) {
		got, err := repo.Find(ctx, store.Filter{Section: "tech"})
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if !equalOrdered(slugsOf(got), []string{seed[0].Slug}) {
			t.Errorf("section=tech -> %v, want [%s]", slugsOf(got), seed[0].Slug)
		}
	})

	t.Run("Find by author", func(t *testing.T) {
		got, err := repo.Find(ctx, store.Filter{Author: "ada"})
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if !setEqual(slugsOf(got), []string{seed[0].Slug, seed[2].Slug}) {
			t.Errorf("author=ada -> %v, want {%s,%s}", slugsOf(got), seed[0].Slug, seed[2].Slug)
		}
	})

	t.Run("Find by topic (normalized join)", func(t *testing.T) {
		got, err := repo.Find(ctx, store.Filter{Topic: "go"})
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if !setEqual(slugsOf(got), []string{seed[0].Slug, seed[2].Slug}) {
			t.Errorf("topic=go -> %v, want {%s,%s}", slugsOf(got), seed[0].Slug, seed[2].Slug)
		}
	})

	t.Run("Find by date range (inclusive From, exclusive To)", func(t *testing.T) {
		got, err := repo.Find(ctx, store.Filter{From: base.Add(24 * time.Hour), To: base.Add(48 * time.Hour)})
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if !equalOrdered(slugsOf(got), []string{seed[1].Slug}) {
			t.Errorf("date range -> %v, want [%s]", slugsOf(got), seed[1].Slug)
		}
	})

	t.Run("Ordering and paging", func(t *testing.T) {
		newest, err := repo.Find(ctx, store.Filter{Order: store.NewestFirst})
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		wantNewest := []string{seed[2].Slug, seed[1].Slug, seed[0].Slug}
		if !equalOrdered(slugsOf(newest), wantNewest) {
			t.Errorf("newest-first -> %v, want %v", slugsOf(newest), wantNewest)
		}
		oldest, err := repo.Find(ctx, store.Filter{Order: store.OldestFirst})
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		wantOldest := []string{seed[0].Slug, seed[1].Slug, seed[2].Slug}
		if !equalOrdered(slugsOf(oldest), wantOldest) {
			t.Errorf("oldest-first -> %v, want %v", slugsOf(oldest), wantOldest)
		}
		page, err := repo.Find(ctx, store.Filter{Order: store.NewestFirst, Limit: 1, Offset: 1})
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if !equalOrdered(slugsOf(page), []string{seed[1].Slug}) {
			t.Errorf("page(limit1,offset1) -> %v, want [%s]", slugsOf(page), seed[1].Slug)
		}
	})

	t.Run("Count respects filter and ignores paging", func(t *testing.T) {
		n, err := repo.Count(ctx, store.Filter{Author: "ada", Limit: 1, Offset: 1})
		if err != nil {
			t.Fatalf("Count: %v", err)
		}
		if n != 2 {
			t.Errorf("count(author=ada) = %d, want 2 (paging ignored)", n)
		}
	})

	// Appended last so it never perturbs the seed-based count assertions above.
	// Production feeds time.Now() (sub-second) for both published_at and
	// created_at. The store truncates to whole seconds on write (SQLite stores
	// RFC3339 text at second precision), so the instant returned through the
	// Repository interface is always whole-second. This case fails if the store
	// ever stops truncating (Nanosecond() != 0).
	t.Run("Sub-second timestamps persist at whole-second resolution", func(t *testing.T) {
		subSec := time.Date(2026, 6, 5, 10, 20, 30, 123456789, time.UTC)
		pub := time.Date(2026, 6, 4, 8, 15, 30, 987654321, time.UTC)
		art := mustArticle(t, domain.PublishInput{
			Title:       "Timestamp precision check",
			Body:        "sub-second",
			Author:      "chronos",
			Section:     "meta",
			Topics:      []string{"precision"},
			PublishedAt: &pub,
		}, subSec)

		res, err := repo.Upsert(ctx, art)
		if err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if !res.Created {
			t.Fatalf("Created = false, want true (distinct content hash)")
		}
		assertWholeSecond(t, "Upsert", res.Article, pub, subSec)

		got, err := repo.BySlug(ctx, art.Slug)
		if err != nil {
			t.Fatalf("BySlug: %v", err)
		}
		assertWholeSecond(t, "BySlug", got, pub, subSec)
	})
}

// RunFilters executes the multi-value + full-text Filter conformance suite
// against repo, which must be empty. It pins the widened Filter's behavior,
// including the LIKE-escaping and the ASCII case-folding boundary. Subtests run
// sequentially and share the seeded state; none of them mutate it after seeding,
// so order is irrelevant. It does not call t.Parallel.
func RunFilters(t *testing.T, repo store.Repository) {
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Eight articles spanning multiple sections, authors, and topics, plus
	// Spanish-accented text and literal LIKE metacharacters ('50%', 'a_b') and a
	// near-miss ('axb', '50 points') that a naive (unescaped) LIKE would wrongly
	// match. Each at = base + i*24h, so newest-first order is strictly i descending.
	seed := []domain.Article{
		mustArticle(t, domain.PublishInput{Title: "Election night", Body: "ballots counted across the country", Author: "ada", Section: "politics", Topics: []string{"election"}}, base),
		mustArticle(t, domain.PublishInput{Title: "Outlook report", Body: "la economía crece despacio", Author: "lin", Section: "economics", Topics: []string{"markets"}}, base.Add(24*time.Hour)),
		mustArticle(t, domain.PublishInput{Title: "Gadget roundup", Body: "a fresh chip arrives", Author: "bo", Section: "tech", Topics: []string{"ai"}}, base.Add(48*time.Hour)),
		mustArticle(t, domain.PublishInput{Title: "Models on the floor", Body: "traders watch the screens", Author: "ada", Section: "tech", Topics: []string{"ai", "markets"}}, base.Add(72*time.Hour)),
		mustArticle(t, domain.PublishInput{Title: "Rate decision", Body: "the central bank cut rates", Author: "lin", Section: "economics", Topics: []string{"markets"}}, base.Add(96*time.Hour)),
		mustArticle(t, domain.PublishInput{Title: "Sale at 50% off", Body: "queues formed early", Author: "cy", Section: "shopping", Topics: []string{"retail"}}, base.Add(120*time.Hour)),
		mustArticle(t, domain.PublishInput{Title: "Naming a_b clearly", Body: "small style guide note", Author: "cy", Section: "tech", Topics: []string{"code"}}, base.Add(144*time.Hour)),
		mustArticle(t, domain.PublishInput{Title: "Naming axb clearly", Body: "earnings rose 50 points", Author: "di", Section: "tech", Topics: []string{"code"}}, base.Add(168*time.Hour)),
	}
	for _, a := range seed {
		res, err := repo.Upsert(ctx, a)
		if err != nil {
			t.Fatalf("seed upsert %q: %v", a.Slug, err)
		}
		if !res.Created {
			t.Fatalf("seed upsert %q: Created=false, want true", a.Slug)
		}
	}
	allSlugs := slugsOf(seed)

	// findSlugs runs Find and returns the result slugs in result order.
	findSlugs := func(t *testing.T, f store.Filter) []string {
		t.Helper()
		got, err := repo.Find(ctx, f)
		if err != nil {
			t.Fatalf("Find(%+v): %v", f, err)
		}
		return slugsOf(got)
	}

	t.Run("Sections: multi-value OR within axis, order respected", func(t *testing.T) {
		// politics(i0) + economics(i1,i4); newest-first -> i4,i1,i0.
		got := findSlugs(t, store.Filter{Sections: []string{"politics", "economics"}})
		want := []string{seed[4].Slug, seed[1].Slug, seed[0].Slug}
		if !equalOrdered(got, want) {
			t.Errorf("Sections=[politics,economics] -> %v, want %v", got, want)
		}
	})

	t.Run("Authors: multi-value OR within axis (union)", func(t *testing.T) {
		got := findSlugs(t, store.Filter{Authors: []string{"ada", "lin"}})
		want := []string{seed[0].Slug, seed[1].Slug, seed[3].Slug, seed[4].Slug}
		if !setEqual(got, want) {
			t.Errorf("Authors=[ada,lin] -> %v, want %v", got, want)
		}
	})

	t.Run("Topics: any-of membership, article with several values appears once", func(t *testing.T) {
		// ai -> i2,i3; markets -> i1,i3,i4. i3 has both yet must appear once.
		got := findSlugs(t, store.Filter{Topics: []string{"ai", "markets"}})
		want := []string{seed[1].Slug, seed[2].Slug, seed[3].Slug, seed[4].Slug}
		if !setEqual(got, want) {
			t.Errorf("Topics=[ai,markets] -> %v, want %v", got, want)
		}
		if len(got) != len(want) {
			t.Errorf("Topics dedup failed: got %d rows %v, want %d unique", len(got), got, len(want))
		}
	})

	t.Run("Query: ASCII case-insensitive over body", func(t *testing.T) {
		// "bank" appears only in i4's body ("the central bank cut rates").
		got := findSlugs(t, store.Filter{Query: "BANK"})
		if !equalOrdered(got, []string{seed[4].Slug}) {
			t.Errorf("Query=BANK -> %v, want [%s]", got, seed[4].Slug)
		}
	})

	t.Run("Query: ASCII case-insensitive over title", func(t *testing.T) {
		// "roundup" appears only in i2's title ("Gadget roundup"), no body has it.
		got := findSlugs(t, store.Filter{Query: "ROUNDUP"})
		if !equalOrdered(got, []string{seed[2].Slug}) {
			t.Errorf("Query=ROUNDUP -> %v, want [%s]", got, seed[2].Slug)
		}
	})

	t.Run("Query: '%' is escaped, not a wildcard", func(t *testing.T) {
		// Only i5 contains the literal "50%". i7 contains "50 points": a naive
		// (unescaped) "%50%%" pattern would match it, the escaped one must not.
		got := findSlugs(t, store.Filter{Query: "50%"})
		if !equalOrdered(got, []string{seed[5].Slug}) {
			t.Errorf("Query=50%% -> %v, want only [%s] (literal, '%%' not a wildcard)", got, seed[5].Slug)
		}
	})

	t.Run("Query: '_' is escaped, not a wildcard", func(t *testing.T) {
		// Only i6 contains literal "a_b". i7 contains "axb": an unescaped "_" would
		// match it (single-char wildcard), the escaped one must not.
		got := findSlugs(t, store.Filter{Query: "a_b"})
		if !equalOrdered(got, []string{seed[6].Slug}) {
			t.Errorf("Query=a_b -> %v, want only [%s] ('_' not a wildcard)", got, seed[6].Slug)
		}
	})

	t.Run("Query: exact non-ASCII substring, same case", func(t *testing.T) {
		// Same-case accented substring matches. (We do NOT assert upper-folding
		// like "ECONOMÍA": SQLite's lower() would not fold it.)
		got := findSlugs(t, store.Filter{Query: "economía"})
		if !equalOrdered(got, []string{seed[1].Slug}) {
			t.Errorf("Query=economía -> %v, want [%s]", got, seed[1].Slug)
		}
	})

	t.Run("Combination: Sections AND date range AND Query intersect", func(t *testing.T) {
		// Sections{economics,tech} = {i1,i2,i3,i4,i6,i7}; [base,base+72h) = {i0,i1,i2};
		// Query "chip" = {i2}. Intersection = {i2}.
		f := store.Filter{
			Sections: []string{"economics", "tech"},
			From:     base,
			To:       base.Add(72 * time.Hour),
			Query:    "chip",
		}
		got := findSlugs(t, f)
		if !equalOrdered(got, []string{seed[2].Slug}) {
			t.Errorf("combination filter -> %v, want [%s]", got, seed[2].Slug)
		}
	})

	t.Run("Scalar Section and plural Sections AND together (scalar not overridden)", func(t *testing.T) {
		// Consistent pair narrows to the scalar: politics IN {politics,economics}.
		got := findSlugs(t, store.Filter{Section: "politics", Sections: []string{"politics", "economics"}})
		if !equalOrdered(got, []string{seed[0].Slug}) {
			t.Errorf("Section=politics + Sections=[politics,economics] -> %v, want [%s]", got, seed[0].Slug)
		}
		// Contradictory pair yields empty, proving the scalar is still applied.
		empty := findSlugs(t, store.Filter{Section: "tech", Sections: []string{"politics", "economics"}})
		if len(empty) != 0 {
			t.Errorf("Section=tech + Sections=[politics,economics] -> %v, want [] (AND)", empty)
		}
	})

	t.Run("Empty and all-blank slices and blank Query impose no constraint", func(t *testing.T) {
		cases := []struct {
			name string
			f    store.Filter
		}{
			{"Sections nil", store.Filter{Sections: nil}},
			{"Sections empty", store.Filter{Sections: []string{}}},
			{"Sections all-blank", store.Filter{Sections: []string{"", "  "}}},
			{"Authors all-blank", store.Filter{Authors: []string{"", "\t"}}},
			{"Topics all-blank", store.Filter{Topics: []string{"", " "}}},
			{"Query empty", store.Filter{Query: ""}},
			{"Query whitespace", store.Filter{Query: "   "}},
		}
		for _, tc := range cases {
			got := findSlugs(t, tc.f)
			if !setEqual(got, allSlugs) {
				t.Errorf("%s: -> %v, want full set %v", tc.name, got, allSlugs)
			}
		}
	})

	t.Run("Count applies the same predicates as Find (parity)", func(t *testing.T) {
		filters := []store.Filter{
			{Sections: []string{"politics", "economics"}},
			{Topics: []string{"ai", "markets"}},
			{Query: "50%"},
			{Authors: []string{"ada", "lin"}, Query: "the"},
		}
		for _, f := range filters {
			got, err := repo.Find(ctx, f)
			if err != nil {
				t.Fatalf("Find(%+v): %v", f, err)
			}
			n, err := repo.Count(ctx, f)
			if err != nil {
				t.Fatalf("Count(%+v): %v", f, err)
			}
			if n != len(got) {
				t.Errorf("Count(%+v) = %d, want %d (== len(Find))", f, n, len(got))
			}
		}
	})
}

// RunFacets executes the Facets conformance suite against repo, which must be
// empty. It pins the aggregate values, counts, and the deterministic ordering
// (Count DESC, then Value ASC). It does not call t.Parallel.
func RunFacets(t *testing.T, repo store.Repository) {
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Six articles arranged so each axis exercises BOTH ordering keys: a clear
	// Count-DESC winner and a Count tie broken by Value-ASC. The sixth article
	// introduces a mixed-case value on every axis (Section "World", Author "Zara",
	// Topic "Zen") to pin that the tie-break is byte order, not a locale collation:
	// under SQLite's BINARY collation every uppercase letter (0x41-0x5A) sorts
	// before every lowercase one (0x61-0x7A), so "World" < the lowercase
	// "economics", and "Zara"/"Zen" lead their count-tie groups. A locale collation
	// (e.g. en_US.UTF-8) would instead order these case-blended (economics < World)
	// or last (Zara, Zen at the 'z' position), so these assertions FAIL if the
	// ordering ever stops being byte order. Section/author are stored verbatim
	// (only trimmed) and topics preserve their original casing
	// (domain.normalizeTopics), so the mixed case reaches the SQL.
	//   Sections: tech=2, politics=2 (tie -> politics<tech), World=1, economics=1 (tie -> World<economics)
	//   Authors:  ada=3, Zara=1, bo=1, cy=1 (tie -> Zara<bo<cy)
	//   Topics:   go=3, election=2, Zen=1, markets=1, release=1 (tie -> Zen<markets<release)
	seed := []domain.Article{
		mustArticle(t, domain.PublishInput{Title: "T1", Body: "b", Author: "ada", Section: "tech", Topics: []string{"go", "release"}}, base),
		mustArticle(t, domain.PublishInput{Title: "T2", Body: "b", Author: "bo", Section: "tech", Topics: []string{"go"}}, base.Add(24*time.Hour)),
		mustArticle(t, domain.PublishInput{Title: "T3", Body: "b", Author: "ada", Section: "politics", Topics: []string{"election"}}, base.Add(48*time.Hour)),
		// One article carrying two topics, to prove COUNT(*) over the join table
		// counts it once per distinct topic, never double.
		mustArticle(t, domain.PublishInput{Title: "T4", Body: "b", Author: "cy", Section: "politics", Topics: []string{"election", "go"}}, base.Add(72*time.Hour)),
		mustArticle(t, domain.PublishInput{Title: "T5", Body: "b", Author: "ada", Section: "economics", Topics: []string{"markets"}}, base.Add(96*time.Hour)),
		// Mixed-case tie-break values on every axis (see comment above).
		mustArticle(t, domain.PublishInput{Title: "T6", Body: "b", Author: "Zara", Section: "World", Topics: []string{"Zen"}}, base.Add(120*time.Hour)),
	}
	for _, a := range seed {
		if _, err := repo.Upsert(ctx, a); err != nil {
			t.Fatalf("seed upsert %q: %v", a.Slug, err)
		}
	}

	// A soft-deleted article must NOT inflate any facet count: seed one whose
	// section/author/topic differ from every value asserted below, then delete it.
	// The exact-equality assertions (which never mention these values) would fail if
	// Facets counted it, so this proves tombstoned rows are excluded.
	gone := mustArticle(t, domain.PublishInput{Title: "T7", Body: "b", Author: "ghost", Section: "sports", Topics: []string{"tombstone"}}, base.Add(144*time.Hour))
	if _, err := repo.Upsert(ctx, gone); err != nil {
		t.Fatalf("seed deleted upsert: %v", err)
	}
	if err := repo.DeleteArticle(ctx, gone.Slug); err != nil {
		t.Fatalf("delete article: %v", err)
	}

	got, err := repo.Facets(ctx)
	if err != nil {
		t.Fatalf("Facets: %v", err)
	}

	// Value-ASC ties below are asserted in BYTE order ("World" before lowercase
	// "economics"; "Zara"/"Zen" ahead of their lowercase peers). This is the
	// ordering SQLite's BINARY default gives for free; a locale collation would
	// order these differently.
	assertFacets(t, "Sections", got.Sections, []store.Facet{
		{Value: "politics", Count: 2}, {Value: "tech", Count: 2}, {Value: "World", Count: 1}, {Value: "economics", Count: 1},
	})
	assertFacets(t, "Authors", got.Authors, []store.Facet{
		{Value: "ada", Count: 3}, {Value: "Zara", Count: 1}, {Value: "bo", Count: 1}, {Value: "cy", Count: 1},
	})
	assertFacets(t, "Topics", got.Topics, []store.Facet{
		{Value: "go", Count: 3}, {Value: "election", Count: 2}, {Value: "Zen", Count: 1}, {Value: "markets", Count: 1}, {Value: "release", Count: 1},
	})
}

// assertFacets checks a facet slice for exact value, count, and order equality.
func assertFacets(t *testing.T, axis string, got, want []store.Facet) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %d facets %v, want %d %v", axis, len(got), got, len(want), want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %+v, want %+v (full got %v)", axis, i, got[i], want[i], got)
		}
	}
}

// assertWholeSecond verifies the article's PublishedAt and CreatedAt carry no
// sub-second component and equal the truncated inputs.
func assertWholeSecond(t *testing.T, stage string, a domain.Article, pub, created time.Time) {
	t.Helper()
	if ns := a.PublishedAt.Nanosecond(); ns != 0 {
		t.Errorf("%s: PublishedAt sub-second = %dns, want 0 (the store must truncate)", stage, ns)
	}
	if ns := a.CreatedAt.Nanosecond(); ns != 0 {
		t.Errorf("%s: CreatedAt sub-second = %dns, want 0 (the store must truncate)", stage, ns)
	}
	if !a.PublishedAt.UTC().Equal(pub.Truncate(time.Second)) {
		t.Errorf("%s: PublishedAt = %v, want %v (truncated to whole second)", stage, a.PublishedAt.UTC(), pub.Truncate(time.Second))
	}
	if !a.CreatedAt.UTC().Equal(created.Truncate(time.Second)) {
		t.Errorf("%s: CreatedAt = %v, want %v (truncated to whole second)", stage, a.CreatedAt.UTC(), created.Truncate(time.Second))
	}
}

// RunSubmissionLog executes the SubmissionLog conformance suite against log,
// which must back an empty submissions table. It pins that submissions encode and
// round-trip faithfully. The production caller writes time.Now().UTC()
// (sub-second precision); the store truncates CreatedAt to whole seconds on write
// (SQLite stores RFC3339 text at second precision), so the round-trip is
// whole-second for any input. The sub-second case below pins that contract: it
// fails if the store leaks a sub-second component. It does not call t.Parallel so
// subtests share state.
func RunSubmissionLog(t *testing.T, log store.SubmissionLog) {
	ctx := context.Background()

	t.Run("FindSubmission on missing key reports not found", func(t *testing.T) {
		got, found, err := log.FindSubmission(ctx, "no-such-key")
		if err != nil {
			t.Fatalf("FindSubmission: %v", err)
		}
		if found {
			t.Errorf("found = true for missing key, want false")
		}
		if got.IdempotencyKey != "" || got.ContentHash != "" || got.ArticleID != "" ||
			got.Slug != "" || got.Author != "" || len(got.Scopes) != 0 || !got.CreatedAt.IsZero() {
			t.Errorf("got = %+v for missing key, want zero Submission", got)
		}
	})

	first := store.Submission{
		IdempotencyKey: "idem-1",
		ContentHash:    "hash-1",
		ArticleID:      "42",
		Slug:           "go-1-26-ships",
		Author:         "ada",
		Scopes:         []string{"section:tech", "author:ada", "topic:go"},
		CreatedAt:      time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}

	t.Run("RecordSubmission then FindSubmission roundtrips every field", func(t *testing.T) {
		if err := log.RecordSubmission(ctx, first); err != nil {
			t.Fatalf("RecordSubmission: %v", err)
		}
		got, found, err := log.FindSubmission(ctx, first.IdempotencyKey)
		if err != nil {
			t.Fatalf("FindSubmission: %v", err)
		}
		if !found {
			t.Fatalf("found = false after RecordSubmission, want true")
		}
		assertSubmissionEqual(t, got, first)
	})

	second := store.Submission{
		IdempotencyKey: "idem-2",
		ContentHash:    "hash-2",
		ArticleID:      "7",
		Slug:           "markets-dip",
		Author:         "bo",
		Scopes:         []string{"section:economics", "topic:markets"},
		CreatedAt:      time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC),
	}

	t.Run("A distinct key roundtrips independently", func(t *testing.T) {
		if err := log.RecordSubmission(ctx, second); err != nil {
			t.Fatalf("RecordSubmission: %v", err)
		}
		got, found, err := log.FindSubmission(ctx, second.IdempotencyKey)
		if err != nil {
			t.Fatalf("FindSubmission: %v", err)
		}
		if !found {
			t.Fatalf("found = false for second key, want true")
		}
		assertSubmissionEqual(t, got, second)

		// The first record is untouched by the second write.
		gotFirst, found, err := log.FindSubmission(ctx, first.IdempotencyKey)
		if err != nil {
			t.Fatalf("FindSubmission(first): %v", err)
		}
		if !found {
			t.Fatalf("found = false for first key after second write, want true")
		}
		assertSubmissionEqual(t, gotFirst, first)
	})

	t.Run("Empty scopes roundtrips as no scopes", func(t *testing.T) {
		empty := store.Submission{
			IdempotencyKey: "idem-empty",
			ContentHash:    "hash-empty",
			ArticleID:      "0",
			Slug:           "no-scopes",
			Author:         "cy",
			Scopes:         nil,
			CreatedAt:      time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC),
		}
		if err := log.RecordSubmission(ctx, empty); err != nil {
			t.Fatalf("RecordSubmission: %v", err)
		}
		got, found, err := log.FindSubmission(ctx, empty.IdempotencyKey)
		if err != nil {
			t.Fatalf("FindSubmission: %v", err)
		}
		if !found {
			t.Fatalf("found = false for empty-scopes key, want true")
		}
		if len(got.Scopes) != 0 {
			t.Errorf("scopes = %v, want empty", got.Scopes)
		}
		assertSubmissionEqual(t, got, empty)
	})

	t.Run("Sub-second CreatedAt truncates to whole seconds", func(t *testing.T) {
		// Production writes h.now().UTC() with sub-second precision (publish.go).
		// The store truncates to whole seconds on write (SQLite stores RFC3339 text
		// at second precision). This case fails if the store keeps a sub-second
		// component.
		raw := time.Date(2026, 6, 4, 8, 15, 30, 123456789, time.UTC)
		sub := store.Submission{
			IdempotencyKey: "idem-subsecond",
			ContentHash:    "hash-subsecond",
			ArticleID:      "99",
			Slug:           "sub-second",
			Author:         "di",
			Scopes:         []string{"section:tech"},
			CreatedAt:      raw,
		}
		if err := log.RecordSubmission(ctx, sub); err != nil {
			t.Fatalf("RecordSubmission: %v", err)
		}
		got, found, err := log.FindSubmission(ctx, sub.IdempotencyKey)
		if err != nil {
			t.Fatalf("FindSubmission: %v", err)
		}
		if !found {
			t.Fatalf("found = false after RecordSubmission, want true")
		}
		want := raw.Truncate(time.Second) // 2026-06-04T08:15:30Z, no sub-second
		if !got.CreatedAt.UTC().Equal(want) {
			t.Errorf("CreatedAt = %v, want %v (truncated to whole second)", got.CreatedAt.UTC(), want)
		}
		if ns := got.CreatedAt.UTC().Nanosecond(); ns != 0 {
			t.Errorf("CreatedAt sub-second = %dns, want 0 (the store must truncate)", ns)
		}
	})

	t.Run("RecordSubmission on a duplicate key errors", func(t *testing.T) {
		if err := log.RecordSubmission(ctx, first); err == nil {
			t.Errorf("RecordSubmission(duplicate key) err = nil, want non-nil (primary-key violation)")
		}
	})
}

// RunListSubmissions executes the ListSubmissions conformance suite against log,
// which must back an empty submissions table. It pins that the audit-log read path
// orders, filters, pages, and round-trips submissions correctly. The seed includes
// two submissions sharing one CreatedAt so the stable idempotency-key DESC
// tiebreak is exercised in byte order (SQLite's BINARY default), the same
// determinism the Facets suite relies on. It does not call t.Parallel so subtests
// share the seeded state; none mutate it.
func RunListSubmissions(t *testing.T, log store.SubmissionLog) {
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	// Authors and timestamps spread so author and date-range filters are distinct,
	// plus a same-CreatedAt pair (idem-z1/idem-z9) to pin the key tiebreak.
	seed := []store.Submission{
		{IdempotencyKey: "idem-a1", ContentHash: "hash-a1", ArticleID: "1", Slug: "alpha", Author: "ada", Scopes: []string{"section:tech", "author:ada"}, CreatedAt: base},
		{IdempotencyKey: "idem-b1", ContentHash: "hash-b1", ArticleID: "2", Slug: "bravo", Author: "bo", Scopes: []string{"section:politics"}, CreatedAt: base.Add(1 * time.Hour)},
		{IdempotencyKey: "idem-a2", ContentHash: "hash-a2", ArticleID: "3", Slug: "charlie", Author: "ada", Scopes: nil, CreatedAt: base.Add(2 * time.Hour)},
		{IdempotencyKey: "idem-c1", ContentHash: "hash-c1", ArticleID: "4", Slug: "delta", Author: "cy", Scopes: []string{"topic:go", "topic:ai"}, CreatedAt: base.Add(3 * time.Hour)},
		{IdempotencyKey: "idem-a3", ContentHash: "hash-a3", ArticleID: "5", Slug: "echo", Author: "ada", Scopes: []string{"section:economics"}, CreatedAt: base.Add(4 * time.Hour)},
		{IdempotencyKey: "idem-z1", ContentHash: "hash-z1", ArticleID: "6", Slug: "foxtrot", Author: "di", Scopes: []string{"section:tech"}, CreatedAt: base.Add(5 * time.Hour)},
		{IdempotencyKey: "idem-z9", ContentHash: "hash-z9", ArticleID: "7", Slug: "golf", Author: "di", Scopes: []string{"section:tech"}, CreatedAt: base.Add(5 * time.Hour)},
	}
	for _, s := range seed {
		if err := log.RecordSubmission(ctx, s); err != nil {
			t.Fatalf("seed RecordSubmission %q: %v", s.IdempotencyKey, err)
		}
	}

	keysOf := func(subs []store.Submission) []string {
		out := make([]string, len(subs))
		for i, s := range subs {
			out[i] = s.IdempotencyKey
		}
		return out
	}
	list := func(t *testing.T, f store.ListSubmissionsFilter) []store.Submission {
		t.Helper()
		got, err := log.ListSubmissions(ctx, f)
		if err != nil {
			t.Fatalf("ListSubmissions(%+v): %v", f, err)
		}
		return got
	}

	t.Run("Newest first with stable idempotency-key tiebreak", func(t *testing.T) {
		// created_at DESC, then idempotency_key DESC. The +5h pair ties on time, so
		// idem-z9 leads idem-z1 (byte order, DESC).
		want := []string{"idem-z9", "idem-z1", "idem-a3", "idem-c1", "idem-a2", "idem-b1", "idem-a1"}
		if got := keysOf(list(t, store.ListSubmissionsFilter{})); !equalOrdered(got, want) {
			t.Errorf("order = %v, want %v", got, want)
		}
	})

	t.Run("Author filter is exact equality", func(t *testing.T) {
		want := []string{"idem-a3", "idem-a2", "idem-a1"}
		if got := keysOf(list(t, store.ListSubmissionsFilter{Author: "ada"})); !equalOrdered(got, want) {
			t.Errorf("author=ada -> %v, want %v", got, want)
		}
	})

	t.Run("Date range (inclusive From, exclusive To)", func(t *testing.T) {
		// [base+1h, base+4h): b1(+1h), a2(+2h), c1(+3h); a3(+4h) excluded (exclusive To).
		f := store.ListSubmissionsFilter{From: base.Add(1 * time.Hour), To: base.Add(4 * time.Hour)}
		want := []string{"idem-c1", "idem-a2", "idem-b1"}
		if got := keysOf(list(t, f)); !equalOrdered(got, want) {
			t.Errorf("date range -> %v, want %v", got, want)
		}
	})

	t.Run("Sub-second From bound truncates to a whole second", func(t *testing.T) {
		// idem-a1 is stored at exactly base (whole second), so a From just past it
		// (base+500ms) must still include it: the bound truncates down to base. An
		// untruncated fractional bound would exclude idem-a1.
		want := []string{"idem-z9", "idem-z1", "idem-a3", "idem-c1", "idem-a2", "idem-b1", "idem-a1"}
		if got := keysOf(list(t, store.ListSubmissionsFilter{From: base.Add(500 * time.Millisecond)})); !equalOrdered(got, want) {
			t.Errorf("From=base+500ms -> %v, want %v (bound must truncate to whole second; idem-a1 stays)", got, want)
		}
	})

	t.Run("Limit and Offset page newest-first", func(t *testing.T) {
		if got := keysOf(list(t, store.ListSubmissionsFilter{Limit: 2})); !equalOrdered(got, []string{"idem-z9", "idem-z1"}) {
			t.Errorf("page1(limit2) -> %v, want [idem-z9 idem-z1]", got)
		}
		if got := keysOf(list(t, store.ListSubmissionsFilter{Limit: 2, Offset: 2})); !equalOrdered(got, []string{"idem-a3", "idem-c1"}) {
			t.Errorf("page2(limit2,offset2) -> %v, want [idem-a3 idem-c1]", got)
		}
		if got := keysOf(list(t, store.ListSubmissionsFilter{Offset: 5})); !equalOrdered(got, []string{"idem-b1", "idem-a1"}) {
			t.Errorf("offset5(no limit) -> %v, want [idem-b1 idem-a1]", got)
		}
	})

	t.Run("Every field and scopes round-trip", func(t *testing.T) {
		got := list(t, store.ListSubmissionsFilter{Author: "cy"})
		if len(got) != 1 {
			t.Fatalf("author=cy returned %d, want 1", len(got))
		}
		assertSubmissionEqual(t, got[0], seed[3]) // idem-c1, including its two scopes
	})

	t.Run("Empty filter lists every submission", func(t *testing.T) {
		if got := list(t, store.ListSubmissionsFilter{}); len(got) != len(seed) {
			t.Errorf("listed %d, want %d", len(got), len(seed))
		}
	})
}

func assertSubmissionEqual(t *testing.T, got, want store.Submission) {
	t.Helper()
	if got.IdempotencyKey != want.IdempotencyKey {
		t.Errorf("IdempotencyKey = %q, want %q", got.IdempotencyKey, want.IdempotencyKey)
	}
	if got.ContentHash != want.ContentHash {
		t.Errorf("ContentHash = %q, want %q", got.ContentHash, want.ContentHash)
	}
	if got.ArticleID != want.ArticleID {
		t.Errorf("ArticleID = %q, want %q", got.ArticleID, want.ArticleID)
	}
	if got.Slug != want.Slug {
		t.Errorf("Slug = %q, want %q", got.Slug, want.Slug)
	}
	if got.Author != want.Author {
		t.Errorf("Author = %q, want %q", got.Author, want.Author)
	}
	if !setEqual(got.Scopes, want.Scopes) {
		t.Errorf("Scopes = %v, want %v", got.Scopes, want.Scopes)
	}
	if !got.CreatedAt.UTC().Equal(want.CreatedAt.UTC()) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt.UTC(), want.CreatedAt.UTC())
	}
}

// RunUpsertMany executes the UpsertMany conformance suite against repo, which
// must be empty and also implement store.SubmissionLog (the store does, since the
// article and ledger writes must commit in one transaction). It pins the atomic
// batch write, the per-item created/deduplicated classification, idempotent
// replay, and the all-or-nothing rollback. Subtests run sequentially and share
// the seeded state, so each asserts the running article count it expects.
func RunUpsertMany(t *testing.T, repo store.Repository) {
	ctx := context.Background()
	log, ok := repo.(store.SubmissionLog)
	if !ok {
		t.Fatalf("repo %T does not implement store.SubmissionLog; UpsertMany must write the ledger in the same tx", repo)
	}
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	item := func(title, body, author, section string, topics []string, key string, at time.Time) store.UpsertItem {
		return store.UpsertItem{
			Article:        mustArticle(t, domain.PublishInput{Title: title, Body: body, Author: author, Section: section, Topics: topics}, at),
			IdempotencyKey: key,
			Scopes:         []string{"articles:write"},
		}
	}
	count := func(t *testing.T) int {
		t.Helper()
		n, err := repo.Count(ctx, store.Filter{})
		if err != nil {
			t.Fatalf("Count: %v", err)
		}
		return n
	}

	t.Run("Empty batch is a no-op", func(t *testing.T) {
		res, err := repo.UpsertMany(ctx, nil)
		if err != nil {
			t.Fatalf("UpsertMany(nil): %v", err)
		}
		if len(res) != 0 {
			t.Errorf("results = %d, want 0", len(res))
		}
		if n := count(t); n != 0 {
			t.Errorf("count = %d after empty batch, want 0", n)
		}
	})

	batch := []store.UpsertItem{
		item("Batch one", "body one", "ada", "tech", []string{"go", "release"}, "k-1", base),
		item("Batch two", "body two", "bo", "politics", []string{"election"}, "k-2", base.Add(time.Hour)),
		item("Batch three", "body three", "ada", "economics", []string{"markets", "go"}, "k-3", base.Add(2*time.Hour)),
	}

	t.Run("Batch commits all items in order, with topics and ledger rows", func(t *testing.T) {
		res, err := repo.UpsertMany(ctx, batch)
		if err != nil {
			t.Fatalf("UpsertMany: %v", err)
		}
		if len(res) != len(batch) {
			t.Fatalf("results = %d, want %d", len(res), len(batch))
		}
		for i := range res {
			if !res[i].Created {
				t.Errorf("item %d Created=false, want true", i)
			}
			if res[i].Article.ID == "" {
				t.Errorf("item %d empty ID, want store-assigned", i)
			}
			if res[i].Article.Slug != batch[i].Article.Slug {
				t.Errorf("item %d slug = %q, want %q (input order preserved)", i, res[i].Article.Slug, batch[i].Article.Slug)
			}
			sub, found, err := log.FindSubmission(ctx, batch[i].IdempotencyKey)
			if err != nil {
				t.Fatalf("FindSubmission(%q): %v", batch[i].IdempotencyKey, err)
			}
			if !found {
				t.Errorf("item %d: no submission ledger row for key %q", i, batch[i].IdempotencyKey)
			}
			if sub.ContentHash != res[i].Article.ContentHash {
				t.Errorf("item %d: ledger content hash = %q, want %q", i, sub.ContentHash, res[i].Article.ContentHash)
			}
			if !setEqual(sub.Scopes, []string{"articles:write"}) {
				t.Errorf("item %d: ledger scopes = %v, want [articles:write]", i, sub.Scopes)
			}
			got, err := repo.BySlug(ctx, res[i].Article.Slug)
			if err != nil {
				t.Fatalf("BySlug(%q): %v", res[i].Article.Slug, err)
			}
			if !setEqual(got.Topics, batch[i].Article.Topics) {
				t.Errorf("item %d topics = %v, want %v", i, got.Topics, batch[i].Article.Topics)
			}
		}
		if n := count(t); n != len(batch) {
			t.Errorf("count = %d, want %d", n, len(batch))
		}
	})

	t.Run("Replaying the same batch deduplicates every item and writes nothing", func(t *testing.T) {
		res, err := repo.UpsertMany(ctx, batch)
		if err != nil {
			t.Fatalf("UpsertMany replay: %v", err)
		}
		for i := range res {
			if res[i].Created {
				t.Errorf("item %d Created=true on replay, want false (deduplicated)", i)
			}
			if res[i].Article.Slug != batch[i].Article.Slug {
				t.Errorf("item %d slug = %q on replay, want %q", i, res[i].Article.Slug, batch[i].Article.Slug)
			}
		}
		if n := count(t); n != len(batch) {
			t.Errorf("count = %d after replay, want %d (no new rows)", n, len(batch))
		}
	})

	t.Run("Mixed batch: a repeated key deduplicates, a new item is created", func(t *testing.T) {
		mixed := []store.UpsertItem{
			batch[0], // already published -> deduplicated
			item("Batch four", "body four", "cy", "tech", []string{"ai"}, "k-4", base.Add(3*time.Hour)),
		}
		res, err := repo.UpsertMany(ctx, mixed)
		if err != nil {
			t.Fatalf("UpsertMany mixed: %v", err)
		}
		if res[0].Created {
			t.Errorf("mixed[0] Created=true, want false (replay)")
		}
		if !res[1].Created {
			t.Errorf("mixed[1] Created=false, want true (new)")
		}
		if n := count(t); n != len(batch)+1 {
			t.Errorf("count = %d, want %d", n, len(batch)+1)
		}
	})

	t.Run("Content-hash dedup: a fresh key for existing content records the key but adds no article", func(t *testing.T) {
		// Same content as batch[1] (identical content hash) under a brand-new key.
		// Mirrors the single path: the article deduplicates, yet the new key is
		// still recorded in the ledger pointing at the existing article.
		dup := store.UpsertItem{Article: batch[1].Article, IdempotencyKey: "k-2-again", Scopes: []string{"articles:write"}}
		before := count(t)
		res, err := repo.UpsertMany(ctx, []store.UpsertItem{dup})
		if err != nil {
			t.Fatalf("UpsertMany dup-content: %v", err)
		}
		if res[0].Created {
			t.Errorf("Created=true for an existing content hash, want false")
		}
		if after := count(t); after != before {
			t.Errorf("count %d -> %d on content dedup, want unchanged", before, after)
		}
		sub, found, err := log.FindSubmission(ctx, "k-2-again")
		if err != nil {
			t.Fatalf("FindSubmission: %v", err)
		}
		if !found {
			t.Errorf("fresh key k-2-again not recorded in the ledger")
		}
		if sub.ContentHash != batch[1].Article.ContentHash {
			t.Errorf("ledger content hash = %q, want %q", sub.ContentHash, batch[1].Article.ContentHash)
		}
	})

	t.Run("Atomic rollback: a key reused for different content writes nothing and returns BatchConflictError", func(t *testing.T) {
		before := count(t)
		// Item 0 is brand new and valid; item 1 reuses k-1 (already mapped to
		// batch[0]'s content) for DIFFERENT content. The whole batch must roll back,
		// so item 0 must not persist either.
		probe := item("Atomic probe", "should not persist", "di", "tech", []string{"code"}, "k-atomic", base.Add(10*time.Hour))
		conflict := store.UpsertItem{
			Article:        mustArticle(t, domain.PublishInput{Title: "Different content", Body: "different body", Author: "ada", Section: "tech"}, base),
			IdempotencyKey: "k-1",
			Scopes:         []string{"articles:write"},
		}
		_, err := repo.UpsertMany(ctx, []store.UpsertItem{probe, conflict})
		var ce *store.BatchConflictError
		if !errors.As(err, &ce) {
			t.Fatalf("err = %v, want *store.BatchConflictError", err)
		}
		if ce.Index != 1 {
			t.Errorf("conflict Index = %d, want 1", ce.Index)
		}
		if ce.IdempotencyKey != "k-1" {
			t.Errorf("conflict key = %q, want k-1", ce.IdempotencyKey)
		}
		if after := count(t); after != before {
			t.Errorf("count %d -> %d, want unchanged (atomic rollback)", before, after)
		}
		if _, err := repo.BySlug(ctx, probe.Article.Slug); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("BySlug(probe) err = %v, want ErrNotFound (rolled back)", err)
		}
		if _, found, _ := log.FindSubmission(ctx, "k-atomic"); found {
			t.Errorf("k-atomic recorded in the ledger despite rollback")
		}
	})
}

func handlesOf(as []store.Author) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Handle
	}
	return out
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func assertAuthorEqual(t *testing.T, got, want store.Author) {
	t.Helper()
	if got.Handle != want.Handle {
		t.Errorf("Handle = %q, want %q", got.Handle, want.Handle)
	}
	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}
	if got.Bio != want.Bio {
		t.Errorf("Bio = %q, want %q", got.Bio, want.Bio)
	}
	if got.Avatar != want.Avatar {
		t.Errorf("Avatar = %q, want %q", got.Avatar, want.Avatar)
	}
	if got.Gender != want.Gender {
		t.Errorf("Gender = %q, want %q", got.Gender, want.Gender)
	}
	if got.About != want.About {
		t.Errorf("About = %q, want %q", got.About, want.About)
	}
	if got.Style != want.Style {
		t.Errorf("Style = %q, want %q", got.Style, want.Style)
	}
	if !equalOrdered(got.Topics, want.Topics) {
		t.Errorf("Topics = %v, want %v (order preserved)", got.Topics, want.Topics)
	}
	if len(got.Metadata) != len(want.Metadata) {
		t.Errorf("Metadata = %v, want %v", got.Metadata, want.Metadata)
	}
	for k, v := range want.Metadata {
		if got.Metadata[k] != v {
			t.Errorf("Metadata[%q] = %v, want %v", k, got.Metadata[k], v)
		}
	}
	if !got.CreatedAt.UTC().Equal(want.CreatedAt.UTC().Truncate(time.Second)) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt.UTC(), want.CreatedAt.UTC().Truncate(time.Second))
	}
}

// RunAuthorStore executes the AuthorStore conformance suite against as, which must
// back an empty authors table. It pins that the registry round-trips every field,
// orders by handle in byte order (SQLite's BINARY default), and tombstones and
// re-activates correctly. Subtests run sequentially and share the seeded state; it
// does not call t.Parallel.
func RunAuthorStore(t *testing.T, as store.AuthorStore) {
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	t.Run("AuthorByHandle on a missing handle reports not found", func(t *testing.T) {
		got, found, err := as.AuthorByHandle(ctx, "ghost")
		if err != nil {
			t.Fatalf("AuthorByHandle: %v", err)
		}
		if found {
			t.Errorf("found = true for missing handle, want false")
		}
		if got.Handle != "" || got.ID != "" {
			t.Errorf("got = %+v for missing handle, want zero Author", got)
		}
	})

	first := SampleAuthor("author-a")
	first.CreatedAt = base
	first.UpdatedAt = base

	t.Run("UpsertAuthor creates then AuthorByHandle round-trips every field", func(t *testing.T) {
		stored, err := as.UpsertAuthor(ctx, first)
		if err != nil {
			t.Fatalf("UpsertAuthor: %v", err)
		}
		if stored.ID == "" {
			t.Errorf("empty ID, want store-assigned")
		}
		if stored.Deleted {
			t.Errorf("Deleted = true on create, want false")
		}
		got, found, err := as.AuthorByHandle(ctx, first.Handle)
		if err != nil {
			t.Fatalf("AuthorByHandle: %v", err)
		}
		if !found {
			t.Fatalf("found = false after create, want true")
		}
		assertAuthorEqual(t, got, first)
	})

	t.Run("UpsertAuthor on an existing handle updates in place (created_at preserved, updated_at advances)", func(t *testing.T) {
		before, _, _ := as.AuthorByHandle(ctx, first.Handle)
		edit := first
		edit.Name = "Sample Author (edited)"
		edit.Bio = "Bio editada."
		edit.Metadata = map[string]any{"beat": "general", "edited": "yes"}
		edit.UpdatedAt = base.Add(48 * time.Hour)
		stored, err := as.UpsertAuthor(ctx, edit)
		if err != nil {
			t.Fatalf("UpsertAuthor update: %v", err)
		}
		if stored.ID != before.ID {
			t.Errorf("ID changed on update: %s -> %s (want same row)", before.ID, stored.ID)
		}
		if stored.Name != "Sample Author (edited)" || stored.Bio != "Bio editada." {
			t.Errorf("mutable fields not updated: %+v", stored)
		}
		if !stored.CreatedAt.UTC().Equal(base) {
			t.Errorf("CreatedAt = %v, want preserved %v", stored.CreatedAt.UTC(), base)
		}
		if !stored.UpdatedAt.UTC().Equal(base.Add(48 * time.Hour)) {
			t.Errorf("UpdatedAt = %v, want advanced to %v", stored.UpdatedAt.UTC(), base.Add(48*time.Hour))
		}
		all, err := as.ListAuthors(ctx, true)
		if err != nil {
			t.Fatalf("ListAuthors: %v", err)
		}
		n := 0
		for _, a := range all {
			if a.Handle == first.Handle {
				n++
			}
		}
		if n != 1 {
			t.Errorf("rows for handle %q = %d, want 1 (update, not a second insert)", first.Handle, n)
		}
	})

	second := SampleAuthor("author-b")
	second.CreatedAt = base
	second.UpdatedAt = base

	t.Run("ListAuthors orders by handle ascending (byte order)", func(t *testing.T) {
		if _, err := as.UpsertAuthor(ctx, second); err != nil {
			t.Fatalf("UpsertAuthor: %v", err)
		}
		got, err := as.ListAuthors(ctx, false)
		if err != nil {
			t.Fatalf("ListAuthors: %v", err)
		}
		want := []string{"author-a", "author-b"}
		if h := handlesOf(got); !equalOrdered(h, want) {
			t.Errorf("order = %v, want %v", h, want)
		}
	})

	t.Run("DeleteAuthor tombstones: excluded by default, included with includeDeleted, re-upsert re-activates", func(t *testing.T) {
		if err := as.DeleteAuthor(ctx, second.Handle); err != nil {
			t.Fatalf("DeleteAuthor: %v", err)
		}
		def, err := as.ListAuthors(ctx, false)
		if err != nil {
			t.Fatalf("ListAuthors(false): %v", err)
		}
		if contains(handlesOf(def), second.Handle) {
			t.Errorf("deleted author %q still listed by default", second.Handle)
		}
		all, err := as.ListAuthors(ctx, true)
		if err != nil {
			t.Fatalf("ListAuthors(true): %v", err)
		}
		if !contains(handlesOf(all), second.Handle) {
			t.Errorf("deleted author %q missing from includeDeleted list", second.Handle)
		}
		got, found, err := as.AuthorByHandle(ctx, second.Handle)
		if err != nil {
			t.Fatalf("AuthorByHandle(deleted): %v", err)
		}
		if !found || !got.Deleted {
			t.Errorf("AuthorByHandle(deleted) found=%v Deleted=%v, want true/true", found, got.Deleted)
		}
		reborn := second
		reborn.UpdatedAt = base.Add(72 * time.Hour)
		stored, err := as.UpsertAuthor(ctx, reborn)
		if err != nil {
			t.Fatalf("re-upsert: %v", err)
		}
		if stored.Deleted {
			t.Errorf("Deleted = true after re-upsert, want re-activated")
		}
		def2, err := as.ListAuthors(ctx, false)
		if err != nil {
			t.Fatalf("ListAuthors(false) after re-upsert: %v", err)
		}
		if !contains(handlesOf(def2), second.Handle) {
			t.Errorf("re-activated author %q missing from default list", second.Handle)
		}
	})

	t.Run("SetAuthorSources sets links, AuthorSources reads them slug-sorted, AuthorByHandle hydrates Sources", func(t *testing.T) {
		// Insertion order is deliberately not sorted; the read must come back in slug
		// byte order.
		if err := as.SetAuthorSources(ctx, first.Handle, []string{"zeta-src", "alfa-src", "mid-src"}); err != nil {
			t.Fatalf("SetAuthorSources: %v", err)
		}
		want := []string{"alfa-src", "mid-src", "zeta-src"}
		got, err := as.AuthorSources(ctx, first.Handle)
		if err != nil {
			t.Fatalf("AuthorSources: %v", err)
		}
		if !equalOrdered(got, want) {
			t.Errorf("AuthorSources = %v, want %v (slug byte order)", got, want)
		}
		hydrated, found, err := as.AuthorByHandle(ctx, first.Handle)
		if err != nil || !found {
			t.Fatalf("AuthorByHandle: found=%v err=%v", found, err)
		}
		if !equalOrdered(hydrated.Sources, want) {
			t.Errorf("AuthorByHandle Sources = %v, want %v", hydrated.Sources, want)
		}
	})

	t.Run("SetAuthorSources replaces wholesale and drops blanks and duplicates", func(t *testing.T) {
		if err := as.SetAuthorSources(ctx, first.Handle, []string{"keep-src", "", "keep-src", "  ", "other-src"}); err != nil {
			t.Fatalf("SetAuthorSources: %v", err)
		}
		got, err := as.AuthorSources(ctx, first.Handle)
		if err != nil {
			t.Fatalf("AuthorSources: %v", err)
		}
		// Prior links (alfa/mid/zeta) are gone; blanks and the duplicate collapse.
		if !equalOrdered(got, []string{"keep-src", "other-src"}) {
			t.Errorf("AuthorSources = %v, want [keep-src other-src] (replaced, deduped, blanks dropped)", got)
		}
		// Clearing to empty removes every link.
		if err := as.SetAuthorSources(ctx, first.Handle, nil); err != nil {
			t.Fatalf("SetAuthorSources(nil): %v", err)
		}
		if got, _ := as.AuthorSources(ctx, first.Handle); len(got) != 0 {
			t.Errorf("AuthorSources after clear = %v, want empty", got)
		}
	})

	t.Run("SetAuthorSources on a missing author returns ErrNotFound (no orphan links)", func(t *testing.T) {
		if err := as.SetAuthorSources(ctx, "no-such-author", []string{"x-src"}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("SetAuthorSources(missing) = %v, want ErrNotFound", err)
		}
		if got, _ := as.AuthorSources(ctx, "no-such-author"); len(got) != 0 {
			t.Errorf("AuthorSources(missing) = %v, want empty (nothing written)", got)
		}
	})

	t.Run("DeleteAuthor on a missing handle returns ErrNotFound", func(t *testing.T) {
		if err := as.DeleteAuthor(ctx, "no-such-author"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeleteAuthor(missing) = %v, want ErrNotFound", err)
		}
	})
}

func topicSlugsOf(ts []store.Topic) []string {
	out := make([]string, len(ts))
	for i, tp := range ts {
		out[i] = tp.Slug
	}
	return out
}

func assertTopicEqual(t *testing.T, got, want store.Topic) {
	t.Helper()
	if got.Slug != want.Slug {
		t.Errorf("Slug = %q, want %q", got.Slug, want.Slug)
	}
	if got.Label != want.Label {
		t.Errorf("Label = %q, want %q", got.Label, want.Label)
	}
	if got.Description != want.Description {
		t.Errorf("Description = %q, want %q", got.Description, want.Description)
	}
	if len(got.Metadata) != len(want.Metadata) {
		t.Errorf("Metadata = %v, want %v", got.Metadata, want.Metadata)
	}
	for k, v := range want.Metadata {
		if got.Metadata[k] != v {
			t.Errorf("Metadata[%q] = %v, want %v", k, got.Metadata[k], v)
		}
	}
	if !got.CreatedAt.UTC().Equal(want.CreatedAt.UTC().Truncate(time.Second)) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt.UTC(), want.CreatedAt.UTC().Truncate(time.Second))
	}
}

// RunTopicStore executes the TopicStore conformance suite against ts, which must
// back an empty topics table. It pins that the registry round-trips every field,
// orders by slug in byte order (SQLite's BINARY default), and tombstones and
// re-activates correctly. Subtests run sequentially and share the seeded state; it
// does not call t.Parallel. It mirrors RunAuthorStore.
func RunTopicStore(t *testing.T, ts store.TopicStore) {
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	t.Run("TopicBySlug on a missing slug reports not found", func(t *testing.T) {
		got, found, err := ts.TopicBySlug(ctx, "ghost")
		if err != nil {
			t.Fatalf("TopicBySlug: %v", err)
		}
		if found {
			t.Errorf("found = true for missing slug, want false")
		}
		if got.Slug != "" || got.ID != "" {
			t.Errorf("got = %+v for missing slug, want zero Topic", got)
		}
	})

	economia := store.Topic{
		Slug: "economia", Label: "Economia", Description: "Cobertura economica.",
		Metadata: map[string]any{"color": "green"}, CreatedAt: base, UpdatedAt: base,
	}

	t.Run("UpsertTopic creates then TopicBySlug round-trips every field", func(t *testing.T) {
		stored, err := ts.UpsertTopic(ctx, economia)
		if err != nil {
			t.Fatalf("UpsertTopic: %v", err)
		}
		if stored.ID == "" {
			t.Errorf("empty ID, want store-assigned")
		}
		if stored.Deleted {
			t.Errorf("Deleted = true on create, want false")
		}
		got, found, err := ts.TopicBySlug(ctx, economia.Slug)
		if err != nil {
			t.Fatalf("TopicBySlug: %v", err)
		}
		if !found {
			t.Fatalf("found = false after create, want true")
		}
		assertTopicEqual(t, got, economia)
	})

	t.Run("UpsertTopic on an existing slug updates in place (created_at preserved, updated_at advances)", func(t *testing.T) {
		before, _, _ := ts.TopicBySlug(ctx, economia.Slug)
		edit := economia
		edit.Label = "Economia y mercados"
		edit.Description = "Descripcion editada."
		edit.Metadata = map[string]any{"color": "green", "edited": "yes"}
		edit.UpdatedAt = base.Add(48 * time.Hour)
		stored, err := ts.UpsertTopic(ctx, edit)
		if err != nil {
			t.Fatalf("UpsertTopic update: %v", err)
		}
		if stored.ID != before.ID {
			t.Errorf("ID changed on update: %s -> %s (want same row)", before.ID, stored.ID)
		}
		if stored.Label != "Economia y mercados" || stored.Description != "Descripcion editada." {
			t.Errorf("mutable fields not updated: %+v", stored)
		}
		if !stored.CreatedAt.UTC().Equal(base) {
			t.Errorf("CreatedAt = %v, want preserved %v", stored.CreatedAt.UTC(), base)
		}
		if !stored.UpdatedAt.UTC().Equal(base.Add(48 * time.Hour)) {
			t.Errorf("UpdatedAt = %v, want advanced to %v", stored.UpdatedAt.UTC(), base.Add(48*time.Hour))
		}
		all, err := ts.ListTopics(ctx, true)
		if err != nil {
			t.Fatalf("ListTopics: %v", err)
		}
		n := 0
		for _, tp := range all {
			if tp.Slug == economia.Slug {
				n++
			}
		}
		if n != 1 {
			t.Errorf("rows for slug %q = %d, want 1 (update, not a second insert)", economia.Slug, n)
		}
	})

	deportes := store.Topic{Slug: "deportes", Label: "Deportes", CreatedAt: base, UpdatedAt: base}

	t.Run("ListTopics orders by slug ascending (byte order)", func(t *testing.T) {
		if _, err := ts.UpsertTopic(ctx, deportes); err != nil {
			t.Fatalf("UpsertTopic: %v", err)
		}
		got, err := ts.ListTopics(ctx, false)
		if err != nil {
			t.Fatalf("ListTopics: %v", err)
		}
		want := []string{"deportes", "economia"}
		if s := topicSlugsOf(got); !equalOrdered(s, want) {
			t.Errorf("order = %v, want %v", s, want)
		}
	})

	t.Run("DeleteTopic tombstones: excluded by default, included with includeDeleted, re-upsert re-activates", func(t *testing.T) {
		if err := ts.DeleteTopic(ctx, deportes.Slug); err != nil {
			t.Fatalf("DeleteTopic: %v", err)
		}
		def, err := ts.ListTopics(ctx, false)
		if err != nil {
			t.Fatalf("ListTopics(false): %v", err)
		}
		if contains(topicSlugsOf(def), deportes.Slug) {
			t.Errorf("deleted topic %q still listed by default", deportes.Slug)
		}
		all, err := ts.ListTopics(ctx, true)
		if err != nil {
			t.Fatalf("ListTopics(true): %v", err)
		}
		if !contains(topicSlugsOf(all), deportes.Slug) {
			t.Errorf("deleted topic %q missing from includeDeleted list", deportes.Slug)
		}
		got, found, err := ts.TopicBySlug(ctx, deportes.Slug)
		if err != nil {
			t.Fatalf("TopicBySlug(deleted): %v", err)
		}
		if !found || !got.Deleted {
			t.Errorf("TopicBySlug(deleted) found=%v Deleted=%v, want true/true", found, got.Deleted)
		}
		reborn := deportes
		reborn.UpdatedAt = base.Add(72 * time.Hour)
		stored, err := ts.UpsertTopic(ctx, reborn)
		if err != nil {
			t.Fatalf("re-upsert: %v", err)
		}
		if stored.Deleted {
			t.Errorf("Deleted = true after re-upsert, want re-activated")
		}
		def2, err := ts.ListTopics(ctx, false)
		if err != nil {
			t.Fatalf("ListTopics(false) after re-upsert: %v", err)
		}
		if !contains(topicSlugsOf(def2), deportes.Slug) {
			t.Errorf("re-activated topic %q missing from default list", deportes.Slug)
		}
	})

	t.Run("DeleteTopic on a missing slug returns ErrNotFound", func(t *testing.T) {
		if err := ts.DeleteTopic(ctx, "no-such-topic"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeleteTopic(missing) = %v, want ErrNotFound", err)
		}
	})
}

func portadaDatesOf(ps []store.PortadaDay) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Date
	}
	return out
}

// assertPortadaEqual checks the observable fields round-trip, including entry
// order and role and the recomendado list order (both are ordered, not sets).
func assertPortadaEqual(t *testing.T, got, want store.PortadaDay) {
	t.Helper()
	if got.Date != want.Date {
		t.Errorf("Date = %q, want %q", got.Date, want.Date)
	}
	if len(got.Entries) != len(want.Entries) {
		t.Fatalf("Entries len = %d, want %d (got %+v)", len(got.Entries), len(want.Entries), got.Entries)
	}
	for i := range want.Entries {
		if got.Entries[i] != want.Entries[i] {
			t.Errorf("Entries[%d] = %+v, want %+v (order + role must round-trip)", i, got.Entries[i], want.Entries[i])
		}
	}
	if !equalOrdered(got.Recomendado, want.Recomendado) {
		t.Errorf("Recomendado = %v, want %v (order preserved)", got.Recomendado, want.Recomendado)
	}
	if !got.CreatedAt.UTC().Equal(want.CreatedAt.UTC().Truncate(time.Second)) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt.UTC(), want.CreatedAt.UTC().Truncate(time.Second))
	}
}

// RunPortadaStore executes the PortadaStore conformance suite against ps, which
// must back an empty portadas table. It pins that the front-page registry
// round-trips every field (entry order + role, recomendado order), orders by date
// in byte order (SQLite's BINARY default), replaces entries wholesale on upsert
// (never merges), and tombstones and re-activates correctly. Subtests run
// sequentially and share the seeded state; it does not call t.Parallel. It mirrors
// RunAuthorStore / RunTopicStore.
func RunPortadaStore(t *testing.T, ps store.PortadaStore) {
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	t.Run("PortadaByDate on a missing date reports not found", func(t *testing.T) {
		got, found, err := ps.PortadaByDate(ctx, "2000-01-01")
		if err != nil {
			t.Fatalf("PortadaByDate: %v", err)
		}
		if found {
			t.Errorf("found = true for missing date, want false")
		}
		if got.Date != "" || len(got.Entries) != 0 {
			t.Errorf("got = %+v for missing date, want zero PortadaDay", got)
		}
	})

	first := store.PortadaDay{
		Date: "2026-06-01",
		Entries: []store.PortadaEntry{
			{Slug: "lead-story", Role: "important"},
			{Slug: "second-story"},
			{Slug: "third-story"},
		},
		Recomendado: []string{"rec-one", "rec-two"},
		CreatedAt:   base,
		UpdatedAt:   base,
	}

	t.Run("UpsertPortada creates then PortadaByDate round-trips every field (entry order + role + recomendado)", func(t *testing.T) {
		stored, err := ps.UpsertPortada(ctx, first)
		if err != nil {
			t.Fatalf("UpsertPortada: %v", err)
		}
		if stored.Deleted {
			t.Errorf("Deleted = true on create, want false")
		}
		got, found, err := ps.PortadaByDate(ctx, first.Date)
		if err != nil {
			t.Fatalf("PortadaByDate: %v", err)
		}
		if !found {
			t.Fatalf("found = false after create, want true")
		}
		assertPortadaEqual(t, got, first)
		// The lead is entries[0] and carries the full-row emphasis role.
		if got.Entries[0].Slug != "lead-story" || got.Entries[0].Role != "important" {
			t.Errorf("lead entry = %+v, want {lead-story important}", got.Entries[0])
		}
		// A normal entry has an empty role.
		if got.Entries[1].Role != "" {
			t.Errorf("Entries[1].Role = %q, want \"\" (normal)", got.Entries[1].Role)
		}
	})

	t.Run("UpsertPortada on an existing date updates in place (created_at preserved, updated_at advances, entries replaced not merged, no dup row)", func(t *testing.T) {
		edit := store.PortadaDay{
			Date: first.Date,
			Entries: []store.PortadaEntry{
				{Slug: "new-lead", Role: "important"},
				{Slug: "new-second"},
			},
			Recomendado: []string{"rec-three"},
			CreatedAt:   base, // ignored on update: the stored created_at is preserved
			UpdatedAt:   base.Add(48 * time.Hour),
		}
		stored, err := ps.UpsertPortada(ctx, edit)
		if err != nil {
			t.Fatalf("UpsertPortada update: %v", err)
		}
		if !stored.CreatedAt.UTC().Equal(base) {
			t.Errorf("CreatedAt = %v, want preserved %v", stored.CreatedAt.UTC(), base)
		}
		if !stored.UpdatedAt.UTC().Equal(base.Add(48 * time.Hour)) {
			t.Errorf("UpdatedAt = %v, want advanced to %v", stored.UpdatedAt.UTC(), base.Add(48*time.Hour))
		}
		// Entries and recomendado are replaced wholesale, not appended/merged.
		if len(stored.Entries) != 2 || stored.Entries[0].Slug != "new-lead" || stored.Entries[1].Slug != "new-second" {
			t.Errorf("entries not replaced: %+v", stored.Entries)
		}
		if !equalOrdered(stored.Recomendado, []string{"rec-three"}) {
			t.Errorf("recomendado not replaced: %v, want [rec-three]", stored.Recomendado)
		}
		// No duplicate row: still exactly one row for the date.
		all, err := ps.ListPortadas(ctx, true)
		if err != nil {
			t.Fatalf("ListPortadas: %v", err)
		}
		n := 0
		for _, p := range all {
			if p.Date == first.Date {
				n++
			}
		}
		if n != 1 {
			t.Errorf("rows for date %q = %d, want 1 (update, not a second insert)", first.Date, n)
		}
	})

	second := store.PortadaDay{
		Date:        "2026-06-02",
		Entries:     []store.PortadaEntry{{Slug: "solo-story"}},
		Recomendado: []string{},
		CreatedAt:   base,
		UpdatedAt:   base,
	}

	t.Run("ListPortadas orders by date ascending (byte order), excludes deleted by default", func(t *testing.T) {
		if _, err := ps.UpsertPortada(ctx, second); err != nil {
			t.Fatalf("UpsertPortada: %v", err)
		}
		got, err := ps.ListPortadas(ctx, false)
		if err != nil {
			t.Fatalf("ListPortadas: %v", err)
		}
		want := []string{"2026-06-01", "2026-06-02"}
		if d := portadaDatesOf(got); !equalOrdered(d, want) {
			t.Errorf("order = %v, want %v", d, want)
		}
	})

	t.Run("DeletePortada tombstones: excluded by default, included with includeDeleted, re-upsert re-activates", func(t *testing.T) {
		if err := ps.DeletePortada(ctx, second.Date); err != nil {
			t.Fatalf("DeletePortada: %v", err)
		}
		def, err := ps.ListPortadas(ctx, false)
		if err != nil {
			t.Fatalf("ListPortadas(false): %v", err)
		}
		if contains(portadaDatesOf(def), second.Date) {
			t.Errorf("deleted portada %q still listed by default", second.Date)
		}
		all, err := ps.ListPortadas(ctx, true)
		if err != nil {
			t.Fatalf("ListPortadas(true): %v", err)
		}
		if !contains(portadaDatesOf(all), second.Date) {
			t.Errorf("deleted portada %q missing from includeDeleted list", second.Date)
		}
		got, found, err := ps.PortadaByDate(ctx, second.Date)
		if err != nil {
			t.Fatalf("PortadaByDate(deleted): %v", err)
		}
		if !found || !got.Deleted {
			t.Errorf("PortadaByDate(deleted) found=%v Deleted=%v, want true/true", found, got.Deleted)
		}
		reborn := second
		reborn.UpdatedAt = base.Add(72 * time.Hour)
		stored, err := ps.UpsertPortada(ctx, reborn)
		if err != nil {
			t.Fatalf("re-upsert: %v", err)
		}
		if stored.Deleted {
			t.Errorf("Deleted = true after re-upsert, want re-activated")
		}
		def2, err := ps.ListPortadas(ctx, false)
		if err != nil {
			t.Fatalf("ListPortadas(false) after re-upsert: %v", err)
		}
		if !contains(portadaDatesOf(def2), second.Date) {
			t.Errorf("re-activated portada %q missing from default list", second.Date)
		}
	})

	t.Run("DeletePortada on a missing date returns ErrNotFound", func(t *testing.T) {
		if err := ps.DeletePortada(ctx, "1999-12-31"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeletePortada(missing) = %v, want ErrNotFound", err)
		}
	})
}

// RunArticleMutations executes the article soft-delete + edit conformance suite
// against repo, which must be empty. It pins that DeleteArticle/RestoreArticle/
// UpdateArticle and the critical replay-after-delete invariant behave correctly.
// Subtests run sequentially and share the seeded state; it does not call
// t.Parallel.
func RunArticleMutations(t *testing.T, repo store.Repository) {
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	seed := []domain.Article{
		mustArticle(t, domain.PublishInput{Title: "Alpha story", Body: "a", Author: "ada", Section: "tech", Topics: []string{"go"}}, base),
		mustArticle(t, domain.PublishInput{Title: "Bravo story", Body: "b", Author: "bo", Section: "politics", Topics: []string{"election"}}, base.Add(24*time.Hour)),
	}
	for _, a := range seed {
		if _, err := repo.Upsert(ctx, a); err != nil {
			t.Fatalf("seed upsert %q: %v", a.Slug, err)
		}
	}

	t.Run("DeleteArticle tombstones: BySlug still finds it, default Find/Count exclude it, IncludeDeleted includes it", func(t *testing.T) {
		if err := repo.DeleteArticle(ctx, seed[0].Slug); err != nil {
			t.Fatalf("DeleteArticle: %v", err)
		}
		got, err := repo.BySlug(ctx, seed[0].Slug)
		if err != nil {
			t.Fatalf("BySlug(deleted): %v", err)
		}
		if !got.Deleted {
			t.Errorf("BySlug(deleted).Deleted = false, want true (admin must still see it)")
		}
		def, err := repo.Find(ctx, store.Filter{})
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if contains(slugsOf(def), seed[0].Slug) {
			t.Errorf("deleted article %q still in default Find", seed[0].Slug)
		}
		if n, err := repo.Count(ctx, store.Filter{}); err != nil || n != 1 {
			t.Errorf("Count(default) = %d (err %v), want 1 (deleted excluded)", n, err)
		}
		all, err := repo.Find(ctx, store.Filter{IncludeDeleted: true})
		if err != nil {
			t.Fatalf("Find(IncludeDeleted): %v", err)
		}
		if !contains(slugsOf(all), seed[0].Slug) {
			t.Errorf("deleted article %q missing from IncludeDeleted Find", seed[0].Slug)
		}
		if n, _ := repo.Count(ctx, store.Filter{IncludeDeleted: true}); n != 2 {
			t.Errorf("Count(IncludeDeleted) = %d, want 2", n)
		}
		flagged := false
		for _, a := range all {
			if a.Slug == seed[0].Slug {
				flagged = a.Deleted
			}
		}
		if !flagged {
			t.Errorf("IncludeDeleted Find did not flag %q Deleted=true", seed[0].Slug)
		}
	})

	t.Run("RestoreArticle re-includes it", func(t *testing.T) {
		if err := repo.RestoreArticle(ctx, seed[0].Slug); err != nil {
			t.Fatalf("RestoreArticle: %v", err)
		}
		def, err := repo.Find(ctx, store.Filter{})
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if !contains(slugsOf(def), seed[0].Slug) {
			t.Errorf("restored article %q missing from default Find", seed[0].Slug)
		}
		got, _ := repo.BySlug(ctx, seed[0].Slug)
		if got.Deleted {
			t.Errorf("restored article Deleted=true, want false")
		}
	})

	t.Run("DeleteArticle and RestoreArticle on a missing slug return ErrNotFound", func(t *testing.T) {
		if err := repo.DeleteArticle(ctx, "no-such"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeleteArticle(missing) = %v, want ErrNotFound", err)
		}
		if err := repo.RestoreArticle(ctx, "no-such"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RestoreArticle(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("Replay after delete returns the prior identity and does NOT un-delete (the critical invariant)", func(t *testing.T) {
		art := mustArticle(t, domain.PublishInput{Title: "Charlie story", Body: "c", Author: "cy", Section: "tech", Topics: []string{"ai"}}, base.Add(48*time.Hour))
		item := store.UpsertItem{Article: art, IdempotencyKey: "k-charlie", Scopes: []string{"articles:write"}}
		res, err := repo.UpsertMany(ctx, []store.UpsertItem{item})
		if err != nil {
			t.Fatalf("UpsertMany seed: %v", err)
		}
		if !res[0].Created {
			t.Fatalf("seed item Created=false, want true")
		}
		if err := repo.DeleteArticle(ctx, art.Slug); err != nil {
			t.Fatalf("DeleteArticle: %v", err)
		}
		// Replay the SAME idempotency key: the publish lane must return the prior
		// identity (Created=false) and write nothing, so the tombstone stands.
		replay, err := repo.UpsertMany(ctx, []store.UpsertItem{item})
		if err != nil {
			t.Fatalf("UpsertMany replay: %v", err)
		}
		if replay[0].Created {
			t.Errorf("replay Created=true, want false (idempotent)")
		}
		got, err := repo.BySlug(ctx, art.Slug)
		if err != nil {
			t.Fatalf("BySlug after replay: %v", err)
		}
		if !got.Deleted {
			t.Errorf("replay resurrected the tombstoned article (Deleted=false), want it to stay deleted")
		}
		def, _ := repo.Find(ctx, store.Filter{})
		if contains(slugsOf(def), art.Slug) {
			t.Errorf("replayed-after-delete article %q reappeared in default Find", art.Slug)
		}
	})

	t.Run("UpdateArticle edits in place: id+slug+created_at preserved, content+topics replaced", func(t *testing.T) {
		before, err := repo.BySlug(ctx, seed[1].Slug)
		if err != nil {
			t.Fatalf("BySlug: %v", err)
		}
		edited := before
		edited.Title = "Bravo story (edited)"
		edited.Body = "b edited"
		edited.Topics = []string{"election", "results"}
		edited.ContentHash = domain.ContentHash(edited.Title, edited.Body, edited.Author, edited.Section)
		got, err := repo.UpdateArticle(ctx, edited)
		if err != nil {
			t.Fatalf("UpdateArticle: %v", err)
		}
		if got.ID != before.ID || got.Slug != before.Slug {
			t.Errorf("id/slug changed: %s/%s -> %s/%s (want preserved)", before.ID, before.Slug, got.ID, got.Slug)
		}
		if !got.CreatedAt.UTC().Equal(before.CreatedAt.UTC()) {
			t.Errorf("CreatedAt changed: %v -> %v (want preserved)", before.CreatedAt.UTC(), got.CreatedAt.UTC())
		}
		if got.Title != "Bravo story (edited)" || got.ContentHash != edited.ContentHash {
			t.Errorf("edit not applied: %+v", got)
		}
		if !setEqual(got.Topics, []string{"election", "results"}) {
			t.Errorf("topics = %v, want [election results]", got.Topics)
		}
		reread, _ := repo.BySlug(ctx, seed[1].Slug)
		if reread.ContentHash != edited.ContentHash {
			t.Errorf("BySlug content hash = %q, want %q", reread.ContentHash, edited.ContentHash)
		}
	})

	t.Run("UpdateArticle to a content hash held by another article returns EditConflictError and writes nothing", func(t *testing.T) {
		a0, _ := repo.BySlug(ctx, seed[0].Slug)
		a1, _ := repo.BySlug(ctx, seed[1].Slug)
		clash := a1
		clash.Title = a0.Title
		clash.Body = a0.Body
		clash.Author = a0.Author
		clash.Section = a0.Section
		clash.ContentHash = a0.ContentHash // collides with seed[0]
		_, err := repo.UpdateArticle(ctx, clash)
		var ce *store.EditConflictError
		if !errors.As(err, &ce) {
			t.Fatalf("err = %v, want *store.EditConflictError", err)
		}
		if ce.Slug != a1.Slug || ce.ContentHash != a0.ContentHash {
			t.Errorf("conflict = %+v, want slug=%s hash=%s", ce, a1.Slug, a0.ContentHash)
		}
		after, _ := repo.BySlug(ctx, seed[1].Slug)
		if after.ContentHash == a0.ContentHash {
			t.Errorf("conflicting edit was applied; want rolled back (content hash unchanged)")
		}
	})

	t.Run("UpdateArticle on a missing slug returns ErrNotFound", func(t *testing.T) {
		ghost := seed[0]
		ghost.Slug = "no-such-slug"
		if _, err := repo.UpdateArticle(ctx, ghost); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("UpdateArticle(missing) = %v, want ErrNotFound", err)
		}
	})
}

func sourceSlugsOf(ss []store.Source) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Slug
	}
	return out
}

// assertSourceEqual checks every observable field round-trips, including the
// feed_urls order, the lean/feed_type/enabled operational fields, and the metadata.
func assertSourceEqual(t *testing.T, got, want store.Source) {
	t.Helper()
	if got.Slug != want.Slug {
		t.Errorf("Slug = %q, want %q", got.Slug, want.Slug)
	}
	if got.Domain != want.Domain {
		t.Errorf("Domain = %q, want %q", got.Domain, want.Domain)
	}
	if got.Homepage != want.Homepage {
		t.Errorf("Homepage = %q, want %q", got.Homepage, want.Homepage)
	}
	if got.Description != want.Description {
		t.Errorf("Description = %q, want %q", got.Description, want.Description)
	}
	if !equalOrdered(got.FeedURLs, want.FeedURLs) {
		t.Errorf("FeedURLs = %v, want %v (order preserved)", got.FeedURLs, want.FeedURLs)
	}
	if got.FeedType != want.FeedType {
		t.Errorf("FeedType = %q, want %q", got.FeedType, want.FeedType)
	}
	if got.Language != want.Language {
		t.Errorf("Language = %q, want %q", got.Language, want.Language)
	}
	if got.OwnershipGroup != want.OwnershipGroup {
		t.Errorf("OwnershipGroup = %q, want %q", got.OwnershipGroup, want.OwnershipGroup)
	}
	if got.Lean != want.Lean {
		t.Errorf("Lean = %q, want %q", got.Lean, want.Lean)
	}
	if got.Enabled != want.Enabled {
		t.Errorf("Enabled = %v, want %v", got.Enabled, want.Enabled)
	}
	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
	if got.LastChecked != want.LastChecked {
		t.Errorf("LastChecked = %q, want %q", got.LastChecked, want.LastChecked)
	}
	if got.LastOK != want.LastOK {
		t.Errorf("LastOK = %q, want %q", got.LastOK, want.LastOK)
	}
	if len(got.Metadata) != len(want.Metadata) {
		t.Errorf("Metadata = %v, want %v", got.Metadata, want.Metadata)
	}
	for k, v := range want.Metadata {
		if got.Metadata[k] != v {
			t.Errorf("Metadata[%q] = %v, want %v", k, got.Metadata[k], v)
		}
	}
	if !got.CreatedAt.UTC().Equal(want.CreatedAt.UTC().Truncate(time.Second)) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt.UTC(), want.CreatedAt.UTC().Truncate(time.Second))
	}
}

// RunSourceStore executes the SourceStore conformance suite against ss, which must
// back an empty sources table (and, for the detach case, an empty authors table:
// ss must also implement store.AuthorStore, which the store does). It pins that
// the registry round-trips every field, orders by slug in byte order (SQLite's
// BINARY default), tombstones and re-activates, and detaches from every author on
// delete. Subtests run sequentially and share the seeded state; it does not call
// t.Parallel. It mirrors RunTopicStore.
func RunSourceStore(t *testing.T, ss store.SourceStore) {
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	t.Run("SourceBySlug on a missing slug reports not found", func(t *testing.T) {
		got, found, err := ss.SourceBySlug(ctx, "ghost")
		if err != nil {
			t.Fatalf("SourceBySlug: %v", err)
		}
		if found {
			t.Errorf("found = true for missing slug, want false")
		}
		if got.Slug != "" || got.ID != "" {
			t.Errorf("got = %+v for missing slug, want zero Source", got)
		}
	})

	clarin := SampleSource("clarin-example")
	clarin.CreatedAt = base
	clarin.UpdatedAt = base

	t.Run("UpsertSource creates then SourceBySlug round-trips every field", func(t *testing.T) {
		stored, err := ss.UpsertSource(ctx, clarin)
		if err != nil {
			t.Fatalf("UpsertSource: %v", err)
		}
		if stored.ID == "" {
			t.Errorf("empty ID, want store-assigned")
		}
		if stored.Deleted {
			t.Errorf("Deleted = true on create, want false")
		}
		got, found, err := ss.SourceBySlug(ctx, clarin.Slug)
		if err != nil {
			t.Fatalf("SourceBySlug: %v", err)
		}
		if !found {
			t.Fatalf("found = false after create, want true")
		}
		assertSourceEqual(t, got, clarin)
	})

	t.Run("UpsertSource on an existing slug updates in place (created_at preserved, updated_at advances)", func(t *testing.T) {
		before, _, _ := ss.SourceBySlug(ctx, clarin.Slug)
		edit := clarin
		edit.Description = "Descripcion editada."
		edit.Lean = "left"
		edit.Enabled = false
		edit.FeedURLs = []string{"https://clarin-example.example/nuevo-rss"}
		edit.UpdatedAt = base.Add(48 * time.Hour)
		stored, err := ss.UpsertSource(ctx, edit)
		if err != nil {
			t.Fatalf("UpsertSource update: %v", err)
		}
		if stored.ID != before.ID {
			t.Errorf("ID changed on update: %s -> %s (want same row)", before.ID, stored.ID)
		}
		if stored.Description != "Descripcion editada." || stored.Lean != "left" || stored.Enabled {
			t.Errorf("mutable fields not updated: %+v", stored)
		}
		if !equalOrdered(stored.FeedURLs, []string{"https://clarin-example.example/nuevo-rss"}) {
			t.Errorf("feed_urls not replaced: %v", stored.FeedURLs)
		}
		if !stored.CreatedAt.UTC().Equal(base) {
			t.Errorf("CreatedAt = %v, want preserved %v", stored.CreatedAt.UTC(), base)
		}
		if !stored.UpdatedAt.UTC().Equal(base.Add(48 * time.Hour)) {
			t.Errorf("UpdatedAt = %v, want advanced to %v", stored.UpdatedAt.UTC(), base.Add(48*time.Hour))
		}
		all, err := ss.ListSources(ctx, true)
		if err != nil {
			t.Fatalf("ListSources: %v", err)
		}
		n := 0
		for _, s := range all {
			if s.Slug == clarin.Slug {
				n++
			}
		}
		if n != 1 {
			t.Errorf("rows for slug %q = %d, want 1 (update, not a second insert)", clarin.Slug, n)
		}
	})

	ambito := SampleSource("ambito-example")
	ambito.CreatedAt = base
	ambito.UpdatedAt = base

	t.Run("ListSources orders by slug ascending (byte order)", func(t *testing.T) {
		if _, err := ss.UpsertSource(ctx, ambito); err != nil {
			t.Fatalf("UpsertSource: %v", err)
		}
		got, err := ss.ListSources(ctx, false)
		if err != nil {
			t.Fatalf("ListSources: %v", err)
		}
		want := []string{"ambito-example", "clarin-example"}
		if s := sourceSlugsOf(got); !equalOrdered(s, want) {
			t.Errorf("order = %v, want %v", s, want)
		}
	})

	t.Run("DeleteSource tombstones: excluded by default, included with includeDeleted, re-upsert re-activates", func(t *testing.T) {
		if err := ss.DeleteSource(ctx, ambito.Slug); err != nil {
			t.Fatalf("DeleteSource: %v", err)
		}
		def, err := ss.ListSources(ctx, false)
		if err != nil {
			t.Fatalf("ListSources(false): %v", err)
		}
		if contains(sourceSlugsOf(def), ambito.Slug) {
			t.Errorf("deleted source %q still listed by default", ambito.Slug)
		}
		all, err := ss.ListSources(ctx, true)
		if err != nil {
			t.Fatalf("ListSources(true): %v", err)
		}
		if !contains(sourceSlugsOf(all), ambito.Slug) {
			t.Errorf("deleted source %q missing from includeDeleted list", ambito.Slug)
		}
		got, found, err := ss.SourceBySlug(ctx, ambito.Slug)
		if err != nil {
			t.Fatalf("SourceBySlug(deleted): %v", err)
		}
		if !found || !got.Deleted {
			t.Errorf("SourceBySlug(deleted) found=%v Deleted=%v, want true/true", found, got.Deleted)
		}
		reborn := ambito
		reborn.UpdatedAt = base.Add(72 * time.Hour)
		stored, err := ss.UpsertSource(ctx, reborn)
		if err != nil {
			t.Fatalf("re-upsert: %v", err)
		}
		if stored.Deleted {
			t.Errorf("Deleted = true after re-upsert, want re-activated")
		}
		def2, err := ss.ListSources(ctx, false)
		if err != nil {
			t.Fatalf("ListSources(false) after re-upsert: %v", err)
		}
		if !contains(sourceSlugsOf(def2), ambito.Slug) {
			t.Errorf("re-activated source %q missing from default list", ambito.Slug)
		}
	})

	t.Run("DeleteSource on a missing slug returns ErrNotFound", func(t *testing.T) {
		if err := ss.DeleteSource(ctx, "no-such-source"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeleteSource(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("DeleteSource detaches the source from every author (cascade to the join)", func(t *testing.T) {
		as, ok := ss.(store.AuthorStore)
		if !ok {
			t.Skipf("store %T does not implement store.AuthorStore; detach-on-delete is untested here", ss)
		}
		// Two authors both read clarin-example; one also reads a source that survives.
		for _, h := range []string{"reader-one", "reader-two"} {
			a := SampleAuthor(h)
			a.CreatedAt, a.UpdatedAt = base, base
			if _, err := as.UpsertAuthor(ctx, a); err != nil {
				t.Fatalf("UpsertAuthor %q: %v", h, err)
			}
		}
		if err := as.SetAuthorSources(ctx, "reader-one", []string{"clarin-example", "keeps-this"}); err != nil {
			t.Fatalf("SetAuthorSources reader-one: %v", err)
		}
		if err := as.SetAuthorSources(ctx, "reader-two", []string{"clarin-example"}); err != nil {
			t.Fatalf("SetAuthorSources reader-two: %v", err)
		}
		if err := ss.DeleteSource(ctx, "clarin-example"); err != nil {
			t.Fatalf("DeleteSource: %v", err)
		}
		one, err := as.AuthorSources(ctx, "reader-one")
		if err != nil {
			t.Fatalf("AuthorSources reader-one: %v", err)
		}
		if !equalOrdered(one, []string{"keeps-this"}) {
			t.Errorf("reader-one sources = %v, want [keeps-this] (clarin detached, other kept)", one)
		}
		two, err := as.AuthorSources(ctx, "reader-two")
		if err != nil {
			t.Fatalf("AuthorSources reader-two: %v", err)
		}
		if len(two) != 0 {
			t.Errorf("reader-two sources = %v, want empty (its only source was detached)", two)
		}
	})
}

// brainMigrationStore is the surface the faithful-migration proof needs: the author
// and source registries together, so it can rebuild a persona (author + profile
// fields + the who_i_am/beat/language/few-shots tail in Metadata) and its portals
// (sources) and the persona.sources links (the join) and assert nothing is lost.
type brainMigrationStore interface {
	store.AuthorStore
	store.SourceStore
}

// RunBrainDataMigration proves the backend can hold the brain's persona + portal data
// FAITHFULLY: every field the brain's personas.db and portals table carry survives a
// round-trip through the backend registries. It is the concrete "brain data migrated
// faithfully" checkpoint for the content-model migration. The promoted profile columns
// (gender/about/style/topics) carry persona gender/about/style/profile_topics; the
// generation-recipe fields the brain also holds (who_i_am, beat, language, the
// few-shots) ride in Metadata (the open-ended tail); each portal becomes a source;
// and persona.sources becomes author_sources links. It proves the migration target
// holds every field the brain carries.
func RunBrainDataMigration(t *testing.T, repo brainMigrationStore) {
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// Two portals with the full field set, mapping the brain's Portal 1:1.
	portals := []store.Source{
		{
			Slug: "diario-uno", Domain: "diario-uno.example", Homepage: "https://diario-uno.example",
			Description: "Diario local independiente.", FeedURLs: []string{"https://diario-uno.example/rss"},
			FeedType: "native_rss", Language: "es", OwnershipGroup: "grupo-uno", Lean: "left",
			Enabled: true, Status: "ok", LastChecked: "2026-05-30T00:00:00Z", LastOK: "2026-05-30T00:00:00Z",
			CreatedAt: base, UpdatedAt: base,
		},
		{
			Slug: "gaceta-dos", Domain: "gaceta-dos.example", Homepage: "https://gaceta-dos.example",
			Description: "Gaceta de oposicion.", FeedURLs: []string{},
			FeedType: "site_search", Language: "es", OwnershipGroup: "", Lean: "right",
			Enabled: false, Status: "unreachable", LastChecked: "2026-05-31T00:00:00Z", LastOK: "",
			CreatedAt: base, UpdatedAt: base,
		},
	}
	for _, p := range portals {
		stored, err := repo.UpsertSource(ctx, p)
		if err != nil {
			t.Fatalf("migrate portal %q: %v", p.Slug, err)
		}
		assertSourceEqual(t, stored, p)
	}

	// One persona with the full field set. id -> handle, display_name -> name,
	// profile_topics -> Topics, and the generation-recipe fields ride in Metadata.
	persona := store.Author{
		Handle: "lara", Name: "Lara Ibanez", Bio: "Cronista politica.",
		Avatar: "/media/lara.png", Gender: "femenino",
		About:  "Lara cubre la politica nacional desde hace una decada.",
		Style:  "Primera persona sobria; nunca adjetivos de relleno; cita siempre la fuente.",
		Topics: []string{"politica", "elecciones", "congreso"},
		Metadata: map[string]any{
			"who_i_am":      "Soy Lara, periodista politica.",
			"beat":          "politics",
			"language":      "espanol neutro",
			"few_shots_pos": "ejemplo bueno",
			"few_shots_neg": "ejemplo malo",
		},
		CreatedAt: base, UpdatedAt: base,
	}
	stored, err := repo.UpsertAuthor(ctx, persona)
	if err != nil {
		t.Fatalf("migrate persona: %v", err)
	}
	assertAuthorEqual(t, stored, persona)

	// persona.sources -> author_sources links.
	if err := repo.SetAuthorSources(ctx, persona.Handle, []string{"diario-uno", "gaceta-dos"}); err != nil {
		t.Fatalf("migrate persona.sources: %v", err)
	}

	// Read the persona back cold and assert nothing was lost: the profile columns, the
	// metadata tail (every generation-recipe key), and the hydrated source links.
	got, found, err := repo.AuthorByHandle(ctx, persona.Handle)
	if err != nil || !found {
		t.Fatalf("AuthorByHandle after migration: found=%v err=%v", found, err)
	}
	assertAuthorEqual(t, got, persona)
	if !equalOrdered(got.Sources, []string{"diario-uno", "gaceta-dos"}) {
		t.Errorf("hydrated Sources = %v, want [diario-uno gaceta-dos]", got.Sources)
	}
	for k, v := range persona.Metadata {
		if got.Metadata[k] != v {
			t.Errorf("generation-recipe Metadata[%q] = %v, want %v (tail must survive)", k, got.Metadata[k], v)
		}
	}
}
