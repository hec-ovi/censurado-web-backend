import { el, clear, field, help, subTabs } from "./el.js";
import { t } from "./i18n.js";

// The Sources tab: a source-registry manager. A source is a news outlet (by its
// domain/homepage/feeds) the newsroom is allowed to draw from, keyed by a slug the
// backend derives from the domain. The panel is a create form on top and a TABLE of
// sources below: one row per source with its domain, the actions (edit /
// enable-disable / delete), the authors that read it, and the description. Edit
// expands an inline form as a full-width row beneath.
//
// The author-per-source column is resolved client-side: it reads every author's
// `sources` pool (attached source slugs) and inverts it to slug -> authors, so no
// extra endpoint is needed. If the authors fetch fails the sources still render,
// without chips.
//
// The backend has no partial PATCH and no enable/disable verb: an edit and a toggle
// are both FULL upserts of the whole row (with an explicit slug + domain so the row
// updates in place and never forks a new one) via api.upsertSource.
//
// `onChanged` (optional) fires after any successful mutation so a parent can refresh
// anything that depends on the source set. `reload()` re-fetches and re-renders the
// table, and is exposed so the app can prime it on mount.

const SOURCE_HELP =
  "A source is a news outlet the newsroom may draw from: its domain, homepage, and feeds. " +
  "Add the outlets you trust here; disabled sources stay on file but are skipped.";

const OWNERSHIP_HELP =
  "Co-owned mastheads collapse to one independent source for the corroboration gate. Give shared owners the " +
  "same group so two papers under one owner count once, not twice, when a claim needs a second source.";

const LEANS = ["right", "neutral", "left"];

