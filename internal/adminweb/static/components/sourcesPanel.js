import { el, clear, field, help } from "./el.js";
import { TrashIcon } from "./icons.js";
import { t } from "./i18n.js";

// The Sources tab: a source-registry manager. A source is a news outlet (by its
// domain/homepage/feeds) the newsroom is allowed to draw from, keyed by a slug the
// backend derives from the domain. The panel is a list-first TABLE of sources with
// author/orientation filters. Each row shows the domain, assigned authors,
// orientation, description, and a two-click delete icon. Clicking a row opens the
// fullscreen editor.
//
// The author-per-source column is resolved client-side: it reads every author's
// `sources` pool (attached source slugs) and inverts it to slug -> authors, so no
// extra endpoint is needed.
//
// The backend has no partial PATCH and no enable/disable verb: edits are FULL
// upserts of the whole row (with an explicit slug + domain so the row updates in
// place and never forks a new one) via api.upsertSource.
//
// `onChanged` (optional) fires after any successful mutation so a parent can refresh
// anything that depends on the source set. `reload()` re-fetches and re-renders the
// table, and is exposed so the app can prime it on mount.

const OWNERSHIP_HELP =
  "Co-owned mastheads collapse to one independent source for the corroboration gate. Give shared owners the " +
  "same group so two papers under one owner count once, not twice, when a claim needs a second source.";

const LEANS = ["right", "neutral", "left"];

