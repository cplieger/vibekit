// Actions for chat lifecycle: delete, archive, restore, discard tangent,
// load history, delete archived, cancel, switch model, send prompt, resolve
// pending change, resolve all pending, permission response, restore checkpoint,
// fork, merge tangent, set supervised, set auto-approve crew, trust pending,
// clear pending trust.
// ---------------------------------------------------------------------------

import { apiAction, transportAction, defineAction, ActionError } from "./index.js";
import { asOp } from "./op.js";
import { get, setThinking, setSupervisedMode, setAutoApproveCrew, enqueuePrompt, removeChat, reinsertSession, indexOfSession, setFrozen, setModel } from "../store.js";
import { send as transportSend } from "../transport.js";

// --- chat.delete ---

export const deleteChatAction = transportAction<string>({
  name: "chat.delete",
  scope: (id) => `chat:${id}`,
  retryable: false,
  command: (id) => ({ type: "delete_chat", chat_id: id }),
  optimistic: (id) => {
    const session = get(id);
    if (session === undefined) return undefined;
    const atIndex = indexOfSession(id);
    removeChat(id);
    return { session, atIndex };
  },
  // Trade-off: if the server actually deleted but the HTTP response timed out,
  // rollback reinserts a ghost session. A subsequent SSE chat_deleted event will
  // remove it, causing a brief flicker. Full correctness would require server-side
  // dedup + ack; the user-visible glitch is negligible so we accept it.
  rollback: (_id, op) => {
    const o = asOp<{ session: import("../types.js").Session; atIndex: number }>(op);
    if (o !== undefined) reinsertSession(o.session, o.atIndex);
  },
  error: "Couldn't delete chat",
});

// --- chat.archive ---

export const archiveChatAction = apiAction<string, unknown>({
  name: "chat.archive",
  scope: (id) => `chat:${id}`,
  retryable: "network",
  request: (id) => ({
    method: "POST",
    path: `/api/chats/${encodeURIComponent(id)}/archive`,
  }),
  optimistic: (id) => {
    const session = get(id);
    if (session === undefined) return undefined;
    const atIndex = indexOfSession(id);
    removeChat(id);
    return { session, atIndex };
  },
  rollback: (_id, op) => {
    const o = asOp<{ session: import("../types.js").Session; atIndex: number }>(op);
    if (o !== undefined) reinsertSession(o.session, o.atIndex);
  },
  success: false,
  error: "Couldn't archive chat",
});

// --- chat.discard_tangent ---

export const discardTangentAction = transportAction<string>({
  name: "chat.discard_tangent",
  scope: (id) => `chat:${id}`,
  retryable: false,
  command: (id) => ({ type: "discard_tangent", chat_id: id }),
  optimistic: (id) => {
    const session = get(id);
    if (session === undefined) return undefined;
    const atIndex = indexOfSession(id);
    const parentID = session.parent_chat_id;
    removeChat(id);
    if (parentID !== undefined) setFrozen(parentID, false);
    return { session, atIndex, parentID };
  },
  rollback: (_id, op) => {
    const o = asOp<{ session: import("../types.js").Session; atIndex: number; parentID?: string }>(op);
    if (o !== undefined) {
      reinsertSession(o.session, o.atIndex);
      if (o.parentID !== undefined) setFrozen(o.parentID, true);
    }
  },
  // Caller (chat.ts onClose) fires a richer manual toast on failure.
  error: false,
});

// --- chat.set_supervised ---

export const setSupervisedAction = transportAction<{ chatID: string; enabled: boolean }>({
  name: "chat.set_supervised",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, enabled }) => ({
    type: "set_supervised_mode",
    chat_id: chatID,
    payload: { enabled },
  }),
  optimistic: ({ chatID, enabled }) => {
    const session = get(chatID);
    if (session === undefined) return undefined;
    const prev: boolean = session.supervised_mode ?? false;
    setSupervisedMode(chatID, enabled);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    const o = asOp<{ prev: boolean }>(op);
    if (o !== undefined) setSupervisedMode(chatID, o.prev);
  },
  retryable: "network",
  retry: { count: 2, delay: 300 },
  error: "Couldn't update supervised mode",
});


