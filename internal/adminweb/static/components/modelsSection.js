import { el, field } from "./el.js";
import { t } from "./i18n.js";

// The Models section of the Automation tab: which model serves the pipeline.
// Two lanes (the local llama.cpp endpoint and OpenRouter) plus a per-stage
// routing table, saved wholesale into the backend's automation-settings
// singleton. The executor merges these settings over the pipeline's file config
// before each firing, so a blank field means "keep the file default". The
// OpenRouter API key never passes through here: it lives in the stack's .env
// (OPENROUTER_API_KEY) and only the executor reads it.

const MODELS_HELP =
  "These settings steer the scheduled batches: each stage of the pipeline runs on the lane you pick, " +
  "with an optional model override. A blank field keeps the pipeline file's default. The OpenRouter " +
  "key is read from the stack's .env (OPENROUTER_API_KEY), never stored here.";

// The pipeline's stage names (the node names in pipeline.config.json) with the
// labels the operator sees. The keys ARE the wire values.
const STAGES = [
  { key: "queries", label: "Research queries" },
  { key: "ledger", label: "Acta (research read)" },
  { key: "outline", label: "Outline" },
  { key: "draft", label: "Draft" },
  { key: "evaluate", label: "Gate (editor de mesa)" },
  { key: "finalize", label: "Corrector" },
];

const LANES = [
  { key: "local", label: "Local (llama.cpp)" },
  { key: "openrouter", label: "OpenRouter" },
];

export function ModelsSection({ api } = {}) {
  const laneInputs = {};
  const stageControls = {};
  const status = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
  const save = el("button", { type: "button" }, t("Save models"));

  const laneCards = LANES.map((lane) => {
    const base = el("input", { type: "text", id: `models-${lane.key}-base`, placeholder: t("(file default)") });
    const model = el("input", { type: "text", id: `models-${lane.key}-model`, placeholder: t("(file default)") });
    laneInputs[lane.key] = { base, model };
    return el("div", { class: "models-lane" }, [
      el("h3", {}, t(lane.label)),
      field(t("Endpoint"), base, `models-${lane.key}-base`),
      field(t("Model"), model, `models-${lane.key}-model`),
    ]);
  });

  const stageRows = STAGES.map((stage) => {
    const lane = el("select", { id: `models-stage-${stage.key}-lane` }, [
      el("option", { value: "" }, t("File default")),
      ...LANES.map((l) => el("option", { value: l.key }, t(l.label))),
    ]);
    const model = el("input", { type: "text", id: `models-stage-${stage.key}-model`, placeholder: t("(lane default)") });
    stageControls[stage.key] = { lane, model };
    return el("tr", { dataset: { stage: stage.key } }, [
      el("td", {}, el("label", { for: `models-stage-${stage.key}-lane` }, t(stage.label))),
      el("td", {}, lane),
      el("td", {}, model),
    ]);
  });

  const element = el("section", { class: "models-section" }, [
    el("div", { class: "panel-head" }, [el("h2", {}, t("Models")), el("p", { class: "muted models-help" }, t(MODELS_HELP))]),
    el("div", { class: "models-lanes" }, laneCards),
    el("table", { class: "models-stages article-table" }, [
      el("thead", {}, el("tr", {}, [
        el("th", {}, t("Stage")),
        el("th", {}, t("Lane")),
        el("th", {}, t("Model override")),
      ])),
      el("tbody", {}, stageRows),
    ]),
    el("div", { class: "source-actions models-actions" }, [save]),
    status,
  ]);

  function fill(settings) {
    const lanes = (settings && settings.lanes) || {};
    for (const key of Object.keys(laneInputs)) {
      laneInputs[key].base.value = (lanes[key] && lanes[key].base_url) || "";
      laneInputs[key].model.value = (lanes[key] && lanes[key].model) || "";
    }
    const stages = (settings && settings.stages) || {};
    for (const key of Object.keys(stageControls)) {
      stageControls[key].lane.value = (stages[key] && stages[key].lane) || "";
      stageControls[key].model.value = (stages[key] && stages[key].model) || "";
    }
  }

  // Collect only the filled values: the stored object stays minimal and every
  // blank keeps meaning "file default".
  function collect() {
    const settings = {};
    const lanes = {};
    for (const [key, inputs] of Object.entries(laneInputs)) {
      const lane = {};
      if (inputs.base.value.trim()) lane.base_url = inputs.base.value.trim();
      if (inputs.model.value.trim()) lane.model = inputs.model.value.trim();
      if (Object.keys(lane).length) lanes[key] = lane;
    }
    if (Object.keys(lanes).length) settings.lanes = lanes;
    const stages = {};
    for (const [key, controls] of Object.entries(stageControls)) {
      const stage = {};
      if (controls.lane.value) stage.lane = controls.lane.value;
      if (controls.model.value.trim()) stage.model = controls.model.value.trim();
      if (Object.keys(stage).length) stages[key] = stage;
    }
    if (Object.keys(stages).length) settings.stages = stages;
    return settings;
  }

  save.addEventListener("click", async () => {
    save.disabled = true;
    setStatus(status, "pending", t("Saving..."));
    try {
      await api.putAutomationSettings(collect());
      setStatus(status, "done", t("Models saved. The next firing uses them."));
    } catch (err) {
      setStatus(status, "error", t("Could not save ({code}): {msg}", { code: err.code, msg: err.message }));
    }
    save.disabled = false;
  });

  async function reload() {
    try {
      const data = await api.getAutomationSettings();
      fill((data && data.settings) || {});
    } catch (err) {
      setStatus(status, "error", t("Could not load model settings: {msg}", { msg: err.message }));
    }
  }

  return { element, reload };
}

function setStatus(node, state, text) {
  node.dataset.state = state;
  node.textContent = text;
  const assertive = state === "error";
  node.setAttribute("role", assertive ? "alert" : "status");
  node.setAttribute("aria-live", assertive ? "assertive" : "polite");
}
