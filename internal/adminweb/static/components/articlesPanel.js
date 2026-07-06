import { el, clear, field, isSafeImageSrc, subTabs } from "./el.js";
import { SearchIcon, TrashIcon } from "./icons.js";
import { t } from "./i18n.js";
import { SECTIONS } from "./personaForm.js";

const MEDIA_FILTERS = ["text", "image", "youtube"];

export function ArticlesPanel({ api } = {}) {
  let allArticles = [];
  let authorsByKey = new Map();
  let layoutBySlug = new Map();
  const filters = { author: "", media: new Set(), authorQuery: "", dateSort: "asc", day: "", dayQuery: "" };

  const mediaChecks = el("div", { class: "article-media-checks" });
  const dateChecks = el("div", { class: "article-date-checks" });
  const daySelect = el("select", { id: "article-day-filter", "aria-label": t("Filter by day") });
  const daySearch = el("input", { type: "search", id: "article-day-search", "aria-label": t("Search days") });
  const authorSelect = el("select", { id: "article-author-filter", "aria-label": t("Filter by author") });
  const authorSearch = el("input", { type: "search", id: "article-author-search", "aria-label": t("Search authors") });
  const tableHost = el("div", { class: "article-table-host scroll-pane" });
  const status = el("p", { class: "form-status", role: "status", "aria-live": "polite" });

  const clearFilters = el("button", { type: "button", class: "secondary article-clear-filters" }, t("Clear filters"));
  clearFilters.addEventListener("click", () => {
    filters.author = "";
    filters.media.clear();
    filters.authorQuery = "";
    filters.dateSort = "asc";
    filters.day = "";
    filters.dayQuery = "";
    authorSearch.value = "";
    daySearch.value = "";
    renderFilters();
    renderTable();
  });

  authorSearch.addEventListener("input", () => {
    filters.authorQuery = authorSearch.value.trim();
    renderAuthorChoices();
  });

  authorSelect.addEventListener("change", () => {
    filters.author = authorSelect.value;
    renderTable();
  });

  daySearch.addEventListener("input", () => {
    filters.dayQuery = daySearch.value.trim();
    renderDayChoices();
  });

  daySelect.addEventListener("change", () => {
    filters.day = daySelect.value;
    renderTable();
  });

  const element = el("div", { class: "articles workspace-section" }, [
    el("section", { class: "panel panel-fill articles-panel" }, [
      el("div", { class: "article-filter-bar" }, [
        el("fieldset", { class: "article-filter-group article-filter-group-media" }, [
          el("legend", {}, t("Show")),
          mediaChecks,
        ]),
        el("fieldset", { class: "article-filter-group article-filter-group-date" }, [
          el("legend", {}, t("Date order")),
          dateChecks,
        ]),
        el("fieldset", { class: "article-filter-group article-filter-group-days" }, [
          el("legend", {}, t("Days")),
          el("div", { class: "article-day-filter" }, [
            el("label", { class: "article-day-search", for: "article-day-search" }, [
              SearchIcon("article-search-icon"),
              daySearch,
            ]),
            daySelect,
          ]),
        ]),
        el("fieldset", { class: "article-filter-group article-filter-group-authors" }, [
          el("legend", { class: "article-filter-legend" }, t("Author")),
          el("div", { class: "article-author-filter" }, [
            el("label", { class: "article-author-search", for: "article-author-search" }, [
              SearchIcon("article-search-icon"),
              authorSearch,
            ]),
            authorSelect,
          ]),
        ]),
        el("div", { class: "article-filter-actions" }, [clearFilters]),
      ]),
      tableHost,
      status,
    ]),
  ]);

  async function reload() {
    renderLoading();
    try {
      const [articlesData, portadasData, authorsData] = await Promise.all([
        api.listArticles({ order: "oldest", limit: 500 }),
        api.getPortadas(),
        api.listAuthors(false),
      ]);
      allArticles = ((articlesData && articlesData.articles) || [])
        .filter((article) => !article.deleted)
        .map((article) => ({ ...article }));
      authorsByKey = authorMap((authorsData && authorsData.authors) || []);
      layoutBySlug = layoutMap((portadasData && portadasData.portadas) || []);
      renderFilters();
      renderTable();
    } catch (err) {
      renderError(t("Could not load articles: {msg}", { msg: err.message }));
    }
  }

  function renderLoading() {
    clear(tableHost);
    tableHost.append(el("p", { class: "muted" }, t("Loading articles...")));
  }

  function renderError(message) {
    clear(tableHost);
    tableHost.append(el("p", { class: "error", role: "alert" }, message));
  }

  function renderFilters() {
    const authors = uniq(allArticles.map((article) => article.author).filter(Boolean));

    if (filters.author && !authors.includes(filters.author)) filters.author = "";

    renderMediaChoices();
    renderDateChoices();
    renderDayChoices();
    renderAuthorChoices();
  }

  function renderMediaChoices() {
    clear(mediaChecks);
    MEDIA_FILTERS.forEach((kind) => {
      const id = `article-media-${kind}`;
      const input = el("input", { type: "checkbox", id, value: kind });
      input.checked = filters.media.has(kind);
      input.addEventListener("change", () => {
        if (input.checked) filters.media.add(kind);
        else filters.media.delete(kind);
        renderTable();
      });
      mediaChecks.append(el("label", { class: "article-filter-choice", for: id }, [input, el("span", {}, mediaLabel(kind))]));
    });
  }

  function renderDateChoices() {
    clear(dateChecks);
    [
      ["asc", t("Ascending")],
      ["desc", t("Descending")],
    ].forEach(([value, label]) => {
      const id = `article-date-${value}`;
      const input = el("input", { type: "radio", id, name: "article-date-order", value });
      input.checked = filters.dateSort === value;
      input.addEventListener("change", () => {
        filters.dateSort = value;
        renderTable();
      });
      dateChecks.append(el("label", { class: "article-filter-choice", for: id }, [input, el("span", {}, label)]));
    });
  }

  function renderDayChoices() {
    const days = uniq(allArticles.map((article) => layoutInfo(article).day || dayOf(article)).filter(Boolean));
    const query = filters.dayQuery.toLowerCase();
    let visible = days.filter((day) => {
      if (!query) return true;
      return `${day} ${formatDayLabel(day)}`.toLowerCase().includes(query);
    });

    if (filters.day && !days.includes(filters.day)) filters.day = "";
    if (filters.day && !visible.includes(filters.day)) visible = [filters.day, ...visible];

    clear(daySelect);
    daySelect.append(el("option", { value: "" }, t("All days")));
    for (const day of visible) {
      daySelect.append(el("option", { value: day }, formatDayLabel(day)));
    }
    daySelect.value = visible.includes(filters.day) ? filters.day : "";
  }

  function renderAuthorChoices() {
    const authors = uniq(allArticles.map((article) => article.author).filter(Boolean));
    const query = filters.authorQuery.toLowerCase();
    const visible = authors.filter((author) => {
      if (!query) return true;
      const profile = authorProfile(author);
      return `${profile.name} ${profile.handle}`.toLowerCase().includes(query);
    });

    clear(authorSelect);
    authorSelect.append(el("option", { value: "" }, t("All authors")));
    if (!authors.length) {
      authorSelect.disabled = true;
      return;
    }
    authorSelect.disabled = false;
    if (!visible.length) {
      authorSelect.append(el("option", { value: "", disabled: true }, t("No authors match.")));
      return;
    }
    if (filters.author && !visible.includes(filters.author)) visible.unshift(filters.author);
    visible.forEach((author) => {
      const profile = authorProfile(author);
      authorSelect.append(el("option", { value: author }, profile.name));
    });
    authorSelect.value = visible.includes(filters.author) ? filters.author : "";
  }

  function renderTable() {
    clear(tableHost);
    const articles = sorted(filtered());
    if (!articles.length) {
      tableHost.append(el("p", { class: "muted empty-state" }, t("No articles yet.")));
      return;
    }

    const tbody = el("tbody");
    let lastDay = "";
    articles.forEach((article, index) => {
      const day = layoutInfo(article).day || dayOf(article);
      if (day && day !== lastDay) {
        tbody.append(
          el("tr", { class: "article-date-separator" }, el("th", { colspan: "7" }, el("time", { datetime: day }, formatDayLabel(day)))),
        );
        lastDay = day;
      }
      tbody.append(row(article, index));
    });

    tableHost.append(
      el("table", { class: "article-table" }, [
        el("thead", {}, el("tr", {}, [
          el("th", { class: "article-media-cell" }, t("Media")),
          el("th", { class: "article-date-cell" }, t("Date")),
          el("th", { class: "article-card-cell" }, t("Card span")),
          el("th", { class: "article-author-cell" }, t("Author")),
          el("th", { class: "article-title-cell" }, t("Title")),
          el("th", { class: "article-subtitle-cell" }, t("Subtitle")),
          el("th", { class: "article-delete-head" }, t("Delete")),
        ])),
        tbody,
      ]),
    );
  }

  function filtered() {
    return allArticles.filter((article) => {
      const day = layoutInfo(article).day || dayOf(article);
      if (filters.day && day !== filters.day) return false;
      if (filters.author && article.author !== filters.author) return false;
      if (filters.media.size && !filters.media.has(articleKind(article))) return false;
      return true;
    });
  }

  function sorted(articles) {
    return [...articles].sort((a, b) => {
      const dayCmp = String(layoutInfo(a).day || dayOf(a)).localeCompare(String(layoutInfo(b).day || dayOf(b)));
      if (dayCmp !== 0) return filters.dateSort === "asc" ? dayCmp : -dayCmp;
      const ai = layoutInfo(a).index ?? Number.MAX_SAFE_INTEGER;
      const bi = layoutInfo(b).index ?? Number.MAX_SAFE_INTEGER;
      if (ai !== bi) return ai - bi;
      const timeCmp = String(a.published_at || "").localeCompare(String(b.published_at || ""));
      return timeCmp;
    });
  }

  function row(article, index) {
    const kind = articleKind(article);
    const layout = layoutInfo(article);
    const profile = authorProfile(article.author || "");
    const meta = article.metadata || {};
    const title = article.title || "";
    const subtitle = meta.subtitle || "";
    const deleteBtn = el("button", { type: "button", class: "article-trash", "aria-label": t("Delete article") }, [
      TrashIcon("article-trash-icon"),
    ]);
    deleteBtn.addEventListener("click", (event) => {
      event.stopPropagation();
      openDeleteDialog(article);
    });

    const tr = el(
      "tr",
      {
        class: "article-row",
        tabindex: "0",
        dataset: {
          slug: article.slug,
          section: article.section || "",
          kind,
          layout: layout.index == null ? "" : String(layout.index),
          role: layout.role || "single",
          parity: index % 2 === 0 ? "even" : "odd",
        },
        onClick: () => openDetail(article),
        onKeydown: (event) => {
          if (event.target !== event.currentTarget) return;
          if (event.key === "Enter" || event.key === " ") {
            event.preventDefault();
            openDetail(article);
          }
        },
      },
      [
        el("td", { class: "article-media-cell" }, mediaPreview(article, kind)),
        el("td", { class: "article-date-cell", title: article.published_at || "" }, formatDateTime(article.published_at)),
        el("td", { class: "article-card-cell" }, cardSpanView(layout)),
        el("td", { class: "article-author-cell", title: profile.name }, authorChip(profile)),
        clippedCell(title, 58, "article-title-cell"),
        clippedCell(subtitle, 54, "article-subtitle-cell"),
        el("td", { class: "article-delete-cell" }, deleteBtn),
      ],
    );
    return tr;
  }

  function clippedCell(text, max, className) {
    return el("td", { class: className, title: text || "" }, text ? truncate(text, max) : "-");
  }

  function mediaPreview(article, kind) {
    const meta = article.metadata || {};
    const card = meta.card || {};
    if (kind === "image") {
      const image = firstText(card.src, meta.image);
      if (isSafeImageSrc(image)) {
        return el("img", { class: "article-media-thumb", src: image, alt: card.alt || meta.image_alt || article.title || "" });
      }
    }
    if (kind === "youtube") {
      const video = articleCardType(article) === "video" ? firstText(card.src, meta.video) : "";
      if (isSafeImageSrc(video)) {
        return el("video", { class: "article-media-thumb", src: video, muted: true, playsinline: true, preload: "metadata" });
      }
      const id = youtubeId(firstText(card.src, meta.youtube, meta.youtube_id));
      if (id) {
        return el("img", { class: "article-media-thumb", src: `https://i.ytimg.com/vi/${id}/mqdefault.jpg`, alt: article.title || "" });
      }
    }
    return el("span", { class: "article-kind", dataset: { kind } }, mediaLabel(kind));
  }

  function layoutInfo(article) {
    return layoutBySlug.get(article.slug) || { day: dayOf(article), index: null, role: "" };
  }

  function authorProfile(author) {
    return authorsByKey.get(author) || { handle: author, name: humanizeAuthor(author), avatar: "" };
  }

  function authorChip(profile) {
    return el("span", { class: "article-author-chip" }, [
      authorAvatar(profile),
      el("span", { class: "article-author-name" }, profile.name),
    ]);
  }

  async function openDetail(article) {
    status.textContent = t("Loading article...");
    try {
      const full = await api.getArticle(article.slug);
      status.textContent = "";
      const dialog = buildDetailDialog(full, status);
      document.body.append(dialog);
      showDialog(dialog);
    } catch (err) {
      status.textContent = t("Could not load article: {msg}", { msg: err.message });
    }
  }

  function openDeleteDialog(article) {
    const dialog = el("dialog", { class: "article-delete-dialog", "aria-label": t("Delete article") });
    const cancel = el("button", { type: "button", class: "secondary" }, t("Cancel"));
    const confirm = el("button", { type: "button", class: "article-delete-confirm" }, t("Delete"));
    cancel.addEventListener("click", () => closeDialog(dialog));
    confirm.addEventListener("click", async () => {
      confirm.disabled = true;
      setStatus(status, "pending", t("Deleting article..."));
      try {
        await api.deleteArticle(article.slug);
        closeDialog(dialog);
        setStatus(status, "done", t("Article deleted."));
        await reload();
      } catch (err) {
        confirm.disabled = false;
        setStatus(status, "error", t("Could not delete article ({code}): {msg}", { code: err.code, msg: err.message }));
      }
    });
    dialog.addEventListener("close", () => dialog.remove());
    dialog.append(
      el("div", { class: "article-delete-shell" }, [
        el("h2", {}, t("Delete article?")),
        el("p", {}, t("This article will be removed from this list. There is no recovery from this screen.")),
        el("div", { class: "article-delete-actions" }, [cancel, confirm]),
      ]),
    );
    document.body.append(dialog);
    showDialog(dialog);
  }

  function buildDetailDialog(full, cardStatus) {
    const close = el("button", { type: "button", class: "secondary article-dialog-close", "aria-label": t("Close") }, "×");
    const dialog = el("dialog", { class: "article-dialog" });
    close.addEventListener("click", () => closeDialog(dialog));
    dialog.addEventListener("cancel", () => closeDialog(dialog));
    dialog.addEventListener("close", () => dialog.remove());
    dialog.append(
      el("div", { class: "article-dialog-shell" }, [
        el("div", { class: "article-dialog-head" }, [
          el("div", {}, [
            el("span", { class: "section-link" }, sectionLabel(full.section)),
            el("h2", {}, full.title || ""),
          ]),
          close,
        ]),
        buildDetail(full, cardStatus),
      ]),
    );
    return dialog;
  }

  function buildDetail(full, cardStatus) {
    const wrap = el("div", { class: "article-detail-wrap" });
    const slug = full.slug;
    const title = el("input", { type: "text", id: `ae-${slug}-title`, value: full.title || "" });
    const author = el("input", { type: "text", id: `ae-${slug}-author`, value: full.author || "" });
    const section = el("select", { id: `ae-${slug}-section` }, SECTIONS.map((s) => el("option", { value: s }, s)));
    section.value = full.section || SECTIONS[0];
    const topics = el("input", { type: "text", id: `ae-${slug}-topics`, value: (full.topics || []).join(", ") });
    const body = el("textarea", { id: `ae-${slug}-body`, rows: "8" }, full.body || "");
    const save = el("button", { type: "submit" }, t("Save article"));

    const editForm = el("form", { class: "article-edit" }, [
      field(t("Title"), title, `ae-${slug}-title`),
      field(t("Author"), author, `ae-${slug}-author`),
      field(t("Section"), section, `ae-${slug}-section`),
      field(t("Topics (comma separated)"), topics, `ae-${slug}-topics`),
      field(t("Body"), body, `ae-${slug}-body`),
      el("div", { class: "article-actions" }, [save]),
    ]);
    editForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const payload = {
        title: title.value.trim(),
        body: body.value,
        author: author.value.trim(),
        section: section.value,
        topics: topics.value.split(/[,\n]/).map((s) => s.trim()).filter(Boolean),
      };
      if (full.metadata && Object.keys(full.metadata).length) payload.metadata = full.metadata;
      save.disabled = true;
      setStatus(cardStatus, "pending", t("Publishing article..."));
      try {
        await api.updateArticle(slug, payload);
        setStatus(cardStatus, "done", t("Article saved."));
        await reload();
      } catch (err) {
        save.disabled = false;
        setStatus(cardStatus, "error", t("Could not save article ({code}): {msg}", { code: err.code, msg: err.message }));
      }
    });

    const previewPane = el("div", { class: "article-preview-pane" }, [
      detailPreview(full),
      el("pre", { class: "article-body" }, full.body || ""),
    ]);
    const detailTabs = subTabs(
      [
        { id: "edit", label: t("Edit"), content: editForm },
        { id: "preview", label: t("Preview"), content: previewPane },
      ],
      { className: "editor-tabs", label: t("Article editor") },
    );
    wrap.append(detailTabs.element);
    return wrap;
  }

  return { element, reload };
}

