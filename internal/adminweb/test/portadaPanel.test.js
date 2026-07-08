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

// jsdom implements no native HTML5 drag-and-drop pipeline and user-event has no
// drag helper, so we hand-build a minimal DataTransfer and fire the real drag
// events the component listens for.
function makeDataTransfer() {
  const store = new Map();
  return {
    effectAllowed: "",
    dropEffect: "",
    setData: (k, v) => store.set(k, String(v)),
    getData: (k) => store.get(k) ?? "",
    setDragImage: () => {},
  };
}

function fireDrag(type, target, dataTransfer) {
  // Build the event from the target's own jsdom window: Node's built-in global
  // Event shadows jsdom's, and jsdom's dispatchEvent rejects a foreign Event.
  const view = target.ownerDocument.defaultView;
  const ev = new view.Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "dataTransfer", { value: dataTransfer, configurable: true });
  target.dispatchEvent(ev);
  return ev;
}

const rows = () => [...document.querySelectorAll(".portada-row")];
const rowFor = (slug) => rows().find((r) => r.dataset.slug === slug);

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

  // The arrow controls step through days in the same newest-to-oldest order.
  await user.click(screen.getByRole("button", { name: "Next day" }));
  rows = document.querySelectorAll(".portada-row");
  assert.deepEqual([...rows].map((r) => r.dataset.slug), ["c"]);
  await user.click(screen.getByRole("button", { name: "Back" }));
  rows = document.querySelectorAll(".portada-row");
  assert.deepEqual([...rows].map((r) => r.dataset.slug), ["a", "b"]);

  // Picking the older day directly still works.
  await user.selectOptions(daySelect, "2026-06-29");
  rows = document.querySelectorAll(".portada-row");
  assert.deepEqual([...rows].map((r) => r.dataset.slug), ["c"]);
});

test("reordering and a role change post the new plan without per-day recomendado", async () => {
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
  // Mark Charlie important. Global recomendado is managed in its own panel now.
  await user.selectOptions(screen.getByLabelText("Role for Charlie"), "important");

  await user.click(screen.getByRole("button", { name: "Save portada" }));

  await waitFor(() => assert.ok(posted, "the save issued a POST"));
  assert.equal(posted.date, "2026-06-30");
  assert.deepEqual(posted.entries, [
    { slug: "b", role: "" },
    { slug: "a", role: "" },
    { slug: "c", role: "important" },
  ]);
  assert.equal(Object.hasOwn(posted, "recomendado"), false);
  await screen.findByText("Portada saved.");
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

  assert.equal(screen.queryByRole("tab", { name: /recomendado/i }), null);
});

test("dragging a card onto another card's full-row zone reorders it and marks it full width", async () => {
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

  // Order starts a, b, c. Grab Charlie's handle and drop it on Alpha's full-row
  // band: Charlie lands before Alpha and becomes a full-width (important) card.
  const dt = makeDataTransfer();
  fireDrag("dragstart", rowFor("c").querySelector(".portada-drag-handle"), dt);
  const fullZone = rowFor("a").querySelector(".portada-drop-full");
  fireDrag("dragover", fullZone, dt);
  fireDrag("drop", fullZone, dt);

  // The in-memory order and the widened role are reflected immediately.
  assert.deepEqual(rows().map((r) => r.dataset.slug), ["c", "a", "b"]);
  assert.equal(rowFor("c").dataset.role, "important");

  await user.click(screen.getByRole("button", { name: "Save portada" }));
  await waitFor(() => assert.ok(posted, "the save issued a POST"));
  assert.deepEqual(posted.entries, [
    { slug: "c", role: "important" },
    { slug: "a", role: "" },
    { slug: "b", role: "" },
  ]);
  await screen.findByText("Portada saved.");
});

test("dropping a card on another card's right zone reorders it as a half-width card", async () => {
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

  // Order starts a, b, c. Drop Alpha on Charlie's right (after) zone: Alpha lands
  // after Charlie and stays a normal half-width card (role "").
  const dt = makeDataTransfer();
  fireDrag("dragstart", rowFor("a").querySelector(".portada-drag-handle"), dt);
  const afterZone = rowFor("c").querySelector(".portada-drop-after");
  fireDrag("dragover", afterZone, dt);
  fireDrag("drop", afterZone, dt);

  assert.deepEqual(rows().map((r) => r.dataset.slug), ["b", "c", "a"]);
  assert.equal(rowFor("a").dataset.role, "");

  await user.click(screen.getByRole("button", { name: "Save portada" }));
  await waitFor(() => assert.ok(posted, "the save issued a POST"));
  assert.deepEqual(posted.entries, [
    { slug: "b", role: "" },
    { slug: "c", role: "" },
    { slug: "a", role: "" },
  ]);
});
