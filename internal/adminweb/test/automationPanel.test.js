import "./setup.js";
import { test } from "node:test";
import assert from "node:assert/strict";
import { http, HttpResponse } from "msw";
import { screen, within, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { installServer, ORIGIN } from "./msw.js";
import { installDom } from "./dom.js";
import { AutomationPanel } from "../static/components/automationPanel.js";
import { api } from "../static/api.js";

installDom();
const server = installServer();

function mount() {
  const panel = AutomationPanel({ api });
  document.body.appendChild(panel.element);
  return panel;
}

// A full backend schedule row, as GET /schedules returns it.
function schedule(over = {}) {
  return {
    slug: "edicion-manana",
    name: "Edición mañana",
    cadence: "weekly",
    times: ["07:30", "18:00"],
    weekdays: [1, 5],
    monthdays: [],
    mode: "auto",
    authors: ["borge"],
    enabled: true,
    runs: [
      { run_id: "lote-2", status: "ok", detail: "5/8 published", started_at: "2026-08-12T07:30:00Z", finished_at: "2026-08-12T07:52:00Z" },
      { run_id: "lote-1", status: "failed", detail: "adapter down", started_at: "2026-08-11T07:30:00Z", finished_at: "" },
    ],
    metadata: {},
    deleted: false,
    ...over,
  };
}

function stubLists({ schedules = [], authors = [] } = {}) {
  server.use(
    http.get(`${ORIGIN}/schedules`, () => HttpResponse.json({ schedules })),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors })),
  );
}

// The schedule name renders in the table row AND the runs strip, so text queries
// are ambiguous; the row's data-id is the unambiguous handle.
async function rowFor(slug) {
  await waitFor(() => assert.ok(document.querySelector(`tr.automation-row[data-id="${slug}"]`)));
  return document.querySelector(`tr.automation-row[data-id="${slug}"]`);
}

test("lists schedules with cadence summary, mode, last outcome, and the runs strip", async () => {
  stubLists({ schedules: [schedule()] });
  const panel = mount();
  await panel.reload();

  const row = await rowFor("edicion-manana");
  assert.ok(within(row).getByText("Edición mañana"));
  assert.ok(within(row).getByText("Active"));
  assert.ok(within(row).getByText("Weekly"));
  assert.ok(within(row).getByText("Mon, Fri"), "weekday summary");
  assert.match(row.querySelector(".automation-cadence-times").textContent, /07:30/, "times shown");
  assert.ok(within(row).getByText("Auto"));
  assert.ok(within(row).getByText("ok"), "last outcome chip");
  assert.ok(within(row).getByText("5/8 published"), "last outcome detail");

  // The strip flattens runs newest first: lote-2 (ok) above lote-1 (failed).
  const strip = document.querySelectorAll(".automation-run-row");
  assert.equal(strip.length, 2);
  assert.equal(strip[0].dataset.runId, "lote-2");
  assert.equal(strip[1].dataset.runId, "lote-1");
  assert.ok(within(strip[1]).getByText("adapter down"));
});

test("a paused schedule shows Paused instead of a next-run forecast", async () => {
  stubLists({ schedules: [schedule({ enabled: false, runs: [] })] });
  const panel = mount();
  await panel.reload();

  const row = await rowFor("edicion-manana");
  const paused = within(row).getAllByText("Paused");
  assert.ok(paused.length >= 2, "the pill and the next-run cell both read Paused");
  await screen.findByText("No runs recorded yet.");
});

test("renders a clean zero-state for an empty registry", async () => {
  stubLists();
  const panel = mount();
  await panel.reload();
  await screen.findByText("No schedules yet. Create one to put the newsroom on a clock.");
});

