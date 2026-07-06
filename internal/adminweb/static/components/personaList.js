import { el, clear, field, help, isSafeImageSrc, subTabs } from "./el.js";
import { t } from "./i18n.js";
import { SECTIONS, GENDERS } from "./personaForm.js";

// The author roster: a beat filter over GET /authors, rendering one card per
// author with an avatar (or initial fallback), beat badge, and who-i-am line.
// Each card carries an Edit action that opens a modal editor. The editor has a
// profile form (a full upsert of the author), a two-click Delete, and a Sources
// panel that links the author's source pool.
//
// `reload()` is exposed so the create form can refresh it after a synthesis.
// `onChanged` (optional) fires after any successful per-card mutation so a parent
// can refresh anything that depends on the author set.

const LINK_HELP =
  "An author only researches the sources linked here. Cross-source corroboration across them is what " +
  "drives relevance: a claim that several linked outlets carry independently rises, an unsourced one does not. " +
  "Link a few trusted, independent outlets per author.";

export function PersonaList({ api, onChanged } = {}) {
  const listEl = el("div", { class: "persona-list" });
  const element = el("section", { class: "panel persona-list-panel" }, [listEl]);

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
      for (const author of authors) listEl.append(card(author));
    } catch (err) {
      clear(listEl);
      listEl.append(el("p", { class: "error", role: "alert" }, t("Could not load authors: {msg}", { msg: err.message })));
    }
  }

  // A single author card: avatar/initial, name + beat badge, who-i-am preview,
  // the per-card actions, and the inline panels (built lazily on first open so
  // the roster stays light and a hidden beat <select> doesn't shadow the visible
  // beat badge in queries).
  function card(author) {
    const cardStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
    const meta = author.metadata || {};

    let editor = null;
    let dialog = null;

    const editBtn = el("button", { type: "button", class: "secondary" }, t("Edit"));
    editBtn.setAttribute("aria-expanded", "false");

    const actions = el("div", { class: "persona-actions" }, [editBtn]);

    const article = el("article", { class: "persona-card", dataset: { id: author.handle, section: meta.beat || "" } }, [
      avatar(author),
      el("div", { class: "persona-body" }, [
        el("div", { class: "persona-head" }, [
          el("h3", {}, author.name),
          el("span", { class: "badge" }, meta.beat),
        ]),
        el("p", { class: "persona-who" }, meta.who_i_am || ""),
      ]),
      actions,
      cardStatus,
    ]);

    editBtn.addEventListener("click", () => {
      if (!editor) {
        const closeEditor = () => {
          if (dialog && dialog.open) closeDialog(dialog);
        };
        editor = buildEditShell(author, cardStatus, closeEditor);
        dialog = buildEditDialog(author, editor.element);
        dialog.addEventListener("close", () => editBtn.setAttribute("aria-expanded", "false"));
        document.body.append(dialog);
        editor.loadSources();
      }
      editBtn.setAttribute("aria-expanded", "true");
      showDialog(dialog);
    });

    return article;
  }

  function buildEditDialog(author, content) {
    const meta = author.metadata || {};
    const titleId = `persona-dialog-${author.handle}-title`;
    const close = el("button", { type: "button", class: "secondary persona-dialog-close", "aria-label": t("Close") }, "×");
    const dialog = el("dialog", { class: "persona-dialog", "aria-labelledby": titleId });
    close.addEventListener("click", () => closeDialog(dialog));
    dialog.addEventListener("cancel", () => closeDialog(dialog));
    dialog.append(
      el("div", { class: "persona-dialog-shell" }, [
        el("div", { class: "persona-dialog-head" }, [
          el("div", {}, [
            el("span", { class: "section-link" }, meta.beat || ""),
            el("h2", { id: titleId }, author.name || t("Author editor")),
          ]),
          close,
        ]),
        content,
      ]),
    );
    return dialog;
  }

  function buildEditShell(author, cardStatus, closeEditor) {
    const sources = buildSourcesPanel(author);
    const tabs = subTabs(
      [
        { id: "profile", label: t("Profile"), content: buildEditForm(author, cardStatus, closeEditor) },
        { id: "sources", label: t("Sources"), content: sources.panel },
      ],
      { className: "editor-tabs", label: t("Author editor") },
    );
    return { element: el("div", { class: "persona-editor" }, tabs.element), loadSources: sources.load };
  }

  // The inline edit form: one compact identity row, the prompt-shaping fields, then
  // about. Sources stay in their own tab. The handle is immutable, so it is never an
  // input; hidden legacy fields (avatar/topics/few-shot metadata) are preserved.
  function buildEditForm(author, cardStatus, closeEditor) {
    const handle = author.handle;
    const meta = author.metadata || {};
    const displayName = el("input", { type: "text", id: `pe-${handle}-name`, value: author.name || "" });
    const beat = el("select", { id: `pe-${handle}-beat` }, SECTIONS.map((s) => el("option", { value: s }, s)));
    beat.value = meta.beat || SECTIONS[0];
    const gender = el("select", { id: `pe-${handle}-gender` }, [
      el("option", { value: "" }, t("Unspecified")),
      ...GENDERS.map((g) => el("option", { value: g }, t(g))),
    ]);
    gender.value = author.gender || "";
    const language = el("input", { type: "text", id: `pe-${handle}-language`, value: meta.language || "" });
    const whoIAm = el("textarea", { id: `pe-${handle}-who`, rows: "3" }, meta.who_i_am || "");
    const about = el("textarea", { id: `pe-${handle}-about`, rows: "3" }, author.about || "");
    const style = el("textarea", { id: `pe-${handle}-style`, rows: "3" }, author.style || "");

    const save = el("button", { type: "submit" }, t("Save"));
    const cancel = el("button", { type: "button", class: "secondary" }, t("Cancel"));
    const deleteBtn = el("button", { type: "button", class: "secondary danger" }, t("Delete"));
    let armed = false;
    function disarmDelete() {
      armed = false;
      deleteBtn.textContent = t("Delete");
      delete deleteBtn.dataset.confirm;
    }
    deleteBtn.addEventListener("click", () => {
      if (!armed) {
        armed = true;
        deleteBtn.textContent = t("Confirm");
        deleteBtn.dataset.confirm = "true";
        return;
      }
      act(deleteBtn, () => api.deleteAuthor(handle), cardStatus, disarmDelete).then((ok) => {
        if (ok) closeEditor();
      });
    });

    const form = el("form", { class: "persona-edit" }, [
      el("div", { class: "persona-field-strip" }, [
        field(t("Display name"), displayName, `pe-${handle}-name`),
        field(t("Gender"), gender, `pe-${handle}-gender`),
        field(t("Beat"), beat, `pe-${handle}-beat`),
        field(t("Language"), language, `pe-${handle}-language`),
      ]),
      el("div", { class: "persona-prompt-grid" }, [
        field(t("Who I am"), whoIAm, `pe-${handle}-who`),
        field(t("Style"), style, `pe-${handle}-style`),
      ]),
      field(t("About"), about, `pe-${handle}-about`),
      el("div", { class: "persona-actions persona-actions--profile" }, [save, cancel, deleteBtn]),
    ]);
    const required = [displayName, gender, beat, language, whoIAm, style, about];
    required.forEach((control) => {
      control.addEventListener("input", updateSaveState);
      control.addEventListener("change", updateSaveState);
    });
    updateSaveState();

    cancel.addEventListener("click", () => {
      closeEditor();
    });

    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      const missing = required.filter((control) => !control.value.trim());
      if (missing.length) {
        missing.forEach((control) => control.setAttribute("aria-invalid", "true"));
        missing[0].focus();
        setStatus(cardStatus, "error", t("Complete all author fields before saving."));
        updateSaveState();
        return;
      }

      // A full upsert: preserve the fields this form does not edit (bio and any
      // extra metadata keys) so they are not blanked. Beat/who-i-am/language and the
      // example lists ride metadata; sources are managed by the Sources sub-tab, so
      // `sources` is OMITTED here to leave the join untouched.
      const lang = language.value.trim();
      const body = {
        handle,
        name: displayName.value.trim(),
        bio: author.bio,
        gender: gender.value,
        about: about.value,
        style: style.value,
        avatar: author.avatar || "",
        topics: Array.isArray(author.topics) ? author.topics : [],
        metadata: {
          ...(author.metadata || {}),
          beat: beat.value,
          who_i_am: whoIAm.value,
          language: lang,
        },
      };

      save.disabled = true;
      setStatus(cardStatus, "pending", t("Saving..."));
      try {
        await api.upsertAuthor(body);
        await load();
        if (onChanged) onChanged();
        closeEditor();
      } catch (err) {
        save.disabled = false;
        setStatus(cardStatus, "error", t("Could not save ({code}): {msg}", { code: err.code, msg: err.message }));
      }
    });
    return form;

    function updateSaveState() {
      let complete = true;
      for (const control of required) {
        const ok = Boolean(control.value.trim());
        if (!ok) complete = false;
        control.setAttribute("aria-invalid", ok ? "false" : "true");
      }
      save.disabled = !complete;
    }
  }

  // The inline source-linking panel: every source as a checkbox, the author's
  // currently-linked slugs pre-checked. Save REPLACES the whole set (unchecking all
  // sends []). Loads the live source list and the author's stored slugs together.
  function buildSourcesPanel(author) {
    const handle = author.handle;
    const checksWrap = el("div", { class: "persona-source-checks" });
    const status = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
    const save = el("button", { type: "button" }, t("Save sources"));

    const panel = el("div", { class: "persona-sources" }, [
      el("div", { class: "panel-head" }, [el("h4", {}, t("Linked sources")), help(t(LINK_HELP))]),
      checksWrap,
      el("div", { class: "persona-actions" }, [save]),
      status,
    ]);

    let boxes = [];

    async function loadSources() {
      clear(checksWrap);
      checksWrap.append(el("p", { class: "muted" }, t("Loading sources...")));
      boxes = [];
      try {
        // Render every source as a checkbox: Save derives the linked set from the
        // rendered boxes, so an unrendered linked source would be dropped.
        const [sourcesData, linked] = await Promise.all([
          api.listSources(),
          api.getAuthorSources(handle),
        ]);
        const sources = (sourcesData && sourcesData.sources) || [];
        const selected = new Set((linked && linked.sources) || []);
        clear(checksWrap);
        if (!sources.length) {
          save.disabled = true;
          checksWrap.append(el("p", { class: "muted" }, t("Add sources in the Sources tab first.")));
          return;
        }
        save.disabled = false;
        const tbody = el("tbody");
        for (const source of sources) {
          const boxId = `ps-${handle}-${source.slug}`;
          const box = el("input", { type: "checkbox", id: boxId });
          box.checked = selected.has(source.slug);
          boxes.push({ id: source.slug, box });
          const desc = (source.description || "").trim();
          tbody.append(
            el("tr", { class: "link-row" }, [
              el("td", { class: "link-check" }, box),
              el("td", { class: "link-portal" }, el("label", { for: boxId }, source.domain)),
              el("td", { class: "link-lean" }, el("span", { class: "source-lean", dataset: { lean: source.lean || "neutral" } }, sourceLeanLabel(source.lean))),
              el("td", { class: "link-desc", title: desc }, desc || "—"),
            ]),
          );
        }
        const table = el("table", { class: "link-table" }, [
          el("thead", {}, el("tr", {}, [
            el("th", { class: "link-check" }, ""),
            el("th", {}, t("Source")),
            el("th", {}, t("Orientation")),
            el("th", {}, t("Description")),
          ])),
          tbody,
        ]);
        checksWrap.append(el("div", { class: "link-table-wrap" }, table));
      } catch (err) {
        clear(checksWrap);
        checksWrap.append(el("p", { class: "error", role: "alert" }, t("Could not load sources: {msg}", { msg: err.message })));
      }
    }

    save.addEventListener("click", async () => {
      const checked = boxes.filter((b) => b.box.checked).map((b) => b.id);
      save.disabled = true;
      setStatus(status, "pending", t("Saving..."));
      try {
        await api.putAuthorSources(handle, checked);
        setStatus(status, "done", t("Sources updated."));
        await loadSources();
        if (onChanged) onChanged();
      } catch (err) {
        save.disabled = false;
        setStatus(status, "error", t("Could not save sources ({code}): {msg}", { code: err.code, msg: err.message }));
      }
    });

    return { panel, load: loadSources };
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

function sourceLeanLabel(lean) {
  if (lean === "right") return t("Right");
  if (lean === "left") return t("Left");
  return t("Neutral");
}

function setStatus(node, state, text) {
  node.dataset.state = state;
  node.textContent = text;
  const assertive = state === "error";
  node.setAttribute("role", assertive ? "alert" : "status");
  node.setAttribute("aria-live", assertive ? "assertive" : "polite");
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
