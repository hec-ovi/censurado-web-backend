// Pure schedule-domain helpers, shared by the Automation panel and its tests.
// A schedule fires on its cadence days at each of its "HH:MM" wall-clock times;
// all math here runs in the browser's local timezone, the same wall clock the
// executor container uses, so the panel's "next run" matches what actually fires.

export const WEEKDAY_SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

// dayMatches reports whether the schedule's cadence fires on the given date:
// daily fires every day, weekly on its weekdays (0=Sunday..6), monthly on its
// monthdays (1..31; a day absent from a month simply never matches it).
export function dayMatches(schedule, day) {
  if (schedule.cadence === "weekly") return (schedule.weekdays || []).includes(day.getDay());
  if (schedule.cadence === "monthly") return (schedule.monthdays || []).includes(day.getDate());
  return true;
}

// nextRun returns the next Date the schedule fires strictly after `now`, or null
// when it never will (disabled, or no times). It scans day by day up to a year
// out, which covers every cadence including a monthly day-31 schedule.
export function nextRun(schedule, now = new Date()) {
  if (!schedule.enabled) return null;
  const times = [...(schedule.times || [])].sort();
  if (!times.length) return null;
  for (let offset = 0; offset <= 366; offset++) {
    const day = new Date(now.getFullYear(), now.getMonth(), now.getDate() + offset);
    if (!dayMatches(schedule, day)) continue;
    for (const time of times) {
      const [h, m] = time.split(":").map(Number);
      const candidate = new Date(day.getFullYear(), day.getMonth(), day.getDate(), h, m);
      if (candidate > now) return candidate;
    }
  }
  return null;
}

// validateSchedule checks a draft before it is sent: returns the first problem as
// a plain-English message, or "" when the draft is valid. The server enforces the
// same rules; this exists so the operator gets the message inline, pre-flight.
export function validateSchedule(draft) {
  if (!String(draft.name || "").trim()) return "A schedule needs a name.";
  if (!(draft.times || []).length) return "Add at least one time.";
  const seen = new Set();
  for (const time of draft.times) {
    if (!/^([01][0-9]|2[0-3]):[0-5][0-9]$/.test(time)) return `"${time}" is not a valid HH:MM time.`;
    if (seen.has(time)) return `The time ${time} is listed twice.`;
    seen.add(time);
  }
  if (draft.cadence === "weekly" && !(draft.weekdays || []).length) {
    return "A weekly schedule needs at least one weekday.";
  }
  if (draft.cadence === "monthly" && !(draft.monthdays || []).length) {
    return "A monthly schedule needs at least one day of the month.";
  }
  return "";
}
