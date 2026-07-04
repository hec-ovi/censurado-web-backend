import "./setup.js";
import { test } from "node:test";
import assert from "node:assert/strict";
import { http, HttpResponse } from "msw";
import { screen, within, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { installServer, ORIGIN } from "./msw.js";
import { installDom } from "./dom.js";
import { ArticlesPanel } from "../static/components/articlesPanel.js";
import { api } from "../static/api.js";

installDom();
const server = installServer();

function mount() {
  const panel = ArticlesPanel({ api });
  document.body.appendChild(panel.element);
  return panel;
}

function listItem(overrides = {}) {
  return {
    slug: "a-piece",
    title: "A piece",
    section: "world",
    author: "Ada Reporter",
    published_at: "2026-06-30T00:00:00Z",
    topics: ["world"],
    metadata: {},
    deleted: false,
    content_hash: "abc",
    ...overrides,
  };
}

test("renders the article list from /articles", async () => {
  server.use(
    http.get(`${ORIGIN}/articles`, () =>
      HttpResponse.json({ articles: [listItem()], total: 1 })),
  );
  const panel = mount();
  await panel.reload();
  await userEvent.setup().click(screen.getByRole("tab", { name: /^articles$/i }));

  const titleCell = await screen.findByText("A piece");
  const row = titleCell.closest(".article-row");
  assert.ok(row, "the article row renders");
  assert.equal(row.dataset.slug, "a-piece");
  assert.ok(within(row).getByText("Ada Reporter"));
  assert.ok(within(row).getByText("world"));
});

test("opening a row fetches and shows the full body", async () => {
  server.use(
    http.get(`${ORIGIN}/articles`, () => HttpResponse.json({ articles: [listItem()], total: 1 })),
    http.get(`${ORIGIN}/articles/a-piece`, () =>
      HttpResponse.json({ ...listItem(), id: "1", body: "The full body text.", created_at: "2026-06-30T00:00:00Z" })),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await user.click(screen.getByRole("tab", { name: /^articles$/i }));
  await screen.findByText("A piece");

  await user.click(screen.getByRole("button", { name: /open/i }));
  // The body shows in the read-only <pre> preview (and is prefilled into the edit
  // textarea); assert the preview specifically.
  const preview = await screen.findByText("The full body text.", { selector: "pre" });
  assert.ok(preview.classList.contains("article-body"));
});

test("does not render the old manual article creation form", async () => {
  let posted = false;
  server.use(
    http.get(`${ORIGIN}/articles`, () => HttpResponse.json({ articles: [], total: 0 })),
    http.post(`${ORIGIN}/articles`, () => {
      posted = true;
      return HttpResponse.json({ id: "1", slug: "fresh-piece" }, { status: 201 });
    }),
  );
  const panel = mount();
  await panel.reload();
  await screen.findByText("No articles yet.");

  assert.equal(screen.queryByRole("button", { name: /publish article/i }), null);
  assert.equal(screen.queryByLabelText(/^title$/i), null);
  assert.equal(posted, false, "the list-only view does not issue article POSTs");
});

test("a deleted article lists with a badge and a Restore action that clears the tombstone", async () => {
  let restored = false;
  let sawIncludeDeleted = false;
  server.use(
    http.get(`${ORIGIN}/articles`, ({ request }) => {
      const url = new URL(request.url);
      // The panel asks for tombstoned rows so a delete is recoverable from here.
      if (url.searchParams.get("include_deleted") === "true") sawIncludeDeleted = true;
      return HttpResponse.json({ articles: [listItem({ deleted: !restored })], total: 1 });
    }),
    http.post(`${ORIGIN}/articles/a-piece/restore`, () => {
      restored = true;
      return HttpResponse.json({ ...listItem({ deleted: false }), id: "1" });
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await user.click(screen.getByRole("tab", { name: /deleted articles/i }));

  const row = (await screen.findByText("A piece")).closest(".article-row");
  assert.ok(sawIncludeDeleted, "the list request opts into include_deleted");
  // The tombstone is visible and the destructive action is replaced by Restore.
  assert.ok(within(row).getByText("deleted"), "the deleted badge renders");
  assert.ok(within(row).getByRole("button", { name: /restore/i }));
  assert.equal(within(row).queryByRole("button", { name: /^delete$/i }), null, "no Delete on a tombstoned row");

  await user.click(within(row).getByRole("button", { name: /restore/i }));

  // The restore posts, then the table reloads without the tombstone.
  await waitFor(() => assert.ok(restored, "POST /articles/{slug}/restore was called"));
  await user.click(screen.getByRole("tab", { name: /^articles$/i }));
  await waitFor(() => {
    const fresh = screen.getByText("A piece").closest(".article-row");
    assert.equal(within(fresh).queryByText("deleted"), null, "badge cleared after restore");
    assert.ok(within(fresh).getByRole("button", { name: /^delete$/i }), "restored row shows Delete again");
  });
});

test("renders a clean zero-state for an empty corpus", async () => {
  server.use(http.get(`${ORIGIN}/articles`, () => HttpResponse.json({ articles: [], total: 0 })));
  const panel = mount();
  await panel.reload();

  await screen.findByText("No articles yet.");
});
