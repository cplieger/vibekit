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

// Enable a pre-populated entry: flips enabled=true on the named tool
// and every transitive dep, then runs setup-tools.sh. The response
// includes the chain that was enabled and the script's combined
// output for the rolling-output UI.
export const enableTool = apiAction<
  { section: string; name: string },
  { output?: string; error?: string; enabled_chain?: string[]; section?: string; name?: string }
>({
  name: "tools.enable",
  scope: "tools",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ section, name }) => ({
    method: "POST",
    path: `/api/tools/${encodeURIComponent(section)}/${encodeURIComponent(name)}/enable`,
  }),
  error: "Couldn't enable tool",
});

// Delete a tool: cancels any in-flight install for the entry, runs
// clear_tool for cleanup, sets enabled=false. Body { force: true }
// cascades to dependents; without force the call returns 409 with
// the dependents list so the UI can confirm.
export const deleteTool = apiAction<
  { section: string; name: string; force?: boolean },
  { output?: string; error?: string; disabled?: string[]; dependents?: string[]; code?: string }
>({
  name: "tools.delete",
  scope: "tools",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ section, name, force }) => ({
    method: "DELETE",
    path: `/api/tools/${encodeURIComponent(section)}/${encodeURIComponent(name)}`,
    body: force === undefined ? undefined : { force },
  }),
  error: false, // 409 cascade is a normal flow, handle in caller
});

// Toggle the per-tool auto_update flag.
export const patchTool = apiAction<
  { section: string; name: string; auto_update: boolean },
  { section?: string; name?: string; auto_update?: boolean; error?: string }
>({
  name: "tools.patch",
  scope: "tools",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ section, name, auto_update }) => ({
    method: "PATCH",
    path: `/api/tools/${encodeURIComponent(section)}/${encodeURIComponent(name)}`,
    body: { auto_update },
  }),
  error: "Couldn't update tool",
});

// Probe which baked-binary names exist on PATH. Used by the MCP add
// modal and Sources sub-tab to decide whether to show an inline
// "Setting up <feature>..." spinner before the user can proceed.
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type
export const getToolsStatus = apiAction<void, Record<string, boolean>>({
  name: "tools.status",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: true,
  request: () => ({ method: "GET", path: "/api/tools/status" }),
  error: false,
});

// Fire a slash command into a chat's live kiro-cli bridge. Used after
// enabling an LSP to run `/code init -f` so the already-running session
// re-scans PATH and picks up the newly-installed language server
// without waiting for a new chat. Best-effort: a missing bridge (no
// active chat) is a normal no-op, so errors are swallowed.
export const execSlash = apiAction<{ chatID: string; command: string }>({
  name: "tools.exec_slash",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ chatID, command }) => ({
    method: "POST",
    path: "/api/slash/execute",
    body: { chat_id: chatID, command },
  }),
  error: false,
});
