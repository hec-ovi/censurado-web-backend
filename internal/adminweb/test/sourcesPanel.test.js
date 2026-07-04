import "./setup.js";
import { test } from "node:test";
import assert from "node:assert/strict";
import { http, HttpResponse } from "msw";
import { screen, within, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { installServer, ORIGIN } from "./msw.js";
import { installDom } from "./dom.js";
import { SourcesPanel } from "../static/components/sourcesPanel.js";
import { api } from "../static/api.js";

installDom();
const server = installServer();

function mount() {
  const panel = SourcesPanel({ api });
  document.body.appendChild(panel.element);
  return panel;
}

// A full backend source row, so a full-upsert edit/toggle can echo every field.
function source(over = {}) {
  return {
    slug: "example-com",
    domain: "example.com",
    homepage: "https://example.com",
    description: "Example news",
    feed_urls: [],
    feed_type: "auto",
    language: "es",
    ownership_group: "",
    lean: "left",
    enabled: true,
    status: "ok",
    last_checked: "",
    last_ok: "",
    metadata: { note: "keep" },
    deleted: false,
    ...over,
  };
}

// Reveal the list sub-tab so its row controls (role=button) become queryable
// (subTabs hides inactive panels with the `hidden` attribute, which role queries skip).
async function showList(user) {
  await user.click(screen.getByRole("tab", { name: /^sources$/i }));
}

test("lists sources from the backend /sources with orientation + enabled pill", async () => {
  server.use(
    http.get(`${ORIGIN}/sources`, () => HttpResponse.json({ sources: [source()] })),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await showList(user);

  const domain = await screen.findByText("example.com");
  const row = domain.closest("tr.source-row");
  assert.equal(row.dataset.id, "example-com");
  assert.ok(within(row).getByText("Left"));
  assert.ok(within(row).getByText("Example news"));
  assert.ok(within(row).getByText("online"));
});

test("renders a clean zero-state for an empty source registry", async () => {
  server.use(
    http.get(`${ORIGIN}/sources`, () => HttpResponse.json({ sources: [] })),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })),
  );
  const panel = mount();
  await panel.reload();
  await screen.findByText("No sources yet.");
});

test("create posts a backend source with NO slug (backend derives it from the domain)", async () => {
  let posted = null;
  let created = false;
  server.use(
    http.get(`${ORIGIN}/sources`, () =>
      HttpResponse.json({ sources: created ? [source({ slug: "newsite-com", domain: "newsite.com" })] : [] })),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })),
    http.post(`${ORIGIN}/sources`, async ({ request }) => {
      posted = await request.json();
      created = true;
      return HttpResponse.json({ slug: "newsite-com", domain: "newsite.com" });
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  await user.type(screen.getByLabelText(/^domain$/i), "newsite.com");
  await user.click(screen.getByRole("button", { name: /add source/i }));

  await waitFor(() => assert.ok(posted, "a POST should have been sent"));
  assert.equal(posted.domain, "newsite.com");
  assert.equal(posted.slug, undefined, "create must omit slug so the backend derives it");
  assert.equal(posted.enabled, true);
  assert.equal(posted.lean, "neutral");
});

test("the enable/disable toggle is a full upsert with explicit slug + domain and enabled flipped", async () => {
  let posted = null;
  server.use(
    http.get(`${ORIGIN}/sources`, () => HttpResponse.json({ sources: [source({ enabled: true })] })),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })),
    http.post(`${ORIGIN}/sources`, async ({ request }) => {
      posted = await request.json();
      return HttpResponse.json({ slug: "example-com" });
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await showList(user);

  const row = (await screen.findByText("example.com")).closest("tr.source-row");
  await user.click(within(row).getByRole("button", { name: /^disable$/i }));

  await waitFor(() => assert.ok(posted, "a POST should have been sent"));
  assert.equal(posted.slug, "example-com");
  assert.equal(posted.domain, "example.com");
  assert.equal(posted.enabled, false, "the toggle flips enabled");
  assert.deepEqual(posted.metadata, { note: "keep" }, "the metadata bag is preserved");
  assert.equal(posted.status, "ok", "server status columns are carried, not blanked");
});

test("two-click delete removes the source by slug", async () => {
  let deleted = null;
  let removed = false;
  server.use(
    http.get(`${ORIGIN}/sources`, () => HttpResponse.json({ sources: removed ? [] : [source()] })),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })),
    http.delete(`${ORIGIN}/sources/example-com`, () => {
      deleted = "example-com";
      removed = true;
      return new HttpResponse(null, { status: 204 });
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await showList(user);

  const row = (await screen.findByText("example.com")).closest("tr.source-row");
  const delBtn = within(row).getByRole("button", { name: /^delete$/i });
  await user.click(delBtn); // arms
  await user.click(within(row).getByRole("button", { name: /^confirm$/i })); // confirms

  await waitFor(() => assert.equal(deleted, "example-com"));
  await screen.findByText("No sources yet.");
});

test("inline edit is a full upsert that keeps the slug/domain and preserves untouched fields", async () => {
  let posted = null;
  server.use(
    http.get(`${ORIGIN}/sources`, () => HttpResponse.json({ sources: [source({ description: "old desc" })] })),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })),
    http.post(`${ORIGIN}/sources`, async ({ request }) => {
      posted = await request.json();
      return HttpResponse.json({ slug: "example-com" });
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await showList(user);

  const row = (await screen.findByText("example.com")).closest("tr.source-row");
  await user.click(within(row).getByRole("button", { name: /^edit$/i }));

  const editForm = document.querySelector("form.source-edit");
  assert.ok(editForm, "the inline edit form opened");
  const desc = within(editForm).getByLabelText(/^description$/i);
  await user.clear(desc);
  await user.type(desc, "new desc");
  await user.click(within(editForm).getByRole("button", { name: /^save$/i }));

  await waitFor(() => assert.ok(posted, "a POST should have been sent"));
  assert.equal(posted.slug, "example-com", "edit sends the explicit slug so the row never forks");
  assert.equal(posted.domain, "example.com", "edit sends the explicit domain");
  assert.equal(posted.description, "new desc");
  assert.deepEqual(posted.metadata, { note: "keep" }, "untouched metadata is preserved");
});

test("a source shows the authors that read it, inverted from the author registry", async () => {
  server.use(
    http.get(`${ORIGIN}/sources`, () => HttpResponse.json({ sources: [source()] })),
    http.get(`${ORIGIN}/authors`, () =>
      HttpResponse.json({ authors: [{ handle: "ada", name: "Ada Lovelace", sources: ["example-com"], metadata: {} }] })),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await showList(user);

  const row = (await screen.findByText("example.com")).closest("tr.source-row");
  assert.ok(within(row).getByText("Ada Lovelace"), "the reading author is shown as a chip");
});
