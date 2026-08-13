// The schedule-domain helpers are the panel's clock math: nextRun feeds the
// "Next run" column and validateSchedule the pre-flight form messages. These
// tests pin the cadence semantics the executor mirrors (daily every day, weekly
// by 0=Sunday..6, monthly by day of month with absent days skipped) so the
// panel's forecast and the actual firing cannot drift apart silently.
import { test } from "node:test";
import assert from "node:assert/strict";
import { nextRun, cadenceDays, formatWhen, validateSchedule } from "../static/schedule.js";

// Wednesday, 2026-08-12 10:00 local.
const NOW = new Date(2026, 7, 12, 10, 0);

const daily = { cadence: "daily", times: ["07:30", "18:00"], enabled: true };

test("nextRun daily: the next listed time today, then tomorrow's first", () => {
  assert.deepEqual(nextRun(daily, NOW), new Date(2026, 7, 12, 18, 0));
  const after = new Date(2026, 7, 12, 18, 0, 1);
  assert.deepEqual(nextRun(daily, after), new Date(2026, 7, 13, 7, 30));
});

test("nextRun weekly: skips to the next matching weekday", () => {
  // Monday (1) and Friday (5); from Wednesday the next match is Friday 07:30.
  const weekly = { cadence: "weekly", times: ["07:30"], weekdays: [1, 5], enabled: true };
  assert.deepEqual(nextRun(weekly, NOW), new Date(2026, 7, 14, 7, 30));
});

test("nextRun monthly: a day absent from this month rolls to the next month that has it", () => {
  // From Sep 1, day 31 does not exist in September (30 days): next is Oct 31.
  const monthly = { cadence: "monthly", times: ["09:00"], monthdays: [31], enabled: true };
  assert.deepEqual(nextRun(monthly, new Date(2026, 8, 1, 12, 0)), new Date(2026, 9, 31, 9, 0));
});

test("nextRun is null when the schedule is disabled or has no times", () => {
  assert.equal(nextRun({ ...daily, enabled: false }, NOW), null);
  assert.equal(nextRun({ cadence: "daily", times: [], enabled: true }, NOW), null);
});

test("cadenceDays renders weekday names and monthdays, and nothing for daily", () => {
  assert.equal(cadenceDays({ cadence: "weekly", weekdays: [1, 3, 5] }), "Mon, Wed, Fri");
  assert.equal(cadenceDays({ cadence: "monthly", monthdays: [1, 15] }), "day 1, 15");
  assert.equal(cadenceDays(daily), "");
});

test("formatWhen: today, tomorrow, then weekday + date", () => {
  assert.equal(formatWhen(new Date(2026, 7, 12, 18, 0), NOW), "today 18:00");
  assert.equal(formatWhen(new Date(2026, 7, 13, 7, 30), NOW), "tomorrow 07:30");
  assert.equal(formatWhen(new Date(2026, 7, 17, 7, 30), NOW), "Mon 17 Aug, 07:30");
});

test("validateSchedule reports the first problem, and passes a sound draft", () => {
  assert.equal(validateSchedule({ name: "", times: ["07:00"] }), "A schedule needs a name.");
  assert.equal(validateSchedule({ name: "X", times: [] }), "Add at least one time.");
  assert.match(validateSchedule({ name: "X", times: ["7:00"] }), /not a valid HH:MM/);
  assert.match(validateSchedule({ name: "X", times: ["07:00", "07:00"] }), /listed twice/);
  assert.equal(
    validateSchedule({ name: "X", cadence: "weekly", times: ["07:00"], weekdays: [] }),
    "A weekly schedule needs at least one weekday.",
  );
  assert.equal(
    validateSchedule({ name: "X", cadence: "monthly", times: ["07:00"], monthdays: [] }),
    "A monthly schedule needs at least one day of the month.",
  );
  assert.equal(validateSchedule({ name: "X", cadence: "weekly", times: ["07:00"], weekdays: [1] }), "");
});