// --- chat.resolve_all_pending ---

export const resolveAllPendingAction = transportAction<{ chatID: string; action: "accept" | "reject" }>({
  name: "chat.resolve_all_pending",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, action }) => ({
    type: "resolve_all_pending_changes",
    chat_id: chatID,
    payload: { action },
  }),
  retryable: "network",
  error: "Couldn't resolve pending changes",
});

// --- chat.trust_pending ---

export const trustPendingAction = transportAction<string>({
  name: "chat.trust_pending",
  scope: (chatID) => `chat:${chatID}`,
  command: (chatID) => ({ type: "trust_pending_changes", chat_id: chatID }),
  retryable: "network",
  error: "Couldn't trust pending changes",
});

// --- chat.clear_pending_trust ---

export const clearPendingTrustAction = transportAction<string>({
  name: "chat.clear_pending_trust",
  scope: (chatID) => `chat:${chatID}`,
  command: (chatID) => ({ type: "clear_pending_trust", chat_id: chatID }),
  retryable: "network",
  error: "Couldn't clear pending trust",
});

// --- chat.fork ---

export const forkChatAction = transportAction<{ chatID: string; tangentID: string }>({
  name: "chat.fork",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  command: ({ chatID, tangentID }) => ({
    type: "fork_chat",
    chat_id: chatID,
    payload: { tangent_id: tangentID },
  }),
  optimistic: ({ chatID }) => {
    const session = get(chatID);
    const wasFrozen = session?.frozen ?? false;
    setFrozen(chatID, true);
    return { chatID, wasFrozen };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined && op !== null && typeof op === "object" && "wasFrozen" in op) {
      const { wasFrozen } = op as { wasFrozen: boolean };
      setFrozen(chatID, wasFrozen);
    }
  },
  retryable: "network",
  error: "Couldn't fork chat",
});

// --- chat.merge_tangent ---
// Not retryable: merge deletes the tangent server-side. If the first
// attempt succeeded but the response timed out, a retry would hit 404
// (tangent already gone). Showing a Retry button would mislead the user
// into thinking the merge failed when it actually succeeded. The SSE
// stream will confirm the merge via a tangent_merged event.

export const mergeTangentAction = transportAction<string>({
  name: "chat.merge_tangent",
  scope: (chatID) => `chat:${chatID}`,
  command: (chatID) => ({ type: "merge_tangent", chat_id: chatID }),
  retryable: false,
  error: "Couldn't merge tangent",
});

// --- chat.set_auto_approve_crew ---

export const setAutoApproveCrewAction = transportAction<{ chatID: string; enabled: boolean }>({
  name: "chat.set_auto_approve_crew",
  scope: ({ chatID }) => `chat:${chatID}`,
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
    const o = asOp<{ prev: boolean }>(op);
    if (o !== undefined) setAutoApproveCrew(chatID, o.prev);
  },
  retryable: "network",
  retry: { count: 2, delay: 300 },
  error: "Couldn't update auto-approve",
});

// --- chat.restore ---

export const restoreChatAction = apiAction<string, { ok: boolean }>({
  name: "chat.restore",
  scope: (id) => `chat:${id}`,
  retryable: "network",
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
  scope: (id) => `chat:${id}`,
  retryable: "network",
  request: (id) => ({
    method: "DELETE",
    path: `/api/chats/archived/${encodeURIComponent(id)}`,
  }),
  error: "Couldn't delete chat",
});

// --- chat.load_history ---

export const loadHistoryAction = apiAction<void, { chats: Array<{ id: string; name: string; summary?: string; updated_at: number }> }>({
  name: "chat.load_history",
  dedupe: true,
  retryable: "network",
  retry: { count: 2, delay: 300 },
  request: () => ({ method: "GET", path: "/api/chats/archived" }),
  error: "Couldn't load chat history",
});

// --- chat.cancel ---
//
// No scope: cancel must fire immediately, not queue behind an in-flight
// sendPromptAction in the same chat. Cancel is naturally idempotent
// server-side (the server ignores it if no turn is active).

