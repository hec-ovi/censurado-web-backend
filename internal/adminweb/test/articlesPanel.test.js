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
  const currentArticles = () => (typeof articles === "function" ? articles() : articles);
  const articleFacets = () => {
    const sections = new Map();
    const facetAuthors = new Map();
    const topics = new Map();
    for (const article of currentArticles().filter((article) => !article.deleted)) {
      if (article.section) sections.set(article.section, (sections.get(article.section) || 0) + 1);
      if (article.author) facetAuthors.set(article.author, (facetAuthors.get(article.author) || 0) + 1);
      for (const topic of article.topics || []) topics.set(topic, (topics.get(topic) || 0) + 1);
    }
    const toFacets = (counts) => [...counts.entries()]
      .map(([value, count]) => ({ value, count }))
      .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
    return { sections: toFacets(sections), authors: toFacets(facetAuthors), topics: toFacets(topics) };
  };
  const visibleArticles = (url) => {
    let list = currentArticles().filter((article) => !article.deleted);
    const author = url.searchParams.get("author");
    const date = url.searchParams.get("date");
    const titleSubtitleQuery = (url.searchParams.get("title_subtitle_q") || "").toLowerCase();
    if (author) list = list.filter((article) => article.author === author);
    if (date) list = list.filter((article) => String(article.published_at || "").slice(0, 10) === date);
    if (titleSubtitleQuery) {
      list = list.filter((article) => {
        const subtitle = (article.metadata && article.metadata.subtitle) || "";
        return `${article.title || ""} ${subtitle}`.toLowerCase().includes(titleSubtitleQuery);
      });
    }
    const dir = url.searchParams.get("order") === "newest" ? -1 : 1;
    list = [...list].sort((a, b) => dir * String(a.published_at || "").localeCompare(String(b.published_at || "")));
    return list;
  };
  return [
    http.get(`${ORIGIN}/articles:days`, ({ request }) => {
      const url = new URL(request.url);
      const counts = new Map();
      for (const article of visibleArticles(url)) {
        const day = String(article.published_at || "").slice(0, 10);
        if (!day) continue;
        counts.set(day, (counts.get(day) || 0) + 1);
      }
      const days = [...counts.entries()]
        .sort(([a], [b]) => (url.searchParams.get("order") === "newest" ? b.localeCompare(a) : a.localeCompare(b)))
        .map(([date, count]) => ({ date, count }));
      return HttpResponse.json({ days });
    }),
    http.get(`${ORIGIN}/articles`, ({ request }) => {
      const url = new URL(request.url);
      assert.ok(["oldest", "newest"].includes(url.searchParams.get("order")));
      const list = visibleArticles(url);
      return HttpResponse.json({ articles: list, total: list.length });
    }),
    http.get(`${ORIGIN}/articles:facets`, () => HttpResponse.json(articleFacets())),
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
  assert.ok(within(row).getByLabelText("Main article"));
  assert.ok(within(row).getByText("30/06/26 00:00"));
  assert.ok(within(row).getByText("A compact subtitle"));
  assert.ok(within(row).getByText("Text"));
  assert.equal(within(row).queryByRole("button", { name: /open/i }), null);
  assert.ok(within(row).getByRole("button", { name: /delete article/i }));
  assert.ok(screen.getAllByText("30/06/26").some((node) => node.tagName === "TIME"));
});

