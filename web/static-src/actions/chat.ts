// Actions for chat lifecycle: delete, archive, restore, discard tangent,
// load history, delete archived, cancel, switch model, send prompt, resolve
// pending change, resolve all pending, permission response, restore checkpoint,
// fork, merge tangent, set supervised, set auto-approve crew, trust pending,
// clear pending trust.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError, retryNetwork } from "./index.js";
import { RETRY_STANDARD } from "./types.js";
import { transportAction } from "./transport.js";
import { get, setThinking, setSupervisedMode, setAutoApproveCrew as storeSetAutoApproveCrew, enqueuePrompt, removeChat, reinsertSession, indexOfSession, setFrozen, setModel } from "../store.js";
import { send as transportSend } from "../transport.js";

// --- chat.delete ---

export const deleteChat = transportAction<string, { session: import("../types.js").Session; atIndex: number }>({
  name: "chat.delete",
  networkMode: "always",
  scope: (id) => `chat:${id}`,
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
    if (op !== undefined) reinsertSession(op.session, op.atIndex);
  },
  error: "Couldn't delete chat",
});

// --- chat.archive ---

export const archiveChat = apiAction<string, unknown, { session: import("../types.js").Session; atIndex: number }>({
  name: "chat.archive",
  scope: (id) => `chat:${id}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
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
    if (op !== undefined) reinsertSession(op.session, op.atIndex);
  },
  success: false,
  error: "Couldn't archive chat",
});

// --- chat.discard_tangent ---

export const discardTangent = transportAction<string, { session: import("../types.js").Session; atIndex: number; parentID: string | undefined }>({
  name: "chat.discard_tangent",
  scope: (id) => `chat:${id}`,
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
    if (op !== undefined) {
      reinsertSession(op.session, op.atIndex);
      if (op.parentID !== undefined) setFrozen(op.parentID, true);
    }
  },
  // Caller (chat.ts onClose) fires a richer manual toast on failure.
  error: false,
});

// --- chat.set_supervised ---

export const setSupervised = transportAction<{ chatID: string; enabled: boolean }, { prev: boolean }>({
  name: "chat.set_supervised",
  networkMode: "always",
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
    if (op !== undefined) setSupervisedMode(chatID, op.prev);
  },
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't update supervised mode",
});


// --- chat.resolve_all_pending ---

export const resolveAllPending = transportAction<{ chatID: string; action: "accept" | "reject" }>({
  name: "chat.resolve_all_pending",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  command: ({ chatID, action }) => ({
    type: "resolve_all_pending_changes",
    chat_id: chatID,
    payload: { action },
  }),
  error: "Couldn't resolve pending changes",
});

// --- chat.trust_pending ---

export const trustPending = transportAction<string>({
  name: "chat.trust_pending",
  networkMode: "always",
  scope: (chatID) => `chat:${chatID}`,
  command: (chatID) => ({ type: "trust_pending_changes", chat_id: chatID }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't trust pending changes",
});

// --- chat.clear_pending_trust ---

export const clearPendingTrust = transportAction<string>({
  name: "chat.clear_pending_trust",
  scope: (chatID) => `chat:${chatID}`,
  command: (chatID) => ({ type: "clear_pending_trust", chat_id: chatID }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't clear pending trust",
});

// --- chat.fork ---

export const forkChat = transportAction<{ chatID: string; tangentID: string }, { chatID: string; wasFrozen: boolean }>({
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
    if (op !== undefined) setFrozen(chatID, op.wasFrozen);
  },
  retryable: retryNetwork,
  error: "Couldn't fork chat",
});

// --- chat.merge_tangent ---
// Not retryable: merge deletes the tangent server-side. If the first
// attempt succeeded but the response timed out, a retry would hit 404
// (tangent already gone). Showing a Retry button would mislead the user
// into thinking the merge failed when it actually succeeded. The tangent
// deletion broadcasts SSE chat_deleted so listeners reconcile state.

export const mergeTangent = transportAction<string>({
  name: "chat.merge_tangent",
  scope: (chatID) => `chat:${chatID}`,
  command: (chatID) => ({ type: "merge_tangent", chat_id: chatID }),
  networkMode: "always",
  error: "Couldn't merge tangent",
});

// --- chat.set_auto_approve_crew ---

export const setAutoApproveCrew = transportAction<{ chatID: string; enabled: boolean }, { prev: boolean }>({
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
    storeSetAutoApproveCrew(chatID, enabled);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) storeSetAutoApproveCrew(chatID, op.prev);
  },
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't update auto-approve",
});

// --- chat.restore ---

export const restoreChat = apiAction<string, { ok: boolean }>({
  name: "chat.restore",
  scope: (id) => `chat:${id}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: (id) => ({
    method: "POST",
    path: "/api/chats/archived",
    body: { id },
  }),
  error: "Couldn't restore chat",
});

// --- chat.delete_archived ---
// No auto-retry and no manual Retry button: DELETE is destructive. If the
// first attempt succeeds but the response times out, a retry would hit 404
// and surface a misleading error toast.

