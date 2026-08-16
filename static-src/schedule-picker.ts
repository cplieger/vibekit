// The recurrence picker behind the Schedule button on /docs/workflows.
//
// Shape is the settled convention every calendar tool uses: ONE frequency
// select drives ONE conditional row, and a plain-English summary with the
// resolved next run sits underneath. Progressive disclosure with a single
// control as the switch — nothing else is ever on screen.
//
// The summary is the load-bearing part. It is the only way a user can confirm
// that what they built means what they intended, and it comes from the SERVER's
// resolved next_run_at rather than a second implementation here, so the line
// cannot disagree with what will actually fire.
//
// It carries the LAST outcome too, from the same record: an unattended schedule
// is exactly the thing nobody watches, so a row that only ever promises a next
// run can repeat the same failure nightly with nothing on screen saying so.

import { el } from "@cplieger/reactive";
import { LAST_DAY, INTERVAL_BOUNDS } from "./schedule-types.js";
import type { ScheduleFreq, ScheduleSpec, ScheduleView } from "./schedule-types.js";

const WEEKDAY_LABELS = ["S", "M", "T", "W", "T", "F", "S"];
const WEEKDAY_NAMES = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
];

/** The default a fresh schedule opens on: daily at 02:00, the overnight case. */
export function defaultSpec(): ScheduleSpec {
  return { freq: "daily", hour: 2, minute: 0, interval: 6, weekdays: [1], month_day: 1 };
}

/** two pads a clock component. */
function two(n: number): string {
  return String(n).padStart(2, "0");
}

/** The active frequency's interval bounds, or undefined for a frequency with no
 *  step. Exported for its own unit test: the minute floor is a rule the user
 *  meets here, so it is worth pinning independently of the DOM. */
export function intervalBounds(freq: ScheduleFreq): { min: number; max: number } | undefined {
  return freq === "minutely" || freq === "hourly" ? INTERVAL_BOUNDS[freq] : undefined;
}

/**
 * clampInterval re-bounds the working spec's step for its current frequency.
 *
 * `interval` is ONE field serving two units, which is the decision's own shape: a
 * minute-level frequency is the same rule one unit down, not a second scheme. So
 * switching frequency has to re-bound the value, or "every 45 minutes" becomes a
 * request for every 45 HOURS and the server rejects the save with nothing on the
 * form having said so.
 */
export function clampInterval(spec: ScheduleSpec): void {
  const b = intervalBounds(spec.freq);
  if (b === undefined) {
    return;
  }
  spec.interval = Math.min(Math.max(spec.interval ?? b.min, b.min), b.max);
}

/** The unattended approval budget, in minutes, mirroring internal/hub's
 *  `unattendedApprovalBudget` (180 seconds). Stated to the user because the
 *  question a schedule form has to answer is what happens when the job asks for
 *  something and nobody is there. Go's TestUnattendedBudget_MatchesTheDisclaimer
 *  fails if the two drift; change both together. */
const UNATTENDED_BUDGET_MINUTES = 3;

/** ordinal renders a month day the way a person says it. */
function ordinal(n: number): string {
  if (n === LAST_DAY) {
    return "the last day";
  }
  const rem100 = n % 100;
  if (rem100 >= 11 && rem100 <= 13) {
    return `the ${n}th`;
  }
  switch (n % 10) {
    case 1:
      return `the ${n}st`;
    case 2:
      return `the ${n}nd`;
    case 3:
      return `the ${n}rd`;
    default:
      return `the ${n}th`;
  }
}

/**
 * describeSpec renders a spec as a sentence. Exported for its own unit test:
 * this is the text the user reads to decide whether the rule is right, so it is
 * worth pinning independently of the DOM.
 */
export function describeSpec(spec: ScheduleSpec): string {
  const clock = `${two(spec.hour)}:${two(spec.minute)}`;
  switch (spec.freq) {
    case "minutely": {
      const n = spec.interval ?? INTERVAL_BOUNDS.minutely.min;
      // The phase, not the raw minute: a step of 15 chosen at :37 fires at
      // 07,22,37,52, so naming :37 alone would describe a different rule. Stated
      // as "from" rather than "at ... past" because a step that does not divide
      // 60 walks the whole day and does not repeat within the hour.
      const phase = n > 0 ? spec.minute % n : spec.minute;
      const step = `Every ${n} minutes`;
      return phase === 0 ? step : `${step} from :${two(phase)}`;
    }
    case "hourly": {
      const n = spec.interval ?? 1;
      const past = spec.minute === 0 ? "" : ` at ${two(spec.minute)} past`;
      return n === 1 ? `Every hour${past}` : `Every ${n} hours${past}`;
    }
    case "daily":
      return `Every day at ${clock}`;
    case "weekly": {
      const days = [...(spec.weekdays ?? [])].sort((a, b) => a - b);
      if (days.length === 0) {
        return `Weekly at ${clock} — pick at least one day`;
      }
      if (days.length === 7) {
        return `Every day at ${clock}`;
      }
      const names = days.map((d) => WEEKDAY_NAMES[d] ?? "?").join(", ");
      return `Every ${names} at ${clock}`;
    }
    case "monthly":
      return `Monthly on ${ordinal(spec.month_day ?? 1)} at ${clock}`;
    default:
      return "Not scheduled";
  }
}

