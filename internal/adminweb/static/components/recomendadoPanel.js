import { el, clear } from "./el.js";
import { SearchIcon, TrashIcon } from "./icons.js";
import { t, sectionLabelFor } from "./i18n.js";

const RECOMMENDED_LIMIT = 10;

export function RecomendadoPanel({ api } = {}) {
  let selected = [];
  let candidates = [];
  let dirty = false;
  const filters = { author: "", date: "", query: "" };

  const selectedList = el("div", { class: "recomendado-selected-list scroll-pane" });
  const inactiveList = el("div", { class: "recomendado-inactive-list scroll-pane" });
  const authorFilter = el("select", { id: "recomendado-author", "aria-label": t("Filter by author") });
  const dateFilter = el("select", { id: "recomendado-date", "aria-label": t("Filter by day") });
  const queryInput = el("input", { type: "search", id: "recomendado-query", "aria-label": t("Search title and description") });
  const save = el("button", { type: "button", disabled: true }, t("Save"));
  const status = el("p", { class: "form-status", role: "status", "aria-live": "polite" });

  authorFilter.addEventListener("change", () => {
    filters.author = authorFilter.value;
    renderInactive();
  });
  dateFilter.addEventListener("change", () => {
    filters.date = dateFilter.value;
    renderInactive();
  });
  queryInput.addEventListener("input", () => {
    filters.query = queryInput.value.trim();
    renderInactive();
  });
  save.addEventListener("click", onSave);

  const element = el("div", { class: "recomendado workspace-section" }, el("section", { class: "panel panel-fill recomendado-panel" }, [
    el("div", { class: "recomendado-shell" }, [
      el("section", { class: "recomendado-list-pane recomendado-list-pane-selected" }, [
        el("div", { class: "recomendado-pane-head" }, [
          el("h2", {}, t("Recomendado")),
          el("span", { class: "recommended-note" }, t("Up to {n} articles.", { n: RECOMMENDED_LIMIT })),
        ]),
        selectedList,
      ]),
      el("section", { class: "recomendado-list-pane recomendado-list-pane-inactive" }, [
        el("div", { class: "article-filter-bar recomendado-filter-bar" }, [
          el("fieldset", { class: "article-filter-group recomendado-filter-group" }, [
            el("legend", {}, t("Author")),
            authorFilter,
          ]),
          el("fieldset", { class: "article-filter-group recomendado-filter-group" }, [
            el("legend", {}, t("Days")),
            dateFilter,
          ]),
          el("fieldset", { class: "article-filter-group recomendado-filter-group recomendado-filter-group-search" }, [
            el("legend", {}, t("Article")),
            el("label", { class: "article-title-search", for: "recomendado-query" }, [
              SearchIcon("article-search-icon"),
              queryInput,
            ]),
          ]),
          el("div", { class: "article-filter-actions recomendado-save-actions" }, [save]),
        ]),
        inactiveList,
      ]),
    ]),
    status,
  ]));

  async function reload() {
    renderLoading();
    try {
      const [recData, articleData] = await Promise.all([
        api.getRecomendado(),
        api.listArticles({ order: "newest", limit: 500 }),
      ]);
      const slugs = Array.isArray(recData && recData.slugs) ? recData.slugs : [];
      candidates = ((articleData && articleData.articles) || []).filter((article) => article && !article.deleted);
      selected = await Promise.all(slugs.map(resolveSelected));
      dirty = false;
      renderFilters();
      renderSelected();
      renderInactive();
      updateSave();
      clearStatus(status);
    } catch (err) {
      clear(selectedList);
      clear(inactiveList);
      selectedList.append(el("p", { class: "error", role: "alert" }, t("Could not load recomendado: {msg}", { msg: err.message })));
    }
  }

  async function resolveSelected(slug) {
    try {
      const article = await api.getArticle(slug);
      return { slug, article, removed: !article || !!article.deleted };
    } catch (err) {
      if (err.status === 404) return { slug, missing: true };
      return { slug, missing: true, error: err.message };
    }
  }

  function renderLoading() {
    clear(selectedList);
    clear(inactiveList);
    selectedList.append(el("p", { class: "muted" }, t("Loading recomendado...")));
    inactiveList.append(el("p", { class: "muted" }, t("Loading articles...")));
  }

  function renderFilters() {
    fillSelect(authorFilter, t("All authors"), unique(candidates.map((article) => article.author).filter(Boolean)));
    fillSelect(dateFilter, t("All days"), unique(candidates.map(articleDate).filter(Boolean)));
    authorFilter.value = filters.author;
    dateFilter.value = filters.date;
  }

  function fillSelect(select, allLabel, values) {
    clear(select);
    select.append(el("option", { value: "" }, allLabel));
    for (const value of values) select.append(el("option", { value }, value));
  }

  function renderSelected() {
    clear(selectedList);
    if (!selected.length) {
      selectedList.append(el("p", { class: "muted empty-state" }, t("No recommended articles selected.")));
      return;
    }
    selected.forEach((item, index) => {
      const up = el("button", { type: "button", class: "portada-move-button", "aria-label": t("Move up") }, "↑");
      up.disabled = index === 0;
      up.addEventListener("click", () => moveSelected(index, index - 1));
      const down = el("button", { type: "button", class: "portada-move-button", "aria-label": t("Move down") }, "↓");
      down.disabled = index === selected.length - 1;
      down.addEventListener("click", () => moveSelected(index, index + 1));
      const remove = el("button", { type: "button", class: "source-trash", "aria-label": t("Delete") }, TrashIcon("article-trash-icon"));
      remove.addEventListener("click", () => removeSelected(index));
      selectedList.append(recommendationRow(item, index, [up, down, remove], true));
    });
  }

  function renderInactive() {
    clear(inactiveList);
    const selectedSlugs = new Set(selected.map((item) => item.slug));
    const visible = candidates
      .filter((article) => !selectedSlugs.has(article.slug))
      .filter(matchesFilters)
      .sort((a, b) => String(b.published_at || "").localeCompare(String(a.published_at || "")));
    if (!visible.length) {
      inactiveList.append(el("p", { class: "muted empty-state" }, t("No articles match.")));
      return;
    }
    const full = selected.length >= RECOMMENDED_LIMIT;
    for (const article of visible) {
      const add = el("button", { type: "button", class: "secondary recomendado-add" }, t("Add"));
      add.disabled = full;
      add.addEventListener("click", () => addSelected(article));
      inactiveList.append(recommendationRow({ slug: article.slug, article }, null, [add], false));
    }
  }

  function recommendationRow(item, index, actions, active) {
    const article = item.article || {};
    const removed = item.missing || item.removed;
    const title = removed ? item.slug : (article.title || item.slug);
    const meta = article.metadata || {};
    const subtitle = removed ? t("Removed / unresolved") : (meta.subtitle || meta.description || "");
    return el("article", { class: "recomendado-row", dataset: { active: active ? "true" : "false", slug: item.slug } }, [
      el("div", { class: "recommended-index" }, active && index != null ? String(index + 1) : "+"),
      el("div", { class: "recomendado-copy" }, [
        el("div", { class: "recomendado-meta" }, [
          article.section ? el("span", { class: "portada-preview-section" }, sectionLabelFor(article.section)) : null,
          article.published_at ? el("span", {}, articleDate(article)) : null,
          article.card_type ? el("span", {}, article.card_type) : null,
        ]),
        el("strong", {}, title),
        subtitle ? el("small", {}, subtitle) : null,
      ]),
      el("div", { class: "recomendado-actions" }, actions),
    ]);
  }

  function matchesFilters(article) {
    if (filters.author && article.author !== filters.author) return false;
    if (filters.date && articleDate(article) !== filters.date) return false;
    if (!filters.query) return true;
    const meta = article.metadata || {};
    const haystack = [article.title, meta.subtitle, meta.description].filter(Boolean).join(" ").toLowerCase();
    return haystack.includes(filters.query.toLowerCase());
  }

  function addSelected(article) {
    if (selected.length >= RECOMMENDED_LIMIT) return;
    selected.push({ slug: article.slug, article });
    markDirty();
  }

  function removeSelected(index) {
    selected.splice(index, 1);
    markDirty();
  }

  function moveSelected(from, to) {
    if (to < 0 || to >= selected.length) return;
    const [item] = selected.splice(from, 1);
    selected.splice(to, 0, item);
    markDirty();
  }

  function markDirty() {
    dirty = true;
    renderSelected();
    renderInactive();
    updateSave();
    clearStatus(status);
  }

  async function onSave() {
    save.disabled = true;
    setStatus(status, "pending", t("Saving..."));
    try {
      await api.putRecomendado(selected.map((item) => item.slug));
      dirty = false;
      await reload();
      setStatus(status, "done", t("Recomendado saved."));
    } catch (err) {
      setStatus(status, "error", t("Could not save recomendado ({code}): {msg}", { code: err.code, msg: err.message }));
      updateSave();
    }
  }

  function updateSave() {
    save.disabled = !dirty;
  }

  return { element, reload };
}

function articleDate(article) {
  return String(article.published_at || "").slice(0, 10);
}

function unique(values) {
  return [...new Set(values)].sort((a, b) => a.localeCompare(b));
}

function setStatus(node, state, text) {
  node.dataset.state = state;
  node.textContent = text;
  const assertive = state === "error";
  node.setAttribute("role", assertive ? "alert" : "status");
  node.setAttribute("aria-live", assertive ? "assertive" : "polite");
}

function clearStatus(node) {
  delete node.dataset.state;
  node.textContent = "";
  node.setAttribute("role", "status");
  node.setAttribute("aria-live", "polite");
}