test("title click opens the fullscreen single-article editor and saves ordered fields", async () => {
  let savedPayload = null;
  server.use(
    ...panelHandlers({
      articles: [listItem({ author: "ada-reporter" })],
      authors: [
        { handle: "ada-reporter", name: "Ada Reporter", avatar: "/media/ada.png" },
        { handle: "bob-editor", name: "Bob Editor", avatar: "/media/bob.png" },
      ],
      extra: [
        http.get(`${ORIGIN}/articles/a-piece`, () =>
          HttpResponse.json({
            ...listItem({ author: "ada-reporter" }),
            id: "1",
            body: "The full body text.",
            created_at: "2026-06-30T00:00:00Z",
          })),
        http.put(`${ORIGIN}/articles/a-piece`, async ({ request }) => {
          savedPayload = await request.json();
          return HttpResponse.json({
            ...listItem({ author: savedPayload.author, title: savedPayload.title, section: savedPayload.section }),
            body: savedPayload.body,
            topics: savedPayload.topics,
            metadata: savedPayload.metadata,
            published_at: savedPayload.published_at,
          });
        }),
      ],
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  const row = (await screen.findByText("A piece")).closest(".article-row");
  await user.click(within(row).getByRole("button", { name: /edit article/i }));
  const dialog = await screen.findByRole("dialog", { name: "Article editor" });
  assert.equal(within(dialog).queryByRole("tablist"), null);
  assert.equal(within(dialog).queryByRole("heading", { name: /A piece/i }), null);
  assert.deepEqual(
    [...dialog.querySelectorAll(".article-edit > .field > label, .article-edit-footer > .field > label")].map((label) => label.textContent),
    ["Title", "Subtitle", "Section", "Body", "Author"],
  );
  assert.equal(within(dialog).getByLabelText("Title").value, "A piece");
  assert.equal(within(dialog).getByLabelText("Subtitle").value, "A compact subtitle");
  assert.equal(within(dialog).getByLabelText("Body").value, "The full body text.");
  assert.ok([...within(dialog).getByLabelText("Section").options].some((option) => option.value === "world"));
  const authorButton = within(dialog).getByLabelText("Author");
  assert.match(authorButton.textContent, /Ada Reporter/);
  assert.ok(authorButton.querySelector("img.article-author-avatar"));
  let saveButton = within(dialog).getByRole("button", { name: "Save article" });
  const restoreButton = within(dialog).getByRole("button", { name: "Restore" });
  assert.equal(saveButton.disabled, true);
  assert.equal(restoreButton.disabled, true);

  await user.clear(within(dialog).getByLabelText("Subtitle"));
  await user.type(within(dialog).getByLabelText("Subtitle"), "Updated deck");
  saveButton = within(dialog).getByRole("button", { name: "Save article" });
  assert.equal(saveButton.disabled, false);
  assert.equal(restoreButton.disabled, false);
  await user.click(saveButton);
  assert.equal(savedPayload, null, "first save click only asks for confirmation");
  assert.ok(within(dialog).getByRole("button", { name: "Confirm" }));

  await user.click(restoreButton);
  assert.equal(within(dialog).getByLabelText("Subtitle").value, "A compact subtitle");
  assert.equal(saveButton.textContent, "Save article");
  assert.equal(saveButton.disabled, true);
  assert.equal(restoreButton.disabled, true);

  await user.clear(within(dialog).getByLabelText("Subtitle"));
  await user.type(within(dialog).getByLabelText("Subtitle"), "Updated deck");
  await user.click(authorButton);
  await user.click(within(dialog).getByRole("option", { name: /Bob Editor/ }));
  await user.click(within(dialog).getByRole("button", { name: "Save article" }));
  await user.click(within(dialog).getByRole("button", { name: "Confirm" }));

  await waitFor(() => assert.equal(savedPayload.author, "bob-editor"));
  assert.equal(savedPayload.metadata.subtitle, "Updated deck");
  assert.equal(savedPayload.published_at, "2026-06-30T00:00:00Z");
  assert.deepEqual(savedPayload.topics, ["world"]);
  await within(dialog).findByText("Article saved.");
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
  await waitFor(() => assert.deepEqual(rows().map((row) => row.dataset.slug), ["ada-text", "ada-youtube"]));

  await user.selectOptions(screen.getByLabelText("Filter by author"), "");
  await waitFor(() => assert.deepEqual(rows().map((row) => row.dataset.slug), ["ada-text", "bob-image", "ada-youtube"]));

  await user.click(screen.getByLabelText("Image"));
  assert.deepEqual(rows().map((row) => row.dataset.slug), ["bob-image"]);

  await user.type(screen.getByLabelText("Search authors"), "bob");
  await waitFor(() => assert.equal([...screen.getByLabelText("Filter by author").options].some((option) => option.textContent === "Ada Reporter"), false));
  assert.equal(screen.getByLabelText("Filter by author").value, "");
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

  assert.deepEqual(rows().map((row) => row.dataset.slug), ["a", "b"]);
  assert.deepEqual(rows().map((row) => row.dataset.layout), ["1", "2"]);
  assert.ok(within(rows()[0]).getByLabelText("Main article"));
  assert.ok(within(rows()[1]).getByLabelText("Double card"));

  await user.click(screen.getByLabelText("Next day"));
  await waitFor(() => assert.deepEqual(rows().map((row) => row.dataset.slug), ["c"]));

  await user.click(screen.getByLabelText("Back"));
  await waitFor(() => assert.deepEqual(rows().map((row) => row.dataset.slug), ["a", "b"]));
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
  assert.equal(screen.getByLabelText("Filter by day").selectedOptions[0].textContent, "29/06/26");

  await user.type(screen.getByLabelText("Search days"), "28");

  await waitFor(() => assert.equal(screen.getByLabelText("Filter by day").value, "2026-06-29"));
  assert.deepEqual(rows().map((row) => row.dataset.slug), ["second-day"]);
  await user.selectOptions(screen.getByLabelText("Filter by day"), "2026-06-28");
  await waitFor(() => assert.deepEqual(rows().map((row) => row.dataset.slug), ["first-day"]));
});

test("searches articles by title or subtitle across all dates on enter", async () => {
  server.use(
    ...panelHandlers({
      articles: [
        listItem({
          slug: "latest-unrelated",
          title: "Latest unrelated",
          published_at: "2026-07-02T09:00:00Z",
          metadata: { subtitle: "No matching deck" },
          body: "mercury appears only in the body and should not match the title search",
        }),
        listItem({
          slug: "title-hit",
          title: "Mercury report",
          published_at: "2026-07-01T09:00:00Z",
          metadata: { subtitle: "A regular deck" },
        }),
        listItem({
          slug: "subtitle-hit",
          title: "Quiet title",
          published_at: "2026-06-30T09:00:00Z",
          metadata: { subtitle: "Mercury in the deck" },
        }),
      ],
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();
  await screen.findByText("Latest unrelated");
  assert.deepEqual(rows().map((row) => row.dataset.slug), ["latest-unrelated"]);

  await user.type(screen.getByLabelText("Search articles"), "mercury{Enter}");

  await waitFor(() => assert.deepEqual(rows().map((row) => row.dataset.slug), ["title-hit", "subtitle-hit"]));
  assert.equal(screen.getByLabelText("Filter by day").disabled, true);
  assert.equal(screen.getByLabelText("Filter by day").selectedOptions[0].textContent, "Search results");
  assert.equal(screen.queryByText("Latest unrelated"), null);
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