export const cancelTurnAction = transportAction<string>({
  name: "chat.cancel",
  command: (chatID) => ({ type: "cancel", chat_id: chatID }),
  error: "Couldn't cancel",
});

// --- chat.switch_model ---
// Uses defineAction because: (1) caller needs boolean return, (2) setThinking
// is a loading indicator (set on start, cleared on completion), not an optimistic
// mutation. transportAction's optimistic/rollback pattern doesn't fit this lifecycle.
//
// On failure, rollback restores the previous model via setModel(). bindLoadingState handles the spinner.

export const switchModelAction = defineAction<{ chatID: string; model: string }, boolean>({
  name: "chat.switch_model",
  scope: ({ chatID }) => `chat:${chatID}`,
  retryable: "network",
  optimistic: ({ chatID, model }) => {
    const session = get(chatID);
    if (session === undefined) return undefined;
    const prev = session.model;
    setModel(chatID, model);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    const o = asOp<{ prev: string }>(op);
    if (o !== undefined) setModel(chatID, o.prev);
  },
  run: async ({ chatID, model }, signal) => {
    // Don't touch thinking state — it's owned by sendPromptAction and
    // bindLoadingState on the model switcher button handles the UI indicator.
    const r = await transportSend(
      { type: "switch_model", chat_id: chatID, payload: { model } },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      throw new ActionError(r.error ?? `send failed (${String(r.status)})`, { status: r.status });
    }
    return true;
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
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  optimistic: ({ chatID }) => {
    setThinking(chatID, true);
    return { chatID };
  },
  rollback: (_args, op) => {
    const o = asOp<{ chatID: string }>(op);
    if (o !== undefined) setThinking(o.chatID, false);
  },
  run: async (args, signal, ctx) => {
    const { chatID, text, messageID, agent, model, activeFile, openFiles, attachments } = args;
    const r = await transportSend(
      {
        type: "prompt", chat_id: chatID,
        payload: {
          text, message_id: messageID, agent, model,
          active_file: activeFile,
          open_files: openFiles,
          attachments: (attachments !== undefined && attachments.length > 0)
            ? attachments : undefined,
          // Only include the key if the framework provided one (avoids
          // sending `idempotency_key: undefined` which serializes inconsistently).
          ...(ctx?.idempotencyKey !== undefined ? { idempotency_key: ctx.idempotencyKey } : {}),
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
      //
      // Limitation: if the SSE connection drops after queuing, the
      // thinking state persists indefinitely. A staleness timer or
      // SSE-reconnect hook that clears thinking after 60s of inactivity
      // would fix this, but is not yet implemented.
      enqueuePrompt(chatID, text, attachments);
      return "queued";
    }
    throw new ActionError(r.error ?? "send failed", { status: r.status });
  },
  error: false,  // send-state.ts (blocked send button) is the surface
});

// --- chat.resolve_pending_change ---

export const resolvePendingChangeAction = transportAction<{ chatID: string; toolCallID: string; action: "accept" | "reject" }>({
  name: "chat.resolve_pending_change",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, toolCallID, action }) => ({
    type: "resolve_pending_change",
    chat_id: chatID,
    payload: { tool_call_id: toolCallID, action },
  }),
  retryable: "network",
  retry: { count: 2, delay: 300 },
  error: "Couldn't resolve change",
});

// --- chat.permission_response ---
// Scope is per-request (not per-chat): two pending permission requests in
// the same chat are independent and should fire in parallel. Serializing
// them behind the chat scope would delay the second response until the
// first round-trips, which feels sluggish when the agent is waiting on
// multiple permissions simultaneously.

export const permissionResponseAction = transportAction<{ chatID: string; requestID: number; optionID: string }>({
  name: "chat.permission_response",
  scope: ({ chatID, requestID }) => `perm:${chatID}:${String(requestID)}`,
  retryable: "network",
  retry: { count: 2, delay: 300 },
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
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, tag }) => ({
    type: "restore_checkpoint",
    chat_id: chatID,
    payload: { tag },
  }),
  retryable: "network",
  error: "Couldn't restore checkpoint",
});
