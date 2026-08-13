import "./setup.js";
import { test } from "node:test";
import assert from "node:assert/strict";
import { http, HttpResponse } from "msw";
import { screen, within } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { installServer, ORIGIN } from "./msw.js";
import { installDom } from "./dom.js";
import { mountApp } from "../static/app.js";

installDom();
const server = installServer();

// Every mount pings the whole panel surface, and it is ALL same-origin backend now:
// the empty author + source registries, the article day index + corpus (used by
// Articles and Temas), and portadas. There is NO brain: MSW runs in
// onUnhandledRequest:"error" mode, so if any component reached for a /api/brain/* (or
// any un-stubbed) route the mount would hard-fail. Registering ONLY backend handlers
// is therefore the "brain down" checkpoint: the panel works with just the backend up.
// The defaults model a fully EMPTY newsroom. `extra` is spread FIRST so a test-specific
// handler overrides the default for the same route.
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

function mount() {
  const root = document.createElement("div");
  document.body.appendChild(root);
  return mountApp(root);
}

test("mounts exactly seven tabs in order, with no Editorial, Agentic, or Status tab", async () => {
  server.use(...baseHandlers());
  mount();

  const consoleTablist = screen.getByRole("tablist", { name: "Console sections" });
  const tabs = within(consoleTablist).getAllByRole("tab");
  assert.equal(tabs.length, 7, "seven operator tabs");
  assert.deepEqual(
    tabs.map((tab) => tab.textContent),
    ["Articles", "Authors", "Sources", "Portada", "Recomendado", "Topics", "Automation"],
  );
  assert.equal(within(consoleTablist).queryByRole("tab", { name: /editorial/i }), null, "no Editorial tab");
  assert.equal(within(consoleTablist).queryByRole("tab", { name: /agentic/i }), null, "no Agentic workflow tab");
  assert.equal(within(consoleTablist).queryByRole("tab", { name: /status/i }), null, "no Status tab");

  assert.equal(tabs[0].getAttribute("aria-selected"), "true");
});

test("renders a clean zero-state on every surviving tab (backend only, brain down)", async () => {
  server.use(...baseHandlers());
  mount();

  assert.ok((await screen.findAllByText("No articles yet.")).length >= 1);
  await screen.findByText("No topics yet.");
  await screen.findByText("No authors yet. Create one to get started.");
  await screen.findByText("No sources yet.");
  await screen.findByText("No schedules yet. Create one to put the newsroom on a clock.");
});

test("the Temas tab renders derived topic rows with stable slugs", async () => {
  server.use(
    ...baseHandlers([
      http.get(`${ORIGIN}/articles`, () =>
        HttpResponse.json({
          articles: [
            { slug: "a", title: "A", section: "world", author: "x", published_at: "", topics: ["Análisis Político"], metadata: {} },
          ],
          total: 1,
        })),
    ]),
  );
  const user = userEvent.setup();
  mount();

  await user.click(screen.getByRole("tab", { name: /topics/i }));
  const panel = screen.getByRole("tabpanel", { name: /topics/i });
  const chip = await within(panel).findByText("Análisis Político");
  const row = chip.closest(".tema-row");
  assert.equal(row.dataset.slug, "analisis-politico");
  assert.equal(within(row).queryByRole("link"), null);
});

test("the Authors tab is list-only with no New author subtab", async () => {
  server.use(...baseHandlers());
  const user = userEvent.setup();
  mount();

  await user.click(screen.getByRole("tab", { name: /authors/i }));
  const panel = screen.getByRole("tabpanel", { name: /authors/i });
  assert.equal(within(panel).queryByRole("tablist", { name: /author sections/i }), null);
  assert.equal(within(panel).queryByRole("tab", { name: /new author/i }), null);
});
