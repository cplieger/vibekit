// ---------------------------------------------------------------------------
// SSE handlers for turn lifecycle + permission dialog + errors.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { setThinking, setWorkingLabel, get, getActiveId, dequeuePrompt, peekQueuedAttachments } from "../store.js";
import { apiGet } from "../api-client.js";
import {
  notifyIfHidden, setBadge, isAgentFinishedEnabled, isPermissionNeededEnabled,
} from "../notify.js";
import { showPermissionDialog } from "../messages.js";
import { sendPromptTo } from "../chat-commands.js";
import { setLastError, clearLastError } from "../send-state.js";
import { refreshGitBadge } from "../git.js";
import {
  showBanner, onTurnEnded,
} from "../banner-stack.js";
import { setSubagentPendingApproval } from "../crew-card.js";
import { permissionResponseAction, restoreCheckpointAction } from "../actions/chat.js";
import type { BannerLevel } from "../types.js";

/** Drain one queued prompt, restoring any attachments that were saved
 *  alongside it so they flow through the next sendPromptTo call. */
function drainQueuedPromptWithAttachments(chatID: string): void {
  const attachments = peekQueuedAttachments(chatID);
  const text = dequeuePrompt(chatID);
  if (text === undefined) return;
  const session = get(chatID);
  void sendPromptTo(chatID, text, {
    ...(session?.agent !== undefined && session.agent !== "" && { agent: session.agent }),
    ...(session?.model !== undefined && session.model !== "" && { model: session.model }),
    ...(attachments.length > 0 && { attachments: attachments as unknown[] }),
  });
}

/** Track last notification time per chat to avoid duplicate notifications
 *  on SSE reconnect replay (events arrive within milliseconds). */
const _lastNotifyMs = new Map<string, number>();
const _NOTIFY_STALE_MS = 10_000;

function _pruneNotifyMap(now: number): void {
  for (const [k, v] of _lastNotifyMs) {
    if (now - v > _NOTIFY_STALE_MS) _lastNotifyMs.delete(k);
  }
}

onSSE("working_label", (chatID, p) => {
  if (typeof p?.label !== "string") return;
  setWorkingLabel(chatID, p.label);
});

