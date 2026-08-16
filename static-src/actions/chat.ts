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

// --- chat.close ---
// The tab-close teardown: the x means "kill all of it" (user decision) — the
// in-flight turn, the chat's runs, the process. The chat RECORD survives;
// reopening session/loads it back. Fire-and-forget with no error toast: the
// tab is already gone, and there is nothing for the user to redo.

export const closeChat = transportAction<string, { ok: boolean }>({
  name: "chat.close",
  networkMode: "always",
  command: (chatID) => ({ type: "close_chat", chat_id: chatID }),
  error: false,
  success: false,
});

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

// --- chat.set_draft ---

/** Persist the composer text the user has typed into a chat and not sent.
 *
 *  Server-side rather than localStorage so a draft follows the user across
 *  devices and joins the state that is already per-chat and canonical. Dispatched
 *  on a 600ms debounce and flushed on blur, on a chat switch and on unload, so it
 *  is the highest-frequency mutation in the app and is deliberately quiet:
 *
 *    - `success: false` / `error: false` — the decision says draft saving is
 *      user-transparent. A toast every 600ms of typing would be the loudest
 *      thing in the UI, and a failed draft save costs nothing the user can act
 *      on: the live textarea still holds the text and the next keystroke retries.
 *    - `scope` per chat so dispatches serialize, which is what removes the need
 *      for a generation counter to stop an older save landing after a newer one.
 *    - no optimism, because there is nothing to render: the composer IS the
 *      view of this value.
 *
 *  Not retryable on purpose. A retry would re-send text the next debounce is
 *  about to supersede. */
export const setDraft = transportAction<{ chatID: string; text: string }>({
  name: "chat.set_draft",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, text }) => ({
    type: "set_draft",
    chat_id: chatID,
    payload: { text },
  }),
  success: false,
  error: false,
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

// --- chat.steer ---

/** Deliver a message INTO the running turn (`_session/steer`).
 *
 *  No optimistic anything, deliberately. The chip appears when KAS's own
 *  `steer_queued` frame arrives, not when this dispatch resolves — same rule as
 *  a user message waiting for `message_appended` (vibekit.md #2). Optimism here
 *  would also be wrong rather than merely early: the server can refuse with 409
 *  when the turn ended mid-flight, and a chip that appeared and then vanished
 *  reads as a message that was lost.
 *
 *  The error toast is the server's own words. Both refusals it can return are
 *  specific and actionable — the turn ended, or the text opens with a
 *  `[notification/...]` prefix KAS would reclassify — so restating them here
 *  would only make them vaguer.
 */
export const steerChat = transportAction<{ chatID: string; text: string; messageID: string }>({
  name: "chat.steer",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  command: ({ chatID, text, messageID }) => ({
    type: "steer",
    chat_id: chatID,
    payload: { text, message_id: messageID },
  }),
});

/** Drop every steer KAS is still holding for this chat (`_session/steer/clear`).
 *
 *  Does not cancel the turn: changing your mind about a message you just sent
 *  should not also throw away the work in flight. */
export const clearSteers = transportAction<{ chatID: string }>({
  name: "chat.clear_steers",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID }) => ({ type: "steer_clear", chat_id: chatID }),
  error: "Couldn't discard",
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

// --- chat.fork ---
// Open a TANGENT off another chat: a new chat that starts with the parent's real
// conversation behind it and then diverges.
//
// The server calls KAS's own `session/fork` on the parent's live session and
// binds the returned session id to the new chat, so the transcript arrives from
// the session/load replay and nothing is copied client-side. The reply's
// `outcome` names which path ran — `forked` (the session was branched) or
// `primed` (the fork was refused, so the parent's transcript is injected into a
// fresh session instead). The tangent opens either way, so the client does not
// branch on it; it is there so a report about a vague answer can say which.

export const forkChat = transportAction<
  { chatID: string; parentChatID: string; title?: string },
  { ok: boolean }
>({
  name: "chat.fork",
  networkMode: "always",
  command: ({ chatID, parentChatID, title }) => ({
    type: "fork_chat",
    chat_id: chatID,
    payload: { parent_chat_id: parentChatID, ...(title === undefined ? {} : { title }) },
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't start a tangent",
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
// `error: false`: the send-state.ts error-button is the canonical
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
      clearLastError(); // transport already painted the error state
      return "sent";
    }
    throw new ActionError(r.error ?? "send failed", {
      status: r.status,
      ...(r.code !== undefined ? { code: r.code } : {}),
    });
  },
  error: false, // send-state.ts (the send button's error face) is the surface
});

// --- the three interactive asks ---
// Scope is per-request (not per-chat): two pending requests in the same chat
// are independent and should fire in parallel. Serializing them behind the
// chat scope would delay the second response until the first round-trips,
// which feels sluggish when the agent is waiting on several at once.

/** The Go sentinel `errAlreadyAnswered` (internal/command/validate.go), served
 *  as 409 `{"error":"already_answered"}`. Matched by value because there is no
 *  generated constant for a command error body; if that string moves, this
 *  breaks silently, so the two are named in each other's comments. */
const ALREADY_ANSWERED = "already_answered";

