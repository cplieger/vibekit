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
  get,
  getActiveId,
  tabStatusFor,
  setTurnFailed,
  setTurnDone,
  clearSteers,
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
import { setLastError, clearLastError } from "../send-state.js";
import { refreshGitBadge } from "../git.js";
import { showBanner, onTurnEnded, type BannerLink } from "../banner-stack.js";
import { openSetting } from "../settings-highlight.js";
import { showLoginModal } from "../modals.js";
import { respondPermission, respondElicitation, respondUserInput } from "../actions/chat.js";
import { ERROR_ROUTES, type ErrorAction } from "./error-routing.js";
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
  clearLastError();
  onTurnEnded(chatID);
  refreshGitBadge();
  // KAS clears its steering buffer at EVERY turn boundary, so the chip row must
  // empty here too. The `steer_cleared` frame covers the case where something
  // was still unread; this covers the ordinary one, where every steer was
  // injected and the server sends nothing because there was nothing to drop.
  clearSteers(chatID);
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

onSSE("error", (chatID, p) => {
  // --- Side-effects: fire unconditionally regardless of active chat ---
  // Unfreeze thinking so send-state can settle correctly.
  setThinking(chatID, false);
  // Latch the failure on the CHAT, then re-derive its tab dot. This half has to
  // run for a background chat: the prose below deliberately does not (one chat's
  // failure must not claim another's send button), which left a failed
  // background turn with no marker anywhere — its dot simply went out, exactly
  // as if the turn had finished cleanly.
  setTurnFailed(chatID);
  setTabStatus(chatID, tabStatusFor(get(chatID), hasPendingDecision(chatID)));

  const code = p.code;
  const msg = p.message;

  // Only surface the error PROSE for the active chat, to avoid polluting the
  // send-button state with errors from background chats.
  if (chatID !== getActiveId()) {
    return;
  }

  const route = ERROR_ROUTES[code];
  if (route === undefined) {
    // Unknown codes: fall through to send-button blocker.
    setLastError(msg !== "" ? `${code}: ${msg}` : code);
    return;
  }
  switch (route.surface) {
    case "banner": {
      // A routed banner that names an action carries a jump to it, so the
      // message and the thing that fixes it are one click apart instead of the
      // reader having to hunt the panel the prose named.
      showBanner(chatID, code, msg, route.level, route.dismissible, bannerLinkFor(route.action));
      break;
    }
    case "send-error":
      setLastError(`${code}: ${msg}`);
      break;
    default:
      route.surface satisfies never;
  }
});