/**
 * formatStamp renders one of the server's resolved timestamps in local time. One
 * format for both the next run and the last one, so the two halves of the row
 * read as the same kind of time.
 */
export function formatStamp(iso: string | undefined): string {
  if (iso === undefined || iso === "") {
    return "";
  }
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return "";
  }
  return d.toLocaleString(undefined, {
    weekday: "short",
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** The result the server records when a launch took. Every other value is a
 *  failure sentence, and the ones vibekit writes carry this prefix. */
const RESULT_STARTED = "started";
const RESULT_FAILED = "failed: ";

/**
 * describeOutcome renders the row's last-run segment from the two fields the
 * client already holds: the server's `last_result` sentence and `last_run_at`.
 *
 * This is the half of the row that makes a schedule failing the same way every
 * night visible. The server writes one of two shapes — `started` when the launch
 * took, or `failed: <why>` when it did not, including the unattended run that
 * was denied an approval minutes later — and the `<why>` is kept in full because
 * it is the actionable part (it names the permission rule to add). Anything else
 * is passed through as written rather than reclassified here.
 */
export function describeOutcome(view: ScheduleView): string {
  const result = view.last_result ?? "";
  if (result === "") {
    return "";
  }
  const when = formatStamp(view.last_run_at);
  const stamp = when === "" ? "" : ` ${when}`;
  if (result === RESULT_STARTED) {
    return `last started${stamp}`;
  }
  if (result.startsWith(RESULT_FAILED)) {
    return `last failed${stamp}: ${result.slice(RESULT_FAILED.length)}`;
  }
  return `last${stamp}: ${result}`;
}

/** summaryLine is what the row and the picker both show. */
export function summaryLine(view: ScheduleView | undefined): string {
  if (!view?.enabled) {
    return "Not scheduled";
  }
  let line = describeSpec(view.spec);
  const next = formatStamp(view.next_run_at);
  if (next !== "") {
    line += ` — next ${next}`;
  }
  // The outcome trails the rule and the next run, separated by the app's own
  // middot: what will happen reads first, what happened last reads second, and a
  // schedule that has never fired shows nothing rather than an empty segment.
  const last = describeOutcome(view);
  return last === "" ? line : `${line} · ${last}`;
}

/**
 * buildUnattendedNote is the form's statement of what running unattended means.
 *
 * A STATIC statement of policy, never a prediction. An earlier draft had the form
 * say what this job would be permitted to do; that is withdrawn, because the
 * agent chooses its tool calls at runtime and any enumeration here would be a
 * guess presented as fact.
 *
 * The one thing it reads live is the auto-approve setting, because that value
 * decides which of the two outcomes actually happens and the client already holds
 * it. Boilerplate ("this may be approved automatically") would be the useless
 * version of the same sentence.
 *
 * The setting is GLOBAL, and the wording says so: it governs every scheduled run,
 * and there is no per-schedule grant. Saying "this job" would invent one.
 */
export function buildUnattendedNote(
  autoApprove: boolean,
  onOpenPermissions: () => void,
): HTMLElement {
  const outcome = autoApprove
    ? `the ask is approved automatically after ${UNATTENDED_BUDGET_MINUTES} minutes`
    : `the ask is refused after ${UNATTENDED_BUDGET_MINUTES} minutes and the run does not complete`;
  const link = el(
    "button",
    { type: "button", className: "btn-small sched-note-link" },
    "Open Settings → Permissions",
  );
  link.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    onOpenPermissions();
  });
  return el(
    "div",
    { className: "sched-note" },
    el("div", { className: "sched-note-title" }, "Runs unattended"),
    el(
      "div",
      { className: "sched-note-body" },
      `Nobody is watching a scheduled run. If the workflow asks for a permission the active policy does not already allow, ${outcome}.`,
    ),
    el(
      "div",
      { className: "sched-note-state" },
      `Auto-approve is ${autoApprove ? "on" : "off"} for every scheduled run, not just this one.`,
    ),
    link,
  );
}