onSSE("turn_ended", (chatID, p) => {
  // --- Side-effects: fire unconditionally regardless of active chat or dedup ---
  setThinking(chatID, false);
  clearLastError();
  onTurnEnded(chatID);
  refreshGitBadge();
  drainQueuedPromptWithAttachments(chatID);

  const stopReason = p.stop_reason;
  if (stopReason !== "cancelled" && isAgentFinishedEnabled()) {
    // Dedup: skip notification if we already notified for this chat
    // within the last 2s (SSE reconnect replay fires duplicates in
    // rapid succession).
    const now = Date.now();
    const last = _lastNotifyMs.get(chatID) ?? 0;
    if (now - last > 2000) {
      _lastNotifyMs.set(chatID, now);
      _pruneNotifyMap(now);
      const s = get(chatID);
      const name = s?.name ?? "Chat";
      notifyIfHidden("Vibekit", `${name}: Agent finished`);
      if (document.visibilityState === "hidden") setBadge(1);
    }
  }

  // --- DOM rendering: only for the active chat ---
  if (chatID !== getActiveId()) return;

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

    const container = document.getElementById("messages");
    const msgs = container !== null
      ? container.querySelectorAll(".message.assistant")
      : [];
    const lastMsg = msgs[msgs.length - 1] as HTMLElement | undefined;
    const actionsRow = lastMsg?.nextElementSibling;
    if (actionsRow !== null
      && actionsRow !== undefined
      && actionsRow.classList.contains("turn-actions")) {
      const leftSlot = actionsRow.querySelector(".turn-actions-summary");
      if (leftSlot !== null) leftSlot.textContent = summaryText;
    } else if (lastMsg !== undefined) {
      // Dedup: skip DOM insertion only (side-effects already fired above)
      const nextEl = lastMsg.nextElementSibling;
      if (!(nextEl !== null && nextEl.classList.contains("turn-summary"))) {
        const summary = document.createElement("div");
        summary.className = "turn-summary";
        summary.setAttribute("role", "note");
        summary.setAttribute("aria-label", "Turn summary");
        summary.setAttribute("data-chat-entry", "");
        summary.textContent = summaryText;
        lastMsg.insertAdjacentElement("afterend", summary);
      }
    }
  }

  // Render file-change summary banner if files were modified.
  const changedFiles = p.changed_files;
  if (changedFiles !== undefined && Object.keys(changedFiles).length > 0) {
    const count = Object.keys(changedFiles).length;
    let added = 0;
    let removed = 0;
    for (const f of Object.values(changedFiles)) {
      added += f.lines_added;
      removed += f.lines_removed;
    }
    const parts: string[] = [`${String(count)} file${count > 1 ? "s" : ""} changed`];
    if (added > 0) parts.push(`+${String(added)}`);
    if (removed > 0) parts.push(`-${String(removed)}`);
    const banner = document.createElement("div");
    banner.className = "turn-file-changes";
    banner.setAttribute("role", "note");
    banner.setAttribute("data-chat-entry", "");
    banner.textContent = parts.join(" · ");
    const msgsEl = document.getElementById("messages");
    if (msgsEl !== null) {
      // Dedup: skip if a turn-file-changes already exists as the last entry
      const existing = msgsEl.lastElementChild;
      if (!(existing !== null && existing.classList.contains("turn-file-changes"))) {
        const lastChild = msgsEl.lastElementChild;
        if (lastChild !== null) {
          lastChild.insertAdjacentElement("afterend", banner);
        }
      }
    }
  }
});

onSSE("permission_needed", (chatID, p) => {
  if (isPermissionNeededEnabled()) {
    notifyIfHidden("Vibekit", `Permission needed: ${p.title ?? "Tool"}`);
    if (document.visibilityState === "hidden") setBadge(1);
  }
  // Mark the subagent row as having a pending approval.
  const subSid = p.sub_session_id;
  if (subSid !== undefined && subSid !== "") {
    setSubagentPendingApproval(subSid, true);
  }
  if (chatID !== getActiveId()) return;

  const toolCallInput = lookupToolInput(chatID, p.tool_call_id ?? "");
  showPermissionDialog(
    p.title ?? "Tool",
    p.tool_call_id ?? "",
    p.kind ?? "",
    toolCallInput,
    p.options,
    (optionID: string) => {
      // Clear the pending-approval indicator only after successful dispatch.
      void permissionResponseAction.dispatch({
        chatID,
        requestID: p.request_id,
        optionID,
      }).then((result) => {
        if (result === null) return;
        if (subSid !== undefined && subSid !== "") {
          setSubagentPendingApproval(subSid, false);
        }
      });
    },
    p.sub_session_id,
  );
});

function lookupToolInput(chatID: string, toolCallID: string): unknown {
  if (toolCallID === "") return undefined;
  const s = get(chatID);
  if (s === undefined) return undefined;
  for (let i = s.messages.length - 1; i >= 0; i--) {
    const m = s.messages[i]!;
    if (m.tool_calls === undefined) continue;
    for (const tc of m.tool_calls) {
      if (tc.id === toolCallID) return tc.input;
    }
  }
  return undefined;
}

// --- Data-driven error classification table ---



export interface ErrorRoute {
  surface: "banner" | "send-error";
  level: BannerLevel;
  dismissible: boolean;
}

