// ---------------------------------------------------------------------------
// SSE handlers for turn lifecycle + permission dialog + errors.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { setThinking, setWorkingLabel, setTurnSummary, get, getActiveId } from "../store.js";
import {
  notifyIfHidden,
  setBadge,
  isAgentFinishedEnabled,
  isPermissionNeededEnabled,
  NOTIFY_TITLE,
} from "../notify.js";
import { showPermissionDialog } from "../permission.js";
import { showElicitationDialog } from "../elicitation.js";
import { drainNext } from "../prompt-queue.js";
import { drainModelSwitchQueue } from "../model-switcher.js";
import { setTabStatus } from "../tabs.js";
import { setLastError, clearLastError } from "../send-state.js";
import { refreshGitBadge } from "../git.js";
import { showBanner, onTurnEnded } from "../banner-stack.js";
import { respondPermission, respondElicitation } from "../actions/chat.js";
import { ERROR_ROUTES } from "./error-routing.js";
export { ERROR_ROUTES };
export { wireCheckpointRestore } from "./checkpoint-restore.js";

/** Notify the user and set the badge if the page is hidden. */
function notifyAndBadge(title: string, body: string): void {
  notifyIfHidden(title, body);
  if (document.visibilityState === "hidden") {
    setBadge(1);
  }
}

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
  // Clear the per-tab "thinking" activity dot for the chat whose turn ended,
  // even when it's a background tab (its own per-chat thinking signal is not
  // tracked by the active-session effect, so the dot would otherwise stick).
  setTabStatus(chatID, "");
  clearLastError();
  onTurnEnded(chatID);
  refreshGitBadge();
  // Prompt queue and model-switch queue both drain off the per-chat turn_ended
  // (not the active-only turn:idle bus event) so a background chat's queued
  // prompt AND queued model switch fire when ITS turn ends.
  drainNext(chatID);
  drainModelSwitchQueue(chatID);

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
      notifyAndBadge(NOTIFY_TITLE, `${name}: Agent finished`);
    }
  }

  // Turn summary (credits · elapsed · files changed): stamp it onto the last
  // assistant message via the store. The renderer projects it into a keyed
  // `.turn-footer` under that turn (fundamentals/turn-footer.ts). This replaces
  // the old direct DOM writes here — those weren't in the store, so they
  // double-rendered on SSE replay and vanished on refresh. Unconditional (not
  // active-gated): a background turn's footer is then present on switch, and
  // the server persists the same fields so it survives reload.
  const summary: { credits?: number; elapsedMs?: number; changedFiles?: typeof p.changed_files } =
    {};
  if (p.credits_delta !== undefined) {
    summary.credits = p.credits_delta;
  }
  if (p.elapsed_ms !== undefined) {
    summary.elapsedMs = p.elapsed_ms;
  }
  if (p.changed_files !== undefined) {
    summary.changedFiles = p.changed_files;
  }
  setTurnSummary(chatID, summary);
});

onSSE("permission_needed", (chatID, p) => {
  if (isPermissionNeededEnabled()) {
    notifyAndBadge(NOTIFY_TITLE, `Permission needed: ${p.title ?? "Tool"}`);
  }
  if (chatID !== getActiveId()) {
    return;
  }

  const toolCallInput = lookupToolInput(chatID, p.tool_call_id ?? "");
  showPermissionDialog(
    p.title ?? "Tool",
    p.tool_call_id ?? "",
    p.kind ?? "",
    toolCallInput,
    p.options,
    (optionID: string) => {
      void respondPermission.dispatch({
        chatID,
        requestID: p.request_id,
        optionID,
      });
    },
  );
});

onSSE("elicitation_needed", (chatID, p) => {
  if (isPermissionNeededEnabled()) {
    notifyAndBadge(NOTIFY_TITLE, "Input requested by a tool");
  }
  if (chatID !== getActiveId()) {
    return;
  }
  showElicitationDialog(p, (action, content) => {
    void respondElicitation.dispatch(
      content !== undefined
        ? { chatID, requestID: p.request_id, action, content }
        : { chatID, requestID: p.request_id, action },
    );
  });
});

function lookupToolInput(chatID: string, toolCallID: string): unknown {
  if (toolCallID === "") {
    return undefined;
  }
  const s = get(chatID);
  if (s === undefined) {
    return undefined;
  }
  for (let i = s.messages.length - 1; i >= 0; i--) {
    const m = s.messages[i];
    if (m?.tool_calls === undefined) {
      continue;
    }
    for (const tc of m.tool_calls) {
      if (tc.id === toolCallID) {
        return tc.input;
      }
    }
  }
  return undefined;
}

// --- Data-driven error classification (imported from error-routing.ts) ---

onSSE("error", (chatID, p) => {
  // Unfreeze thinking so send-state can settle correctly.
  setThinking(chatID, false);

  const code = p.code;
  const msg = p.message;

  // Only surface errors for the active chat to avoid polluting the
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
    case "banner":
      showBanner(chatID, code, msg, route.level, route.dismissible);
      break;
    case "send-error":
      setLastError(`${code}: ${msg}`);
      break;
    default:
      route.surface satisfies never;
  }
});

// --- Checkpoint restore (extracted to handlers/checkpoint-restore.ts) ---