interface PickerOptions {
  spec: ScheduleSpec;
  /** Whether the stored schedule is currently running, and so the initial state
   *  of the Run-on-this-schedule box. A recipe with no schedule yet opens the
   *  form checked: the user came here to create one. */
  enabled: boolean;
  /** Whether a schedule RECORD exists, which is what Remove needs to know.
   *  Distinct from `enabled` since a schedule can be saved paused: keying Remove
   *  on `enabled` would leave a paused schedule with no way to delete it. */
  exists: boolean;
  /** The auto-approve setting's CURRENT value, for the unattended note's live
   *  read-out. Passed in rather than fetched here so this module stays a pure
   *  form over the data its caller already holds. */
  autoApprove: boolean;
  onSave: (spec: ScheduleSpec, enabled: boolean) => void;
  onRemove: () => void;
  onClose: () => void;
  /** Open Settings → Permissions, where the auto-approve choice lives. Injected
   *  rather than imported, the git-status-banner.ts convention: it keeps this
   *  module out of the tab/router graph, which is what lets it be unit-tested as
   *  a form instead of as an app. */
  onOpenPermissions: () => void;
}

/**
 * buildSchedulePicker returns the picker body. The caller anchors it (a popup on
 * the Schedule button) and owns dismissal; this function owns only the form and
 * its live summary.
 */
