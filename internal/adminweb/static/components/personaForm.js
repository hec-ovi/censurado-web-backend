import { el, field } from "./el.js";
import { t } from "./i18n.js";
import { slugify } from "../slugify.js";

// The harness's closed section vocabulary, mirrored from the backend's sections so
// the beat picker can only offer valid beats. Beat is carried in the author's
// metadata (the backend author row has no beat column of its own).
export const SECTIONS = ["tech", "world", "politics", "economics"];

// Gender options for the byline voice. Blank is allowed (unset).
export const GENDERS = ["female", "male", "nonbinary"];

// The create-author form. Submitting POSTs the explicit fields to POST /authors (a
// full upsert; the backend has no partial create). The handle is derived from the
// display name (the backend keys authors by handle). Beat, who-i-am, and language ride
// the author's metadata; style/about/gender/topics are first-class columns. On success
// it resets and calls onCreated(handle) so the roster can refresh.
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
  const topicsInput = el("input", { type: "text", id: "pf-topics", autocomplete: "off", placeholder: t("comma-separated") });
  const languageInput = el("input", { type: "text", id: "pf-language", autocomplete: "off" });
  const avatarInput = el("input", { type: "text", id: "pf-avatar", autocomplete: "off" });
  const submit = el("button", { type: "submit" }, t("Create author"));
  const status = el("p", { class: "form-status", role: "status", "aria-live": "polite" });

  const form = el("form", { class: "persona-form" }, [
    field(t("Display name"), nameInput, "pf-name"),
    field(t("Beat"), beatSelect, "pf-beat"),
    field(t("Gender"), genderSelect, "pf-gender"),
    field(t("Who I am"), whoInput, "pf-who"),
    field(t("Style"), styleInput, "pf-style"),
    field(t("About"), aboutInput, "pf-about"),
    field(t("Topics"), topicsInput, "pf-topics"),
    field(t("Language"), languageInput, "pf-language"),
    field(t("Avatar path"), avatarInput, "pf-avatar"),
    submit,
    status,
  ]);

  form.addEventListener("submit", onSubmit);

  async function onSubmit(event) {
    event.preventDefault();
    const required = [nameInput, beatSelect, whoInput, styleInput];
    required.forEach((node) => node.setAttribute("aria-invalid", "false"));

    const name = nameInput.value.trim();
    const beat = beatSelect.value;
    const who_i_am = whoInput.value.trim();
    const style = styleInput.value.trim();
    const handle = slugify(name);

    const missing = [];
    if (!name) missing.push(nameInput);
    if (!beat) missing.push(beatSelect);
    if (!who_i_am) missing.push(whoInput);
    if (!style) missing.push(styleInput);
    if (missing.length) {
      missing.forEach((node) => node.setAttribute("aria-invalid", "true"));
      missing[0].focus();
      setStatus("error", t("Display name, beat, who I am, and style are required."));
      return;
    }
    if (!handle) {
      nameInput.setAttribute("aria-invalid", "true");
      nameInput.focus();
      setStatus("error", t("Display name must contain letters or digits."));
      return;
    }

    const metadata = { beat, who_i_am };
    const language = languageInput.value.trim();
    if (language) metadata.language = language;

    const body = { handle, name, style, metadata };
    const gender = genderSelect.value;
    const about = aboutInput.value.trim();
    const avatar = avatarInput.value.trim();
    const topics = parseList(topicsInput.value);
    if (gender) body.gender = gender;
    if (about) body.about = about;
    if (avatar) body.avatar = avatar;
    if (topics.length) body.topics = topics;

    setBusy(true);
    setStatus("pending", t("Creating..."));
    try {
      const author = await api.upsertAuthor(body);
      setStatus("done", t("Created {id}.", { id: author.handle }));
      form.reset();
      if (onCreated) onCreated(author.handle);
    } catch (err) {
      setStatus("error", t("Could not create author: {msg}", { msg: err.message }));
    } finally {
      setBusy(false);
    }
  }

  function setBusy(busy) {
    submit.disabled = busy;
    form.setAttribute("aria-busy", busy ? "true" : "false");
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

// parseList splits a comma-separated field into a trimmed, de-duplicated,
// non-empty string array. An empty field yields [].
export function parseList(text) {
  const seen = new Set();
  const out = [];
  for (const raw of String(text || "").split(",")) {
    const v = raw.trim();
    if (v && !seen.has(v)) {
      seen.add(v);
      out.push(v);
    }
  }
  return out;
}
