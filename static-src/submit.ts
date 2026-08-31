// ---------------------------------------------------------------------------
// Submit: the single owner of what pressing Send MEANS, which depends on
// whether a turn is already running.
//
//   idle       → a prompt. Starts a turn. Unchanged.
//   mid-turn   → a STEER. Joins the turn already running (`_session/steer`).
//
// That is the whole decision, and it replaced a client-side prompt queue.
//
// WHY THE QUEUE IS GONE. It buffered the text on the session and drained it on
// `turn_ended`, so a correction was delivered only after the work it was
// correcting had finished — the one moment it could no longer help. Its escape
// hatch was a per-chip "send now" that CANCELLED the running turn to get ahead
// of it, discarding everything since the last durable step. Both existed because
// vibekit believed it could not reach a live turn. It could: KAS has taken a
// per-session steering buffer and a mid-turn injection point all along, and the
// belief traced to a probe of the wrong method name (`session/steer`, which does
// not exist, rather than `_session/steer`) whose -32601 was recorded as a
// capability KAS lacked.
//
// So there is no queue here, no drain, no re-entrancy guard, no FIFO to keep in
// order and no idle re-check. KAS owns the buffer; `session.steers` is vibekit's
// read-only projection of it (store.ts), written only by the three steer SSE
// events. Nothing in this module writes it.
//
// Layering: submit → chat-commands (sendPromptTo) / actions/chat (steerChat) →
// transport. Nothing below imports this module, so there is no cycle.
// ---------------------------------------------------------------------------

import { sendPromptTo } from "./chat-commands.js";
import { handleTypedCommand } from "./typed-commands.js";
import { newMessageID } from "./transport.js";
import {
  takeAttachments,
  addAttachmentTo,
  attachmentGeneration,
  type AttachedFile,
} from "./attachments.js";
import { isThinking, hasMessage } from "./store.js";
import { steerChat } from "./actions/chat.js";
import { clearAgentDown, reportSendRefused } from "./send-state.js";
import { restoreFailedSend } from "./composer-state.js";

export type SubmitResult = "sent" | "steered" | "failed";

/** The send-error face for a 409 reason:"starting" refusal. Holder-neutral on
 *  purpose: the admission slot may be held by a cold spawn, a shell or a prime,
 *  and for a shell holder nothing is "starting" — the honest common claim is
 *  busy-now-retry. Rendered through send-state's error surface; the next Send
 *  is the retry and is also what clears it. */
const STARTING_FACE = "The chat is busy right now — send again to retry";

/** The last failed attempt, so a retry of the SAME text on the SAME chat reuses
 *  its message id.
 *
 *  Required, not tidy: `appendUserMessage` persists the user row BEFORE the ACP
 *  call and nothing rolls it back (`AbandonInFlightTurn` deliberately keeps the
 *  partial), so a retry under a fresh id appends a second identical user row and
 *  hands KAS a second messageId. Reusing the id makes the retry idempotent
 *  server-side — `hasMessageID` no-ops the append — which is the same reason the
 *  retired prompt queue re-sent under the id it first used.
 *
 *  One slot is enough: there is one composer, so only the most recent failure can
 *  be sitting in it. A different chat or edited text is a different message and
 *  earns a fresh id. */
let lastFailed: { chatID: string; text: string; messageID: string } | undefined;

/** The id to send under: the failed attempt's when this is a retry of it. */
function messageIDFor(chatID: string, text: string): string {
  if (lastFailed?.chatID === chatID && lastFailed.text === text) {
    return lastFailed.messageID;
  }
  return newMessageID();
}

/**
 * User-facing submit. Sends a prompt on an idle chat and a steer on a busy one.
 *
 * The 409 path matters as much as the idle check and is the reason the two are
 * not one branch: a turn can start between reading `thinking` and the POST
 * landing, and the server answers that with 409 busy. The old code enqueued
 * there; this steers instead, because the message belongs in the turn that just
 * started rather than in the one after it.
 *
 * The conversion runs BOTH ways, bounded: a plain-409 prompt becomes a steer,
 * and a steer refused with reason `no_turn` (the chat was idle by the time it
 * landed — stale thinking, or the turn ended mid-flight) becomes the prompt it
 * should have been. Each hop spends one unit of a shared budget, so a chat
 * churning turn boundaries faster than the round trips ends at the busy face
 * instead of looping.
 *
 * On a hard failure the text AND the attachments go back to the input row, so a
 * retry is one keystroke rather than a retype; the send button surfaces the
 * error through send-state, and the retry travels under the failed attempt's own
 * message id so it lands on the user row that attempt already persisted.
 */
