import { el, field } from "./el.js";
import { t } from "./i18n.js";

// The Configuración view's two model cards, edited as WHAT ACTUALLY RUNS: the
// executor's heartbeat publishes the EFFECTIVE configuration (file config with
// the saved settings merged), the forms show those real values, and saving
// stores them back wholesale. Two separate configurations with separate saves:
//
//   Modelos - the two lanes: the local llama.cpp endpoint/model, the OpenRouter
//             endpoint/model and its API key (stored on save, never displayed
//             back; the heartbeat only reports whether one is available).
//   Etapas  - which lane each pipeline stage runs on, with an optional model
//             override per stage.
//
// Each save merges its own half over the last-loaded settings object, so saving
// one card never clobbers the other.

const MODELS_HELP =
  "These are the endpoints and models the scheduled batches actually run on, live from the " +
  "executor. Edit and save: the next firing uses what you see here.";

const STAGES_HELP =
  "Each pipeline stage runs on the lane you pick; the model override replaces that lane's " +
  "default model for the stage only.";

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

// A heartbeat older than this is a dead executor's snapshot, not the truth.
const HEARTBEAT_FRESH_MS = 180000;

export function ModelsSection({ api } = {}) {
  let stored = {};        // the settings singleton as last loaded/saved (authoritative once set)
  let effective = null;   // the executor's FRESH running config, null = stale or no heartbeat
  let saving = false;     // one save at a time: the two buttons share the lock

  const laneInputs = {};
  const stageControls = {};
  const modelsStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
  const stagesStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
  const keyHint = el("p", { class: "muted models-key-hint" });
  const offlineNote = el("p", { class: "muted models-offline-note" });
  const saveModels = el("button", { type: "button" }, t("Save models"));
  const saveStages = el("button", { type: "button" }, t("Save stages"));

  const keyInput = el("input", {
    type: "password", id: "models-openrouter-key", autocomplete: "off",
  });
  laneInputs.key = keyInput;

  const laneCards = LANES.map((lane) => {
    const base = el("input", { type: "text", id: `models-${lane.key}-base` });
    const model = el("input", { type: "text", id: `models-${lane.key}-model` });
    laneInputs[lane.key] = { base, model };
    const rows = [
      el("h3", {}, t(lane.label)),
      field(t("Endpoint"), base, `models-${lane.key}-base`),
      field(t("Model"), model, `models-${lane.key}-model`),
    ];
    if (lane.key === "openrouter") {
      rows.push(field(t("API key"), keyInput, "models-openrouter-key"), keyHint);
    }
    return el("div", { class: "models-lane" }, rows);
  });

  const stageRows = STAGES.map((stage) => {
    const lane = el("select", { id: `models-stage-${stage.key}-lane` },
      LANES.map((l) => el("option", { value: l.key }, t(l.label))));
    const model = el("input", { type: "text", id: `models-stage-${stage.key}-model`, placeholder: t("(lane default)") });
    stageControls[stage.key] = { lane, model };
    return el("tr", { dataset: { stage: stage.key } }, [
      el("td", {}, el("label", { for: `models-stage-${stage.key}-lane` }, t(stage.label))),
      el("td", {}, lane),
      el("td", {}, model),
    ]);
  });

  const element = el("div", { class: "models-section" }, [
    el("section", { class: "models-card" }, [
      el("div", { class: "panel-head" }, [el("h2", {}, t("Models")), el("p", { class: "muted models-help" }, t(MODELS_HELP))]),
      offlineNote,
      el("div", { class: "models-lanes" }, laneCards),
      el("div", { class: "source-actions models-actions" }, [saveModels]),
      modelsStatus,
    ]),
    el("section", { class: "models-card" }, [
      el("div", { class: "panel-head" }, [el("h2", {}, t("Stages")), el("p", { class: "muted models-help" }, t(STAGES_HELP))]),
      el("table", { class: "models-stages article-table" }, [
        el("thead", {}, el("tr", {}, [
          el("th", {}, t("Stage")),
          el("th", {}, t("Lane")),
          el("th", {}, t("Model override")),
        ])),
        el("tbody", {}, stageRows),
      ]),
      el("div", { class: "source-actions models-actions" }, [saveStages]),
      stagesStatus,
    ]),
  ]);

  // The model that applies for a lane, for stage placeholders: what the operator
  // SAVED wins (it is what the next firing merges in), the fresh heartbeat fills
  // the rest.
  function laneModel(laneKey) {
    const saved = stored.lanes && stored.lanes[laneKey] && stored.lanes[laneKey].model;
    const eff = effective && effective.lanes && effective.lanes[laneKey] && effective.lanes[laneKey].model;
    return saved || eff || "";
  }

  // Fill the forms with what the next firing RUNS: saved settings are
  // authoritative (the executor merges them at fire time); the fresh heartbeat
  // supplies everything not saved yet. A stale heartbeat never masks a save.
  function fill() {
    const effLanes = (effective && effective.lanes) || {};
    const storedLanes = (stored && stored.lanes) || {};
    for (const key of ["local", "openrouter"]) {
      const saved = storedLanes[key] || {};
      const eff = effLanes[key] || {};
      laneInputs[key].base.value = saved.base_url || eff.base_url || "";
      laneInputs[key].model.value = saved.model || eff.model || "";
    }
    keyInput.value = "";
    const so = storedLanes.openrouter || {};
    const keySet = !!(so.api_key || so.api_key_set)
      || !!(effLanes.openrouter && effLanes.openrouter.key_set);
    keyHint.textContent = keySet
      ? t("Key saved. Leave empty to keep it.")
      : t("No key yet: paste it here or set OPENROUTER_API_KEY in .env.");
    keyHint.dataset.state = keySet ? "set" : "missing";

    const effStages = (effective && effective.stages) || {};
    const storedStages = (stored && stored.stages) || {};
    for (const { key } of STAGES) {
      const saved = storedStages[key] || {};
      const eff = effStages[key] || {};
      const lane = saved.lane || eff.lane || "local";
      stageControls[key].lane.value = lane;
      stageControls[key].model.value = saved.model || "";
      stageControls[key].model.placeholder = laneModel(lane) || t("(lane default)");
    }
    offlineNote.textContent = effective ? "" : t("Executor offline: showing the last saved values.");
    offlineNote.hidden = !!effective;
  }

  // Switching a stage's lane retargets its placeholder to that lane's model.
  for (const { key } of STAGES) {
    stageControls[key].lane.addEventListener("change", () => {
      stageControls[key].model.placeholder = laneModel(stageControls[key].lane.value) || t("(lane default)");
    });
  }

  function collectLanes() {
    const lanes = {
      local: {
        base_url: laneInputs.local.base.value.trim(),
        model: laneInputs.local.model.value.trim(),
      },
      openrouter: {
        base_url: laneInputs.openrouter.base.value.trim(),
        model: laneInputs.openrouter.model.value.trim(),
      },
    };
    // A typed key is sent; otherwise the SERVER keeps the stored one (the key
    // never round-trips through the browser).
    const typed = keyInput.value.trim();
    if (typed) lanes.openrouter.api_key = typed;
    for (const lane of Object.values(lanes)) {
      for (const [k, v] of Object.entries(lane)) if (v === "") delete lane[k];
    }
    return lanes;
  }

  function collectStages() {
    const stages = {};
    for (const { key } of STAGES) {
      const stage = { lane: stageControls[key].lane.value };
      const model = stageControls[key].model.value.trim();
      if (model) stage.model = model;
      stages[key] = stage;
    }
    return stages;
  }

  async function saveHalf(button, statusNode, half) {
    if (saving) return; // the two saves share one lock: no clobbering races
    saving = true;
    saveModels.disabled = true;
    saveStages.disabled = true;
    setStatus(statusNode, "pending", t("Saving..."));
    try {
      const next = { ...stored, ...half };
      const data = await api.putAutomationSettings(next);
      stored = (data && data.settings) || next;
      fill(); // the form now shows exactly what was saved (= what fires next)
      setStatus(statusNode, "done", t("Saved. The next firing uses this."));
    } catch (err) {
      setStatus(statusNode, "error", t("Could not save ({code}): {msg}", { code: err.code, msg: err.message }));
    }
    saving = false;
    saveModels.disabled = false;
    saveStages.disabled = false;
  }

  saveModels.addEventListener("click", () => saveHalf(saveModels, modelsStatus, { lanes: collectLanes() }));
  saveStages.addEventListener("click", () => saveHalf(saveStages, stagesStatus, { stages: collectStages() }));

  async function reload() {
    try {
      const [settingsData, statusData] = await Promise.all([
        api.getAutomationSettings(), api.getAutomationStatus(),
      ]);
      stored = (settingsData && settingsData.settings) || {};
      const live = (statusData && statusData.settings) || {};
      // Only a FRESH heartbeat counts: a dead executor's snapshot must never
      // pose as the running truth.
      const fresh = live.at && Date.now() - new Date(live.at).getTime() < HEARTBEAT_FRESH_MS;
      effective = fresh && live.effective && live.effective.lanes ? live.effective : null;
      fill();
    } catch (err) {
      setStatus(modelsStatus, "error", t("Could not load model settings: {msg}", { msg: err.message }));
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
