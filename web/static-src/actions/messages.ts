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

/** Accept or reject a pending supervised change. */
export const resolvePending = transportAction<{ chatID: string; toolCallID: string; action: "accept" | "reject" }>({
  name: "messages.resolve_pending",
  command: ({ chatID, toolCallID, action }) => ({
    type: "resolve_pending_change",
    chat_id: chatID,
    payload: { tool_call_id: toolCallID, action },
  }),
  error: (args, err) => `Failed to ${args.action} change: ${err.message}`,
});

/** Hand a plan to the running agent as a prompt. */
export const runPlanAction = defineAction<{ chatID: string; content: string }, void>({
  name: "plan.run",
  run: async ({ chatID, content }) => {
    const result = await sendPromptTo(chatID, `Please implement this plan:\n\n${content}`);
    if (result === "failed") {
      throw new ActionError("prompt rejected");
    }
  },
  error: "Failed to send plan",
});