export function SourcesPanel({ api, onChanged } = {}) {
  const filters = { author: "", lean: "", sortKey: "source", sortDir: "asc" };
  let sources = [];
  let authors = [];
  let authorsBySource = new Map();

  const authorFilter = el("select", { id: "source-author-filter", "aria-label": t("Filter by author") });
  const leanFilter = el("select", { id: "source-lean-filter", "aria-label": t("Filter by orientation") });
  const listEl = el("div", { class: "source-list article-table-host scroll-pane" });
  const listStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
  const clearFilters = el("button", { type: "button", class: "secondary article-clear-filters" }, t("Clear filters"));
  clearFilters.addEventListener("click", () => {
    filters.author = "";
    filters.lean = "";
    renderFilters();
    renderTable();
  });
  authorFilter.addEventListener("change", () => {
    filters.author = authorFilter.value;
    renderTable();
  });
  leanFilter.addEventListener("change", () => {
    filters.lean = leanFilter.value;
    renderTable();
  });

  const element = el("div", { class: "sources workspace-section" }, el("section", { class: "panel panel-fill sources-panel" }, [
    el("div", { class: "article-filter-bar source-filter-bar" }, [
      el("fieldset", { class: "article-filter-group source-filter-group" }, [
        el("legend", {}, t("Author")),
        authorFilter,
      ]),
      el("fieldset", { class: "article-filter-group source-filter-group" }, [
        el("legend", {}, t("Orientation")),
        leanFilter,
      ]),
      el("div", { class: "article-filter-actions" }, [clearFilters]),
    ]),
    listEl,
    listStatus,
  ]));

  async function reload() {
    clear(listEl);
    listEl.append(el("p", { class: "muted" }, t("Loading sources...")));
    clearStatus(listStatus);
    try {
      const [sourceData, authorData] = await Promise.all([api.listSources(), api.listAuthors()]);
      sources = (sourceData && sourceData.sources) || [];
      authors = (authorData && authorData.authors) || [];
      authorsBySource = invertAuthorsBySource(authors);
      if (filters.author && !authors.some((author) => author.handle === filters.author)) filters.author = "";
      renderFilters();
      renderTable();
    } catch (err) {
      clear(listEl);
      listEl.append(el("p", { class: "error", role: "alert" }, t("Could not load sources: {msg}", { msg: err.message })));
    }
  }

  function renderFilters() {
    clear(authorFilter);
    authorFilter.append(el("option", { value: "" }, t("All authors")));
    for (const author of authorOptions(authors)) {
      authorFilter.append(el("option", { value: author.handle }, author.name || author.handle));
    }
    authorFilter.value = filters.author;

    clear(leanFilter);
    for (const option of [{ value: "", label: t("All") }, ...LEANS.map((lean) => ({ value: lean, label: leanLabel(lean) }))]) {
      leanFilter.append(el("option", { value: option.value }, option.label));
    }
    leanFilter.value = filters.lean;
  }

  function renderTable() {
    clear(listEl);
    const visible = sources.filter(sourceMatches).sort(compareSources);
    if (!sources.length) {
      listEl.append(el("p", { class: "muted" }, t("No sources yet.")));
      return;
    }
    if (!visible.length) {
      listEl.append(el("p", { class: "muted" }, t("No sources match.")));
      return;
    }
    const tbody = el("tbody", {});
    visible.forEach((source, index) => {
      tbody.append(row(source, authorsBySource.get(source.slug) || [], index));
    });
    listEl.append(
      el("table", { class: "source-table article-table" }, [
        el("thead", {}, el("tr", {}, [
          sortTh("source", t("Source")),
          sortTh("assigned", t("Assigned to")),
          sortTh("orientation", t("Orientation")),
          el("th", {}, t("Description")),
          el("th", { class: "source-delete-head" }, t("Delete")),
        ])),
        tbody,
      ]),
    );
  }

  // Invert the per-author source pools into slug -> [{handle, name, beat}], so a row
  // can show and filter by which authors read it.
  function invertAuthorsBySource(items) {
    const map = new Map();
    for (const a of items || []) {
      const meta = a.metadata || {};
      for (const slug of a.sources || []) {
        if (!map.has(slug)) map.set(slug, []);
        map.get(slug).push({ handle: a.handle, name: a.name || a.handle, beat: meta.beat || a.beat || "" });
      }
    }
    return map;
  }

  function authorOptions(items) {
    return [...(items || [])].sort((a, b) => (a.name || a.handle || "").localeCompare(b.name || b.handle || ""));
  }

  function sourceMatches(source) {
    if (filters.lean && (source.lean || "neutral") !== filters.lean) return false;
    if (!filters.author) return true;
    return (authorsBySource.get(source.slug) || []).some((author) => author.handle === filters.author);
  }

  function sortTh(key, label) {
    const active = filters.sortKey === key;
    const direction = active ? (filters.sortDir === "asc" ? "ascending" : "descending") : "none";
    const button = el("button", { type: "button", class: "source-sort-button" }, [
      el("span", {}, label),
      el("span", { class: "source-sort-mark", "aria-hidden": "true" }, active ? (filters.sortDir === "asc" ? "^" : "v") : ""),
    ]);
    button.addEventListener("click", () => {
      if (filters.sortKey === key) {
        filters.sortDir = filters.sortDir === "asc" ? "desc" : "asc";
      } else {
        filters.sortKey = key;
        filters.sortDir = "asc";
      }
      renderTable();
    });
    return el("th", { "aria-sort": direction }, button);
  }

  function compareSources(a, b) {
    const av = sourceSortValue(a, filters.sortKey);
    const bv = sourceSortValue(b, filters.sortKey);
    const result = av.localeCompare(bv, undefined, { sensitivity: "base", numeric: true });
    if (result !== 0) return filters.sortDir === "asc" ? result : -result;
    return (a.domain || "").localeCompare(b.domain || "", undefined, { sensitivity: "base", numeric: true });
  }

  function sourceSortValue(source, key) {
    if (key === "assigned") return assignedLabel(source);
    if (key === "orientation") return leanLabel(source.lean);
    return source.domain || source.slug || "";
  }

  function assignedLabel(source) {
    const assigned = authorsBySource.get(source.slug) || [];
    if (!assigned.length) return "zzzzzzzzzz";
    return assigned.map((author) => author.name || author.handle || "").sort((a, b) => a.localeCompare(b)).join(", ");
  }

  function row(source, sourceAuthors, index) {
    const enabled = !!source.enabled;
    const pill = el("span", { class: "status", dataset: { state: enabled ? "online" : "offline" } }, enabled ? t("online") : t("offline"));
    const portalCell = el("td", {}, el("div", { class: "source-portal" }, [
      el("span", { class: "source-domain" }, source.domain),
      pill,
      source.ownership_group ? el("span", { class: "badge" }, source.ownership_group) : null,
    ]));

    const cardStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });

    // Two-click delete instead of window.confirm (a no-op under jsdom): the first
    // click arms the button, the second performs the delete.
    let armed = false;
    const deleteBtn = el("button", { type: "button", class: "source-trash", "aria-label": t("Delete") }, TrashIcon("article-trash-icon"));
    deleteBtn.addEventListener("click", (event) => {
      event.stopPropagation();
      if (!armed) {
        armed = true;
        clear(deleteBtn);
        deleteBtn.append(t("Confirm"));
        deleteBtn.setAttribute("aria-label", t("Confirm"));
        deleteBtn.dataset.confirm = "true";
        return;
      }
      act(deleteBtn, () => api.deleteSource(source.slug), cardStatus);
    });

    const actionsCell = el("td", {}, [
      el("div", { class: "source-actions source-actions--stack" }, [deleteBtn]),
      cardStatus,
    ]);

    // Which authors read this source (a source can feed several). Empty = unassigned.
    const authorsCell = el(
      "td",
      {},
      el(
        "div",
        { class: "source-authors" },
        sourceAuthors.length
          ? sourceAuthors.map((a) => el("span", { class: "author-chip", dataset: { section: a.beat } }, a.name))
          : [el("span", { class: "source-authors-none" }, t("No authors assigned"))],
      ),
    );

    const leanCell = el("td", {}, el("span", { class: "source-lean", dataset: { lean: source.lean || "neutral" } }, leanLabel(source.lean)));
    const descCell = el("td", { class: "source-desc" }, source.description || "");

    const tableRow = el("tr", { class: "source-row article-row", tabindex: "0", dataset: { id: source.slug, parity: index % 2 === 0 ? "even" : "odd" } }, [
      portalCell,
      authorsCell,
      leanCell,
      descCell,
      actionsCell,
    ]);
    tableRow.addEventListener("click", () => openEditor(source));
    tableRow.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      openEditor(source);
    });
    return tableRow;
  }

  function openEditor(source) {
    let dialog = null;
    const closeEditor = () => {
      if (dialog) closeDialog(dialog);
    };
    const form = buildEditForm(source, closeEditor);
    const close = el("button", { type: "button", class: "secondary source-dialog-close", "aria-label": t("Close") }, "×");
    dialog = el("dialog", { class: "source-dialog", "aria-label": t("Source editor") });
    close.addEventListener("click", closeEditor);
    dialog.addEventListener("cancel", closeEditor);
    dialog.addEventListener("close", () => dialog.remove(), { once: true });
    dialog.append(el("div", { class: "source-dialog-shell" }, [close, form]));
    document.body.append(dialog);
    showDialog(dialog);
  }

  function buildEditForm(initialSource, onClose) {
    let source = initialSource;
    const id = source.slug;
    const domain = el("input", { type: "text", id: `se-${id}-domain`, value: source.domain || "", readonly: true });
    const homepage = el("input", { type: "url", id: `se-${id}-homepage`, value: source.homepage || "" });
    const description = el("input", { type: "text", id: `se-${id}-desc`, value: source.description || "" });
    const ownership = el("input", { type: "text", id: `se-${id}-ownership`, value: source.ownership_group || "" });
    const feeds = el("input", { type: "text", id: `se-${id}-feeds`, value: (source.feed_urls || []).join(", ") });
    const feedType = el("input", { type: "text", id: `se-${id}-feed-type`, value: source.feed_type || "auto" });
    const language = el("input", { type: "text", id: `se-${id}-language`, value: source.language || "es" });
    const lean = leanSelect(`se-${id}-lean`);
    lean.value = LEANS.includes(source.lean) ? source.lean : "neutral";
    const enabled = el("input", { type: "checkbox", id: `se-${id}-enabled` });
    enabled.checked = !!source.enabled;
    const refs = { homepage, description, ownership, feeds, feedType, language, lean, enabled };
    const editStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });

    const save = el("button", { type: "submit" }, t("Save"));
    const cancel = el("button", { type: "button", class: "secondary" }, t("Cancel"));

    const editForm = el("form", { class: "source-edit source-edit-full" }, [
      el("div", { class: "source-edit-grid" }, [
        field(t("Domain"), domain, `se-${id}-domain`),
        field(t("Description"), description, `se-${id}-desc`),
        helpField(t("Ownership group"), ownership, `se-${id}-ownership`, t(OWNERSHIP_HELP)),
        field(t("Homepage"), homepage, `se-${id}-homepage`),
        field(t("Feed URLs (comma separated)"), feeds, `se-${id}-feeds`),
        field(t("Feed type"), feedType, `se-${id}-feed-type`),
        field(t("Language"), language, `se-${id}-language`),
        field(t("Orientation"), lean, `se-${id}-lean`),
        checkField(t("Enabled"), enabled, `se-${id}-enabled`),
      ]),
      el("div", { class: "source-actions source-actions--editor" }, [cancel, save]),
      editStatus,
    ]);
    cancel.addEventListener("click", () => onClose());
    editForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      // Full upsert: the collected form values override the carried row (explicit slug +
      // domain + preserved status columns), so the row is updated in place.
      const body = fullSourceBody(source, collectEditable(refs, { allowClear: true }));
      save.disabled = true;
      setStatus(editStatus, "pending", t("Saving..."));
      try {
        await api.upsertSource(body);
        source = body;
        await reload();
        setStatus(editStatus, "done", t("Saved."));
        save.disabled = false;
        if (onChanged) onChanged();
      } catch (err) {
        save.disabled = false;
        setStatus(editStatus, "error", t("Could not save ({code}): {msg}", { code: err.code, msg: err.message }));
      }
    });
    return editForm;
  }

  // Run a row action, then reload the table. On success the row is replaced by
  // the fresh render, so the button is discarded; on failure it re-enables and
  // the row surfaces the backend's error.
  async function act(button, run, statusNode) {
    button.disabled = true;
    try {
      await run();
      await reload();
      if (onChanged) onChanged();
    } catch (err) {
      button.disabled = false;
      setStatus(statusNode, "error", t("Action failed ({code}): {msg}", { code: err.code, msg: err.message }));
    }
  }

  return { element, reload };
}

