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
} from "./index.js";
import type { ActionContext } from "./index.js";
import type {
  ToolJobAccepted,
  ToolsJobsResponse,
  ToolsList,
  ToolsSearchResponse,
} from "../types.js";

import { MCP_API } from "./mcp.js";

/** Fields accepted by POST /api/tools (create). Everything except the
 *  name is optional — the server fills source/version/description from
 *  the catalog when the name is known there. */
export interface CreateToolRequest {
  name: string;
  source?: string;
  version?: string;
  pin?: boolean;
  requires?: string[];
  shims?: Record<string, string>;
  description?: string;
  origin?: string;
  install?: string;
  uninstall?: string;
  probe?: string;
}

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
export const loadTools = apiAction<void, ToolsList>({
  name: "tools.load",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: true,
  request: () => ({ method: "GET", path: "/api/tools" }),
  error: false,
});

export const createTool = apiAction<CreateToolRequest, ToolJobAccepted>({
  name: "tools.create",
  scope: "tools",
  idempotencyKey: true,
  request: (body) => ({ method: "POST", path: "/api/tools", body }),
  error: "Couldn't add tool",
});

export const installTool = apiAction<{ name: string }, ToolJobAccepted>({
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

export const updateTools = apiAction<{ names?: string[] } | undefined, ToolJobAccepted>({
  name: "tools.update",
  scope: "tools",
  idempotencyKey: true,
  request: (body) => ({ method: "POST", path: "/api/tools/update", body: body ?? {} }),
  error: "Couldn't start update",
});

/** PATCH fields; a version change flips the response to 202 + job. */
export const patchTool = apiAction<
  {
    name: string;
    version?: string;
    pin?: boolean;
    description?: string;
    install?: string;
    uninstall?: string;
  },
  ToolJobAccepted & { ok?: boolean }
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
  error: "Couldn't update tool",
});

// Delete needs the 409 has_dependents envelope, which apiAction can't
// surface (any non-2xx throws before the caller sees the body). The
// raw-fetch runner decodes the body on every status and only treats
// non-409 failures as errors. Raw fetch inside an action run() is the
// sanctioned pattern for non-standard wire contracts (see git-changes).
export interface DeleteToolResult {
  job?: { id: string };
  code?: string;
  dependents?: string[];
  error?: string;
}

/** Same header apiAction sets from ctx.idempotencyKey. */
const IDEMPOTENCY_HEADER = "Idempotency-Key";
const DELETE_TIMEOUT_MS = 30_000;

async function runDelete(
  args: { name: string; force?: boolean },
  signal: AbortSignal,
  ctx?: ActionContext,
): Promise<DeleteToolResult> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (ctx?.idempotencyKey !== undefined) {
    headers[IDEMPOTENCY_HEADER] = ctx.idempotencyKey;
  }
  let res: Response;
  try {
    res = await fetch(`/api/tools/${encodeURIComponent(args.name)}`, {
      method: "DELETE",
      headers,
      body: JSON.stringify(args.force === undefined ? {} : { force: args.force }),
      signal: withTimeout(signal, DELETE_TIMEOUT_MS),
    });
  } catch (e) {
    throw classifyFetchError(e, signal);
  }
  let parsed: unknown;
  try {
    parsed = await res.json();
  } catch {
    parsed = undefined;
  }
  if (res.ok || res.status === 409) {
    return parsed ?? {};
  }
  const msg = hasErrorString(parsed) ? parsed.error : `HTTP ${String(res.status)}`;
  throw new ActionError(msg, { status: res.status });
}

export const deleteTool = defineAction<{ name: string; force?: boolean }, DeleteToolResult>({
  name: "tools.delete",
  scope: "tools",
  idempotencyKey: true,
  run: runDelete,
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
): Promise<ToolJobAccepted> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (ctx?.idempotencyKey !== undefined) {
    headers[IDEMPOTENCY_HEADER] = ctx.idempotencyKey;
  }
  const post = async (path: string, body: unknown): Promise<Response> => {
    try {
      return await fetch(path, {
        method: "POST",
        headers,
        body: JSON.stringify(body),
        signal: withTimeout(signal, DELETE_TIMEOUT_MS),
      });
    } catch (e) {
      throw classifyFetchError(e, signal);
    }
  };
  let res = await post("/api/tools", { name: args.name });
  if (res.status === 400) {
    res = await post(`/api/tools/${encodeURIComponent(args.name)}/install`, {});
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

export const ensureTool = defineAction<{ name: string }, ToolJobAccepted>({
  name: "tools.ensure",
  scope: "tools",
  idempotencyKey: true,
  run: runEnsure,
  error: false,
});

export const searchTools = apiAction<{ q: string }, ToolsSearchResponse>({
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
export const getToolsJobs = apiAction<void, ToolsJobsResponse>({
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
