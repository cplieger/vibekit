// ActionError: thrown by an action's run() function to signal a typed
// failure. Carries optional HTTP status, server-side error code, and
// cause chain for diagnostics. apiAction / transportAction wrappers
// normalise their failure shapes into this.
//
// Catching code that handles "any error" can use `instanceof ActionError`
// to discriminate user-actionable failures from infra exceptions.
// ---------------------------------------------------------------------------

import type { ActionErrorLike } from "./types.js";

/**
 * Structured error thrown from an action's `run()` to signal a typed failure.
 * Carries optional HTTP status and server-side error code for downstream
 * classification (retry eligibility, toast formatting, telemetry).
 *
 * @example
 * ```ts
 * throw new ActionError("Server rejected", { status: 409, code: "conflict" });
 * ```
 */
export class ActionError extends Error implements ActionErrorLike {
  readonly status?: number;
  readonly code?: string;
  override readonly cause?: unknown;

  constructor(
    message: string,
    opts?: { status?: number; code?: string; cause?: unknown },
  ) {
    super(message);
    this.name = "ActionError";
    if (opts?.status !== undefined) this.status = opts.status;
    if (opts?.code !== undefined) this.code = opts.code;
    if (opts?.cause !== undefined) this.cause = opts.cause;
  }
}

/** Type predicate: narrows `unknown` to ActionError. */
export function isActionError(e: unknown): e is ActionError {
  return e instanceof ActionError;
}

/** Type predicate: true when `v` is a non-null object with a string
 *  `error` property. Replaces unsafe `as { error?: string }` casts on
 *  parsed JSON bodies throughout the action framework and api-client. */
export function hasErrorString(v: unknown): v is { error: string } {
  if (typeof v !== "object" || v === null || !("error" in v)) return false;
  // After the `in` check, TS narrows `v` to `object & Record<"error", unknown>`.
  return typeof v.error === "string";
}

/** Coerce any thrown value into an ActionErrorLike snapshot. Used by
 *  the dispatcher when recording an instance to the registry.
 *
 *  Builds a minimal result object — only includes status/code/cause
 *  fields when they carry a defined value. */
export function toActionError(e: unknown): ActionErrorLike {
  if (e instanceof ActionError) {
    // Direct property assignment avoids temporary object allocation from
    // spread-based conditional inclusion (`...(x && { x })`). Measurable
    // in V8 microbenchmarks when toActionError sits on the error hot path.
    const r: { message: string; status?: number; code?: string; cause?: unknown } = { message: e.message };
    if (e.status !== undefined) r.status = e.status;
    if (e.code !== undefined) r.code = e.code;
    if (e.cause !== undefined) r.cause = e.cause;
    return r;
  }
  if (e instanceof DOMException) {
    const code = e.name === "TimeoutError" ? "timeout"
               : e.name === "AbortError" ? "cancelled"
               : e.name === "NetworkError" ? "network"
               : e.name.toLowerCase();
    // Network/timeout DOMExceptions get status: 0 to match the convention
    // established by classifyFetchError (browser convention for failed-to-connect).
    const isNetLayer = code === "network" || code === "timeout";
    return { message: e.message, code, ...(isNetLayer && { status: 0 }), cause: e };
  }
  if (e instanceof AggregateError) {
    // AggregateError from Promise.any / Promise.allSettled: surface the
    // first child error's message for readability, attach the aggregate
    // as cause so callers can inspect all errors if needed.
    const first = e.errors[0];
    const inner = first instanceof Error ? first.message : e.message;
    return { message: inner || e.message, code: "aggregate", cause: e };
  }
  if (e instanceof Error) {
    const rawStatus = "status" in e ? (e as { status: unknown }).status : undefined;
    const status = typeof rawStatus === "number" ? rawStatus : undefined;
    const rawCode = "code" in e ? (e as { code: unknown }).code : undefined;
    const code = typeof rawCode === "string" ? rawCode : undefined;
    const r: { message: string; status?: number; code?: string; cause: unknown } = { message: e.message, cause: e };
    if (status !== undefined) r.status = status;
    if (code !== undefined) r.code = code;
    return r;
  }
  if (typeof e === "object" && e !== null && "message" in e) {
    const obj = e as { message: unknown; status?: unknown; code?: unknown };
    const message = typeof obj.message === "string" ? obj.message : String(obj.message);
    const status = typeof obj.status === "number" ? obj.status : undefined;
    const code = typeof obj.code === "string" ? obj.code : undefined;
    const r: { message: string; status?: number; code?: string; cause: unknown } = { message, cause: e };
    if (status !== undefined) r.status = status;
    if (code !== undefined) r.code = code;
    return r;
  }
  if (e === null) return { message: "Unknown error (null thrown)", code: "unknown" };
  if (e === undefined) return { message: "Unknown error (undefined thrown)", code: "unknown" };
  const msg = String(e);
  return { message: msg !== "" ? msg : "Unknown error (empty value thrown)", code: "unknown", cause: e };
}