function detailPreview(article) {
  const metadata = article.metadata || {};
  const image = metadata.image;
  const subtitle = metadata.subtitle || metadata.description || "";
  const hasImage = isSafeImageSrc(image);
  return el("div", { class: "site-preview", dataset: { media: hasImage ? "true" : "false" } }, [
    hasImage ? el("figure", { class: "site-preview-media" }, el("img", { src: image, alt: article.title || "" })) : null,
    el("div", { class: "site-preview-body" }, [
      el("div", { class: "site-preview-kicker" }, [
        el("span", { class: "section-link" }, sectionLabel(article.section)),
        article.published_at ? el("time", { datetime: article.published_at }, dayOf(article)) : null,
      ]),
      el("h3", { class: "site-preview-title" }, article.title || ""),
      subtitle ? el("p", { class: "site-preview-subtitle" }, subtitle) : null,
      el("div", { class: "site-preview-meta" }, [
        el("span", { class: "byline" }, article.author || t("unknown")),
        el("span", { class: "published" }, article.published_at || ""),
      ]),
    ]),
  ]);
}

function articleKind(article) {
  const value = articleCardType(article);
  if (value === "image" || value === "youtube") return value;
  if (value === "video") return "youtube";
  return "text";
}

function articleCardType(article) {
  return String(article.card_type || (article.metadata && article.metadata.card && article.metadata.card.type) || "").toLowerCase();
}

