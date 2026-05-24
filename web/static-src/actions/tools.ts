// Actions for tools.ts user-initiated mutations.
// ---------------------------------------------------------------------------

import { apiAction } from "./api.js";

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
