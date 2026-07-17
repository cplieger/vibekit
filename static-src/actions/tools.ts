// Actions for the tools engine (Settings -> Tools).
//
// Every mutation is a fast request: the server enqueues a job on its
// single-flight queue and answers 202 with the job view; progress
// streams over the tool_job_changed / tool_job_output SSE events. No
// long-running requests, no extended timeouts, no retry hazards.
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
  IDEMPOTENCY_HEADER,
} from "./index.js";
import type { ActionContext } from "./index.js";
import type { Inventory, JobResponse, JobsResponse, SearchResponse } from "../types.js";

import { MCP_API } from "./mcp.js";

/** Fields accepted by POST /api/tools (create). Everything except the
 *  name is optional — the server fills source/version/description from
 *  the catalog when the name is known there. */
export interface CreateToolRequest {
  name: string;
  source?: string;
  version?: string;
  pin?: boolean;
  /** Add as a disabled template: recorded, not installed, no job. */
  disabled?: boolean;
  requires?: string[];
  description?: string;
  origin?: string;
  install?: string;
  uninstall?: string;
  probe?: string;
}

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
export const loadTools = apiAction<void, Inventory>({
  name: "tools.load",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: true,
  request: () => ({ method: "GET", path: "/api/tools" }),
  error: false,
});

export const createTool = apiAction<CreateToolRequest, JobResponse>({
  name: "tools.create",
  scope: "tools",
  idempotencyKey: true,
  request: (body) => ({ method: "POST", path: "/api/tools", body }),
  error: "Couldn't add tool",
});

export const installTool = apiAction<{ name: string }, JobResponse>({
  name: "tools.install",
  scope: "tools",
  idempotencyKey: true,
  request: ({ name }) => ({
    method: "POST",
    path: `/api/tools/${encodeURIComponent(name)}/install`,
    body: {},
  }),
  error: "Couldn't start install",
});

export const updateTools = apiAction<{ names?: string[] } | undefined, JobResponse>({
  name: "tools.update",
  scope: "tools",
  idempotencyKey: true,
  request: (body) => ({ method: "POST", path: "/api/tools/update", body: body ?? {} }),
  error: "Couldn't start update",
});

/** PATCH result: 202 + job (null when no work was needed); a 409
 *  has_dependents envelope (disabling a tool others require) resolves
 *  as a success payload so the caller can run the force-confirm flow. */
export interface PatchToolResult {
  job?: { id: string } | null;
  code?: string;
  dependents?: string[];
  error?: string;
}

/** PATCH fields. `disabled` is the enable/disable toggle: false→true
 *  uninstalls (keeps the template; may 409 with dependents unless
 *  force), true→false installs. A version change enqueues a reinstall. */
export const patchTool = apiAction<
  {
    name: string;
    version?: string;
    pin?: boolean;
    disabled?: boolean;
    force?: boolean;
    description?: string;
    install?: string;
    uninstall?: string;
  },
  PatchToolResult
>({
  name: "tools.patch",
  scope: "tools",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ name, ...body }) => ({
    method: "PATCH",
    path: `/api/tools/${encodeURIComponent(name)}`,
    body,
  }),
  decode: (data) => data ?? {},
  decodeError: (info) =>
    info.status === 409 ? { kind: "success", value: info.body ?? {} } : undefined,
  error: "Couldn't update tool",
});

// Delete needs the 409 has_dependents envelope for the cascade-confirm
// flow; apiAction's decodeError seam resolves that status as a success
// payload (info.body carries the parsed envelope) while every other
// failure keeps the default error mapping.
export interface DeleteToolResult {
  job?: { id: string };
  code?: string;
  dependents?: string[];
  error?: string;
}

const DELETE_TIMEOUT_MS = 30_000;

export const deleteTool = apiAction<{ name: string; force?: boolean }, DeleteToolResult>({
  name: "tools.delete",
  scope: "tools",
  idempotencyKey: true,
  request: ({ name, force }) => ({
    method: "DELETE",
    path: `/api/tools/${encodeURIComponent(name)}${force === true ? "?force=1" : ""}`,
  }),
  // Mirror the previous runner's `parsed ?? {}` so callers never see
  // undefined on an empty-body 2xx.
  decode: (data) => data ?? {},
  decodeError: (info) =>
    info.status === 409 ? { kind: "success", value: info.body ?? {} } : undefined,
  error: false, // 409 cascade is a normal flow, handled by the caller
});

// ensureTool: install-by-name for feature banners (forge CLIs, MCP's
// node runtime). Creates the tool from the catalog; when it already
// exists in the manifest (400), falls back to a plain (re)install.
// error: false — the banners render their own inline progress/errors.
async function runEnsure(
  args: { name: string },
  signal: AbortSignal,
  ctx?: ActionContext,
): Promise<JobResponse> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (ctx?.idempotencyKey !== undefined) {
    headers[IDEMPOTENCY_HEADER] = ctx.idempotencyKey;
  }
  const send = async (method: string, path: string, body: unknown): Promise<Response> => {
    try {
      return await fetch(path, {
        method,
        headers,
        body: JSON.stringify(body),
        signal: withTimeout(signal, DELETE_TIMEOUT_MS),
      });
    } catch (e) {
      throw classifyFetchError(e, signal);
    }
  };
  // Create from the catalog; already-manifested names 400 -> retry as a
  // plain install; a disabled template 409s -> enable it (PATCH), which
  // installs.
  let res = await send("POST", "/api/tools", { name: args.name });
  if (res.status === 400) {
    res = await send("POST", `/api/tools/${encodeURIComponent(args.name)}/install`, {});
  }
  if (res.status === 409) {
    res = await send("PATCH", `/api/tools/${encodeURIComponent(args.name)}`, { disabled: false });
  }
  let parsed: unknown;
  try {
    parsed = await res.json();
  } catch {
    parsed = undefined;
  }
  if (!res.ok) {
    const msg = hasErrorString(parsed) ? parsed.error : `HTTP ${String(res.status)}`;
    throw new ActionError(msg, { status: res.status });
  }
  return parsed ?? {};
}

export const ensureTool = defineAction<{ name: string }, JobResponse>({
  name: "tools.ensure",
  scope: "tools",
  idempotencyKey: true,
  run: runEnsure,
  error: false,
});

export const searchTools = apiAction<{ q: string }, SearchResponse>({
  name: "tools.search",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: true,
  request: ({ q }) => ({
    method: "GET",
    path: `/api/tools/search?q=${encodeURIComponent(q)}`,
  }),
  error: false,
});

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
export const getToolsJobs = apiAction<void, JobsResponse>({
  name: "tools.jobs",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: true,
  request: () => ({ method: "GET", path: "/api/tools/jobs" }),
  error: false,
});

export const cancelToolJob = apiAction<{ id: string }>({
  name: "tools.cancel_job",
  scope: "tools",
  idempotencyKey: true,
  request: ({ id }) => ({
    method: "POST",
    path: `/api/tools/jobs/${encodeURIComponent(id)}/cancel`,
    body: {},
  }),
  error: "Couldn't cancel job",
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

// Probe which well-known binary names exist on PATH. Used by the MCP
// add modal and Sources sub-tab to decide whether to show an inline
// "Setting up <feature>..." install banner before the user can proceed.
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type
export const getToolsStatus = apiAction<void, Record<string, boolean>>({
  name: "tools.status",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: true,
  request: () => ({ method: "GET", path: "/api/tools/status" }),
  error: false,
});
