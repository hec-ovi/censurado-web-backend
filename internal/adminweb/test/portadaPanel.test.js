import "./setup.js";
import { test } from "node:test";
import assert from "node:assert/strict";
import { http, HttpResponse } from "msw";
import { screen, within, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { installServer, ORIGIN } from "./msw.js";
import { installDom } from "./dom.js";
import { PortadaPanel } from "../static/components/portadaPanel.js";
import { api } from "../static/api.js";

installDom();
const server = installServer();

function article(overrides = {}) {
  return {
    slug: "a-piece",
    title: "A piece",
    section: "world",
    author: "Ada Reporter",
    published_at: "2026-06-30T00:00:00Z",
    topics: [],
    metadata: {},
    deleted: false,
    ...overrides,
  };
}

// Stub the two GETs the panel reads plus the POST the save issues. `onPost`
// captures the upsert body so a test can assert the exact plan it sent.
function stub({ articles = [], portadas = [], onPost } = {}) {
  server.use(
    http.get(`${ORIGIN}/articles`, () => HttpResponse.json({ articles, total: articles.length })),
    http.get(`${ORIGIN}/portadas`, () => HttpResponse.json({ portadas })),
    http.post(`${ORIGIN}/portadas`, async ({ request }) => {
      const body = await request.json();
      if (onPost) onPost(body);
      return HttpResponse.json({ ...body, deleted: false, created_at: "", updated_at: "" });
    }),
  );
}

function mount() {
  const panel = PortadaPanel({ api });
  document.body.appendChild(panel.element);
  return panel;
}

test("groups articles by published day and marks the first row as the lead", async () => {
  stub({
    articles: [
      article({ slug: "a", title: "Alpha", published_at: "2026-06-30T09:00:00Z" }),
      article({ slug: "b", title: "Bravo", published_at: "2026-06-30T08:00:00Z" }),
      article({ slug: "c", title: "Charlie", published_at: "2026-06-29T10:00:00Z" }),
    ],
  });
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  // The day <select> lists both days, newest first, and defaults to the newest.
  const daySelect = screen.getByLabelText("Day");
  assert.deepEqual(
    within(daySelect).getAllByRole("option").map((o) => o.value),
    ["2026-06-30", "2026-06-29"],
  );
  assert.equal(daySelect.value, "2026-06-30");

  // The selected day lists its articles in endpoint order; row 0 is the lead.
  let rows = document.querySelectorAll(".portada-row");
  assert.deepEqual([...rows].map((r) => r.dataset.slug), ["a", "b"]);
  assert.equal(rows[0].dataset.lead, "true");
  assert.equal(rows[0].querySelector(".portada-index")?.textContent, "Portada");
  assert.equal(rows[1].dataset.lead, "false");

  // Picking the older day re-lists that day's articles.
  await user.selectOptions(daySelect, "2026-06-29");
  rows = document.querySelectorAll(".portada-row");
  assert.deepEqual([...rows].map((r) => r.dataset.slug), ["c"]);
});

test("reordering, a role change, and a recomendado tick post the new plan", async () => {
  let posted = null;
  stub({
    articles: [
      article({ slug: "a", title: "Alpha", published_at: "2026-06-30T09:00:00Z" }),
      article({ slug: "b", title: "Bravo", published_at: "2026-06-30T08:00:00Z" }),
      article({ slug: "c", title: "Charlie", published_at: "2026-06-30T07:00:00Z" }),
    ],
    onPost: (body) => (posted = body),
  });
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  // Move Alpha (row 0) down: the in-memory order becomes Bravo, Alpha, Charlie.
  await user.click(screen.getAllByRole("button", { name: "Move down" })[0]);
  // Mark Charlie important and recommend Bravo.
  await user.selectOptions(screen.getByLabelText("Role for Charlie"), "important");
  await user.click(screen.getByRole("tab", { name: /recomendado/i }));
  await user.click(screen.getByRole("checkbox", { name: "Bravo" }));

  await user.click(screen.getByRole("tab", { name: /^portada$/i }));
  await user.click(screen.getByRole("button", { name: "Guardar portada" }));

  await waitFor(() => assert.ok(posted, "the save issued a POST"));
  assert.equal(posted.date, "2026-06-30");
  assert.deepEqual(posted.entries, [
    { slug: "b", role: "" },
    { slug: "a", role: "" },
    { slug: "c", role: "important" },
  ]);
  assert.deepEqual(posted.recomendado, ["b"]);
  await screen.findByText("Portada guardada.");
});

test("prefills the selected day's order and roles from an existing plan", async () => {
  stub({
    articles: [
      article({ slug: "a", title: "Alpha", published_at: "2026-06-30T09:00:00Z" }),
      article({ slug: "b", title: "Bravo", published_at: "2026-06-30T08:00:00Z" }),
      article({ slug: "c", title: "Charlie", published_at: "2026-06-30T07:00:00Z" }),
    ],
    portadas: [
      {
        date: "2026-06-30",
        entries: [
          { slug: "c", role: "important" },
          { slug: "a", role: "" },
        ],
        recomendado: ["b"],
        deleted: false,
        created_at: "",
        updated_at: "",
      },
    ],
  });
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  // Plan order first (Charlie, Alpha), then the uncovered article (Bravo) appended.
  const rows = document.querySelectorAll(".portada-row");
  assert.deepEqual([...rows].map((r) => r.dataset.slug), ["c", "a", "b"]);
  assert.equal(rows[0].dataset.lead, "true"); // Charlie leads per the plan

  // Roles come straight from the plan.
  assert.equal(screen.getByLabelText("Role for Charlie").value, "important");
  assert.equal(screen.getByLabelText("Role for Alpha").value, "");
  assert.equal(screen.getByLabelText("Role for Bravo").value, "");

  // Recomendado prefilled: Bravo checked, the rest clear.
  await user.click(screen.getByRole("tab", { name: /recomendado/i }));
  assert.equal(screen.getByRole("checkbox", { name: "Bravo" }).checked, true);
  assert.equal(screen.getByRole("checkbox", { name: "Alpha" }).checked, false);
});
