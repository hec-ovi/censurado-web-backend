import { el, clear, field, help, isSafeImageSrc } from "./el.js";
import { t } from "./i18n.js";
import { slugify } from "../slugify.js";

// The harness's closed section vocabulary, mirrored from the backend's sections so
// the beat picker can only offer valid beats. Beat is carried in the author's
// metadata (the backend author row has no beat column of its own).
export const SECTIONS = ["tech", "world", "politics", "economics"];

// Gender options for the byline voice. Blank is allowed (unset).
export const GENDERS = ["female", "male", "nonbinary"];

const LINK_HELP =
  "An author only researches the sources linked here. Cross-source corroboration across them is what " +
  "drives relevance: a claim that several linked outlets carry independently rises, an unsourced one does not. " +
  "Link a few trusted, independent outlets per author.";

// The create-author form. Submitting POSTs the explicit fields to POST /authors (a
// full upsert; the backend has no partial create). The handle is derived from the
// display name (the backend keys authors by handle). Beat, who-i-am, and language ride
// the author's metadata; style/about/gender are first-class columns. On success it
// resets and calls onCreated(handle) so the roster can refresh.
export function PersonaForm({ api, onCreated } = {}) {
  const nameInput = el("input", { type: "text", id: "pf-name", autocomplete: "off" });
  // A blank first option so an operator must pick a beat; a blank submission is
  // rejected client-side (the invalid state) before any request goes out.
  const beatSelect = el("select", { id: "pf-beat" }, [
    el("option", { value: "" }, t("Select a beat")),
    ...SECTIONS.map((s) => el("option", { value: s }, s)),
  ]);
  const genderSelect = el("select", { id: "pf-gender" }, [
    el("option", { value: "" }, t("Unspecified")),
    ...GENDERS.map((g) => el("option", { value: g }, t(g))),
  ]);
  const whoInput = el("textarea", { id: "pf-who", rows: "2" });
  const styleInput = el("textarea", { id: "pf-style", rows: "2" });
  const aboutInput = el("textarea", { id: "pf-about", rows: "2" });
  const languageInput = el("input", { type: "text", id: "pf-language", autocomplete: "off" });
  const avatarInput = el("input", { type: "text", id: "pf-avatar", autocomplete: "off" });
  const avatarPreview = el("div", { class: "persona-avatar-preview", "aria-label": t("Avatar preview") });
  const sourceChecks = el("div", { class: "persona-source-checks persona-source-checks--create" });
  const sourceStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
  const submit = el("button", { type: "submit", disabled: true }, t("Create author"));
  const status = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
  let sourceBoxes = [];

  const form = el("form", { class: "persona-form" }, [
    el("div", { class: "persona-field-strip" }, [
      field(t("Display name"), nameInput, "pf-name"),
      field(t("Gender"), genderSelect, "pf-gender"),
      field(t("Beat"), beatSelect, "pf-beat"),
      field(t("Language"), languageInput, "pf-language"),
    ]),
    el("div", { class: "persona-avatar-row" }, [
      field(t("Avatar path"), avatarInput, "pf-avatar"),
      el("div", { class: "persona-avatar-preview-wrap" }, [
        el("span", { class: "field-label" }, t("Avatar preview")),
        avatarPreview,
      ]),
    ]),
    el("div", { class: "persona-prompt-grid" }, [
      field(t("Who I am"), whoInput, "pf-who"),
      field(t("Style"), styleInput, "pf-style"),
    ]),
    field(t("About"), aboutInput, "pf-about"),
    el("div", { class: "persona-sources persona-sources--create" }, [
      el("div", { class: "panel-head" }, [el("h4", {}, t("Linked sources")), help(t(LINK_HELP))]),
      sourceChecks,
      sourceStatus,
    ]),
    el("div", { class: "persona-actions persona-actions--profile" }, [submit]),
    status,
  ]);

  form.addEventListener("submit", onSubmit);
  const required = [nameInput, genderSelect, beatSelect, languageInput, avatarInput, whoInput, styleInput, aboutInput];
  required.forEach((node) => {
    node.addEventListener("input", updateSubmitState);
    node.addEventListener("change", updateSubmitState);
  });
  avatarInput.addEventListener("input", updateAvatarPreview);
  queueMicrotask(loadSources);
  updateAvatarPreview();
  updateSubmitState();

  async function onSubmit(event) {
    event.preventDefault();
    const name = nameInput.value.trim();
    const beat = beatSelect.value;
    const gender = genderSelect.value;
    const language = languageInput.value.trim();
    const who_i_am = whoInput.value.trim();
    const style = styleInput.value.trim();
    const about = aboutInput.value.trim();
    const avatar = avatarInput.value.trim();
    const handle = slugify(name);

    const missing = required.filter((node) => !node.value.trim());
    if (missing.length) {
      missing.forEach((node) => node.setAttribute("aria-invalid", "true"));
      missing[0].focus();
      setStatus("error", t("Complete all author fields before saving."));
      updateSubmitState();
      return;
    }
    if (!handle) {
      nameInput.setAttribute("aria-invalid", "true");
      nameInput.focus();
      setStatus("error", t("Display name must contain letters or digits."));
      return;
    }

    const metadata = { beat, who_i_am, language };

    const body = {
      handle,
      name,
      gender,
      about,
      style,
      avatar,
      metadata,
      sources: sourceBoxes.filter((item) => item.box.checked).map((item) => item.slug),
    };

    setBusy(true);
    setStatus("pending", t("Creating..."));
    try {
      const author = await api.upsertAuthor(body);
      setStatus("done", t("Created {id}.", { id: author.handle }));
      form.reset();
      sourceBoxes.forEach((item) => {
        item.box.checked = false;
      });
      updateAvatarPreview();
      updateSubmitState();
      if (onCreated) onCreated(author.handle);
    } catch (err) {
      setStatus("error", t("Could not create author: {msg}", { msg: err.message }));
    } finally {
      setBusy(false);
    }
  }

  function setBusy(busy) {
    submit.disabled = busy || !isComplete();
    form.setAttribute("aria-busy", busy ? "true" : "false");
  }
  function isComplete() {
    return required.every((node) => Boolean(node.value.trim()));
  }
  function updateSubmitState() {
    required.forEach((node) => node.setAttribute("aria-invalid", node.value.trim() ? "false" : "true"));
    submit.disabled = !isComplete() || form.getAttribute("aria-busy") === "true";
  }
  function updateAvatarPreview() {
    clear(avatarPreview);
    const src = avatarInput.value.trim();
    if (isSafeImageSrc(src)) {
      avatarPreview.append(el("img", { src, alt: "" }));
      return;
    }
    const initial = nameInput.value.trim().charAt(0).toUpperCase() || "?";
    avatarPreview.append(el("span", { class: "avatar avatar--fallback", "aria-hidden": "true" }, initial));
  }
  async function loadSources() {
    clear(sourceChecks);
    sourceBoxes = [];
    sourceChecks.append(el("p", { class: "muted" }, t("Loading sources...")));
    try {
      const data = await api.listSources();
      const sources = (data && data.sources) || [];
      clear(sourceChecks);
      if (!sources.length) {
        sourceChecks.append(el("p", { class: "muted" }, t("Add sources in the Sources tab first.")));
        return;
      }
      sourceChecks.append(sourceCheckboxList(sources, new Set(), sourceBoxes, "pf-source"));
    } catch (err) {
      clear(sourceChecks);
      sourceChecks.append(el("p", { class: "error", role: "alert" }, t("Could not load sources: {msg}", { msg: err.message })));
    }
  }
  function setStatus(state, text) {
    status.dataset.state = state;
    status.textContent = text;
    const assertive = state === "error";
    status.setAttribute("role", assertive ? "alert" : "status");
    status.setAttribute("aria-live", assertive ? "assertive" : "polite");
  }

  return { element: form };
}

