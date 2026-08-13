import { el, clear, field, help, subTabs } from "./el.js";
import { NewsroomIcon, PenIcon, TagIcon, TrashIcon } from "./icons.js";
import { t } from "./i18n.js";
import { ClockIcon } from "./clockIcon.js";
import { ModelsSection } from "./modelsSection.js";
import { MonthCalendar } from "./monthCalendar.js";
import { WEEKDAY_SHORT, nextRun, validateSchedule } from "../schedule.js";

// The Automation tab, two subtabs so each view does one thing:
//   Configuración - first: the operational side. The live status card (RUNNING
//                   while a batch is in flight, the queue behind it, executor +
//                   model health from the executor's heartbeat), the recent-runs
//                   console, and the Models setup.
//   Tareas        - the task list, directly: name, status, the next firing's
//                   month/day, the fire times as round clock icons, and a link to
//                   that month's page on the production site. Scrolls in its own
//                   pane; empty cells stay empty. A row opens the details popup
//                   (run history as a console) over the dimmed list; tasks are
//                   view or delete, created only through the new-task button.
//
// A task is either an article-edition batch (task=batch: mode + optional prompt)
// or a topic-normalization sweep (task=topics: one fixed job, no mode or prompt).
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

const PROMPT_HELP =
  "Empty prompt = a GENERIC run: the whole newsroom pitches and the jefe picks freely. With a prompt " +
  "it is a CUSTOM run: your instruction (e.g. \"using this author cover the march downtown\") steers " +
  "what the authors pitch and what the jefe selects.";

// The public production origin the per-row link points at (its /YYYY/MM/ month
// archive). The panel itself stays localhost-only; this is only an href.
const PRODUCTION_BASE = "https://elcensuradoweb.com";

const RUN_STATE = { queued: "ready", running: "running", ok: "done", failed: "failed" };
const MONTH_SHORT = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

