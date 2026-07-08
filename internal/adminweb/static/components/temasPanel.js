import { el, clear, help } from "./el.js";
import { t, sectionLabelFor } from "./i18n.js";
import { slugify } from "../slugify.js";

const TEMAS_HELP =
  "Topics are derived from the published article corpus, not a separate store. Filter or order them to inspect the tag surface.";

export function TemasPanel({ api } = {}) {
  let articles = [];

  const search = el("input", { type: "search", id: "temas-search", placeholder: t("Filter topics") });
  const authorFilter = el("select", { id: "temas-author" }, el("option", { value: "" }, t("All authors")));
  const sectionFilter = el("select", { id: "temas-section" }, el("option", { value: "" }, t("All sections")));
  const sortSelect = el("select", { id: "temas-sort" }, [
    el("option", { value: "count" }, t("Most used")),
    el("option", { value: "latest" }, t("Latest first")),
    el("option", { value: "label" }, t("Alphabetical")),
  ]);
  const listEl = el("div", { class: "temas-list scroll-pane" });

  const controls = el("div", { class: "filter-grid" }, [
    filterField(t("Search"), search, "temas-search"),
    filterField(t("Author"), authorFilter, "temas-author"),
    filterField(t("Section"), sectionFilter, "temas-section"),
    filterField(t("Order by"), sortSelect, "temas-sort"),
  ]);

  const element = el("section", { class: "panel panel-fill temas" }, [
    el("div", { class: "panel-head" }, [el("h2", {}, t("Topics")), help(t(TEMAS_HELP))]),
    controls,
    listEl,
  ]);

  [search, authorFilter, sectionFilter, sortSelect].forEach((control) => {
    control.addEventListener("input", render);
    control.addEventListener("change", render);
  });

  function aggregate(filteredArticles) {
    const facets = new Map();
    for (const article of filteredArticles) {
      const seen = new Set();
      for (const topic of article.topics || []) {
        const slug = slugify(topic);
        if (!slug || seen.has(slug)) continue;
        seen.add(slug);
        if (!facets.has(slug)) {
          facets.set(slug, {
            slug,
            label: topic,
            count: 0,
            authors: new Set(),
            sections: new Set(),
            latest: "",
            articles: [],
          });
        }
        const facet = facets.get(slug);
        facet.count += 1;
        if (article.author) facet.authors.add(article.author);
        if (article.section) facet.sections.add(article.section);
        const published = String(article.published_at || "");
        if (published > facet.latest) facet.latest = published;
        facet.articles.push(article);
      }
    }
    return [...facets.values()].sort((a, b) => {
      if (sortSelect.value === "latest") return String(b.latest).localeCompare(String(a.latest)) || b.count - a.count;
      if (sortSelect.value === "label") return a.label.localeCompare(b.label);
      return b.count - a.count || a.slug.localeCompare(b.slug);
    });
  }

  function filteredArticles() {
    const q = search.value.trim().toLowerCase();
    const author = authorFilter.value;
    const section = sectionFilter.value;
    return articles.filter((article) => {
      if (author && article.author !== author) return false;
      if (section && article.section !== section) return false;
      if (!q) return true;
      const haystack = [
        article.title,
        article.author,
        article.section,
        ...(article.topics || []),
      ].join(" ").toLowerCase();
      return haystack.includes(q);
    });
  }

  function row(facet) {
    const latestTitles = facet.articles.slice(0, 3).map((article) => article.title).filter(Boolean);
    return el("article", { class: "tema-row", dataset: { slug: facet.slug } }, [
      el("div", { class: "tema-main" }, [
        el("div", { class: "tema-title-row" }, [
          el("span", { class: "topic-chip tema-topic-bullet" }, facet.label),
          el("div", { class: "tema-metrics" }, [
            stat(t("Articles"), String(facet.count), "primary"),
            stat(t("Authors"), String(facet.authors.size)),
            stat(t("Sections"), String(facet.sections.size)),
          ]),
        ]),
        latestTitles.length
          ? el("p", { class: "tema-sample" }, latestTitles.join(" · "))
          : null,
      ]),
      el("div", { class: "topic-chips" }, [
        ...[...facet.sections].slice(0, 4).map((section) => el("span", { class: "topic-chip" }, sectionLabelFor(section))),
        ...[...facet.authors].slice(0, 4).map((author) => el("span", { class: "author-chip" }, author)),
      ]),
    ]);
  }

  function render() {
    const facets = aggregate(filteredArticles());
    clear(listEl);
    if (!facets.length) {
      listEl.append(el("p", { class: "muted empty-state" }, t("No topics yet.")));
      return;
    }
    for (const facet of facets) listEl.append(row(facet));
  }

  async function reload() {
    clear(listEl);
    listEl.append(el("p", { class: "muted" }, t("Loading topics...")));
    try {
      const data = await api.listArticles({ limit: 500 });
      articles = (data && data.articles) || [];
      populateFilters();
      render();
    } catch (err) {
      clear(listEl);
      listEl.append(el("p", { class: "error", role: "alert" }, t("Could not load topics: {msg}", { msg: err.message })));
    }
  }

  function populateFilters() {
    const currentAuthor = authorFilter.value;
    const currentSection = sectionFilter.value;
    const authors = [...new Set(articles.map((article) => article.author).filter(Boolean))].sort();
    const sections = [...new Set(articles.map((article) => article.section).filter(Boolean))].sort();

    clear(authorFilter);
    authorFilter.append(el("option", { value: "" }, t("All authors")));
    authors.forEach((author) => authorFilter.append(el("option", { value: author }, author)));
    authorFilter.value = authors.includes(currentAuthor) ? currentAuthor : "";

    clear(sectionFilter);
    sectionFilter.append(el("option", { value: "" }, t("All sections")));
    sections.forEach((section) => sectionFilter.append(el("option", { value: section }, sectionLabelFor(section))));
    sectionFilter.value = sections.includes(currentSection) ? currentSection : "";
  }

  return { element, reload };
}

function filterField(labelText, control, id) {
  return el("div", { class: "field field-compact" }, [el("label", { for: id }, labelText), control]);
}

function stat(label, value, tone = "") {
  return el("div", { class: "tema-stat", dataset: tone ? { tone } : {} }, [
    el("span", { class: "tema-stat-value" }, value),
    el("span", { class: "tema-stat-label" }, label),
  ]);
}
