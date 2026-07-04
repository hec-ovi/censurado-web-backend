// A byte-exact JS port of domain.Slugify (censurado-web-backend/domain/article.go).
// The Temas tab uses it to build /topic/<slug>/ links whose slug must match the
// generator's, which derives the same slug from domain.Slugify. A divergence here
// 404s the topic link, so this stays in lockstep with the Go original (guarded by
// panel/test/slugify.test.js, which mirrors the Go TestSlugify cases).
//
// The Go pipeline is: lowercase + trim, transliterate common Latin accents, replace
// runs of non-alphanumerics with a single hyphen, trim leading/trailing hyphens,
// return "" when nothing usable remains.

// slugTranslit folds common Latin (especially Spanish) accented letters to ASCII,
// copied verbatim from the Go slugTranslit replacer. It runs after lowercasing.
const SLUG_TRANSLIT = {
  "á": "a", "à": "a", "ä": "a", "â": "a", "ã": "a", "å": "a",
  "é": "e", "è": "e", "ë": "e", "ê": "e",
  "í": "i", "ì": "i", "ï": "i", "î": "i",
  "ó": "o", "ò": "o", "ö": "o", "ô": "o", "õ": "o",
  "ú": "u", "ù": "u", "ü": "u", "û": "u",
  "ñ": "n", "ç": "c", "ý": "y", "ÿ": "y",
};

// slugNonAlnum matches a run of characters outside the ASCII alnum set, mirroring
// the Go regexp `[^a-z0-9]+`. No `u` flag: like Go's run-based collapse, a run of
// non-alnum code units becomes a single hyphen.
const SLUG_NON_ALNUM = /[^a-z0-9]+/g;

export function slugify(value) {
  // strings.ToLower(strings.TrimSpace(s)): trim, then lowercase. (Order is
  // immaterial for whitespace vs case, but kept faithful to the Go call.)
  let s = String(value == null ? "" : value).trim().toLowerCase();
  // slugTranslit.Replace: fold accents one character at a time.
  s = Array.from(s, (ch) => (ch in SLUG_TRANSLIT ? SLUG_TRANSLIT[ch] : ch)).join("");
  // slugNonAlnum.ReplaceAllString(s, "-"): collapse non-alnum runs to one hyphen.
  s = s.replace(SLUG_NON_ALNUM, "-");
  // strings.Trim(s, "-"): drop leading/trailing hyphens. Empty when nothing remains.
  return s.replace(/^-+/, "").replace(/-+$/, "");
}
