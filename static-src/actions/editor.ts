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
export interface SaveFileResult {
  ok?: boolean;
  error?: string;
  /** Present only on a refused stale write: the file's CURRENT content, so the
   *  caller can show what changed rather than telling the user to reload. */
  content?: string;
  content_hash?: string;
}

export const saveFile = apiAction<
  { path: string; content: string; expectedHash?: string },
  SaveFileResult
>({
  name: "editor.save_file",
  scope: (args) => "file:" + args.path,
  retryable: retryNetwork,
  request: ({ path, content, expectedHash }) => ({
    method: "PUT",
    path: routeForPath(path).writeURL,
    // expected_hash is the digest the file had when this buffer loaded it. The
    // server refuses the write with 409 when the file changed since, which is
    // the routine case here rather than an exotic one: the agent writes to the
    // same tree the editor reads.
    body: expectedHash === undefined ? { content } : { content, expected_hash: expectedHash },
  }),
  // A 409 is the stale-write refusal, and its body carries the current content.
  // Resolved as a SUCCESS payload (the same seam tools.ts uses for its
  // has_dependents cascade) so the caller can branch on `error` and show the
  // difference; every other status keeps the default error mapping.
  decodeError: (info) =>
    info.status === 409 ? { kind: "success", value: info.body ?? {} } : undefined,
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
