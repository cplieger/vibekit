// ---------------------------------------------------------------------------
// Prompt queue: the single owner of "a prompt was posted while a turn is in
// flight" behaviour.
//
// The server rejects a concurrent prompt on the same chat with HTTP 409
// (per-bridge TryLock). When that happens the prompt is buffered on the chat's
// reactive `prompt_queue` (store.ts) and drained one-per-turn on the SSE
// `turn_ended` (handlers/turn.ts). This module owns every decision around that
// buffer so the logic lives in one place instead of being smeared across the
// action, the turn handler, and the send helper:
//
//   submitPrompt      — user-facing send; enqueues on 409, re-checks idle.
//   drainNext         — failure-safe drain (peek → send → remove only on sent).
//   maybeDrainIfIdle  — drain when the chat is idle (race + gap recovery).
//   cancelQueuedPrompt— remove one queued entry (UI cancel affordance).
//
// Invariant it protects (vibekit.md #2/#6): a queued prompt never renders as a
// user bubble. It stays a pending-send that only becomes a real message once
// the server echoes `message_appended`. This module only ever calls
// sendPromptTo (which posts a command); it never touches the message list.
//
// Layering: prompt-queue → chat-commands (sendPromptTo) → actions/chat →
// transport. Nothing below imports this module, so there is no cycle.
// ---------------------------------------------------------------------------

import { sendPromptTo } from "./chat-commands.js";
import { handleTypedCommand } from "./typed-commands.js";
import { newMessageID } from "./transport.js";
import { takeAttachments, addAttachment, type AttachedFile } from "./attachments.js";
import {
  get,
  isThinking,
  enqueuePrompt,
  peekPrompt,
  dequeuePrompt,
  removeQueuedAt,
  queueLength,
} from "./store.js";
import { showBanner } from "./banner-stack.js";
import type { QueuedPrompt } from "./types.js";

export type SubmitResult = "sent" | "queued" | "failed";

// Chats with a drain currently awaiting a server response. Guards against a
// second trigger (turn_ended replay, a gap fired mid-drain) starting a
// re-entrant drainNext before the first send resolves.
const draining = new Set<string>();

/**
 * User-facing prompt submit. Owns the queue decision:
 *  - If prompts are already queued, append behind them so FIFO order holds
 *    instead of racing ahead of pending work.
 *  - Otherwise send once. A 409 means a turn is in flight: enqueue, then
 *    re-check idle to close the race where the 409 lands *after* `turn_ended`
 *    already drained an empty queue (otherwise the prompt strands forever).
 *  - On a hard failure the attachments are restored to the input row so the
 *    user's files aren't silently dropped (the send button surfaces the error
 *    via send-state).
 */
export async function submitPrompt(chatID: string, text: string): Promise<SubmitResult> {
  if (chatID === "" || text === "") {
    return "failed";
  }
  // Typed commands vibekit owns are intercepted BEFORE anything else: before the
  // attachments are taken, before a message id is minted, and before the queue.
  // A command is not a prompt, so it must not consume an attachment, occupy a
  // queue slot, or leave a user bubble behind.
  if (handleTypedCommand(chatID, text)) {
    return "sent";
  }
  const attachments = takeAttachments();
  // One message id per user submit, minted HERE so the queue entry and
  // every (re-)send share it: the server appends by id idempotently, so
  // a 409'd first attempt (which already persisted the bubble) and the
  // later drain can never render the prompt twice.
  const messageID = newMessageID();

  // Something already queued → queue behind it; the drain sends in order.
  if (queueLength(chatID) > 0) {
    enqueuePrompt(chatID, text, messageID, attachments);
    maybeDrainIfIdle(chatID);
    return "queued";
  }

  const result = await sendPromptTo(chatID, text, {
    messageID,
    ...(attachments.length > 0 ? { attachments } : {}),
  });
  if (result === "queued") {
    enqueuePrompt(chatID, text, messageID, attachments);
    maybeDrainIfIdle(chatID);
  } else if (result === "failed") {
    restoreAttachmentsToInput(attachments);
  }
  return result;
}

/**
 * Drain the front of the queue, if any. Failure-safe: the entry is peeked
 * (not removed) and only dropped once the server accepts it, so a re-send that
 * fails or 409s again is never lost.
 *
 *  - sent   → remove the entry; the turn we just started drives the next drain
 *             via its own `turn_ended`.
 *  - queued → a turn is genuinely in flight again (a re-409 race); leave the
 *             entry at the front for that turn's `turn_ended` to re-drain. No
 *             duplicate is created because sendPromptTo never enqueues.
 *  - failed → keep the entry queued and surface a dismissible banner. We do
 *             NOT auto-retry here (that would hot-loop against a down server):
 *             the entry stays visible + cancelable and re-drains on the next
 *             real turn boundary or the next user submit.
 */
export function drainNext(chatID: string): void {
  if (draining.has(chatID)) {
    return;
  }
  const next = peekPrompt(chatID);
  if (next === undefined) {
    return;
  }
  draining.add(chatID);
  const model = get(chatID)?.model;
  void sendPromptTo(chatID, next.text, {
    messageID: next.messageId,
    ...(model !== undefined && model !== "" ? { model } : {}),
    ...(next.attachments.length > 0 ? { attachments: next.attachments } : {}),
  })
    .then((result) => {
      if (result === "sent") {
        dequeuePrompt(chatID);
      } else if (result === "failed") {
        showBanner(
          chatID,
          "prompt_queue_send_failed",
          "A queued prompt couldn't be sent. It's still queued — send again to retry, or remove it.",
          "warning",
          true,
        );
      }
      // "queued": leave the entry in place for the in-flight turn to re-drain.
    })
    .finally(() => {
      draining.delete(chatID);
    });
}

/**
 * Drain the front entry only when the chat is idle (no turn in flight). Called
 * after an enqueue (to close the 409-after-turn_ended race) and after a
 * reconnect gap clears the thinking flag, so a queued prompt on a now-idle
 * chat doesn't strand waiting for a `turn_ended` that will never arrive.
 */
export function maybeDrainIfIdle(chatID: string): void {
  if (queueLength(chatID) === 0 || isThinking(chatID)) {
    return;
  }
  drainNext(chatID);
}

/**
 * Cancel a queued prompt (UI cancel affordance). Removes the entry at `index`
 * and returns it so the caller can restore its text + attachments to the
 * input. Returns undefined when the index is out of range.
 */
export function cancelQueuedPrompt(chatID: string, index: number): QueuedPrompt | undefined {
  return removeQueuedAt(chatID, index);
}

function restoreAttachmentsToInput(attachments: readonly unknown[]): void {
  for (const a of attachments) {
    const path = (a as AttachedFile).path;
    if (typeof path === "string" && path !== "") {
      addAttachment(path);
    }
  }
}
