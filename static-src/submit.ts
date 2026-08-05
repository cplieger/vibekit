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
import { takeAttachments, addAttachment, type AttachedFile } from "./attachments.js";
import { isThinking } from "./store.js";
import { steerChat } from "./actions/chat.js";

export type SubmitResult = "sent" | "steered" | "failed";

/**
 * User-facing submit. Sends a prompt on an idle chat and a steer on a busy one.
 *
 * The 409 path matters as much as the idle check and is the reason the two are
 * not one branch: a turn can start between reading `thinking` and the POST
 * landing, and the server answers that with 409 busy. The old code enqueued
 * there; this steers instead, because the message belongs in the turn that just
 * started rather than in the one after it.
 *
 * On a hard failure the attachments go back to the input row so the user's files
 * are not silently dropped; the send button surfaces the error through
 * send-state.
 */
export async function submitPrompt(chatID: string, text: string): Promise<SubmitResult> {
  if (chatID === "" || text === "") {
    return "failed";
  }
  // Typed commands vibekit owns are intercepted BEFORE anything else: before the
  // attachments are taken and before a message id is minted. A command is not a
  // prompt, so it must not consume an attachment or leave a user bubble behind.
  if (handleTypedCommand(chatID, text)) {
    return "sent";
  }
  const attachments = takeAttachments();
  const messageID = newMessageID();

  if (isThinking(chatID)) {
    return steer(chatID, text, messageID, attachments);
  }

  const result = await sendPromptTo(chatID, text, {
    messageID,
    ...(attachments.length > 0 ? { attachments } : {}),
  });
  if (result === "queued") {
    // 409 busy: a turn started underneath us. Steer into it.
    return steer(chatID, text, messageID, attachments);
  }
  if (result === "failed") {
    restoreAttachmentsToInput(attachments);
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
): Promise<SubmitResult> {
  const ok = await steerChat.dispatch({
    chatID,
    text: withAttachmentPaths(text, attachments),
    messageID,
  });
  if (ok === undefined) {
    restoreAttachmentsToInput(attachments);
    return "failed";
  }
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

function restoreAttachmentsToInput(attachments: readonly unknown[]): void {
  for (const a of attachments) {
    const path = (a as AttachedFile).path;
    if (typeof path === "string" && path !== "") {
      addAttachment(path);
    }
  }
}
