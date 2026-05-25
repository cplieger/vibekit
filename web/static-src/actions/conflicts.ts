// Actions for conflict-resolution user-initiated mutations.
// ---------------------------------------------------------------------------

import { defineAction } from "./define.js";
import { apiAction } from "./api.js";
import { ActionError } from "./error.js";
import { withTimeout } from "../api-client.js";
import type { Conflict } from "../conflicts.js";

interface OpenDiffArgs {
  chatID: string;
  path: string;
  expectedSha: string;
  actualSha: string;
  otherChat: string;
}

async function fetchBlob(chatID: string, sha: string, signal: AbortSignal): Promise<string> {
  if (sha === "") return "";
  try {
    const resp = await fetch(
      `/api/checkpoints/${encodeURIComponent(chatID)}/blob/${encodeURIComponent(sha)}`,
      { signal: withTimeout(signal, 15_000) },
    );
    if (!resp.ok) throw new ActionError("server returned non-ok", { status: resp.status });
    return await resp.text();
  } catch (e) {
    if (e instanceof ActionError) throw e;
    if (signal.aborted) throw new ActionError("cancelled", { code: "cancelled", cause: e });
    // Any DOMException (AbortError/TimeoutError) from the derived
    // timeout signal means the 15 s budget expired — the caller's
    // signal is still live so this is a timeout, not a user cancel.
    if (e instanceof DOMException) {
      throw new ActionError("Request timed out", { code: "timeout", cause: e });
    }
    throw new ActionError("network error", { cause: e });
  }
}

/** Open a side-by-side diff for a conflict chip. Fetches both blobs
 *  in parallel and opens the editor in diff mode. Errors toast as
 *  "Could not load file content for conflict diff". */
export const openConflictDiff = defineAction<OpenDiffArgs, void>({
  name: "conflicts.open_diff",
  retryable: "network",
  run: async (args, signal) => {
    const [expected, actual] = await Promise.all([
      fetchBlob(args.chatID, args.expectedSha, signal),
      fetchBlob(args.chatID, args.actualSha, signal),
    ]);
    // Dynamic import: keeps the editor-openers chain (which transitively
    // touches router.ts and `window`) out of this module's static
    // dependency graph, so node-environment tests can stub it.
    const { openFileDiff } = await import("../editor-openers.js");
    openFileDiff(args.path, expected, actual, {
      oldLabel: `chat ${args.otherChat} left`,
      newLabel: "this chat saw",
    });
  },
  error: "Could not load file content for conflict diff",
});

/** Fetch past conflicts for a chat. Background best-effort — fails
 *  silently so a network hiccup doesn't surface an error toast. */
export const loadConflictsAction = apiAction<string, { conflicts?: Conflict[] }>({
  name: "conflicts.load",
  retryable: "network",
  retry: { count: 2, delay: 300 },
  request: (chatID) => ({ method: "GET", path: `/api/checkpoints/${encodeURIComponent(chatID)}/conflicts` }),
  error: false,
});