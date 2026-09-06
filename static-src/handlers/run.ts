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
import {
  invalidateRun,
  noteRunChat,
  noteRunLive,
  noteRunSettled,
  hasLiveRunForChat,
} from "../run-store.js";
import { isThinking } from "../store.js";
import { trackRun } from "../run-dots.js";
import { autoCloseRunSubTab, noteAutoOpenedRun, applyRunStep } from "../run-view.js";
import {
  pushDecision,
  collapseSettledRunInput,
  dropRunAsks,
  dropTurnDecisions,
} from "../decision-dock.js";
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
  // A start frame is proof of execution: it fires on the launch and again on every
  // resume, so it is exactly the moment frames begin arriving into this chat.
  noteRunLive(p.workflow_id, chatID, true);
  // The tab itself is the SERVER's, opened at the frame that grants the run's
  // lease. What is recorded here is only that the tab is the app's doing, which is
  // what lets the completion auto-close tell it from one the reader asked for.
  noteAutoOpenedRun(p.workflow_id, chatID);
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
  if (p.status === "paused") {
    // A policy stop (`onMaxIterations`) reports through this same frame, so the run
    // is resumable and its row stays — but it has stopped executing, which is what
    // lets its chat's message window be reclaimed while it sits.
    noteRunLive(p.workflow_id, chatID, false);
  } else {
    noteRunSettled(p.workflow_id);
    // A run that is over cannot still be waiting on a person. Which asks that
    // reaches, and why each kind needs it, is `dropRunAsks`' own doc.
    dropRunAsks(p.workflow_id);
    // And the ORPHANS that sweep cannot name: a step's request-shaped ask whose
    // `run_id` arrived EMPTY (the step-session registry had not seen its
    // sub-session). Every run-scoped remover keys on `runID`, so such an ask is
    // reachable only by the turn-scoped sweep — and its one other trigger,
    // `turn_ended` on this chat, never fires for a step-driven turn, because the
    // attribution gate drops that turn's `turn_end`.
    //
    // A run's terminal frame is the turn-boundary-equivalent moment for it: the
    // launching turn ended when the run was created, so anything still queued here
    // with no `runID` is moot. Both gates are load-bearing. Without the first, a
    // user who prompted this chat while the run was going has that turn's own
    // permission ask swept, stranding a live JSON-RPC request. Without the second,
    // a SIBLING run's orphan — also `runID: ""`, and indistinguishable from this
    // run's — is swept while that run is still executing; `noteRunSettled` ran two
    // lines above, so the predicate answers about siblings only.
    //
    // An empty envelope chat id is a parentless run, whose asks `dropRunAsks`
    // already reached through the `run:<id>` key. There is no chat to sweep.
    if (chatID !== "" && !isThinking(chatID) && !hasLiveRunForChat(chatID)) {
      dropTurnDecisions(chatID);
    }
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
// endpoint serves it. Reaches exactly one surface: the run tab's DETAIL PANE,
// which hosts a transcript per node path.
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
  // re-learns the run here. Whether it is proof of EXECUTION is the kind's to
  // say — the run-level `paused` folds into this event, so treating every
  // progress frame as executing would let a parked run keep its chat's window
  // resident on the strength of the frame that parked it. A node-level pause
  // (`node_paused`) is a step waiting inside a run that is still going, so it
  // deliberately reads as executing.
  noteRunLive(p.workflow_id, chatID, p.kind !== "paused");
  // No tab open, and no auto-open MARKER either. The server retries its own offer
  // on each step's frame, and claiming the tab here would let the completion
  // auto-close take one a mid-run reader opened themselves.
  invalidateRun(p.workflow_id);
});