/** Answering an ask has THREE outcomes, not two: the answer landed, the request
 *  was already settled by another surface, or the send failed.
 *
 *  `superseded` is NOT an error. The reader's intent — dispose of this pending
 *  decision — was met; their particular choice simply did not win, and the agent
 *  server accepts exactly one answer per request id (hub.TakePendingPerm is what
 *  decides the winner). Reporting it through the error branch produced a
 *  "Couldn't send permission response" toast beside the dock's own correct
 *  "answered in another window", which reads as a failure to retry.
 *
 *  It is deliberately SILENT here. The surface that owns the explanation is the
 *  one that was showing the superseded card, and decision-dock.ts already
 *  announces it with attribution and only when the card was on screen. A toast
 *  from this layer could only duplicate that or contradict it.
 *
 *  Why these three do not use `transportAction`: its generated run() throws on
 *  every !ok, and the framework's notification vocabulary is success/error, so a
 *  third outcome cannot be expressed through it. THAT is the real defect and it
 *  belongs upstream in @cplieger/actions as a `superseded` branch defaulting to
 *  silent. Until it exists these carry their own runner, which is the path the
 *  package sanctions by exporting IDEMPOTENCY_COMMAND_FIELD "so consumer-authored
 *  custom runners can share the wire convention instead of hand-copying the
 *  literal". The cost, stated: they no longer share the adapter's error mapping,
 *  so a change there will not reach them. */
type DecisionAnswer = "answered" | "superseded";

/** Sends one answer and classifies the outcome. The idempotency key is injected
 *  at the command's TOP level, exactly where transportAction put it, so the wire
 *  is unchanged by this refactor. (That field is inert against vibekit's own
 *  transport — the server dedups on the envelope's request_id and on an
 *  Idempotency-Key HEADER that transport.ts never sets — but preserving it keeps
 *  this a behaviour-preserving change plus one new outcome.) */
async function answerDecision(
  cmd: { type: string; chat_id: string; payload: Record<string, unknown> },
  signal: AbortSignal,
  idempotencyKey: string | undefined,
): Promise<DecisionAnswer> {
  const withKey =
    idempotencyKey !== undefined ? { ...cmd, [IDEMPOTENCY_COMMAND_FIELD]: idempotencyKey } : cmd;
  // The cast and `reportSendState: false` both mirror the transport bridge in
  // actions/boot.ts, which is the path transportAction took. The envelope's
  // request_id is minted by transport.send, so an action-level command is
  // legitimately looser than the `Command` union; and send-state is the prompt
  // button's surface, which an ask's answer must not touch.
  const r = await transportSend(withKey as Parameters<typeof transportSend>[0], {
    signal,
    reportSendState: false,
  });
  if (r.ok) {
    return "answered";
  }
  if (r.status === 409 && r.error === ALREADY_ANSWERED) {
    return "superseded";
  }
  throw new ActionError(r.error ?? `send failed (${String(r.status)})`, {
    status: r.status,
    ...(r.code !== undefined ? { code: r.code } : {}),
  });
}

export const respondPermission = defineAction<
  {
    chatID: string;
    requestID: number;
    optionID: string;
    /** A TURN APPROVAL's per-action verdicts: KAS's action id → keep. Absent on
     *  an ordinary tool permission. Every action the request offered must appear
     *  — KAS restores whatever is not in the accepted set, so an omitted id is a
     *  silent rollback rather than "no opinion". */
    fileDecisions?: Record<string, boolean>;
  },
  DecisionAnswer
>({
  name: "chat.respond_permission",
  scope: ({ chatID, requestID }) => `perm:${chatID}:${String(requestID)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: ({ chatID, requestID, optionID, fileDecisions }, signal, ctx) =>
    answerDecision(
      {
        type: "permission_response",
        chat_id: chatID,
        payload:
          fileDecisions !== undefined
            ? { request_id: requestID, option_id: optionID, file_decisions: fileDecisions }
            : { request_id: requestID, option_id: optionID },
      },
      signal,
      ctx?.idempotencyKey,
    ),
  // Reached only by a REAL failure now: a superseded answer returns normally.
  error: "Couldn't send permission response",
});

export const respondElicitation = defineAction<
  {
    chatID: string;
    requestID: number;
    action: "accept" | "decline" | "cancel";
    content?: Record<string, unknown>;
  },
  DecisionAnswer
>({
  name: "chat.respond_elicitation",
  scope: ({ chatID, requestID }) => `elicit:${chatID}:${String(requestID)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: ({ chatID, requestID, action, content }, signal, ctx) =>
    answerDecision(
      {
        type: "elicitation_response",
        chat_id: chatID,
        payload:
          action === "accept" && content !== undefined
            ? { request_id: requestID, action, content }
            : { request_id: requestID, action },
      },
      signal,
      ctx?.idempotencyKey,
    ),
  error: "Couldn't send elicitation response",
});

export const respondUserInput = defineAction<
  {
    chatID: string;
    requestID: number;
    action: "answered" | "dismissed";
    answer?: string;
  },
  DecisionAnswer
>({
  name: "chat.respond_user_input",
  scope: ({ chatID, requestID }) => `user-input:${chatID}:${String(requestID)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: ({ chatID, requestID, action, answer }, signal, ctx) =>
    answerDecision(
      {
        type: "user_input_response",
        chat_id: chatID,
        payload:
          action === "answered" && answer !== undefined
            ? { request_id: requestID, action, answer }
            : { request_id: requestID, action },
      },
      signal,
      ctx?.idempotencyKey,
    ),
  error: "Couldn't send your answer",
});
