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
