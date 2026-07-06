import { el, clear, field, help, isSafeImageSrc } from "./el.js";
import { t } from "./i18n.js";
import { SECTIONS, GENDERS, sourceCheckboxList, sourceLeanLabel } from "./personaForm.js";

// The author roster: a GET /authors table with an avatar (or initial fallback),
// beat badge, and who-i-am line. Clicking a row opens the fullscreen modal editor.
// The editor has a profile form (a full upsert of the author), a two-click Delete,
// and a side panel that links the author's source pool.
//
// `reload()` is exposed so the create form can refresh it after a synthesis.
// `onChanged` (optional) fires after any successful per-card mutation so a parent
// can refresh anything that depends on the author set.

const LINK_HELP =
  "An author only researches the sources linked here. Cross-source corroboration across them is what " +
  "drives relevance: a claim that several linked outlets carry independently rises, an unsourced one does not. " +
  "Link a few trusted, independent outlets per author.";

export function PersonaList({ api, onChanged } = {}) {
  const listEl = el("div", { class: "persona-list article-table-host scroll-pane" });
  const element = el("section", { class: "panel panel-fill persona-list-panel authors-panel" }, [listEl]);

  async function load() {
    clear(listEl);
    listEl.append(el("p", { class: "muted" }, t("Loading authors...")));
    try {
      const data = await api.listAuthors();
      const authors = (data && data.authors) || [];
      clear(listEl);
      if (!authors.length) {
        listEl.append(el("p", { class: "muted" }, t("No authors yet. Create one to get started.")));
        return;
      }
      const tbody = el("tbody");
      authors.forEach((author, index) => tbody.append(row(author, index)));
      listEl.append(
        el("table", { class: "persona-table article-table" }, [
          el("thead", {}, el("tr", {}, [
            el("th", { class: "persona-avatar-head" }, ""),
            el("th", {}, t("Author")),
            el("th", {}, t("Beat")),
            el("th", {}, t("Who I am")),
          ])),
          tbody,
        ]),
      );
    } catch (err) {
      clear(listEl);
      listEl.append(el("p", { class: "error", role: "alert" }, t("Could not load authors: {msg}", { msg: err.message })));
    }
  }

  // A single author row. The fullscreen editor is built lazily on first open so
  // the roster stays light and a hidden beat <select> doesn't shadow the visible
  // beat badge in queries.
  function row(author, index) {
    const meta = author.metadata || {};

    let editor = null;
    let dialog = null;
    const tableRow = el("tr", { class: "persona-row article-row", tabindex: "0", dataset: { id: author.handle, section: meta.beat || "", parity: index % 2 === 0 ? "even" : "odd" } }, [
      el("td", { class: "persona-avatar-cell" }, avatar(author)),
      el("td", { class: "persona-name-cell" }, el("strong", {}, author.name)),
      el("td", { class: "persona-beat-cell" }, el("span", { class: "badge" }, meta.beat || t("(unset)"))),
      el("td", { class: "persona-who-cell" }, el("span", {}, meta.who_i_am || "")),
    ]);
    tableRow.setAttribute("aria-expanded", "false");

    function openAuthorEditor() {
      if (!editor) {
        const closeEditor = () => {
          if (dialog && dialog.open) closeDialog(dialog);
        };
        editor = buildEditShell(author, closeEditor);
        dialog = buildEditDialog(author, editor.element);
        dialog.addEventListener("close", () => {
          tableRow.setAttribute("aria-expanded", "false");
        });
        document.body.append(dialog);
        editor.loadSources();
      }
      tableRow.setAttribute("aria-expanded", "true");
      showDialog(dialog);
    }

    tableRow.addEventListener("click", openAuthorEditor);
    tableRow.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      openAuthorEditor();
    });

    return tableRow;
  }

  function buildEditDialog(author, content) {
    const close = el("button", { type: "button", class: "secondary persona-dialog-close", "aria-label": t("Close") }, "×");
    const dialog = el("dialog", { class: "persona-dialog", "aria-label": t("Author editor") });
    close.addEventListener("click", () => closeDialog(dialog));
    dialog.addEventListener("cancel", () => closeDialog(dialog));
    dialog.append(
      el("div", { class: "persona-dialog-shell" }, [
        close,
        content,
      ]),
    );
    return dialog;
  }

  function buildEditShell(author, closeEditor) {
    const editor = buildEditForm(author, closeEditor);
    return { element: el("div", { class: "persona-editor" }, editor.element), loadSources: editor.loadSources };
  }

  // The inline edit form: one compact identity row, the prompt-shaping fields, then
  // about. Sources stay in their own tab. The handle is immutable, so it is never an
  // input; hidden legacy fields (avatar/topics/few-shot metadata) are preserved.
  function buildEditForm(author, closeEditor) {
    const handle = author.handle;
    const meta = author.metadata || {};
    const displayName = el("input", { type: "text", id: `pe-${handle}-name`, value: author.name || "" });
    // Offer the canonical sections, but keep the author's current beat selectable even if
    // it is a legacy/off-list value (a removed or renamed section), so editing another
    // field never silently rewrites the beat to the first option.
    const beatOptions = meta.beat && !SECTIONS.includes(meta.beat) ? [meta.beat, ...SECTIONS] : SECTIONS;
    const beat = el("select", { id: `pe-${handle}-beat` }, beatOptions.map((s) => el("option", { value: s }, s)));
    beat.value = meta.beat || SECTIONS[0];
    const gender = el("select", { id: `pe-${handle}-gender` }, [
      el("option", { value: "" }, t("Unspecified")),
      ...GENDERS.map((g) => el("option", { value: g }, t(g))),
    ]);
    gender.value = author.gender || "";
    const language = el("input", { type: "text", id: `pe-${handle}-language`, value: meta.language || "" });
    const avatarPath = el("input", { type: "text", id: `pe-${handle}-avatar`, value: author.avatar || "" });
    const avatarPreview = el("div", { class: "persona-avatar-preview", "aria-label": t("Avatar preview") });
    const whoIAm = el("textarea", { id: `pe-${handle}-who`, rows: "3" }, meta.who_i_am || "");
    const about = el("textarea", { id: `pe-${handle}-about`, rows: "3" }, author.about || "");
    const style = el("textarea", { id: `pe-${handle}-style`, rows: "3" }, author.style || "");
    const sourceChecks = el("div", { class: "persona-source-checks" });
    const editStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });

    const restore = el("button", { type: "button", class: "secondary", disabled: true }, t("Restore"));
    const save = el("button", { type: "submit", disabled: true }, t("Save"));
    const deleteBtn = el("button", { type: "button", class: "secondary danger" }, t("Delete"));
    let deleteArmed = false;
    let saveArmed = false;
    let sourceBoxes = [];
    let sourcesLoaded = false;
    let initialProfile;
    let initialSources = null;
    function disarmDelete() {
      deleteArmed = false;
      deleteBtn.textContent = t("Delete");
      delete deleteBtn.dataset.confirm;
    }
    deleteBtn.addEventListener("click", () => {
      if (!deleteArmed) {
        deleteArmed = true;
        deleteBtn.textContent = t("Confirm");
        deleteBtn.dataset.confirm = "true";
        return;
      }
      act(deleteBtn, () => api.deleteAuthor(handle), editStatus, disarmDelete).then((ok) => {
        if (ok) closeEditor();
      });
    });

    const identityColumn = el("div", { class: "persona-edit-column persona-identity-column" }, [
      el("div", { class: "persona-avatar-row" }, [
        avatarPreview,
        field(t("Avatar path"), avatarPath, `pe-${handle}-avatar`),
      ]),
      field(t("Display name"), displayName, `pe-${handle}-name`),
      field(t("Gender"), gender, `pe-${handle}-gender`),
      field(t("Beat"), beat, `pe-${handle}-beat`),
      field(t("Language"), language, `pe-${handle}-language`),
      field(t("About"), about, `pe-${handle}-about`),
    ]);

    // The middle column is the prompt instructions: Who I am and Style split the column
    // height 50/50 (persona-prompt-grid--stacked), so each textarea fills half.
    const writingColumn = el("div", { class: "persona-edit-column persona-writing-column" }, [
      el("section", { class: "persona-prompt-section" }, [
        el("h3", { class: "persona-form-section-title" }, t("Prompt instructions")),
        el("div", { class: "persona-prompt-grid persona-prompt-grid--stacked" }, [
          field(t("Who I am"), whoIAm, `pe-${handle}-who`),
          field(t("Style"), style, `pe-${handle}-style`),
        ]),
      ]),
    ]);

    // Linked-sources filter: mirror the Sources view (orientation), but replace its
    // "author" facet with an "active on this author" toggle over the linked set.
    const activeFilter = el("select", { id: `pe-src-active-${handle}`, "aria-label": t("Filter by active on this author") },
      [["", t("All")], ["active", t("Active")], ["inactive", t("Inactive")]].map(([v, label]) => el("option", { value: v }, label)));
    const leanFilter = el("select", { id: `pe-src-lean-${handle}`, "aria-label": t("Filter by orientation") },
      [["", t("All")], ["right", sourceLeanLabel("right")], ["neutral", sourceLeanLabel("neutral")], ["left", sourceLeanLabel("left")]]
        .map(([v, label]) => el("option", { value: v }, label)));
    const clearSourceFilters = el("button", { type: "button", class: "secondary article-clear-filters" }, t("Clear filters"));
    const sourceFilterBar = el("div", { class: "article-filter-bar source-filter-bar persona-source-filter-bar" }, [
      el("fieldset", { class: "article-filter-group source-filter-group" }, [
        el("legend", {}, t("Active on this author")),
        activeFilter,
      ]),
      el("fieldset", { class: "article-filter-group source-filter-group" }, [
        el("legend", {}, t("Orientation")),
        leanFilter,
      ]),
      el("div", { class: "article-filter-actions" }, [clearSourceFilters]),
    ]);
    activeFilter.addEventListener("change", applySourceFilter);
    leanFilter.addEventListener("change", applySourceFilter);
    clearSourceFilters.addEventListener("click", () => {
      activeFilter.value = "";
      leanFilter.value = "";
      applySourceFilter();
    });

    const form = el("form", { class: "persona-edit" }, [
      el("div", { class: "persona-edit-layout" }, [
        identityColumn,
        writingColumn,
        el("aside", { class: "persona-sources persona-sources--side" }, [
          el("div", { class: "panel-head" }, [el("h4", {}, t("Linked sources")), help(t(LINK_HELP))]),
          sourceFilterBar,
          sourceChecks,
        ]),
      ]),
      el("div", { class: "persona-actions persona-actions--profile" }, [restore, save, deleteBtn]),
      editStatus,
    ]);
    const required = [displayName, gender, beat, language, avatarPath, whoIAm, style, about];
    required.forEach((control) => {
      control.addEventListener("input", markEdited);
      control.addEventListener("change", markEdited);
    });
    avatarPath.addEventListener("input", updateAvatarPreview);
    displayName.addEventListener("input", updateAvatarPreview);
    restore.addEventListener("click", restoreInitial);

    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (!isComplete()) {
        setStatus(editStatus, "error", t("Complete all author fields before saving."));
        updateDirtyState();
        return;
      }
      if (!isDirty()) return;
      if (!saveArmed) {
        saveArmed = true;
        save.textContent = t("Confirm");
        return;
      }

      const snapshot = profileSnapshot();
      const body = {
        handle,
        name: snapshot.name,
        bio: author.bio,
        gender: snapshot.gender,
        about: snapshot.about,
        style: snapshot.style,
        avatar: snapshot.avatar,
        topics: Array.isArray(author.topics) ? author.topics : [],
        metadata: {
          ...(author.metadata || {}),
          beat: snapshot.beat,
          who_i_am: snapshot.who,
          language: snapshot.language,
        },
      };

      save.disabled = true;
      restore.disabled = true;
      setStatus(editStatus, "pending", t("Saving..."));
      try {
        await api.upsertAuthor(body);
        const sourcesChanged = sourcesLoaded && initialSources != null && !sameList(sourceSelection(), initialSources);
        if (sourcesChanged) await api.putAuthorSources(handle, sourceSelection());
        initialProfile = profileSnapshot();
        if (sourcesLoaded) initialSources = sourceSelection();
        saveArmed = false;
        save.textContent = t("Save");
        updateDirtyState();
        setStatus(editStatus, "done", t("Saved."));
        await load();
        if (onChanged) onChanged();
      } catch (err) {
        setStatus(editStatus, "error", t("Could not save ({code}): {msg}", { code: err.code, msg: err.message }));
      } finally {
        updateDirtyState();
      }
    });

    initialProfile = profileSnapshot();
    updateAvatarPreview();
    updateDirtyState();
    return { element: form, loadSources };

    async function loadSources() {
      clear(sourceChecks);
      sourceBoxes = [];
      sourcesLoaded = false;
      sourceChecks.append(el("p", { class: "muted" }, t("Loading sources...")));
      try {
        const [sourcesData, linked] = await Promise.all([
          api.listSources(),
          api.getAuthorSources(handle),
        ]);
        const sources = (sourcesData && sourcesData.sources) || [];
        const selected = new Set((linked && linked.sources) || []);
        clear(sourceChecks);
        if (!sources.length) {
          sourceChecks.append(el("p", { class: "muted" }, t("Add sources in the Sources tab first.")));
          sourcesLoaded = true;
          initialSources = [];
          updateDirtyState();
          return;
        }
        sourceChecks.append(sourceCheckboxList(sources, selected, sourceBoxes, `pe-source-${handle}`));
        sourceBoxes.forEach((item) => item.box.addEventListener("change", () => {
          markEdited();
          applySourceFilter(); // a toggle can change what the active/inactive filter shows
        }));
        sourcesLoaded = true;
        initialSources = sourceSelection();
        applySourceFilter();
        updateDirtyState();
      } catch (err) {
        clear(sourceChecks);
        sourceChecks.append(el("p", { class: "error", role: "alert" }, t("Could not load sources: {msg}", { msg: err.message })));
      }
    }

    function profileSnapshot() {
      return {
        name: displayName.value.trim(),
        gender: gender.value,
        beat: beat.value,
        language: language.value.trim(),
        avatar: avatarPath.value.trim(),
        who: whoIAm.value.trim(),
        style: style.value.trim(),
        about: about.value.trim(),
      };
    }

    function applyProfile(snapshot) {
      displayName.value = snapshot.name;
      gender.value = snapshot.gender;
      beat.value = snapshot.beat;
      language.value = snapshot.language;
      avatarPath.value = snapshot.avatar;
      whoIAm.value = snapshot.who;
      style.value = snapshot.style;
      about.value = snapshot.about;
      updateAvatarPreview();
    }

    function sourceSelection() {
      return sourceBoxes.filter((item) => item.box.checked).map((item) => item.slug).sort((a, b) => a.localeCompare(b));
    }

    // Show/hide each linked-source row in place (never re-render, so the in-progress
    // selection survives): keep rows that match the "active on this author" toggle
    // (linked vs not) AND the orientation filter.
    function applySourceFilter() {
      const activeVal = activeFilter.value;
      const leanVal = leanFilter.value;
      for (const item of sourceBoxes) {
        if (!item.item) continue;
        const activeOk = !activeVal || (activeVal === "active" ? item.box.checked : !item.box.checked);
        const leanOk = !leanVal || (item.lean || "neutral") === leanVal;
        item.item.hidden = !(activeOk && leanOk);
      }
    }

    function applySources(slugs) {
      const selected = new Set(slugs || []);
      sourceBoxes.forEach((item) => {
        item.box.checked = selected.has(item.slug);
      });
    }

    function markEdited() {
      saveArmed = false;
      save.textContent = t("Save");
      if (editStatus.dataset.state === "done") clearStatus(editStatus);
      updateAvatarPreview();
      updateDirtyState();
    }

    function restoreInitial() {
      applyProfile(initialProfile);
      if (initialSources) applySources(initialSources);
      saveArmed = false;
      save.textContent = t("Save");
      clearStatus(editStatus);
      updateDirtyState();
    }

    function isComplete() {
      let complete = true;
      for (const control of required) {
        const ok = Boolean(control.value.trim());
        if (!ok) complete = false;
        control.setAttribute("aria-invalid", ok ? "false" : "true");
      }
      return complete;
    }

    function isDirty() {
      return !sameProfile(profileSnapshot(), initialProfile) || (initialSources != null && !sameList(sourceSelection(), initialSources));
    }

    function updateDirtyState() {
      const complete = isComplete();
      const dirty = isDirty();
      save.disabled = !complete || !dirty;
      restore.disabled = !dirty;
      if (!dirty) {
        saveArmed = false;
        save.textContent = t("Save");
      }
    }

    function updateAvatarPreview() {
      clear(avatarPreview);
      const src = avatarPath.value.trim();
      if (isSafeImageSrc(src)) {
        avatarPreview.append(el("img", { src, alt: "" }));
        return;
      }
      const initial = displayName.value.trim().charAt(0).toUpperCase() || "?";
      avatarPreview.append(el("span", { class: "avatar avatar--fallback", "aria-hidden": "true" }, initial));
    }
  }

  // Run a card action, then reload the roster. On success the card is replaced by
  // the fresh render, so the button is discarded; on failure it re-enables and the
  // card surfaces the backend's code/detail (e.g. author_in_use on delete).
  async function act(button, run, statusNode, onFail) {
    button.disabled = true;
    try {
      await run();
      await load();
      if (onChanged) onChanged();
      return true;
    } catch (err) {
      button.disabled = false;
      if (onFail) onFail();
      setStatus(statusNode, "error", t("Action failed ({code}): {msg}", { code: err.code, msg: err.message }));
      return false;
    }
  }

  return { element, reload: load };
}

function avatar(author) {
  if (isSafeImageSrc(author.avatar)) {
    return el("img", { class: "avatar", src: author.avatar, alt: author.name });
  }
  const initial = (author.name || "?").trim().charAt(0).toUpperCase() || "?";
  return el("div", { class: "avatar avatar--fallback", "aria-hidden": "true" }, initial);
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

function sameProfile(a, b) {
  if (!a || !b) return false;
  return a.name === b.name &&
    a.gender === b.gender &&
    a.beat === b.beat &&
    a.language === b.language &&
    a.avatar === b.avatar &&
    a.who === b.who &&
    a.style === b.style &&
    a.about === b.about;
}

function sameList(a, b) {
  if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
  return a.every((value, index) => value === b[index]);
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
