// ---------------------------------------------------------------------------
// Actions: editor + diff pane user-initiated mutations.
// ---------------------------------------------------------------------------

import { apiAction, transportAction, defineAction, ActionError } from "./index.js";
import { routeForPath } from "../editor-types.js";

/** Save the active editor file (PUT). Inline error surface in the editor pane;
 *  framework toast suppressed. */
export const saveFile = apiAction<{ path: string; content: string }, { ok?: boolean; error?: string }>({
  name: "editor.save_file",
  request: ({ path, content }) => ({
    method: "PUT",
    path: routeForPath(path).writeURL,
    body: { content },
  }),
  error: false,
});

/** Send the active plan to the agent. writePlanDraft shows its own banner;
 *  framework toast suppressed.
 *  Note: writePlanDraft is not cancellable — once started it will complete
 *  regardless of signal state. Cancellation is checked between steps. */
export const sendPlan = defineAction<{ chatID: string; content: string }, void>({
  name: "editor.send_plan",
  run: async ({ chatID, content }, signal) => {
    const { writePlanDraft, runPlan } = await import("../plan-actions.js");
    if (!(await writePlanDraft(chatID, content))) {
      throw new ActionError("Could not save plan draft", { code: "draft_failed" });
    }
    if (signal.aborted) throw new ActionError("cancelled", { code: "cancelled" });
    const sent = await runPlan(chatID, content.trim());
    if (!sent) {
      throw new ActionError("Plan send failed", { code: "run_plan_failed" });
    }
  },
  error: false,
});

/** Apply partial (per-hunk) pending change via transport. */
export const resolvePendingPartial = transportAction<{ chatID: string; toolCallID: string; mergedText: string }>({
  name: "editor.resolve_pending_partial",
  command: ({ chatID, toolCallID, mergedText }) => ({
    type: "resolve_pending_change_partial",
    chat_id: chatID,
    payload: { tool_call_id: toolCallID, merged_text: mergedText },
  }),
  error: "Couldn't apply partial change",
});

/** Request AI conflict resolution suggestion. Inline error; no retry (not idempotent). */
export const suggestResolution = apiAction<{ ours: string; theirs: string; context: string }, { output?: string; error?: string }>({
  name: "editor.suggest_resolution",
  request: (body) => ({ method: "POST", path: "/api/utility/resolve-conflict", body }),
  error: false,
});

/** Fetch agent-modified line ranges for gutter highlighting. Retry on network failure. */
export const fetchAgentLinesAction = apiAction<{ chatID: string; path: string }, { changes: Array<{ start_line: number; end_line: number }> }>({
  name: "editor.fetch_agent_lines",
  request: ({ chatID, path }) => ({
    method: "GET",
    path: `/api/file-changes?chat_id=${encodeURIComponent(chatID)}&path=${encodeURIComponent(path)}`,
  }),
  retryable: "network",
  retry: { count: 2, delay: 300 },
  error: false,
});

/** Fetch git diff sources for the editor diff view. Toast on failure. */
export const loadDiff = defineAction<{ path: string; repo: string; ref: string }, { oldContent: string; newContent: string; error: string }>({
  name: "editor.load_diff",
  retryable: "network",
  run: async ({ path, repo, ref }, signal) => {
    const { apiGet } = await import("../api-client.js");
    const repoParam = repo !== "" ? `&repo=${encodeURIComponent(repo)}` : "";
    const [oldD, newD] = await Promise.all([
      apiGet<{ content?: string }>(
        `/api/git/show?path=${encodeURIComponent(path)}&ref=${encodeURIComponent(ref)}${repoParam}`, signal),
      apiGet<{ content?: string; error?: string }>(
        `/api/file?path=${encodeURIComponent(path)}`, signal),
    ]);
    if (signal.aborted) throw new ActionError("cancelled", { code: "cancelled" });
    if (oldD === null || newD === null) {
      throw new ActionError("Could not load base/new revision", { code: "network" });
    }
    return {
      oldContent: oldD.content ?? "",
      newContent: newD.content ?? "",
      error: newD.error ?? "",
    };
  },
  error: "Could not load diff",
});
