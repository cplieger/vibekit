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
  // DOMExceptions carry a meaningful name ('AbortError', 'TimeoutError')
  // that downstream classifiers rely on as a code.
  if (e instanceof DOMException) {
    const code = e.name === "TimeoutError" ? "timeout"
               : e.name === "AbortError" ? "cancelled"
               : e.name.toLowerCase();
    return { message: e.message, code, cause: e };
  }
  if (e instanceof Error) {
    return { message: e.message, cause: e };
  }
  return { message: String(e), cause: e };
}
