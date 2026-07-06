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

function panelHandlers({ articles = [], authors = [], portadas = [], extra = [] } = {}) {
  return [
    http.get(`${ORIGIN}/articles`, ({ request }) => {
      const url = new URL(request.url);
      assert.equal(url.searchParams.get("order"), "oldest");
      const list = typeof articles === "function" ? articles() : articles;
      return HttpResponse.json({ articles: list, total: list.length });
    }),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors })),
    http.get(`${ORIGIN}/portadas`, () => HttpResponse.json({ portadas })),
    ...extra,
  ];
}

function listItem(overrides = {}) {
  return {
    slug: "a-piece",
    title: "A piece",
    section: "world",
    author: "Ada Reporter",
    published_at: "2026-06-30T00:00:00Z",
    topics: ["world"],
    metadata: {
      subtitle: "A compact subtitle",
      summary: "A compact summary for operators.",
    },
    has_media: false,
    card_type: "text",
    deleted: false,
    content_hash: "abc",
    ...overrides,
  };
}

function rows() {
  return [...document.querySelectorAll(".article-row")];
}

test("renders the article table from /articles", async () => {
  server.use(
    ...panelHandlers({
      articles: [listItem({ author: "ada-reporter" })],
      authors: [{ handle: "ada-reporter", name: "Ada Reporter", avatar: "/media/ada.png" }],
      portadas: [{ date: "2026-06-30", entries: [{ slug: "a-piece", role: "important" }], recomendado: [] }],
    }),
  );
  const panel = mount();
  await panel.reload();

  const row = (await screen.findByText("A piece")).closest(".article-row");
  assert.ok(row, "the article row renders");
  assert.equal(row.dataset.slug, "a-piece");
  assert.equal(row.dataset.kind, "text");
  assert.equal(row.dataset.layout, "1");
  assert.equal(row.dataset.role, "important");
  assert.ok(within(row).getByText("Ada Reporter"));
  assert.ok(within(row).getByLabelText("Full row"));
  assert.ok(within(row).getByText("30/06/26 00:00"));
  assert.ok(within(row).getByText("A compact subtitle"));
  assert.ok(within(row).getByText("Text"));
  assert.equal(within(row).queryByRole("button", { name: /open/i }), null);
  assert.ok(within(row).getByRole("button", { name: /delete article/i }));
  assert.ok(screen.getAllByText("30/06/26").some((node) => node.tagName === "TIME"));
});

