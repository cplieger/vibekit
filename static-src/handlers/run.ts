// ---------------------------------------------------------------------------
// Workflow-run SSE handlers.
//
// Three events, all meaning "something about a run changed, go read it" —
// the payloads are too thin to reconstruct a run from (`run_start` re-fires
// on every resume; `node_complete` carries neither `iteration` nor
// `branchId`). `_kiro/workflow/inspect` is the truth; these events only say
// when to ask it.
//
// So this file routes and interprets nothing: one store invalidation, one
// bus emit, two facts recorded, and toasts. Every surface that shows a run
// reads `run-store.ts` and re-renders when that store changes.
//
// Surfaces: `run-store.ts` by direct import (a leaf over api-client, the one
// fetch for all readers); the history list over the bus (importing
// history.ts here would drag chat.ts in behind it); the toast stack by
// direct import; the tab dot via `run-dots.ts` (needs to know whether the
// run is parentless).
//
// A run's own transcript needs nothing here: a step's content arrives on
// the launching chat's connection as ordinary blocks through the same
// handlers that render every other block.
// ---------------------------------------------------------------------------

import { onSSE, emitBus, BUS_RUNS_CHANGED } from "../bus.js";
import { info, success, error } from "../toast.js";
import { invalidateRun, noteRunChat, noteRunLive, noteRunSettled } from "../run-store.js";
import { trackRun } from "../run-dots.js";
import { autoCloseRunSubTab, openRunSubTab, applyRunStep } from "../run-view.js";
import { pushDecision, collapseSettledRunInput } from "../decision-dock.js";
import { answerRunInput, continueRunStep } from "../actions/runs.js";
import { notifyIfHidden, NOTIFY_TITLE } from "../notify.js";

// ---------------------------------------------------------------------------
// The signal half: an ephemeral toast at each end of a run.
//
// A START is only worth announcing for a SCHEDULED run: a manual launch
// already has the user's attention and an agent-launched one grows a card
// in the transcript. A COMPLETION is worth announcing for any run, since
// nothing else tells anyone. `scheduled` has to come from the server — a
// parentless run's frames are workspace-global and a manual launch is
// parentless too, so nothing observable here separates the two.
// ---------------------------------------------------------------------------

/** Runs whose start has already been announced. `run_start` re-fires on
 *  every resume, so without this a scheduled run produces duplicate
 *  toasts. Cleared when the run reports finished. */
const announcedStarts = new Set<string>();

/** How a run's own name reads in a toast, or a generic label. */
function runLabel(name: string | undefined): string {
  return name === undefined || name === "" ? "Workflow run" : name;
}

/** The completion signal. Level follows the outcome, not the event: failed
 *  and aborted are failures, cancelled is what the user asked for, and
 *  `paused` gets no toast (KAS reports an onMaxIterations stop through this
 *  same frame; the run is still resumable). */
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
      info(`${label} finished: ${status}`);
  }
}

// The chat id is read rather than discarded: non-empty names the launching
// chat (the parent tab this run nests under); empty means parentless.
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
  // Recorded even here: a later re-open nests under the launching chat.
  noteRunChat(p.workflow_id, chatID);
  // `paused` stays live: stopped waiting for something, not over. Anything
  // else is an ending.
  if (p.status !== "paused") {
    noteRunSettled(p.workflow_id);
  }
  // Deliberately NOT opened: a run that finished before anyone looked has
  // nothing live to watch. History is the door to a finished run.
  invalidateRun(p.workflow_id);
  emitBus(BUS_RUNS_CHANGED);
  announcedStarts.delete(p.workflow_id);
  toastCompletion(p.status, p.name);
  // AFTER the toast: the strip stops carrying a row for work that is over,
  // and the toast is what says the work is over.
  autoCloseRunSubTab(p.workflow_id, p.status);
});

// A parentless run's step content — the one run event that is not an
// invalidation, since a step's transcript is not in `inspect` and no
// endpoint serves it. Reaches exactly one surface: the run tab holding the
// card whose step rows the content belongs in.
//
// A chat-parented run raises none of these: its steps travel as ordinary
// blocks on the launching chat's connection, keyed by `_meta.kiro.workflow`.
onSSE("run_step", (_chatID, p) => {
  applyRunStep(p);
});

// A workflow STEP asked a person a question and its run is parked until somebody
// answers. The one run event that reaches the interaction dock, and the reason it
// carries a payload rather than an invalidation: KAS parks the run with a fixed
// pauseReason literal and an empty pauseDetail, so refetching `inspect` says a
// step wants input and never says what it asked.
//
// The chat id comes off the ENVELOPE, exactly as the three request-shaped asks
// take theirs: the launching chat for an agent-parented run, `run:<workflowId>`
// for a parentless one. That is what puts the card in the parent tab's composer
// dock and in the run tab's at once, since one Decision matches both hosts.
//
// `notifyIfHidden` like the other three, and with the strongest case of the four:
// this ask blocks a run indefinitely and the run may have no surface anyone is
// looking at.
onSSE("run_input_needed", (chatID, p) => {
  trackRun(p.workflow_id);
  // The launching chat, like run_started and run_progress record it. It matters most
  // on the connect replay: after a reload the replayed ask can be the FIRST frame
  // this client sees for a run, and without it the run card's footer link opens the
  // tab top-level instead of beside the conversation that launched it. A parentless
  // run's ask arrives keyed to the synthetic `run:<workflowId>`, which noteRunChat
  // refuses — that is not a chat id.
  noteRunChat(p.workflow_id, chatID);
  notifyIfHidden(NOTIFY_TITLE, "A workflow step is waiting for your answer");
  pushDecision({
    kind: "run_input",
    chatID,
    runID: p.workflow_id,
    askID: p.ask_id,
    payload: p,
    submit: (text) => {
      if (text === null) {
        // Continue without answering. Addressed by NODE rather than by ask, because
        // that is what the step-status verb takes — which is also why the card does
        // not offer the button at all for an ask carrying no node id (the verb refuses
        // 400 without one). The server settles the ask when the status write lands.
        void continueRunStep.dispatch({ workflowID: p.workflow_id, nodeID: p.node_id });
        return;
      }
      void answerRunInput.dispatch({ workflowID: p.workflow_id, ask_id: p.ask_id, text });
    },
  });
});

// Its twin, and it does the same job `decision_settled` does for the three
// request-shaped asks: every surface is offered the ask and only the first answer
// is accepted, so something has to retire the cards that lost.
onSSE("run_input_settled", (_chatID, p) => {
  collapseSettledRunInput(p.workflow_id, p.ask_id, p.settled_by);
});

onSSE("run_progress", (chatID, p) => {
  trackRun(p.workflow_id);
  noteRunChat(p.workflow_id, chatID);
  // A progress frame is proof of life: a client that missed run_started
  // re-learns the run here.
  noteRunLive(p.workflow_id, chatID);
  // Also opens, since `run_started` can be missed (run events are not in
  // the SSE replay ring). `openRunSubTab` offers once per run per client,
  // so a close stays final for the automatic path.
  openRunSubTab(p.workflow_id, "Workflow run", chatID);
  invalidateRun(p.workflow_id);
});