export function AutomationPanel({ api, onChanged, refreshMs = 0 } = {}) {
  let schedules = [];
  let liveStatus = {};
  const models = ModelsSection({ api });

  const listEl = el("div", { class: "automation-list article-table-host scroll-pane automation-scroll" });
  const runsEl = el("div", { class: "automation-runs" });
  const statusEl = el("div", { class: "automation-status", role: "status", "aria-label": t("Executor status") });
  const listStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
  const newBtn = el("button", { type: "button", class: "automation-new" }, t("New schedule"));
  newBtn.addEventListener("click", () => openEditor());

  const schedulesPane = el("div", { class: "automation-schedules" }, [
    el("div", { class: "automation-head" }, [
      help(t(AUTOMATION_HELP)),
      el("div", { class: "automation-head-actions" }, [newBtn]),
    ]),
    listEl,
    listStatus,
  ]);
  const configPane = el("div", { class: "automation-config" }, [
    statusEl,
    models.element,
    el("div", { class: "automation-runs-block" }, [
      el("div", { class: "panel-head automation-runs-head" }, [el("h2", {}, t("Recent runs"))]),
      runsEl,
    ]),
  ]);
  const views = subTabs([
    { id: "configuracion", label: t("Settings"), content: [configPane] },
    { id: "cronogramas", label: t("Schedules"), content: [schedulesPane] },
  ], { className: "autotabs", label: t("Automation sections") });

  const element = el("div", { class: "automation workspace-section" },
    el("section", { class: "panel panel-fill automation-panel" }, [views.element]));

  async function reload() {
    clear(listEl);
    listEl.append(el("p", { class: "muted" }, t("Loading schedules...")));
    clearStatus(listStatus);
    try {
      const [scheduleData, statusData] = await Promise.all([
        api.listSchedules(), api.getAutomationStatus(),
      ]);
      schedules = (scheduleData && scheduleData.schedules) || [];
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

  // The Day column shows the schedule's PATTERN (readable at a glance even when
  // paused); the Month column shows the next firing's month, only when one exists.
  function dayPattern(schedule) {
    if (schedule.cadence === "weekly") {
      return (schedule.weekdays || []).map((d) => t(WEEKDAY_SHORT[d])).join(", ");
    }
    if (schedule.cadence === "monthly") return (schedule.monthdays || []).join(", ");
    return t("Every day");
  }

  function row(schedule, index, now) {
    const topics = schedule.task === "topics";
    const custom = !!(schedule.prompt || "").trim();
    const kindIcon = el("span", {
      class: "automation-kind", dataset: { kind: topics ? "topics" : custom ? "custom" : "generic" },
      title: topics ? t("Topic normalization")
        : custom ? t("Custom run (has a prompt)") : t("Generic run (whole newsroom)"),
    }, [(topics ? TagIcon : custom ? PenIcon : NewsroomIcon)("automation-kind-icon")]);
    const nameCell = el("td", { class: "automation-name-cell" }, el("div", { class: "automation-name" }, [
      kindIcon,
      el("span", { class: "automation-title" }, schedule.name || schedule.slug),
      topics ? null : el("span", { class: "muted automation-mode-word" }, schedule.mode === "auto" ? t("auto") : t("preview")),
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
    const monthCell = el("td", {}, next ? t(MONTH_SHORT[next.getMonth()]) : "");
    const dayCell = el("td", { class: "automation-day-cell" }, dayPattern(schedule));

    const times = [...(schedule.times || [])].sort();
    const shown = times.slice(0, 2);
    const timeCell = el("td", {}, el("span", { class: "automation-clocks" }, [
      ...shown.map((time) => el("span", { class: "automation-clock-time" }, [ClockIcon(time, { size: 20 }), time])),
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
    tableRow.addEventListener("click", () => openDetails(schedule));
    tableRow.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      openDetails(schedule);
    });
    return tableRow;
  }

  // ---- the details popup (a row click) --------------------------------------

  function openDetails(schedule) {
    const close = el("button", { type: "button", class: "secondary source-dialog-close", "aria-label": t("Close") }, "×");
    const dialog = el("dialog", { class: "source-dialog automation-details-dialog", "aria-label": t("Run history") });
    const closeDetails = () => closeDialog(dialog);
    close.addEventListener("click", closeDetails);
    dialog.addEventListener("cancel", closeDetails);
    dialog.addEventListener("close", () => dialog.remove(), { once: true });

    const runs = schedule.runs || [];
    const okCount = runs.filter((r) => r.status === "ok").length;
    const failedCount = runs.filter((r) => r.status === "failed").length;

    // The run history as a literal console: one line element per run, so every
    // firing lands on its own line.
    const list = el("div", { class: "automation-runs automation-details-console" },
      runs.length
        ? runs.map((run) => consoleLine(run))
        : [el("p", { class: "automation-console-line automation-console-empty" }, t("No runs recorded yet."))]);

    dialog.append(el("div", { class: "source-dialog-shell" }, [
      el("div", { class: "automation-details" }, [
        close,
        el("div", { class: "automation-details-head" }, [
          el("h2", {}, schedule.name || schedule.slug),
          el("span", { class: "badge automation-details-task" }, taskLabel(schedule.task)),
          el("span", { class: "status", dataset: { state: schedule.enabled ? "active" : "offline" } },
            schedule.enabled ? t("Active") : t("Paused")),
        ]),
        el("p", { class: "muted automation-details-meta" },
          `${cadenceLabel(schedule.cadence)} · ${dayPattern(schedule)} · ${(schedule.times || []).join("  ")}`),
        (schedule.prompt || "").trim()
          ? el("p", { class: "automation-details-prompt" }, schedule.prompt)
          : null,
        el("div", { class: "automation-details-counts" }, [
          el("span", { class: "status", dataset: { state: "done" } }, `${okCount} ok`),
          el("span", { class: "status", dataset: { state: "failed" } }, `${failedCount} ${t("failed")}`),
        ]),
        list,
      ]),
    ]));
    document.body.append(dialog);
    showDialog(dialog);
  }

  // One console line: date, status, optional task name, detail.
  function consoleLine(run, name) {
    const when = (run.started_at || "").replace("T", " ").slice(0, 16);
    return el("div", { class: "automation-console-line", dataset: { runId: run.run_id } }, [
      el("span", { class: "console-when" }, when || "---------- --:--"),
      el("span", { class: "console-status", dataset: { state: run.status } },
        runLabel(run.status).padEnd(9, " ")),
      name ? el("span", { class: "console-schedule" }, name) : null,
      el("span", { class: "console-detail" }, run.detail ? `· ${run.detail}` : `· ${run.run_id}`),
    ]);
  }

  // The recent runs as a CONSOLE: every recorded firing across every schedule,
  // newest first, one monospace line each, scrolling like a docker log.
  function renderRuns() {
    clear(runsEl);
    const all = [];
    for (const schedule of schedules) {
      for (const run of schedule.runs || []) {
        all.push({ schedule: schedule.name || schedule.slug, ...run });
      }
    }
    // Sort by start time, newest first; a record with no start yet (just
    // queued) sinks to the bottom instead of pinning to the top forever.
    const sortKey = (r) => r.started_at || "0000";
    all.sort((a, b) => sortKey(b).localeCompare(sortKey(a)));
    if (!all.length) {
      runsEl.append(el("p", { class: "automation-console-line automation-console-empty" },
        t("No runs recorded yet.")));
      return;
    }
    for (const run of all) runsEl.append(consoleLine(run, run.schedule));
  }

  // ---- the calendar editor (create-only: tasks are view or delete once made) --

  function openEditor() {
    let dialog = null;
    const closeEditor = () => {
      if (dialog) closeDialog(dialog);
    };
    const form = buildEditForm(closeEditor);
    const close = el("button", { type: "button", class: "secondary source-dialog-close", "aria-label": t("Close") }, "×");
    dialog = el("dialog", { class: "source-dialog automation-dialog", "aria-label": t("Schedule editor") });
    close.addEventListener("click", closeEditor);
    dialog.addEventListener("cancel", closeEditor);
    dialog.addEventListener("close", () => dialog.remove(), { once: true });
    dialog.append(el("div", { class: "source-dialog-shell" }, [close, form]));
    document.body.append(dialog);
    showDialog(dialog);
  }

  function buildEditForm(onClose) {
    const id = "new";
    const draft = { times: [], weekdays: new Set(), monthdays: new Set() };

    const name = el("input", { type: "text", id: `sch-${id}-name`, value: "" });

    // Cadence as visible tabs: Daily | Weekly | Monthly. The chosen tab drives
    // what the calendar's clicks mean.
    let cadenceValue = "daily";
    const cadenceTabs = [["daily", t("Daily")], ["weekly", t("Weekly")], ["monthly", t("Monthly")]]
      .map(([value, label]) => {
        const tab = el("button", {
          type: "button", class: "cadence-tab", dataset: { cadence: value },
          "aria-pressed": cadenceValue === value ? "true" : "false",
        }, label);
        tab.addEventListener("click", () => {
          cadenceValue = value;
          cadenceBar.querySelectorAll(".cadence-tab").forEach((b) =>
            b.setAttribute("aria-pressed", b === tab ? "true" : "false"));
          calendar.setMode(value);
        });
        return tab;
      });
    const cadenceBar = el("div", { class: "cadence-tabs", role: "group", "aria-label": t("Cadence") }, cadenceTabs);

    // What fires: an article-edition batch or the topic-normalization sweep.
    // Topics is one fixed job, so mode and prompt only apply to batch.
    const task = el("select", { id: `sch-${id}-task` }, [
      el("option", { value: "batch" }, t("Article edition")),
      el("option", { value: "topics" }, t("Topic normalization")),
    ]);
    task.value = "batch";

    const mode = el("select", { id: `sch-${id}-mode` }, [
      el("option", { value: "preview" }, t("Preview (hold for approval)")),
      el("option", { value: "auto" }, t("Auto (publish on gate pass)")),
    ]);
    mode.value = "preview";
    const enabled = el("input", { type: "checkbox", id: `sch-${id}-enabled` });
    enabled.checked = true;

    // Days: the month calendar; its meaning follows the cadence tab.
    const calendar = MonthCalendar({ weekdays: draft.weekdays, monthdays: draft.monthdays });
    calendar.setMode(cadenceValue);

    // Times: one plain select (30-minute steps) feeding the chip list.
    const timeSelect = el("select", { id: `sch-${id}-time`, "aria-label": t("Time") },
      Array.from({ length: 48 }, (_, i) => {
        const hhmm = `${String(Math.floor(i / 2)).padStart(2, "0")}:${i % 2 ? "30" : "00"}`;
        return el("option", { value: hhmm }, hhmm);
      }));
    const addTime = el("button", { type: "button", class: "time-add" }, t("Add time"));
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
    addTime.addEventListener("click", () => {
      const hhmm = timeSelect.value;
      if (draft.times.includes(hhmm)) {
        setStatus(editStatus, "error", t("The time {time} is already on the list.", { time: hhmm }));
        return;
      }
      draft.times.push(hhmm);
      clearStatus(editStatus);
      renderChips();
    });
    renderChips();

    // The custom-run prompt: free text steering the batch. Empty = generic.
    const prompt = el("textarea", {
      id: `sch-${id}-prompt`, rows: "6",
      placeholder: t("Empty = generic run (whole newsroom). Write an instruction to make it a custom run."),
    });

    const save = el("button", { type: "submit" }, t("Save"));
    const cancel = el("button", { type: "button", class: "secondary" }, t("Cancel"));

    const modeField = field(t("Mode"), mode, `sch-${id}-mode`);
    const promptField = el("div", { class: "field automation-prompt-field" }, [
      el("span", { class: "field-label" }, [
        el("label", { for: `sch-${id}-prompt` }, t("Prompt (optional)")),
        help(t(PROMPT_HELP)),
      ]),
      prompt,
    ]);
    function syncTaskFields() {
      const topics = task.value === "topics";
      modeField.hidden = topics;
      promptField.hidden = topics;
    }
    task.addEventListener("change", syncTaskFields);
    syncTaskFields();

    // The editor card: a form SIDEBAR (name, type, mode, prompt, active, actions)
    // beside the MAIN area (the month calendar and the time picker), the shape
    // of a calendar app's event editor.
    const editForm = el("form", { class: "automation-edit" }, [
      el("div", { class: "automation-editor-card" }, [
        el("aside", { class: "automation-editor-side" }, [
          el("h2", { class: "automation-editor-title" }, t("New schedule")),
          field(t("Name"), name, `sch-${id}-name`),
          field(t("Type"), task, `sch-${id}-task`),
          modeField,
          promptField,
          el("div", { class: "field field-check" }, [
            enabled,
            el("label", { for: `sch-${id}-enabled` }, t("Active")),
            help(t("Active fires on schedule; switched off it stays listed but never fires (paused).")),
          ]),
          el("div", { class: "automation-editor-actions" }, [cancel, save]),
          editStatus,
        ]),
        el("div", { class: "automation-editor-main" }, [
          el("section", { class: "automation-editor-block" }, [
            el("h3", {}, t("Days")),
            cadenceBar,
            calendar.element,
          ]),
          el("section", { class: "automation-editor-block" }, [
            el("h3", {}, t("Times (several per day allowed)")),
            el("div", { class: "time-entry" }, [timeSelect, addTime]),
            chips,
          ]),
        ]),
      ]),
    ]);
    cancel.addEventListener("click", () => onClose());
    editForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const body = {
        name: name.value.trim(),
        cadence: cadenceValue,
        times: [...draft.times].sort(),
        task: task.value,
        mode: mode.value,
        prompt: task.value === "topics" ? "" : prompt.value.trim(),
        enabled: enabled.checked,
      };
      if (cadenceValue === "weekly") body.weekdays = [...draft.weekdays].sort((a, b) => a - b);
      if (cadenceValue === "monthly") body.monthdays = [...draft.monthdays].sort((a, b) => a - b);
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

function cadenceLabel(cadence) {
  if (cadence === "weekly") return t("Weekly");
  if (cadence === "monthly") return t("Monthly");
  return t("Daily");
}

function runLabel(status) {
  if (status === "ok") return t("ok");
  if (status === "failed") return t("failed");
  if (status === "queued") return t("queued");
  return t("running");
}

function taskLabel(task) {
  return task === "topics" ? t("Topic normalization") : t("Article edition");
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
  // The event must come from the dialog's own realm (jsdom under tests).
  const EventCtor = (dialog.ownerDocument.defaultView || globalThis).Event;
  dialog.dispatchEvent(new EventCtor("close"));
}
