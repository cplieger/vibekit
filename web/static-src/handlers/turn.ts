// ---------------------------------------------------------------------------
// SSE handlers for turn lifecycle + permission dialog + errors.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { setThinking, setWorkingLabel, get, getActiveId } from "../store.js";
import { apiGet } from "../api-client.js";
import {
  notifyIfHidden, setBadge, isAgentFinishedEnabled, isPermissionNeededEnabled,
} from "../notify.js";
import { showPermissionDialog } from "../messages.js";
import { drainQueuedPrompt } from "../chat.js";
import { setLastError, clearLastError } from "../send-state.js";
import { refreshGitBadge } from "../git.js";
import {
  showBanner, onTurnEnded,
} from "../banner-stack.js";
import { setSubagentPendingApproval } from "../crew-card.js";
import { permissionResponseAction, restoreCheckpointAction } from "../actions/chat.js";
import type { BannerLevel } from "../types.js";

onSSE("working_label", (chatID, p) => {
  setWorkingLabel(chatID, p.label);
});

onSSE("turn_ended", (chatID, p) => {
  setThinking(chatID, false);
  clearLastError();
  onTurnEnded(chatID);

  // Refresh the git badge so file changes the agent made during the
  // turn surface immediately. Uses the &quick=1 status endpoint to
  // avoid a full ahead/behind network round-trip.
  refreshGitBadge();

  // Render turn summary (credits + elapsed time) into the existing
  // turn-actions row's left slot if the assistant message already got
  // its copy/export buttons. Otherwise append a standalone summary
  // below the last assistant message. This keeps a single row of
  // chrome under every finalized turn instead of stacking two.
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
        const s = Math.round((elapsed % 60000) / 1000);
        parts.push(`${String(m)}m ${String(s)}s`);
      } else {
        parts.push(`${(elapsed / 1000).toFixed(1)}s`);
      }
    }
    const summaryText = parts.join(" · ");

    // Prefer the existing turn-actions row (thin line below the
    // bubble, same style as checkpoint-line). messages.ts renders
    // this row on every `message_updated` that finalises the
    // assistant message.
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
      // Dedup: skip if a turn-summary already follows this message
      const nextEl = lastMsg.nextElementSibling;
      if (nextEl !== null && nextEl.classList.contains("turn-summary")) return;
      // Fallback (pre-rename path or turn with no rendered actions).
      const summary = document.createElement("div");
      summary.className = "turn-summary";
      summary.setAttribute("role", "note");
      summary.setAttribute("aria-label", "Turn summary");
      summary.setAttribute("data-chat-entry", "");
      summary.textContent = summaryText;
      lastMsg.insertAdjacentElement("afterend", summary);
    }
  }

  const stopReason = p.stop_reason;

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
    // Insert after the turn summary (or after the last assistant message).
    const msgs = document.getElementById("messages");
    if (msgs !== null) {
      // Dedup: skip if a turn-file-changes already exists as the last entry
      const existing = msgs.lastElementChild;
      if (existing !== null && existing.classList.contains("turn-file-changes")) return;
      const lastChild = msgs.lastElementChild;
      if (lastChild !== null) {
        lastChild.insertAdjacentElement("afterend", banner);
      }
    }
  }

  if (stopReason !== "cancelled" && isAgentFinishedEnabled()) {
    const s = get(chatID);
    const name = s?.name ?? "Chat";
    notifyIfHidden("Vibekit", `${name}: Agent finished`);
    if (document.visibilityState === "hidden") setBadge(1);
  }
  drainQueuedPrompt(chatID);
});

onSSE("permission_needed", (chatID, p) => {
  if (p === undefined) return;
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
      // Clear the pending-approval indicator on the crew row.
      if (subSid !== undefined && subSid !== "") {
        setSubagentPendingApproval(subSid, false);
      }
      void permissionResponseAction.dispatch({
        chatID,
        requestID: p.request_id,
        optionID,
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
  mcp_server_init_failure: { surface: "banner", level: "warning", dismissible: true },
  rate_limit:              { surface: "banner", level: "warning", dismissible: true },
  compaction_failed:       { surface: "banner", level: "error",   dismissible: true },
  switch_failed:           { surface: "send-error", level: "error", dismissible: false },
  bridge_start_failed:     { surface: "send-error", level: "error", dismissible: false },
  prompt_failed:           { surface: "send-error", level: "error", dismissible: false },
};

onSSE("error", (chatID, p) => {
  // Unfreeze thinking so send-state can settle correctly.
  setThinking(chatID, false);
  if (p === undefined) return;

  const code = p.code ?? "";
  const msg = p.message ?? "";

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