// The editable subset of the source, shared by create and edit. `enabled` is
// always sent as the checkbox state; `feed_urls` is split on commas/newlines,
// trimmed, emptied. The free-text fields (homepage, description, ownership_group,
// feed_urls) behave differently per mode: on CREATE (allowClear=false) a blank is
// omitted so the server default applies and never clobbers; on EDIT
// (allowClear=true) a blank is sent as "" / [] so an operator can CLEAR a field
// (e.g. un-group a co-owned masthead). `feed_type`/`language` are enum-ish and
// never sent blank in either mode, so they can't be cleared to an invalid value.
// The caller adds `domain` for a create.
function collectEditable(refs, { allowClear = false } = {}) {
  const body = {};
  const homepage = refs.homepage.value.trim();
  if (homepage || allowClear) body.homepage = homepage;
  const description = refs.description.value.trim();
  if (description || allowClear) body.description = description;
  const ownership = refs.ownership.value.trim();
  if (ownership || allowClear) body.ownership_group = ownership;
  const feeds = splitList(refs.feeds.value);
  if (feeds.length || allowClear) body.feed_urls = feeds;
  const feedType = refs.feedType.value.trim();
  if (feedType) body.feed_type = feedType;
  const language = refs.language.value.trim();
  if (language) body.language = language;
  if (refs.lean && refs.lean.value) body.lean = refs.lean.value;
  body.enabled = refs.enabled.checked;
  return body;
}

