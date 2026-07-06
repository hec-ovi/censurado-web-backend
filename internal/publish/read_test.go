package publish_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
	"github.com/hec-ovi/censurado-web-backend/store/storetest"
)

// newReadServer wires the full server WITH the JSON read API over a real sqlite
// store, returning the handler and the concrete store so a test can seed authors,
// topics, and articles (and soft-delete them) directly. The same three keys as the
// write tests: ak_ada (write), ak_ro (read-only scope), ak_op (publish-any).
func newReadServer(t *testing.T) (http.Handler, *sqlite.Store) {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "read.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	auth := publish.NewStaticKeyAuth()
	auth.Add("ak_ada", publish.HashSecret(adaSecret), "ada", publish.ScopeWrite)
	auth.Add("ak_ro", publish.HashSecret(roSecret), "ro", "articles:read")
	auth.Add("ak_op", publish.HashSecret(opSecret), "editor", publish.ScopeWrite, publish.ScopePublishAny)

	now := func() time.Time { return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC) }
	h := publish.NewHandler(repo, repo, auth, now)
	rh := publish.NewReadHandler(repo, auth)
	limiter := publish.NewRateLimiter(1000, 1000, now)
	return publish.NewServerHandler(h, limiter, nil, rh, nil), repo
}

func getAuth(t *testing.T, h http.Handler, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// seedArticle publishes one article through the real write path (as the operator
// key, so it can author as anyone) and returns its slug.
func seedArticle(t *testing.T, srv http.Handler, opToken, key, title, author, section string, topics []string) string {
	t.Helper()
	payload := map[string]any{
		"title": title, "body": "# H\n\nbody for " + title,
		"author": author, "section": section, "topics": topics,
	}
	b, _ := json.Marshal(payload)
	rec := post(t, srv, opToken, key, string(b))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed %q: status = %d (%s)", title, rec.Code, rec.Body.String())
	}
	_, slug := decodeCreate(t, rec)
	return slug
}

func seedStoredArticle(t *testing.T, repo store.Repository, title, author, section string, topics []string, at time.Time) string {
	return seedStoredArticleWithMetadata(t, repo, title, author, section, topics, nil, at)
}

func seedStoredArticleWithMetadata(t *testing.T, repo store.Repository, title, author, section string, topics []string, metadata map[string]any, at time.Time) string {
	t.Helper()
	article, err := domain.NewArticle(domain.PublishInput{
		Title: title, Body: "# H\n\nbody for " + title,
		Author: author, Section: section, Topics: topics,
		Metadata:    metadata,
		PublishedAt: &at,
	}, at)
	if err != nil {
		t.Fatalf("new article %q: %v", title, err)
	}
	res, err := repo.Upsert(context.Background(), article)
	if err != nil {
		t.Fatalf("upsert %q: %v", title, err)
	}
	return res.Article.Slug
}

type authorsResp struct {
	Authors []struct {
		Handle   string         `json:"handle"`
		Name     string         `json:"name"`
		Bio      string         `json:"bio"`
		Metadata map[string]any `json:"metadata"`
		Deleted  bool           `json:"deleted"`
	} `json:"authors"`
}

type topicsResp struct {
	Topics []struct {
		Slug    string `json:"slug"`
		Label   string `json:"label"`
		Deleted bool   `json:"deleted"`
	} `json:"topics"`
}

type articlesResp struct {
	Articles []struct {
		Slug     string   `json:"slug"`
		Title    string   `json:"title"`
		Section  string   `json:"section"`
		Author   string   `json:"author"`
		Topics   []string `json:"topics"`
		HasMedia bool     `json:"has_media"`
		Deleted  bool     `json:"deleted"`
	} `json:"articles"`
	Total int `json:"total"`
}

type articleDaysResp struct {
	Days []struct {
		Date  string `json:"date"`
		Count int    `json:"count"`
	} `json:"days"`
}

