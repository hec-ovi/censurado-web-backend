import { el, clear, field, isSafeImageSrc } from "./el.js";
import { SearchIcon, TrashIcon } from "./icons.js";
import { t } from "./i18n.js";

const MEDIA_FILTERS = ["text", "image", "youtube"];

export function ArticlesPanel({ api } = {}) {
  let allArticles = [];
  let articleDays = [];
  let authorOptions = [];
  let sectionOptions = [];
  let authorsByKey = new Map();
  let layoutBySlug = new Map();
  let articleSearchActive = false;
  const filters = { author: "", media: new Set(), articleQuery: "", authorQuery: "", dateSort: "desc", day: "", dayQuery: "" };

  const mediaChecks = el("div", { class: "article-media-checks" });
  const dateChecks = el("div", { class: "article-date-checks" });
  const prevDay = el("button", { type: "button", class: "secondary article-day-nav", "aria-label": t("Back") }, "<");
  const nextDay = el("button", { type: "button", class: "secondary article-day-nav", "aria-label": t("Next day") }, ">");
  const daySelect = el("select", { id: "article-day-filter", "aria-label": t("Filter by day") });
  const daySearch = el("input", { type: "search", id: "article-day-search", "aria-label": t("Search days") });
  const articleSearch = el("input", { type: "search", id: "article-title-search", "aria-label": t("Search articles") });
  const authorSelect = el("select", { id: "article-author-filter", "aria-label": t("Filter by author") });
  const authorSearch = el("input", { type: "search", id: "article-author-search", "aria-label": t("Search authors") });
  const tableHost = el("div", { class: "article-table-host scroll-pane" });
  const status = el("p", { class: "form-status", role: "status", "aria-live": "polite" });

  const clearFilters = el("button", { type: "button", class: "secondary article-clear-filters" }, t("Clear filters"));
  clearFilters.addEventListener("click", () => {
    filters.author = "";
    filters.media.clear();
    filters.articleQuery = "";
    filters.authorQuery = "";
    filters.dateSort = "desc";
    filters.day = "";
    filters.dayQuery = "";
    articleSearchActive = false;
    articleSearch.value = "";
    authorSearch.value = "";
    daySearch.value = "";
    renderFilters();
    void loadDayIndex({ preserveDay: false });
  });

  articleSearch.addEventListener("keydown", (event) => {
    if (event.key !== "Enter") return;
    event.preventDefault();
    filters.articleQuery = articleSearch.value.trim();
    if (filters.articleQuery) {
      void loadArticleSearch();
    } else {
      articleSearchActive = false;
      void loadDayIndex({ preserveDay: true });
    }
  });

  authorSearch.addEventListener("input", () => {
    filters.authorQuery = authorSearch.value.trim();
    renderAuthorChoices();
  });

  authorSelect.addEventListener("change", () => {
    filters.author = authorSelect.value;
    if (articleSearchActive) void loadArticleSearch();
    else void loadDayIndex({ preserveDay: false });
  });

  daySearch.addEventListener("input", () => {
    filters.dayQuery = daySearch.value.trim();
    renderDayChoices();
  });

  daySelect.addEventListener("change", () => {
    filters.day = daySelect.value;
    void loadArticlesForDay();
  });

  prevDay.addEventListener("click", () => moveDay(-1));
  nextDay.addEventListener("click", () => moveDay(1));

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
            el("div", { class: "article-day-pager" }, [prevDay, daySelect, nextDay]),
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
        el("fieldset", { class: "article-filter-group article-filter-group-article" }, [
          el("legend", {}, t("Article")),
          el("label", { class: "article-title-search", for: "article-title-search" }, [
            SearchIcon("article-search-icon"),
            articleSearch,
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
      const [portadasData, authorsData, facetsData] = await Promise.all([
        api.getPortadas(),
        api.listAuthors(false),
        api.listArticleFacets(),
      ]);
      authorsByKey = authorMap((authorsData && authorsData.authors) || []);
      authorOptions = authorList((authorsData && authorsData.authors) || []);
      sectionOptions = facetValues((facetsData && facetsData.sections) || []);
      layoutBySlug = layoutMap((portadasData && portadasData.portadas) || []);
      renderFilters();
      if (articleSearchActive && filters.articleQuery) await loadArticleSearch();
      else await loadDayIndex();
    } catch (err) {
      renderError(t("Could not load articles: {msg}", { msg: err.message }));
    }
  }

  async function loadDayIndex({ preserveDay = true } = {}) {
    renderLoading();
    const previousDay = preserveDay ? filters.day : "";
    try {
      const daysData = await api.listArticleDays(dayIndexParams());
      articleDays = ((daysData && daysData.days) || [])
        .filter((day) => day && day.date)
        .map((day) => ({ date: day.date, count: Number(day.count) || 0 }));
      filters.day = previousDay && articleDays.some((day) => day.date === previousDay) ? previousDay : (articleDays[0] && articleDays[0].date) || "";
      renderFilters();
      await loadArticlesForDay();
    } catch (err) {
      renderError(t("Could not load articles: {msg}", { msg: err.message }));
    }
  }

  async function loadArticlesForDay() {
    if (!filters.day) {
      allArticles = [];
      renderFilters();
      renderTable();
      return;
    }
    renderLoading();
    try {
      const articlesData = await api.listArticles(articlePageParams());
      allArticles = ((articlesData && articlesData.articles) || [])
        .filter((article) => !article.deleted)
        .map((article) => ({ ...article }));
      renderFilters();
      renderTable();
    } catch (err) {
      renderError(t("Could not load articles: {msg}", { msg: err.message }));
    }
  }

  async function loadArticleSearch() {
    const query = filters.articleQuery.trim();
    if (!query) {
      articleSearchActive = false;
      await loadDayIndex({ preserveDay: true });
      return;
    }
    articleSearchActive = true;
    renderLoading();
    try {
      const articlesData = await api.listArticles(articleSearchParams());
      allArticles = ((articlesData && articlesData.articles) || [])
        .filter((article) => !article.deleted)
        .map((article) => ({ ...article }));
      renderFilters();
      renderTable();
    } catch (err) {
      renderError(t("Could not load articles: {msg}", { msg: err.message }));
    }
  }

  function dayIndexParams() {
    return {
      order: filters.dateSort === "asc" ? "oldest" : "newest",
      author: filters.author,
    };
  }

  function articlePageParams() {
    return {
      order: "oldest",
      date: filters.day,
      author: filters.author,
    };
  }

  function articleSearchParams() {
    return {
      order: filters.dateSort === "asc" ? "oldest" : "newest",
      title_subtitle_q: filters.articleQuery,
      author: filters.author,
    };
  }

  function moveDay(delta) {
    const days = visibleDayValues();
    const index = days.indexOf(filters.day);
    const next = days[index + delta];
    if (!next) return;
    filters.day = next;
    daySelect.value = next;
    updateDayPager(days);
    void loadArticlesForDay();
  }

  function visibleDayValues() {
    const options = [...daySelect.options].map((option) => option.value).filter(Boolean);
    return options.length ? options : articleDays.map((day) => day.date);
  }

  function updateDayPager(visibleDays) {
    const days = visibleDays || [];
    const index = days.indexOf(filters.day);
    const total = days.length;
    prevDay.disabled = index <= 0;
    nextDay.disabled = index < 0 || index >= total - 1;
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
    const authors = authorOptions.map((author) => author.handle);

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
        if (articleSearchActive) void loadArticleSearch();
        else void loadDayIndex();
      });
      dateChecks.append(el("label", { class: "article-filter-choice", for: id }, [input, el("span", {}, label)]));
    });
  }

  function renderDayChoices() {
    if (articleSearchActive) {
      clear(daySelect);
      daySelect.append(el("option", { value: "" }, t("Search results")));
      daySelect.disabled = true;
      updateDayPager([]);
      return [];
    }
    const days = articleDays.map((day) => day.date);
    const query = filters.dayQuery.toLowerCase();
    const matches = articleDays.filter((day) => {
      if (!query) return true;
      return `${day.date} ${formatDayLabel(day.date)}`.toLowerCase().includes(query);
    });
    let visible = [...matches];

    if (filters.day && !days.includes(filters.day)) filters.day = "";
    if (filters.day && !visible.some((day) => day.date === filters.day)) {
      const selected = articleDays.find((day) => day.date === filters.day);
      if (selected) visible = [selected, ...visible];
    }

    clear(daySelect);
    if (!articleDays.length) {
      daySelect.append(el("option", { value: "" }, t("No days")));
      daySelect.disabled = true;
    } else if (!visible.length) {
      daySelect.append(el("option", { value: "" }, t("No days match.")));
      daySelect.disabled = true;
    } else {
      daySelect.disabled = false;
      for (const day of visible) {
        daySelect.append(el("option", { value: day.date }, formatDayLabel(day.date)));
      }
    }
    daySelect.value = visible.some((day) => day.date === filters.day) ? filters.day : "";
    updateDayPager(visible.map((day) => day.date));
    return matches.map((day) => day.date);
  }

  function renderAuthorChoices() {
    const authors = authorOptions.map((author) => author.handle);
    const query = filters.authorQuery.toLowerCase();
    const matches = authorOptions.filter((author) => {
      if (!query) return true;
      return `${author.name} ${author.handle}`.toLowerCase().includes(query);
    });
    let visible = [...matches];

    clear(authorSelect);
    authorSelect.append(el("option", { value: "" }, t("All authors")));
    if (!authors.length) {
      authorSelect.disabled = true;
      return [];
    }
    authorSelect.disabled = false;
    if (!visible.length) {
      authorSelect.append(el("option", { value: "", disabled: true }, t("No authors match.")));
      return [];
    }
    if (filters.author && !visible.some((author) => author.handle === filters.author)) {
      visible = [{ handle: filters.author, name: authorProfile(filters.author).name }, ...visible];
    }
    visible.forEach((author) => {
      authorSelect.append(el("option", { value: author.handle }, author.name));
    });
    authorSelect.value = visible.some((author) => author.handle === filters.author) ? filters.author : "";
    return matches.map((author) => author.handle);
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
      const day = dayOf(article) || layoutInfo(article).day;
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
      if (filters.author && article.author !== filters.author) return false;
      if (filters.media.size && !filters.media.has(articleKind(article))) return false;
      return true;
    });
  }

  function sorted(articles) {
    return [...articles].sort((a, b) => {
      const dayCmp = String(dayOf(a) || layoutInfo(a).day).localeCompare(String(dayOf(b) || layoutInfo(b).day));
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
        titleCell(article, title),
        clippedCell(subtitle, 54, "article-subtitle-cell"),
        el("td", { class: "article-delete-cell" }, deleteBtn),
      ],
    );
    return tr;
  }

  function clippedCell(text, max, className) {
    return el("td", { class: className, title: text || "" }, text ? truncate(text, max) : "-");
  }

  function titleCell(article, title) {
    const button = el(
      "button",
      {
        type: "button",
        class: "article-title-edit",
        "aria-label": t("Edit article"),
        onClick: (event) => {
          event.stopPropagation();
          void openDetail(article);
        },
      },
      title ? truncate(title, 58) : "-",
    );
    return el("td", { class: "article-title-cell", title: title || "" }, button);
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
      const dialog = buildDetailDialog(full);
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

  function buildDetailDialog(full) {
    const close = el("button", { type: "button", class: "secondary article-dialog-close", "aria-label": t("Close") }, "×");
    const dialog = el("dialog", { class: "article-dialog", "aria-label": t("Article editor") });
    close.addEventListener("click", () => closeDialog(dialog));
    dialog.addEventListener("cancel", () => closeDialog(dialog));
    dialog.addEventListener("close", () => dialog.remove());
    dialog.append(
      el("div", { class: "article-dialog-shell" }, [
        close,
        buildDetail(full),
      ]),
    );
    return dialog;
  }

  function buildDetail(full) {
    const wrap = el("div", { class: "article-detail-wrap" });
    const slug = full.slug;
    const title = el("input", { type: "text", id: `ae-${slug}-title`, value: full.title || "" });
    const subtitle = el("input", { type: "text", id: `ae-${slug}-subtitle`, value: (full.metadata && full.metadata.subtitle) || "" });
    const sectionValues = sectionChoices(full.section);
    const section = el("select", { id: `ae-${slug}-section` }, sectionValues.map((s) => el("option", { value: s }, s)));
    section.value = full.section || sectionValues[0] || "";
    const body = el("textarea", { id: `ae-${slug}-body`, class: "article-body-editor" }, full.body || "");
    let initialSnapshot;
    let confirmArmed = false;
    const authorPicker = authorDropdown(`ae-${slug}-author`, full.author || "", markEdited);
    const saveStatus = el("p", { class: "form-status article-save-status", role: "status", "aria-live": "polite" });
    const restore = el("button", { type: "button", class: "secondary article-restore-button", disabled: true }, t("Restore"));
    const save = el("button", { type: "submit", class: "article-save-button", disabled: true }, t("Save article"));

    const editForm = el("form", { class: "article-edit" }, [
      field(t("Title"), title, `ae-${slug}-title`),
      field(t("Subtitle"), subtitle, `ae-${slug}-subtitle`),
      field(t("Section"), section, `ae-${slug}-section`),
      field(t("Body"), body, `ae-${slug}-body`),
      el("div", { class: "article-edit-footer" }, [
        field(t("Author"), authorPicker.element, `ae-${slug}-author`),
        el("div", { class: "article-save-cluster" }, [restore, saveStatus, save]),
      ]),
    ]);

    [title, subtitle, section, body].forEach((control) => {
      control.addEventListener("input", markEdited);
      control.addEventListener("change", markEdited);
    });
    restore.addEventListener("click", () => {
      applySnapshot(initialSnapshot);
      confirmArmed = false;
      clearSaveStatus();
      updateDirtyState();
    });
    editForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (!isDirty()) return;
      if (!confirmArmed) {
        confirmArmed = true;
        save.textContent = t("Confirm");
        return;
      }
      const payload = buildArticlePayload(formSnapshot());
      save.disabled = true;
      restore.disabled = true;
      setStatus(saveStatus, "pending", t("Saving..."));
      try {
        await api.updateArticle(slug, payload);
        initialSnapshot = formSnapshot();
        confirmArmed = false;
        updateDirtyState();
        setStatus(saveStatus, "done", t("Article saved."));
        await reload();
      } catch (err) {
        setStatus(saveStatus, "error", t("Could not save article ({code}): {msg}", { code: err.code, msg: err.message }));
      } finally {
        updateDirtyState();
      }
    });

    initialSnapshot = formSnapshot();
    updateDirtyState();
    wrap.append(editForm);
    requestAnimationFrame(() => title.focus());
    return wrap;

    function markEdited() {
      confirmArmed = false;
      save.textContent = t("Save article");
      if (saveStatus.dataset.state === "done") clearSaveStatus();
      updateDirtyState();
    }

    function formSnapshot() {
      return {
        title: title.value.trim(),
        subtitle: subtitle.value.trim(),
        section: section.value,
        body: body.value,
        author: authorPicker.value().trim(),
      };
    }

    function applySnapshot(snapshot) {
      if (!snapshot) return;
      title.value = snapshot.title;
      subtitle.value = snapshot.subtitle;
      section.value = snapshot.section;
      body.value = snapshot.body;
      authorPicker.setValue(snapshot.author);
    }

    function buildArticlePayload(snapshot) {
      const metadata = { ...((full && full.metadata) || {}) };
      if (snapshot.subtitle) metadata.subtitle = snapshot.subtitle;
      else delete metadata.subtitle;
      const payload = {
        title: snapshot.title,
        body: snapshot.body,
        author: snapshot.author,
        section: snapshot.section,
        topics: Array.isArray(full.topics) ? full.topics : [],
        metadata,
      };
      if (full.published_at) payload.published_at = full.published_at;
      return payload;
    }

    function isDirty() {
      return !sameArticleEditState(formSnapshot(), initialSnapshot);
    }

    function updateDirtyState() {
      const dirty = isDirty();
      save.disabled = !dirty;
      restore.disabled = !dirty;
      if (!dirty) {
        confirmArmed = false;
        save.textContent = t("Save article");
      }
    }

    function clearSaveStatus() {
      delete saveStatus.dataset.state;
      saveStatus.textContent = "";
      saveStatus.setAttribute("role", "status");
      saveStatus.setAttribute("aria-live", "polite");
    }
  }

  function sectionChoices(current) {
    const values = [...sectionOptions];
    if (current && !values.includes(current)) values.unshift(current);
    return values.length ? values : (current ? [current] : []);
  }

  function authorDropdown(id, initial, onChange = () => {}) {
    let selected = initial || "";
    const choices = authorChoices(selected);
    if (!selected && choices[0]) selected = choices[0].handle;
    const button = el("button", {
      type: "button",
      class: "article-author-picker-button",
      id,
      "aria-haspopup": "listbox",
      "aria-expanded": "false",
    });
    const menu = el("div", { class: "article-author-picker-menu", role: "listbox", hidden: true });
    const picker = el("div", { class: "article-author-picker" }, [button, menu]);

    function setOpen(open) {
      menu.hidden = !open;
      button.setAttribute("aria-expanded", open ? "true" : "false");
      if (open) renderMenu();
    }

    function choose(handle) {
      if (selected === handle) {
        setOpen(false);
        button.focus();
        return;
      }
      selected = handle;
      renderButton();
      renderMenu();
      setOpen(false);
      onChange();
      button.focus();
    }

    function renderButton() {
      clear(button);
      const profile = authorProfile(selected);
      button.append(authorChip(profile), el("span", { class: "article-author-picker-caret", "aria-hidden": "true" }, "v"));
    }

    function renderMenu() {
      clear(menu);
      if (!choices.length) {
        menu.append(el("p", { class: "article-author-option-empty" }, t("No authors yet.")));
        return;
      }
      for (const profile of choices) {
        const option = el(
          "button",
          {
            type: "button",
            class: "article-author-option",
            role: "option",
            "aria-selected": profile.handle === selected ? "true" : "false",
            onClick: () => choose(profile.handle),
          },
          authorChip(profile),
        );
        menu.append(option);
      }
    }

    button.addEventListener("click", () => setOpen(menu.hidden));
    button.addEventListener("keydown", (event) => {
      if (event.key === "Escape") setOpen(false);
      if (event.key === "ArrowDown") {
        event.preventDefault();
        setOpen(true);
        const first = menu.querySelector(".article-author-option");
        if (first) first.focus();
      }
    });
    picker.addEventListener("focusout", () => {
      setTimeout(() => {
        if (!picker.contains(document.activeElement)) setOpen(false);
      }, 0);
    });
    renderButton();
    renderMenu();
    return {
      element: picker,
      value: () => selected,
      setValue: (handle) => {
        selected = handle || "";
        renderButton();
        renderMenu();
      },
    };
  }

  function authorChoices(current) {
    let choices = authorOptions.map((author) => authorProfile(author.handle));
    if (current && !choices.some((author) => author.handle === current)) choices = [authorProfile(current), ...choices];
    return choices;
  }

  return { element, reload };
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
  if (layout.index === 1) {
    const label = t("Main article");
    return el("span", { class: "article-card-span", dataset: { role: "main" }, title: label, "aria-label": label }, [
      el("span", { class: "article-card-slot article-card-slot-main" }),
    ]);
  }
  if (layout.role === "important") {
    const label = t("Double card");
    return el("span", { class: "article-card-span", dataset: { role: "double" }, title: label, "aria-label": label }, [
      el("span", { class: "article-card-slot article-card-slot-wide" }),
    ]);
  }
  const label = t("Single card");
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

function authorList(authors) {
  return (authors || [])
    .filter((author) => author && !author.deleted && author.handle)
    .map((author) => ({
      handle: author.handle,
      name: author.name || humanizeAuthor(author.handle),
      avatar: author.avatar || "",
    }))
    .sort((a, b) => a.name.localeCompare(b.name));
}

function facetValues(facets) {
  return [...new Set((facets || []).map((facet) => String(facet.value || "").trim()).filter(Boolean))];
}

function sameArticleEditState(a, b) {
  if (!a || !b) return false;
  return a.title === b.title &&
    a.subtitle === b.subtitle &&
    a.section === b.section &&
    a.body === b.body &&
    a.author === b.author;
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
