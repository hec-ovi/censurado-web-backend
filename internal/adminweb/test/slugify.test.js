import { test } from "node:test";
import assert from "node:assert/strict";
import { slugify } from "../static/slugify.js";

// Parity guard: slugify() must match domain.Slugify byte-for-byte, or the Temas
// tab's /topic/<slug>/ links 404. These are EXACTLY the cases in the Go
// TestSlugify (censurado-web-backend/domain/article_test.go) and plan section 1.7.
test("matches every domain.Slugify case", () => {
  const cases = {
    "Hello, World!": "hello-world",
    "  spaced  out ": "spaced-out",
    "AI & ML": "ai-ml",
    "---": "",
    // Common Latin accents transliterate to ASCII; other scripts still drop.
    "café münchen": "cafe-munchen",
    "Análisis Político": "analisis-politico",
    "política": "politica",
    "日本語": "",
  };
  for (const [input, want] of Object.entries(cases)) {
    assert.equal(slugify(input), want, `slugify(${JSON.stringify(input)})`);
  }
});

test("política and politica collapse to the same slug", () => {
  assert.equal(slugify("Política"), slugify("politica"));
  assert.equal(slugify("Política"), "politica");
});

test("empty and nullish inputs return an empty slug", () => {
  assert.equal(slugify(""), "");
  assert.equal(slugify(null), "");
  assert.equal(slugify(undefined), "");
});
