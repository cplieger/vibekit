// Actions for chat lifecycle: delete, archive, restore, discard tangent,
// load history, delete archived, cancel, switch model, send prompt, resolve
// pending change, resolve all pending, permission response, restore
// checkpoint, fork, merge tangent, set supervised, set auto-approve crew,
// trust pending, clear pending trust.
// ---------------------------------------------------------------------------

import { apiAction, transportAction, defineAction, ActionError } from "./index.js";
import { get, setThinking, setSupervisedMode, setAutoApproveCrew, enqueuePrompt } from "../store.js";
import { send as transportSend } from "../transport.js";

// --- chat.delete ---

export const deleteChatAction = transportAction<string>({
  name: "chat.delete",
  command: (id) => ({ type: "delete_chat", chat_id: id }),
  // No optimistic: SSE chat_deleted is the canonical removal path.
  // A rollback would require re-inserting into _sessions which isn't exposed.
  error: "Couldn't delete chat",
});

// --- chat.archive ---

export const archiveChatAction = apiAction<string, unknown>({
  name: "chat.archive",
  request: (id) => ({
    method: "POST",
    path: `/api/chats/${encodeURIComponent(id)}/archive`,
  }),
  success: false,
  error: "Couldn't archive chat",
});

// --- chat.discard_tangent ---

export const discardTangentAction = transportAction<string>({
  name: "chat.discard_tangent",
  command: (id) => ({ type: "discard_tangent", chat_id: id }),
  // Caller (chat.ts onClose) fires a richer manual toast on failure.
  error: false,
});

// --- chat.set_supervised ---

export const setSupervisedAction = transportAction<{ chatID: string; enabled: boolean }>({
  name: "chat.set_supervised",
  command: ({ chatID, enabled }) => ({
    type: "set_supervised_mode",
    chat_id: chatID,
    payload: { enabled },
  }),
  optimistic: ({ chatID, enabled }) => {
    const session = get(chatID);
    if (session === undefined) return undefined;
    const prev = session.supervised_mode;
    setSupervisedMode(chatID, enabled);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined && op !== null && typeof op === "object" && "prev" in op) {
      setSupervisedMode(chatID, (op as { prev: boolean }).prev);
    }
  },
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
  optimistic: ({ chatID, enabled }) => {
    const session = get(chatID);
    if (session === undefined) return undefined;
    const prev = session.auto_approve_crew;
    setAutoApproveCrew(chatID, enabled);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined && op !== null && typeof op === "object" && "prev" in op) {
      setAutoApproveCrew(chatID, (op as { prev: boolean }).prev);
    }
  },
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

export const loadHistoryAction = apiAction<void, { chats: Array<{ id: string; name: string; summary?: string; updated_at: number }> }>({
  name: "chat.load_history",
  request: () => ({ method: "GET", path: "/api/chats/archived" }),
  error: "Couldn't load chat history",
});

// --- chat.cancel ---

export const cancelTurnAction = transportAction<string>({
  name: "chat.cancel",
  command: (chatID) => ({ type: "cancel", chat_id: chatID }),
  error: "Couldn't cancel",
});

// --- chat.switch_model ---
// Uses defineAction because: (1) caller needs boolean return, (2) setThinking
// is a loading indicator (set on start, cleared on completion), not an optimistic
// mutation. transportAction's optimistic/rollback pattern doesn't fit this lifecycle.

export const switchModelAction = defineAction<{ chatID: string; model: string }, boolean>({
  name: "chat.switch_model",
  run: async ({ chatID, model }, signal) => {
    setThinking(chatID, true);
    try {
      const r = await transportSend(
        { type: "switch_model", chat_id: chatID, payload: { model } },
        { signal, reportSendState: false },
      );
      if (!r.ok) {
        throw new ActionError(r.error ?? `send failed (${String(r.status)})`, { status: r.status });
      }
      return true;
    } finally {
      setThinking(chatID, false);
    }
  },
  error: "Couldn't switch model",
});

// --- chat.send_prompt ---
//
// The most-used user mutation in the app. Posts a prompt to a chat
// with the shared thinking + 409-queue lifecycle. Returns "sent" on
// 2xx, "queued" on 409 (the prompt drains when the in-flight turn
// ends), or null (= caller's "failed") on any other error.
//
// `error: false`: the send-state.ts blocked-button is the canonical
// error surface for prompt sends specifically. transport.send
// reportSendState defaults to true here so setLastError fires; we
// don't want a toast on top.

export interface SendPromptArgs {
  chatID: string;
  text: string;
  messageID: string;
  agent: string;
  model: string;
  activeFile: string;
  openFiles: readonly string[];
  attachments?: readonly unknown[];
}

export const sendPromptAction = defineAction<SendPromptArgs, "sent" | "queued">({
  name: "chat.send_prompt",
  optimistic: ({ chatID }) => {
    setThinking(chatID, true);
    return { chatID };
  },
  rollback: (_args, op) => {
    const o = op as { chatID: string };
    setThinking(o.chatID, false);
  },
  run: async (args, signal) => {
    const { chatID, text, messageID, agent, model, activeFile, openFiles, attachments } = args;
    const r = await transportSend(
      {
        type: "prompt", chat_id: chatID,
        payload: {
          text, message_id: messageID, agent, model,
          active_file: activeFile,
          open_files: openFiles as string[],
          attachments: (attachments !== undefined && attachments.length > 0)
            ? (attachments as unknown[]) : undefined,
        },
      },
      { signal, reportSendState: true },  // send-state IS the error surface
    );
    if (r.ok) return "sent";
    if (r.status === 409) {
      // Server says "in-flight turn"; queue the text and report queued.
      // The queue drains via SSE turn_ended. enqueuePrompt runs in
      // run() (not optimistic) because it should only happen if the
      // server actually 409'd, not on cancel.
      enqueuePrompt(chatID, text);
      return "queued";
    }
    throw new ActionError(r.error ?? "send failed", { status: r.status });
  },
  error: false,  // send-state.ts (blocked send button) is the surface
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
