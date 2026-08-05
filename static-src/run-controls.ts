// ---------------------------------------------------------------------------
// Which controls a workflow run offers, by status. Pure and DOM-free.
//
// Its own leaf module rather than a const inside run-view.ts, for the reason
// several other pure rules in this codebase live apart from their renderers
// (turns.ts, mcp-content.ts, url-safety.ts): run-view.ts imports tabs.ts, which
// reads `location` at module scope, so a test of this table through that module
// would drag the whole app graph and a partially-staged DOM in behind it.
// ---------------------------------------------------------------------------

/** The run-control verbs, in the order a row presents them. */
export type RunVerb = "pause" | "resume" | "cancel";

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
 *  A live run may still offer nothing: these verbs reach a run only through its
 *  own bridge, which an agent-launched run never has and a restarted server has
 *  lost. The server answers 409 in that case, which is why the buttons render
 *  from status alone and the reply is what tells the user the run is out of
 *  reach.
 *
 *  An unknown status is ABSENT rather than mapped to an empty list, so a future
 *  KAS status degrades to a read-only view instead of a wrong control. */
export const RUN_CONTROLS: Record<string, readonly RunVerb[]> = {
  running: ["pause", "cancel"],
  paused: ["resume", "cancel"],
  // Every terminal status offers nothing, and retry is absent entirely.
  //
  // Retry looked like the natural verb for a failed run and is unreachable by
  // construction: KAS accepts it only from failed or aborted, and vibekit closes
  // a run's bridge on every terminal run_complete, so the moment retry becomes
  // legal is the moment its carrier is gone. Re-hosting an orphaned run is a
  // feature (see run_host.go); until it exists, a finished run is a record.
  completed: [],
  failed: [],
  aborted: [],
};

/** Button text per verb. */
export const CONTROL_LABEL: Record<RunVerb, string> = {
  pause: "Pause",
  resume: "Resume",
  cancel: "Cancel",
};
