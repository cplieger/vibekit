// ---------------------------------------------------------------------------
// SSE handlers for turn lifecycle + the three decision types + errors.
//
// A decision is ENQUEUED, not shown: decision-dock.ts owns a per-chat queue
// and renders the active chat's head, so these handlers do not gate on
// getActiveId() — a permission raised on a background chat must still reach
// the dock, with the tab dot pointing at it.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import {
  setWorkingLabel,
  setTurnSummary,
  get,
  getActiveId,
  setTurnFailed,
  setTurnDone,
  dropSteers,
} from "../store.js";
import { notifyIfHidden, isAgentFinishedEnabled, NOTIFY_TITLE } from "../notify.js";
import { pushDecision, collapseSettledDecision, dropTurnDecisions } from "../decision-dock.js";
import { setAgentDown, clearAgentDown } from "../send-state.js";
import { reportFailure } from "../failure-notice.js";
import { refreshGitBadge } from "../git.js";
import type { ToastRetry } from "../toast.js";
import { openSetting } from "../settings-highlight.js";
import { showLoginModal } from "../modals.js";
import { respondPermission, respondElicitation, respondUserInput } from "../actions/chat.js";
import { ERROR_ROUTES, type ErrorAction } from "./error-routing.js";
import { clearTurnState } from "../turn-teardown.js";
import { refreshTurnRail } from "../turn-rail.js";
export { ERROR_ROUTES };

/** Track last notification time per chat to avoid duplicate notifications
 *  on SSE reconnect replay (events arrive within milliseconds). */
const _lastNotifyMs = new Map<string, number>();
const _NOTIFY_STALE_MS = 10_000;
/** Hard cap: prune aggressively past this size (e.g. notifications disabled
 *  so the map is never pruned by the notify path). */
const _NOTIFY_MAP_CAP = 200;

function _pruneNotifyMap(now: number): void {
  for (const [k, v] of _lastNotifyMs) {
    if (now - v > _NOTIFY_STALE_MS) {
      _lastNotifyMs.delete(k);
    }
  }
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
  // Every ask this turn raised is over — a workflow run's ask survives since
  // it outlives the turn that launched it. Must run before the dot is
  // re-derived, or a stale ask decides the state one last time.
  dropTurnDecisions(chatID);
  // The turn's own verdict. `completed` only arrives if the model calls its
  // status tool. Latched even for the chat the reader is watching (2026-08):
  // skipping it there hid the "I am done" state at the exact moment it
  // happened. Cleared only by the next turn's progress, matching
  // web-terminal-kiro's engine-side latch.
  //
  // Outcome decides, not stop reason: `cancelled`/`interrupted`/`unknown`
  // latch neither, since the user stopped it or nothing said how it went.
  if (p.outcome === "completed") {
    setTurnDone(chatID);
  } else if (p.outcome === "failed" || p.outcome === "refused") {
    setTurnFailed(chatID);
  }
  clearTurnState(chatID);
  // This chat's turn index changed, so its rail record needs a re-read.
  void refreshTurnRail(chatID);
  clearAgentDown();
  refreshGitBadge();
  // KAS clears its steering buffer at every turn boundary; anything still in
  // the dock was never read. `dropSteers` promotes each as "not delivered"
  // rather than deleting it silently.
  dropSteers(chatID);

  const now = Date.now();
  _pruneNotifyMap(now);

  const stopReason = p.stop_reason;
  if (stopReason !== "cancelled" && isAgentFinishedEnabled()) {
    // Dedup: SSE reconnect replay can fire duplicates in rapid succession.
    const last = _lastNotifyMs.get(chatID) ?? 0;
    if (now - last > 2000) {
      _lastNotifyMs.set(chatID, now);
      const s = get(chatID);
      const name = s?.name ?? "Chat";
      notifyIfHidden(NOTIFY_TITLE, `${name}: Agent finished`);
    }
  }

  // Turn summary (credits · elapsed · files changed), stamped onto the last
  // assistant message; the renderer projects it into a keyed `.turn-footer`.
  // Unconditional so a background turn's footer is present on switch.
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
  if (p.model !== undefined) {
    summary.model = p.model;
  }
  setTurnSummary(chatID, summary);
});

// Each of the three asks below notifies unconditionally, gated only by the
// master notifications switch. Each blocks the turn until answered, so a
// per-kind mute would stall every later turn with nothing on screen saying
// why. Settings -> Permissions relaxation is what stops the asks entirely.

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

// Every ask above is offered to every surface at once; only the first answer
// is accepted, so the server names the settled request and this retires the
// card everywhere else.
onSSE("decision_settled", (chatID, p) => {
  collapseSettledDecision(chatID, p.kind, p.request_id, p.settled_by);
});

// --- Data-driven error classification (imported from error-routing.ts) ---

/** Turns a route's declared action into the toast's one action slot. */
function toastActionFor(action: ErrorAction | undefined): ToastRetry | undefined {
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

// This handler touches no turn state: the server ends every turn exactly once
// via `turn_ended`, so an error is a report only.
onSSE("error", (chatID, p) => {
  const code = p.code;
  const msg = p.message;

  const route = ERROR_ROUTES[code];
  if (route === undefined) {
    reportFailure(chatID, msg !== "" ? msg : code);
    return;
  }
  switch (route.surface) {
    case "toast":
      // Reported for every chat, not just the active one: a background
      // chat's failure must still surface, since it claims no shared control.
      reportFailure(chatID, msg, toastActionFor(route.action));
      break;
    case "agent-down":
      // Active chat only: this DOES paint a shared control, so a background
      // chat's dead bridge must not alert the button of the chat in use.
      if (chatID === getActiveId()) {
        setAgentDown(msg !== "" ? msg : "The agent could not be started for this chat.");
      }
      break;
    default:
      route.surface satisfies never;
  }
});
