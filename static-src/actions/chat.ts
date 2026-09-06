// Actions for chat lifecycle: delete, list previous sessions, resume a
// previous session, cancel, switch model, set mode, compact, send prompt,
// permission response, elicitation response, user-input response, and the
// supervised (autopilot) toggle.

import {
  apiAction,
  defineAction,
  ActionError,
  retryNetwork,
  RETRY_STANDARD,
  transportAction,
  IDEMPOTENCY_COMMAND_FIELD,
  API_TIMEOUT_MS,
} from "./index.js";

import type {
  ChatHeader,
  PendingSteer,
  Session,
  SessionListResponse,
  TabSubject,
} from "../types.js";
import {
  decodeChatHeader,
  decodeSessionListResponse,
  decodeTabSubject,
} from "../wire/decoders.gen.js";
import {
  get,
  setThinking,
  setSupervisedMode,
  removeChat,
  reinsertSession,
  indexOfSession,
  setModel,
  setCurrentMode,
  setTurnFailed,
  setTurnDone,
  recordSteerSent,
  recordSteerQueued,
  forgetSteer,
  steerIDFor,
  dropConfirmedSteers,
  restoreSteers,
} from "../store.js";
import { send as transportSend, type SendResult } from "../transport.js";

// --- chat.create ---
// Server mints the chat id and returns it. `opID` is a dispatch argument
// (never minted inside `run()`) so a retry past the Idempotency-Key TTL
// resolves via the server's op_id ledger (command/create_ledger.go) instead
// of minting a second chat. No `dedupe`: two clicks on New chat are two chats.

export const createChat = defineAction<
  { opID: string; name?: string; model?: string },
  CreatedChat | null
>({
  name: "chat.create",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: async ({ opID, name, model }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "create_chat",
        payload: {
          op_id: opID,
          ...(name === undefined || name === "" ? {} : { name }),
          ...(model === undefined || model === "" ? {} : { model }),
        },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    return chatFromReply(r, signal, "create a chat");
  },
  error: "Couldn't create a chat",
});

/** What a creating command committed: the chat, and the tab the coordinator
 *  opened for it in the same operation. The caller adopts `subject` to paint
 *  and activate the tab with no second `open_tab` round trip. `subject` is
 *  absent when no tab store is wired; `version` is the collection version
 *  the open committed. */
export interface CreatedChat {
  chat: ChatHeader;
  subject?: TabSubject;
  version: number;
}

/** Reads the chat a creating command returned, or throws — a reply with no
 *  chat is a failure even at HTTP 200, shared across all three creating
 *  actions. */
function chatFromReply(r: SendResult, signal: AbortSignal, what: string): CreatedChat {
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
  const body = r.body;
  if (typeof body !== "object" || body === null || !("chat" in body)) {
    throw new ActionError(`the server did not say which chat it created (${what})`, {
      status: r.status,
      code: "missing_chat",
    });
  }
  const rec = body as Record<string, unknown>;
  const version = rec["version"];
  return {
    chat: decodeChatHeader(rec["chat"]),
    ...(rec["subject"] === undefined || rec["subject"] === null
      ? {}
      : { subject: decodeTabSubject(rec["subject"]) }),
    // 0 when absent or malformed: below every real version, so the machine
    // treats the op as already covered.
    version: typeof version === "number" && Number.isFinite(version) ? version : 0,
  };
}

// There is no `chat.close` action: `close_tab` is the one gesture, and it runs
// the same teardown server-side through the membership coordinator
// (`closeChatTeardown`).

// --- chat.delete ---
// With retention off, closing a non-empty chat's tab deletes it permanently.
// With retention on, a close just drops the tab (removeChat) and the server
// keeps the chat until the purge window expires. This is the app's ONLY chat
// delete path.

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
  // If the server deleted but the response timed out, rollback reinserts a
  // ghost session that a later SSE chat_deleted event removes.
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

/** Persists the composer text typed and not sent, server-side so a draft
 *  follows the user across devices. Debounced 600ms, flushed on blur, chat
 *  switch, and unload.
 *
 *  - `success: false` / `error: false`: draft saving is user-transparent —
 *    the live textarea still holds the text and the next keystroke retries.
 *  - `scope` is per COMPOSER, not per chat: it serializes draft writes
 *    against each other, never against `chat.send_prompt`'s `chat:<id>`
 *    scope (held for the whole turn) — sharing it would queue the send's
 *    own draft-clear behind the turn it just started. Attachments share
 *    this scope too, since `draft_changed` carries both fields in one frame.
 *  - No optimism: the composer IS the view of this value.
 *
 *  Not retryable: a retry would re-send text the next debounce supersedes. */
