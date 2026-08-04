// ---------------------------------------------------------------------------
// Actions: editor + diff pane user-initiated mutations.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError, retryNetwork, RETRY_STANDARD } from "./index.js";

import { routeForPath } from "../editor-types.js";

/** Save the active editor file (PUT). Inline error surface in the editor pane;
 *  framework toast suppressed.
 *
 *  No auto-retry: content is captured at dispatch time. If the user types
 *  more after the first attempt fails, a retry would overwrite their new
 *  edits with the stale snapshot. The manual Retry button (retryable:
 *  "network") is kept so the user can consciously re-save, but auto-retry
 *  is intentionally omitted. */
export const saveFile = apiAction<
  { path: string; content: string },
  { ok?: boolean; error?: string }
>({
  name: "editor.save_file",
  scope: (args) => "file:" + args.path,
  retryable: retryNetwork,
  request: ({ path, content }) => ({
    method: "PUT",
    path: routeForPath(path).writeURL,
    body: { content },
  }),
  error: false,
});

// There is no per-hunk resolve action. KAS decides per ACTION, not per hunk
// (a multi-file rename shares one toolCallId), so a merged-text reply had
// nowhere to go. Editing after approval is the replacement.

/** Request AI conflict resolution suggestion. Inline error; no retry (not idempotent). */
export const suggestResolution = apiAction<
  { ours: string; theirs: string; context: string },
  { output?: string; error?: string }
>({
  name: "editor.suggest_resolution",
  // No dedupe: the per-file suggestionGen counter in editor-conflict.ts
  // already handles supersession (only the latest dispatch's result is
  // rendered). dedupe would also collapse same-args calls into one
  // promise, but rapid clicks on the same hunk are guarded earlier by
  // requestSuggestion's `existing?.loading` check.
  request: (body) => ({ method: "POST", path: "/api/utility/resolve-conflict", body }),
  error: false,
});

/** Fetch agent-modified line ranges for gutter highlighting. Retry on network failure. */
export const fetchAgentLines = apiAction<
  { chatID: string; path: string },
  { changes: { start_line: number; end_line: number }[] }
>({
  name: "editor.fetch_agent_lines",
  dedupe: (args) => JSON.stringify([args.chatID, args.path]),
  request: ({ chatID, path }) => ({
    method: "GET",
    path: `/api/file-changes?chat_id=${encodeURIComponent(chatID)}&path=${encodeURIComponent(path)}`,
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: false,
});

/** Fetch git diff sources for the editor diff view. Toast on failure. */
export const loadDiff = defineAction<
  { path: string; repo: string; ref: string },
  { oldContent: string; newContent: string; error: string }
>({
  name: "editor.load_diff",
  retryable: retryNetwork,
  run: async ({ path, repo, ref }, signal) => {
    const { apiGet } = await import("../api-client.js");
    const repoParam = repo !== "" ? `&repo=${encodeURIComponent(repo)}` : "";
    const [oldD, newD] = await Promise.all([
      apiGet<{ content?: string }>(
        `/api/git/show?path=${encodeURIComponent(path)}&ref=${encodeURIComponent(ref)}${repoParam}`,
        signal,
      ),
      apiGet<{ content?: string; error?: string }>(
        `/api/file?path=${encodeURIComponent(path)}`,
        signal,
      ),
    ]);
    if (signal.aborted) {
      throw new ActionError("cancelled", { code: "cancelled" });
    }
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
