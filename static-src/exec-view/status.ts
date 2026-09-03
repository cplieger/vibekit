// One status vocabulary for every surface reporting delegated work (tree,
// timeline, detail pane, transcript run card). See vibekit-ui.md for the
// three design rules this encodes (shape carries state, motion means in
// flight, quiet default).
//
// THE MARK TABLE IS ONE TAGGED RECORD, not a character map beside an icon map.
// Two tables keyed on the same `ExecState` can both answer for one state, so a
// row could paint a glyph AND a character; a tagged union makes that
// unrepresentable, and `paintStateMark` keeps the two consumers from spelling the
// write differently. This module is the app's only status vocabulary and a second
// private copy of it is a recurring defect.
//
// Two states keep a CHARACTER, deliberately. `input` is the one step state a
// reader must ACT on, and a `?` says that where a silhouette cannot; `skipped`
// means nothing happened, and a dash was never one of the marks the glyph ruling
// removed.

import { outcomeIcon } from "../icons.js";
import { iconEl } from "../icon-el.js";

/** A node's state, as every exec surface reports it. */
export type ExecState =
  "pending" | "running" | "waiting" | "input" | "ok" | "fail" | "warn" | "skipped";

/** The statuses `stateOf` recognises. `stateOf` takes a bare `string` because the
 *  value comes off a foreign wire (KAS's `NodeStateSchema`); a member added
 *  upstream must fall through to `pending` rather than fail a decode. */
export type WireStatus =
  "pending" | "running" | "paused" | "completed" | "failed" | "aborted" | "skipped";

/** The one mark a state carries, tagged so a state cannot carry two. `none` is a
 *  state CSS draws a ring for (hollow / still / spinning). */
type StateMark =
  | { readonly kind: "none" }
  | { readonly kind: "char"; readonly text: string }
  | { readonly kind: "icon"; readonly svg: string };

/** Exactly one mark per state, so tint is never the only channel (WCAG 1.4.1):
 *  the three settled outcomes are solid road-sign SVGs from the shared set, two
 *  states keep a character, and the three in-flight states are CSS rings. */
export const STATE_MARK: Readonly<Record<ExecState, StateMark>> = {
  pending: { kind: "none" },
  running: { kind: "none" },
  waiting: { kind: "none" },
  input: { kind: "char", text: "?" },
  ok: { kind: "icon", svg: outcomeIcon("ok") },
  fail: { kind: "icon", svg: outcomeIcon("fail") },
  warn: { kind: "icon", svg: outcomeIcon("warn") },
  skipped: { kind: "char", text: "\u2013" },
};

/** Write a state's mark into a slot. The one writer, so the run card and the exec
 *  tree cannot spell one state two ways. Idempotent: `replaceChildren` leaves the
 *  slot holding exactly the current mark however often it is called. */
export function paintStateMark(slot: HTMLElement, state: ExecState): void {
  const mark = STATE_MARK[state];
  // Every kind named, no `default`: a kind added later then paints nothing
  // visibly unhandled rather than being absorbed into one of these arms.
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

/** The word an accessible name uses. Not the wire enum: "aborted"/"failed" read
 *  as one thing to a listener, and "pending" reads better as "not started". */
export const STATE_WORD: Readonly<Record<ExecState, string>> = {
  pending: "not started",
  running: "running",
  waiting: "waiting",
  input: "waiting for your answer",
  ok: "succeeded",
  fail: "failed",
  warn: "stopped",
  skipped: "skipped",
};

/** Whether a state is still in flight, so a caller keeps a clock going without
 *  re-deriving the set. `input` counts: the node's turn is open, it is merely
 *  blocked on a person. */
export function inFlight(state: ExecState): boolean {
  return state === "running" || state === "waiting" || state === "input";
}

/** Fold a wire status onto the vocabulary. `skipped` stays its own state (a
 *  branch that never ran did not succeed). `aborted` maps to `warn`, not `fail`:
 *  a stop is not a fault. */
export function stateOf(status: string | undefined): ExecState {
  switch (status) {
    case "completed":
      return "ok";
    case "failed":
      return "fail";
    case "aborted":
      return "warn";
    case "paused":
      return "waiting";
    case "running":
      return "running";
    case "skipped":
      return "skipped";
    default:
      return "pending";
  }
}

/** Reclassify an in-flight node whose ask is unanswered. Guarded on the node being
 *  otherwise in flight: on the workflow wire `node_id` is a node ID rather than a
 *  path, so a repeat's iterations share it and a finished pass would light up
 *  beside the live one. */
export function withAsk(state: ExecState, asked: boolean): ExecState {
  return asked && (state === "running" || state === "waiting") ? "input" : state;
}
