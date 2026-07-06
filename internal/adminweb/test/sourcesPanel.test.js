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

test("lists sources from the backend /sources with orientation + enabled pill", async () => {
  server.use(
    http.get(`${ORIGIN}/sources`, () => HttpResponse.json({ sources: [source()] })),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })),
  );
  const panel = mount();
  await panel.reload();

  const domain = await screen.findByText("example.com");
  const row = domain.closest("tr.source-row");
  assert.equal(row.dataset.id, "example-com");
  assert.ok(within(row).getByText("Left"));
  assert.ok(within(row).getByText("Example news"));
  assert.ok(within(row).getByText("online"));
  assert.equal(screen.queryByRole("tab", { name: /^add source$/i }), null);
  assert.equal(within(row).queryByRole("button", { name: /^edit$/i }), null);
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

test("enabled is edited from the fullscreen source editor with a full upsert", async () => {
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

  const row = (await screen.findByText("example.com")).closest("tr.source-row");
  assert.equal(within(row).queryByRole("button", { name: /^disable$/i }), null, "disable is not a row action");
  await user.click(row);
  const dialog = await screen.findByRole("dialog", { name: "Source editor" });
  const enabled = within(dialog).getByLabelText(/^enabled$/i);
  assert.equal(enabled.checked, true);
  await user.click(enabled);
  await user.click(within(dialog).getByRole("button", { name: /^save$/i }));

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

  const row = (await screen.findByText("example.com")).closest("tr.source-row");
  const delBtn = within(row).getByRole("button", { name: /^delete$/i });
  await user.click(delBtn); // arms
  await user.click(within(row).getByRole("button", { name: /^confirm$/i })); // confirms

  await waitFor(() => assert.equal(deleted, "example-com"));
  await screen.findByText("No sources yet.");
});

test("fullscreen edit is a full upsert that keeps the slug/domain and preserves untouched fields", async () => {
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

  const row = (await screen.findByText("example.com")).closest("tr.source-row");
  await user.click(row);

  const dialog = await screen.findByRole("dialog", { name: "Source editor" });
  const editForm = dialog.querySelector("form.source-edit-full");
  assert.ok(editForm, "the fullscreen edit form opened");
  assert.equal(document.querySelector("tr.source-edit-row"), null, "edit no longer opens an inline table row");
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
  const panel = mount();
  await panel.reload();

  const row = (await screen.findByText("example.com")).closest("tr.source-row");
  assert.ok(within(row).getByText("Ada Lovelace"), "the reading author is shown as a chip");
});

test("filters sources by linked author and orientation", async () => {
  server.use(
    http.get(`${ORIGIN}/sources`, () =>
      HttpResponse.json({
        sources: [
          source({ slug: "example-com", domain: "example.com", lean: "left" }),
          source({ slug: "right-com", domain: "right.com", lean: "right" }),
        ],
      })),
    http.get(`${ORIGIN}/authors`, () =>
      HttpResponse.json({
        authors: [
          { handle: "ada", name: "Ada Lovelace", sources: ["example-com"], metadata: { beat: "tech" } },
          { handle: "jorge", name: "Jorge Luis", sources: ["right-com"], metadata: { beat: "literatura" } },
        ],
      })),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  await user.selectOptions(screen.getByLabelText(/filter by author/i), "ada");
  assert.ok(screen.getByText("example.com"));
  assert.equal(screen.queryByText("right.com"), null);

  await user.selectOptions(screen.getByLabelText(/filter by orientation/i), "right");
  await screen.findByText("No sources match.");

  await user.click(screen.getByRole("button", { name: /clear filters/i }));
  assert.ok(await screen.findByText("right.com"));
});

test("sorts sources by source, assigned author, and orientation headers", async () => {
  server.use(
    http.get(`${ORIGIN}/sources`, () =>
      HttpResponse.json({
        sources: [
          source({ slug: "zeta-com", domain: "zeta.com", lean: "neutral" }),
          source({ slug: "alpha-com", domain: "alpha.com", lean: "right" }),
          source({ slug: "middle-com", domain: "middle.com", lean: "left" }),
        ],
      })),
    http.get(`${ORIGIN}/authors`, () =>
      HttpResponse.json({
        authors: [
          { handle: "zoe", name: "Zoe", sources: ["zeta-com"], metadata: { beat: "tech" } },
          { handle: "ada", name: "Ada", sources: ["middle-com"], metadata: { beat: "world" } },
        ],
      })),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  assert.deepEqual(sourceDomains(), ["alpha.com", "middle.com", "zeta.com"]);

  await user.click(screen.getByRole("button", { name: /assigned to/i }));
  assert.deepEqual(sourceDomains(), ["middle.com", "zeta.com", "alpha.com"]);

  await user.click(screen.getByRole("button", { name: /orientation/i }));
  assert.deepEqual(sourceDomains(), ["middle.com", "zeta.com", "alpha.com"]);
});

function sourceDomains() {
  return Array.from(document.querySelectorAll("tr.source-row .source-domain")).map((node) => node.textContent);
}
