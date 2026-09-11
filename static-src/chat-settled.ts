// ---------------------------------------------------------------------------
// "Is everything this chat started actually over?" — ONE predicate, consulted by
// every path that raises an out-of-page cue for a finished turn.
//
// THE HOLE IT FILLS. A chat's TURN ending is not the same fact as a chat's WORK
// ending, and `run_workflow` returns as soon as the run is created — so the
// launching turn ends, `thinking` clears, the tab dot goes green and the run
// carries on for another forty minutes. Every cue path read the turn's own end
// and nothing else: `handlers/turn.ts` pushed a browser notification on the frame
// that settled the turn, and `cueCandidates` reported that chat as `done`, so it
// counted toward the app badge. Both said the work was finished while it was
// still going.
//
// A RECORD, NOT A BARE BOOL, because every caller needs the REASON as well as the
// verdict: the deferral's own log line, the badge rule, and the tests all want to
// tell "a run is still going" from "an ask is open". `chatSettled` is the fold for
// the callers that only need the verdict.
//
// EVERY READ IS TRACKED, which is what makes the re-fire in `agent-finished-cue.ts`
// a plain effect rather than a second event wire: `watchSession` subscribes to that
// chat's own session signal, `liveRunIDsForChat` touches the live-run inventory's
// version, and `hasPendingDecision` touches the dock's queue. An effect calling
// this therefore re-runs when a run starts or ends, when an ask arrives or is
// answered, and when the chat's own turn state moves.
//
// SUBAGENTS ARE DELIBERATELY NOT AN INPUT. A delegate runs INSIDE its launching
// turn, so `turn` already covers one that is still going; and `openSubagentRefs`
// answers only for delegates whose sub-tab a reader happens to have opened, so a
// predicate reading it would give a different answer per reader. A run is the one
// kind of work that can outlive the turn that started it, which is why it is the
// term this module exists for.
// ---------------------------------------------------------------------------

import { hasPendingDecision } from "./decision-dock.js";
import { liveRunIDsForChat } from "./run-store.js";
import { turnLive, watchSession } from "./store.js";

/** What is still outstanding for a chat. Every field is a reason a cue must wait. */
export interface ChatOutstanding {
  /** This chat's own turn is running (`turnLive`: thinking, an open turn, or a row
   *  that states no liveness at all). */
  readonly turn: boolean;
  /** How many live workflow runs this chat launched — parked ones INCLUDED, because
   *  a run stopped on a person is precisely the case a badge must not call finished
   *  (`run-store.ts` records why `hasLiveRunForChat` is the settle question and
   *  `hasExecutingRunForChat` is the store-eviction one). */
  readonly runs: number;
  /** An unanswered decision sits in this chat's dock queue. */
  readonly asks: boolean;
}

/** Everything outstanding for `chatID`, each term named.
 *
 *  An unknown chat is outstanding-free: it has no session, no live run and no
 *  queue, so a caller needs no second existence check. */
export function chatOutstanding(chatID: string): ChatOutstanding {
  // No early return for an empty id, deliberately: all three reads answer the
  // empty case correctly on their own, and a branch that skipped them would leave
  // a calling effect subscribed to nothing at all.
  const session = watchSession(chatID);
  return {
    turn: session !== undefined && turnLive(session),
    runs: liveRunIDsForChat(chatID).length,
    asks: hasPendingDecision(chatID),
  };
}

/** Is this chat fully settled — nothing running, nothing parked, nothing asked? */
export function chatSettled(chatID: string): boolean {
  const outstanding = chatOutstanding(chatID);
  return !outstanding.turn && outstanding.runs === 0 && !outstanding.asks;
}