function dayOf(article) {
  return String(article.published_at || "").slice(0, 10);
}

function formatDateTime(value) {
  const text = String(value || "");
  if (!text) return "-";
  const day = text.slice(0, 10);
  const time = text.length >= 16 ? text.slice(11, 16) : "";
  const formattedDay = formatDayLabel(day);
  return time ? `${formattedDay} ${time}` : formattedDay;
}

function formatDayLabel(value) {
  const [year, month, day] = String(value || "").slice(0, 10).split("-");
  if (!year || !month || !day) return value || "-";
  return `${day}/${month}/${year.slice(-2)}`;
}

function mediaLabel(kind) {
  const labels = { text: t("Text"), image: t("Image"), youtube: t("YouTube") };
  return labels[kind] || capitalize(kind);
}

function cardSpanView(layout) {
  if (layout.index == null) return el("span", { class: "article-card-missing" }, "-");
  const full = layout.role === "important";
  const label = full ? t("Full row") : t("Single card");
  if (full) {
    return el("span", { class: "article-card-span", dataset: { role: "full" }, title: label, "aria-label": label }, [
      el("span", { class: "article-card-slot article-card-slot-wide" }),
    ]);
  }
  return el("span", { class: "article-card-span", dataset: { role: "single" }, title: label, "aria-label": label }, [
    el("span", { class: "article-card-slot article-card-slot-used" }),
    el("span", { class: "article-card-slot article-card-slot-empty" }),
  ]);
}

