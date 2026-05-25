// ---------------------------------------------------------------------------
// Actions: messages, plan, clipboard (ui.copy_clipboard).
// ---------------------------------------------------------------------------

import { defineAction, apiAction, transportAction, ActionError } from "./index.js";
import { sendPromptTo } from "../chat-commands.js";

/** Copy text to clipboard with success/error toast. */
export const copyClipboard = defineAction<string, void>({
  name: "ui.copy_clipboard",
  run: async (text) => {
    try {
      await navigator.clipboard.writeText(text);
    } catch (e) {
      throw new ActionError("Clipboard unavailable", { cause: e });
    }
  },
  success: "Copied",
  error: "Couldn't copy",
});

/** Ask the utility bridge to explain a tool error. */
export const explainError = apiAction<{ errorText: string; context: string }, { output?: string }>({
  name: "messages.explain_error",
  request: ({ errorText, context }) => ({
    method: "POST",
    path: "/api/utility/explain-error",
    body: { error: errorText.slice(0, 2000), context },
  }),
  error: "Couldn't explain error",
  retryable: "network",
  retry: { count: 2, delay: 300 },
});

/** Undo a single file edit via checkpoint restore. */
export const undoEdit = transportAction<{ chatID: string; tag: string; filePath: string }>({
  name: "messages.undo_edit",
  command: ({ chatID, tag, filePath }) => ({
    type: "undo_edit",
    chat_id: chatID,
    payload: { tag, file_path: filePath },
  }),
  error: "Undo failed — the checkpoint may have expired",
});

/** Hand a plan to the running agent as a prompt.
 *  Note: sendPromptTo doesn't accept a signal, so cancellation is
 *  best-effort between calls (checked before sendPromptTo). */
export const runPlanAction = defineAction<{ chatID: string; content: string }, void>({
  name: "plan.run",
  run: async ({ chatID, content }, signal) => {
    if (signal.aborted) throw new ActionError("cancelled", { code: "cancelled" });
    const result = await sendPromptTo(chatID, `Please implement this plan:\n\n${content}`);
    if (result === "failed") {
      throw new ActionError("prompt rejected");
    }
  },
  error: "Failed to send plan",
});
