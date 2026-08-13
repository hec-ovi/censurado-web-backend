import { el, field } from "./el.js";
import { EyeIcon } from "./icons.js";
import { t } from "./i18n.js";

// The Configuración view's two model cards, edited as WHAT ACTUALLY RUNS: the
// executor's heartbeat publishes the EFFECTIVE configuration (file config with
// the saved settings merged) plus a live probe of each lane, the forms show
// those real values, and saving stores them back. Two separate configurations
// with separate saves:
//
//   Modelos - the two lanes. LOCAL is the llama.cpp server; REMOTE is ANY
//             OpenAI-compatible inference API authenticated with a Bearer key
//             (OpenRouter, OpenAI, xAI/Grok, Groq, DeepSeek...) — the provider
//             preset fills the endpoint. The stored key loads into its field
//             masked as dots (an admin-session secrets read); the eye toggles
//             the real value. Each lane shows its live health with a Verify
//             refresh.
//   Etapas  - which lane each pipeline stage runs on. Local or Remote, nothing
//             else to decide: the lane's model applies.
//
// Each save merges its own half over the last-loaded settings object, so saving
// one card never clobbers the other. The stored wire key for the remote lane
// stays `openrouter` (the settings/derive contract); only the labels are
// provider-neutral.

const MODELS_HELP =
  "These are the endpoints and models the scheduled batches actually run on, live from the " +
  "executor. Edit and save: the next firing uses what you see here.";

const REMOTE_HELP_KEY = "Any OpenAI-compatible inference API with a Bearer key works here.";

const STAGES_HELP =
  "Each pipeline stage runs on the lane you pick: the local model or the remote API.";

const STAGES = [
  { key: "queries", label: "Research queries" },
  { key: "ledger", label: "Acta (research read)" },
  { key: "outline", label: "Outline" },
  { key: "draft", label: "Draft" },
  { key: "evaluate", label: "Gate (editor de mesa)" },
  { key: "finalize", label: "Corrector" },
];

// Known OpenAI-compatible providers; picking one fills the endpoint. All of
// them authenticate with `Authorization: Bearer <key>` on the same API shape.
const PROVIDERS = [
  ["https://openrouter.ai/api/v1", "OpenRouter"],
  ["https://api.openai.com/v1", "OpenAI"],
  ["https://api.x.ai/v1", "xAI (Grok)"],
  ["https://api.groq.com/openai/v1", "Groq"],
  ["https://api.deepseek.com/v1", "DeepSeek"],
];

// A heartbeat older than this is a dead executor's snapshot, not the truth.
const HEARTBEAT_FRESH_MS = 180000;

