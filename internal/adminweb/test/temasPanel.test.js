import "./setup.js";
import { test } from "node:test";
import assert from "node:assert/strict";
import { http, HttpResponse } from "msw";
import { screen, within } from "@testing-library/dom";
import { installServer, ORIGIN } from "./msw.js";
import { installDom } from "./dom.js";
import { TemasPanel } from "../static/components/temasPanel.js";
import { api } from "../static/api.js";

installDom();
const server = installServer();

function mount() {
  const panel = TemasPanel({ api });
  document.body.appendChild(panel.element);
  return panel;
}

function articlesResponse(articles) {
  return { articles, total: articles.length };
}

test("derives one compact facet row per distinct slug", async () => {
  server.use(
    http.get(`${ORIGIN}/articles`, () =>
      HttpResponse.json(
        articlesResponse([
          { slug: "a", title: "A", section: "world", author: "x", published_at: "", topics: ["Análisis Político", "Café München"], metadata: {} },
          { slug: "b", title: "B", section: "world", author: "y", published_at: "", topics: ["política"], metadata: {} },
          { slug: "c", title: "C", section: "world", author: "z", published_at: "", topics: ["Política"], metadata: {} },
        ]),
      )),
  );
  const panel = mount();
  await panel.reload();

  // "política" (article b) and "Política" (article c) transliterate + lowercase to
  // the same slug "politica", so they collapse into ONE facet whose count is the
  // two carrying articles.
  const politicaChip = await screen.findByText("política");
  const politicaRow = politicaChip.closest(".tema-row");
  assert.equal(politicaRow.dataset.slug, "politica");
  assert.equal(politicaRow.querySelector("a"), null, "topic rows are not public-page links");
  const articleStat = politicaRow.querySelector('.tema-stat[data-tone="primary"]');
  assert.ok(articleStat, "the row includes an article-count stat");
  assert.equal(within(articleStat).getByText("2").textContent, "2");

  // "Análisis Político" slugifies to "analisis-politico" (a distinct slug from
  // "politica"), and "Café München" to "cafe-munchen": accents transliterate.
  assert.ok(document.querySelector('.tema-row[data-slug="analisis-politico"]'));
  assert.ok(document.querySelector('.tema-row[data-slug="cafe-munchen"]'));

  // Exactly three distinct facet rows: politica, analisis-politico, cafe-munchen.
  assert.equal(document.querySelectorAll(".tema-row").length, 3);
});

test("drops topics whose slug is empty", async () => {
  server.use(
    http.get(`${ORIGIN}/articles`, () =>
      HttpResponse.json(
        articlesResponse([
          { slug: "a", title: "A", section: "world", author: "x", published_at: "", topics: ["日本語", "Tech"], metadata: {} },
        ]),
      )),
  );
  const panel = mount();
  await panel.reload();

  await screen.findByText("Tech");
  // "日本語" slugifies to "" and is not rendered as a row.
  assert.equal(document.querySelectorAll(".tema-row").length, 1);
  assert.equal(document.querySelector(".tema-row").dataset.slug, "tech");
});

test("renders a clean zero-state for an empty corpus", async () => {
  server.use(http.get(`${ORIGIN}/articles`, () => HttpResponse.json(articlesResponse([]))));
  const panel = mount();
  await panel.reload();

  await screen.findByText("No topics yet.");
  assert.equal(document.querySelectorAll(".tema-row").length, 0);
});
