// Unit tests for the single-language i18n core: t() resolves against the injected
// catalog and falls back to the English key, applyCatalog installs it, sectionLabelFor
// reads the shared section labels, and the former language-toggle exports are gone.
import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import * as i18n from "../static/components/i18n.js";
import { t, applyCatalog, sectionLabelFor } from "../static/components/i18n.js";

// The catalog is module-level shared state; reset it after every case so ordering
// cannot leak an override into the next test.
afterEach(() => applyCatalog({}));

test("t() falls back to the English key when no catalog is installed", () => {
  applyCatalog({});
  assert.equal(t("Save"), "Save");
  assert.equal(t("Portada saved."), "Portada saved.");
});

test("applyCatalog installs DB overrides that t() returns; unseeded keys still pass through", () => {
  applyCatalog({ strings: { "Save portada": "Guardar portada", Save: "Store" } });
  assert.equal(t("Save portada"), "Guardar portada");
  assert.equal(t("Save"), "Store");
  assert.equal(t("Delete"), "Delete");
});

test("applyCatalog also accepts a bare key->value map (test convenience)", () => {
  applyCatalog({ Save: "Guardar" });
  assert.equal(t("Save"), "Guardar");
});

test("t() substitutes {name} params after resolving the key", () => {
  applyCatalog({});
  assert.equal(t("Created {id}.", { id: "ada" }), "Created ada.");
  applyCatalog({ strings: { "Created {id}.": "Se creó {id}." } });
  assert.equal(t("Created {id}.", { id: "ada" }), "Se creó ada.");
});

test("sectionLabelFor uses the injected section labels and falls back to the slug", () => {
  applyCatalog({ sections: { world: "World", tech: "Technology" } });
  assert.equal(sectionLabelFor("world"), "World");
  assert.equal(sectionLabelFor("tech"), "Technology");
  assert.equal(sectionLabelFor("unknown-slug"), "unknown-slug");
  applyCatalog({});
  assert.equal(sectionLabelFor("world"), "world");
});

test("the language-toggle exports are gone (single-language panel)", () => {
  assert.equal(i18n.currentLocale, undefined);
  assert.equal(i18n.setLocale, undefined);
  assert.equal(i18n.LANGS, undefined);
  assert.equal(i18n.LANG_LABEL, undefined);
  assert.equal(i18n.ES, undefined);
});