export const setDraft = transportAction<{ chatID: string; text: string }>({
  name: "chat.set_draft",
  networkMode: "always",
  scope: ({ chatID }) => `composer:${chatID}`,
  command: ({ chatID, text }) => ({
    type: "set_draft",
    chat_id: chatID,
    payload: { text },
  }),
  success: false,
  error: false,
});

// --- chat.set_attachments ---

/** Persists the workspace paths staged beside a chat's draft, the draft's
 *  twin: same 600ms cadence, same `composer:<id>` scope (keeps the two
 *  halves of one `draft_changed` frame from interleaving), same silence, no
 *  retry. Paths, never contents — the server reads each file at send time.
 *  Sends the WHOLE list, since the row on screen is the authoritative copy. */
export const setAttachments = transportAction<{ chatID: string; paths: string[] }>({
  name: "chat.set_attachments",
  networkMode: "always",
  scope: ({ chatID }) => `composer:${chatID}`,
  command: ({ chatID, paths }) => ({
    type: "set_attachments",
    chat_id: chatID,
    payload: { paths },
  }),
  success: false,
  error: false,
});

// --- chat.compact ---

/** Summarizes the conversation through KAS's native `compact` verb (typed
 *  `/compact` reaches the model as prose, not this — see typed-commands.ts).
 *  `idempotencyKey` because a retry that compacts twice summarizes a summary. */
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

/** Delivers a message into the running turn (`_session/steer`). Optimistic:
 *  the row is drawn on submit since the composer clears its text then, and a
 *  refusal (409 turn-ended-mid-flight or idle, 400 on a `[notification/...]`
 *  prefix) un-draws it and restores the composer text.
 *
 *  Custom runner rather than `transportAction`: the 200 carries the
 *  authoritative `steer_id`, adopted here to confirm the chip within the
 *  POST's own round trip rather than waiting on `steer_queued`. It also
 *  lifts the envelope's `reason` into the ActionError's code, letting
 *  submit.ts convert a `no_turn` refusal back into a prompt.
 *
 *  `error: false`: submit.ts owns the failure surface. */
export const steerChat = defineAction<
  { chatID: string; text: string; messageID: string },
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- run consumes the POST body itself, no result for a caller
  void,
  { chatID: string; steerID: string }
>({
  name: "chat.steer",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  error: false,
  run: async ({ chatID, text, messageID }, signal, ctx) => {
    const cmd: Parameters<typeof transportSend>[0] = {
      type: "steer",
      chat_id: chatID,
      payload: { text, message_id: messageID },
    };
    if (ctx?.idempotencyKey !== undefined) {
      (cmd as Record<string, unknown>)[IDEMPOTENCY_COMMAND_FIELD] = ctx.idempotencyKey;
    }
    const r: SendResult = await transportSend(cmd, { signal, reportSendState: false });
    if (!r.ok) {
      const opts: { status: number; code?: string } = { status: r.status };
      const code = r.reason ?? r.code;
      if (code !== undefined) {
        opts.code = code;
      }
      throw new ActionError(r.error ?? `send failed (${String(r.status)})`, opts);
    }
    const steerID = steerIDOf(r.body);
    if (steerID !== "") {
      // `user` is a fact on this path, not a guess: this is the reply to THIS
      // device's own POST, and the server has just recorded the same id in the
      // ledger its own `steer_queued` frame will be stamped from.
      recordSteerQueued(chatID, { id: steerID, text, origin: "user" });
    }
  },
  optimistic: ({ chatID, text, messageID }) => {
    recordSteerSent(chatID, messageID, text);
    return { chatID, steerID: steerIDFor(messageID) };
  },
  // args re-derives the id rather than reading `op`, which is undefined when
  // the dispatch dies before `optimistic` ran.
  rollback: ({ chatID, messageID }) => {
    forgetSteer(chatID, steerIDFor(messageID));
  },
});

/** The `steer_id` off the steer response body, or "" for an older server
 *  whose reply the SSE frame then covers. */
function steerIDOf(body: unknown): string {
  if (body === null || typeof body !== "object") {
    return "";
  }
  const id = (body as Record<string, unknown>)["steer_id"];
  return typeof id === "string" ? id : "";
}

/** Drops every steer KAS is still holding for this chat
 *  (`_session/steer/clear`). Does not cancel the turn. Optimistic, so an
 *  explicit discard never leaves a "not delivered" mark. Only CONFIRMED
 *  entries go — a `pending` one has no server-side id yet, so the clear
 *  cannot address it. */
