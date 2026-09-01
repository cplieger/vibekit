// One status vocabulary for every surface reporting delegated work (tree,
// timeline, detail pane, transcript run card). See vibekit-ui.md for the
// three design rules this encodes (shape carries state, motion means in
// flight, quiet default).

/** A node's state, as every exec surface reports it. */
export type ExecState =
  "pending" | "running" | "waiting" | "input" | "ok" | "fail" | "warn" | "skipped";

/** The statuses `stateOf` recognises. `stateOf` takes a bare `string` because the
 *  value comes off a foreign wire (KAS's `NodeStateSchema`); a member added
 *  upstream must fall through to `pending` rather than fail a decode. */
export type WireStatus =
  "pending" | "running" | "paused" | "completed" | "failed" | "aborted" | "skipped";

/** The character a state paints. Empty for the three with none; CSS gives those a
 *  ring instead (hollow/still/spinning), so tint is never the only channel
 *  (WCAG 1.4.1). */
export const STATE_BADGE: Readonly<Record<ExecState, string>> = {
  pending: "",
  running: "",
  waiting: "",
  input: "?",
  ok: "\u2713",
  fail: "\u2717",
  warn: "\u26A0",
  skipped: "\u2013",
};

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
