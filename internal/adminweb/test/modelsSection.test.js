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

test("loads the stored settings into lanes and stages, blanks meaning file default", async () => {
  server.use(
    http.get(`${ORIGIN}/automation-settings`, () =>
      HttpResponse.json({ settings: {
        lanes: { openrouter: { model: "deepseek/deepseek-chat" } },
        stages: { evaluate: { lane: "openrouter", model: "openai/gpt-5-mini" } },
      } })),
  );
  const section = mount();
  await section.reload();

  assert.equal(document.getElementById("models-openrouter-model").value, "deepseek/deepseek-chat");
  assert.equal(document.getElementById("models-local-model").value, "", "unset lane stays blank (file default)");
  assert.equal(document.getElementById("models-stage-evaluate-lane").value, "openrouter");
  assert.equal(document.getElementById("models-stage-evaluate-model").value, "openai/gpt-5-mini");
  assert.equal(document.getElementById("models-stage-draft-lane").value, "", "untouched stage stays on file default");
});

test("saves only the filled values, wholesale", async () => {
  let put = null;
  server.use(
    http.get(`${ORIGIN}/automation-settings`, () => HttpResponse.json({ settings: {} })),
    http.put(`${ORIGIN}/automation-settings`, async ({ request }) => {
      put = await request.json();
      return HttpResponse.json(put);
    }),
  );
  const user = userEvent.setup();
  const section = mount();
  await section.reload();

  await user.type(document.getElementById("models-openrouter-model"), "deepseek/deepseek-chat");
  await user.selectOptions(document.getElementById("models-stage-evaluate-lane"), "openrouter");
  await user.click(screen.getByRole("button", { name: "Save models" }));

  await waitFor(() => assert.ok(put, "a PUT should have been sent"));
  assert.deepEqual(put, { settings: {
    lanes: { openrouter: { model: "deepseek/deepseek-chat" } },
    stages: { evaluate: { lane: "openrouter" } },
  } }, "blank fields are omitted, not sent as empty strings");
  await screen.findByText("Models saved. The next firing uses them.");
});

test("surfaces a save failure without losing the form", async () => {
  server.use(
    http.get(`${ORIGIN}/automation-settings`, () => HttpResponse.json({ settings: {} })),
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
