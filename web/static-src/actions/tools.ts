// Actions for tools.ts user-initiated mutations.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork } from "./index.js";
import { RETRY_STANDARD } from "./types.js";
import { MCP_API } from "./mcp.js";

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const installTools = apiAction<void, { output?: string; error?: string }>({
  name: "tools.install",
  scope: "tools",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "POST", path: "/api/tools/install" }),
  error: "Tool install failed",
});

export const saveTools = apiAction<Record<string, Record<string, Record<string, unknown>>>>({
  name: "tools.save",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  scope: "tools",
  idempotencyKey: true,
  request: (data) => ({ method: "PUT", path: "/api/tools", body: data }),
  error: "Couldn't save tool config",
});

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const runDiagnostics = apiAction<void, { report?: string; error?: string }>({
  name: "tools.run_diagnostics",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "POST", path: "/api/diagnostics", body: {} }),
  error: false,
});

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const loadTools = apiAction<void, Record<string, Record<string, Record<string, unknown>>>>({
  name: "tools.load",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: true,
  request: () => ({ method: "GET", path: "/api/tools" }),
  error: false,
});

export const seedMcp = apiAction<{ name: string; install?: string }>({
  name: "tools.seed_mcp",
  scope: "tools",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ name, install }) => ({
    method: "POST",
    path: MCP_API,
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