func TestReadAPI_AuthRequired(t *testing.T) {
	srv, _ := newReadServer(t)

	t.Run("no token -> 401", func(t *testing.T) {
		if rec := getAuth(t, srv, "", "/authors"); rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
	t.Run("bad token -> 401", func(t *testing.T) {
		if rec := getAuth(t, srv, "ak_ada.wrong", "/topics"); rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
	t.Run("read-only key may read (no scope required)", func(t *testing.T) {
		if rec := getAuth(t, srv, "ak_ro."+roSecret, "/articles"); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
	})
}

func TestReadAuthors(t *testing.T) {
	srv, repo := newReadServer(t)
	ctx := context.Background()
	roToken := "ak_ro." + roSecret

	if _, err := repo.UpsertAuthor(ctx, storetest.SampleAuthor("author-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertAuthor(ctx, storetest.SampleAuthor("author-b")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertAuthor(ctx, storetest.SampleAuthor("gone")); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAuthor(ctx, "gone"); err != nil {
		t.Fatal(err)
	}

	t.Run("default excludes the tombstoned author", func(t *testing.T) {
		rec := getAuth(t, srv, roToken, "/authors")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
		}
		var got authorsResp
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		handles := authorHandles(got)
		if !handles["author-a"] || !handles["author-b"] {
			t.Errorf("missing seeded authors: %v", handles)
		}
		if handles["gone"] {
			t.Error("tombstoned author leaked into the default listing")
		}
		for _, a := range got.Authors {
			if a.Handle == "author-a" && a.Metadata["beat"] != "general" {
				t.Errorf("metadata did not round-trip: %+v", a.Metadata)
			}
		}
	})

	t.Run("include_deleted surfaces the tombstoned author", func(t *testing.T) {
		rec := getAuth(t, srv, roToken, "/authors?include_deleted=true")
		var got authorsResp
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var gone *struct {
			Handle   string         `json:"handle"`
			Name     string         `json:"name"`
			Bio      string         `json:"bio"`
			Metadata map[string]any `json:"metadata"`
			Deleted  bool           `json:"deleted"`
		}
		for i := range got.Authors {
			if got.Authors[i].Handle == "gone" {
				gone = &got.Authors[i]
			}
		}
		if gone == nil {
			t.Fatal("tombstoned author missing with include_deleted=true")
		}
		if !gone.Deleted {
			t.Error("tombstoned author should carry deleted=true")
		}
	})
}

func authorHandles(r authorsResp) map[string]bool {
	out := map[string]bool{}
	for _, a := range r.Authors {
		out[a.Handle] = true
	}
	return out
}

func TestReadTopics(t *testing.T) {
	srv, repo := newReadServer(t)
	ctx := context.Background()
	roToken := "ak_ro." + roSecret

	if _, err := repo.UpsertTopic(ctx, store.Topic{Slug: "go", Label: "Go", Description: "the language"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertTopic(ctx, store.Topic{Slug: "ai", Label: "AI"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertTopic(ctx, store.Topic{Slug: "old", Label: "Old"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteTopic(ctx, "old"); err != nil {
		t.Fatal(err)
	}

	rec := getAuth(t, srv, roToken, "/topics")
	var got topicsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, tp := range got.Topics {
		slugs[tp.Slug] = true
	}
	if !slugs["go"] || !slugs["ai"] {
		t.Errorf("missing seeded topics: %v", slugs)
	}
	if slugs["old"] {
		t.Error("tombstoned topic leaked into the default listing")
	}

	rec = getAuth(t, srv, roToken, "/topics?include_deleted=true")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tp := range got.Topics {
		if tp.Slug == "old" && tp.Deleted {
			found = true
		}
	}
	if !found {
		t.Error("tombstoned topic missing or not flagged deleted with include_deleted=true")
	}
}

// TestReadArticles_HasMedia proves the server-derived has_media flag: an article
// with a metadata image, one whose body carries a {{video:...}} marker, and a
// plain-text piece (whose only markers are {{relacionado}}/{{tweet}}, which are NOT
// image/video media) are seeded through the real publish path, then the flag is
// asserted on BOTH the light list (/articles) and the single article (/articles/{slug}).
func TestReadArticles_HasMedia(t *testing.T) {
	srv, _ := newReadServer(t)
	opToken := "ak_op." + opSecret

	seed := func(idem, title, body string, meta map[string]any) string {
		t.Helper()
		payload := map[string]any{
			"title": title, "body": body, "author": "ada", "section": "tech",
		}
		if meta != nil {
			payload["metadata"] = meta
		}
		b, _ := json.Marshal(payload)
		rec := post(t, srv, opToken, idem, string(b))
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed %q: status = %d (%s)", title, rec.Code, rec.Body.String())
		}
		_, slug := decodeCreate(t, rec)
		return slug
	}

	imgSlug := seed("m1", "Hero image piece", "# H\n\nplain body", map[string]any{"image": "/media/abc.jpg"})
	vidSlug := seed("m2", "Body video piece", "# H\n\nlead\n\n{{video:wD0ay7ttFTg}}\n\nmore", nil)
	txtSlug := seed("m3", "Plain text piece", "# H\n\nwords {{relacionado:other-slug}} and {{tweet:12345}}", nil)

	want := map[string]bool{imgSlug: true, vidSlug: true, txtSlug: false}

	t.Run("light list carries has_media", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles"), &got)
		seen := map[string]bool{}
		for _, a := range got.Articles {
			seen[a.Slug] = a.HasMedia
		}
		for slug, exp := range want {
			if seen[slug] != exp {
				t.Errorf("list has_media[%s] = %v, want %v", slug, seen[slug], exp)
			}
		}
	})

	t.Run("single article carries has_media", func(t *testing.T) {
		for slug, exp := range want {
			var got struct {
				HasMedia bool `json:"has_media"`
			}
			decodeBody(t, getAuth(t, srv, opToken, "/articles/"+slug), &got)
			if got.HasMedia != exp {
				t.Errorf("article %s has_media = %v, want %v", slug, got.HasMedia, exp)
			}
		}
	})

	// card_type is the label (what the front-page card shows), derived here for
	// these legacy pieces (no authored card block): the image piece -> "image", the
	// body-video piece -> "video", the plain piece -> "text".
	t.Run("light list carries card_type label", func(t *testing.T) {
		var got struct {
			Articles []struct {
				Slug     string `json:"slug"`
				CardType string `json:"card_type"`
			} `json:"articles"`
		}
		decodeBody(t, getAuth(t, srv, opToken, "/articles"), &got)
		seen := map[string]string{}
		for _, a := range got.Articles {
			seen[a.Slug] = a.CardType
		}
		wantCard := map[string]string{imgSlug: "image", vidSlug: "video", txtSlug: "text"}
		for slug, exp := range wantCard {
			if seen[slug] != exp {
				t.Errorf("list card_type[%s] = %q, want %q", slug, seen[slug], exp)
			}
		}
	})
}

func TestReadArticles_FilterPagingAndBodyOmitted(t *testing.T) {
	srv, _ := newReadServer(t)
	opToken := "ak_op." + opSecret

	seedArticle(t, srv, opToken, "a1", "Chip ships", "ada", "tech", []string{"go", "hardware"})
	seedArticle(t, srv, opToken, "a2", "Summit opens", "bo", "world", []string{"diplomacy"})
	seedArticle(t, srv, opToken, "a3", "Markets dip", "ada", "economics", []string{"go"})

	t.Run("all -> total and body omitted", func(t *testing.T) {
		rec := getAuth(t, srv, opToken, "/articles")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "body for ") {
			t.Error("list response leaked the article body")
		}
		var got articlesResp
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Total != 3 || len(got.Articles) != 3 {
			t.Errorf("total=%d len=%d, want 3/3", got.Total, len(got.Articles))
		}
	})

	t.Run("filter by author", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?author=ada"), &got)
		if got.Total != 2 {
			t.Errorf("total = %d, want 2", got.Total)
		}
		for _, a := range got.Articles {
			if a.Author != "ada" {
				t.Errorf("author = %q, want ada", a.Author)
			}
		}
	})

	t.Run("filter by section", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?section=world"), &got)
		if got.Total != 1 || (len(got.Articles) == 1 && got.Articles[0].Section != "world") {
			t.Errorf("section filter wrong: %+v", got)
		}
	})

	t.Run("filter by topic", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?topic=go"), &got)
		if got.Total != 2 {
			t.Errorf("topic=go total = %d, want 2", got.Total)
		}
	})

	t.Run("paging keeps total but pages items", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?limit=1"), &got)
		if got.Total != 3 {
			t.Errorf("total = %d, want 3 (paging must not shrink total)", got.Total)
		}
		if len(got.Articles) != 1 {
			t.Errorf("len = %d, want 1 (limit=1)", len(got.Articles))
		}
	})
}

func TestReadArticles_DayIndexAndDatePage(t *testing.T) {
	srv, repo := newReadServer(t)
	opToken := "ak_op." + opSecret
	base := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)

	oldSlug := seedStoredArticle(t, repo, "Older day", "ada", "tech", []string{"go"}, base)
	latestAda := seedStoredArticle(t, repo, "Latest Ada", "ada", "tech", []string{"go"}, base.AddDate(0, 0, 2))
	latestBo := seedStoredArticle(t, repo, "Latest Bo", "bo", "world", []string{"diplomacy"}, base.AddDate(0, 0, 2).Add(2*time.Hour))

	t.Run("day index defaults newest first", func(t *testing.T) {
		var got articleDaysResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles:days"), &got)
		if len(got.Days) != 2 {
			t.Fatalf("days = %+v, want 2 days", got.Days)
		}
		if got.Days[0].Date != "2026-06-24" || got.Days[0].Count != 2 || got.Days[1].Date != "2026-06-22" || got.Days[1].Count != 1 {
			t.Errorf("days = %+v, want latest day count 2 then older day count 1", got.Days)
		}
	})

	t.Run("day index can be oldest first and filtered by author", func(t *testing.T) {
		var got articleDaysResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles:days?order=oldest&author=ada"), &got)
		if len(got.Days) != 2 {
			t.Fatalf("days = %+v, want 2 ada days", got.Days)
		}
		if got.Days[0].Date != "2026-06-22" || got.Days[0].Count != 1 || got.Days[1].Date != "2026-06-24" || got.Days[1].Count != 1 {
			t.Errorf("author-filtered days = %+v, want oldest ada day then latest ada day", got.Days)
		}
	})

	t.Run("date param loads only that UTC day", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?date=2026-06-24&order=oldest"), &got)
		if got.Total != 2 || len(got.Articles) != 2 {
			t.Fatalf("date page total=%d len=%d, want 2/2", got.Total, len(got.Articles))
		}
		slugs := []string{got.Articles[0].Slug, got.Articles[1].Slug}
		if slugs[0] != latestAda || slugs[1] != latestBo {
			t.Errorf("date page slugs = %v, want [%s %s]", slugs, latestAda, latestBo)
		}

		decodeBody(t, getAuth(t, srv, opToken, "/articles?date=2026-06-22&order=oldest"), &got)
		if got.Total != 1 || len(got.Articles) != 1 || got.Articles[0].Slug != oldSlug {
			t.Errorf("older date page = %+v, want only %s", got, oldSlug)
		}
	})
}

func TestReadFacets_DistinctLiveValuesWithCounts(t *testing.T) {
	srv, repo := newReadServer(t)
	opToken := "ak_op." + opSecret
	base := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)

	seedStoredArticle(t, repo, "P1", "ada", "politics", []string{"milei", "go"}, base)
	seedStoredArticle(t, repo, "P2", "ada", "politics", []string{"milei"}, base.Add(time.Hour))
	seedStoredArticle(t, repo, "T1", "bo", "tech", []string{"go"}, base.Add(2*time.Hour))
	// An old article on a section that is being retired: soft-deleting it must drop
	// `economics` out of the live facet list entirely (the point of the endpoint).
	gone := seedStoredArticle(t, repo, "Old economics piece", "bo", "economics", []string{"go"}, base.Add(3*time.Hour))
	if err := repo.DeleteArticle(context.Background(), gone); err != nil {
		t.Fatalf("soft-delete economics article: %v", err)
	}

	type facet struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	}
	var got struct {
		Sections []facet `json:"sections"`
		Authors  []facet `json:"authors"`
		Topics   []facet `json:"topics"`
	}
	decodeBody(t, getAuth(t, srv, opToken, "/articles:facets"), &got)

	// sections: politics(2) before tech(1) by count DESC; economics excluded (deleted).
	wantSections := []facet{{"politics", 2}, {"tech", 1}}
	if len(got.Sections) != len(wantSections) {
		t.Fatalf("sections = %+v, want %+v (economics must be absent)", got.Sections, wantSections)
	}
	for i, w := range wantSections {
		if got.Sections[i] != w {
			t.Errorf("sections[%d] = %+v, want %+v", i, got.Sections[i], w)
		}
	}
	for _, s := range got.Sections {
		if s.Value == "economics" {
			t.Error("economics section still present after its only article was tombstoned")
		}
	}

	// authors and topics come back over the same call (the impact on authors the
	// consult verb surfaces): ada wrote both live politics pieces, bo one live tech.
	authorCount := map[string]int{}
	for _, a := range got.Authors {
		authorCount[a.Value] = a.Count
	}
	if authorCount["ada"] != 2 || authorCount["bo"] != 1 {
		t.Errorf("authors = %+v, want ada:2 bo:1 (deleted piece excluded)", got.Authors)
	}
	topicCount := map[string]int{}
	for _, tp := range got.Topics {
		topicCount[tp.Value] = tp.Count
	}
	if topicCount["milei"] != 2 || topicCount["go"] != 2 {
		t.Errorf("topics = %+v, want milei:2 go:2 (go on the deleted piece excluded)", got.Topics)
	}

	t.Run("requires a valid bearer token", func(t *testing.T) {
		if rec := getAuth(t, srv, "", "/articles:facets"); rec.Code != http.StatusUnauthorized {
			t.Errorf("no-token status = %d, want 401", rec.Code)
		}
	})
}