export const ERROR_ROUTES: Readonly<Record<string, ErrorRoute>> = {
  agent_not_found:         { surface: "banner", level: "error",   dismissible: true },
  agent_config_error:      { surface: "banner", level: "error",   dismissible: false },
  model_not_found:         { surface: "banner", level: "warning", dismissible: true },
  rate_limit:              { surface: "banner", level: "warning", dismissible: true },
  compaction_failed:       { surface: "banner", level: "error",   dismissible: true },
  switch_failed:           { surface: "send-error", level: "error", dismissible: false },
  bridge_start_failed:     { surface: "send-error", level: "error", dismissible: false },
  prompt_failed:           { surface: "send-error", level: "error", dismissible: false },
};

onSSE("error", (chatID, p) => {
  // Unfreeze thinking so send-state can settle correctly.
  setThinking(chatID, false);

  const code = p.code ?? "";
  const msg = p.message ?? "";

  // Only surface errors for the active chat to avoid polluting the
  // send-button state with errors from background chats.
  if (chatID !== getActiveId()) return;

  const route = ERROR_ROUTES[code];
  if (route !== undefined && route.surface === "banner") {
    showBanner(chatID, code, msg, route.level, route.dismissible);
  } else if (route !== undefined && route.surface === "send-error") {
    setLastError(`${code}: ${msg}`);
  } else {
    // Unknown codes: fall through to send-button blocker.
    setLastError(msg !== "" ? `${code}: ${msg}` : code);
  }
});

// --- Checkpoint restore (event delegation on messages container) ---

/** Wire checkpoint restore buttons via event delegation. Called once at
 *  startup from app.ts. */
export function wireCheckpointRestore(messagesEl: HTMLElement): void {
  messagesEl.addEventListener("click", (e: MouseEvent) => {
    const btn = (e.target as HTMLElement).closest(".checkpoint-restore") as HTMLButtonElement | null;
    if (btn === null) return;
    const tag = btn.dataset["tag"];
    const chatID = getActiveId();
    if (tag === undefined || chatID === "") return;
    void confirmAndRestore(chatID, tag);
  });
}

async function confirmAndRestore(chatID: string, tag: string): Promise<void> {
  // Two-phase confirm: if the restore would touch any file with
  // unsaved edits in the editor, surface them BEFORE the generic
  // restore prompt. The check is advisory — the server always
  // proceeds; we just give the user a chance to cancel.
  const preview = await fetchRestorePreview(chatID, tag);
  const dirty = await intersectDirty(preview);
  if (dirty.length > 0) {
    const sample = dirty.slice(0, 3).join(", ");
    const more = dirty.length > 3 ? ` (+${String(dirty.length - 3)} more)` : "";
    const ok = await confirmDestructive(
      `Restore would overwrite unsaved edits in ${sample}${more}. Continue anyway?`,
      "Discard and restore",
    );
    if (!ok) return;
  } else if (!(await confirmRestore())) {
    return;
  }
  void restoreCheckpointAction.dispatch({ chatID, tag });
}

async function fetchRestorePreview(chatID: string, tag: string): Promise<string[]> {
  // Best-effort: network failure or a server without preview
  // support (older build) falls through to the normal confirm
  // dialog so restores never get wedged by the advisory step.
  const url = `/api/checkpoints/${encodeURIComponent(chatID)}/restore-preview?tag=${encodeURIComponent(tag)}`;
  const resp = await apiGet<{ files?: string[] }>(url);
  return resp?.files ?? [];
}

async function intersectDirty(preview: string[]): Promise<string[]> {
  if (preview.length === 0) return [];
  const { getDirtyEditorPaths } = await import("../editor-core.js");
  const dirty = new Set(getDirtyEditorPaths());
  return preview.filter((p) => dirty.has(p));
}

async function confirmRestore(): Promise<boolean> {
  const { confirm: confirmDialog } = await import("../confirm.js");
  return confirmDialog("Restore to this checkpoint? Current file changes will be reverted.", "Restore", "destructive");
}

async function confirmDestructive(msg: string, btn: string): Promise<boolean> {
  const { confirm: confirmDialog } = await import("../confirm.js");
  return confirmDialog(msg, btn, "destructive");
}
