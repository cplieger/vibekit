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
import { isThinking } from "./store.js";
import { steerChat } from "./actions/chat.js";
import { clearAgentDown } from "./send-state.js";
import { restorePromptText } from "./prompt-input.js";

export type SubmitResult = "sent" | "steered" | "failed";

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
    return steer(chatID, text, messageID, attachments, attachGen);
  }

  const result = await sendPromptTo(chatID, text, {
    messageID,
    ...(attachments.length > 0 ? { attachments } : {}),
  });
  if (result === "queued") {
    // 409 busy: a turn started underneath us. Steer into it.
    return steer(chatID, text, messageID, attachments, attachGen);
  }
  if (result === "failed") {
    recordFailure(chatID, text, messageID, attachments, attachGen);
  } else {
    lastFailed = undefined;
  }
  return result === "sent" ? "sent" : "failed";
}

/** Post one steer.
 *
 * No local record is kept. The chip appears when KAS's `steer_queued` frame
 * comes back, which is what makes the row correct on every device and after a
 * reconnect rather than only on the tab that typed it.
 */
async function steer(
  chatID: string,
  text: string,
  messageID: string,
  attachments: readonly unknown[],
  attachGen: number,
): Promise<SubmitResult> {
  const ok = await steerChat.dispatch({
    chatID,
    text: withAttachmentPaths(text, attachments),
    messageID,
  });
  if (ok === undefined) {
    recordFailure(chatID, text, messageID, attachments, attachGen);
    return "failed";
  }
  lastFailed = undefined;
  return "steered";
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
 *  The text goes in the box and the attachment pills back on the row, because
 *  the prompt input clears itself the moment Send fires (it cannot know the
 *  outcome yet) and without this a throttled turn costs the message as well as
 *  the turn. The id is kept so the retry lands on the row the failed attempt
 *  already persisted rather than a second copy of it. */
function recordFailure(
  chatID: string,
  text: string,
  messageID: string,
  attachments: readonly unknown[],
  attachGen: number,
): void {
  lastFailed = { chatID, text, messageID };
  restorePromptText(text);
  for (const a of attachments) {
    const path = (a as AttachedFile).path;
    if (typeof path === "string" && path !== "") {
      // Named chat, not "the active one": the send is asynchronous and
      // takeAttachments emptied the row when it started, so by the time a
      // failure lands the user may be looking at a different conversation.
      // addAttachment would hang these files off that one's prompt.
      //
      // With the send's own generation, so a chat CLOSED in the meantime is not
      // handed its files back: the close forgot them on purpose, and a stash entry
      // written after it would reappear the next time the chat is opened.
      addAttachmentTo(chatID, path, attachGen);
    }
  }
}
