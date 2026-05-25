// apiAction: factory for HTTP-backed actions. Wraps apiPostOrError /
// apiPutOrError / DELETE so the run() implementation is just the
// request descriptor. Surface errors are normalised into ActionError
// with HTTP status + parsed server message.
//
// The 90% case for user-initiated mutations:
//
//   const deleteFile = apiAction({
//     name: "files.delete",
//     request: (path: string) => ({ method: "DELETE", path: `/api/files?path=${encodeURIComponent(path)}` }),
//     error: "Couldn't delete",
//   });
//   await deleteFile.dispatch(somePath);
//
// For HTTP actions that also take an optimistic UI step:
//
//   const renameChat = apiAction({
//     name: "chat.rename",
//     request: ({ id, name }) => ({
//       method: "POST",
//       path: `/api/chats/${id}/rename`,
//       body: { name },
//     }),
//     optimistic: ({ id, name }) => {
//       const before = store.getChatName(id);
//       store.setChatName(id, name);
//       return { before };
//     },
//     rollback: ({ id }, op) => {
//       if (op?.before) store.setChatName(id, op.before);
//     },
//     error: "Couldn't rename chat",
//   });
// ---------------------------------------------------------------------------

import { withTimeout, API_TIMEOUT_MS } from "../api-client.js";
import { defineAction, IDEMPOTENCY_HEADER } from "./define.js";
import { ActionError } from "./error.js";
import type {
  Action,
  ActionContext,
  ActionDefinition,
  RequestSpec,
} from "./types.js";

const JSON_HEADERS = { "Content-Type": "application/json" };

/** Caller-facing shape of an apiAction definition. Differs from the
 *  raw ActionDefinition in that `request` replaces `run`. */
export interface ApiActionDefinition<TArgs, TResult, TOp = unknown>
  extends Omit<ActionDefinition<TArgs, TResult, TOp>, "run"> {
  /** HTTP request descriptor. Re-evaluated for each dispatch with the
   *  current args (so paths can interpolate args). */
  request: (args: TArgs) => RequestSpec;
}

/**
 * Build an Action from an HTTP request descriptor. The generated `run()`
 * handles fetch, non-ok status parsing, JSON decode, timeout/abort
 * classification, and throws {@link ActionError} on failure.
 *
 * @param def - API action definition where `request` replaces `run`.
 * @returns An {@link Action} backed by fetch with full lifecycle support.
 */
export function apiAction<TArgs, TResult = unknown, TOp = unknown>(
  def: ApiActionDefinition<TArgs, TResult, TOp>,
): Action<TArgs, TResult> {
  const { request, ...rest } = def;
  return defineAction<TArgs, TResult, TOp>({
    ...rest,
    run: async (args, signal, ctx) => {
      const spec = request(args);
      return executeRequest<TResult>(spec, signal, ctx);
    },
  });
}

/** Internal: execute an HTTP request and parse the result. Mirrors
 *  api-client.ts's request() shape but throws ActionError on failure
 *  rather than returning ApiResult, since the dispatcher expects
 *  exceptions to drive the error branch. The optional ctx carries
 *  per-dispatch metadata (e.g. idempotency key) populated by the
 *  framework. */
async function executeRequest<T>(
  spec: RequestSpec,
  signal: AbortSignal,
  ctx?: ActionContext,
): Promise<T> {
  const init: RequestInit = { method: spec.method };
  // Build headers: JSON content-type when there's a body, plus the
  // idempotency key if the framework generated one. Servers that
  // don't recognize Idempotency-Key ignore it harmlessly.
  const headers: Record<string, string> = {};
  if (spec.method !== "GET" && spec.body !== undefined) {
    Object.assign(headers, JSON_HEADERS);
    init.body = JSON.stringify(spec.body);
  }
  if (ctx?.idempotencyKey !== undefined) {
    headers[IDEMPOTENCY_HEADER] = ctx.idempotencyKey;
  }
  if (Object.keys(headers).length > 0) {
    init.headers = headers;
  }
  init.signal = withTimeout(signal, API_TIMEOUT_MS);
  let r: Response;
  try {
    r = await fetch(spec.path, init);
  } catch (e) {
    // Distinguish: user cancellation vs request timeout vs network.
    // The original `signal` is the caller's; `init.signal` is the
    // composed signal from withTimeout. If only the composed signal
    // aborted, the timeout fired — surface that as a typed error.
    if (signal.aborted) {
      throw new ActionError("cancelled", { code: "cancelled", cause: e });
    }
    if (e instanceof DOMException) {
      if (e.name === "TimeoutError") throw new ActionError("Request timed out", { code: "timeout", cause: e });
      if (e.name === "AbortError" && !signal.aborted) throw new ActionError("Request timed out", { code: "timeout", cause: e });
      // Otherwise rethrow as cancellation — the framework handles signal.aborted separately.
    }
    throw new ActionError(
      e instanceof Error ? e.message : "network error",
      { code: "network", cause: e },
    );
  }
  if (!r.ok) {
    // Try to parse a JSON error body for a server-supplied message.
    let serverError = "";
    try {
      const body = (await r.json()) as { error?: unknown };
      if (typeof body.error === "string") serverError = body.error;
    } catch {
      // Body wasn't JSON or parse failed — leave serverError empty.
    }
    throw new ActionError(
      serverError !== "" ? serverError : `HTTP ${String(r.status)}`,
      { status: r.status },
    );
  }
  // 204 No Content: no body to parse, regardless of method.
  if (r.status === 204) {
    // SAFETY: Callers that declare TResult as non-void must not issue
    // requests that return 204. The cast is unavoidable without a
    // runtime type guard; callers accept this by choosing TResult.
    return undefined as T;
  }
  // Parse JSON body. DELETE responses with bodies (e.g. confirmation
  // payload, remaining count) ARE parsed — only 204 short-circuits.
  // Empty body returns undefined.
  const text = await r.text();
  if (text === "") {
    if (spec.method !== "DELETE") {
      console.warn(`[actions] ${spec.method} ${spec.path} returned empty body — callers expecting data will receive undefined`);
    }
    return undefined as T; // same SAFETY note as above
  }
  try {
    return JSON.parse(text) as T;
  } catch (e) {
    throw new ActionError(
      `response not JSON: ${e instanceof Error ? e.message : String(e)}`,
      { status: r.status, cause: e },
    );
  }
}
