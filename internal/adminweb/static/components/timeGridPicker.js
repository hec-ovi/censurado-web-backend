import { el, clear } from "./el.js";
import { t } from "./i18n.js";
import { ClockIcon } from "./clockIcon.js";

// The schedule editor's time picker, in the shape of a phone time picker: a
// composed-time header (clock + HH:MM once an hour is chosen), an hour grid in
// two halves (day hours beside a sun, night hours beside a moon, big numbers,
// hairline separators), and a :00/:30 half-hour step, so every fire time lands
// on a 30-minute boundary. Add hands the composed HH:MM to the editor's chips.

const DAY_HOURS = [8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19];
const NIGHT_HOURS = [20, 21, 22, 23, 0, 1, 2, 3, 4, 5, 6, 7];

export function TimeGridPicker({ onAdd } = {}) {
  let hour = null;
  let minutes = "00";

  const add = el("button", { type: "button", class: "time-add" }, t("Add time"));
  add.disabled = true;
  const preview = el("span", { class: "time-preview-value" }, "--:--");
  const previewWrap = el("span", { class: "time-preview" }, [preview]);

  function syncPreview() {
    clear(previewWrap);
    if (hour === null) {
      preview.textContent = "--:--";
      previewWrap.append(preview);
      return;
    }
    preview.textContent = `${hour}:${minutes}`;
    previewWrap.append(ClockIcon(`${hour}:${minutes}`, { size: 20 }), preview);
    add.disabled = false;
  }

  const hourButtons = [];
  function hourButton(h) {
    const label = String(h).padStart(2, "0");
    const btn = el("button", {
      type: "button", class: "time-hour", "aria-pressed": "false", dataset: { hour: label },
    }, label);
    btn.addEventListener("click", () => {
      hour = label;
      hourButtons.forEach((b) => b.setAttribute("aria-pressed", b === btn ? "true" : "false"));
      syncPreview();
    });
    hourButtons.push(btn);
    return btn;
  }

  function block(mark, hours) {
    return el("div", { class: "time-block" }, [
      el("span", { class: "time-block-mark", "aria-hidden": "true" }, mark),
      el("div", { class: "time-block-hours" }, hours.map(hourButton)),
    ]);
  }

  const minuteButtons = ["00", "30"].map((mm) => {
    const btn = el("button", {
      type: "button", class: "time-minutes", "aria-pressed": mm === "00" ? "true" : "false",
    }, ":" + mm);
    btn.addEventListener("click", () => {
      minutes = mm;
      minuteButtons.forEach((b) => b.setAttribute("aria-pressed", b === btn ? "true" : "false"));
      syncPreview();
    });
    return btn;
  });

  add.addEventListener("click", () => {
    if (hour === null) return;
    onAdd?.(`${hour}:${minutes}`);
  });

  const element = el("div", { class: "time-grid", role: "group", "aria-label": t("Pick a time") }, [
    block("☀", DAY_HOURS),
    block("☾", NIGHT_HOURS),
    el("div", { class: "time-minute-row" }, [...minuteButtons, previewWrap, add]),
  ]);
  return { element };
}
