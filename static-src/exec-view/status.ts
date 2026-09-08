// One status vocabulary for every surface reporting delegated work (tree,
// timeline, detail pane, transcript run card).

import { outcomeIcon } from "../icons.js";
import { iconEl } from "../icon-el.js";
import type { ClassifiedRunNodeStatus, ClassifiedRunStatus } from "../run-status.js";

/** A node's state, as every exec surface reports it. */
export type ExecState =
  "pending" | "running" | "waiting" | "input" | "unknown" | "ok" | "fail" | "warn" | "skipped";

/** The one mark a state carries, tagged so a state cannot carry two. */
type StateMark =
  | { readonly kind: "none" }
  | { readonly kind: "char"; readonly text: string }
  | { readonly kind: "icon"; readonly svg: string };

/** Exactly one mark per state, so tint is never the only channel (WCAG 1.4.1).
 *  A `kind: "none"` state is drawn by CSS as a ring, so a new member here needs a
 *  `.ev-row[data-state=…]` arm or it renders nothing at all. */
export const STATE_MARK: Readonly<Record<ExecState, StateMark>> = {
  pending: { kind: "none" },
  running: { kind: "none" },
  waiting: { kind: "none" },
  input: { kind: "char", text: "?" },
  unknown: { kind: "none" },
  ok: { kind: "icon", svg: outcomeIcon("ok") },
  fail: { kind: "icon", svg: outcomeIcon("fail") },
  warn: { kind: "icon", svg: outcomeIcon("warn") },
  skipped: { kind: "char", text: "\u2013" },
};

/** Write a state's mark into a slot. */
export function paintStateMark(slot: HTMLElement, state: ExecState): void {
  const mark = STATE_MARK[state];
  // Every kind named, no `default`, so a kind added later fails the type check
  // here rather than being absorbed silently into one of these arms.
  switch (mark.kind) {
    case "icon":
      slot.replaceChildren(iconEl(mark.svg));
      break;
    case "char":
      slot.replaceChildren(mark.text);
      break;
    case "none":
      slot.replaceChildren();
      break;
  }
}

/** The word an accessible name uses. Not the wire enum: "aborted"/"failed" read as
 *  one thing to a listener, and "pending" reads better as "not started". */
export const STATE_WORD: Readonly<Record<ExecState, string>> = {
  pending: "not started",
  running: "running",
  waiting: "waiting",
  input: "waiting for your answer",
  unknown: "unknown",
  ok: "succeeded",
  fail: "failed",
  warn: "stopped",
  skipped: "skipped",
};

/** Whether a state is still in flight, so a caller keeps a clock going without
 *  re-deriving the set. `input` counts (the turn is open, merely blocked on a
 *  person) and so does `unknown` (not knowing is not the same as finished). */
export function inFlight(state: ExecState): boolean {
  return state === "running" || state === "waiting" || state === "input" || state === "unknown";
}

/** Whether a node produced nothing because it never ran: `pending` has not started
 *  and `skipped` never will.
 *
 *  The COMPLEMENT of `inFlight` is not "ran" — these two states sit between them —
 *  and a caller that treats it as one answers for a node that has no execution to
 *  describe. Exported for the same reason `inFlight` is: the set belongs to the
 *  vocabulary, and a consumer re-deriving it privately is how a copy drifts. */
export function neverRan(state: ExecState): boolean {
  return state === "pending" || state === "skipped";
}

/** Whether a node RAN and stopped — the third bucket of the MECE partition `inFlight`
 *  and `neverRan` are the other two. `fail` and `warn` are IN (such a node can carry a
 *  capture worth reading); `skipped` is OUT, being terminal without having run. No
 *  `default`, so a tenth state fails the type check instead of reading as done. */
export function settled(state: ExecState): boolean {
  switch (state) {
    case "ok":
    case "fail":
    case "warn":
      return true;
    case "pending":
    case "running":
    case "waiting":
    case "input":
    case "unknown":
    case "skipped":
      return false;
  }
}

/** Fold a classified wire status onto the presentation vocabulary. `skipped` stays
 *  its own state (a branch that never ran did not succeed) and both stops — `aborted`
 *  and `cancelled` — map to `warn`, not `fail`: a stop is not a fault. No `default`, so
 *  a status added to the wire enum fails this fold rather than reading as `pending`. */
export function stateOf(
  status: ClassifiedRunNodeStatus | ClassifiedRunStatus | undefined,
): ExecState {
  switch (status) {
    case undefined:
    case "pending":
      return "pending";
    case "completed":
      return "ok";
    case "failed":
      return "fail";
    case "aborted":
    case "cancelled":
      return "warn";
    case "paused":
      return "waiting";
    case "running":
      return "running";
    case "skipped":
      return "skipped";
    case "unknown":
      return "unknown";
  }
}

/** Reclassify an in-flight node whose ask is unanswered. Guarded on the node being
 *  otherwise in flight: on the workflow wire `node_id` is a node ID rather than a
 *  path, so a repeat's iterations share it and a finished pass would light up beside
 *  the live one. */
export function withAsk(state: ExecState, asked: boolean): ExecState {
  return asked && (state === "running" || state === "waiting" || state === "unknown")
    ? "input"
    : state;
}
