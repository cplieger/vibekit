// Actions for chat lifecycle: delete, archive, restore, discard tangent,
// load history, delete archived, cancel, switch model, resolve pending
// change, permission response, restore checkpoint.
// ---------------------------------------------------------------------------

import { apiAction, transportAction, defineAction, ActionError } from "./index.js";
import { get, setThinking } from "../store.js";
import { send as transportSend } from "../transport.js";

// --- chat.delete ---

export const deleteChatAction = transportAction<string>({
  name: "chat.delete",
  command: (id) => ({ type: "delete_chat", chat_id: id }),
  optimistic: (id) => {
    const session = get(id);
    return session !== undefined ? { ...session } : undefined;
  },
  rollback: (_id, op) => {
    // Rollback not feasible without re-inserting into store; the SSE
    // chat_deleted handler is the canonical removal path. Since
    // transport failures are rare and the server echoes back, we
    // accept the race. A full rollback would require re-adding to
    // _sessions which isn't exposed. Log only.
  },
  error: "Couldn't delete chat",
});

// --- chat.archive ---

export const archiveChatAction = apiAction<string, unknown>({
  name: "chat.archive",
  request: (id) => ({
    method: "POST",
    path: `/api/chats/${encodeURIComponent(id)}/archive`,
  }),
  // Silent: auto-triggered on tab close, not user-initiated feedback.
  success: false,
  error: false,
});

// --- chat.discard_tangent ---

export const discardTangentAction = transportAction<string>({
  name: "chat.discard_tangent",
  command: (id) => ({ type: "discard_tangent", chat_id: id }),
  error: "Couldn't discard tangent",
});

// --- chat.set_supervised ---

export const setSupervisedAction = transportAction<{ chatID: string; enabled: boolean }>({
  name: "chat.set_supervised",
  command: ({ chatID, enabled }) => ({
    type: "set_supervised_mode",
    chat_id: chatID,
    payload: { enabled },
  }),
  error: "Couldn't update supervised mode",
});

// --- chat.resolve_all_pending ---

export const resolveAllPendingAction = transportAction<{ chatID: string; action: "accept" | "reject" }>({
  name: "chat.resolve_all_pending",
  command: ({ chatID, action }) => ({
    type: "resolve_all_pending_changes",
    chat_id: chatID,
    payload: { action },
  }),
  error: "Couldn't resolve pending changes",
});

// --- chat.trust_pending ---

export const trustPendingAction = transportAction<string>({
  name: "chat.trust_pending",
  command: (chatID) => ({ type: "trust_pending_changes", chat_id: chatID }),
  error: "Couldn't trust pending changes",
});

// --- chat.clear_pending_trust ---

export const clearPendingTrustAction = transportAction<string>({
  name: "chat.clear_pending_trust",
  command: (chatID) => ({ type: "clear_pending_trust", chat_id: chatID }),
  error: "Couldn't clear pending trust",
});

// --- chat.fork ---

export const forkChatAction = transportAction<{ chatID: string; tangentID: string }>({
  name: "chat.fork",
  command: ({ chatID, tangentID }) => ({
    type: "fork_chat",
    chat_id: chatID,
    payload: { tangent_id: tangentID },
  }),
  error: "Couldn't fork chat",
});

// --- chat.merge_tangent ---

export const mergeTangentAction = transportAction<string>({
  name: "chat.merge_tangent",
  command: (chatID) => ({ type: "merge_tangent", chat_id: chatID }),
  error: "Couldn't merge tangent",
});

// --- chat.set_auto_approve_crew ---

export const setAutoApproveCrewAction = transportAction<{ chatID: string; enabled: boolean }>({
  name: "chat.set_auto_approve_crew",
  command: ({ chatID, enabled }) => ({
    type: "set_auto_approve_crew",
    chat_id: chatID,
    payload: { enabled },
  }),
  error: "Couldn't update auto-approve",
});

// --- chat.restore ---

export const restoreChatAction = apiAction<string, { ok: boolean }>({
  name: "chat.restore",
  request: (id) => ({
    method: "POST",
    path: "/api/chats/archived",
    body: { id },
  }),
  error: "Couldn't restore chat",
});

// --- chat.delete_archived ---

export const deleteArchivedChatAction = apiAction<string, unknown>({
  name: "chat.delete_archived",
  request: (id) => ({
    method: "DELETE",
    path: `/api/chats/archived/${encodeURIComponent(id)}`,
  }),
  error: "Couldn't delete chat",
});

// --- chat.load_history ---

export const loadHistoryAction = defineAction<void, { chats: Array<{ id: string; name: string; summary?: string; updated_at: number }> }>({
  name: "chat.load_history",
  run: async (_args, signal) => {
    const r = await fetch("/api/chats/archived", { signal });
    if (!r.ok) {
      let serverError = "";
      try {
        const body = (await r.json()) as { error?: string };
        if (typeof body.error === "string") serverError = body.error;
      } catch { /* ignore */ }
      throw new ActionError(serverError || `HTTP ${String(r.status)}`, { status: r.status });
    }
    return (await r.json()) as { chats: Array<{ id: string; name: string; summary?: string; updated_at: number }> };
  },
  error: "Couldn't load chat history",
});

// --- chat.cancel ---

export const cancelTurnAction = transportAction<string>({
  name: "chat.cancel",
  command: (chatID) => ({ type: "cancel", chat_id: chatID }),
  error: "Couldn't cancel",
});

// --- chat.switch_model ---

export const switchModelAction = defineAction<{ chatID: string; model: string }, boolean>({
  name: "chat.switch_model",
  run: async ({ chatID, model }, signal) => {
    if (chatID === "") return false;
    setThinking(chatID, true);
    const r = await transportSend({ type: "switch_model", chat_id: chatID, payload: { model } }, { signal });
    if (r.status !== 409) setThinking(chatID, false);
    if (!r.ok && r.status !== 409) {
      throw new ActionError(r.error ?? `send failed (${String(r.status)})`, { status: r.status });
    }
    return true;
  },
  error: "Couldn't switch model",
});

// --- chat.resolve_pending_change ---

export const resolvePendingChangeAction = transportAction<{ chatID: string; toolCallID: string; action: "accept" | "reject" }>({
  name: "chat.resolve_pending_change",
  command: ({ chatID, toolCallID, action }) => ({
    type: "resolve_pending_change",
    chat_id: chatID,
    payload: { tool_call_id: toolCallID, action },
  }),
  error: "Couldn't resolve change",
});

// --- chat.permission_response ---

export const permissionResponseAction = transportAction<{ chatID: string; requestID: number; optionID: string }>({
  name: "chat.permission_response",
  command: ({ chatID, requestID, optionID }) => ({
    type: "permission_response",
    chat_id: chatID,
    payload: { request_id: requestID, option_id: optionID },
  }),
  error: "Couldn't send permission response",
});

// --- chat.restore_checkpoint ---

export const restoreCheckpointAction = transportAction<{ chatID: string; tag: string }>({
  name: "chat.restore_checkpoint",
  command: ({ chatID, tag }) => ({
    type: "restore_checkpoint",
    chat_id: chatID,
    payload: { tag },
  }),
  error: "Couldn't restore checkpoint",
});
