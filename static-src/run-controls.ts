// ---------------------------------------------------------------------------
// Which controls a workflow run offers, by status. Pure and DOM-free.
//
// Its own leaf module rather than a const inside run-view.ts, for the reason
// several other pure rules in this codebase live apart from their renderers
// (turns.ts, mcp-content.ts, url-safety.ts): run-view.ts imports tabs.ts, which
// reads `location` at module scope, so a test of this table through that module
// would drag the whole app graph and a partially-staged DOM in behind it.
// ---------------------------------------------------------------------------

/** The four run-control verbs, in the order a row presents them. */
export type RunVerb = "pause" | "resume" | "retry" | "cancel";

/** KAS's WorkflowStatusSchema. Exported so a test can be exhaustive over it
 *  rather than over whatever subset the table happens to name. */
export const RUN_STATUSES = ["running", "paused", "completed", "failed", "aborted"] as const;

/** Status → the verbs that status accepts.
 *
 *  The table mirrors the server's `runVerb.from` gates, which in turn mirror what
 *  KAS itself accepts: retry throws for anything non-terminal, pause sets a flag
 *  that means nothing once a run has stopped, resume re-drives a paused one.
 *
 *  Duplicating the rule on both sides is deliberate. The server is the authority
 *  and answers 409 naming the live status, but rendering a button whose only
 *  possible outcome is 409 teaches a reader to distrust every other button. The
 *  Go test and the TS test pin the same matrix so the copies cannot drift.
 *
 *  Cancel is absent from the terminal statuses and present on both live ones:
 *  every live run must offer a way out, and a finished run must not offer a stop
 *  that would do nothing.
 *
 *  An unknown status is ABSENT rather than mapped to an empty list, so a future
 *  KAS status degrades to a read-only view instead of a wrong control. */
export const RUN_CONTROLS: Record<string, readonly RunVerb[]> = {
  running: ["pause", "cancel"],
  paused: ["resume", "cancel"],
  // No retry on `completed`, even though it is terminal. KAS's retry throws on
  // the no-nodeId branch unless the status is failed or aborted, and then throws
  // again if the walk finds nothing failed to reset, so a Retry button on a
  // completed run could only ever produce an error.
  completed: [],
  failed: ["retry"],
  aborted: ["retry"],
};

/** Button text per verb. */
export const CONTROL_LABEL: Record<RunVerb, string> = {
  pause: "Pause",
  resume: "Resume",
  retry: "Retry",
  cancel: "Cancel",
};
