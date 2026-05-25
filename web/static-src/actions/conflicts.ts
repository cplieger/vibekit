// Actions for conflict-resolution user-initiated mutations.
// ---------------------------------------------------------------------------

import { defineAction, apiAction, ActionError, classifyFetchError } from "./index.js";
import { RETRY_STANDARD } from "./types.js";
import { withTimeout } from "../api-client.js";
import type { Conflict } from "../conflicts.js";

interface OpenDiffArgs {
  chatID: string;
  path: string;
  expectedSha: string;
  actualSha: string;
  otherChat: string;
}

/** Fetch a single blob by SHA. Uses raw fetch because the response is
 *  plain text (not JSON), which apiAction's executeRequest doesn't
 *  support. Error classification is handled via classifyFetchError. */
async function fetchBlob(chatID: string, sha: string, signal: AbortSignal): Promise<string> {
  if (sha === "") return "";
  const path = `/api/checkpoints/${encodeURIComponent(chatID)}/blob/${encodeURIComponent(sha)}`;
  let resp: Response;
  try {
    resp = await fetch(path, { signal: withTimeout(signal, 15_000) });
  } catch (e) {
    throw classifyFetchError(e, signal);
  }
  if (!resp.ok) throw new ActionError("server returned non-ok", { status: resp.status });
  return resp.text();
}

/** Open a side-by-side diff for a conflict chip. Fetches both blobs
 *  in parallel and opens the editor in diff mode. Errors toast as
 *  "Could not load file content for conflict diff". */
export const openConflictDiff = defineAction<OpenDiffArgs, void>({
  name: "conflicts.open_diff",
  retryable: "network",
  retry: { count: 1, delay: 500 },
  dedupe: true,
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
export const loadConflicts = apiAction<string, { conflicts?: Conflict[] }>({
  name: "conflicts.load",
  retryable: "network",
  retry: RETRY_STANDARD,
  dedupe: (args) => args,
  request: (chatID) => ({ method: "GET", path: `/api/checkpoints/${encodeURIComponent(chatID)}/conflicts` }),
  error: false,
});