// ---------------------------------------------------------------------------
// Workflow-run SSE handlers.
//
// Three events, and all three mean the same thing to this client: something about
// a run changed, go and read it. That is the whole contract — the payloads are
// deliberately too thin to reconstruct a run from, because a client that
// accumulated them would garble it. `run_start` re-fires on every resume (three
// frames were measured for one run), and `node_complete` carries neither
// `iteration` nor `branchId`, so two passes of one loop are indistinguishable on
// the wire. `_kiro/workflow/inspect` is the truth; these events only say when to
// ask it.
//
// Two surfaces react, and neither is created by this handler:
//
//   - an open run review, which re-reads the run it is showing (a direct call:
//     run-view.ts is a leaf over api-client + tabs)
//   - the history list, reached over the BUS. Two reasons, and the second is the
//     load-bearing one: they are UI affordances that should not know about each
//     other, and importing history.ts from here drags chat.ts in behind it —
//     which put real network calls into every test that touches this handler.
//
// A run's own transcript needs nothing here: a step's content arrives on the
// launching chat's connection as ordinary blocks, attributed to the step, through
// the same handlers that render every other block.
//
// A THIRD surface reacts since the toasts landed: the ephemeral stack, reached by
// a direct import for the same reason run-view is (toast.ts is a leaf over
// ui-primitives, so it drags nothing in behind it).
// ---------------------------------------------------------------------------

import { onSSE, emitBus, BUS_RUNS_CHANGED } from "../bus.js";
import { refreshRunView } from "../run-view.js";
import { info, success, error } from "../toast.js";

// ---------------------------------------------------------------------------
// The signal half: an ephemeral toast at each end of a run.
//
// Two different questions, so two different filters. A START is only worth
// announcing for a SCHEDULED run: a manual launch already has the user's
// attention, since they clicked Run and a run tab opened in front of them. A
// COMPLETION is worth announcing for any run, because nothing else tells anyone:
// vibekit.PushKind has no run member, the schedule row only speaks when someone opens
// /docs/workflows, and one shared #run-dock element means a background run tab has
// no on-screen surface at all.
//
// `scheduled` has to come from the SERVER. A parentless run's frames are
// workspace-global with an empty chat id and a manual launch is parentless too, so
// nothing observable here separates the two.
//
// The overlap with an open run view is real and narrow: that view repaints its
// status word in place on this same event, so a reader watching the foreground tab
// sees one word twice. Fired anyway, because the repaint is silent, has no
// transition, and deliberately suppresses its loading row on a refetch — it is
// exactly the change a reader misses — and because the run on screen is the only
// case that overlaps at all.
// ---------------------------------------------------------------------------

/** Runs whose start has already been announced. `run_start` re-fires on every
 *  resume (probe 6 measured three frames for one run) and toast.ts coalesces
 *  nothing, so without this one scheduled run produces three identical toasts.
 *  Cleared when the run reports finished, which is also what makes a resumed run
 *  announce itself again: the client stopped believing it was running. Bounded by
 *  the runs this tab has seen start and not finish. */
const announcedStarts = new Set<string>();

/** How a run's own name reads in a toast, or a generic label for a run this
 *  client has no name for (a page opened mid-run, or a frame carrying no state). */
function runLabel(name: string | undefined): string {
  return name === undefined || name === "" ? "Workflow run" : name;
}

/** The completion signal. Level follows the outcome rather than the event, because
 *  "finished" is not a verdict: failed and aborted are failures, cancelled is what
 *  the user asked for, and `paused` is not a completion at all (KAS reports an
 *  onMaxIterations policy stop through this same frame), so it gets no toast — the
 *  run is still this process's to resume, and calling it finished would be wrong. */
function toastCompletion(status: string, name: string | undefined): void {
  const label = runLabel(name);
  switch (status) {
    case "completed":
      success(`${label} finished`);
      return;
    case "failed":
      error(`${label} failed`);
      return;
    case "aborted":
      error(`${label} was aborted`);
      return;
    case "cancelled":
      info(`${label} was cancelled`);
      return;
    case "paused":
      return;
    default:
      // An unrecognised status is still an ending. Better to name it verbatim
      // than to stay silent or to guess a level for it.
      info(`${label} finished: ${status}`);
  }
}

// The two ends of a run change the LIST as well as the run: one adds a row, the
// other settles its outcome. Everything between them only changes the run.
onSSE("run_started", (_chatID, p) => {
  refreshRunView(p.workflow_id);
  emitBus(BUS_RUNS_CHANGED);
  if (p.scheduled === true && !announcedStarts.has(p.workflow_id)) {
    announcedStarts.add(p.workflow_id);
    info(`Scheduled run started: ${runLabel(p.name)}`);
  }
});

onSSE("run_finished", (_chatID, p) => {
  refreshRunView(p.workflow_id);
  emitBus(BUS_RUNS_CHANGED);
  announcedStarts.delete(p.workflow_id);
  toastCompletion(p.status, p.name);
});

onSSE("run_progress", (_chatID, p) => {
  refreshRunView(p.workflow_id);
});
