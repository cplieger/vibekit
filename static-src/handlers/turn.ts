// ---------------------------------------------------------------------------
// SSE handlers for turn lifecycle + the three decision types + errors.
//
// A decision is ENQUEUED, not shown: decision-dock.ts owns a per-chat queue
// and renders the active chat's head. That is why these handlers no longer gate
// on getActiveId() — a permission raised on a background chat used to be
// dropped here and only came back if the SSE connection happened to reconnect,
// which left the agent waiting on an answer the user was never offered. The
// notification still fires either way; the tab dot is what points at it.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import {
  setThinking,
  setWorkingLabel,
  setTurnSummary,
  clearSnapshotSeq,
  clearLiveTurnMessage,
  get,
  getActiveId,
  tabStatusFor,
  setTurnFailed,
  setTurnDone,
  dropSteers,
} from "../store.js";
import { notifyIfHidden, isAgentFinishedEnabled, NOTIFY_TITLE } from "../notify.js";
import {
  pushDecision,
  collapseSettledDecision,
  hasPendingDecision,
  dropTurnDecisions,
} from "../decision-dock.js";
import { drainModelSwitchQueue } from "../model-switcher.js";
import { setTabStatus } from "../tabs.js";
import { setAgentDown, clearAgentDown } from "../send-state.js";
import { reportFailure } from "../failure-notice.js";
import { refreshGitBadge } from "../git.js";
import { showBanner, onTurnEnded, type BannerLink } from "../banner-stack.js";
import { openSetting } from "../settings-highlight.js";
import { showLoginModal } from "../modals.js";
import { respondPermission, respondElicitation, respondUserInput } from "../actions/chat.js";
import { ERROR_ROUTES, type ErrorAction } from "./error-routing.js";
import type { ErrorCode } from "../wire/types.gen.js";
import { refreshTurnRail } from "../turn-rail.js";
export { ERROR_ROUTES };

// `notifyAndBadge` is gone. Its badge half wrote the literal 1 into
// document.title whenever the page was hidden, which is a FLAG where the reader
// wants a count; the attention fold carries the real number, and two writers
// fighting over one title would make the count wrong rather than absent. The
// Notification half is `notifyIfHidden`, called directly.

/** Track last notification time per chat to avoid duplicate notifications
 *  on SSE reconnect replay (events arrive within milliseconds). */
const _lastNotifyMs = new Map<string, number>();
const _NOTIFY_STALE_MS = 10_000;
/** Hard cap: if the map exceeds this size, prune aggressively. Prevents
 *  unbounded growth when many chats fire turn_ended without pruning
 *  (e.g. notifications disabled so the notify path is skipped). */
const _NOTIFY_MAP_CAP = 200;

function _pruneNotifyMap(now: number): void {
  for (const [k, v] of _lastNotifyMs) {
    if (now - v > _NOTIFY_STALE_MS) {
      _lastNotifyMs.delete(k);
    }
  }
  // Hard cap: if still over limit after stale pruning, drop oldest entries.
  if (_lastNotifyMs.size > _NOTIFY_MAP_CAP) {
    const excess = _lastNotifyMs.size - _NOTIFY_MAP_CAP;
    let dropped = 0;
    for (const k of _lastNotifyMs.keys()) {
      if (dropped >= excess) {
        break;
      }
      _lastNotifyMs.delete(k);
      dropped++;
    }
  }
}

onSSE("working_label", (chatID, p) => {
  setWorkingLabel(chatID, p.label);
});

