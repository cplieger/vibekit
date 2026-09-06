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
export type RunVerb = "pause" | "resume" | "cancel" | "retry";

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
 *  NOTHING here needs to reason about the carrier, and that is a property of the
 *  server rather than a simplification. A run nothing holds is RE-HOSTED on
 *  demand (`hostOrRehost`, run_host.go): pause, resume, retry and the step verbs
 *  each start a process for the run and let KAS rehydrate it from disk, so a
 *  container restart and a closed launching chat are no longer states a button
 *  has to know about. A verb the run's own state refuses is answered 409 by KAS
 *  itself, whose reason names the state — which is why the buttons render from
 *  status alone.
 *
 *  An unknown status is ABSENT rather than mapped to an empty list, so a future
 *  KAS status degrades to a read-only view instead of a wrong control. */
export const RUN_CONTROLS: Record<string, readonly RunVerb[]> = {
  running: ["pause", "cancel"],
  paused: ["resume", "cancel"],
  // A completed run is a record: nothing to retry, nothing to stop.
  completed: [],
  // Retry resets only the FAILED and aborted nodes plus their ancestors, so the
  // completed work survives — unlike relaunching the recipe, which starts at
  // step one. It is legal exactly here, and exactly here vibekit has already
  // closed the run's bridge, which is why the server RE-HOSTS one rather than
  // requiring it (run_host.go `Retry`).
  //
  // Offered only on a PARENTLESS run (user decision): an agent-parented run's
  // recovery is the agent's own, on a bridge it already holds. The gate is the
  // caller's, not this table's — the table is status-only, as its name says.
  failed: ["retry"],
  aborted: ["retry"],
};

/** Button text per verb. */
export const CONTROL_LABEL: Record<RunVerb, string> = {
  pause: "Pause",
  resume: "Resume",
  cancel: "Cancel",
  retry: "Retry failed steps",
};

/** The two RECOGNISED clean endings. Lives here rather than in `run-view.ts`
 *  because this module is already the pure, DOM-free owner of the wire's status
 *  vocabulary — which is what lets a test be exhaustive over `RUN_STATUSES` rather
 *  than over whatever subset a table happens to name. */
const CLEAN_ENDINGS: ReadonlySet<string> = new Set(["completed", "cancelled"]);

/**
 * Whether a run ended in a way that leaves a reader nothing to come back for.
 *
 * ONE OF FOUR CONDITIONS on the automatic sub-tab close (`run-view.ts`
 * `autoCloseRunSubTab` holds the other three: the app opened the tab and this
 * client still holds the claim, the run's DOT state is `done`, and the tab is not
 * the one on screen). It is deliberately NARROWER than the dot: `runStatusFor`'s
 * `default` arm answers `done` for any unrecognised terminal status, and a status
 * this build has never seen is not one to close a tab on.
 *
 * `cancelled` is in, and stays in: the reader asked for the stop, so there is
 * nothing they were waiting to read. `failed` and `aborted` are out because a
 * failure is not noise — it is the run whose detail is worth the row.
 *
 * WHY THIS DOES NOT CONTRADICT AN OPEN RESULTS BOX. The exec page opens its
 * results by default because a run's product is what a reader opens the page for;
 * this closes a tab that shows it. The two never describe the same tab — the gates
 * only reach tabs nobody claimed and nobody is looking at — and a close destroys
 * no durable content: re-opening the run rebuilds the whole page from
 * `GET /api/runs/{id}`, results included and open. The one thing a close loses is
 * the LIVE step transcript, which is live-only either way and is lost by a refresh
 * whether the tab closed or not.
 */
export function runEndedCleanly(status: string): boolean {
  return CLEAN_ENDINGS.has(status);
}
