package storetest

import "github.com/hec-ovi/censurado-web-backend/store"

// SampleAuthor returns a generic author fixture. The store, the publish API, and
// the admin UI are author-agnostic: authors live in the database, never in code.
// So the author-entity tests assert the mechanism (create, list, order,
// soft-delete, restore, field round-trip) against ONE generic author shape rather
// than any real persona. It carries every first-class column (including the
// gender/about/style profile fields and the topics list) so the round-trip check
// covers them; Sources is intentionally left unset because UpsertAuthor never writes
// the join (SetAuthorSources does). Pass a distinct handle only where a test needs
// more than one row (for example, listing order); every row is the same shape and
// differs only by handle.
func SampleAuthor(handle string) store.Author {
	return store.Author{
		Handle:   handle,
		Name:     "Sample Author",
		Bio:      "Sample author bio.",
		Avatar:   "/media/avatar.png",
		Gender:   "no binario",
		About:    "Sample about-page text, longer than the bio.",
		Style:    "Voz directa y sobria; frases cortas, sin adjetivos de relleno.",
		Topics:   []string{"politica", "economia"},
		Metadata: map[string]any{"beat": "general"},
	}
}

// SampleSource returns a generic source fixture, the source analogue of
// SampleAuthor: the registry is source-agnostic, so the entity tests assert the
// mechanism against one generic shape and differ only by slug. It carries every
// column (the JSON feed_urls list, the lean/feed_type enums, the enabled toggle, and
// the health fields) so the round-trip check covers them. Domain derives from the
// slug so distinct slugs stay domain-unique.
func SampleSource(slug string) store.Source {
	return store.Source{
		Slug:           slug,
		Domain:         slug + ".example",
		Homepage:       "https://" + slug + ".example",
		Description:    "Sample source description.",
		FeedURLs:       []string{"https://" + slug + ".example/rss", "https://" + slug + ".example/atom"},
		FeedType:       "native_rss",
		Language:       "es",
		OwnershipGroup: "grupo-ejemplo",
		Lean:           "neutral",
		Enabled:        true,
		Status:         "ok",
		LastChecked:    "2026-06-01T00:00:00Z",
		LastOK:         "2026-06-01T00:00:00Z",
		Metadata:       map[string]any{"note": "seed"},
	}
}
