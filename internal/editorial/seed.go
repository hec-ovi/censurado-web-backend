// Package editorial holds the seed catalog for the newsroom's per-language editorial
// config (the editorial_text text-store scope): the language-specific writing anchors the
// workflow prompts apply and the Telegram bot's response directive. Unlike the two UI
// catalogs (frontend_text/panel_text) it has NO English base row: each row is authored in
// its own language, because Spanish slop words, lexicon, and orthography are not the
// English ones. Today the catalog carries only the live language, Spanish ("es"); a new
// language is generated per language, not translated from a base.
//
// These rows are the SINGLE source of truth the brain repo's workflow prompts read at
// write time (via `cli/censurado.py editorial-rules <lang>`) and the Telegram bridge reads
// for its response directive, so the anchors live in the DB (operator-editable) instead of
// hardcoded across the prompt files. Structured values (lists, maps) are JSON-encoded in
// the value string; scalar values are plain text.
package editorial

// EditorialSeedEntry is one editorial-config row: its stable key, the language it is
// authored in, and the value (plain text, or a JSON array/object for a structured anchor).
type EditorialSeedEntry struct {
	Key   string
	Lang  string
	Value string
}

// EditorialSeed returns the live editorial-config catalog. Every current row is Spanish
// ("es"), transcribed from the newsroom's EDITORIAL-RULES.md, prompts/editorial/style.md,
// and the Telegram bridge's response directive.
func EditorialSeed() []EditorialSeedEntry { return editorialSeed }

var editorialSeed = []EditorialSeedEntry{
	// How a source is named in the prose: plain text, never a link. The rule (attribute as
	// plain text, no body links) stays in the English prompt; only the exemplar is per-language.
	{"attribution.example", "es", "según X"},
	// The Telegram bot's response-language directive: the register the bridge prepends so
	// the agent answers in the portal's voice regardless of the language it is written in.
	{"bot.directive", "es", "Respondé siempre en español rioplatense (voseo), el registro del portal Censurado, aunque te escriban en otro idioma."},
	// The one-line italic disclaimer a satire/opinion/fiction voice opens with. The rule
	// (one disclaimer line, straight news carries none) stays English; the wording is per-language.
	{"disclaimer.satire", "es", "*Tómelo como un cuentillo de ciencia ficción.*"},
	// Banned sensational words: the lexicon that is specific to this language.
	{"lexicon.bans", "es", `["demoledor","escandaloso","letal","brutal","sin precedentes","increíble","no te lo podés perder"]`},
	// Preferred swaps: an inflated word mapped to its flatter, accurate replacement.
	{"lexicon.swaps", "es", `{"polémico":"discutido","fulminó":"rechazó","castigó":"afectó","histórico":"destacado"}`},
	// The accents and opening marks the orthography pass enforces.
	{"orthography.charset", "es", "á é í ó ú ü, la ñ, y los signos de apertura ¿ ¡"},
	// Worked examples of ASCII-stripped words and their correctly accented form.
	{"orthography.examples", "es", `{"programacion":"programación","numero":"número","publico":"público","analisis":"análisis","economia":"economía"}`},
	// Candor tics: opening phrases that fake sincerity, to be cut.
	{"slop.candor_tics", "es", `["la verdad es que","seamos honestos","hay que decirlo"]`},
	// Generic AI-slop phrases the drafting and evaluate nodes reject.
	{"slop.phrases", "es", `["en el mundo actual","es importante notar"]`},
}