onSSE("turn_ended", (chatID, p) => {
  // --- Side-effects: fire unconditionally regardless of active chat or dedup ---
  setThinking(chatID, false);
  // The turn's snapshot chunk-watermark (connect-time turn_state) is
  // finished business once the turn ends.
  clearSnapshotSeq(chatID);
  // So is the in-flight marker. `message_appended` normally clears it first —
  // the server persists the turn before it broadcasts either frame — so this is
  // the backstop for a turn that ended without one (an abandoned turn, a dropped
  // frame), where leaving the marker would make a later refetch keep a message
  // the chat file already holds under a different shape.
  clearLiveTurnMessage(chatID);
  // Every ask this TURN raised is over: each one blocks the turn, so a turn that
  // has ended is not waiting on one. Answered asks are already gone; what is left
  // is an abandoned queue (the user cancelled, and cmdCancel cleared the server's
  // own pending set), and leaving it here marked the chat `input` forever —
  // `tabStatusFor` puts that ahead of everything. A workflow run's ask survives:
  // it outlives the turn that launched it. Runs BEFORE the dot is re-derived, or
  // the stale ask decides the state one last time.
  dropTurnDecisions(chatID);
  // A finished turn is `done` even when the agent never said so. `completed`
  // arrives only if the model calls its status tool, so the transport's own
  // verdict is what makes "the last turn finished" hold; tabStatusFor still
  // prefers `completed` where it lands.
  //
  // ONE condition, and the second one was removed in 2026-08 (user decision):
  // the latch used to be skipped for the chat the reader was WATCHING, on the
  // reasoning that a green dot beside the transcript you are reading is noise.
  // The effect was that the dot fell back to hollow `idle` at the exact moment a
  // turn completed in front of you, so the state that means "I am done" was the
  // one state you could never see happen. web-terminal-kiro latches its own
  // `done` in the engine, focus-blind and cleared only by the next turn's
  // progress state (`terminal/events.go`), and this is now the same rule.
  // Nothing is lost on the attention side: a cue on the watched chat is
  // acknowledged as it is observed (attention.ts `refresh`), so the title count
  // and the favicon still ignore it.
  //
  // A CANCELLED turn is still not `done` — it finished nothing, which is the
  // same line the "Agent finished" notification draws below.
  if (p.stop_reason !== "cancelled") {
    setTurnDone(chatID);
  }
  // Re-derive the per-tab activity dot for the chat whose turn ended, even
  // when it's a background tab. Shares tabStatusFor with the store effect:
  // thinking clears, but an agent-declared waiting_on_user survives turn
  // end (that's its point — "I asked you something") and so does a RUN's
  // pending decision, whose ask outlives the turn that raised it.
  setTabStatus(chatID, tabStatusFor(get(chatID), hasPendingDecision(chatID)));
  // A turn ended, so there is demonstrably an agent behind this chat. Failure
  // toasts are deliberately NOT retracted here: they report a past event and
  // time out on their own, and the turn's divider keeps the record regardless.
  clearAgentDown();
  onTurnEnded(chatID);
  refreshGitBadge();
  // KAS clears its steering buffer at EVERY turn boundary, so the dock must empty
  // here too — and anything still sitting in it was never read, which is a fact
  // worth keeping. `dropSteers` promotes each one into the transcript as "not
  // delivered" with its text and a put-it-back control, rather than deleting it
  // silently. The ordinary path leaves nothing to do: every steer was injected,
  // so the dock is already empty and the server sent no `steer_cleared` because
  // there was nothing to drop.
  dropSteers(chatID);
  // The model-switch queue still drains off the per-chat turn_ended (not the
  // active-only turn:idle bus event) so a background chat's queued switch fires
  // when ITS turn ends. There is no prompt queue left to drain — a mid-turn
  // prompt is a steer now and was delivered during the turn.
  drainModelSwitchQueue(chatID);
  // turn_ended is the only moment the set of turns changes, so it is the only
  // moment the rail's session-wide index needs re-reading.
  void refreshTurnRail(chatID);

  // Prune stale entries unconditionally to prevent unbounded growth
  // when notifications are disabled (the notify path below is the only
  // consumer but may be skipped).
  const now = Date.now();
  _pruneNotifyMap(now);

  const stopReason = p.stop_reason;
  if (stopReason !== "cancelled" && isAgentFinishedEnabled()) {
    // Dedup: skip notification if we already notified for this chat
    // within the last 2s (SSE reconnect replay fires duplicates in
    // rapid succession).
    const last = _lastNotifyMs.get(chatID) ?? 0;
    if (now - last > 2000) {
      _lastNotifyMs.set(chatID, now);
      const s = get(chatID);
      const name = s?.name ?? "Chat";
      notifyIfHidden(NOTIFY_TITLE, `${name}: Agent finished`);
    }
  }

  // Turn summary (credits · elapsed · files changed): stamp it onto the last
  // assistant message via the store. The renderer projects it into a keyed
  // `.turn-footer` under that turn (fundamentals/turn-footer.ts). This replaces
  // the old direct DOM writes here — those weren't in the store, so they
  // double-rendered on SSE replay and vanished on refresh. Unconditional (not
  // active-gated): a background turn's footer is then present on switch, and
  // the server persists the same fields so it survives reload.
  const summary: {
    credits?: number;
    elapsedMs?: number;
    changedFiles?: typeof p.changed_files;
    model?: string;
  } = {};
  if (p.credits_delta !== undefined) {
    summary.credits = p.credits_delta;
  }
  if (p.elapsed_ms !== undefined) {
    summary.elapsedMs = p.elapsed_ms;
  }
  if (p.changed_files !== undefined) {
    summary.changedFiles = p.changed_files;
  }
  // Which model served the turn. Live half of the pair; the same value is
  // persisted on the message so the footer survives a reload.
  if (p.model !== undefined) {
    summary.model = p.model;
  }
  setTurnSummary(chatID, summary);
});

