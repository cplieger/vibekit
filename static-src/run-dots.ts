// ---------------------------------------------------------------------------
// The activity dot for a workflow run's tab.
//
// A run is the one kind of work in this app that has no activity signal of its
// own. Its `run:<workflowId>` tab has no chat, no `Session` row and no `thinking`
// flag — the three things that make `tabStatusFor` work — so the strip showed a
// static glyph whether the run was going, blocked on a permission, or long
// finished. The reader could not tell a live background run from a dead one.
//
// EVERY RUN GETS ONE, agent-launched included, and that is a REVERSAL of the
// first cut. It covered parentless runs only, on the reasoning that an
// agent-launched run's launching chat already shows `working` for the turn that
// started it. That reasoning does not hold, and the reason is the whole shape of a
// run: `run_workflow` returns as soon as the run is CREATED, so the launching turn
// ends, the chat's `thinking` clears and its dot goes idle while the run carries on
// for another forty minutes. A chat's dot can only cover a run that is turn-bound,
// and no run is. The double-counting the exclusion was avoiding is not real either:
// the chat is idle by then, so the two dots describe different things.
//
// That "goes idle" is now TRUE rather than intended, and it was not when this was
// written. A chat-parented run executes on the launching chat's session, so a
// step's frames arrive on that chat's connection and used to open a turn there
// that latched `thinking` and re-latched it on every reconnect — so the launching
// chat read `working` for the whole run and this module's premise was false for
// exactly the runs it excluded. The turn is now marked as the RUN's on both sides
// (`vibekit.TurnSourceWorkflowStep`, `turn_state.workflow_step`, and the `wf:`
// gate in `handlers/messages.ts`), which is what leaves the run's liveness to the
// dot below and nothing else.
//
// The dot element needs no new markup. `createTabEl` builds a `.tab-status-dot`
// on EVERY row and paints it `""` for non-chat kinds, parking it in the trailing
// slot beside the pin marker where the editor's unsaved mark already lives, and
// `12-tabs.css` styles that position. What was missing was a writer.
//
// ONE WRITER, and it is the effect rather than the call sites. A run's dot has TWO
// inputs on different clocks — its lifecycle status, and whether the dock is
// holding an ask for it — so painting from the SSE handlers would cover the first
// and miss the second entirely, which is the state that matters most: a background
// run blocked on a permission with nobody watching. Both are signals
// (`run-store.ts` holds one per run, `hasPendingDecision` reads the dock's queue),
// so one effect over both repaints on either, the same shape `chat.ts`'s tab
// effect already uses.
//
// It keeps NO state of its own. It used to hold a parallel map of statuses fed by
// the run events, which is a second copy of something `GET /api/runs/{id}` already
// answers; the tracked set below is just which runs have been SEEN, a fact the
// status cannot carry.
// ---------------------------------------------------------------------------

import { effect, signal, touch } from "@cplieger/reactive";
import { setTabStatus, tabIdFor, tabSetVersion } from "./tabs.js";
import { runStatusFor } from "./store.js";
import { runPendingAsks } from "./decision-dock.js";
import { runState } from "./run-store.js";

/** The runs this client has seen an event for, and the version counter that makes
 *  the effect depend on the set.
 *
 *  A set of ids and nothing else: the STATUS comes from `run-store.ts` on every
 *  repaint, so there is no second copy of it to go stale. Bounded by runs seen
 *  this session — an id whose tab never opens or has closed paints nothing and
 *  costs one lookup per repaint. */
const tracked = new Set<string>();
const version = signal(0);

/** Start painting a run's tab. Called on every run event, for every run: the
 *  origin no longer decides, because a chat's own dot cannot cover a run that
 *  outlives its turn (see the header). */
export function trackRun(workflowID: string): void {
  if (workflowID === "" || tracked.has(workflowID)) {
    return;
  }
  tracked.add(workflowID);
  bump();
}

/** Nudge the dot without a new run event. Called by the run view after it reads
 *  `GET /api/runs/{id}`, which is what paints a tab restored on boot or opened
 *  from History onto a run already going — a PAUSED run emits no frames at all, so
 *  nothing else would. */
export function refreshRunDots(): void {
  bump();
}

/** Advance the counter without READING it as a dependency.
 *
 *  `peek` is load-bearing: both writers above are reachable from inside an effect
 *  (`run-view.ts`'s paint calls them), and a plain `version.value + 1` subscribes
 *  the CALLING effect to the signal it is about to write — a self-cycle the
 *  reactive layer refuses with `Cycle detected` on every paint of a run view. */
function bump(): void {
  version.value = version.peek() + 1;
}

function repaint(): void {
  for (const workflowID of tracked) {
    // The TAB id, resolved from the subject, and the DOCK key `run:<workflowId>`
    // below are two different strings now. The dock's is a synthetic chat id it
    // files a parentless run's asks under; the tab's is opaque and server-minted.
    const id = tabIdFor("run", workflowID);
    if (id === "") {
      // No tab YET — the automatic offer's open_tab round trip is still in
      // flight — or none any more. KEEP the id: the effect depends on the tab
      // set's version, so the dot paints the moment the row lands. The old
      // sweep DELETED the id here, which raced that round trip: `trackRun`
      // bumps only for a first-seen id, so a run that emitted no later frame
      // (a paused run emits none at all) was swept out before its tab existed
      // and its dot stayed blank until an unrelated dock churn repainted it.
      continue;
    }
    // The SAME join the other two run surfaces make (the transcript's card and the
    // exec page both take `runPendingAsks`), rather than two `hasPendingDecision`
    // reads.
    //
    // The pair it replaced could not see a chat-parented run's ask at all. That
    // predicate keys on a CHAT id: `run:<workflowId>` is the synthetic one a
    // parentless run's asks are filed under, so that read worked, while the second
    // read passed a WORKFLOW id where a chat id was expected and matched nothing
    // ever — an agent-launched run's ask sits under the launching chat's id with
    // the run stamped on the payload, which only a scan over `runID` finds. So the
    // one population whose asks arrive on a chat bridge had no amber dot.
    const asking = runPendingAsks(workflowID).count > 0;
    // TRACKED read, deliberately: the run's cell resolving (the fetch an
    // invalidation coalesces into) is exactly the moment the dot must repaint,
    // and `run_progress` for an already-tracked run bumps nothing else here.
    setTabStatus(id, runStatusFor(runState(workflowID)?.status, asking));
  }
}

/** Wire the effect. Called from the composition root, not at import: an effect
 *  running at module load would paint against a tab strip that has not been
 *  restored yet, and `setTabStatus` parks its state on a spec that does not exist
 *  then. The tab-set dependency is what picks a tab up once it does. */
export function installRunDotSubscriber(): void {
  effect(() => {
    touch(version);
    // Subscribes to the tab SET as well, so a run tab arriving after its run's
    // frames (the open_tab round trip) or restored on boot paints without a
    // fresh run event.
    void tabSetVersion();
    // Subscribes to the dock queue too: runPendingAsks reads queueVersion,
    // so an ask arriving for a background run repaints its dot with no run
    // event. And to every tracked run's own cell, through repaint's runState
    // reads.
    repaint();
  });
}
