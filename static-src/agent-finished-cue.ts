// ---------------------------------------------------------------------------
// The agent-finished cue: raised when a turn ends and EVERYTHING that turn started
// is over, withheld until then.
//
// One module owns the whole policy — the per-kind switch, the replay dedup, the
// settle test, the parking and the re-fire — because the alternative is a switch
// checked at the defer and not at the raise, or two debounce maps that disagree
// about whether a chat has already been told. `handlers/turn.ts` hands over one
// fact (the turn ended, and this is what it would say) and makes no decision.
//
// WHY A DEFERRAL AND NOT A WIDER PREDICATE AT THE TURN. The turn genuinely ended,
// and the run it launched genuinely has not; the reader wants ONE cue, at the end
// of the work. So the cue is parked with the body the TURN's own outcome produced
// and released by whatever ends the last outstanding thing — which is the release
// path for a run that FAILED, was aborted or was cancelled just as much as one that
// completed, because all four arrive as the same terminal `run_finished` frame.
//
// THE RE-FIRE IS AN EFFECT, not a second event wire, and that is `chat-settled.ts`'s
// doing: every term of `chatSettled` is a tracked read, so one effect over the
// parked set re-runs on every live-run rebuild, every run lifecycle frame, every ask
// pushed or answered, and every move in a chat's own turn state.
//
// NOT PERSISTED, deliberately. A reload drops the parked set, which is correct: the
// reader has come back to the app, so the cue has served its purpose. A persisted
// deferral would raise an OS notification for work the reader has already seen —
// the same defect in the opposite direction.
// ---------------------------------------------------------------------------

import { effect, signal, touch } from "@cplieger/reactive";
import { chatSettled } from "./chat-settled.js";
import { isAgentFinishedEnabled, notifyIfHidden, NOTIFY_TITLE } from "./notify.js";

/** Per-chat dedup window. An SSE reconnect replays `turn_ended`, and the duplicates
 *  arrive within milliseconds of each other. */
const DEDUP_MS = 2000;
/** How long a dedup stamp is worth keeping. Pruning is what stops the map growing
 *  for a workspace whose chats churn while notifications are switched off, which is
 *  the one state the notify path never visits. */
const DEDUP_STALE_MS = 10_000;
/** Bound on both maps. */
const MAP_CAP = 200;

/** chat id -> the millisecond a cue was last raised for it. */
const lastNotifyMs = new Map<string, number>();

/** chat id -> the body of the cue withheld for it.
 *
 *  Insertion-ordered, so the eviction below drops the OLDEST parked cue, which is
 *  the one whose chat has been waiting longest without settling. */
const deferred = new Map<string, string>();

/** Bumped when a cue is PARKED, so the release effect below has something to wake
 *  on before any chat is in the set.
 *
 *  Required rather than tidy: an effect's dependency set is whatever it read during
 *  its last run, so an effect that found the set empty read no signal at all and
 *  would never run again. Deliberately NOT bumped when an entry LEAVES — the effect
 *  writes that path, and a write to a signal the effect itself read is the cycle the
 *  reactive layer refuses. A stale subscription to a departed chat's signals costs
 *  one extra pass that finds nothing. */
const deferredVersion = signal(0);

/** Drop dedup stamps nobody needs, and cap what is left. */
function pruneNotifyMap(now: number): void {
  for (const [chatID, at] of lastNotifyMs) {
    if (now - at > DEDUP_STALE_MS) {
      lastNotifyMs.delete(chatID);
    }
  }
  while (lastNotifyMs.size > MAP_CAP) {
    const oldest = lastNotifyMs.keys().next().value;
    if (oldest === undefined) {
      break;
    }
    lastNotifyMs.delete(oldest);
  }
}

/** Raise the cue now, through the per-kind switch and the dedup window.
 *
 *  The switch is re-read HERE rather than trusted from the defer, because a
 *  deferral can outlive the reader's decision to be told: a cue parked while
 *  agent-finished notifications were on and released after they were switched off
 *  must not fire.
 *
 *  There is deliberately no TIME bound on a parked cue, so a run that legitimately
 *  executes for hours still releases its cue when it ends. What bounds the set is
 *  the entry cap: a run whose row leaks server-side cannot hold a slot forever, and
 *  an evicted cue is DROPPED rather than fired, because raising a cue for work
 *  nothing can confirm has ended is the worse direction. */
function raiseAgentFinished(chatID: string, body: string): boolean {
  if (!isAgentFinishedEnabled()) {
    return false;
  }
  const now = Date.now();
  pruneNotifyMap(now);
  if (now - (lastNotifyMs.get(chatID) ?? 0) <= DEDUP_MS) {
    return false;
  }
  lastNotifyMs.set(chatID, now);
  return notifyIfHidden(NOTIFY_TITLE, body);
}

/** A turn ended on `chatID` and `body` is what a cue for it would say ("" for a turn
 *  that says nothing at all — a cancel, or an end vibekit could not read).
 *
 *  Raises immediately when the chat is settled, parks the cue otherwise. The
 *  per-kind switch is checked before parking as well as before raising, so a cue is
 *  never held for a channel the reader has switched off. */
export function noteAgentFinished(chatID: string, body: string): void {
  if (chatID === "" || body === "" || !isAgentFinishedEnabled()) {
    return;
  }
  if (chatSettled(chatID)) {
    raiseAgentFinished(chatID, body);
    return;
  }
  // A repeat park is an overwrite by key, so a replayed `turn_ended` cannot produce
  // two cues for one turn — the dedup window in the raise covers the same burst on
  // the immediate path.
  deferred.set(chatID, body);
  while (deferred.size > MAP_CAP) {
    const oldest = deferred.keys().next().value;
    if (oldest === undefined) {
      break;
    }
    deferred.delete(oldest);
  }
  deferredVersion.value = deferredVersion.peek() + 1;
}

/** Drop a chat's parked cue. Called wherever the chat itself goes away — a tab
 *  close, a remote delete — because a cue for a conversation that no longer exists
 *  can never be acted on. */
export function forgetDeferredCue(chatID: string): void {
  deferred.delete(chatID);
}

/** Whether a cue is currently parked for `chatID`. Read by tests and by nothing in
 *  production: the release is an effect, so no caller polls. */
export function hasDeferredCue(chatID: string): boolean {
  return deferred.has(chatID);
}

/** Wire the release. Called from the composition root rather than at import: an
 *  effect running at module load would read a store that has not been hydrated yet,
 *  and the tab restore is what brings the first live-run inventory in.
 *
 *  ONE effect, because every input is a signal read inside the pass it makes:
 *  `deferredVersion` for the set itself, and inside `chatSettled` each parked chat's
 *  session signal, the live-run inventory's version and the dock's queue version. */
export function installDeferredCueSubscriber(): () => void {
  return effect(() => {
    touch(deferredVersion);
    // A copy, so deleting the entry a pass is releasing cannot disturb the walk.
    for (const [chatID, body] of [...deferred]) {
      if (!chatSettled(chatID)) {
        continue;
      }
      // Removed BEFORE the raise: the raise reaches `notifyIfHidden`, and a cue that
      // stayed parked through a refused raise would be re-offered on every later
      // pass for the rest of the page's life.
      deferred.delete(chatID);
      raiseAgentFinished(chatID, body);
    }
  });
}

/** Reset every map. Exported for test isolation only. */
export function _resetAgentFinishedCueForTest(): void {
  deferred.clear();
  lastNotifyMs.clear();
  deferredVersion.value = 0;
}
