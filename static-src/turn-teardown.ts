// ---------------------------------------------------------------------------
// The outcome-independent half of "a turn is no longer running".
//
// TWO doors reach it and they are not the same event. `turn_ended` is an OUTCOME:
// the server says how the turn finished, so it latches `done` or `failed`, fires
// the finished notice and stamps the footer. `transport:gap` is an ABSENCE: the
// replay ring no longer covers what this client missed, so it can assert nothing
// about how anything finished and instead UNLATCHES what it can no longer support.
//
// What they share is everything else, and until this module existed the gap door
// spelled it independently and was short by four effects — the transient banners,
// the two in-flight markers and the rail — so a reconnect left a rate-limit banner
// over a finished turn, a chunk watermark that dropped the next turn's early
// deltas, and a live-message marker that made the next refetch keep a message the
// chat file already held.
//
// Deliberately NOT here, and each absence is the asymmetry rather than an
// oversight: the outcome latches (a gap knows no outcome), the finished
// notification (nothing finished), `clearAgentDown` (a turn ending PROVES an agent
// is behind the chat; a gap proves nothing), and the decision/steer teardown,
// whose two doors differ in kind — a turn end retires that turn's asks and
// promotes an unread steer into the transcript, while a gap drops every ask
// including a run's and FORGETS its steers rather than asserting the agent never
// read them.
// ---------------------------------------------------------------------------

import { setThinking, clearSnapshotSeq, clearLiveTurnMessage, get, tabStatusFor } from "./store.js";
import { setTabStatus } from "./tabs.js";
import { hasPendingDecision } from "./decision-dock.js";
import { onTurnEnded } from "./banner-stack.js";
import { refreshTurnRail } from "./turn-rail.js";
import { drainModelSwitchQueue } from "./model-switcher.js";

/** Bring one chat's local turn state to rest.
 *
 *  The caller has already applied whatever its own door knows — an outcome latch,
 *  or the unlatching a gap owes — because the tab dot is re-derived here and reads
 *  those latches plus the dock's queue. */
export function clearTurnState(chatID: string): void {
  setThinking(chatID, false);
  // The connect-time turn_state watermark and the in-flight-message marker are
  // both finished business. Left standing, the first drops the NEXT turn's early
  // chunks as already-folded and the second makes a later refetch keep a message
  // the chat file now holds under a different shape.
  clearSnapshotSeq(chatID);
  clearLiveTurnMessage(chatID);
  // One owner for which banners a turn boundary retires, so a second transient
  // code added there reaches both doors instead of one.
  onTurnEnded(chatID);
  // A queued mid-turn model switch drains on the turn ending. On the gap door the
  // turn_ended that would have drained it may be among the dropped events, which
  // is what left the switch stranded behind a stuck `.pending` pill.
  drainModelSwitchQueue(chatID);
  // The set of turns only changes when one ends, so this is the only moment the
  // rail's session-wide index needs re-reading.
  void refreshTurnRail(chatID);
  // Last: the dot is derived from everything above plus the caller's own latches.
  setTabStatus(chatID, tabStatusFor(get(chatID), hasPendingDecision(chatID)));
}