func TestReadArticles_TitleSubtitleQuery(t *testing.T) {
	srv, repo := newReadServer(t)
	opToken := "ak_op." + opSecret
	base := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)

	titleSlug := seedStoredArticle(t, repo, "Mercury dispatch", "ada", "tech", []string{"go"}, base)
	subtitleSlug := seedStoredArticleWithMetadata(t, repo, "Quiet headline", "bo", "world", []string{"diplomacy"}, map[string]any{"subtitle": "Mercury appears in the deck"}, base.Add(24*time.Hour))
	bodyOnlySlug := seedStoredArticle(t, repo, "No visible match", "cy", "world", []string{"misc"}, base.Add(48*time.Hour))

	t.Run("matches title and subtitle across dates", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?title_subtitle_q=mercury&order=oldest"), &got)
		if got.Total != 2 || len(got.Articles) != 2 {
			t.Fatalf("total=%d len=%d, want 2/2", got.Total, len(got.Articles))
		}
		slugs := []string{got.Articles[0].Slug, got.Articles[1].Slug}
		if slugs[0] != titleSlug || slugs[1] != subtitleSlug {
			t.Errorf("title_subtitle_q slugs = %v, want [%s %s]", slugs, titleSlug, subtitleSlug)
		}
		for _, a := range got.Articles {
			if a.Slug == bodyOnlySlug {
				t.Error("body-only article matched title_subtitle_q")
			}
		}
	})

	t.Run("does not search body-only text", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?title_subtitle_q=body%20for%20No%20visible%20match"), &got)
		if got.Total != 0 || len(got.Articles) != 0 {
			t.Errorf("body-only title_subtitle_q = %+v, want empty", got)
		}
	})
}

