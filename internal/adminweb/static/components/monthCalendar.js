import { el, clear } from "./el.js";
import { t } from "./i18n.js";
import { WEEKDAY_SHORT } from "../schedule.js";

// A month calendar for the schedule editor, in the shape of a calendar app's
// mini-month: ‹ month year › navigation, a weekday header row, and the month's
// days laid out on their real weekday columns (leading/trailing cells stay
// empty). What clicking means depends on the cadence:
//   - monthly: day cells toggle day-of-month numbers (the selection repeats
//     every month; the grid is how you see them on a real month).
//   - weekly: the weekday HEADER cells toggle weekdays (0=Sunday..6), and the
//     matching day columns light up.
//   - daily: every day fires; the grid renders softly all-on and takes no input.
// The two Sets are the single source of truth the editor submits.

const MONTH_LONG = ["January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December"];

export function MonthCalendar({ weekdays, monthdays } = {}) {
  let mode = "daily";
  let shown = new Date();
  shown = new Date(shown.getFullYear(), shown.getMonth(), 1);

  const title = el("span", { class: "cal-title" });
  const prev = el("button", { type: "button", class: "cal-nav", "aria-label": t("Previous month") }, "‹");
  const next = el("button", { type: "button", class: "cal-nav", "aria-label": t("Next month") }, "›");
  prev.addEventListener("click", () => { shown = new Date(shown.getFullYear(), shown.getMonth() - 1, 1); render(); });
  next.addEventListener("click", () => { shown = new Date(shown.getFullYear(), shown.getMonth() + 1, 1); render(); });

  const grid = el("div", { class: "cal-grid", role: "grid" });
  const caption = el("p", { class: "cal-caption muted" });
  const element = el("div", { class: "month-calendar", dataset: { mode } }, [
    el("div", { class: "cal-head" }, [prev, title, next]),
    grid,
    caption,
  ]);

  function render() {
    element.dataset.mode = mode;
    title.textContent = `${t(MONTH_LONG[shown.getMonth()])} ${shown.getFullYear()}`;
    clear(grid);

    // Weekday header: toggle buttons in weekly mode, plain labels otherwise.
    for (let day = 0; day < 7; day++) {
      const label = t(WEEKDAY_SHORT[day]);
      if (mode === "weekly") {
        const btn = el("button", {
          type: "button", class: "cal-weekday cal-weekday-toggle",
          "aria-pressed": weekdays.has(day) ? "true" : "false",
        }, label);
        btn.addEventListener("click", () => {
          if (weekdays.has(day)) weekdays.delete(day);
          else weekdays.add(day);
          render();
        });
        grid.append(btn);
      } else {
        grid.append(el("span", { class: "cal-weekday" }, label));
      }
    }

    const daysInMonth = new Date(shown.getFullYear(), shown.getMonth() + 1, 0).getDate();
    const lead = new Date(shown.getFullYear(), shown.getMonth(), 1).getDay();
    const today = new Date();
    const isThisMonth = today.getFullYear() === shown.getFullYear() && today.getMonth() === shown.getMonth();
    for (let i = 0; i < lead; i++) grid.append(el("span", { class: "cal-cell cal-cell-empty" }));
    for (let day = 1; day <= daysInMonth; day++) {
      const weekday = (lead + day - 1) % 7;
      const todayCls = isThisMonth && today.getDate() === day ? " cal-today" : "";
      if (mode === "monthly") {
        const btn = el("button", {
          type: "button", class: "cal-cell cal-day" + todayCls,
          "aria-pressed": monthdays.has(day) ? "true" : "false",
        }, String(day));
        btn.addEventListener("click", () => {
          if (monthdays.has(day)) monthdays.delete(day);
          else monthdays.add(day);
          btn.setAttribute("aria-pressed", monthdays.has(day) ? "true" : "false");
        });
        grid.append(btn);
      } else {
        // A quiet, calendar-looking cell: plain numbers; in weekly mode a small
        // marker under the numbers of the chosen weekday columns.
        const on = mode === "weekly" && weekdays.has(weekday);
        grid.append(el("span", {
          class: "cal-cell cal-day cal-day-static" + todayCls, dataset: on ? { on: "true" } : {},
        }, String(day)));
      }
    }
    if (mode === "daily") caption.textContent = t("Fires every day at the listed times.");
    else if (mode === "weekly") caption.textContent = t("Pick weekdays on the header row.");
    else caption.textContent = t("Pick the days of the month.");
  }

  function setMode(next) {
    mode = next;
    render();
  }

  render();
  return { element, setMode };
}
