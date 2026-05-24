// Actions for tools.ts and conflicts.ts user-initiated mutations.
// ---------------------------------------------------------------------------

import { apiAction } from "./api.js";
import { defineAction, } from "./define.js";
import { ActionError } from "./error.js";
import { withTimeout } from "../api-client.js";

// -- tools ------------------------------------------------------------------

export const installTools = apiAction<void, { output?: string; error?: string }>({
  name: "tools.install",
  request: () => ({ method: "POST", path: "/api/tools/install" }),
  error: "Tool install failed",
});

export const saveTools = apiAction<Record<string, Record<string, Record<string, unknown>>>, unknown>({
  name: "tools.save",
  request: (data) => ({ method: "PUT", path: "/api/tools", body: data }),
  error: "Couldn't save tool config",
});

export const seedMcp = apiAction<{ name: string; install: string }, unknown>({
  name: "tools.seed_mcp",
  request: ({ name }) => ({
    method: "POST",
    path: "/api/mcp",
    body: {
      name,
      transport: "stdio",
      enabled: false,
      prewarm: false,
      command: name,
      args: [],
      env: [],
    },
  }),
  error: "Couldn't create MCP entry",
});

// -- conflicts --------------------------------------------------------------

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
    throw new ActionError("network error", { cause: e });
  }
}

export const openConflictDiff = defineAction<OpenDiffArgs, void>({
  name: "conflicts.open_diff",
  run: async (args, signal) => {
    const [expected, actual] = await Promise.all([
      fetchBlob(args.chatID, args.expectedSha, signal),
      fetchBlob(args.chatID, args.actualSha, signal),
    ]);
    const { openFileDiff } = await import("../editor-openers.js");
    openFileDiff(args.path, expected, actual, {
      oldLabel: `chat ${args.otherChat} left`,
      newLabel: "this chat saw",
    });
  },
  error: "Could not load file content for conflict diff",
});
