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

function stubLists({ schedules = [], authors = [], status = {} } = {}) {
  server.use(
    http.get(`${ORIGIN}/schedules`, () => HttpResponse.json({ schedules })),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors })),
    http.get(`${ORIGIN}/automation-settings`, () => HttpResponse.json({ settings: {} })),
    http.get(`${ORIGIN}/automation-status`, () => HttpResponse.json({ settings: status })),
  );
}

// The schedule name renders in the table row AND the runs strip, so text queries
// are ambiguous; the row's data-id is the unambiguous handle.
async function rowFor(slug) {
  await waitFor(() => assert.ok(document.querySelector(`tr.automation-row[data-id="${slug}"]`)));
  return document.querySelector(`tr.automation-row[data-id="${slug}"]`);
}

test("lists schedules with status, next month/day, clock icons, and a production link", async () => {
  stubLists({ schedules: [schedule()] });
  const panel = mount();
  await panel.reload();

  const row = await rowFor("edicion-manana");
  assert.ok(within(row).getByText("Edición mañana"));
  assert.ok(within(row).getByText("Active"));
  assert.ok(within(row).getByText("auto"), "the mode rides under the name");

  const cells = row.querySelectorAll("td");
  // An enabled weekly schedule always has a next firing within a week, so the
  // Month cell carries a real value; the Day cell shows the schedule's pattern.
  assert.match(cells[2].textContent, /^[A-Z][a-z]{2}$/, "month short name");
  assert.equal(cells[3].textContent, "Mon, Fri", "the day pattern");

  // The fire times render as round clocks with their HH:MM beside them.
  const clocks = row.querySelectorAll("svg.clock-icon");
  assert.equal(clocks.length, 2);
  assert.equal(clocks[0].getAttribute("aria-label"), "07:30");
  assert.match(cells[4].textContent, /07:30/);

  // The production link points at the month archive of the next firing.
  const link = within(row).getByRole("link", { name: "open" });
  assert.match(link.href, /^https:\/\/elcensuradoweb\.com\/\d{4}\/\d{2}\/$/);

  // The runs strip flattens runs newest first, on the right half.
  const strip = document.querySelectorAll(".automation-run-row");
  assert.equal(strip.length, 2);
  assert.equal(strip[0].dataset.runId, "lote-2");
});

test("a paused schedule keeps a clean empty month gap but still shows its pattern", async () => {
  stubLists({ schedules: [schedule({ enabled: false, runs: [] })] });
  const panel = mount();
  await panel.reload();

  const row = await rowFor("edicion-manana");
  assert.ok(within(row).getByText("Paused"));
  const cells = row.querySelectorAll("td");
  assert.equal(cells[2].textContent, "", "month stays an empty gap (no next firing)");
  assert.equal(cells[3].textContent, "Mon, Fri", "the day pattern stays readable");
  assert.equal(within(row).queryByRole("link"), null, "no production link without a firing");
});

test("a queued firing shows Queued on its row until the worker picks it up", async () => {
  stubLists({ schedules: [schedule({ runs: [{ run_id: "lote-3", status: "queued", detail: "", started_at: "", finished_at: "" }] })] });
  const panel = mount();
  await panel.reload();

  const row = await rowFor("edicion-manana");
  assert.ok(within(row).getByText("Queued"));
});

test("the status card shows RUNNING with the queue while a batch is in flight", async () => {
  stubLists({
    schedules: [schedule()],
    status: { at: new Date().toISOString(), llama_ok: true, running: "edicion-20260813-1830", queued: ["noche-20260813-1830"] },
  });
  const panel = mount();
  await panel.reload();

  const card = document.querySelector(".automation-status");
  assert.ok(within(card).getByText("RUNNING"));
  assert.ok(within(card).getByText("edicion-20260813-1830"));
  assert.ok(within(card).getByText("noche-20260813-1830"), "the queue is visible");
  const healthy = within(card).getAllByText("healthy");
  assert.equal(healthy.length, 2, "executor and model both healthy");
});