export async function submitPrompt(chatID: string, text: string): Promise<SubmitResult> {
  if (chatID === "" || text === "") {
    return "failed";
  }
  // A new attempt IS the retry, so a previous "no agent behind this chat" verdict
  // goes now rather than outliving the thing it described: the send that follows
  // is what respawns the bridge. Without this the signal is sticky for the life of
  // the chat (nothing on the failure path emits the turn_ended that clears it) and
  // every later send inherits a stale alert face.
  //
  // Failure TOASTS are deliberately left alone: they report what happened rather
  // than what is true now, they time out on their own, and dismissing one here
  // would race a fresh failure arriving for this very attempt.
  // Ahead of the typed-command branch on purpose: a command is an attempt too.
  clearAgentDown();
  // Typed commands vibekit owns are intercepted BEFORE anything else: before the
  // attachments are taken and before a message id is minted. A command is not a
  // prompt, so it must not consume an attachment or leave a user bubble behind.
  if (handleTypedCommand(chatID, text)) {
    return "sent";
  }
  const attachments = takeAttachments();
  // Read BEFORE the await: this is the attachment state the send is taking with
  // it, and recordFailure hands the token back so a restore into a state that was
  // dropped meanwhile (the chat was closed) is refused rather than recreating it.
  const attachGen = attachmentGeneration(chatID);
  const messageID = messageIDFor(chatID, text);

  if (isThinking(chatID)) {
    return steer(chatID, text, messageID, attachments, attachGen, CONVERT_BUDGET);
  }
  return prompt(chatID, text, messageID, attachments, attachGen, CONVERT_BUDGET);
}

/** How many prompt⇄steer conversions one submit may make. Two allows the honest
 *  double race (steer → the turn is gone → prompt → a new turn started → steer)
 *  and stops there. */
const CONVERT_BUDGET = 2;

/** Post one prompt, converting a plain 409 into a steer while budget remains. */
async function prompt(
  chatID: string,
  text: string,
  messageID: string,
  attachments: readonly unknown[],
  attachGen: number,
  convertBudget: number,
): Promise<SubmitResult> {
  const result = await sendPromptTo(chatID, text, {
    messageID,
    ...(attachments.length > 0 ? { attachments } : {}),
  });
  if (result === "queued") {
    // Plain 409: a steerable turn started underneath us. Steer into it. The
    // conversion is gated on the ABSENCE of the "starting" reason — that
    // refusal's holder cannot receive a steer and takes the branch below.
    if (convertBudget > 0) {
      return steer(chatID, text, messageID, attachments, attachGen, convertBudget - 1);
    }
    recordFailure(chatID, text, messageID, attachments, attachGen);
    reportSendRefused(STARTING_FACE);
    return "failed";
  }
  if (result === "starting") {
    // 409 reason:"starting": the admission slot is held by a cold spawn, a
    // shell or a prime, so neither a turn nor a steer can land right now. A
    // POST-PERSIST failure class: the user row is already persisted and
    // rendered (persist precedes reservation server-side), so recordFailure's
    // hasMessage gate keeps the text out of the composer while still
    // remembering the id — a re-send of the same text travels under it and the
    // server dedupes the append.
    recordFailure(chatID, text, messageID, attachments, attachGen);
    reportSendRefused(STARTING_FACE);
    return "failed";
  }
  if (result === "failed") {
    recordFailure(chatID, text, messageID, attachments, attachGen);
    return "failed";
  }
  lastFailed = undefined;
  return "sent";
}

