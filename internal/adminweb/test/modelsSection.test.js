import "./setup.js";
import { test } from "node:test";
import assert from "node:assert/strict";
import { http, HttpResponse } from "msw";
import { screen, within, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { installServer, ORIGIN } from "./msw.js";
import { installDom } from "./dom.js";
import { ModelsSection } from "../static/components/modelsSection.js";
import { api } from "../static/api.js";

installDom();
const server = installServer();

function mount() {
  const section = ModelsSection({ api });
  document.body.appendChild(section.element);
  return section;
}

// The executor's heartbeat carries the EFFECTIVE running configuration; the
// forms edit those real values, never an abstract overlay.
function effective(over = {}) {
  return {
    lanes: {
      local: { base_url: "http://127.0.0.1:8080/v1", model: "qwen-local" },
      openrouter: { base_url: "https://openrouter.ai/api/v1", model: "poolside/laguna-s-2.1:free", key_set: true },
    },
    stages: {
      queries: { lane: "local", model: "qwen-local" },
      ledger: { lane: "local", model: "qwen-local" },
      outline: { lane: "local", model: "qwen-local" },
      draft: { lane: "local", model: "qwen-local" },
      evaluate: { lane: "openrouter", model: "poolside/laguna-s-2.1:free" },
      finalize: { lane: "local", model: "qwen-local" },
    },
    ...over,
  };
}

function stub({ settings = {}, eff = effective(), at = new Date().toISOString() } = {}) {
  server.use(
    http.get(`${ORIGIN}/automation-settings`, () => HttpResponse.json({ settings })),
    http.get(`${ORIGIN}/automation-status`, () =>
      HttpResponse.json({ settings: eff ? { at, effective: eff } : {} })),
  );
}

test("shows the EFFECTIVE endpoints and models as real editable values", async () => {
  stub();
  const section = mount();
  await section.reload();

  assert.equal(document.getElementById("models-local-base").value, "http://127.0.0.1:8080/v1");
  assert.equal(document.getElementById("models-local-model").value, "qwen-local");
  assert.equal(document.getElementById("models-openrouter-base").value, "https://openrouter.ai/api/v1");
  assert.equal(document.getElementById("models-openrouter-model").value, "poolside/laguna-s-2.1:free");
  // The key never round-trips: an empty password field plus the saved-state hint.
  assert.equal(document.getElementById("models-openrouter-key").value, "");
  await screen.findByText("Key saved. Leave empty to keep it.");
  // The stage rows show the lane each stage ACTUALLY runs on after the merge.
  assert.equal(document.getElementById("models-stage-evaluate-lane").value, "openrouter");
  assert.equal(document.getElementById("models-stage-draft-lane").value, "local");
  assert.equal(document.getElementById("models-stage-draft-model").placeholder, "qwen-local");
});

test("saving models stores the lanes (with a typed key) without touching the stages", async () => {
  let put = null;
  stub({ settings: { stages: { evaluate: { lane: "openrouter" } } },
         eff: effective({ lanes: { local: { base_url: "http://127.0.0.1:8080/v1", model: "qwen-local" },
                                   openrouter: { base_url: "https://openrouter.ai/api/v1", model: "poolside/laguna-s-2.1:free", key_set: false } } }) });
  server.use(
    http.put(`${ORIGIN}/automation-settings`, async ({ request }) => {
      put = await request.json();
      return HttpResponse.json(put);
    }),
  );
  const user = userEvent.setup();
  const section = mount();
  await section.reload();

  await screen.findByText("No key yet: paste it here or set OPENROUTER_API_KEY in .env.");
  const model = document.getElementById("models-openrouter-model");
  await user.clear(model);
  await user.type(model, "openai/gpt-5-mini");
  await user.type(document.getElementById("models-openrouter-key"), "sk-or-nueva");
  await user.click(screen.getByRole("button", { name: "Save models" }));

  await waitFor(() => assert.ok(put, "a PUT should have been sent"));
  assert.equal(put.settings.lanes.openrouter.model, "openai/gpt-5-mini");
  assert.equal(put.settings.lanes.openrouter.api_key, "sk-or-nueva", "the typed key is stored");
  assert.equal(put.settings.lanes.local.model, "qwen-local", "the visible local values persist");
  assert.deepEqual(put.settings.stages, { evaluate: { lane: "openrouter" } },
    "saving models never clobbers the stored stages");
  await screen.findAllByText("Saved. The next firing uses this.");
});

test("saving stages stores every stage's lane; the key never rides through the browser", async () => {
  let put = null;
  // The GET serves the key REDACTED (api_key_set); the server preserves the
  // real key on saves that do not carry one.
  stub({ settings: { lanes: { openrouter: { api_key_set: true } } } });
  server.use(
    http.put(`${ORIGIN}/automation-settings`, async ({ request }) => {
      put = await request.json();
      return HttpResponse.json(put);
    }),
  );
  const user = userEvent.setup();
  const section = mount();
  await section.reload();

  await screen.findByText("Key saved. Leave empty to keep it.");
  await user.selectOptions(document.getElementById("models-stage-draft-lane"), "openrouter");
  await user.type(document.getElementById("models-stage-draft-model"), "deepseek/deepseek-chat");
  await user.click(screen.getByRole("button", { name: "Save stages" }));

  await waitFor(() => assert.ok(put, "a PUT should have been sent"));
  assert.equal(put.settings.stages.draft.lane, "openrouter");
  assert.equal(put.settings.stages.draft.model, "deepseek/deepseek-chat");
  assert.equal(put.settings.stages.evaluate.lane, "openrouter", "the effective lanes persist explicitly");
  assert.equal(put.settings.stages.queries.lane, "local");
  assert.equal(put.settings.lanes.openrouter.api_key, undefined,
    "no raw key in the browser's PUT; the server preserves the stored one");
});

test("a stale heartbeat never poses as live: saved values win and the offline note shows", async () => {
  stub({
    settings: { lanes: { local: { base_url: "http://127.0.0.1:7777/v1", model: "recien-guardado" } } },
    at: "2026-08-13T00:00:00Z", // hours old: the executor is dead
  });
  const section = mount();
  await section.reload();

  await screen.findByText("Executor offline: showing the last saved values.");
  assert.equal(document.getElementById("models-local-base").value, "http://127.0.0.1:7777/v1");
  assert.equal(document.getElementById("models-local-model").value, "recien-guardado",
    "the dead executor's snapshot cannot mask what was saved");
});

test("with the executor offline it falls back to the saved values and says so", async () => {
  stub({ settings: { lanes: { local: { base_url: "http://127.0.0.1:9999/v1", model: "guardado" } } }, eff: null });
  const section = mount();
  await section.reload();

  await screen.findByText("Executor offline: showing the last saved values.");
  assert.equal(document.getElementById("models-local-base").value, "http://127.0.0.1:9999/v1");
  assert.equal(document.getElementById("models-local-model").value, "guardado");
});

test("surfaces a save failure without losing the form", async () => {
  stub();
  server.use(
    http.put(`${ORIGIN}/automation-settings`, () =>
      HttpResponse.json({ code: "insufficient_scope", detail: "requires admin:write" }, { status: 403 })),
  );
  const user = userEvent.setup();
  const section = mount();
  await section.reload();

  await user.click(screen.getByRole("button", { name: "Save models" }));
  const alert = await screen.findByRole("alert");
  assert.match(alert.textContent, /insufficient_scope/);
  assert.equal(screen.getByRole("button", { name: "Save models" }).disabled, false);
});
