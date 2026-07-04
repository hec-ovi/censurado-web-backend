import { el, clear, field, help, isSafeImageSrc, subTabs } from "./el.js";
import { t } from "./i18n.js";

const PORTADA_HELP =
  "A portada is one day's front-page plan. Pick a day, then order its articles (the first row is the day's lead), " +
  "mark any as important, and check the ones that belong in the recomendado list. Saving writes just that day; the " +
  "articles themselves are untouched.";

const RECOMMENDED_LIMIT = 10;

export function PortadaPanel({ api } = {}) {
  let groups = new Map();
  let plansByDate = new Map();
  let allArticles = [];
  let selectedDate = null;
  let order = [];
  const roles = new Map();
  const recomendado = new Set();

  const daySelect = el("select", { id: "portada-day", onChange: (e) => selectDay(e.target.value) });
  const listEl = el("div", { class: "portada-list scroll-pane" });
  const recEl = el("div", { class: "portada-recomendado scroll-pane" });
  const save = el("button", { type: "submit" }, t("Guardar portada"));
  const status = el("p", { class: "form-status", role: "status", "aria-live": "polite" });

  const controls = el("div", { class: "pane-toolbar" }, [field(t("Day"), daySelect, "portada-day")]);
  const form = el("form", { class: "portada-form workspace-form" }, [
    controls,
    listEl,
    el("div", { class: "form-actions" }, [save]),
    status,
  ]);
  form.addEventListener("submit", onSave);

  const tabs = subTabs(
    [
      {
        id: "front",
        label: t("Portada"),
        content: el("section", { class: "panel panel-fill" }, [
          el("div", { class: "panel-head" }, [el("h2", {}, t("Portada")), help(t(PORTADA_HELP))]),
          form,
        ]),
      },
      {
        id: "recommended",
        label: t("Recomendado"),
        content: el("section", { class: "panel panel-fill" }, [
          el("div", { class: "panel-head" }, [
            el("h2", {}, t("Recomendado")),
            el("p", { class: "muted" }, t("Up to {n} articles.", { n: RECOMMENDED_LIMIT })),
          ]),
          recEl,
        ]),
      },
    ],
    { className: "content-tabs", label: t("Portada sections") },
  );

  const element = el("div", { class: "portada workspace-section" }, tabs.element);

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
    recomendado.clear();

    const dayArticles = groups.get(date) || [];
    const bySlug = new Map(dayArticles.map((a) => [a.slug, a]));
    const byAnySlug = new Map(allArticles.map((a) => [a.slug, a]));
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
    if (plan) {
      for (const slug of (plan.recomendado || []).slice(0, RECOMMENDED_LIMIT)) {
        if (byAnySlug.has(slug)) recomendado.add(slug);
      }
    }

    renderList();
    renderRecomendado(allArticles);
  }

  function renderList() {
    clear(listEl);
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
            el("div", { class: "portada-move" }, [up, down]),
          ]),
          el("div", { class: "portada-role-wrap" }, [el("span", { class: "field-label" }, t("Card size")), role]),
        ],
      );
      listEl.append(row);
    });
  }

  function move(from, to) {
    if (to < 0 || to >= order.length) return;
    const [item] = order.splice(from, 1);
    order.splice(to, 0, item);
    renderList();
  }

  function renderRecomendado(candidates) {
    clear(recEl);
    if (!candidates.length) {
      recEl.append(el("p", { class: "muted empty-state" }, t("No articles yet.")));
      return;
    }

    const count = () => recomendado.size;
    const note = el("div", { class: "recommended-note" });
    const updateNote = () => {
      note.textContent = t("{count}/{limit} recommended selected.", { count: count(), limit: RECOMMENDED_LIMIT });
    };
    const updateDisabled = (boxes) => {
      const full = count() >= RECOMMENDED_LIMIT;
      boxes.forEach(({ box }) => {
        box.disabled = full && !box.checked;
      });
    };

    const boxes = [];
    const ordered = [...candidates].sort((a, b) => {
      const ar = recomendado.has(a.slug) ? 0 : 1;
      const br = recomendado.has(b.slug) ? 0 : 1;
      return ar - br || String(b.published_at || "").localeCompare(String(a.published_at || ""));
    });
    const list = el("div", { class: "recommended-list" });
    for (const article of ordered) {
      const id = `portada-rec-${article.slug}`;
      const box = el("input", { type: "checkbox", id });
      box.checked = recomendado.has(article.slug);
      boxes.push({ box });
      box.addEventListener("change", () => {
        if (box.checked) recomendado.add(article.slug);
        else recomendado.delete(article.slug);
        updateNote();
        updateDisabled(boxes);
      });
      list.append(
        el("label", { class: "recommended-choice", for: id, dataset: { slug: article.slug } }, [
          box,
          el(
            "span",
            { class: "recommended-index", "aria-hidden": "true" },
            recomendado.has(article.slug) ? String([...recomendado].indexOf(article.slug) + 1) : "—",
          ),
          el("span", { class: "recommended-copy" }, [
            el("strong", {}, article.title || ""),
            article.metadata && article.metadata.subtitle ? el("small", {}, article.metadata.subtitle) : null,
          ]),
        ]),
      );
    }
    updateNote();
    recEl.append(note, list);
    updateDisabled(boxes);
  }

  function render() {
    const days = sortedDays();
    clear(daySelect);
    if (!days.length) {
      daySelect.append(el("option", { value: "" }, t("No articles yet.")));
      daySelect.disabled = true;
      save.disabled = true;
      selectedDate = null;
      order = [];
      roles.clear();
      recomendado.clear();
      clear(listEl);
      listEl.append(el("p", { class: "muted empty-state" }, t("No articles yet.")));
      clear(recEl);
      recEl.append(el("p", { class: "muted empty-state" }, t("No articles yet.")));
      return;
    }
    daySelect.disabled = false;
    save.disabled = false;
    for (const day of days) daySelect.append(el("option", { value: day }, day));
    const keep = selectedDate && days.includes(selectedDate) ? selectedDate : days[0];
    daySelect.value = keep;
    selectDay(keep);
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
    const rec = allArticles.map((a) => a.slug).filter((slug) => recomendado.has(slug)).slice(0, RECOMMENDED_LIMIT);

    setBusy(save, form, true);
    setStatus(status, "pending", t("Saving..."));
    try {
      await api.upsertPortada({ date: selectedDate, entries, recomendado: rec });
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
