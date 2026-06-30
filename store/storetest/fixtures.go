package storetest

import "github.com/hec-ovi/censurado-web-backend/store"

// SampleAuthor returns a generic author fixture. The store, the publish API, and
// the admin UI are author-agnostic: authors live in the database, never in code.
// So the author-entity tests assert the mechanism (create, list, order,
// soft-delete, restore, field round-trip) against ONE generic author shape rather
// than any real persona. Pass a distinct handle only where a test needs more than
// one row (for example, listing order); every row is the same shape and differs
// only by handle.
func SampleAuthor(handle string) store.Author {
	return store.Author{
		Handle:   handle,
		Name:     "Sample Author",
		Bio:      "Sample author bio.",
		Avatar:   "/media/avatar.png",
		Metadata: map[string]any{"beat": "general"},
	}
}