export const clearSteers = transportAction<
  { chatID: string },
  { chatID: string; removed: readonly PendingSteer[] }
>({
  name: "chat.clear_steers",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID }) => ({ type: "steer_clear", chat_id: chatID }),
  optimistic: ({ chatID }) => ({ chatID, removed: dropConfirmedSteers(chatID) }),
  // The removed entries exist only on the optimistic result, so this rollback
  // reads the second parameter. `undefined` means `optimistic` never ran, so
  // nothing was taken out and there is nothing to put back.
  rollback: (_args, op) => {
    if (op !== undefined) {
      restoreSteers(op.chatID, op.removed);
    }
  },
  error: "Couldn't discard",
});

// --- chat.set_mode ---
// Switches the chat's session mode (v3). On a live bridge, switches in
// place via session/set_mode; on an empty chat, persists and applies at the
// first prompt. Optimistic so the pill flips instantly.

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

/** The History picker's inventory. Read through the GENERATED decoder, because
 *  the reply's two per-list verdicts are what the picker branches on to say
 *  whether there is nothing to resume or the read failed — a claim with no
 *  decoder behind it would let an absent verdict read as success. */
export const loadSessions = apiAction<
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
  void,
  SessionListResponse
>({
  name: "chat.load_sessions",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/sessions" }),
  decode: decodeSessionListResponse,
  error: "Couldn't load previous sessions",
});

// --- chat.resume_session ---
// Adopts a KAS session the picker listed as a NEW chat, bound to the session
// id; the transcript arrives from the session/load replay. Returns the
// header (server-minted id) so the caller can open it. `opID` matters more
// here than for a bare create: minting per attempt would leave two chats
// bound to one KAS session.

export const resumeSession = defineAction<
  { opID: string; sessionID: string; name: string },
  CreatedChat | null
>({
  name: "chat.resume_session",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: async ({ opID, sessionID, name }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "resume_session",
        payload: { session_id: sessionID, name, op_id: opID },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    return chatFromReply(r, signal, "resume a session");
  },
  error: "Couldn't resume that session",
});

// --- chat.fork ---
// Opens a tangent: a new chat starting with the parent's conversation, then
// diverging. Server calls `session/fork` on the parent's live session and
// binds the returned id, so nothing is copied client-side. Reply's `outcome`
// (`forked` or `primed`) is informational; the tangent opens either way.
// `opID` stops a retry forking twice.

export const forkChat = defineAction<
  { opID: string; parentChatID: string; title?: string },
  CreatedChat | null
>({
  name: "chat.fork",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: async ({ opID, parentChatID, title }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "fork_chat",
        payload: {
          parent_chat_id: parentChatID,
          op_id: opID,
          ...(title === undefined ? {} : { title }),
        },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    return chatFromReply(r, signal, "start a tangent");
  },
  error: "Couldn't start a tangent",
});

// --- chat.cancel_turn ---
// No scope: cancel must fire immediately, not queue behind an in-flight
// sendPrompt in the same chat. Idempotent server-side.
// Named "cancel_turn" rather than "cancel" to avoid confusion with the
// Action.cancel() method.

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
// defineAction because the caller needs a boolean return, and setThinking is
// a loading indicator here rather than an optimistic mutation that
// transportAction's rollback pattern fits. Rollback restores the previous
// model via setModel().

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
// Posts a prompt to a chat with the shared thinking + 409 lifecycle. The
// server acks at admission ({accepted, message_id}), so dispatch runs at the
// standard API timeout; turn completion is SSE-anchored. Returns "sent" on
// ack, "queued" on plain 409 (steerable turn in flight), "starting" on 409
// reason:"starting" (admission holder is a spawn/shell/prime, cannot
// receive a steer), or null on any other error.
//
// `error: false`: failure-notice.ts already raises the toast via
// transport.send's reportSendState.

interface SendPromptArgs {
  chatID: string;
  text: string;
  messageID: string;
  model: string;
  attachments?: readonly unknown[];
}

/** Latch snapshots for in-flight sends, keyed by message id. The `starting`
 *  arm returns a VALUE (framework rollback never runs for it), so this
 *  restores what rollback would: the previous turn's verdict stands. */
const latchSnapshots = new Map<string, { turnFailed: boolean; turnDone: boolean }>();

export const sendPrompt = defineAction<
  SendPromptArgs,
  "sent" | "queued" | "starting",
  { chatID: string; turnFailed: boolean; turnDone: boolean }