export function ModelsSection({ api } = {}) {
  let stored = {};        // the settings singleton as last loaded/saved (authoritative once set)
  let storedKey = "";     // the remote lane's stored key, kept OUT of `stored` so saves never round-trip it
  let effective = null;   // the executor's FRESH running config, null = stale or no heartbeat
  let health = {};        // {at, llama_ok, remote_state} from the freshest heartbeat
  let saving = false;     // one save at a time: the two buttons share the lock

  const laneInputs = {};
  const stageControls = {};
  const modelsStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
  const stagesStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
  const offlineNote = el("p", { class: "muted models-offline-note" });
  const saveModels = el("button", { type: "button" }, t("Save models"));
  const saveStages = el("button", { type: "button" }, t("Save stages"));

  // --- the masked key with its eye ------------------------------------------
  // The stored key is preloaded into the field masked as dots (the load is the
  // admin-session secrets read); the eye only flips visibility.
  const keyInput = el("input", {
    type: "password", id: "models-openrouter-key", autocomplete: "off",
  });
  const eye = el("button", {
    type: "button", class: "models-eye", "aria-label": t("Show the key"), "aria-pressed": "false",
  }, [EyeIcon("models-eye-icon")]);
  eye.addEventListener("click", () => {
    const show = keyInput.type === "password";
    keyInput.type = show ? "text" : "password";
    eye.setAttribute("aria-pressed", show ? "true" : "false");
  });
  keyInput.addEventListener("input", () => { keyInput.dataset.edited = "true"; });

  // --- per-lane health rows --------------------------------------------------
  const healthRows = {
    local: el("span", { class: "models-health", dataset: { lane: "local" } }),
    openrouter: el("span", { class: "models-health", dataset: { lane: "openrouter" } }),
  };

  function verifyButton() {
    const button = el("button", { type: "button", class: "secondary models-verify" }, t("Verify"));
    button.addEventListener("click", async () => {
      button.disabled = true;
      await fetchHealth();
      button.disabled = false;
    });
    return button;
  }

  function renderHealth() {
    const fresh = health.at && Date.now() - new Date(health.at).getTime() < HEARTBEAT_FRESH_MS;
    const age = fresh ? Math.max(0, Math.round((Date.now() - new Date(health.at).getTime()) / 1000)) : null;
    const stamp = age === null ? t("no report from the executor") : t("checked {s}s ago", { s: age });

    const localOk = fresh && health.llama_ok === true;
    fillHealth(healthRows.local,
      fresh ? (localOk ? "online" : "offline") : "offline",
      fresh ? (localOk ? t("healthy") : t("down")) : t("unknown"), stamp);

    const state = fresh ? (health.remote_state || "down") : null;
    const remoteText = { ok: t("healthy"), auth: t("key rejected"), down: t("down"), nokey: t("no key") };
    fillHealth(healthRows.openrouter,
      state === "ok" ? "online" : state === "nokey" ? "ready" : "offline",
      state ? remoteText[state] || t("down") : t("unknown"), stamp);
  }

  function fillHealth(row, dotState, text, stamp) {
    row.replaceChildren(
      el("span", { class: "status", dataset: { state: dotState } }, text),
      el("span", { class: "muted models-health-stamp" }, stamp),
    );
  }

  async function fetchHealth() {
    try {
      const statusData = await api.getAutomationStatus();
      const live = (statusData && statusData.settings) || {};
      health = { at: live.at, llama_ok: live.llama_ok, remote_state: live.remote_state };
      renderHealth();
    } catch {
      /* the rows keep their last state */
    }
  }

  // --- the cards -------------------------------------------------------------
  const localBase = el("input", { type: "text", id: "models-local-base" });
  const localModel = el("input", { type: "text", id: "models-local-model" });
  laneInputs.local = { base: localBase, model: localModel };

  const remoteBase = el("input", { type: "text", id: "models-openrouter-base" });
  const remoteModel = el("input", { type: "text", id: "models-openrouter-model" });
  laneInputs.openrouter = { base: remoteBase, model: remoteModel };

  const provider = el("select", { id: "models-provider" }, [
    el("option", { value: "" }, t("Provider...")),
    ...PROVIDERS.map(([url, label]) => el("option", { value: url }, label)),
  ]);
  provider.addEventListener("change", () => {
    if (provider.value) remoteBase.value = provider.value;
  });
  remoteBase.addEventListener("input", () => syncProvider());
  function syncProvider() {
    const hit = PROVIDERS.find(([url]) => url === remoteBase.value.trim());
    provider.value = hit ? hit[0] : "";
  }

  const localCard = el("div", { class: "models-lane" }, [
    el("div", { class: "models-lane-head" }, [
      el("h3", {}, t("Local (llama.cpp)")),
      healthRows.local,
      verifyButton(),
    ]),
    field(t("Endpoint"), localBase, "models-local-base"),
    field(t("Model"), localModel, "models-local-model"),
  ]);
  const remoteCard = el("div", { class: "models-lane" }, [
    el("div", { class: "models-lane-head" }, [
      el("h3", {}, t("Remote API")),
      healthRows.openrouter,
      verifyButton(),
    ]),
    el("p", { class: "muted models-lane-note" }, t(REMOTE_HELP_KEY)),
    field(t("Provider"), provider, "models-provider"),
    field(t("Endpoint"), remoteBase, "models-openrouter-base"),
    field(t("Model"), remoteModel, "models-openrouter-model"),
    el("div", { class: "field" }, [
      el("label", { for: "models-openrouter-key" }, t("API key")),
      el("div", { class: "models-key-row" }, [keyInput, eye]),
    ]),
  ]);

  const stageRows = STAGES.map((stage) => {
    const lane = el("select", { id: `models-stage-${stage.key}-lane` }, [
      el("option", { value: "local" }, t("Local (llama.cpp)")),
      el("option", { value: "openrouter" }, t("Remote API")),
    ]);
    stageControls[stage.key] = { lane };
    return el("tr", { dataset: { stage: stage.key } }, [
      el("td", {}, el("label", { for: `models-stage-${stage.key}-lane` }, t(stage.label))),
      el("td", {}, lane),
    ]);
  });

  const element = el("div", { class: "models-section" }, [
    el("section", { class: "models-card" }, [
      el("div", { class: "panel-head" }, [el("h2", {}, t("Models")), el("p", { class: "muted models-help" }, t(MODELS_HELP))]),
      offlineNote,
      el("div", { class: "models-lanes" }, [localCard, remoteCard]),
      el("div", { class: "source-actions models-actions" }, [saveModels]),
      modelsStatus,
    ]),
    el("section", { class: "models-card" }, [
      el("div", { class: "panel-head" }, [el("h2", {}, t("Stages")), el("p", { class: "muted models-help" }, t(STAGES_HELP))]),
      el("table", { class: "models-stages article-table" }, [
        el("thead", {}, el("tr", {}, [
          el("th", {}, t("Stage")),
          el("th", {}, t("Lane")),
        ])),
        el("tbody", {}, stageRows),
      ]),
      el("div", { class: "source-actions models-actions" }, [saveStages]),
      stagesStatus,
    ]),
  ]);

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
    syncProvider();
    keyInput.value = storedKey;
    keyInput.type = "password";
    delete keyInput.dataset.edited;
    eye.setAttribute("aria-pressed", "false");

    const effStages = (effective && effective.stages) || {};
    const storedStages = (stored && stored.stages) || {};
    for (const { key } of STAGES) {
      const saved = storedStages[key] || {};
      const eff = effStages[key] || {};
      stageControls[key].lane.value = saved.lane || eff.lane || "local";
    }
    offlineNote.textContent = effective ? "" : t("Executor offline: showing the last saved values.");
    offlineNote.hidden = !!effective;
  }

  function collectLanes() {
    const lanes = {
      local: {
        base_url: localBase.value.trim(),
        model: localModel.value.trim(),
      },
      openrouter: {
        base_url: remoteBase.value.trim(),
        model: remoteModel.value.trim(),
      },
    };
    // A typed key is sent; otherwise the SERVER keeps the stored one (the key
    // never round-trips through the browser on a routine save).
    const typed = keyInput.dataset.edited ? keyInput.value.trim() : "";
    if (typed) {
      lanes.openrouter.api_key = typed;
      storedKey = typed; // what the field shows (masked) after the save
    }
    for (const lane of Object.values(lanes)) {
      for (const [k, v] of Object.entries(lane)) if (v === "") delete lane[k];
    }
    return lanes;
  }

  function collectStages() {
    const stages = {};
    for (const { key } of STAGES) {
      stages[key] = { lane: stageControls[key].lane.value };
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
      // The secret lives in storedKey only; keep it out of the merge base.
      if (stored.lanes && stored.lanes.openrouter) delete stored.lanes.openrouter.api_key;
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
      // The settings load includes the stored secret (admin session), so the
      // key field opens filled and masked. The secret moves to storedKey and
      // leaves `stored`, keeping it out of every save body.
      const [settingsData, statusData] = await Promise.all([
        api.getAutomationSettingsSecrets(), api.getAutomationStatus(),
      ]);
      stored = (settingsData && settingsData.settings) || {};
      const so = (stored.lanes && stored.lanes.openrouter) || {};
      storedKey = so.api_key || "";
      delete so.api_key;
      const live = (statusData && statusData.settings) || {};
      health = { at: live.at, llama_ok: live.llama_ok, remote_state: live.remote_state };
      // Only a FRESH heartbeat counts: a dead executor's snapshot must never
      // pose as the running truth.
      const fresh = live.at && Date.now() - new Date(live.at).getTime() < HEARTBEAT_FRESH_MS;
      effective = fresh && live.effective && live.effective.lanes ? live.effective : null;
      fill();
      renderHealth();
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
