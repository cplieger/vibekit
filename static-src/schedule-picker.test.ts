// @vitest-environment happy-dom
import { describe, it, expect, vi } from "vitest";
import {
  describeSpec,
  describeOutcome,
  formatStamp,
  summaryLine,
  defaultSpec,
  clampInterval,
  intervalBounds,
  buildSchedulePicker,
  buildUnattendedNote,
} from "./schedule-picker.js";
import { LAST_DAY, INTERVAL_BOUNDS } from "./schedule-types.js";
import type { ScheduleFreq, ScheduleSpec, ScheduleView } from "./schedule-types.js";

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

// A minute-level rule is an interval AND a phase, so the sentence has to state
// both or it describes a different schedule than the one that will fire.
describe("describeSpec for minutely", () => {
  it("names the step alone when the phase is zero", () => {
    expect(describeSpec({ freq: "minutely", interval: 15, hour: 0, minute: 0 })).toBe(
      "Every 15 minutes",
    );
  });

  it("names the phase, not the raw minute", () => {
    // A step of 15 chosen at :37 fires at 07,22,37,52 — so :07 is the anchor
    // that describes the rule, and printing :37 alone would name one of four.
    expect(describeSpec({ freq: "minutely", interval: 15, hour: 0, minute: 37 })).toBe(
      "Every 15 minutes from :07",
    );
  });

  it("zero-pads a single-digit phase", () => {
    expect(describeSpec({ freq: "minutely", interval: 30, hour: 0, minute: 7 })).toBe(
      "Every 30 minutes from :07",
    );
  });

  // A hand-edited store could hold a zero the form cannot produce; NaN in the
  // sentence would be worse than a wrong number.
  it("does not print NaN for a zero interval", () => {
    expect(describeSpec({ freq: "minutely", interval: 0, hour: 0, minute: 20 })).not.toContain(
      "NaN",
    );
  });
});

/**
 * mount builds the form and exposes what it would SEND.
 *
 * The picker edits a working COPY of the spec — Cancel is a no-op rather than an
 * undo — so the caller's own object never sees a keystroke, and asserting on it
 * would pass for the wrong reason forever. Save is the observation point, and it
 * is the one that matters: it is the spec that reaches the server.
 */
function mount(
  spec: ScheduleSpec,
  autoApprove = false,
  stored?: { enabled: boolean },
): {
  body: HTMLElement;
  save: () => ScheduleSpec;
  savedEnabled: () => boolean;
  remove: () => HTMLButtonElement | null;
} {
  let saved: ScheduleSpec | undefined;
  let savedEnabled: boolean | undefined;
  const body = buildSchedulePicker({
    spec,
    enabled: stored?.enabled ?? false,
    exists: stored !== undefined,
    autoApprove,
    onSave: (s: ScheduleSpec, enabled: boolean) => {
      saved = s;
      savedEnabled = enabled;
    },
    onRemove: vi.fn(),
    onClose: vi.fn(),
    onOpenPermissions: vi.fn(),
  });
  return {
    body,
    save: (): ScheduleSpec => {
      saved = undefined;
      body.querySelector<HTMLButtonElement>(".btn-primary")?.click();
      if (saved === undefined) {
        throw new Error("Save did not fire");
      }
      return saved;
    },
    savedEnabled: (): boolean => {
      if (savedEnabled === undefined) {
        throw new Error("Save did not fire");
      }
      return savedEnabled;
    },
    remove: (): HTMLButtonElement | null => body.querySelector<HTMLButtonElement>(".btn-danger"),
  };
}