>({
  name: "chat.send_prompt",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  optimistic: ({ chatID, messageID }) => {
    // Captured because setThinking(true) clears these latches; a rollback
    // must restore a failure/done mark the reader had not yet seen.
    const s = get(chatID);
    const snapshot = {
      chatID,
      turnFailed: s?.turn_failed === true,
      turnDone: s?.turn_done === true,
    };
    latchSnapshots.set(messageID, { turnFailed: snapshot.turnFailed, turnDone: snapshot.turnDone });
    setThinking(chatID, true);
    return snapshot;
  },
  rollback: (args, op) => {
    latchSnapshots.delete(args.messageID);
    if (op !== undefined) {
      setThinking(op.chatID, false);
      if (op.turnFailed) {
        setTurnFailed(op.chatID);
      }
      if (op.turnDone) {
        setTurnDone(op.chatID);
      }
    }
  },
  run: async (args, signal, ctx) => {
    const { chatID, text, messageID, model, attachments } = args;
    const r = await transportSend(
      {
        type: "prompt",
        chat_id: chatID,
        // Top level, where transport.send reads it to build the
        // Idempotency-Key header — not inside `payload`.
        ...(ctx?.idempotencyKey !== undefined
          ? { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }
          : {}),
        payload: {
          text,
          message_id: messageID,
          model,
          attachments:
            attachments !== undefined && attachments.length > 0 ? attachments : undefined,
        },
      },
      { signal, reportSendState: true, timeoutMs: API_TIMEOUT_MS },
    );
    if (r.ok) {
      latchSnapshots.delete(messageID);
      return "sent";
    }
    if (r.status === 409) {
      if (r.reason === "starting") {
        // The admission holder is a cold spawn, a shell or a prime — none of
        // which can receive a steer — so this is a POST-PERSIST failure class:
        // the user row is already persisted and rendered (persist precedes
        // reservation server-side). Returned as a VALUE so the caller can
        // branch on it, which means the framework's rollback never runs; the
        // full optimistic write is undone here instead. Thinking retracted,
        // because thinking left true would turn the user's retry into a steer
        // The holder is a cold spawn, shell or prime — none can receive a
        // steer, so this is a post-persist failure class. Thinking retracted
        // and the latches restored so the previous turn's verdict stands
        // until the holder's own turn opens.
        setThinking(chatID, false);
        const snap = latchSnapshots.get(messageID);
        if (snap?.turnFailed === true) {
          setTurnFailed(chatID);
        }
        if (snap?.turnDone === true) {
          setTurnDone(chatID);
        }
        latchSnapshots.delete(messageID);
        return "starting";
      }
      // A steerable turn is in flight; caller (submit.ts) converts to a steer.
      latchSnapshots.delete(messageID);
      return "queued";
    }
    throw new ActionError(r.error ?? "send failed", {
      status: r.status,
      ...(r.code !== undefined ? { code: r.code } : {}),
    });
  },
  error: false, // send-state.ts is the surface
});

// --- the three interactive asks ---
// Scope is per-request, not per-chat: two pending requests in the same chat
// are independent and should fire in parallel.

/** The Go sentinel `errAlreadyAnswered` (internal/command/validate.go), served
 *  as 409 `{"error":"already_answered"}`. Matched by value; no generated
 *  constant exists for a command error body. */
const ALREADY_ANSWERED = "already_answered";

/** Answering an ask has three outcomes: answered, already settled by another
 *  surface (`superseded`), or failed. `superseded` is not an error — the
 *  server accepts exactly one answer per request id, so the reader's intent
 *  was still met. It is silent here; decision-dock.ts already announces the
 *  superseded card with attribution.
 *
 *  These carry a custom runner rather than `transportAction` because that
 *  framework's `run()` throws on every `!ok` and has no third outcome. */
type DecisionAnswer = "answered" | "superseded";

/** Sends one answer and classifies the outcome. */
async function answerDecision(
  cmd: { type: string; chat_id: string; payload: Record<string, unknown> },
  signal: AbortSignal,
  idempotencyKey: string | undefined,
): Promise<DecisionAnswer> {
  const withKey =
    idempotencyKey !== undefined ? { ...cmd, [IDEMPOTENCY_COMMAND_FIELD]: idempotencyKey } : cmd;
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
    /** A turn approval's per-action verdicts: KAS's action id → keep. Absent
     *  on an ordinary tool permission. Every offered action must appear —
     *  an omitted id is a silent rollback, not "no opinion". */
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
  // Reached only by a real failure: a superseded answer returns normally.
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
