// ---------------------------------------------------------------------------
// SSE handlers for turn lifecycle + permission dialog + errors.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { onSSE } from "../bus.js";
import {
  setThinking,
  setWorkingLabel,
  get,
  getActiveId,
  dequeuePrompt,
  peekQueuedAttachments,
} from "../store.js";
import {
  notifyIfHidden,
  setBadge,
  isAgentFinishedEnabled,
  isPermissionNeededEnabled,
  NOTIFY_TITLE,
} from "../notify.js";
import { showPermissionDialog } from "../permission.js";
import { showElicitationDialog, dismissElicitation } from "../elicitation.js";
import { sendPromptTo } from "../chat-commands.js";
import { setLastError, clearLastError } from "../send-state.js";
import { refreshGitBadge } from "../git.js";
import { showBanner, onTurnEnded } from "../banner-stack.js";
import { setSubagentPendingApproval } from "../crew-card.js";
import { respondPermission, respondElicitation } from "../actions/chat.js";
import { type ErrorRoute, ERROR_ROUTES } from "./error-routing.js";
export type { ErrorRoute };
export { ERROR_ROUTES };
export { wireCheckpointRestore } from "./checkpoint-restore.js";

/** Notify the user and set the badge if the page is hidden. */
function notifyAndBadge(title: string, body: string): void {
  notifyIfHidden(title, body);
  if (document.visibilityState === "hidden") {
    setBadge(1);
  }
}

/** Drain one queued prompt, restoring any attachments that were saved
 *  alongside it so they flow through the next sendPromptTo call. */
function drainQueuedPromptWithAttachments(chatID: string): void {
  const attachments = peekQueuedAttachments(chatID);
  const text = dequeuePrompt(chatID);
  if (text === undefined) {
    return;
  }
  const session = get(chatID);
  void sendPromptTo(chatID, text, {
    ...(session?.agent !== undefined && session.agent !== "" && { agent: session.agent }),
    ...(session?.model !== undefined && session.model !== "" && { model: session.model }),
    ...(attachments.length > 0 && { attachments }),
  });
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
  clearLastError();
  onTurnEnded(chatID);
  refreshGitBadge();
  drainQueuedPromptWithAttachments(chatID);

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

  // --- DOM rendering: only for the active chat ---
  if (chatID !== getActiveId()) {
    return;
  }

  // Single DOM lookup shared by both turn-summary and file-changes rendering.
  const msgsEl = document.getElementById("messages");
  if (msgsEl === null) {
    return;
  }

  // Render turn summary (credits + elapsed time).
  const credits = p.credits_delta;
  const elapsed = p.elapsed_ms;
  if ((credits !== undefined && credits > 0) || (elapsed !== undefined && elapsed > 0)) {
    const parts: string[] = [];
    if (credits !== undefined && credits > 0) {
      parts.push(`Est. ${credits.toFixed(2)} credits`);
    }
    if (elapsed !== undefined && elapsed > 0) {
      if (elapsed >= 60000) {
        const m = Math.floor(elapsed / 60000);
        const s = Math.floor((elapsed % 60000) / 1000);
        parts.push(`${String(m)}m ${String(s)}s`);
      } else {
        parts.push(`${(elapsed / 1000).toFixed(1)}s`);
      }
    }
    const summaryText = parts.join(" · ");

    const msgs = msgsEl.querySelectorAll(".message.assistant");
    const lastMsg = msgs[msgs.length - 1] as HTMLElement | undefined;
    const actionsRow = lastMsg?.nextElementSibling;
    if (actionsRow?.classList.contains("turn-actions")) {
      const leftSlot = actionsRow.querySelector(".turn-actions-summary");
      if (leftSlot !== null) {
        leftSlot.textContent = summaryText;
      }
    } else if (lastMsg !== undefined) {
      // Dedup: skip DOM insertion only (side-effects already fired above)
      const nextEl = lastMsg.nextElementSibling;
      if (!nextEl?.classList.contains("turn-summary")) {
        const summary = el(
          "div",
          {
            className: "turn-summary",
            role: "note",
            "aria-label": "Turn summary",
            "data-chat-entry": "",
          },
          summaryText,
        );
        lastMsg.insertAdjacentElement("afterend", summary);
      }
    }
  }

  // Render file-change summary banner if files were modified.
  const changedFiles = p.changed_files;
  if (changedFiles !== undefined && Object.keys(changedFiles).length > 0) {
    const entries = Object.values(changedFiles);
    const count = entries.length;
    let added = 0;
    let removed = 0;
    for (const f of entries) {
      added += f.lines_added;
      removed += f.lines_removed;
    }
    const parts: string[] = [`${String(count)} file${count > 1 ? "s" : ""} changed`];
    if (added > 0) {
      parts.push(`+${String(added)}`);
    }
    if (removed > 0) {
      parts.push(`-${String(removed)}`);
    }
    const banner = el(
      "div",
      { className: "turn-file-changes", role: "note", "data-chat-entry": "" },
      parts.join(" · "),
    );
    // Dedup: skip if a turn-file-changes already exists as the last entry
    const lastChild = msgsEl.lastElementChild;
    if (lastChild !== null && !lastChild.classList.contains("turn-file-changes")) {
      lastChild.insertAdjacentElement("afterend", banner);
    }
  }
});

onSSE("permission_needed", (chatID, p) => {
  if (isPermissionNeededEnabled()) {
    notifyAndBadge(NOTIFY_TITLE, `Permission needed: ${p.title ?? "Tool"}`);
  }
  // Mark the subagent row as having a pending approval.
  const subSid = p.sub_session_id;
  if (subSid !== undefined && subSid !== "") {
    setSubagentPendingApproval(subSid, true);
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
      // Clear the pending-approval indicator only after successful dispatch.
      void respondPermission
        .dispatch({
          chatID,
          requestID: p.request_id,
          optionID,
        })
        .then((result: unknown) => {
          if (result === null) {
            return;
          }
          if (subSid !== undefined && subSid !== "") {
            setSubagentPendingApproval(subSid, false);
          }
        });
    },
    p.sub_session_id,
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

onSSE("elicitation_complete", (chatID, p) => {
  if (chatID !== getActiveId()) {
    return;
  }
  dismissElicitation(p.request_id);
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