// The floor exists because "every minute" is what a user types when they mean
// "often", and the server refuses it. The form has to refuse it too, or the only
// feedback is a failed save with no reason on screen.
describe("the minute interval floor", () => {
  it("agrees with the server's range", () => {
    expect(INTERVAL_BOUNDS.minutely.min).toBe(5);
    expect(INTERVAL_BOUNDS.minutely.max).toBe(59);
  });

  it("has no step to bound on the calendar frequencies", () => {
    for (const freq of ["daily", "weekly", "monthly"] as ScheduleFreq[]) {
      expect(intervalBounds(freq)).toBeUndefined();
    }
  });

  it("stops the number field below the floor and above the ceiling", () => {
    const form = mount({ freq: "minutely", interval: 15, hour: 0, minute: 0 });
    const nums = form.body.querySelectorAll<HTMLInputElement>(".sched-num");
    // Two fields on this frequency: the offset, then the step.
    expect(nums).toHaveLength(2);
    const step = nums[1] as HTMLInputElement;
    expect(step.min).toBe("5");
    expect(step.max).toBe("59");

    for (const refused of ["1", "0", "60", "7.5"]) {
      step.value = refused;
      step.dispatchEvent(new Event("change"));
      // Refused, and the previous value is what would be sent — the same
      // silent-ignore the time field uses, never a spec the server would reject.
      expect(form.save().interval).toBe(15);
    }

    step.value = "5";
    step.dispatchEvent(new Event("change"));
    expect(form.save().interval).toBe(5);
  });

  // The offset is the other half of "at this minute, every X", and it is bounded
  // by the clock rather than by the step.
  it("bounds the offset to a minute of the hour", () => {
    const form = mount({ freq: "minutely", interval: 15, hour: 0, minute: 7 });
    const offset = form.body.querySelector<HTMLInputElement>(".sched-num");
    expect(offset?.min).toBe("0");
    expect(offset?.max).toBe("59");

    (offset as HTMLInputElement).value = "60";
    offset?.dispatchEvent(new Event("change"));
    expect(form.save().minute).toBe(7);

    (offset as HTMLInputElement).value = "42";
    offset?.dispatchEvent(new Event("change"));
    expect(form.save().minute).toBe(42);
  });

  // `interval` is one field serving two units, so a frequency switch has to
  // re-bound it: 45 minutes is legal, 45 hours is not, and the server would
  // reject the save with nothing on the form having said why.
  it("re-bounds the shared step when the frequency changes", () => {
    const spec: ScheduleSpec = { freq: "hourly", interval: 45, hour: 0, minute: 0 };
    clampInterval(spec);
    expect(spec.interval).toBe(24);

    spec.freq = "minutely";
    spec.interval = 1;
    clampInterval(spec);
    expect(spec.interval).toBe(5);

    // A value already in range is left exactly as chosen.
    spec.interval = 20;
    clampInterval(spec);
    expect(spec.interval).toBe(20);

    // A frequency with no step is untouched rather than given one.
    const daily: ScheduleSpec = { freq: "daily", hour: 2, minute: 0 };
    clampInterval(daily);
    expect(daily.interval).toBeUndefined();
  });

  it("clamps through the form's own frequency select", () => {
    const form = mount({ freq: "minutely", interval: 45, hour: 0, minute: 0 });
    const sel = form.body.querySelector<HTMLSelectElement>(".sched-freq");
    expect(sel).not.toBeNull();
    // Every frequency the server accepts is offered, minute-level included.
    expect([...(sel?.options ?? [])].map((o) => o.value)).toEqual([
      "minutely",
      "hourly",
      "daily",
      "weekly",
      "monthly",
    ]);

    (sel as HTMLSelectElement).value = "hourly";
    sel?.dispatchEvent(new Event("change"));
    expect(form.save().interval).toBe(24);
    // The painted field agrees with what would be sent; showing 45 over an
    // interval of 24 would be the form lying about it.
    expect(form.body.querySelector<HTMLInputElement>(".sched-num")?.value).toBe("24");
  });
});

