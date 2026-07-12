// Actions for conflict-resolution user-initiated mutations.
// ---------------------------------------------------------------------------

import {
  defineAction,
  apiAction,
  ActionError,
  classifyFetchError,
  retryNetwork,
  RETRY_STANDARD,
  withTimeout,
} from "./index.js";

import type { Conflict } from "../conflicts.js";

/** Timeout for individual blob fetches — shorter than API_TIMEOUT_MS (30s)
 *  because blobs are small payloads; 15s is a generous upper bound that
 *  avoids blocking the UI on a stalled connection. */
const BLOB_FETCH_TIMEOUT_MS = 15_000;

/** Base path for checkpoint API endpoints. */
export const API_CHECKPOINTS = "/api/checkpoints";

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
  if (sha === "") {
    return "";
  }
  const path = `${API_CHECKPOINTS}/${encodeURIComponent(chatID)}/blob/${encodeURIComponent(sha)}`;
  let resp: Response;
  try {
    resp = await fetch(path, { signal: withTimeout(signal, BLOB_FETCH_TIMEOUT_MS) });
  } catch (e) {
    throw classifyFetchError(e, signal);
  }
  if (!resp.ok) {
    throw new ActionError("server returned non-ok", { status: resp.status });
  }
  return resp.text();
}

/** Open a side-by-side diff for a conflict chip. Fetches both blobs
 *  in parallel and opens the editor in diff mode. Errors toast as
 *  "Could not load file content for conflict diff". */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const openConflictDiff = defineAction<OpenDiffArgs, void>({
  name: "conflicts.open_diff",
  retryable: retryNetwork,
  retry: { count: 1, delay: 500 },
  dedupe: true,
  run: async (args, signal) => {
    // The "expected" side (what the OTHER chat last wrote) is a blob owned
    // by that other chat — its SHA was never registered under the observing
    // chat's blobRefs, so fetching it under args.chatID 404s. Fetch it under
    // args.otherChat. The "actual" side (what this chat saw) stays under
    // args.chatID.
    const [expected, actual] = await Promise.all([
      fetchBlob(args.otherChat, args.expectedSha, signal),
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
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: (args) => args,
  request: (chatID) => ({
    method: "GET",
    path: `${API_CHECKPOINTS}/${encodeURIComponent(chatID)}/conflicts`,
  }),
  error: false,
});
