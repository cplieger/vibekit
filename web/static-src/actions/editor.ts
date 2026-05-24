// ---------------------------------------------------------------------------
// Actions: editor + diff pane user-initiated mutations.
// ---------------------------------------------------------------------------

import { apiAction, transportAction, defineAction, ActionError } from "./index.js";
import type { FileState } from "../editor-types.js";
import { routeForPath } from "../editor-types.js";

/** Save the active editor file (PUT). Toast on error. */
export const saveFile = apiAction<{ path: string; content: string }, { ok?: boolean; error?: string }>({
  name: "editor.save_file",
  request: ({ path, content }) => ({
    method: "PUT",
    path: routeForPath(path).writeURL,
    body: { content },
  }),
  error: "Save failed",
});

/** Send the active plan to the agent. Toast on failure. */
export const sendPlan = defineAction<{ chatID: string; content: string }, void>({
  name: "editor.send_plan",
  run: async ({ chatID, content }) => {
    const { writePlanDraft, runPlan } = await import("../plan-actions.js");
    if (!(await writePlanDraft(chatID, content))) {
      // writePlanDraft already shows a banner on failure.
      throw new ActionError("Could not save plan draft", { code: "draft_failed" });
    }
    await runPlan(chatID, content.trim());
  },
  error: "Couldn't send plan",
});

/** Resolve a pending change (accept/reject) via transport. */
export const resolvePending = transportAction<{ chatID: string; toolCallID: string; action: "accept" | "reject" }>({
  name: "editor.resolve_pending",
  command: ({ chatID, toolCallID, action }) => ({
    type: "resolve_pending_change",
    chat_id: chatID,
    payload: { tool_call_id: toolCallID, action },
  }),
  error: "Couldn't resolve change",
});

/** Apply partial (per-hunk) pending change via transport. */
export const resolvePendingPartial = transportAction<{ chatID: string; toolCallID: string; mergedText: string }>({
  name: "editor.resolve_pending_partial",
  command: ({ chatID, toolCallID, mergedText }) => ({
    type: "resolve_pending_change_partial" as const,
    chat_id: chatID,
    payload: { tool_call_id: toolCallID, merged_text: mergedText },
  }),
  error: "Couldn't apply partial change",
});

/** Fetch git diff sources for the editor diff view. Toast on failure. */
export const loadDiff = defineAction<{ state: FileState; repo: string; ref: string }, { oldContent: string; newContent: string; error: string }>({
  name: "editor.load_diff",
  run: async ({ state, repo, ref }, signal) => {
    const { apiGet } = await import("../api-client.js");
    const repoParam = repo !== "" ? `&repo=${encodeURIComponent(repo)}` : "";
    const [oldD, newD] = await Promise.all([
      apiGet<{ content?: string }>(
        `/api/git/show?path=${encodeURIComponent(state.path)}&ref=${encodeURIComponent(ref)}${repoParam}`, signal),
      apiGet<{ content?: string; error?: string }>(
        `/api/file?path=${encodeURIComponent(state.path)}`, signal),
    ]);
    if (signal.aborted) throw new ActionError("cancelled", { code: "cancelled" });
    if (oldD === null && newD === null) {
      throw new ActionError("Could not load diff");
    }
    return {
      oldContent: oldD?.content ?? "",
      newContent: newD?.content ?? "",
      error: newD?.error ?? "",
    };
  },
  error: "Could not load diff",
});