export function SourcesPanel({ api, onChanged } = {}) {
  // --- Create form -------------------------------------------------------
  const domainInput = el("input", { type: "text", id: "sp-domain", autocomplete: "off" });
  const descInput = el("input", { type: "text", id: "sp-desc", autocomplete: "off" });
  const ownershipInput = el("input", { type: "text", id: "sp-ownership", autocomplete: "off" });
  const homepageInput = el("input", { type: "url", id: "sp-homepage", placeholder: "https://example.com" });
  const feedsInput = el("input", { type: "text", id: "sp-feeds", placeholder: "https://example.com/feed.xml, ..." });
  const feedTypeInput = el("input", { type: "text", id: "sp-feed-type", value: "auto" });
  const languageInput = el("input", { type: "text", id: "sp-language", value: "es" });
  const leanInput = leanSelect("sp-lean", "neutral");
  const enabledCheck = el("input", { type: "checkbox", id: "sp-enabled", checked: true });
  const submit = el("button", { type: "submit" }, t("Add source"));
  const createStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });

  const createRefs = {
    homepage: homepageInput,
    description: descInput,
    ownership: ownershipInput,
    feeds: feedsInput,
    feedType: feedTypeInput,
    language: languageInput,
    lean: leanInput,
    enabled: enabledCheck,
  };

  const form = el("form", { class: "source-form" }, [
    field(t("Domain"), domainInput, "sp-domain"),
    field(t("Description"), descInput, "sp-desc"),
    helpField(t("Ownership group"), ownershipInput, "sp-ownership", t(OWNERSHIP_HELP)),
    field(t("Homepage"), homepageInput, "sp-homepage"),
    field(t("Feed URLs (comma separated)"), feedsInput, "sp-feeds"),
    field(t("Feed type"), feedTypeInput, "sp-feed-type"),
    field(t("Language"), languageInput, "sp-language"),
    field(t("Orientation"), leanInput, "sp-lean"),
    checkField(t("Enabled"), enabledCheck, "sp-enabled"),
    submit,
    createStatus,
  ]);
  form.addEventListener("submit", onCreate);

  const createPanel = el("section", { class: "panel panel-fill" }, [
    el("div", { class: "panel-head" }, [el("h2", {}, t("Add source")), help(t(SOURCE_HELP))]),
    form,
  ]);

  // --- List (table) ------------------------------------------------------
  const listEl = el("div", { class: "source-list scroll-pane" });
  const listPanel = el("section", { class: "panel panel-fill" }, [
    el("div", { class: "panel-head" }, [el("h2", {}, t("Sources"))]),
    listEl,
  ]);

  const tabs = subTabs(
    [
      { id: "add-source", label: t("Add source"), content: createPanel },
      { id: "sources-list", label: t("Sources"), content: listPanel },
    ],
    { className: "content-tabs", label: t("Source sections") },
  );
  const element = el("div", { class: "sources workspace-section" }, tabs.element);

  async function onCreate(event) {
    event.preventDefault();
    domainInput.setAttribute("aria-invalid", "false");
    const domain = domainInput.value.trim();
    if (!domain) {
      domainInput.setAttribute("aria-invalid", "true");
      domainInput.focus();
      setStatus(createStatus, "error", t("Domain is required."));
      return;
    }
    // No slug: the backend derives it from the domain on create.
    const body = collectEditable(createRefs);
    body.domain = domain;

    setBusy(submit, form, true);
    setStatus(createStatus, "pending", t("Adding source..."));
    try {
      await api.upsertSource(body);
      form.reset();
      domainInput.setAttribute("aria-invalid", "false");
      setStatus(createStatus, "done", t("Added {domain}.", { domain }));
      await reload();
      tabs.select(1);
      if (onChanged) onChanged();
    } catch (err) {
      setStatus(createStatus, "error", t("Could not add source ({code}): {msg}", { code: err.code, msg: err.message }));
    } finally {
      setBusy(submit, form, false);
    }
  }

  async function reload() {
    clear(listEl);
    listEl.append(el("p", { class: "muted" }, t("Loading sources...")));
    try {
      const [data, authorsBySource] = await Promise.all([api.listSources(), loadAuthorsBySource()]);
      const sources = (data && data.sources) || [];
      clear(listEl);
      if (!sources.length) {
        listEl.append(el("p", { class: "muted" }, t("No sources yet.")));
        return;
      }
      const tbody = el("tbody", {});
      for (const source of sources) {
        const { dataRow, editRow } = rows(source, authorsBySource.get(source.slug) || []);
        tbody.append(dataRow, editRow);
      }
      listEl.append(
        el("table", { class: "source-table" }, [
          el("thead", {}, el("tr", {}, [
            el("th", {}, t("Title")),
            el("th", {}, t("Assigned to")),
            el("th", {}, t("Orientation")),
            el("th", {}, t("Description")),
            el("th", {}, t("Actions")),
          ])),
          tbody,
        ]),
      );
    } catch (err) {
      clear(listEl);
      listEl.append(el("p", { class: "error", role: "alert" }, t("Could not load sources: {msg}", { msg: err.message })));
    }
  }

  // Invert the per-author source pools into slug -> [{handle, name, beat}], so a row
  // can show which authors read it. Read from the authors list (author.sources holds
  // attached source SLUGS); if that fetch fails the map is empty and sources render
  // without chips (not an error).
  async function loadAuthorsBySource() {
    const map = new Map();
    try {
      const data = await api.listAuthors();
      for (const a of (data && data.authors) || []) {
        for (const slug of a.sources || []) {
          if (!map.has(slug)) map.set(slug, []);
          map.get(slug).push({ handle: a.handle, name: a.name || a.handle, beat: a.beat || "" });
        }
      }
    } catch {
      /* authors unavailable: render sources without author chips */
    }
    return map;
  }

  // A source's two rows: the data row (.source-row) and a hidden full-width edit
  // row (.source-edit-row) the Edit button expands. The cardStatus line is shared
  // so a failed toggle/delete/edit surfaces under the row's actions.
  function rows(source, authors) {
    const enabled = !!source.enabled;
    const pill = el("span", { class: "status", dataset: { state: enabled ? "online" : "offline" } }, enabled ? t("online") : t("offline"));
    // The flex layouts live on inner divs, never on the <td> itself: a flex <td>
    // drops out of the table's column grid and the cells stop aligning.
    const portalCell = el("td", {}, el("div", { class: "source-portal" }, [
      el("span", { class: "source-domain" }, source.domain),
      pill,
      source.ownership_group ? el("span", { class: "badge" }, source.ownership_group) : null,
    ]));

    const cardStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });

    // No enable/disable verb on the backend: the toggle is a full upsert of the whole
    // row (explicit slug + domain + every field) with `enabled` flipped.
    const toggleBtn = el("button", { type: "button" }, enabled ? t("Disable") : t("Enable"));
    toggleBtn.addEventListener("click", () =>
      act(toggleBtn, () => api.upsertSource(fullSourceBody(source, { enabled: !enabled })), cardStatus));

    // Two-click delete instead of window.confirm (a no-op under jsdom): the first
    // click arms the button, the second performs the delete.
    let armed = false;
    const deleteBtn = el("button", { type: "button", class: "secondary" }, t("Delete"));
    deleteBtn.addEventListener("click", () => {
      if (!armed) {
        armed = true;
        deleteBtn.textContent = t("Confirm");
        deleteBtn.dataset.confirm = "true";
        return;
      }
      act(deleteBtn, () => api.deleteSource(source.slug), cardStatus);
    });

    const editBtn = el("button", { type: "button", class: "secondary" }, t("Edit"));
    const actionsCell = el("td", {}, [
      el("div", { class: "source-actions source-actions--stack" }, [toggleBtn, editBtn, deleteBtn]),
      cardStatus,
    ]);

    // Which authors read this source (a source can feed several). Empty = unassigned.
    const authorsCell = el(
      "td",
      {},
      el(
        "div",
        { class: "source-authors" },
        authors.length
          ? authors.map((a) => el("span", { class: "author-chip", dataset: { section: a.beat } }, a.name))
          : [el("span", { class: "source-authors-none" }, t("No authors assigned"))],
      ),
    );

    const leanCell = el("td", {}, el("span", { class: "source-lean", dataset: { lean: source.lean || "neutral" } }, leanLabel(source.lean)));
    const descCell = el("td", { class: "source-desc" }, source.description || "");

    const dataRow = el("tr", { class: "source-row", dataset: { id: source.slug } }, [
      portalCell,
      authorsCell,
      leanCell,
      descCell,
      actionsCell,
    ]);

    const editForm = buildEditForm(source, cardStatus, () => setEditOpen(false));
    const editRow = el("tr", { class: "source-edit-row", dataset: { id: source.slug }, hidden: true }, [
      el("td", { colspan: "5" }, editForm),
    ]);
    function setEditOpen(open) {
      editRow.hidden = !open;
      editBtn.setAttribute("aria-expanded", open ? "true" : "false");
    }
    setEditOpen(false);
    editBtn.addEventListener("click", () => setEditOpen(editRow.hidden));

    return { dataRow, editRow };
  }

  // The inline edit form: every editable field, prefilled from the source. Domain is
  // carried from the existing row (not editable here) but sent explicitly along with
  // the slug, so the full upsert updates the row in place instead of forking a new one.
  // `onClose` collapses the edit row (Cancel); a successful save reloads the whole
  // table so it never has to close by hand.
  function buildEditForm(source, cardStatus, onClose) {
    const id = source.slug;
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

    const save = el("button", { type: "submit" }, t("Save"));
    const cancel = el("button", { type: "button", class: "secondary" }, t("Cancel"));

    const editForm = el("form", { class: "source-edit" }, [
      field(t("Description"), description, `se-${id}-desc`),
      helpField(t("Ownership group"), ownership, `se-${id}-ownership`, t(OWNERSHIP_HELP)),
      field(t("Homepage"), homepage, `se-${id}-homepage`),
      field(t("Feed URLs (comma separated)"), feeds, `se-${id}-feeds`),
      field(t("Feed type"), feedType, `se-${id}-feed-type`),
      field(t("Language"), language, `se-${id}-language`),
      field(t("Orientation"), lean, `se-${id}-lean`),
      checkField(t("Enabled"), enabled, `se-${id}-enabled`),
      el("div", { class: "source-actions" }, [save, cancel]),
    ]);
    cancel.addEventListener("click", () => onClose());
    editForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      // Full upsert: the collected form values override the carried row (explicit slug +
      // domain + preserved status columns), so the row is updated in place.
      const body = fullSourceBody(source, collectEditable(refs, { allowClear: true }));
      save.disabled = true;
      setStatus(cardStatus, "pending", t("Saving..."));
      try {
        await api.upsertSource(body);
        await reload();
        if (onChanged) onChanged();
      } catch (err) {
        save.disabled = false;
        setStatus(cardStatus, "error", t("Could not save ({code}): {msg}", { code: err.code, msg: err.message }));
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
  const select = el("select", { id }, LEANS.map((lean) => el("option", { value: lean }, leanLabel(lean))));
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