/**
 * Classify a caught fetch error into an ActionError with a canonical code.
 * Used by transportAction / apiAction wrappers to normalise network-layer
 * failures into retry-eligible ActionErrors.
 *
 * Classification priority:
 *  1. Signal already aborted → "cancelled" (user or framework cancelled)
 *  2. DOMException TimeoutError → "timeout"
 *  3. DOMException AbortError with live signal → "timeout" (AbortSignal.timeout)
 *  4. TypeError → "network" (browsers throw TypeError for network failures)
 *  5. Everything else → "network"
 */
export function classifyFetchError(e: unknown, signal: AbortSignal): ActionError {
  if (signal.aborted) {
    return new ActionError("Request cancelled", { code: "cancelled", cause: e });
  }
  if (e instanceof DOMException) {
    if (e.name === "TimeoutError") {
      return new ActionError("Request timed out", { status: 0, code: "timeout", cause: e });
    }
    if (e.name === "AbortError") {
      return new ActionError("Request timed out", { status: 0, code: "timeout", cause: e });
    }
  }
  if (e instanceof TypeError) {
    return new ActionError(e.message, { status: 0, code: "network", cause: e });
  }
  const msg = e instanceof Error ? e.message : "network error";
  return new ActionError(msg, { status: 0, code: "network", cause: e });
}

/** True when the error represents a network-layer failure: the request
 *  never reached the server or the connection was lost. Matches errors
 *  with code "network" or "timeout", or status === 0 (browser convention
 *  for failed-to-connect). Complements `isTransientStatus` (server-side
 *  transient) and `isPermanentCode` (never-retry). */
export function isNetworkError(err: ActionErrorLike): boolean {
  if (err.code === "network" || err.code === "timeout") return true;
  if (err.status === 0) return true;
  return false;
}

/** HTTP status codes that represent transient server-side conditions
 *  (rate-limiting, temporary unavailability). These qualify for retry
 *  under the "network" retryable mode because the server is expected
 *  to recover without client-side changes.
 *
 *  408: server closed connection waiting for the request (keep-alive timeout)
 *  429: rate-limited
 *  502/503/504: upstream unavailability or gateway timeout */
const TRANSIENT_STATUSES = new Set([408, 429, 502, 503, 504]);

/** True when the HTTP status code represents a transient condition
 *  eligible for retry (408 Request Timeout, 429 Too Many Requests,
 *  502 Bad Gateway, 503 Service Unavailable, 504 Gateway Timeout). */
export function isTransientStatus(status: number | undefined): boolean {
  return status !== undefined && TRANSIENT_STATUSES.has(status);
}

/** Codes that represent permanent failures — never retry regardless
 *  of the retryable mode. These indicate the operation was explicitly
 *  rejected or is semantically invalid. */
const PERMANENT_CODES = new Set(["cancelled", "send_failed", "clipboard", "unsupported", "server_rejected"]);

/** True when the error code represents a permanent failure that should
 *  never be retried (cancelled, send_failed, clipboard, unsupported,
 *  server_rejected). Used by external callers that need to distinguish
 *  permanent from transient failures for UI decisions. */
export function isPermanentCode(code: string | undefined): boolean {
  return code !== undefined && PERMANENT_CODES.has(code);
}

/**
 * Determine whether an error qualifies for retry under the given mode.
 *
 * - `undefined` / `false`: never retryable.
 * - `"network"`: retryable when the error is a network/timeout failure
 *   (code === "network" | "timeout", status === 0, or transient HTTP status).
 * - `"always"`: retryable for any error EXCEPT permanent failure codes.
 *
 * Used by:
 *  - `runWithRetry` to decide whether to auto-retry before surfacing the error.
 *  - `buildRetryButton` to decide whether to show the manual Retry button.
 */
export function isRetryableError(
  err: ActionErrorLike,
  mode: "network" | "always" | false | undefined,
): boolean {
  if (mode === undefined || mode === false) return false;
  // Permanent codes are never retryable regardless of mode.
  if (PERMANENT_CODES.has(err.code ?? "")) return false;
  if (mode === "always") return true;
  // mode === "network"
  if (isNetworkError(err)) return true;
  if (isTransientStatus(err.status)) return true;
  return false;
}
