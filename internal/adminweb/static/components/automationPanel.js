import { el, clear, field, help } from "./el.js";
import { TrashIcon } from "./icons.js";
import { t } from "./i18n.js";
import { ModelsSection } from "./modelsSection.js";
import { WEEKDAY_SHORT, nextRun, cadenceDays, formatWhen, validateSchedule } from "../schedule.js";

// The Automation tab: the batch-run schedule manager. A schedule tells the
// executor (a compose service that runs whenever Docker runs) when to fire the
// newsroom's edition batch: a cadence (daily/weekly/monthly), one or more HH:MM
// wall-clock times per day, the batch mode, and an optional author subset.
//
// The tab is a list-first TABLE of schedules (name + enabled pill, cadence
// summary, mode, computed next fire time, last outcome, two-click delete);
// clicking a row opens the fullscreen editor dialog, and "New schedule" opens it
// blank. Below the table, a recent-runs strip flattens the newest run records
// across every schedule. Edits are FULL upserts (explicit slug so a rename of the
// display name never forks a row) via api.upsertSchedule; the runs strip is
// server-owned and an upsert never touches it.

const AUTOMATION_HELP =
  "A schedule fires the newsroom's edition batch on its days at each listed time (the server's local " +
  "wall clock). Preview holds every article for approval; auto publishes as soon as the gate passes. " +
  "The executor runs while Docker runs: times missed while it is off are skipped, never replayed.";

const AUTHORS_HELP =
  "Leave every author unchecked to run the whole newsroom: each author with a beat and linked sources " +
  "pitches candidates. Check authors to restrict the edition to them.";

const RUN_STATE = { ok: "done", failed: "failed", running: "running" };

