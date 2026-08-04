// ---------------------------------------------------------------------------
// Actions: messages, plan, clipboard (ui.copy_clipboard).
// ---------------------------------------------------------------------------

import { defineAction, apiAction, ActionError, retryNetwork, RETRY_STANDARD } from "./index.js";

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
