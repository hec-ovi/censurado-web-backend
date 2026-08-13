import { el, clear, field, help } from "./el.js";
import { TrashIcon } from "./icons.js";
import { t } from "./i18n.js";
import { ClockIcon } from "./clockIcon.js";
import { ModelsSection } from "./modelsSection.js";
import { MonthCalendar } from "./monthCalendar.js";
import { TimeGridPicker } from "./timeGridPicker.js";
import { nextRun, validateSchedule } from "../schedule.js";

// The Automation tab, split in two halves:
//   LEFT  - the schedule list: name, status, the next firing's month/day, the
//           fire times as round clock icons, and a link to that month's page on
//           the production site. Scrolls inside its own pane; empty cells stay
//           empty. Clicking a row opens the fullscreen calendar editor.
//   RIGHT - the operational side: the live status card (RUNNING while a batch
//           is in flight, the queue behind it, executor + model health from the
//           executor's heartbeat), the recent-runs strip, and the Models setup.
//
// The editor is calendar-shaped: a month grid to pick days (weekday header
// toggles for weekly, day toggles for monthly) and an hour grid with a :00/:30
// step for times. Edits are FULL upserts (explicit slug) via api.upsertSchedule;
// the runs strip and the status heartbeat are server-owned.
//
// Close firings QUEUE on the executor: a schedule due while another batch runs
// shows "queued" on its strip and fires as soon as the lock frees.

const AUTOMATION_HELP =
  "A schedule fires the newsroom's edition batch on its days at each listed time (the server's local " +
  "wall clock). Preview holds every article for approval; auto publishes as soon as the gate passes. " +
  "The executor runs while Docker runs: firings that come due while a batch is in flight queue behind " +
  "it, and times missed while the executor is off are skipped, never replayed.";

const AUTHORS_HELP =
  "Leave every author unchecked to run the whole newsroom: each author with a beat and linked sources " +
  "pitches candidates. Check authors to restrict the edition to them.";

// The public production origin the per-row link points at (its /YYYY/MM/ month
// archive). The panel itself stays localhost-only; this is only an href.
const PRODUCTION_BASE = "https://elcensuradoweb.com";

const RUN_STATE = { queued: "ready", running: "running", ok: "done", failed: "failed" };
const MONTH_SHORT = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