// A schedule runs with nobody watching, and the form is the only place that fact
// is stated before the job is created. The setting it reports is GLOBAL: there is
// no per-schedule grant, so wording that implies one would be false.
describe("the unattended note", () => {
  it("states the refusal and the budget when auto-approve is off", () => {
    const note = buildUnattendedNote(false, vi.fn());
    const text = note.textContent ?? "";
    expect(text).toContain("Nobody is watching a scheduled run");
    expect(text).toContain("refused after 3 minutes");
    expect(text).toContain("does not complete");
    expect(text).toContain("Auto-approve is off");
    // Never claims the opposite outcome in the same breath.
    expect(text).not.toContain("approved automatically");
  });

  it("states the automatic approval when the setting is on", () => {
    const text = buildUnattendedNote(true, vi.fn()).textContent ?? "";
    expect(text).toContain("approved automatically after 3 minutes");
    expect(text).toContain("Auto-approve is on");
    expect(text).not.toContain("refused");
  });

  it("says the setting is global in both states", () => {
    for (const on of [true, false]) {
      expect(buildUnattendedNote(on, vi.fn()).textContent).toContain(
        "for every scheduled run, not just this one",
      );
    }
  });

  it("routes to the panel that owns the choice", () => {
    const onOpenPermissions = vi.fn();
    const note = buildUnattendedNote(false, onOpenPermissions);
    const link = note.querySelector<HTMLButtonElement>(".sched-note-link");
    expect(link?.textContent).toBe("Open Settings → Permissions");
    link?.click();
    expect(onOpenPermissions).toHaveBeenCalledTimes(1);
  });

  // The live read-out is the whole point: boilerplate would describe both
  // outcomes and commit to neither.
  it("reaches the form, reflecting the value it was built with", () => {
    const build = (autoApprove: boolean): string =>
      buildSchedulePicker({
        spec: defaultSpec(),
        enabled: false,
        exists: false,
        autoApprove,
        onSave: vi.fn(),
        onRemove: vi.fn(),
        onClose: vi.fn(),
        onOpenPermissions: vi.fn(),
      }).querySelector(".sched-note-state")?.textContent ?? "";

    expect(build(true)).toContain("Auto-approve is on");
    expect(build(false)).toContain("Auto-approve is off");
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

// Save could only ever produce enabled=true, so Remove was the one off-switch
// and deleting the rule was the price of pausing it. The store, summaryLine and
// the server's runner already modelled disabled; only the form could not reach it.
describe("pausing a schedule", () => {
  const daily = (): ScheduleSpec => ({ freq: "daily", hour: 2, minute: 0 });

  it("saves a new schedule enabled without touching the box", () => {
    const form = mount(daily());
    form.save();
    expect(form.savedEnabled()).toBe(true);
  });

  it("offers the box checked for a new schedule", () => {
    const box = mount(daily()).body.querySelector<HTMLInputElement>(".sched-enabled input");
    expect(box?.checked).toBe(true);
  });

  it("saves disabled when the box is unchecked, keeping the rule", () => {
    const form = mount(daily(), false, { enabled: true });
    const box = form.body.querySelector<HTMLInputElement>(".sched-enabled input");
    expect(box?.checked).toBe(true);
    if (box === null) {
      throw new Error("no enabled box");
    }
    box.checked = false;
    box.dispatchEvent(new Event("change"));
    expect(form.save()).toEqual(daily());
    expect(form.savedEnabled()).toBe(false);
  });

  it("round-trips a stored disabled schedule back to enabled", () => {
    const form = mount(daily(), false, { enabled: false });
    const box = form.body.querySelector<HTMLInputElement>(".sched-enabled input");
    expect(box?.checked).toBe(false);
    if (box === null) {
      throw new Error("no enabled box");
    }
    box.checked = true;
    box.dispatchEvent(new Event("change"));
    form.save();
    expect(form.savedEnabled()).toBe(true);
  });

  it("marks the preview paused, and unmarks it, without altering the rule", () => {
    const form = mount(daily(), false, { enabled: true });
    const summary = form.body.querySelector(".sched-summary");
    const rule = summary?.textContent ?? "";
    expect(rule).not.toContain("paused");
    const box = form.body.querySelector<HTMLInputElement>(".sched-enabled input");
    if (box === null) {
      throw new Error("no enabled box");
    }
    box.checked = false;
    box.dispatchEvent(new Event("change"));
    expect(summary?.textContent).toBe(`${rule} (paused)`);
    box.checked = true;
    box.dispatchEvent(new Event("change"));
    expect(summary?.textContent).toBe(rule);
  });

  // Remove keys on the record existing, not on it running. Keying it on enabled
  // (as it did) leaves a paused schedule with no way to delete it.
  it("offers Remove for a paused schedule and withholds it when there is none", () => {
    expect(mount(daily(), false, { enabled: false }).remove()).not.toBeNull();
    expect(mount(daily(), false, { enabled: true }).remove()).not.toBeNull();
    expect(mount(daily()).remove()).toBeNull();
  });
});