function authorMap(authors) {
  const map = new Map();
  for (const author of authors || []) {
    if (!author || author.deleted) continue;
    const profile = {
      handle: author.handle || "",
      name: author.name || humanizeAuthor(author.handle || ""),
      avatar: author.avatar || "",
    };
    if (profile.handle) map.set(profile.handle, profile);
    if (profile.name) map.set(profile.name, profile);
  }
  return map;
}

function layoutMap(portadas) {
  const map = new Map();
  for (const plan of portadas || []) {
    if (!plan || plan.deleted || !plan.date) continue;
    (plan.entries || []).forEach((entry, index) => {
      if (!entry || !entry.slug || map.has(entry.slug)) return;
      map.set(entry.slug, {
        day: plan.date,
        index: index + 1,
        role: entry.role === "important" ? "important" : "",
      });
    });
  }
  return map;
}

function uniq(values) {
  return [...new Set(values)].sort((a, b) => a.localeCompare(b));
}

function firstText(...values) {
  for (const value of values) {
    if (typeof value === "string" && value.trim() !== "") return value.trim();
  }
  return "";
}

function youtubeId(value) {
  const text = firstText(value);
  if (!text) return "";
  if (/^[a-zA-Z0-9_-]{11}$/.test(text)) return text;
  try {
    const url = new URL(text);
    const v = url.searchParams.get("v");
    if (v && /^[a-zA-Z0-9_-]{11}$/.test(v)) return v;
    const parts = url.pathname.split("/").filter(Boolean);
    const last = parts[parts.length - 1] || "";
    if (/^[a-zA-Z0-9_-]{11}$/.test(last)) return last;
    const embed = parts[0] === "embed" ? parts[1] : "";
    if (embed && /^[a-zA-Z0-9_-]{11}$/.test(embed)) return embed;
  } catch {
    return "";
  }
  return "";
}