/** Post one steer, converting a `no_turn` refusal into a prompt while budget
 * remains.
 *
 * The refusal branch reads the outcome's CODE, never error prose: the server
 * stamps reason `no_turn` on every there-is-nothing-to-join refusal (idle chat,
 * shell holder, the turn ending mid-flight), and that class means the message
 * should have been a prompt all along. The action's rollback has already
 * un-drawn the optimistic chip by the time the outcome resolves, so the
 * conversion leaves nothing behind.
 *
 * Every other failure surfaces through the send-error face with the server's
 * own words — the action carries `error: false`, so this is the one surface.
 */
async function steer(
  chatID: string,
  text: string,
  messageID: string,
  attachments: readonly unknown[],
  attachGen: number,
  convertBudget: number,
): Promise<SubmitResult> {
  const outcome = await steerChat.dispatch({
    chatID,
    text: withAttachmentPaths(text, attachments),
    messageID,
  }).outcome;
  if (outcome.status === "success") {
    lastFailed = undefined;
    return "steered";
  }
  if (outcome.status === "error" && outcome.error.code === "no_turn" && convertBudget > 0) {
    return prompt(chatID, text, messageID, attachments, attachGen, convertBudget - 1);
  }
  recordFailure(chatID, text, messageID, attachments, attachGen);
  if (outcome.status === "error") {
    reportSendRefused(outcome.error.message);
  }
  return "failed";
}

/** Fold attachment paths into the steer text.
 *
 * `_session/steer` takes a plain string, so there is no content block for a file
 * to ride in — a steer cannot carry an attachment the way a prompt can. Rather
 * than refuse the send or drop the files silently, this uses the SAME path
 * reference the server already falls back to for a document type KAS will not
 * accept inline ("Attached file: <path>", one per line, in
 * command/prompt_attachments.go). The agent opens them with its file tools.
 *
 * Matching that wording is the point: two formats for "here is a file" would
 * teach the model two conventions for one thing.
 */
function withAttachmentPaths(text: string, attachments: readonly unknown[]): string {
  const lines = attachments
    .map((a) => (a as AttachedFile).path)
    .filter((path) => typeof path === "string" && path !== "")
    .map((path) => `Attached file: ${path}`);
  return lines.length === 0 ? text : `${text}\n\n${lines.join("\n")}`;
}

/** Put a failed send back where the user left it and remember its id.
 *
 *  Two gates, and the first is the one that made a sent prompt look unsent.
 *
 *  A prompt whose user row the server ALREADY PERSISTED is not handed back to the
 *  composer. `CmdPrompt` appends and broadcasts that row before the ACP call and
 *  nothing rolls it back, so a turn that dies afterwards — a network fault at the
 *  model, a bridge exit, a cancelled context — leaves the message in the
 *  transcript and its text in the box at the same time. On this instance 40 of
 *  those POSTs answered 500 in three days, four of them inside 250ms, which is
 *  indistinguishable from an Enter that never cleared the box. The turn's failure
 *  is reported by the toast and the transcript's own divider; the composer is not
 *  a second channel for it. The echo test is the same one the dead-POST rescue in
 *  actions/chat.ts already trusts, applied to every failure rather than only to a
 *  connection death.
 *
 *  The id is still recorded whatever the gate decides, because a retype-and-resend
 *  must land on the row the failed attempt already persisted rather than a second
 *  copy of it.
 *
 *  What survives the gate is a prompt the server never took (a 400, a 413, a dead
 *  POST with no echo), and there the text and the attachment pills both go back —
 *  the prompt input clears itself the moment Send fires, so without this a refused
 *  send costs the message as well as the turn. */
function recordFailure(
  chatID: string,
  text: string,
  messageID: string,
  attachments: readonly unknown[],
  attachGen: number,
): void {
  lastFailed = { chatID, text, messageID };
  if (hasMessage(chatID, messageID)) {
    return;
  }
  // Named chat, not "the composer on screen": the send is asynchronous, so by the
  // time a failure lands the user may be looking at a different conversation.
  restoreFailedSend(chatID, text);
  for (const a of attachments) {
    const path = (a as AttachedFile).path;
    if (typeof path === "string" && path !== "") {
      // With the send's own generation, so a chat CLOSED in the meantime is not
      // handed its files back: the close forgot them on purpose, and a stash entry
      // written after it would reappear the next time the chat is opened.
      addAttachmentTo(chatID, path, attachGen);
    }
  }
}
