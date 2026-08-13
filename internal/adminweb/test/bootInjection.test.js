// The real boot path: the server injects the panel_text catalog into the #panel-i18n
// data block, and app.js's module-eval boot reads it (getElementById -> JSON.parse ->
// applyCatalog) BEFORE mountApp so the first render is already in the injected strings.
// Each case sets up that DOM and imports app.js with a unique query so its boot re-runs.
import "./setup.js";
import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import { http, HttpResponse } from "msw";
import { screen, within } from "@testing-library/dom";
import { installServer, ORIGIN } from "./msw.js";
import { installDom } from "./dom.js";
import { applyCatalog } from "../static/components/i18n.js";

installDom();
const server = installServer();

// app.js and i18n.js are module singletons shared across the query-busted re-imports,
// so reset the catalog between cases; otherwise a prior injection leaks into the next.
afterEach(() => applyCatalog({}));

function baseHandlers(extra = []) {
  return [
    ...extra,
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })),
    http.get(`${ORIGIN}/sources`, () => HttpResponse.json({ sources: [] })),
    http.get(`${ORIGIN}/articles:days`, () => HttpResponse.json({ days: [] })),
    http.get(`${ORIGIN}/articles:facets`, () => HttpResponse.json({ sections: [], authors: [], topics: [] })),
    http.get(`${ORIGIN}/articles`, () => HttpResponse.json({ articles: [], total: 0 })),
    http.get(`${ORIGIN}/portadas`, () => HttpResponse.json({ portadas: [] })),
    http.get(`${ORIGIN}/recomendado`, () => HttpResponse.json({ slugs: [] })),
    http.get(`${ORIGIN}/schedules`, () => HttpResponse.json({ schedules: [] })),
  ];
}

async function boot(json, tag) {
  document.body.innerHTML =
    `<script type="application/json" id="panel-i18n">${json}</script><div id="app"></div>`;
  await import(`../static/app.js?boot=${tag}`);
}

test("the boot reads #panel-i18n and renders the injected labels", async () => {
  server.use(...baseHandlers());
  await boot(
    JSON.stringify({ strings: { Articles: "Stories", "Admin Panel": "Console" }, sections: {} }),
    "labels",
  );
  const tablist = await screen.findByRole("tablist", { name: "Console sections" });
  const first = within(tablist).getAllByRole("tab")[0];
  assert.equal(first.textContent, "Stories");
  assert.equal(document.title, "Console");
});

test("a malformed #panel-i18n block does not throw; the panel renders English", async () => {
  server.use(...baseHandlers());
  await boot("}{ not json", "malformed");
  const tablist = await screen.findByRole("tablist", { name: "Console sections" });
  const labels = within(tablist).getAllByRole("tab").map((tab) => tab.textContent);
  assert.deepEqual(labels, ["Articles", "Authors", "Sources", "Portada", "Recomendado", "Topics", "Automation"]);
});
