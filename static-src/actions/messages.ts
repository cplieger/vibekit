// ---------------------------------------------------------------------------
// Actions: messages, plan, clipboard (ui.copy_clipboard).
// ---------------------------------------------------------------------------

import {
  defineAction,
  apiAction,
  ActionError,
  retryNetwork,
  RETRY_STANDARD,
  transportAction,
} from "./index.js";

import { submitPrompt } from "../prompt-queue.js";

/** Copy text to clipboard with success/error toast. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const copyClipboard = defineAction<string, void>({
  name: "ui.copy_clipboard",
  networkMode: "always",
  run: async (text) => {
    try {
      await navigator.clipboard.writeText(text);
    } catch (e) {
      throw new ActionError("Clipboard unavailable", { code: "clipboard", cause: e });
    }
  },
  success: "Copied",
  error: "Couldn't copy",
});

/** Ask the utility bridge to explain a tool error. */
export const explainError = apiAction<{ errorText: string; context: string }, { output?: string }>({
  name: "messages.explain_error",
  dedupe: (args) => args.errorText.slice(0, 100),
  request: ({ errorText, context }) => ({
    method: "POST",
    path: "/api/utility/explain-error",
    body: { error: errorText.slice(0, 2000), context },
  }),
  error: "Couldn't explain error",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Undo a single file edit via checkpoint restore. */
export const undoEdit = transportAction<{ chatID: string; tag: string; filePath: string }>({
  name: "messages.undo_edit",
  networkMode: "always",
  scope: (args) => "chat:" + args.chatID,
  idempotencyKey: (args) => `messages.undo_edit:${args.tag}:${args.filePath}`,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  command: ({ chatID, tag, filePath }) => ({
    type: "undo_edit",
    chat_id: chatID,
    payload: { tag, file_path: filePath },
  }),
  success: (args) =>
    `Undone edit to \u201c${args.filePath.split("/").pop() ?? args.filePath}\u201d`,
  error: "Undo failed — the checkpoint may have expired",
});

/** Hand a plan to the running agent as a prompt. Routes through the queue's
 *  submitPrompt so a plan sent while a turn is in flight is buffered and
 *  drained like any other prompt (returns "queued") rather than dropped.
 *  Returns the send status so the caller (plan-actions.runPlan) can delete the
 *  draft only on "sent" and keep it as the durable copy on "queued".
 *  Note: submitPrompt doesn't accept a signal, so cancellation is best-effort
 *  between calls (checked before the send). */
export const runPlan = defineAction<{ chatID: string; content: string }, "sent" | "queued">({
  name: "plan.run",
  scope: (args) => "chat:" + args.chatID,
  idempotencyKey: (args) => `plan.run:${args.chatID}:${args.content.slice(0, 40)}`,
  retryable: (err) => err.code === "send_failed" || retryNetwork(err),
  run: async ({ chatID, content }, signal) => {
    if (signal.aborted) {
      throw new ActionError("cancelled", { code: "cancelled" });
    }
    const result = await submitPrompt(chatID, `Please implement this plan:\n\n${content}`);
    if (result === "failed") {
      throw new ActionError("prompt rejected", { code: "send_failed" });
    }
    return result;
  },
  error: "Failed to send plan",
});