export function sourceCheckboxList(sources, selected, boxes, prefix) {
  const tbody = el("tbody");
  for (const source of sources) {
    const boxId = `${prefix}-${source.slug}`;
    const box = el("input", { type: "checkbox", id: boxId });
    box.checked = selected.has(source.slug);
    boxes.push({ slug: source.slug, box });
    const desc = (source.description || "").trim();
    tbody.append(
      el("tr", { class: "link-row" }, [
        el("td", { class: "link-check" }, box),
        el("td", { class: "link-portal" }, el("label", { for: boxId }, source.domain)),
        el("td", { class: "link-lean" }, el("span", { class: "source-lean", dataset: { lean: source.lean || "neutral" } }, sourceLeanLabel(source.lean))),
        el("td", { class: "link-desc", title: desc }, desc || "-"),
      ]),
    );
  }
  return el("div", { class: "link-table-wrap" }, el("table", { class: "link-table" }, [
    el("thead", {}, el("tr", {}, [
      el("th", { class: "link-check" }, ""),
      el("th", {}, t("Source")),
      el("th", {}, t("Orientation")),
      el("th", {}, t("Description")),
    ])),
    tbody,
  ]));
}

export function sourceLeanLabel(lean) {
  if (lean === "right") return t("Right");
  if (lean === "left") return t("Left");
  return t("Neutral");
}