function truncate(value, max) {
  const text = String(value || "").replace(/\s+/g, " ").trim();
  if (text.length <= max) return text;
  return text.slice(0, Math.max(0, max - 3)).trimEnd() + "...";
}

function humanizeAuthor(value) {
  const text = String(value || t("unknown")).replace(/[-_]+/g, " ").replace(/\s+/g, " ").trim();
  return text ? text.split(" ").map(capitalize).join(" ") : t("unknown");
}

function authorAvatar(profile) {
  const name = profile.name || profile.handle || t("unknown");
  if (isSafeImageSrc(profile.avatar)) {
    return el("img", { class: "article-author-avatar", src: profile.avatar, alt: "" });
  }
  return el("span", { class: "article-author-avatar", "aria-hidden": "true" }, name.charAt(0).toUpperCase() || "?");
}

function capitalize(value) {
  return value.charAt(0).toUpperCase() + value.slice(1);
}

function sectionLabel(section) {
  const labels = { politics: "Política", economics: "Misterio y conspiración", tech: "Tecnología", world: "Mundo" };
  return labels[section] || section || "CN";
}

function showDialog(dialog) {
  if (typeof dialog.showModal === "function") dialog.showModal();
  else dialog.setAttribute("open", "");
}

function closeDialog(dialog) {
  if (typeof dialog.close === "function") dialog.close();
  else {
    dialog.removeAttribute("open");
    const EventCtor = dialog.ownerDocument.defaultView.Event;
    dialog.dispatchEvent(new EventCtor("close"));
  }
}

function setStatus(node, state, text) {
  node.dataset.state = state;
  node.textContent = text;
  const assertive = state === "error";
  node.setAttribute("role", assertive ? "alert" : "status");
  node.setAttribute("aria-live", assertive ? "assertive" : "polite");
}
