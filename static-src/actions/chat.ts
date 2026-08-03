// Actions for chat lifecycle: delete, list previous sessions, resume a
// previous session, cancel, switch model, set mode, compact, send prompt,
// permission response, elicitation response, user-input response, and the
// supervised (autopilot) toggle.
//
// There are no resolve/trust pending-change actions: staging is KAS's, and a
// turn's writes are approved through the ORDINARY permission reply carrying a
// per-action fileDecisions map (see api.PermissionOutcomeWithFileDecisions).
// ---------------------------------------------------------------------------

import {
  apiAction,
  defineAction,
  ActionError,
  retryNetwork,
  RETRY_STANDARD,
  transportAction,
  IDEMPOTENCY_COMMAND_FIELD,
} from "./index.js";

import type { ResumableSessionRow, Session, WorkflowRunRow } from "../types.js";
import {
  get,
  setThinking,
  setSupervisedMode,
  removeChat,
  reinsertSession,
  indexOfSession,
  setModel,
  setCurrentMode,
} from "../store.js";
import { send as transportSend } from "../transport.js";
import { clearLastError } from "../send-state.js";

// --- chat.delete ---
// The tab-close path in the "no retention" mode (retention = 0): closing a
// non-empty chat deletes it permanently (ephemeral chats). With retention on,
// a close just drops the tab (removeChat) and the server keeps the chat until
// the purge window expires — there is no archive action and no
// chat.delete_archived. This is the app's ONLY chat delete path.

export const deleteChat = transportAction<string, { session: Session; atIndex: number }>({
  name: "chat.delete",
  networkMode: "always",
  scope: (id) => `chat:${id}`,
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  command: (id) => ({ type: "delete_chat", chat_id: id }),
  optimistic: (id) => {
    const session = get(id);
    if (session === undefined) {
      return undefined;
    }
    const atIndex = indexOfSession(id);
    removeChat(id);
    return { session, atIndex };
  },
  // Trade-off: if the server actually deleted but the HTTP response timed out,
  // rollback reinserts a ghost session. A subsequent SSE chat_deleted event will
  // remove it, causing a brief flicker. Full correctness would require server-side
  // dedup + ack; the user-visible glitch is negligible so we accept it.
  rollback: (_id, op) => {
    if (op !== undefined) {
      reinsertSession(op.session, op.atIndex);
    }
  },
  error: "Couldn't delete chat",
});

// --- chat.set_supervised ---

export const setSupervised = transportAction<
  { chatID: string; enabled: boolean },
  { prev: boolean }
>({
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
    if (session === undefined) {
      return undefined;
    }
    const prev: boolean = session.supervised_mode ?? false;
    setSupervisedMode(chatID, enabled);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) {
      setSupervisedMode(chatID, op.prev);
    }
  },
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't update supervised mode",
});

// --- chat.compact ---

/** compactChat summarizes the conversation through KAS's NATIVE verb.
 *
 *  An action rather than passed-through text because typed `/compact` performs no
 *  compaction — nothing in KAS parses it, so the text reaches the model, which
 *  answers as if it had happened. See typed-commands.ts.
 *
 *  `idempotencyKey` because a retry that compacts twice summarizes a summary.
 *  The error is the server's own message: KAS gives one undiscriminated
 *  `{success: false}` for a turn in flight and for a compaction already running,
 *  and the server turns that into the single cause a user can act on. */
export const compactChat = transportAction<{ chatID: string }>({
  name: "chat.compact",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  command: ({ chatID }) => ({
    type: "compact",
    chat_id: chatID,
  }),
  error: "Couldn't compact",
});

// --- chat.set_mode ---
//
// Switch the chat's session mode (v3): the prompt-bar mode picker's apply
// path. On a chat with a live bridge the server switches in place via
// session/set_mode; on an empty chat it persists the choice and applies it
// when the first prompt starts the session. Optimistic so the pill flips
// instantly; the server's mode_changed broadcast reconciles (idempotent).

export const setMode = transportAction<{ chatID: string; modeID: string }, { prev: string }>({
  name: "chat.set_mode",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, modeID }) => ({
    type: "set_mode",
    chat_id: chatID,
    payload: { mode_id: modeID },
  }),
  optimistic: ({ chatID, modeID }) => {
    const session = get(chatID);
    if (session === undefined) {
      return undefined;
    }
    const prev = session.current_mode_id;
    setCurrentMode(chatID, modeID);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) {
      setCurrentMode(chatID, op.prev);
    }
  },
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't switch mode",
});

// --- chat.restore ---

export const loadSessions = apiAction<
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
  void,
  { sessions: ResumableSessionRow[]; runs: WorkflowRunRow[] }
>({
  name: "chat.load_sessions",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/sessions" }),
  error: "Couldn't load previous sessions",
});

// --- chat.resume_session ---
// Adopts a KAS session the picker listed as a NEW chat. The server creates the
// chat already bound to the session id; the transcript arrives from the
// session/load replay, so nothing is copied client-side.

export const resumeSession = transportAction<
  { chatID: string; sessionID: string; name: string },
  { ok: boolean }
