// Buildless i18n for the single-language (English) cockpit. English source strings
// ARE the keys; t() resolves each against a catalog the server injects for the panel
// and falls back to the key itself, so the panel renders correct English even with an
// empty or absent catalog. The panel has no locale concept: there is no language
// switch, no localStorage locale, and no per-call locale read.
//
// The catalog is populated ONCE at boot by applyCatalog(), which app.js calls with the
// JSON the server injected into the #panel-i18n block (panel_text strings + the shared
// section labels from frontend_text). Reading the block lives in app.js, not here, so
// module evaluation never depends on a DOM node and tests can drive applyCatalog
// directly. Behavior-bearing tokens (DOM ids/classes, dataset/data-state values, API
// JSON keys, enum values sent to the server, {{TOKEN}} markers, URLs/paths) are NEVER
// keys here: only display text.

let CATALOG = {};
let SECTION_LABELS = {};

// applyCatalog installs the injected catalog. It accepts either the wrapper shape
// {strings, sections} (what the server injects) or a bare key->value map (convenient in
// tests). Any missing half leaves that map empty, so t()/sectionLabelFor() degrade to
// their identity/slug fallbacks. It is the single writer of the module state.
export function applyCatalog(data) {
  if (data && typeof data === "object" && (data.strings || data.sections)) {
    CATALOG = data.strings && typeof data.strings === "object" ? data.strings : {};
    SECTION_LABELS = data.sections && typeof data.sections === "object" ? data.sections : {};
    return;
  }
  CATALOG = data && typeof data === "object" ? data : {};
  SECTION_LABELS = {};
}

// Resolve a key against the injected catalog, then substitute {name} placeholders from
// params. A key absent from the catalog passes straight through (English identity), so
// an unseeded key or an empty catalog still renders the English source string.
export function t(key, params) {
  let s = Object.prototype.hasOwnProperty.call(CATALOG, key) ? CATALOG[key] : key;
  if (params) s = s.replace(/\{(\w+)\}/g, (_, k) => (params[k] != null ? String(params[k]) : ""));
  return s;
}

// sectionLabelFor maps a section slug to its display label from the shared
// frontend_text section rows (one source of record, so the panel never drifts from the
// site). An unknown slug falls back to the slug itself.
export function sectionLabelFor(slug) {
  return SECTION_LABELS[slug] || slug || "";
}
