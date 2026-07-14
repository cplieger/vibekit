// Actions for tools.ts user-initiated mutations.
// ---------------------------------------------------------------------------

import {
  apiAction,
  defineAction,
  ActionError,
  classifyFetchError,
  hasErrorString,
  withTimeout,
  retryNetwork,
  RETRY_STANDARD,
} from "./index.js";
import type { ActionContext } from "./index.js";

import { MCP_API } from "./mcp.js";

// Install/enable runs execute setup-tools.sh synchronously server-side —
// a cold Rust toolchain, clangd (~160 MB), or a JRE takes MINUTES. The
// framework's uniform 30s API timeout killed such installs mid-download
// (the abort cancels the request context server-side), and retryNetwork
// then re-POSTed, cancelling and restarting the run twice more (C1). So
// these two actions run a raw fetch with a 16-minute budget (matching the
// server's 15-minute script cap) and are never auto-retried.
const TOOLS_RUN_TIMEOUT_MS = 16 * 60 * 1000;

/** Same header apiAction sets from ctx.idempotencyKey (see git-changes.ts). */
const IDEMPOTENCY_HEADER = "Idempotency-Key";

/** POST a long-running tools mutation with the extended timeout, decoding
 *  the JSON body regardless of status. Non-2xx → ActionError with status
 *  (409 = install already in progress, surfaced verbatim). */
async function runToolsPost<T>(path: string, signal: AbortSignal, ctx?: ActionContext): Promise<T> {
  const headers: Record<string, string> = {};
  if (ctx?.idempotencyKey !== undefined) {
    headers[IDEMPOTENCY_HEADER] = ctx.idempotencyKey;
  }
  let res: Response;
  try {
    res = await fetch(path, {
      method: "POST",
      headers,
      signal: withTimeout(signal, TOOLS_RUN_TIMEOUT_MS),
    });
  } catch (e) {
    throw classifyFetchError(e, signal);
  }
  let parsed: unknown;
  try {
    parsed = await res.json();
  } catch (e) {
    if (signal.aborted) {
      throw classifyFetchError(e, signal);
    }
    parsed = undefined;
  }
  if (!res.ok) {
    const msg = hasErrorString(parsed) ? parsed.error : `HTTP ${String(res.status)}`;
    throw new ActionError(msg, { status: res.status });
  }
  return parsed as T;
}

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const installTools = defineAction<void, { output?: string; error?: string }>({
  name: "tools.install",
  scope: "tools",
  idempotencyKey: true,
  // NOT retryable: a "timeout" here means a 16-minute install budget was
  // exhausted; re-POSTing would cancel and restart the server-side run.
  run: (_args, signal, ctx) => runToolsPost("/api/tools/install", signal, ctx),
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
export const enableTool = defineAction<
  { section: string; name: string },
  { output?: string; error?: string; enabled_chain?: string[]; section?: string; name?: string }
>({
  name: "tools.enable",
  scope: "tools",
  idempotencyKey: true,
  // NOT retryable — see installTools: a retry cancels + restarts the
  // in-flight setup run for the same entry (the server's inflightInstalls
  // prior() cancel), turning one slow install into three killed ones.
  run: ({ section, name }, signal, ctx) =>
    runToolsPost(
      `/api/tools/${encodeURIComponent(section)}/${encodeURIComponent(name)}/enable`,
      signal,
      ctx,
    ),
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