test("the status card reports the executor offline on a stale heartbeat", async () => {
  stubLists({ schedules: [], status: { at: "2026-08-13T00:00:00Z", llama_ok: false, running: null, queued: [] } });
  const panel = mount();
  await panel.reload();

  const card = document.querySelector(".automation-status");
  assert.ok(within(card).getByText("Executor offline"));
  assert.equal(within(card).getAllByText("down").length, 2);
});

test("creates a weekly schedule from the calendar editor: time select, weekday header, chips", async () => {
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

  // One plain time select (30-minute steps) + Add; adding it again is refused.
  await user.selectOptions(within(dialog).getByRole("combobox", { name: "Time" }), "07:30");
  const add = within(dialog).getByRole("button", { name: "Add time" });
  await user.click(add);
  await user.click(add);
  await within(dialog).findByText("The time 07:30 is already on the list.");
  assert.equal(within(dialog).getAllByRole("listitem").length, 1, "one chip, no duplicate");
  assert.ok(dialog.querySelector(".time-chip svg.clock-icon"), "the chip carries its round clock");

  // The Weekly cadence TAB turns the calendar's weekday header into toggles.
  await user.click(within(dialog).getByRole("button", { name: "Weekly" }));
  await user.click(within(dialog).getByRole("button", { name: "Mon" }));
  await user.click(within(dialog).getByRole("button", { name: "Fri" }));

  await user.selectOptions(within(dialog).getByLabelText("Mode"), "auto");
  await user.click(within(dialog).getByLabelText("Giuliano"));
  await user.click(within(dialog).getByRole("button", { name: "Save" }));

  await waitFor(() => assert.ok(posted, "a POST should have been sent"));
  assert.equal(posted.name, "Edición tarde");
  assert.equal(posted.cadence, "weekly");
  assert.deepEqual(posted.times, ["07:30"]);
  assert.deepEqual(posted.weekdays, [1, 5]);
  assert.equal(posted.monthdays, undefined, "monthdays are not sent for a weekly cadence");
  assert.equal(posted.mode, "auto");
  assert.deepEqual(posted.authors, ["giuliano"]);
  assert.equal(posted.slug, undefined, "a create carries no slug; the server derives it");
});

test("a monthly schedule picks its days on the month grid", async () => {
  let posted = null;
  stubLists();
  server.use(
    http.post(`${ORIGIN}/schedules`, async ({ request }) => {
      posted = await request.json();
      return HttpResponse.json({ slug: "mensual" });
    }),
  );
  const user = userEvent.setup();
  const panel = mount();
  await panel.reload();

  await user.click(screen.getByRole("button", { name: "New schedule" }));
  const dialog = await screen.findByRole("dialog", { name: "Schedule editor" });
  await user.type(within(dialog).getByLabelText("Name"), "Mensual");
  await user.selectOptions(within(dialog).getByRole("combobox", { name: "Time" }), "09:00");
  await user.click(within(dialog).getByRole("button", { name: "Add time" }));

  await user.click(within(dialog).getByRole("button", { name: "Monthly" }));
  const calendar = dialog.querySelector(".month-calendar");
  assert.equal(calendar.dataset.mode, "monthly");
  await user.click(within(calendar).getByRole("button", { name: "1" }));
  await user.click(within(calendar).getByRole("button", { name: "15" }));
  await user.click(within(dialog).getByRole("button", { name: "Save" }));

  await waitFor(() => assert.ok(posted, "a POST should have been sent"));
  assert.equal(posted.cadence, "monthly");
  assert.deepEqual(posted.monthdays, [1, 15]);
  assert.deepEqual(posted.times, ["09:00"]);
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
  await user.click(within(dialog).getByLabelText("Active"));
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
    http.get(`${ORIGIN}/automation-settings`, () => HttpResponse.json({ settings: {} })),
    http.get(`${ORIGIN}/automation-status`, () => HttpResponse.json({ settings: {} })),
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
