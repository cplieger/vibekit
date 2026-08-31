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

import { effect, signal } from "@cplieger/reactive";
import { setTabStatus, tabIdFor, tabSetVersion } from "./tabs.js";
import { runStatusFor } from "./store.js";
import { hasPendingDecision } from "./decision-dock.js";
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
    // Both keys the dock files a run's asks under, the same join
    // `mountRunDecisionDock` makes: `run_id` on the payload for an agent-launched
    // run, the synthetic chat id for a parentless one. Asking both costs one extra
    // map read and means a relocated ask cannot go unnoticed.
    const asking = hasPendingDecision(`run:${workflowID}`) || hasPendingDecision(workflowID);
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
    void version.value;
    // Subscribes to the tab SET as well, so a run tab arriving after its run's
    // frames (the open_tab round trip) or restored on boot paints without a
    // fresh run event.
    void tabSetVersion();
    // Subscribes to the dock queue too: hasPendingDecision reads queueVersion,
    // so an ask arriving for a background run repaints its dot with no run
    // event. And to every tracked run's own cell, through repaint's runState
    // reads.
    repaint();
  });
}
