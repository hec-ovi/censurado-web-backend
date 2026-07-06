import { el, clear, help, isSafeImageSrc } from "./el.js";
import { SearchIcon } from "./icons.js";
import { t } from "./i18n.js";

const PORTADA_HELP =
  "A portada is one day's front-page plan. Pick a day, then order its articles (the first row is the day's lead), " +
  "and mark any as important. Saving writes just that day; the articles themselves are untouched.";

export function PortadaPanel({ api } = {}) {
  let groups = new Map();
  let plansByDate = new Map();
  let allArticles = [];
  let dayQuery = "";
  let selectedDate = null;
  let order = [];
  const roles = new Map();
  let dragSlug = null;

  const daySelect = el("select", { id: "portada-day", "aria-label": t("Day"), onChange: (e) => selectDay(e.target.value) });
  const daySearch = el("input", { type: "search", id: "portada-day-search", "aria-label": t("Search days") });
  const prevDay = el("button", { type: "button", class: "secondary article-day-nav portada-day-nav", "aria-label": t("Back") }, "<");
  const nextDay = el("button", { type: "button", class: "secondary article-day-nav portada-day-nav", "aria-label": t("Next day") }, ">");
  const listEl = el("div", { class: "portada-list scroll-pane" });
  const save = el("button", { type: "submit" }, t("Guardar portada"));
  const status = el("p", { class: "form-status", role: "status", "aria-live": "polite" });

  daySearch.addEventListener("input", () => {
    dayQuery = daySearch.value.trim();
    renderDayChoices();
  });
  prevDay.addEventListener("click", () => moveDay(-1));
  nextDay.addEventListener("click", () => moveDay(1));

  const form = el("form", { class: "portada-form workspace-form" }, [
    el("div", { class: "article-filter-bar portada-filter-bar" }, [
      el("fieldset", { class: "article-filter-group portada-filter-group" }, [
        el("legend", {}, t("Days")),
        el("div", { class: "article-day-filter" }, [
          el("label", { class: "article-day-search", for: "portada-day-search" }, [
            SearchIcon("article-search-icon"),
            daySearch,
          ]),
          el("div", { class: "article-day-pager portada-day-pager" }, [prevDay, daySelect, nextDay]),
        ]),
      ]),
      el("div", { class: "article-filter-actions portada-filter-actions" }, [save]),
    ]),
    listEl,
    status,
  ]);
  form.addEventListener("submit", onSave);

  const element = el("div", { class: "portada workspace-section" }, el("section", { class: "panel panel-fill portada-panel" }, [
    el("div", { class: "panel-head" }, [el("h2", {}, t("Portada")), help(t(PORTADA_HELP))]),
    form,
  ]));

  function groupByDay(articles) {
    const map = new Map();
    for (const article of articles) {
      const day = String(article.published_at || "").slice(0, 10);
      if (day.length !== 10) continue;
      if (!map.has(day)) map.set(day, []);
      map.get(day).push(article);
    }
    return map;
  }

  function sortedDays() {
    return [...groups.keys()].sort().reverse();
  }

  function selectDay(date) {
    selectedDate = date;
    order = [];
    roles.clear();

    const dayArticles = groups.get(date) || [];
    const bySlug = new Map(dayArticles.map((a) => [a.slug, a]));
    const plan = plansByDate.get(date);
    const placed = new Set();

    if (plan) {
      for (const entry of plan.entries || []) {
        const article = bySlug.get(entry.slug);
        if (!article || placed.has(article.slug)) continue;
        placed.add(article.slug);
        order.push(article);
        roles.set(article.slug, entry.role === "important" ? "important" : "");
      }
    }
    for (const article of dayArticles) {
      if (placed.has(article.slug)) continue;
      order.push(article);
      if (!roles.has(article.slug)) roles.set(article.slug, "");
    }
    renderList();
  }

  function renderList() {
    clear(listEl);
    listEl.classList.remove("dragging");
    if (!order.length) {
      listEl.append(el("p", { class: "muted empty-state" }, t("No articles yet.")));
      return;
    }
    order.forEach((article, i) => {
      const isLead = i === 0;
      const up = el("button", { type: "button", class: "portada-move-button", "aria-label": t("Move up") }, "↑");
      up.disabled = i === 0;
      up.addEventListener("click", () => move(i, i - 1));

      const down = el("button", { type: "button", class: "portada-move-button", "aria-label": t("Move down") }, "↓");
      down.disabled = i === order.length - 1;
      down.addEventListener("click", () => move(i, i + 1));

      const role = el(
        "select",
        { class: "portada-role", "aria-label": t("Role for {title}", { title: article.title }) },
        [el("option", { value: "" }, t("Single card")), el("option", { value: "important" }, t("Double card"))],
      );
      role.value = roles.get(article.slug) || "";
      role.addEventListener("change", () => roles.set(article.slug, role.value));

      // Native drag-and-drop: a dedicated grip starts the drag; the up/down
      // buttons and the size select stay as the keyboard/touch/accessible path.
      // Dropping a card on another card's left or right zone reorders it (half
      // width); dropping on the full-row band marks it important so it spans the
      // row. Reorder happens only on drop, so renderList never fights the drag.
      const handle = el(
        "div",
        { class: "portada-drag-handle", draggable: "true", "aria-hidden": "true", title: t("Drag to reorder") },
        "⠿",
      );

      const dropzones = el("div", { class: "portada-dropzones", "aria-hidden": "true" }, [
        dropZone("before", article.slug, "before", "", t("Place before {title}", { title: article.title })),
        dropZone("after", article.slug, "after", "", t("Place after {title}", { title: article.title })),
        dropZone("full", article.slug, "before", "important", t("Make {title} a full row", { title: article.title })),
      ]);

      const row = el(
        "div",
        {
          class: "portada-row portada-card-row",
          dataset: {
            slug: article.slug,
            lead: isLead ? "true" : "false",
            role: roles.get(article.slug) || "",
          },
        },
        [
          miniPreview(article, isLead),
          el("div", { class: "portada-order-overlay" }, [
            el("span", { class: "portada-index" }, isLead ? t("Portada") : String(i + 1)),
            el("div", { class: "portada-move" }, [handle, up, down]),
          ]),
          el("div", { class: "portada-role-wrap" }, [el("span", { class: "field-label" }, t("Card size")), role]),
          dropzones,
        ],
      );

      handle.addEventListener("dragstart", (e) => {
        dragSlug = article.slug;
        if (e.dataTransfer) {
          e.dataTransfer.effectAllowed = "move";
          e.dataTransfer.setData("text/plain", article.slug);
          try {
            e.dataTransfer.setDragImage(row, 0, 0);
          } catch {
            /* jsdom and a few browsers reject setDragImage; the drag still works. */
          }
        }
        row.classList.add("dragging");
        listEl.classList.add("dragging");
      });
      handle.addEventListener("dragend", finishDrag);

      listEl.append(row);
    });
  }

  // One drop target inside a card. kind drives the visual zone; position/role
  // say where the dragged card lands and whether it becomes a full-row card.
  function dropZone(kind, targetSlug, position, role, label) {
    const children = kind === "full" ? [el("span", { class: "portada-drop-label" }, t("Full row"))] : [];
    const zone = el("div", { class: `portada-drop portada-drop-${kind}`, "aria-hidden": "true", title: label }, children);
    zone.addEventListener("dragover", (e) => {
      if (dragSlug == null) return;
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
      zone.classList.add("over");
    });
    zone.addEventListener("dragleave", () => zone.classList.remove("over"));
    zone.addEventListener("drop", (e) => {
      e.preventDefault();
      zone.classList.remove("over");
      dropReorder(targetSlug, position, role);
    });
    return zone;
  }

  function finishDrag() {
    dragSlug = null;
    listEl.classList.remove("dragging");
  }

  function move(from, to) {
    if (to < 0 || to >= order.length) return;
    const [item] = order.splice(from, 1);
    order.splice(to, 0, item);
    renderList();
  }

  // Drop the dragged card next to (or in place of) the target and set its role.
  // Works on slugs so the array splice never depends on stale indices.
  function dropReorder(targetSlug, position, role) {
    if (dragSlug == null) return;
    const fromSlug = dragSlug;
    finishDrag();
    if (fromSlug === targetSlug) {
      if ((roles.get(fromSlug) || "") !== role) {
        roles.set(fromSlug, role);
        renderList();
      }
      return;
    }
    const from = order.findIndex((a) => a.slug === fromSlug);
    if (from < 0) return;
    const [item] = order.splice(from, 1);
    let to = order.findIndex((a) => a.slug === targetSlug);
    if (to < 0) to = order.length;
    if (position === "after") to += 1;
    order.splice(to, 0, item);
    roles.set(fromSlug, role);
    renderList();
  }

  function render() {
    const days = sortedDays();
    if (!days.length) {
      clear(daySelect);
      daySelect.append(el("option", { value: "" }, t("No articles yet.")));
      daySelect.disabled = true;
      prevDay.disabled = true;
      nextDay.disabled = true;
      save.disabled = true;
      selectedDate = null;
      order = [];
      roles.clear();
      clear(listEl);
      listEl.append(el("p", { class: "muted empty-state" }, t("No articles yet.")));
      return;
    }
    daySelect.disabled = false;
    save.disabled = false;
    const keep = selectedDate && days.includes(selectedDate) ? selectedDate : days[0];
    selectedDate = keep;
    renderDayChoices();
    selectDay(keep);
  }

  function renderDayChoices() {
    const days = sortedDays();
    const query = dayQuery.toLowerCase();
    let visible = query ? days.filter((day) => day.toLowerCase().includes(query)) : days;
    if (selectedDate && !visible.includes(selectedDate) && days.includes(selectedDate)) {
      visible = [selectedDate, ...visible];
    }
    clear(daySelect);
    if (!visible.length) {
      daySelect.append(el("option", { value: "" }, t("No days match.")));
      return;
    }
    for (const day of visible) daySelect.append(el("option", { value: day }, day));
    daySelect.value = visible.includes(selectedDate) ? selectedDate : visible[0];
    updateDayButtons();
  }

  function moveDay(delta) {
    const days = sortedDays();
    const index = days.indexOf(selectedDate);
    if (index < 0) return;
    const nextIndex = index + delta;
    if (nextIndex < 0 || nextIndex >= days.length) return;
    selectedDate = days[nextIndex];
    daySelect.value = selectedDate;
    renderDayChoices();
    selectDay(selectedDate);
  }

  function updateDayButtons() {
    const days = sortedDays();
    const index = days.indexOf(selectedDate);
    prevDay.disabled = index <= 0;
    nextDay.disabled = index < 0 || index >= days.length - 1;
  }

  async function reload() {
    clear(listEl);
    listEl.append(el("p", { class: "muted" }, t("Loading articles...")));
    try {
      const [articlesData, portadasData] = await Promise.all([
        api.listArticles({ order: "newest", limit: 500 }),
        api.getPortadas(),
      ]);
      const articles = (articlesData && articlesData.articles) || [];
      const portadas = (portadasData && portadasData.portadas) || [];
      allArticles = articles;
      groups = groupByDay(articles);
      plansByDate = new Map();
      for (const plan of portadas) {
        if (plan && plan.date && !plan.deleted) plansByDate.set(plan.date, plan);
      }
      render();
    } catch (err) {
      clear(listEl);
      listEl.append(el("p", { class: "error", role: "alert" }, t("Could not load portadas: {msg}", { msg: err.message })));
    }
  }

  async function onSave(event) {
    event.preventDefault();
    if (!selectedDate) return;
    const entries = order.map((article) => ({ slug: article.slug, role: roles.get(article.slug) || "" }));

    setBusy(save, form, true);
    setStatus(status, "pending", t("Saving..."));
    try {
      await api.upsertPortada({ date: selectedDate, entries });
      setStatus(status, "done", t("Portada guardada."));
      await reload();
    } catch (err) {
      setStatus(status, "error", t("Could not save portada ({code}): {msg}", { code: err.code, msg: err.message }));
    } finally {
      setBusy(save, form, false);
    }
  }

  return { element, reload };
}

function miniPreview(article, isLead) {
  const metadata = article.metadata || {};
  const image = metadata.image;
  const hasImage = isSafeImageSrc(image);
  return el("div", {
    class: "portada-preview",
    dataset: { lead: isLead ? "true" : "false", section: article.section || "", media: hasImage ? "true" : "false" },
  }, [
    hasImage ? el("figure", { class: "portada-preview-media" }, el("img", { src: image, alt: article.title || "" })) : null,
    el("div", { class: "portada-preview-body" }, [
      el("span", { class: "portada-preview-section" }, article.section || ""),
      el("h3", { class: "portada-title" }, el("span", { class: "portada-title-text" }, article.title || "")),
      metadata.subtitle ? el("p", { class: "portada-preview-subtitle" }, metadata.subtitle) : null,
    ]),
  ]);
}

function setBusy(button, formEl, busy) {
  button.disabled = busy;
  formEl.setAttribute("aria-busy", busy ? "true" : "false");
}

function setStatus(node, state, text) {
  node.dataset.state = state;
  node.textContent = text;
  const assertive = state === "error";
  node.setAttribute("role", assertive ? "alert" : "status");
  node.setAttribute("aria-live", assertive ? "assertive" : "polite");
}
