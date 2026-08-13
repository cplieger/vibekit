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
import { LAST_DAY } from "./schedule-types.js";
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

interface PickerOptions {
  spec: ScheduleSpec;
  enabled: boolean;
  onSave: (spec: ScheduleSpec, enabled: boolean) => void;
  onRemove: () => void;
  onClose: () => void;
}

/**
 * buildSchedulePicker returns the picker body. The caller anchors it (a popup on
 * the Schedule button) and owns dismissal; this function owns only the form and
 * its live summary.
 */
export function buildSchedulePicker(opts: PickerOptions): HTMLElement {
  // A working copy: nothing is committed until Save, so Cancel is a no-op
  // rather than an undo.
  const spec: ScheduleSpec = { ...opts.spec };

  const freqSel = el("select", { className: "sched-freq" }) as HTMLSelectElement;
  for (const [value, label] of [
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

  function renderDetail(): void {
    detail.replaceChildren();
    if (spec.freq === "hourly") {
      const n = el("input", {
        type: "number",
        min: "1",
        max: "24",
        className: "sched-num",
        value: String(spec.interval ?? 6),
      }) as HTMLInputElement;
      n.addEventListener("change", () => {
        const v = Number(n.value);
        if (Number.isFinite(v) && v >= 1 && v <= 24) {
          spec.interval = v;
          paint();
        }
      });
      detail.append(el("span", { className: "sched-label" }, "every"), n, el("span", {}, "hours"));
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
    summary.textContent = describeSpec(spec);
  }

  freqSel.addEventListener("change", () => {
    spec.freq = freqSel.value as ScheduleFreq;
    renderDetail();
    paint();
  });

  const save = el("button", { type: "button", className: "btn-small btn-primary" }, "Save");
  save.addEventListener("click", () => {
    opts.onSave(spec, true);
  });
  const cancel = el("button", { type: "button", className: "btn-small" }, "Cancel");
  cancel.addEventListener("click", opts.onClose);

  const actions = el("div", { className: "sched-actions" }, save, cancel);
  if (opts.enabled) {
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
    actions,
  );
}