export function AutomationPanel({ api, onChanged } = {}) {
  let schedules = [];
  let authors = [];
  const models = ModelsSection({ api });

  const listEl = el("div", { class: "automation-list article-table-host scroll-pane" });
  const runsEl = el("div", { class: "automation-runs" });
  const listStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
  const newBtn = el("button", { type: "button", class: "automation-new" }, t("New schedule"));
  newBtn.addEventListener("click", () => openEditor(null));

  const element = el("div", { class: "automation workspace-section" }, el("section", { class: "panel panel-fill automation-panel" }, [
    el("div", { class: "panel-head automation-head" }, [
      el("h2", {}, t("Schedules")),
      help(t(AUTOMATION_HELP)),
      el("div", { class: "automation-head-actions" }, [newBtn]),
    ]),
    listEl,
    listStatus,
    el("div", { class: "panel-head automation-runs-head" }, [el("h2", {}, t("Recent runs"))]),
    runsEl,
    models.element,
  ]));

  async function reload() {
    clear(listEl);
    listEl.append(el("p", { class: "muted" }, t("Loading schedules...")));
    clearStatus(listStatus);
    try {
      const [scheduleData, authorData] = await Promise.all([api.listSchedules(), api.listAuthors()]);
      schedules = (scheduleData && scheduleData.schedules) || [];
      authors = (authorData && authorData.authors) || [];
      renderTable();
      renderRuns();
      await models.reload();
    } catch (err) {
      clear(listEl);
      listEl.append(el("p", { class: "error", role: "alert" }, t("Could not load schedules: {msg}", { msg: err.message })));
    }
  }

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
          el("th", {}, t("Cadence")),
          el("th", {}, t("Mode")),
          el("th", {}, t("Next run")),
          el("th", {}, t("Last run")),
          el("th", { class: "automation-delete-head" }, t("Delete")),
        ])),
        tbody,
      ]),
    );
  }

  function row(schedule, index, now) {
    const enabled = !!schedule.enabled;
    const nameCell = el("td", {}, el("div", { class: "automation-name" }, [
      el("span", { class: "automation-title" }, schedule.name || schedule.slug),
      el("span", { class: "status", dataset: { state: enabled ? "active" : "offline" } }, enabled ? t("Active") : t("Paused")),
    ]));

    const days = cadenceDays(schedule);
    const cadenceCell = el("td", {}, el("div", { class: "automation-cadence" }, [
      el("span", { class: "automation-cadence-word" }, cadenceLabel(schedule.cadence)),
      days ? el("span", { class: "automation-cadence-days" }, days) : null,
      el("span", { class: "automation-cadence-times" }, (schedule.times || []).join("  ")),
    ]));

    const modeCell = el("td", {}, el("span", { class: "badge automation-mode", dataset: { mode: schedule.mode } }, modeLabel(schedule.mode)));

    const next = nextRun(schedule, now);
    const nextCell = el("td", {}, el("span", { class: "automation-next" },
      enabled ? (next ? formatWhen(next, now) : t("never")) : t("Paused")));

    const last = (schedule.runs || [])[0];
    const lastCell = el("td", {}, last
      ? el("div", { class: "automation-last" }, [
          el("span", { class: "status", dataset: { state: RUN_STATE[last.status] || "running" } }, runLabel(last.status)),
          last.detail ? el("span", { class: "automation-last-detail" }, last.detail) : null,
        ])
      : el("span", { class: "muted" }, t("never ran")));

    const cardStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
    // Two-click delete instead of window.confirm (a no-op under jsdom): the first
    // click arms the button, the second performs the delete.
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
      nameCell, cadenceCell, modeCell, nextCell, lastCell, actionsCell,
    ]);
    tableRow.addEventListener("click", () => openEditor(schedule));
    tableRow.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      openEditor(schedule);
    });
    return tableRow;
  }

  // The recent-runs strip: the newest run records across every schedule, newest
  // first (started_at is RFC3339, so the string compare orders correctly).
  function renderRuns() {
    clear(runsEl);
    const all = [];
    for (const schedule of schedules) {
      for (const run of schedule.runs || []) {
        all.push({ schedule: schedule.name || schedule.slug, ...run });
      }
    }
    all.sort((a, b) => String(b.started_at).localeCompare(String(a.started_at)));
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

    // --- times: a time input + Add button feeding a chip list -----------------
    const timeInput = el("input", { type: "time", id: `sch-${id}-time` });
    const addTime = el("button", { type: "button", class: "secondary automation-add-time" }, t("Add time"));
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
        chips.append(el("span", { class: "time-chip", role: "listitem" }, [time, remove]));
      }
    }
    function addTimeValue() {
      const value = timeInput.value;
      if (!value) return;
      const hhmm = value.slice(0, 5);
      if (draft.times.includes(hhmm)) {
        setStatus(editStatus, "error", t("The time {time} is already on the list.", { time: hhmm }));
        return;
      }
      draft.times.push(hhmm);
      timeInput.value = "";
      clearStatus(editStatus);
      renderChips();
    }
    addTime.addEventListener("click", addTimeValue);
    timeInput.addEventListener("keydown", (event) => {
      if (event.key !== "Enter") return;
      event.preventDefault();
      addTimeValue();
    });
    renderChips();

    // --- cadence day pickers: one grid per cadence, only the relevant one shown --
    const weekdayGrid = el("div", { class: "day-grid day-grid-week", role: "group", "aria-label": t("Weekdays") },
      WEEKDAY_SHORT.map((label, day) => dayToggle(label, day, draft.weekdays)));
    const monthdayGrid = el("div", { class: "day-grid day-grid-month", role: "group", "aria-label": t("Days of the month") },
      Array.from({ length: 31 }, (_, i) => dayToggle(String(i + 1), i + 1, draft.monthdays)));
    const weekdayField = el("div", { class: "field automation-days-field" }, [
      el("label", {}, t("On weekdays")), weekdayGrid,
    ]);
    const monthdayField = el("div", { class: "field automation-days-field" }, [
      el("label", {}, t("On days of the month")), monthdayGrid,
    ]);
    function syncCadence() {
      weekdayField.hidden = cadence.value !== "weekly";
      monthdayField.hidden = cadence.value !== "monthly";
    }
    cadence.addEventListener("change", syncCadence);
    syncCadence();

    // --- authors: checkbox per author; none checked = the whole newsroom ---------
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
      el("div", { class: "source-edit-grid automation-edit-grid" }, [
        field(t("Name"), name, `sch-${id}-name`),
        field(t("Cadence"), cadence, `sch-${id}-cadence`),
        el("div", { class: "field" }, [
          el("label", { for: `sch-${id}-time` }, t("Times (several per day allowed)")),
          el("div", { class: "automation-time-entry" }, [timeInput, addTime]),
          chips,
        ]),
        weekdayField,
        monthdayField,
        field(t("Mode"), mode, `sch-${id}-mode`),
        el("div", { class: "field automation-authors-field" }, [
          el("span", { class: "field-label" }, [el("label", {}, t("Authors")), help(t(AUTHORS_HELP))]),
          el("div", { class: "automation-author-choices" },
            authorBoxes.length ? authorBoxes : [el("span", { class: "muted" }, t("No authors registered yet."))]),
        ]),
        checkField(t("Enabled"), enabled, `sch-${id}-enabled`),
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

  // Run a row action, then reload the table. On success the row is replaced by
  // the fresh render; on failure the button re-enables and the row surfaces the
  // backend's error.
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

// One day-of-week / day-of-month toggle button. aria-pressed carries the state;
// the Set is the single source of truth the submit reads.
function dayToggle(label, value, set) {
  const button = el("button", {
    type: "button",
    class: "day-toggle",
    "aria-pressed": set.has(value) ? "true" : "false",
  }, label);
  button.addEventListener("click", () => {
    if (set.has(value)) set.delete(value);
    else set.add(value);
    button.setAttribute("aria-pressed", set.has(value) ? "true" : "false");
  });
  return button;
}

function cadenceLabel(cadence) {
  if (cadence === "weekly") return t("Weekly");
  if (cadence === "monthly") return t("Monthly");
  return t("Daily");
}

function modeLabel(mode) {
  return mode === "auto" ? t("Auto") : t("Preview");
}

function runLabel(status) {
  if (status === "ok") return t("ok");
  if (status === "failed") return t("failed");
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