>({
  name: "chat.resume_session",
  command: ({ chatID, sessionID, name }) => ({
    type: "resume_session",
    chat_id: chatID,
    payload: { session_id: sessionID, name },
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't resume that session",
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

export const cancelTurn = transportAction<string, { wasThinking: boolean }>({
  name: "chat.cancel_turn",
  command: (chatID) => ({ type: "cancel", chat_id: chatID }),
  optimistic: (chatID) => {
    const session = get(chatID);
    const wasThinking = session?.thinking ?? false;
    setThinking(chatID, false);
    return { wasThinking };
  },
  rollback: (chatID, op) => {
    if (op !== undefined) {
      setThinking(chatID, op.wasThinking);
    }
  },
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

export const switchModel = defineAction<
  { chatID: string; model: string },
  boolean,
  { prev: string }
>({
  name: "chat.switch_model",
  scope: ({ chatID }) => `chat:${chatID}`,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  optimistic: ({ chatID, model }) => {
    const session = get(chatID);
    if (session === undefined) {
      return undefined;
    }
    const prev = session.model;
    setModel(chatID, model);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) {
      setModel(chatID, op.prev);
    }
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
      if (r.code !== undefined) {
        errOpts.code = r.code;
      }
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
  model: string;
  attachments?: readonly unknown[];
}

/** How long a dead prompt POST waits for the user-message SSE echo
 *  before conceding failure. Covers the race where the connection died
 *  as the server accepted: the echo is usually already ingested (the
 *  turn has been streaming for the whole POST lifetime); the wait only
 *  matters when death and accept were near-simultaneous. */
const PROMPT_ECHO_GRACE_MS = 2000;

/** True when the server's message_appended echo for the given user
 *  message id is in the store — proof the prompt was accepted AND that
 *  SSE is delivering, which outranks a dead POST connection. */
function promptEchoArrived(chatID: string, messageID: string): boolean {
  const s = get(chatID);
  return s?.messages.some((m) => m.id === messageID) ?? false;
}

/** Check for the prompt echo, allowing one grace window. */
async function promptEchoed(chatID: string, messageID: string): Promise<boolean> {
  if (promptEchoArrived(chatID, messageID)) {
    return true;
  }
  await new Promise((resolve) => setTimeout(resolve, PROMPT_ECHO_GRACE_MS));
  return promptEchoArrived(chatID, messageID);
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
    if (op !== undefined) {
      setThinking(op.chatID, false);
    }
  },
  run: async (args, signal, ctx) => {
    const { chatID, text, messageID, model, attachments } = args;
    const r = await transportSend(
      {
        type: "prompt",
        chat_id: chatID,
        payload: {
          text,
          message_id: messageID,
          model,
          attachments:
            attachments !== undefined && attachments.length > 0 ? attachments : undefined,
          // Only include the key if the framework provided one (avoids
          // sending `idempotency_key: undefined` which serializes
          // inconsistently). Field name shared with transportAction's
          // injection via the package constant.
          ...(ctx?.idempotencyKey !== undefined
            ? { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }
            : {}),
        },
      },
      { signal, reportSendState: true }, // send-state IS the error surface
    );
    if (r.ok) {
      return "sent";
    }
    if (r.status === 409) {
      // A turn is in flight on this chat. Report "queued" and let the caller
      // (prompt-queue.ts submitPrompt) buffer it. Keeping this action a pure
      // send is what lets the drain path re-send a queued prompt without
      // double-enqueuing it (peek → send → remove-on-sent lives in the queue).
      return "queued";
    }
    // Dead POST, live SSE (P9 residue): the prompt POST is the one
    // long-lived connection that can't carry a keepalive, so it can
    // die (proxy reset, network flap, 15-min timeout) while the turn
    // it started runs on fine. The POST result is NOT authoritative —
    // completion is SSE-anchored. If the server's message_appended
    // echo for OUR message id is in the store (now, or within a short
    // grace window for the race where the POST died right at accept),
    // the send succeeded: report "sent" instead of a false failure.
    if (r.status === 0 && r.code !== "cancelled" && (await promptEchoed(chatID, messageID))) {
      clearLastError(); // transport already painted the blocked state
      return "sent";
    }
    throw new ActionError(r.error ?? "send failed", {
      status: r.status,
      ...(r.code !== undefined ? { code: r.code } : {}),
    });
  },
  error: false, // send-state.ts (blocked send button) is the surface
});

// --- chat.respond_permission ---
// Scope is per-request (not per-chat): two pending permission requests in
// the same chat are independent and should fire in parallel. Serializing
// them behind the chat scope would delay the second response until the
// first round-trips, which feels sluggish when the agent is waiting on
// multiple permissions simultaneously.

export const respondPermission = transportAction<{
  chatID: string;
  requestID: number;
  optionID: string;
}>({
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

export const respondElicitation = transportAction<{
  chatID: string;
  requestID: number;
  action: "accept" | "decline" | "cancel";
  content?: Record<string, unknown>;
}>({
  name: "chat.respond_elicitation",
  scope: ({ chatID, requestID }) => `elicit:${chatID}:${String(requestID)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  command: ({ chatID, requestID, action, content }) => ({
    type: "elicitation_response",
    chat_id: chatID,
    payload:
      action === "accept" && content !== undefined
        ? { request_id: requestID, action, content }
        : { request_id: requestID, action },
  }),
  error: "Couldn't send elicitation response",
});

export const respondUserInput = transportAction<{
  chatID: string;
  requestID: number;
  action: "answered" | "dismissed";
  answer?: string;
}>({
  name: "chat.respond_user_input",
  scope: ({ chatID, requestID }) => `user-input:${chatID}:${String(requestID)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  command: ({ chatID, requestID, action, answer }) => ({
    type: "user_input_response",
    chat_id: chatID,
    payload:
      action === "answered" && answer !== undefined
        ? { request_id: requestID, action, answer }
        : { request_id: requestID, action },
  }),
  error: "Couldn't send your answer",
});
