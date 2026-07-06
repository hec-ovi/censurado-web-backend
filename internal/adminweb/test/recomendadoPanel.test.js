import "./setup.js";
import { test } from "node:test";
import assert from "node:assert/strict";
import { http, HttpResponse } from "msw";
import { screen, within, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { installServer, ORIGIN } from "./msw.js";
import { installDom } from "./dom.js";
import { RecomendadoPanel } from "../static/components/recomendadoPanel.js";
import { api } from "../static/api.js";

installDom();
const server = installServer();

function article(overrides = {}) {
  return {
    slug: "alpha",
    title: "Alpha title",
    section: "world",
    author: "Ada",
    published_at: "2026-07-05T10:00:00Z",
    card_type: "text",
    metadata: { subtitle: "Alpha subtitle", description: "Alpha description" },
    deleted: false,
    ...overrides,
  };
}

function mount() {
  const panel = RecomendadoPanel({ api });
  document.body.appendChild(panel.element);
  return panel;
}

test("edits the global recomendado list with selected and inactive article lists", async () => {
  let stored = ["bravo"];
  let putBody = null;
  const articles = [
    article({ slug: "alpha", title: "Alpha title", author: "Ada", published_at: "2026-07-05T10:00:00Z" }),
    article({ slug: "bravo", title: "Bravo title", author: "Berta", published_at: "2026-07-04T10:00:00Z" }),
  ];
  server.use(
    http.get(`${ORIGIN}/recomendado`, () => HttpResponse.json({ slugs: stored })),
    http.get(`${ORIGIN}/articles`, () => HttpResponse.json({ articles, total: articles.length })),
    http.get(`${ORIGIN}/articles/:slug`, ({ params }) => {
      const found = articles.find((item) => item.slug === params.slug);
      return found ? HttpResponse.json(found) : new HttpResponse(null, { status: 404 });
    }),
    http.put(`${ORIGIN}/recomendado`, async ({ request }) => {
      putBody = await request.json();
      stored = putBody.slugs;
      return HttpResponse.json({ slugs: stored });
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  const selected = document.querySelector(".recomendado-selected-list");
  const inactive = document.querySelector(".recomendado-inactive-list");
  assert.ok(within(selected).getByText("Bravo title"));
  assert.ok(within(inactive).getByText("Alpha title"));

  await user.click(within(inactive).getByRole("button", { name: /^add$/i }));
  await user.click(within(selected).getAllByRole("button", { name: "Move up" })[1]);
  await user.click(screen.getByRole("button", { name: /^save$/i }));

  await waitFor(() => assert.deepEqual(putBody.slugs, ["alpha", "bravo"]));
  await screen.findByText("Recomendado saved.");
});

test("filters inactive recomendado candidates by author, date, and title/description text", async () => {
  const articles = [
    article({ slug: "alpha", title: "Alpha title", author: "Ada", published_at: "2026-07-05T10:00:00Z", metadata: { description: "economy analysis" } }),
    article({ slug: "gamma", title: "Gamma title", author: "Berta", published_at: "2026-07-04T10:00:00Z", metadata: { description: "deep culture" } }),
  ];
  server.use(
    http.get(`${ORIGIN}/recomendado`, () => HttpResponse.json({ slugs: [] })),
    http.get(`${ORIGIN}/articles`, () => HttpResponse.json({ articles, total: articles.length })),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  const inactive = document.querySelector(".recomendado-inactive-list");
  assert.ok(within(inactive).getByText("Alpha title"));
  assert.ok(within(inactive).getByText("Gamma title"));

  await user.selectOptions(screen.getByLabelText(/filter by author/i), "Berta");
  assert.equal(within(inactive).queryByText("Alpha title"), null);
  assert.ok(within(inactive).getByText("Gamma title"));

  await user.selectOptions(screen.getByLabelText(/filter by day/i), "2026-07-05");
  await within(inactive).findByText("No articles match.");

  await user.selectOptions(screen.getByLabelText(/filter by day/i), "");
  await user.clear(screen.getByLabelText(/search title and description/i));
  await user.type(screen.getByLabelText(/search title and description/i), "culture");
  assert.equal(within(inactive).queryByText("Alpha title"), null);
  assert.ok(within(inactive).getByText("Gamma title"));
});

test("shows dangling recomendado slugs as removable unresolved entries", async () => {
  let putBody = null;
  server.use(
    http.get(`${ORIGIN}/recomendado`, () => HttpResponse.json({ slugs: ["ghost"] })),
    http.get(`${ORIGIN}/articles`, () => HttpResponse.json({ articles: [], total: 0 })),
    http.get(`${ORIGIN}/articles/ghost`, () => new HttpResponse(null, { status: 404 })),
    http.put(`${ORIGIN}/recomendado`, async ({ request }) => {
      putBody = await request.json();
      return HttpResponse.json({ slugs: putBody.slugs });
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  const selected = document.querySelector(".recomendado-selected-list");
  assert.ok(within(selected).getByText("ghost"));
  assert.ok(within(selected).getByText("Removed / unresolved"));

  await user.click(within(selected).getByRole("button", { name: /^delete$/i }));
  await user.click(screen.getByRole("button", { name: /^save$/i }));

  await waitFor(() => assert.deepEqual(putBody.slugs, []));
});
