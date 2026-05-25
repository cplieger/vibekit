// Actions for tools.ts user-initiated mutations.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";

export const installTools = apiAction<void, { output?: string; error?: string }>({
  name: "tools.install",
  request: () => ({ method: "POST", path: "/api/tools/install" }),
  error: "Tool install failed",
});

export const saveTools = apiAction<Record<string, Record<string, Record<string, unknown>>>, unknown>({
  name: "tools.save",
  retryable: "network",
  request: (data) => ({ method: "PUT", path: "/api/tools", body: data }),
  error: "Couldn't save tool config",
});

export const runDiagnostics = apiAction<void, { report?: string; error?: string }>({
  name: "tools.diagnostics",
  request: () => ({ method: "POST", path: "/api/diagnostics", body: {} }),
  error: false,
});

export const loadToolsListAction = apiAction<void, Record<string, Record<string, Record<string, unknown>>>>({
  name: "tools.load_list",
  retryable: "network",
  retry: { count: 2, delay: 300 },
  request: () => ({ method: "GET", path: "/api/tools" }),
  error: false,
});

export const seedMcp = apiAction<{ name: string; install?: string }, unknown>({
  name: "tools.seed_mcp",
  request: ({ name, install }) => ({
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
      ...(install !== undefined ? { install } : {}),
    },
  }),
  error: "Couldn't create MCP entry",
});
