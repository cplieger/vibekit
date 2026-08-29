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
// So this file ROUTES and interprets nothing. One store invalidation, one bus
// emit, two facts recorded, and the toasts. Every surface that shows a run —
// the transcript's run card, the `/run/{id}` view, the tab dot — reads
// `run-store.ts` and re-renders itself when that store changes.
//
// Surfaces, and why each is reached the way it is:
//
//   - `run-store.ts`, by direct import: it is a leaf over api-client, so it drags
//     nothing in behind it, and it is the ONE fetch for all three readers.
//   - the history list, over the BUS. Two reasons, and the second is the
//     load-bearing one: they are UI affordances that should not know about each
//     other, and importing history.ts from here drags chat.ts in behind it —
//     which put real network calls into every test that touches this handler.
//   - the ephemeral stack, by direct import (toast.ts is a leaf over
//     ui-primitives, so it drags nothing in behind it either).
//   - the tab dot, via `run-dots.ts`, which needs one fact the store cannot
//     carry: whether the run is parentless.
//
// A run's own transcript needs nothing here: a step's content arrives on the
// launching chat's connection as ordinary blocks, attributed to the step, through
// the same handlers that render every other block — and lands inside its run's
// card because its subtask id names the run.
// ---------------------------------------------------------------------------

import { onSSE, emitBus, BUS_RUNS_CHANGED } from "../bus.js";
import { info, success, error } from "../toast.js";
import { invalidateRun, noteRunChat, noteRunLive, noteRunSettled } from "../run-store.js";
import { trackRun } from "../run-dots.js";
import { autoCloseRunSubTab, openRunSubTab, applyRunStep } from "../run-view.js";

// ---------------------------------------------------------------------------
// The signal half: an ephemeral toast at each end of a run.
//
// Two different questions, so two different filters. A START is only worth
// announcing for a SCHEDULED run: a manual launch already has the user's
// attention, since they clicked Run and a run tab opened in front of them, and an
// agent-launched one now grows a card in the transcript the reader is watching. A
// COMPLETION is worth announcing for any run, because nothing else tells anyone:
// vibekit.PushKind has no run member, and the schedule row only speaks when
// someone opens /docs/workflows.
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
//
// The chat id is READ rather than discarded, and it answers two questions at once:
// a NON-empty one names the chat whose agent launched this run, which is the
// parent its tab nests under; an empty one means the run is parentless, and its
// tab has no parent to nest under.
onSSE("run_started", (chatID, p) => {
  trackRun(p.workflow_id);
  noteRunChat(p.workflow_id, chatID);
  noteRunLive(p.workflow_id, chatID);
  openRunSubTab(p.workflow_id, runLabel(p.name), chatID);
  invalidateRun(p.workflow_id);
  emitBus(BUS_RUNS_CHANGED);
  if (p.scheduled === true && !announcedStarts.has(p.workflow_id)) {
    announcedStarts.add(p.workflow_id);
    info(`Scheduled run started: ${runLabel(p.name)}`);
  }
});

onSSE("run_finished", (chatID, p) => {
  trackRun(p.workflow_id);
  // Recorded even here: the launching chat is what a LATER re-open nests under, and
  // a client that joined after the run ended still needs it.
  noteRunChat(p.workflow_id, chatID);
  // `paused` keeps the run in the live inventory: it is stopped waiting for
  // something, not over (KAS reports an onMaxIterations policy stop through
  // this same frame), and the server's lease survives a pause the same way.
  // Anything else — the four terminal statuses and any status this client
  // does not recognise — is an ending, matching toastCompletion below.
  if (p.status !== "paused") {
    noteRunSettled(p.workflow_id);
  }
  // Deliberately NOT opened. A run that finished before anyone looked has nothing
  // live to watch, and a tab appearing at the moment work ENDS is noise; History is
  // the door to a finished run.
  invalidateRun(p.workflow_id);
  emitBus(BUS_RUNS_CHANGED);
  announcedStarts.delete(p.workflow_id);
  toastCompletion(p.status, p.name);
  // AFTER the toast, which is what the closing tab is traded for: the strip stops
  // carrying a row for work that is over, and the toast is what says the work is
  // over. Which tabs qualify is run-view.ts's — a tangent's sub-tab is
  // unreachable from there, and so is any run tab a reader opened themselves.
  autoCloseRunSubTab(p.workflow_id, p.status);
});

// A PARENTLESS run's step content, and the one run event that is not an
// invalidation — there is nothing to invalidate, because a step's transcript is
// not in `inspect` and no endpoint serves it. So this is the only frame whose
// payload is READ rather than used as a signal to refetch.
//
// It reaches exactly one surface. The run tab holds the card whose step rows the
// content belongs in, and it drops a frame naming a run it is not showing; the
// event is workspace-global (a parentless run has no chat to address), so every
// client receives every run's steps and the tab is what narrows that.
//
// A chat-parented run raises none of these: its steps travel as ordinary blocks on
// the launching chat's connection and already reach that chat's transcript, keyed
// by `_meta.kiro.workflow`. See internal/agent/run_host.go for the split.
onSSE("run_step", (_chatID, p) => {
  applyRunStep(p);
});

onSSE("run_progress", (chatID, p) => {
  trackRun(p.workflow_id);
  noteRunChat(p.workflow_id, chatID);
  // A progress frame is proof of life: a client that missed run_started (a
  // mid-run join, a rebuild that raced a fresh launch) re-learns the run here.
  noteRunLive(p.workflow_id, chatID);
  // Also opens, because `run_started` can be missed: a client that connects mid-run
  // gets no replay of it (the run events are not in the SSE replay ring's
  // pending-state synthesis), so the progress frames are the only door left. It
  // cannot fight a reader who closed the tab: `openRunSubTab` offers once per run
  // per client, so a close is final for the automatic path.
  openRunSubTab(p.workflow_id, "Workflow run", chatID);
  invalidateRun(p.workflow_id);
});
