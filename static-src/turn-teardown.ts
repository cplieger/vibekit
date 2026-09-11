// ---------------------------------------------------------------------------
// The outcome-independent half of "a turn is no longer running".
//
// THREE doors reach it and they are not the same event. `turn_ended` is an OUTCOME:
// the server says how the turn finished, so it latches `done` or `failed`, fires
// the finished notice and stamps the footer. `transport:gap` is an ABSENCE: the
// replay ring no longer covers what this client missed, so it can assert nothing
// about how anything finished and instead UNLATCHES what it can no longer support.
// `healSettledChat` below is the third, and it is a server STATEMENT that no turn
// is open at all — so it re-derives the verdict AFTER the teardown instead of
// applying one before it.
//
// What they share is everything else, and until this module existed the gap door
// spelled it independently and was short by three effects — the two in-flight
// markers and the rail — so a reconnect left a chunk watermark that dropped the
// next turn's early deltas, and a live-message marker that made the next refetch
// keep a message the chat file already held.
//
// Deliberately NOT here, and each absence is the asymmetry rather than an
// oversight: the outcome latches (a gap knows no outcome), the finished
// notification (nothing finished), `clearAgentDown` (a turn ending PROVES an agent
// is behind the chat; a gap proves nothing), the decision/steer teardown,
// whose two doors differ in kind — a turn end retires that turn's asks and
// promotes an unread steer into the transcript, while a gap drops every ask
// including a run's and FORGETS its steers rather than asserting the agent never
// read them — and the rail's session-wide index. A turn ending changes ONE
// chat's set of turns, so that door re-reads that chat's index; a gap makes
// every chat's index equally unsupportable, which the sync epoch already
// records, so its door heals only the active chat and leaves the rest to
// their next activation instead of fanning a fetch out per open chat.
// ---------------------------------------------------------------------------

import {
  setThinking,
  clearChunkWatermark,
  clearLiveTurnMessage,
  clearTruncatedSnapshots,
  relatchTurnVerdict,
  get,
  tabStatusFor,
} from "./store.js";
import { setTabStatus, tabIdFor } from "./tabs.js";
import { hasPendingDecision } from "./decision-dock.js";
import { drainModelSwitchQueue } from "./model-switcher.js";

/** Bring one chat's local turn state to rest.
 *
 *  On its first two doors the caller has already applied whatever that door knows — an
 *  outcome latch, or the unlatching a gap owes — because the tab dot is re-derived here
 *  and reads those latches plus the dock's queue.
 *
 *  `healSettledChat` INVERTS that deliberately: it has no outcome to apply up front, so it
 *  re-derives the verdict AFTERWARDS and the dot painted at the end of this function reads
 *  the pre-heal latches, then repaints a moment later. The double paint is accepted; the
 *  alternative ordering means asking `relatchTurnVerdict` while `thinking` is still true and
 *  narrowing its guard to get an answer, which is the shape that paints a `failed` latch
 *  over a streaming reply. */
export function clearTurnState(chatID: string): void {
  setThinking(chatID, false);
  // The chunk watermark and the in-flight-message marker are both finished business.
  // Left standing, the first drops the NEXT turn's early chunks as already-folded and
  // the second makes a later refetch keep a message the chat file now holds under a
  // different shape. The mark covers what the live stream folded in as well as what a
  // server copy of the turn declared, so it is per-turn state either way.
  clearChunkWatermark(chatID);
  clearLiveTurnMessage(chatID);
  // The third fact from the same connect: a capped turn_state's withheld-output
  // note. The turn is over, so `message_appended` has delivered the whole message
  // (the outcome door) or the replay ring no longer covers what was missed (the
  // gap door) — either way there is nothing left for the note to be true about,
  // and left standing it claims output is still coming.
  clearTruncatedSnapshots(chatID);
  // A queued mid-turn model switch drains on the turn ending. On the gap door the
  // turn_ended that would have drained it may be among the dropped events, which
  // is what left the switch stranded behind a stuck `.pending` pill.
  drainModelSwitchQueue(chatID);
  // Last: the dot is derived from everything above plus the caller's own latches.
  // The TAB id, resolved from the subject — setTabStatus takes the opaque
  // server-minted id, and this call passed the CHAT id for months, a silent
  // no-op masked by the chat row effect repainting on the same signal churn.
  setTabStatus(tabIdFor("chat", chatID), tabStatusFor(get(chatID), hasPendingDecision(chatID)));
}

/** The FULL teardown plus a re-derivation. Admissible ONLY where the server's statement
 *  covers the chat's WHOLE liveness, which is one door: a newest-page GET answering
 *  `turn_open === false`, meaning "no turn record AND no admitted prompt".
 *
 *  Ordering is the reason no caller has to reason about latch precedence: `clearTurnState`
 *  runs FIRST, so by the time `relatchTurnVerdict` is asked, `thinking` is false and its own
 *  guard passes for the honest reason rather than being narrowed.
 *
 *  A door whose statement is narrower than this one may NOT use it — see
 *  `retractStaleThinking`, and do not add a third spelling. */
export function healSettledChat(chatID: string): void {
  clearTurnState(chatID);
  relatchTurnVerdict(chatID);
}

/** The NARROW retraction: stop believing a stale `thinking`, re-derive the verdict, and touch
 *  nothing else.
 *
 *  For a door whose statement is about a RUN, or about this chat's OWN turn alone — the
 *  connect replay's busy set deliberately excludes a workflow-step turn, so a chat it omits
 *  may still have one open. Every other per-turn marker `clearTurnState` drops belongs to
 *  whatever turn is genuinely still streaming, above all the live-turn message marker, which
 *  is the ONLY thing stopping `loadMessages`' array replacement from deleting an in-flight
 *  assistant reply.
 *
 *  No `setTabStatus` of its own: `setThinking` ends in `scheduleMessages(id, "fact")` and
 *  `chat.ts`'s chat-row effect repaints the row from the session signal, which is the churn
 *  every other session write already relies on — so this door paints once where the full
 *  teardown paints twice.
 *
 *  Unconditional rather than gated on `thinking` being true: `setThinking(false)` clears no
 *  latch (both are cleared only on TRUE) and `relatchTurnVerdict` returns early when a
 *  verdict already stands. Two side effects come with that on a chat that was NOT thinking,
 *  and the run door is the one that reaches such a chat: `setThinking` also stamps the
 *  eviction LRU, and its false branch writes `working_label: "Thinking"`, discarding a live
 *  turn's declared label. Both are harmless here — a run door's chat did just have run
 *  activity — but they are the cost, not one signal write. */
export function retractStaleThinking(chatID: string): void {
  setThinking(chatID, false);
  relatchTurnVerdict(chatID);
}
