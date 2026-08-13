import { describe, it, expect } from "vitest";
import {
  describeSpec,
  describeOutcome,
  formatStamp,
  summaryLine,
  defaultSpec,
} from "./schedule-picker.js";
import { LAST_DAY } from "./schedule-types.js";
import type { ScheduleView } from "./schedule-types.js";

// The summary sentence is the only way a user can confirm the rule they built
// means what they intended, so it is pinned independently of the DOM.
describe("describeSpec", () => {
  it("names an hourly step and its offset", () => {
    expect(describeSpec({ freq: "hourly", interval: 6, hour: 0, minute: 15 })).toBe(
      "Every 6 hours at 15 past",
    );
  });

  it("drops the offset when it is on the hour", () => {
    expect(describeSpec({ freq: "hourly", interval: 6, hour: 0, minute: 0 })).toBe("Every 6 hours");
  });

  it("says 'every hour' rather than 'every 1 hours'", () => {
    expect(describeSpec({ freq: "hourly", interval: 1, hour: 0, minute: 0 })).toBe("Every hour");
  });

  it("zero-pads a daily clock", () => {
    expect(describeSpec({ freq: "daily", hour: 2, minute: 5 })).toBe("Every day at 02:05");
  });

  it("lists weekdays by name", () => {
    expect(describeSpec({ freq: "weekly", weekdays: [1, 3], hour: 9, minute: 0 })).toBe(
      "Every Monday, Wednesday at 09:00",
    );
  });

  it("sorts weekdays regardless of click order", () => {
    expect(describeSpec({ freq: "weekly", weekdays: [5, 1], hour: 9, minute: 0 })).toBe(
      "Every Monday, Friday at 09:00",
    );
  });

  it("collapses all seven weekdays to daily", () => {
    expect(
      describeSpec({ freq: "weekly", weekdays: [0, 1, 2, 3, 4, 5, 6], hour: 9, minute: 0 }),
    ).toBe("Every day at 09:00");
  });

  // A weekly spec with no days is the one state the picker can hold that the
  // server would reject, so the summary must say so rather than read as valid.
  it("flags a weekly rule with no days chosen", () => {
    expect(describeSpec({ freq: "weekly", weekdays: [], hour: 9, minute: 0 })).toContain(
      "pick at least one day",
    );
  });

  it("uses ordinals for a month day", () => {
    expect(describeSpec({ freq: "monthly", month_day: 3, hour: 3, minute: 0 })).toBe(
      "Monthly on the 3rd at 03:00",
    );
    expect(describeSpec({ freq: "monthly", month_day: 11, hour: 3, minute: 0 })).toBe(
      "Monthly on the 11th at 03:00",
    );
    expect(describeSpec({ freq: "monthly", month_day: 22, hour: 3, minute: 0 })).toBe(
      "Monthly on the 22nd at 03:00",
    );
  });

  it("names the last-day sentinel in words", () => {
    expect(describeSpec({ freq: "monthly", month_day: LAST_DAY, hour: 3, minute: 0 })).toBe(
      "Monthly on the last day at 03:00",
    );
  });
});

describe("formatStamp", () => {
  it("is empty for a missing or unparseable value", () => {
    expect(formatStamp(undefined)).toBe("");
    expect(formatStamp("")).toBe("");
    expect(formatStamp("not a date")).toBe("");
  });

  it("renders a real timestamp", () => {
    expect(formatStamp("2026-08-10T09:00:00+02:00")).not.toBe("");
  });
});

const view = (over: Partial<ScheduleView>): ScheduleView => ({
  id: "bundled://demo",
  source: "bundled://demo",
  spec: { freq: "daily", hour: 2, minute: 0 },
  enabled: true,
  ...over,
});