func TestReadArticles_SoftDeleteVisibility(t *testing.T) {
	srv, repo := newReadServer(t)
	ctx := context.Background()
	opToken := "ak_op." + opSecret

	keep := seedArticle(t, srv, opToken, "k1", "Keep me", "ada", "tech", []string{"go"})
	gone := seedArticle(t, srv, opToken, "k2", "Delete me", "ada", "tech", []string{"go"})
	if err := repo.DeleteArticle(ctx, gone); err != nil {
		t.Fatal(err)
	}

	t.Run("default list excludes the deleted article", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles"), &got)
		if got.Total != 1 {
			t.Errorf("total = %d, want 1", got.Total)
		}
		for _, a := range got.Articles {
			if a.Slug == gone {
				t.Error("deleted article appeared in the default list")
			}
		}
	})

	t.Run("include_deleted lists the deleted article flagged", func(t *testing.T) {
		var got articlesResp
		decodeBody(t, getAuth(t, srv, opToken, "/articles?include_deleted=true"), &got)
		if got.Total != 2 {
			t.Errorf("total = %d, want 2", got.Total)
		}
		var saw bool
		for _, a := range got.Articles {
			if a.Slug == gone {
				saw = a.Deleted
			}
		}
		if !saw {
			t.Error("deleted article missing or not flagged deleted")
		}
	})

	t.Run("GET by slug returns body and the deleted one with deleted=true", func(t *testing.T) {
		rec := getAuth(t, srv, opToken, "/articles/"+keep)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "body for Keep me") {
			t.Fatalf("keep: status=%d body=%s", rec.Code, rec.Body.String())
		}
		var single struct {
			Slug, Body string
			Deleted    bool
		}
		rec = getAuth(t, srv, opToken, "/articles/"+gone)
		if rec.Code != http.StatusOK {
			t.Fatalf("deleted by slug status = %d", rec.Code)
		}
		decodeBody(t, rec, &single)
		if !single.Deleted || !strings.Contains(single.Body, "body for Delete me") {
			t.Errorf("deleted-by-slug = %+v, want deleted=true with body", single)
		}
	})

	t.Run("missing slug -> 404", func(t *testing.T) {
		if rec := getAuth(t, srv, opToken, "/articles/no-such-slug"); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

func TestReadArticles_InvalidQuery(t *testing.T) {
	srv, _ := newReadServer(t)
	opToken := "ak_op." + opSecret

	for _, path := range []string{"/articles?limit=abc", "/articles?offset=-1", "/articles?from=not-a-time", "/articles?date=not-a-day"} {
		if rec := getAuth(t, srv, opToken, path); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
}