// The three asks below notify unconditionally, gated only by the master
// notifications switch inside notifyIfHidden. Each one BLOCKS the turn until it
// is answered and none has a per-tab marker of its own, so a per-kind mute
// stalled every later turn of that chat with nothing on screen to say why. What
// relaxes the interruptions is the Settings -> Permissions workspace
// relaxation, which stops the asks from being raised at all.

onSSE("permission_needed", (chatID, p) => {
  notifyIfHidden(
    NOTIFY_TITLE,
    p.files !== undefined && p.files.length > 0
      ? "Review this turn's changes"
      : "Permission needed",
  );
  pushDecision({
    kind: "permission",
    chatID,
    runID: p.run_id ?? "",
    requestID: p.request_id,
    payload: p,
    submit: (optionID, fileDecisions) => {
      void respondPermission.dispatch(
        fileDecisions !== undefined
          ? { chatID, requestID: p.request_id, optionID, fileDecisions }
          : { chatID, requestID: p.request_id, optionID },
      );
    },
  });
});

onSSE("elicitation_needed", (chatID, p) => {
  notifyIfHidden(NOTIFY_TITLE, "Input requested by a tool");
  pushDecision({
    kind: "elicitation",
    chatID,
    runID: p.run_id ?? "",
    requestID: p.request_id,
    payload: p,
    submit: (action, content) => {
      void respondElicitation.dispatch(
        content !== undefined
          ? { chatID, requestID: p.request_id, action, content }
          : { chatID, requestID: p.request_id, action },
      );
    },
  });
});

onSSE("user_input_needed", (chatID, p) => {
  notifyIfHidden(NOTIFY_TITLE, "The agent has a question");
  pushDecision({
    kind: "user_input",
    chatID,
    runID: p.run_id ?? "",
    requestID: p.request_id,
    payload: p,
    submit: (action, answer) => {
      void respondUserInput.dispatch(
        action === "answered" && answer !== undefined
          ? { chatID, requestID: p.request_id, action, answer }
          : { chatID, requestID: p.request_id, action },
      );
    },
  });
});

// The three asks above are offered to every surface at once and only the first
// answer is accepted, so the server names the settled request and this retires
// the card everywhere else. It is the whole client half of that: the dock owns
// the queue, so the handler routes and interprets nothing.
onSSE("decision_settled", (chatID, p) => {
  collapseSettledDecision(chatID, p.kind, p.request_id, p.settled_by);
});

// --- Data-driven error classification (imported from error-routing.ts) ---

/** Turn a route's declared action into the banner's link. The table stays data;
 *  this is the one place that knows how each kind is performed. */