// The outcome is the only signal an unattended schedule has. Nobody watches a
// 02:00 run, so a row that promised a next run and said nothing about the last
// one let the same failure repeat every night in silence.
describe("describeOutcome", () => {
  it("says nothing for a schedule that has never fired", () => {
    expect(describeOutcome(view({}))).toBe("");
  });

  it("reports a launch that took, with when", () => {
    const out = describeOutcome(
      view({ last_result: "started", last_run_at: "2026-08-10T02:00:00+02:00" }),
    );
    expect(out).toContain("last started");
    expect(out).toContain(formatStamp("2026-08-10T02:00:00+02:00"));
  });

  // The reason is the actionable half — it names the fix — so it is kept whole
  // rather than reduced to the word "failed".
  it("leads with the word failed and keeps the reason", () => {
    const out = describeOutcome(
      view({
        last_result: "failed: needed approval for fs_write with nobody watching",
        last_run_at: "2026-08-10T02:00:00+02:00",
      }),
    );
    expect(out).toContain("last failed");
    expect(out).toContain("needed approval for fs_write with nobody watching");
    // The server's prefix is not printed twice.
    expect(out).not.toContain("failed: failed");
  });

  it("passes an unrecognised result through as written", () => {
    expect(describeOutcome(view({ last_result: "cancelled by hand" }))).toBe(
      "last: cancelled by hand",
    );
  });

  it("drops the stamp when the server sent no last-run time", () => {
    expect(describeOutcome(view({ last_result: "started" }))).toBe("last started");
  });
});

describe("summaryLine", () => {
  it("reads 'Not scheduled' with no schedule", () => {
    expect(summaryLine(undefined)).toBe("Not scheduled");
  });

  it("reads 'Not scheduled' when disabled", () => {
    expect(summaryLine(view({ enabled: false }))).toBe("Not scheduled");
  });

  it("is the rule alone when the server resolved no next run", () => {
    expect(summaryLine(view({}))).toBe("Every day at 02:00");
  });

  // The next run comes from the SERVER, never recomputed here, so the line
  // cannot disagree with what will actually fire.
  it("appends the server's resolved next run", () => {
    const line = summaryLine(view({ next_run_at: "2026-08-10T02:00:00+02:00" }));
    expect(line).toContain("Every day at 02:00");
    expect(line).toContain("next ");
  });

  // Rule, then what will happen, then what happened last. A failure has to be
  // readable on the row itself: /docs/workflows is where a nightly schedule is
  // looked at, and nothing else surfaces its outcome.
  it("carries the rule, the next run and the last outcome in that order", () => {
    const line = summaryLine(
      view({
        next_run_at: "2026-08-11T02:00:00+02:00",
        last_result: "failed: needed approval for fs_write with nobody watching",
        last_run_at: "2026-08-10T02:00:00+02:00",
      }),
    );
    expect(line.indexOf("Every day at 02:00")).toBeLessThan(line.indexOf("next "));
    expect(line.indexOf("next ")).toBeLessThan(line.indexOf("last failed"));
    expect(line).toContain("needed approval for fs_write");
  });

  it("shows the outcome even when no next run resolved", () => {
    expect(summaryLine(view({ last_result: "started", last_run_at: "" }))).toBe(
      "Every day at 02:00 · last started",
    );
  });

  // A disabled schedule reads "Not scheduled" and nothing else: its history is
  // not what the row is for once it will not fire again.
  it("stays 'Not scheduled' when disabled, outcome or not", () => {
    expect(summaryLine(view({ enabled: false, last_result: "started" }))).toBe("Not scheduled");
  });
});

describe("defaultSpec", () => {
  it("opens on the overnight case and carries a value for every branch", () => {
    const s = defaultSpec();
    expect(s.freq).toBe("daily");
    expect(s.hour).toBe(2);
    // Every conditional control needs a value up front, or switching frequency
    // would render an empty field.
    expect(s.interval).toBeGreaterThan(0);
    expect(s.weekdays?.length).toBeGreaterThan(0);
    expect(s.month_day).toBeGreaterThan(0);
  });
});