test("opening a row fetches and shows the full body", async () => {
  server.use(
    ...panelHandlers({
      articles: [listItem()],
      extra: [
        http.get(`${ORIGIN}/articles/a-piece`, () =>
          HttpResponse.json({ ...listItem(), id: "1", body: "The full body text.", created_at: "2026-06-30T00:00:00Z" })),
      ],
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  await user.click((await screen.findByText("A piece")).closest(".article-row"));
  const preview = await screen.findByText("The full body text.", { selector: "pre" });
  assert.ok(preview.classList.contains("article-body"));
});

test("filters by media and author dropdown with author search", async () => {
  server.use(
    ...panelHandlers({
      articles: [
        listItem({ slug: "ada-text", title: "Ada text", author: "ada-reporter", metadata: {}, has_media: false, card_type: "text" }),
        listItem({
          slug: "bob-image",
          title: "Bob image",
          author: "bob-editor",
          metadata: { card: { type: "image", src: "https://images.test/a.jpg", alt: "A card" } },
          has_media: true,
          card_type: "image",
        }),
        listItem({
          slug: "ada-youtube",
          title: "Ada youtube",
          author: "ada-reporter",
          metadata: { card: { type: "youtube", src: "https://youtu.be/x" } },
          has_media: true,
          card_type: "youtube",
        }),
      ],
      authors: [
        { handle: "ada-reporter", name: "Ada Reporter", avatar: "/media/ada.png" },
        { handle: "bob-editor", name: "Bob Editor", avatar: "/media/bob.png" },
      ],
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await screen.findByText("Bob image");

  await user.selectOptions(screen.getByLabelText("Filter by author"), "ada-reporter");
  assert.deepEqual(rows().map((row) => row.dataset.slug), ["ada-text", "ada-youtube"]);

  await user.selectOptions(screen.getByLabelText("Filter by author"), "");
  assert.deepEqual(rows().map((row) => row.dataset.slug), ["ada-text", "bob-image", "ada-youtube"]);

  await user.click(screen.getByLabelText("Image"));
  assert.deepEqual(rows().map((row) => row.dataset.slug), ["bob-image"]);

  await user.type(screen.getByLabelText("Search authors"), "bob");
  assert.equal([...screen.getByLabelText("Filter by author").options].some((option) => option.textContent === "Ada Reporter"), false);
  assert.equal([...screen.getByLabelText("Filter by author").options].some((option) => option.textContent === "Bob Editor"), true);
});

test("groups video cards under youtube and renders a video preview", async () => {
  server.use(
    ...panelHandlers({
      articles: [
        listItem({
          slug: "self-hosted-video",
          title: "Self hosted video",
          metadata: { card: { type: "video", src: "/media/clip.mp4" } },
          card_type: "video",
        }),
        listItem({
          slug: "youtube-card",
          title: "YouTube card",
          metadata: { card: { type: "youtube", src: "dQw4w9WgXcQ" } },
          card_type: "youtube",
        }),
      ],
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await screen.findByText("Self hosted video");

  assert.equal(screen.queryByLabelText("Video"), null);
  assert.ok(document.querySelector('tr[data-slug="self-hosted-video"] video.article-media-thumb'));

  await user.click(screen.getByLabelText("YouTube"));
  assert.deepEqual(rows().map((row) => row.dataset.slug), ["self-hosted-video", "youtube-card"]);
});

test("sorts by date and keeps portada layout order inside each day", async () => {
  server.use(
    ...panelHandlers({
      articles: [
        listItem({ slug: "b", title: "Bravo story", published_at: "2026-06-30T08:00:00Z" }),
        listItem({ slug: "a", title: "Alpha story", published_at: "2026-06-30T09:00:00Z" }),
        listItem({ slug: "c", title: "Charlie story", published_at: "2026-06-29T09:00:00Z" }),
      ],
      portadas: [
        { date: "2026-06-30", entries: [{ slug: "a", role: "" }, { slug: "b", role: "important" }], recomendado: [] },
        { date: "2026-06-29", entries: [{ slug: "c", role: "" }], recomendado: [] },
      ],
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await screen.findByText("Alpha story");

  assert.deepEqual(rows().map((row) => row.dataset.slug), ["c", "a", "b"]);
  assert.deepEqual(rows().map((row) => row.dataset.layout), ["1", "1", "2"]);

  await user.click(screen.getByLabelText("Descending"));
  assert.deepEqual(rows().map((row) => row.dataset.slug), ["a", "b", "c"]);
});

test("filters by day with a searchable day dropdown", async () => {
  server.use(
    ...panelHandlers({
      articles: [
        listItem({ slug: "first-day", title: "First day", published_at: "2026-06-28T08:00:00Z" }),
        listItem({ slug: "second-day", title: "Second day", published_at: "2026-06-29T09:00:00Z" }),
      ],
      portadas: [
        { date: "2026-06-28", entries: [{ slug: "first-day", role: "" }], recomendado: [] },
        { date: "2026-06-29", entries: [{ slug: "second-day", role: "" }], recomendado: [] },
      ],
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await screen.findByText("Second day");

  await user.type(screen.getByLabelText("Search days"), "29");
  await user.selectOptions(screen.getByLabelText("Filter by day"), "2026-06-29");

  assert.deepEqual(rows().map((row) => row.dataset.slug), ["second-day"]);
});

test("deletes from an internal confirmation modal", async () => {
  let deleted = false;
  server.use(
    ...panelHandlers({
      articles: () => (deleted ? [] : [listItem()]),
      extra: [
        http.delete(`${ORIGIN}/articles/a-piece`, () => {
          deleted = true;
          return new HttpResponse(null, { status: 204 });
        }),
      ],
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  const row = (await screen.findByText("A piece")).closest(".article-row");
  await user.click(within(row).getByRole("button", { name: /delete article/i }));
  const dialog = screen.getByRole("dialog", { name: /delete article/i });
  assert.ok(within(dialog).getByText(/no recovery/i));

  await user.click(within(dialog).getByRole("button", { name: /^delete$/i }));

  await waitFor(() => assert.ok(deleted, "DELETE /articles/{slug} was called"));
  await screen.findByText("No articles yet.");
  assert.equal(screen.queryByText("A piece"), null);
});

test("does not render the old manual article creation form", async () => {
  let posted = false;
  server.use(
    ...panelHandlers({
      extra: [
        http.post(`${ORIGIN}/articles`, () => {
          posted = true;
          return HttpResponse.json({ id: "1", slug: "fresh-piece" }, { status: 201 });
        }),
      ],
    }),
  );
  const panel = mount();
  await panel.reload();
  await screen.findByText("No articles yet.");

  assert.equal(screen.queryByRole("button", { name: /publish article/i }), null);
  assert.equal(screen.queryByLabelText(/^title$/i), null);
  assert.equal(posted, false, "the list-only view does not issue article POSTs");
});

test("renders a clean zero-state for an empty corpus", async () => {
  server.use(...panelHandlers());
  const panel = mount();
  await panel.reload();

  await screen.findByText("No articles yet.");
});