export function AutomationPanel({ api, onChanged, refreshMs = 0 } = {}) {
  let schedules = [];
  let authors = [];
  let liveStatus = {};
  const models = ModelsSection({ api });

  const listEl = el("div", { class: "automation-list article-table-host scroll-pane automation-scroll" });
  const runsEl = el("div", { class: "automation-runs" });
  const statusEl = el("div", { class: "automation-status", role: "status", "aria-label": t("Executor status") });
  const listStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
  const newBtn = el("button", { type: "button", class: "automation-new" }, t("New schedule"));
  newBtn.addEventListener("click", () => openEditor(null));

  const element = el("div", { class: "automation workspace-section" }, el("section", { class: "panel panel-fill automation-panel" }, [
    el("div", { class: "automation-split" }, [
      el("div", { class: "automation-half automation-schedules" }, [
        el("div", { class: "panel-head automation-head" }, [
          el("h2", {}, t("Schedules")),
          help(t(AUTOMATION_HELP)),
          el("div", { class: "automation-head-actions" }, [newBtn]),
        ]),
        listEl,
        listStatus,
      ]),
      el("div", { class: "automation-half automation-ops" }, [
        statusEl,
        el("div", { class: "panel-head automation-runs-head" }, [el("h2", {}, t("Recent runs"))]),
        runsEl,
        models.element,
      ]),
    ]),
  ]));

  async function reload() {
    clear(listEl);
    listEl.append(el("p", { class: "muted" }, t("Loading schedules...")));
    clearStatus(listStatus);
    try {
      const [scheduleData, authorData, statusData] = await Promise.all([
        api.listSchedules(), api.listAuthors(), api.getAutomationStatus(),
      ]);
      schedules = (scheduleData && scheduleData.schedules) || [];
      authors = (authorData && authorData.authors) || [];
      liveStatus = (statusData && statusData.settings) || {};
      renderTable();
      renderRuns();
      renderStatus();
      await models.reload();
    } catch (err) {
      clear(listEl);
      listEl.append(el("p", { class: "error", role: "alert" }, t("Could not load schedules: {msg}", { msg: err.message })));
    }
  }

  // The light refresh the poll timer runs: schedules + heartbeat only, so an
  // open editor or a half-typed Models form is never touched.
  async function refresh() {
    try {
      const [scheduleData, statusData] = await Promise.all([api.listSchedules(), api.getAutomationStatus()]);
      schedules = (scheduleData && scheduleData.schedules) || [];
      liveStatus = (statusData && statusData.settings) || {};
      renderTable();
      renderRuns();
      renderStatus();
    } catch {
      /* transient; the next poll retries */
    }
  }
  if (refreshMs > 0) {
    const timer = setInterval(refresh, refreshMs);
    // Under Node (jsdom tests) the timer must not hold the event loop open;
    // browsers return a number and ignore this.
    if (typeof timer === "object" && typeof timer.unref === "function") timer.unref();
  }

  // ---- the live status card -------------------------------------------------

  function renderStatus() {
    clear(statusEl);
    const running = liveStatus.running
      || (schedules.some((s) => (s.runs || [])[0]?.status === "running")
        ? (schedules.find((s) => (s.runs || [])[0]?.status === "running").runs[0].run_id) : null);
    const queued = liveStatus.queued || [];
    const beatAt = liveStatus.at ? new Date(liveStatus.at) : null;
    const executorUp = beatAt !== null && Date.now() - beatAt.getTime() < 180000;

    statusEl.append(el("div", { class: "automation-status-main", dataset: { running: running ? "true" : "false" } },
      running
        ? [el("span", { class: "status", dataset: { state: "running" } }, t("RUNNING")),
           el("span", { class: "automation-status-run" }, running)]
        : [el("span", { class: "status", dataset: { state: executorUp ? "active" : "offline" } },
             executorUp ? t("Idle") : t("Executor offline")),
           queued.length === 0 ? el("span", { class: "muted" }, t("No batch in flight.")) : null]));
    if (queued.length) {
      statusEl.append(el("div", { class: "automation-status-queue" }, [
        el("span", { class: "muted" }, t("Queued:")),
        ...queued.map((id) => el("span", { class: "badge" }, id)),
      ]));
    }
    statusEl.append(el("div", { class: "automation-status-health" }, [
      healthDot(t("Executor"), executorUp),
      healthDot(t("Model (llama.cpp)"), liveStatus.llama_ok === true),
    ]));
  }

  function healthDot(label, ok) {
    return el("span", { class: "automation-health" }, [
      el("span", { class: "status", dataset: { state: ok ? "online" : "offline" } }, ok ? t("healthy") : t("down")),
      el("span", { class: "automation-health-label" }, label),
    ]);
  }

  // ---- the schedule table ---------------------------------------------------

  function renderTable() {
    clear(listEl);
    if (!schedules.length) {
      listEl.append(el("p", { class: "muted empty-state" }, t("No schedules yet. Create one to put the newsroom on a clock.")));
      return;
    }
    const now = new Date();
    const ordered = [...schedules].sort((a, b) => {
      const an = nextRun(a, now);
      const bn = nextRun(b, now);
      if (an && bn) return an - bn;
      if (an) return -1;
      if (bn) return 1;
      return (a.name || a.slug).localeCompare(b.name || b.slug);
    });
    const tbody = el("tbody", {});
    ordered.forEach((schedule, index) => tbody.append(row(schedule, index, now)));
    listEl.append(
      el("table", { class: "automation-table article-table" }, [
        el("thead", {}, el("tr", {}, [
          el("th", {}, t("Schedule")),
          el("th", {}, t("Status")),
          el("th", {}, t("Month")),
          el("th", {}, t("Day")),
          el("th", {}, t("Time")),
          el("th", {}, t("Prod")),
          el("th", { class: "automation-delete-head" }, t("Delete")),
        ])),
        tbody,
      ]),
    );
  }

  function row(schedule, index, now) {
    const nameCell = el("td", {}, el("div", { class: "automation-name" }, [
      el("span", { class: "automation-title" }, schedule.name || schedule.slug),
      el("span", { class: "muted automation-mode-word" }, schedule.mode === "auto" ? t("auto") : t("preview")),
    ]));

    const newest = (schedule.runs || [])[0];
    let state = schedule.enabled ? "active" : "offline";
    let stateLabel = schedule.enabled ? t("Active") : t("Paused");
    if (newest && (newest.status === "running" || newest.status === "queued")) {
      state = RUN_STATE[newest.status];
      stateLabel = newest.status === "running" ? t("RUNNING") : t("Queued");
    }
    const statusCell = el("td", {}, el("span", { class: "status", dataset: { state } }, stateLabel));

    const next = schedule.enabled ? nextRun(schedule, now) : null;
    const monthCell = el("td", {}, next ? MONTH_SHORT[next.getMonth()] : "");
    const dayCell = el("td", {}, next ? String(next.getDate()) : "");

    const times = [...(schedule.times || [])].sort();
    const shown = times.slice(0, 4);
    const timeCell = el("td", {}, el("span", { class: "automation-clocks" }, [
      ...shown.map((time) => ClockIcon(time)),
      times.length > shown.length ? el("span", { class: "muted" }, `+${times.length - shown.length}`) : null,
    ]));

    // The production month page this schedule's next (or last) firing lands on.
    const linkDate = next || (newest && newest.started_at ? new Date(newest.started_at) : null);
    const prodCell = el("td", {}, linkDate
      ? el("a", {
          class: "automation-prod-link",
          href: `${PRODUCTION_BASE}/${linkDate.getFullYear()}/${String(linkDate.getMonth() + 1).padStart(2, "0")}/`,
          target: "_blank", rel: "noopener",
          onClick: (event) => event.stopPropagation(),
        }, t("open"))
      : "");

    const cardStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
    // Two-click delete instead of window.confirm (a no-op under jsdom).
    let armed = false;
    const deleteBtn = el("button", { type: "button", class: "source-trash", "aria-label": t("Delete") }, TrashIcon("article-trash-icon"));
    deleteBtn.addEventListener("click", (event) => {
      event.stopPropagation();
      if (!armed) {
        armed = true;
        clear(deleteBtn);
        deleteBtn.append(t("Confirm"));
        deleteBtn.setAttribute("aria-label", t("Confirm"));
        deleteBtn.dataset.confirm = "true";
        return;
      }
      act(deleteBtn, () => api.deleteSchedule(schedule.slug), cardStatus);
    });
    const actionsCell = el("td", {}, [el("div", { class: "source-actions source-actions--stack" }, [deleteBtn]), cardStatus]);

    const tableRow = el("tr", { class: "automation-row article-row", tabindex: "0", dataset: { id: schedule.slug, parity: index % 2 === 0 ? "even" : "odd" } }, [
      nameCell, statusCell, monthCell, dayCell, timeCell, prodCell, actionsCell,
    ]);
    tableRow.addEventListener("click", () => openEditor(schedule));
    tableRow.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      openEditor(schedule);
    });
    return tableRow;
  }

  // The recent-runs strip: the newest run records across every schedule.
  function renderRuns() {
    clear(runsEl);
    const all = [];
    for (const schedule of schedules) {
      for (const run of schedule.runs || []) {
        all.push({ schedule: schedule.name || schedule.slug, ...run });
      }
    }
    all.sort((a, b) => String(b.started_at || b.run_id).localeCompare(String(a.started_at || a.run_id)));
    const latest = all.slice(0, 8);
    if (!latest.length) {
      runsEl.append(el("p", { class: "muted empty-state" }, t("No runs recorded yet.")));
      return;
    }
    for (const run of latest) {
      runsEl.append(el("div", { class: "automation-run-row", dataset: { runId: run.run_id } }, [
        el("span", { class: "status", dataset: { state: RUN_STATE[run.status] || "running" } }, runLabel(run.status)),
        el("span", { class: "automation-run-schedule" }, run.schedule),
        el("span", { class: "automation-run-detail" }, run.detail || run.run_id),
        el("span", { class: "automation-run-when muted" }, (run.started_at || "").replace("T", " ").slice(0, 16)),
      ]));
    }
  }

  // ---- the calendar editor --------------------------------------------------

  function openEditor(schedule) {
    let dialog = null;
    const closeEditor = () => {
      if (dialog) closeDialog(dialog);
    };
    const form = buildEditForm(schedule, closeEditor);
    const close = el("button", { type: "button", class: "secondary source-dialog-close", "aria-label": t("Close") }, "×");
    dialog = el("dialog", { class: "source-dialog automation-dialog", "aria-label": t("Schedule editor") });
    close.addEventListener("click", closeEditor);
    dialog.addEventListener("cancel", closeEditor);
    dialog.addEventListener("close", () => dialog.remove(), { once: true });
    dialog.append(el("div", { class: "source-dialog-shell" }, [close, form]));
    document.body.append(dialog);
    showDialog(dialog);
  }

  function buildEditForm(schedule, onClose) {
    const isNew = !schedule;
    const id = isNew ? "new" : schedule.slug;
    const draft = {
      times: [...((schedule && schedule.times) || [])],
      weekdays: new Set((schedule && schedule.weekdays) || []),
      monthdays: new Set((schedule && schedule.monthdays) || []),
      authors: new Set((schedule && schedule.authors) || []),
    };

    const name = el("input", { type: "text", id: `sch-${id}-name`, value: (schedule && schedule.name) || "" });
    const cadence = el("select", { id: `sch-${id}-cadence` }, [
      el("option", { value: "daily" }, t("Daily")),
      el("option", { value: "weekly" }, t("Weekly")),
      el("option", { value: "monthly" }, t("Monthly")),
    ]);
    cadence.value = (schedule && schedule.cadence) || "daily";
    const mode = el("select", { id: `sch-${id}-mode` }, [
      el("option", { value: "preview" }, t("Preview (hold for approval)")),
      el("option", { value: "auto" }, t("Auto (publish on gate pass)")),
    ]);
    mode.value = (schedule && schedule.mode) || "preview";
    const enabled = el("input", { type: "checkbox", id: `sch-${id}-enabled` });
    enabled.checked = isNew ? true : !!schedule.enabled;

    // Days: the month calendar; its meaning follows the cadence.
    const calendar = MonthCalendar({ weekdays: draft.weekdays, monthdays: draft.monthdays });
    calendar.setMode(cadence.value);
    cadence.addEventListener("change", () => calendar.setMode(cadence.value));

    // Times: the hour grid (30-minute step) feeding the chip list.
    const chips = el("div", { class: "time-chips", role: "list", "aria-label": t("Times") });
    const editStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
    function renderChips() {
      clear(chips);
      if (!draft.times.length) {
        chips.append(el("span", { class: "muted" }, t("No times yet.")));
        return;
      }
      for (const time of [...draft.times].sort()) {
        const remove = el("button", { type: "button", class: "time-chip-remove", "aria-label": t("Remove {time}", { time }) }, "×");
        remove.addEventListener("click", () => {
          draft.times = draft.times.filter((x) => x !== time);
          renderChips();
        });
        chips.append(el("span", { class: "time-chip", role: "listitem" }, [ClockIcon(time, { size: 14 }), time, remove]));
      }
    }
    const picker = TimeGridPicker({
      onAdd: (hhmm) => {
        if (draft.times.includes(hhmm)) {
          setStatus(editStatus, "error", t("The time {time} is already on the list.", { time: hhmm }));
          return;
        }
        draft.times.push(hhmm);
        clearStatus(editStatus);
        renderChips();
      },
    });
    renderChips();

    const authorBoxes = authors.map((author) => {
      const box = el("input", { type: "checkbox", id: `sch-${id}-author-${author.handle}` });
      box.checked = draft.authors.has(author.handle);
      box.addEventListener("change", () => {
        if (box.checked) draft.authors.add(author.handle);
        else draft.authors.delete(author.handle);
      });
      return el("span", { class: "automation-author-choice" }, [
        box,
        el("label", { for: `sch-${id}-author-${author.handle}` }, author.name || author.handle),
      ]);
    });

    const save = el("button", { type: "submit" }, t("Save"));
    const cancel = el("button", { type: "button", class: "secondary" }, t("Cancel"));

    const editForm = el("form", { class: "source-edit source-edit-full automation-edit" }, [
      el("div", { class: "automation-edit-grid" }, [
        el("div", { class: "automation-edit-left" }, [
          field(t("Name"), name, `sch-${id}-name`),
          field(t("Cadence"), cadence, `sch-${id}-cadence`),
          field(t("Mode"), mode, `sch-${id}-mode`),
          el("div", { class: "field automation-authors-field" }, [
            el("span", { class: "field-label" }, [el("label", {}, t("Authors")), help(t(AUTHORS_HELP))]),
            el("div", { class: "automation-author-choices" },
              authorBoxes.length ? authorBoxes : [el("span", { class: "muted" }, t("No authors registered yet."))]),
          ]),
          checkField(t("Enabled"), enabled, `sch-${id}-enabled`),
        ]),
        el("div", { class: "automation-edit-right" }, [
          el("div", { class: "field" }, [el("label", {}, t("Days")), calendar.element]),
          el("div", { class: "field" }, [el("label", {}, t("Times (several per day allowed)")), picker.element, chips]),
        ]),
      ]),
      el("div", { class: "source-actions source-actions--editor" }, [cancel, save]),
      editStatus,
    ]);
    cancel.addEventListener("click", () => onClose());
    editForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const body = {
        name: name.value.trim(),
        cadence: cadence.value,
        times: [...draft.times].sort(),
        mode: mode.value,
        authors: [...draft.authors].sort(),
        enabled: enabled.checked,
      };
      if (!isNew) body.slug = schedule.slug;
      if (cadence.value === "weekly") body.weekdays = [...draft.weekdays].sort((a, b) => a - b);
      if (cadence.value === "monthly") body.monthdays = [...draft.monthdays].sort((a, b) => a - b);
      const problem = validateSchedule(body);
      if (problem) {
        setStatus(editStatus, "error", t(problem));
        return;
      }
      save.disabled = true;
      setStatus(editStatus, "pending", t("Saving..."));
      try {
        await api.upsertSchedule(body);
        await reload();
        setStatus(editStatus, "done", t("Saved."));
        save.disabled = false;
        if (onChanged) onChanged();
        onClose();
      } catch (err) {
        save.disabled = false;
        setStatus(editStatus, "error", t("Could not save ({code}): {msg}", { code: err.code, msg: err.message }));
      }
    });
    return editForm;
  }

  async function act(button, run, statusNode) {
    button.disabled = true;
    try {
      await run();
      await reload();
      if (onChanged) onChanged();
    } catch (err) {
      button.disabled = false;
      setStatus(statusNode, "error", t("Action failed ({code}): {msg}", { code: err.code, msg: err.message }));
    }
  }

  return { element, reload };
}

function runLabel(status) {
  if (status === "ok") return t("ok");
  if (status === "failed") return t("failed");
  if (status === "queued") return t("queued");
  return t("running");
}

function checkField(labelText, control, id) {
  return el("div", { class: "field field-check" }, [control, el("label", { for: id }, labelText)]);
}

function setStatus(node, state, text) {
  node.dataset.state = state;
  node.textContent = text;
  const assertive = state === "error";
  node.setAttribute("role", assertive ? "alert" : "status");
  node.setAttribute("aria-live", assertive ? "assertive" : "polite");
}

function clearStatus(node) {
  delete node.dataset.state;
  node.textContent = "";
  node.setAttribute("role", "status");
  node.setAttribute("aria-live", "polite");
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
