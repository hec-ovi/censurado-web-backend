import { el } from "./el.js";
import { t } from "./i18n.js";

// The schedule editor's time picker: an hour grid (day hours up, night hours
// down, like a calendar app's slot picker) plus a :00/:30 half-hour step, so
// every fire time lands on a 30-minute boundary. Picking an hour arms Add;
// Add hands the composed HH:MM to the editor's chip list.

export function TimeGridPicker({ onAdd } = {}) {
  let hour = null;
  let minutes = "00";

  const add = el("button", { type: "button", class: "secondary time-add" }, t("Add time"));
  add.disabled = true;

  const hourButtons = [];
  function hourButton(h) {
    const btn = el("button", {
      type: "button", class: "time-hour", "aria-pressed": "false",
      dataset: { hour: String(h).padStart(2, "0") },
    }, String(h).padStart(2, "0"));
    btn.addEventListener("click", () => {
      hour = String(h).padStart(2, "0");
      hourButtons.forEach((b) => b.setAttribute("aria-pressed", b === btn ? "true" : "false"));
      add.disabled = false;
    });
    hourButtons.push(btn);
    return btn;
  }

  const dayRow = el("div", { class: "time-hours" }, [
    el("span", { class: "time-block-mark", "aria-hidden": "true" }, "☀"),
    ...Array.from({ length: 12 }, (_, i) => hourButton(i + 8)), // 08..19
  ]);
  const nightRow = el("div", { class: "time-hours" }, [
    el("span", { class: "time-block-mark", "aria-hidden": "true" }, "☾"),
    ...Array.from({ length: 12 }, (_, i) => hourButton((i + 20) % 24)), // 20..23, 00..07
  ]);

  const minuteButtons = ["00", "30"].map((mm) => {
    const btn = el("button", {
      type: "button", class: "time-minutes", "aria-pressed": mm === "00" ? "true" : "false",
    }, ":" + mm);
    btn.addEventListener("click", () => {
      minutes = mm;
      minuteButtons.forEach((b) => b.setAttribute("aria-pressed", b === btn ? "true" : "false"));
    });
    return btn;
  });

  add.addEventListener("click", () => {
    if (hour === null) return;
    onAdd?.(`${hour}:${minutes}`);
  });

  const element = el("div", { class: "time-grid", role: "group", "aria-label": t("Pick a time") }, [
    dayRow,
    nightRow,
    el("div", { class: "time-minute-row" }, [...minuteButtons, add]),
  ]);
  return { element };
}