export function buildSchedulePicker(opts: PickerOptions): HTMLElement {
  // A working copy: nothing is committed until Save, so Cancel is a no-op
  // rather than an undo. The enabled flag is part of that copy for the same
  // reason.
  const spec: ScheduleSpec = { ...opts.spec };
  let enabled = opts.exists ? opts.enabled : true;

  const freqSel = el("select", { className: "sched-freq" }) as HTMLSelectElement;
  for (const [value, label] of [
    ["minutely", "Every few minutes"],
    ["hourly", "Every few hours"],
    ["daily", "Every day"],
    ["weekly", "Every week"],
    ["monthly", "Every month"],
  ] as [ScheduleFreq, string][]) {
    freqSel.appendChild(el("option", { value }, label));
  }
  freqSel.value = spec.freq;

  const detail = el("div", { className: "sched-detail" });
  const summary = el("div", { className: "sched-summary" });

  const hourInput = () => {
    const i = el("input", {
      type: "time",
      className: "sched-time",
      value: `${two(spec.hour)}:${two(spec.minute)}`,
    }) as HTMLInputElement;
    i.addEventListener("change", () => {
      const [h, m] = i.value.split(":");
      const hn = Number(h);
      const mn = Number(m);
      // An empty time input reports "", which would become NaN and silently
      // send an invalid spec the server would reject.
      if (Number.isFinite(hn) && Number.isFinite(mn)) {
        spec.hour = hn;
        spec.minute = mn;
        paint();
      }
    });
    return i;
  };

  /** A bounded integer field. Out-of-range input leaves the spec alone, the same
   *  silent-ignore the time field uses, and the min/max attributes are where the
   *  user meets the rule. Integers only: a fractional value cannot unmarshal into
   *  the Go int field, so accepting 6.5 would guarantee a rejected save. */
  const numberField = (
    value: number,
    min: number,
    max: number,
    set: (v: number) => void,
  ): HTMLInputElement => {
    const i = el("input", {
      type: "number",
      min: String(min),
      max: String(max),
      className: "sched-num",
      value: String(value),
    }) as HTMLInputElement;
    i.addEventListener("change", () => {
      const v = Number(i.value);
      if (Number.isInteger(v) && v >= min && v <= max) {
        set(v);
        paint();
      }
    });
    return i;
  };

  function renderDetail(): void {
    detail.replaceChildren();
    if (spec.freq === "minutely") {
      // "At this minute, every X": the offset reads first because it is what the
      // step is phased from, and the step is what repeats.
      const b = INTERVAL_BOUNDS.minutely;
      detail.append(
        el("span", { className: "sched-label" }, "at minute"),
        numberField(spec.minute, 0, 59, (v) => {
          spec.minute = v;
        }),
        el("span", { className: "sched-label" }, "every"),
        numberField(spec.interval ?? b.min, b.min, b.max, (v) => {
          spec.interval = v;
        }),
        el("span", {}, "minutes"),
      );
      return;
    }
    if (spec.freq === "hourly") {
      const b = INTERVAL_BOUNDS.hourly;
      detail.append(
        el("span", { className: "sched-label" }, "every"),
        numberField(spec.interval ?? b.min, b.min, b.max, (v) => {
          spec.interval = v;
        }),
        el("span", {}, "hours"),
      );
      return;
    }
    if (spec.freq === "weekly") {
      const chips = el("div", { className: "sched-days" });
      for (let d = 0; d < 7; d++) {
        const on = (spec.weekdays ?? []).includes(d);
        const b = el("button", {
          type: "button",
          className: `sched-day${on ? " on" : ""}`,
          "aria-pressed": on ? "true" : "false",
          "aria-label": WEEKDAY_NAMES[d] ?? "",
        }) as HTMLButtonElement;
        b.textContent = WEEKDAY_LABELS[d] ?? "";
        b.addEventListener("click", () => {
          const cur = new Set(spec.weekdays ?? []);
          if (cur.has(d)) {
            cur.delete(d);
          } else {
            cur.add(d);
          }
          spec.weekdays = [...cur].sort((a, b2) => a - b2);
          renderDetail();
          paint();
        });
        chips.appendChild(b);
      }
      detail.append(chips, el("span", { className: "sched-label" }, "at"), hourInput());
      return;
    }
    if (spec.freq === "monthly") {
      const daySel = el("select", { className: "sched-monthday" }) as HTMLSelectElement;
      for (let d = 1; d <= 31; d++) {
        daySel.appendChild(el("option", { value: String(d) }, ordinal(d)));
      }
      // "Last day" is offered explicitly because months differ in length: a
      // day past the month's end CLAMPS server-side rather than skipping the
      // month, and this is the honest way to ask for the end of February.
      daySel.appendChild(el("option", { value: String(LAST_DAY) }, "the last day"));
      daySel.value = String(spec.month_day ?? 1);
      daySel.addEventListener("change", () => {
        spec.month_day = Number(daySel.value);
        paint();
      });
      detail.append(
        el("span", { className: "sched-label" }, "on"),
        daySel,
        el("span", { className: "sched-label" }, "at"),
        hourInput(),
      );
      return;
    }
    detail.append(el("span", { className: "sched-label" }, "at"), hourInput());
  }

  function paint(): void {
    // The picker's own preview has no next_run_at yet (the server resolves that
    // on save), so it shows the rule alone; the ROW shows the rule plus the
    // resolved next run once saved.
    //
    // A paused rule is still shown, marked: it is the rule Save is about to
    // store, and the alternative (blanking it) hides the thing being edited.
    const rule = describeSpec(spec);
    summary.textContent = enabled ? rule : `${rule} (paused)`;
  }

  freqSel.addEventListener("change", () => {
    spec.freq = freqSel.value as ScheduleFreq;
    // Before the re-render, so the field paints the clamped value rather than
    // showing one number while the spec holds another.
    clampInterval(spec);
    renderDetail();
    paint();
  });

  // Pause is a state Save can produce, so a user who wants a schedule back next
  // week does not have to re-author the rule to keep it. Remove was the only
  // off-switch, which made deletion the price of a pause; the store, the row's
  // summary line and the runner's own skip already modelled the disabled state,
  // so this is the half that was missing rather than a new capability.
  //
  // The input is nested in its label rather than paired by id: the picker is
  // rebuilt on every open, and a fixed id would collide with a picker still in
  // the DOM on another row.
  const enabledBox = el("input", { type: "checkbox" }) as HTMLInputElement;
  enabledBox.checked = enabled;
  enabledBox.addEventListener("change", () => {
    enabled = enabledBox.checked;
    paint();
  });
  const enabledRow = el(
    "label",
    { className: "sched-enabled" },
    enabledBox,
    el("span", {}, "Run on this schedule"),
  );

  const save = el("button", { type: "button", className: "btn-small btn-primary" }, "Save");
  save.addEventListener("click", () => {
    opts.onSave(spec, enabled);
  });
  const cancel = el("button", { type: "button", className: "btn-small" }, "Cancel");
  cancel.addEventListener("click", opts.onClose);

  const actions = el("div", { className: "sched-actions" }, save, cancel);
  if (opts.exists) {
    const remove = el("button", { type: "button", className: "btn-small btn-danger" }, "Remove");
    remove.addEventListener("click", opts.onRemove);
    actions.appendChild(remove);
  }

  renderDetail();
  paint();
  return el(
    "div",
    { className: "sched-picker" },
    el("div", { className: "sched-row" }, freqSel),
    el("div", { className: "sched-row" }, detail),
    summary,
    enabledRow,
    // Below the rule and above the actions: it is what the user should have read
    // before pressing Save, not a footnote after it.
    buildUnattendedNote(opts.autoApprove, opts.onOpenPermissions),
    actions,
  );
}
