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

/** Type predicate: true when `v` is a non-null object with a string
 *  `error` property. Replaces unsafe `as { error?: string }` casts on
 *  parsed JSON bodies throughout the action framework and api-client. */
export function hasErrorString(v: unknown): v is { error: string } {
  return typeof v === "object" && v !== null && "error" in v && typeof (v as Record<string, unknown>)["error"] === "string";
}

/** Coerce any thrown value into an ActionErrorLike snapshot. Used by
 *  the dispatcher when recording an instance to the registry. */
export function toActionError(e: unknown): ActionErrorLike {
  if (e instanceof ActionError) {
    return {
      message: e.message,
      ...(e.status !== undefined ? { status: e.status } : {}),
      ...(e.code !== undefined ? { code: e.code } : {}),
      ...(e.cause !== undefined ? { cause: e.cause } : {}),
    };
  }
  // DOMExceptions carry a meaningful name ('AbortError', 'TimeoutError',
  // 'NetworkError') that downstream classifiers rely on as a canonical code.
  // Map known names explicitly; lowercase fallback for others.
  if (e instanceof DOMException) {
    const code = e.name === "TimeoutError" ? "timeout"
               : e.name === "AbortError" ? "cancelled"
               : e.name === "NetworkError" ? "network"
               : e.name.toLowerCase();
    return { message: e.message, code, cause: e };
  }
  if (e instanceof Error) {
    const rawStatus = "status" in e ? (e as Record<string, unknown>)["status"] : undefined;
    const status = typeof rawStatus === "number" ? rawStatus : undefined;
    return { message: e.message, ...(status !== undefined ? { status } : {}), cause: e };
  }
  return { message: String(e), cause: e };
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
 *  4. Everything else → "network"
 */
export function classifyFetchError(e: unknown, signal: AbortSignal): ActionError {
  if (signal.aborted) {
    return new ActionError("Request cancelled", { code: "cancelled", cause: e });
  }
  if (e instanceof DOMException) {
    if (e.name === "TimeoutError") {
      return new ActionError("Request timed out", { code: "timeout", cause: e });
    }
    if (e.name === "AbortError") {
      return new ActionError("Request timed out", { code: "timeout", cause: e });
    }
  }
  const msg = e instanceof Error ? e.message : "network error";
  return new ActionError(msg, { code: "network", cause: e });
}