export const deleteArchivedChat = apiAction<string, unknown>({
  name: "chat.delete_archived",
  scope: (id) => `chat:${id}`,
  request: (id) => ({
    method: "DELETE",
    path: `/api/chats/archived/${encodeURIComponent(id)}`,
  }),
  error: "Couldn't delete chat",
});

// --- chat.load_history ---

export const loadHistory = apiAction<void, { chats: Array<{ id: string; name: string; summary?: string; updated_at: number }> }>({
  name: "chat.load_history",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/chats/archived" }),
  error: "Couldn't load chat history",
});

// --- chat.cancel_turn ---
//
// No scope: cancel must fire immediately, not queue behind an in-flight
// sendPrompt in the same chat. Cancel is naturally idempotent
// server-side (the server ignores it if no turn is active).
//
// Named "chat.cancel_turn" (not "chat.cancel") to avoid confusion with
// the Action.cancel() method — `cancelTurn.cancel()` reads as
// "abort the cancel-turn action's in-flight instances", not "cancel a turn".

export const cancelTurn = transportAction<string>({
  name: "chat.cancel_turn",
  command: (chatID) => ({ type: "cancel", chat_id: chatID }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't cancel turn",
});

// --- chat.switch_model ---
// Uses defineAction because: (1) caller needs boolean return, (2) setThinking
// is a loading indicator (set on start, cleared on completion), not an optimistic
// mutation. transportAction's optimistic/rollback pattern doesn't fit this lifecycle.
//
// On failure, rollback restores the previous model via setModel(). bindLoadingState handles the spinner.

export const switchModel = defineAction<{ chatID: string; model: string }, boolean, { prev: string }>({
  name: "chat.switch_model",
  scope: ({ chatID }) => `chat:${chatID}`,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  optimistic: ({ chatID, model }) => {
    const session = get(chatID);
    if (session === undefined) return undefined;
    const prev = session.model;
    setModel(chatID, model);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) setModel(chatID, op.prev);
  },
  run: async ({ chatID, model }, signal) => {
    // Don't touch thinking state — it's owned by sendPrompt and
    // bindLoadingState on the model switcher button handles the UI indicator.
    const r = await transportSend(
      { type: "switch_model", chat_id: chatID, payload: { model } },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      if (signal.aborted || r.code === "cancelled") {
        throw new ActionError("cancelled", { code: "cancelled" });
      }
      const errOpts: { status: number; code?: string } = { status: r.status };
      if (r.code !== undefined) errOpts.code = r.code;
      throw new ActionError(r.error ?? `send failed (${String(r.status)})`, errOpts);
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

interface SendPromptArgs {
  chatID: string;
  text: string;
  messageID: string;
  agent: string;
  model: string;
  activeFile: string;
  openFiles: readonly string[];
  attachments?: readonly unknown[];
}

export const sendPrompt = defineAction<SendPromptArgs, "sent" | "queued", { chatID: string }>({
  name: "chat.send_prompt",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  optimistic: ({ chatID }) => {
    setThinking(chatID, true);
    return { chatID };
  },
  rollback: (_args, op) => {
    if (op !== undefined) setThinking(op.chatID, false);
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
    throw new ActionError(r.error ?? "send failed", {
      status: r.status,
      ...(r.code !== undefined ? { code: r.code } : {}),
    });
  },
  error: false,  // send-state.ts (blocked send button) is the surface
});

// --- chat.resolve_pending_change ---

export const resolvePendingChange = transportAction<{ chatID: string; toolCallID: string; action: "accept" | "reject" }>({
  name: "chat.resolve_pending_change",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  command: ({ chatID, toolCallID, action }) => ({
    type: "resolve_pending_change",
    chat_id: chatID,
    payload: { tool_call_id: toolCallID, action },
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  // Args-aware error message: "Failed to accept change" / "Failed to reject change".
  error: ({ action }) => `Failed to ${action} change`,
});

// --- chat.respond_permission ---
// Scope is per-request (not per-chat): two pending permission requests in
// the same chat are independent and should fire in parallel. Serializing
// them behind the chat scope would delay the second response until the
// first round-trips, which feels sluggish when the agent is waiting on
// multiple permissions simultaneously.

export const respondPermission = transportAction<{ chatID: string; requestID: number; optionID: string }>({
  name: "chat.respond_permission",
  scope: ({ chatID, requestID }) => `perm:${chatID}:${String(requestID)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  command: ({ chatID, requestID, optionID }) => ({
    type: "permission_response",
    chat_id: chatID,
    payload: { request_id: requestID, option_id: optionID },
  }),
  error: "Couldn't send permission response",
});

// --- chat.restore_checkpoint ---

export const restoreCheckpoint = transportAction<{ chatID: string; tag: string }>({
  name: "chat.restore_checkpoint",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  command: ({ chatID, tag }) => ({
    type: "restore_checkpoint",
    chat_id: chatID,
    payload: { tag },
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't restore checkpoint",
});