function bannerLinkFor(action: ErrorAction | undefined): BannerLink | undefined {
  if (action === undefined) {
    return undefined;
  }
  switch (action.kind) {
    case "setting":
      return {
        label: action.label,
        onClick: () => {
          openSetting(action.tab, action.control);
        },
      };
    case "sign-in":
      return { label: action.label, onClick: showLoginModal };
    default:
      action satisfies never;
      return undefined;
  }
}

/** Does this error END the running turn?
 *
 *  `surface` already carries that judgement and the routing table states it per
 *  code: a `toast` or `agent-down` means the send did not land, a `banner` means
 *  something else is wrong while the turn runs on ("A banner rather than a toast
 *  because it is not one send that is broken"). Deriving the answer keeps one
 *  table instead of a second field that is a pure function of the first.
 *
 *  An unknown code ends the turn, because a `thinking` left stuck true holds the
 *  composer on Cancel for a turn nothing will ever finish. */
function endsTurn(code: ErrorCode): boolean {
  return ERROR_ROUTES[code]?.surface !== "banner";
}

onSSE("error", (chatID, p) => {
  const code = p.code;
  const msg = p.message;

  // --- Side-effects: fire regardless of active chat ---
  // A banner-surfaced error must NOT touch the turn lifecycle. `agent_config_error`
  // is the measured case: KAS scans `.kiro/agents/**` at session construction, so
  // one malformed file there fires this event at the START of the first turn. That
  // cleared `thinking`, and `thinking` is what messages.ts reads to decide whether
  // an assistant bubble subscribes to its own deltas — so a `.kiro` typo froze the
  // whole transcript at its first streamed chunk. Same shape for `agent_not_found`
  // (a fallback notice), `rate_limit` (the model is retrying), `mode_not_applied`
  // (the chat is running, in another mode) and `compaction_failed`.
  if (endsTurn(code)) {
    // Unfreeze thinking so send-state can settle correctly.
    setThinking(chatID, false);
    // Latch the failure on the CHAT, then re-derive its tab dot.
    setTurnFailed(chatID);
    setTabStatus(chatID, tabStatusFor(get(chatID), hasPendingDecision(chatID)));
  }

  const route = ERROR_ROUTES[code];
  if (route === undefined) {
    // An unknown code is a failed attempt of unknown shape, so it takes the same
    // surface as the known ones. The code stands in when the server sent no
    // message, which is the only time machine vocabulary is better than nothing.
    reportFailure(chatID, msg !== "" ? msg : code);
    return;
  }
  switch (route.surface) {
    case "toast":
      // Reported for EVERY chat, not just the active one, and that is the half the
      // send button could not do. This used to sit behind a `chatID !==
      // getActiveId()` return, on the correct reasoning that one chat's failure
      // must not claim another's send button — but the consequence was that a
      // background chat's failure left nothing anywhere except a tab dot. A toast
      // claims no control, and failure-notice.ts names the chat when it is not the
      // one on screen.
      reportFailure(chatID, msg);
      break;
    case "agent-down":
      // Active chat only: this one DOES paint a shared control, so a background
      // chat's dead bridge must not put an alert face on the button of the chat
      // the reader is using. The chat keeps its red tab dot either way, and
      // send-state.ts re-clears the state on every chat switch.
      if (chatID === getActiveId()) {
        setAgentDown(msg !== "" ? msg : "The agent could not be started for this chat.");
      }
      break;
    case "banner": {
      // Banners are per-chat and keyed by chat, so this stays gated on the active
      // chat exactly as before: banner-stack.ts renders the active chat's stack.
      //
      // A routed banner that names an action carries a jump to it, so the
      // message and the thing that fixes it are one click apart instead of the
      // reader having to hunt the panel the prose named.
      if (chatID === getActiveId()) {
        showBanner(chatID, code, msg, route.level, route.dismissible, bannerLinkFor(route.action));
      }
      break;
    }
    default:
      route.surface satisfies never;
  }
});