// Rebuild the whole source row for a full upsert. There is no partial PATCH: an edit
// or an enable/disable toggle sends the entire row, so it MUST carry an EXPLICIT slug
// AND domain (a blank slug would re-derive from the domain and could fork a new row)
// and must not blank the server-managed status columns (status/last_checked/last_ok)
// or the metadata bag. `overrides` changes only the touched fields, e.g. the edit
// form's collected values or the toggle's flipped `enabled`.
function fullSourceBody(source, overrides = {}) {
  return {
    slug: source.slug,
    domain: source.domain,
    homepage: source.homepage || "",
    description: source.description || "",
    ownership_group: source.ownership_group || "",
    feed_urls: source.feed_urls || [],
    feed_type: source.feed_type || "auto",
    language: source.language || "es",
    lean: LEANS.includes(source.lean) ? source.lean : "neutral",
    enabled: !!source.enabled,
    status: source.status,
    last_checked: source.last_checked,
    last_ok: source.last_ok,
    metadata: { ...(source.metadata || {}) },
    ...overrides,
  };
}

function leanSelect(id, value = "") {
  const select = el("select", { id }, LEANS.map((lean) => el("option", { value: lean, selected: value === lean }, leanLabel(lean))));
  if (value) select.value = value;
  return select;
}

function leanLabel(lean) {
  if (lean === "right") return t("Right");
  if (lean === "left") return t("Left");
  return t("Neutral");
}

function splitList(value) {
  return value
    .split(/[,\n]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

// A labeled control with an inline (?) help marker. The help sits beside the
// label (not inside it) so the control's accessible name stays the bare label.
function helpField(labelText, control, id, helpText) {
  return el("div", { class: "field" }, [
    el("span", { class: "field-label" }, [el("label", { for: id }, labelText), help(helpText)]),
    control,
  ]);
}

// A checkbox field laid out inline (box then label on one row), so it never
// inherits the full-width text-input look. Used for the Enabled toggles.
function checkField(labelText, control, id) {
  return el("div", { class: "field field-check" }, [control, el("label", { for: id }, labelText)]);
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

function showDialog(dialog) {
  if (typeof dialog.showModal === "function") {
    dialog.showModal();
    return;
  }
  dialog.setAttribute("open", "");
}

function closeDialog(dialog) {
  if (typeof dialog.close === "function") {
    dialog.close();
    return;
  }
  dialog.removeAttribute("open");
  dialog.dispatchEvent(new Event("close"));
}
