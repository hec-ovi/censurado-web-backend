package store

import (
	"context"
	"time"
)

// TextScope selects which per-language text catalog a text operation targets: the
// reader-facing site strings (frontend_text), the operator-facing admin panel strings
// (panel_text), or the newsroom's per-language editorial config (editorial_text). The
// three are separate tables of identical shape that version independently, so the site
// can carry several languages, the panel stays single language, and the editorial
// anchors (the language-specific lexicon, orthography, slop phrases, attribution and
// disclaimer exemplars, and the Telegram bot directive) are authored per language.
type TextScope string

const (
	// ScopeFrontend is the reader-facing site string catalog (frontend_text).
	ScopeFrontend TextScope = "frontend"
	// ScopePanel is the operator-facing admin panel string catalog (panel_text).
	ScopePanel TextScope = "panel"
	// ScopeEditorial is the newsroom's per-language editorial config (editorial_text):
	// the language-specific writing anchors the workflow prompts apply (lexicon bans and
	// swaps, orthography, slop phrases, attribution and disclaimer exemplars) plus the
	// Telegram bot's response directive. Unlike the two UI catalogs it has no English
	// base row: each row is authored in its own language (Spanish slop words are not the
	// English ones), so a new language is generated, not translated.
	ScopeEditorial TextScope = "editorial"
)

// TextEntry is one UI string: a (Key, Lang) pair and its Value. Key is the stable
// lookup identifier the code references (e.g. "nav.about"); Lang is the language code
// ("en" is the base every key is authored in, "es"/others are translations); Value is
// the rendered string in that language. Deleted reports the tombstone; the timestamps
// are whole-second, matching the other registries.
type TextEntry struct {
	Key       string
	Lang      string
	Value     string
	Metadata  map[string]any
	Deleted   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TextStore is the operator- and translator-owned UI-string catalog behind the two
// scopes. It is a separate concern from the article Repository (like the author,
// topic, portada, and source registries) so the frozen article contract stays
// focused even though one store implements them all. Writes go through the same
// single writer as the publish path.
//
// The generator (Layer 1) reads Text() at build time to render a language from the DB,
// so this method set is frozen by store/contract_interface_test.go like the other store
// interfaces: a deliberate signature change updates that test and contracts/CONTRACT.md
// together. Text() returns a plain key->value map (not a struct), so it adds no new
// frozen overlay shape; ListText/UpsertText/DeleteText back the panel and the one-time
// translate CLI over the HTTP seam.
type TextStore interface {
	// Text returns all live entries for the scope and language as a key->value map, for
	// a single bulk lookup at render time. An unknown scope is an error; an unknown or
	// empty-result language yields an empty (non-nil) map.
	Text(ctx context.Context, scope TextScope, lang string) (map[string]string, error)
	// ListText returns entries for the scope ordered by key ascending then lang (byte
	// order), deterministic. A blank lang returns every language's rows; tombstoned
	// rows are excluded unless includeDeleted.
	ListText(ctx context.Context, scope TextScope, lang string, includeDeleted bool) ([]TextEntry, error)
	// UpsertText creates or replaces one entry keyed on (scope, Key, Lang). On an
	// existing pair it replaces Value/Metadata, advances UpdatedAt, preserves CreatedAt,
	// and clears any tombstone. On create it sets CreatedAt/UpdatedAt from the supplied
	// values (defaulting to now when zero). It returns the stored row.
	UpsertText(ctx context.Context, scope TextScope, e TextEntry) (TextEntry, error)
	// DeleteText soft-deletes the (scope, key, lang) entry by setting a tombstone.
	// Deleting an absent entry returns ErrNotFound; it is idempotent on an already
	// tombstoned entry (re-stamps it, like DeleteTopic).
	DeleteText(ctx context.Context, scope TextScope, key, lang string) error
}