test("creates a weekly schedule from the editor: chips, day grid, authors, full body", async () => {
  let posted = null;
  stubLists({ authors: [{ handle: "borge", name: "Borge" }, { handle: "giuliano", name: "Giuliano" }] });
  server.use(
    http.post(`${ORIGIN}/schedules`, async ({ request }) => {
      posted = await request.json();
      return HttpResponse.json({ slug: "edicion-tarde" });
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  await user.click(screen.getByRole("button", { name: "New schedule" }));
  const dialog = await screen.findByRole("dialog", { name: "Schedule editor" });
  await user.type(within(dialog).getByLabelText("Name"), "Edición tarde");

  // Two times via the chip entry; adding a duplicate is refused inline.
  const timeInput = within(dialog).getByLabelText("Times (several per day allowed)");
  const add = within(dialog).getByRole("button", { name: "Add time" });
  timeInput.value = "07:30";
  await user.click(add);
  timeInput.value = "18:00";
  await user.click(add);
  timeInput.value = "18:00";
  await user.click(add);
  await within(dialog).findByText("The time 18:00 is already on the list.");
  assert.equal(within(dialog).getAllByRole("listitem").length, 2, "two chips, no duplicate");

  // Weekly cadence reveals the weekday grid (and only that grid); pick Mon + Fri.
  await user.selectOptions(within(dialog).getByLabelText("Cadence"), "weekly");
  const weekdays = within(dialog).getByRole("group", { name: "Weekdays" });
  assert.equal(weekdays.closest(".field").hidden, false);
  // The monthday grid stays hidden for a weekly cadence (hidden elements carry
  // no accessible role, so reach it through the DOM).
  assert.ok(dialog.querySelector(".day-grid-month").closest(".field").hidden);
  await user.click(within(weekdays).getByRole("button", { name: "Mon" }));
  await user.click(within(weekdays).getByRole("button", { name: "Fri" }));

  await user.selectOptions(within(dialog).getByLabelText("Mode"), "auto");
  await user.click(within(dialog).getByLabelText("Giuliano"));
  await user.click(within(dialog).getByRole("button", { name: "Save" }));

  await waitFor(() => assert.ok(posted, "a POST should have been sent"));
  assert.equal(posted.name, "Edición tarde");
  assert.equal(posted.cadence, "weekly");
  assert.deepEqual(posted.times, ["07:30", "18:00"]);
  assert.deepEqual(posted.weekdays, [1, 5]);
  assert.equal(posted.monthdays, undefined, "monthdays are not sent for a weekly cadence");
  assert.equal(posted.mode, "auto");
  assert.deepEqual(posted.authors, ["giuliano"]);
  assert.equal(posted.enabled, true);
  assert.equal(posted.slug, undefined, "a create carries no slug; the server derives it");
});

test("refuses to save without a time, before any request is sent", async () => {
  let posted = false;
  stubLists();
  server.use(
    http.post(`${ORIGIN}/schedules`, () => {
      posted = true;
      return HttpResponse.json({});
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  await user.click(screen.getByRole("button", { name: "New schedule" }));
  const dialog = await screen.findByRole("dialog", { name: "Schedule editor" });
  await user.type(within(dialog).getByLabelText("Name"), "Sin horas");
  await user.click(within(dialog).getByRole("button", { name: "Save" }));

  await within(dialog).findByText("Add at least one time.");
  assert.equal(posted, false, "no request reaches the backend");
});

test("editing an existing schedule sends a full upsert with the explicit slug", async () => {
  let posted = null;
  stubLists({ schedules: [schedule()], authors: [{ handle: "borge", name: "Borge" }] });
  server.use(
    http.post(`${ORIGIN}/schedules`, async ({ request }) => {
      posted = await request.json();
      return HttpResponse.json({ slug: "edicion-manana" });
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  await user.click(await rowFor("edicion-manana"));
  const dialog = await screen.findByRole("dialog", { name: "Schedule editor" });
  await user.click(within(dialog).getByLabelText("Enabled"));
  await user.click(within(dialog).getByRole("button", { name: "Save" }));

  await waitFor(() => assert.ok(posted, "a POST should have been sent"));
  assert.equal(posted.slug, "edicion-manana", "the explicit slug pins the row");
  assert.equal(posted.enabled, false, "the toggle flips enabled");
  assert.deepEqual(posted.weekdays, [1, 5], "the carried weekdays survive the edit");
});

test("two-click delete removes the schedule by slug", async () => {
  let deleted = null;
  let removed = false;
  server.use(
    http.get(`${ORIGIN}/schedules`, () => HttpResponse.json({ schedules: removed ? [] : [schedule()] })),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })),
    http.delete(`${ORIGIN}/schedules/edicion-manana`, () => {
      deleted = "edicion-manana";
      removed = true;
      return new HttpResponse(null, { status: 204 });
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  const row = await rowFor("edicion-manana");
  const trash = within(row).getByRole("button", { name: "Delete" });
  await user.click(trash);
  assert.equal(deleted, null, "the first click only arms");
  await user.click(within(row).getByRole("button", { name: "Confirm" }));
  await waitFor(() => assert.equal(deleted, "edicion-manana"));
  await screen.findByText("No schedules yet. Create one to put the newsroom on a clock.");
});
