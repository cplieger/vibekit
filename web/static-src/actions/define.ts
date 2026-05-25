// defineAction: the lifecycle runner. Takes an ActionDefinition,
// returns an Action whose dispatch() executes:
//
//   1. record instance as "pending"; run optimistic() if present
//   2. await run(args, signal)  (with auto-retry if def.retry is set)
//   3a. on success: record "success", fire success toast, return result
//   3b. on error:   record "error", call rollback() with the captured
//                   OptimisticOp + ActionError, fire error toast, return null
//   3c. on cancel:  record "cancelled", call rollback(), return null
//
// Cancellation: each dispatch creates an AbortController. Calling
// action.cancel() aborts every in-flight instance for that action.
// run() is expected to honour the signal; HTTP/transport adapters do
// this automatically.
//
// Scope serialization: when def.scope is set, dispatches with the same
// scope key serialize through a per-scope FIFO queue. The next entry
// only starts after the previous resolves (success/error/cancelled).
// Without scope, dispatches run in parallel.
//
// Auto-retry: when def.retry.count > 0 and the error is retry-class
// (matches def.retryable's classifier), the action transparently
// retries with exponential backoff before surfacing the error toast.
//
// Toast wiring: integrates with toast.ts. Defaults:
//   - success: NO toast unless `success` is set in the definition or
//     overridden via dispatch({ successMessage })
//   - error:   ALWAYS toast unless `error: false` is explicitly set.
//     Default error message is "<HumanName> failed: <serverMessage>".
//
// Per-dispatch hooks: opts.onSuccess / onError / onSettled fire AFTER
// the toast emission. Useful when a specific callsite needs to react
// without changing the action definition.
// ---------------------------------------------------------------------------

import { error as toastError, success as toastSuccess } from "../toast.js";
import { toActionError } from "./error.js";
import { record } from "./registry.js";
import { _registerAction } from "./cleanup.js";
import type {
  Action,
  ActionContext,
  ActionDefinition,
  ActionErrorLike,
  DispatchOptions,
  OptimisticOp,
  ToastSpec,
} from "./types.js";

let instanceCounter = 0;

function nextInstanceID(name: string): string {
  instanceCounter += 1;
  return `${name}#${String(instanceCounter)}`;
}

/** Header name used by apiAction when an idempotency key is generated.
 *  Servers can dedupe on this header; the value is a per-dispatch
 *  ULID-like string that survives across retries (so a retry of the
 *  same dispatch sends the same key). */
export const IDEMPOTENCY_HEADER = "Idempotency-Key";

/** Generate a ULID-ish idempotency key. Doesn't need true ULID
 *  ordering; just needs to be unique across dispatches and stable
 *  enough that a retry sends the same value. */
function newIdempotencyKey(): string {
  // 26-char base32 string: 12 hex from ts + 14 random base36. Cheap,
  // collision-resistant for our scale (up to ~36^14 unique keys per ms).
  const ts = Date.now().toString(36);
  const rnd = Math.random().toString(36).slice(2, 16).padEnd(14, "0");
  return `${ts}-${rnd}`;
}

/** Defensive JSON.stringify — falls back to String(args) on cycles
 *  or non-serializable values (DOM elements, functions). Used by
 *  the default dedupe key computation. */
function safeStringify(args: unknown): string {
  try { return JSON.stringify(args) ?? "undefined"; } catch { return String(args); }
}

/** Resolve a ToastSpec to its message string. Returns null when
 *  the spec is `false` (suppressed) or undefined and no fallback. */
function resolveToast<TArgs, TPayload>(
  spec: ToastSpec<TArgs, TPayload> | undefined,
  args: TArgs,
  payload: TPayload,
  fallback?: string,
): string | null {
  if (spec === false) return null;
  if (spec === undefined) return fallback ?? null;
  if (typeof spec === "string") return spec;
  return spec(args, payload);
}

/** Build a default error toast prefix from the action name. Converts
 *  "chat.delete" -> "Delete failed", "mcp.add_server" -> "Add server
 *  failed", "files.create_file" -> "Create file failed". Callers
 *  usually override via the `error` field. */
function defaultErrorPrefix(name: string): string {
  const parts = name.split(".");
  const tail = parts[parts.length - 1] ?? name;
  // Convert underscores/hyphens to spaces for readability, then
  // capitalise the first character only.
  const readable = tail.replace(/[_-]/g, " ");
  return readable.charAt(0).toUpperCase() + readable.slice(1) + " failed";
}

/** Per-scope FIFO chain. Each scope key maps to the tail of its
 *  serial-promise chain. New dispatches in that scope await the tail
 *  before starting their own work. Module-scope so all actions sharing
 *  the same scope key serialize together (e.g. two different settings
 *  actions can both use `scope: "settings"` to serialize against each
 *  other, not just within one action). */
const scopeChains = new Map<string, Promise<unknown>>();

/** In-flight dedupe map. dedupe-keyed dispatches that match an active
 *  in-flight key return the SAME promise as the original (no new
 *  optimistic fires, no second run() call). When the original
 *  resolves, the entry is removed so the next dispatch starts fresh.
 *
 *  The entry stores both the shared promise AND a mutable error/
 *  cancelled flag so deduped callers' onError callbacks receive the
 *  ACTUAL error from the original dispatch (not a synthetic stub).
 *  Populated by runOnce via the optional errorSink param. */
interface DedupeEntry {
  promise: Promise<unknown>;
  /** Set by runOnce when the original dispatch errors (NOT cancelled). */
  error?: ActionErrorLike;
  /** Set by runOnce when the original dispatch was cancelled. */
  cancelled?: boolean;
}
const dedupeInflight = new Map<string, DedupeEntry>();

/** Sleep helper for retry backoff. Cancellable via signal: rejects
 *  with an AbortError if the signal aborts during the wait, so the
 *  retry chain unwinds cleanly when action.cancel() fires mid-backoff. */
function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (signal.aborted) {
      reject(new DOMException("aborted", "AbortError"));
      return;
    }
    const t = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = (): void => {
      clearTimeout(t);
      reject(new DOMException("aborted", "AbortError"));
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

export function defineAction<TArgs, TResult>(
  def: ActionDefinition<TArgs, TResult>,
): Action<TArgs, TResult> {
  // Track in-flight controllers so action.cancel() can abort them.
  const inFlight = new Map<string, AbortController>();

  function dispatch(
    args: TArgs,
    opts: DispatchOptions<TArgs, TResult> = {},
  ): Promise<TResult | null> {
    // Dedupe: if a dispatch with a matching dedupe key is already
    // in flight, return its promise instead of starting a new one.
    // Different from scope (which queues sequentially) — dedupe
    // collapses to a single shared dispatch.
    const dedupeKey = computeDedupeKey(args);
    if (dedupeKey !== null) {
      const entry = dedupeInflight.get(dedupeKey);
      if (entry !== undefined) {
        // Wrap so the second caller's per-call callbacks fire with the
        // ACTUAL outcome of the original dispatch (not a synthetic
        // stub). entry.error / entry.cancelled are populated by
        // runOnce when the original dispatch settles.
        return (entry.promise as Promise<TResult | null>).then(
          (v) => {
            try {
              if (v !== null) {
                opts.onSuccess?.(v as TResult, args);
              } else if (entry.error !== undefined) {
                // Original errored; propagate the real error.
                opts.onError?.(entry.error, args);
              } else if (entry.cancelled === true) {
                // Original was cancelled. Per the contract documented
                // in DispatchOptions, onSettled fires for cancellation
                // but onError does not. Skip onError here.
              } else {
                // Defensive: should not happen — promise resolved null
                // without setting error or cancelled. Fall back to
                // synthetic.
                opts.onError?.({ message: "deduped dispatch did not succeed", code: "dedupe" }, args);
              }
            } finally {
              opts.onSettled?.(args);
            }
            return v;
          },
        );
      }
    }

    // Compute the scope key (if any) and queue behind the previous
    // entry in that scope. A scope is just a string identifier; two
    // different actions sharing the same string serialize together.
    const scopeKey =
      typeof def.scope === "function" ? def.scope(args)
      : typeof def.scope === "string" ? def.scope
      : null;

    // Create AbortController at dispatch time so cancel() reaches
    // scope-queued dispatches that haven't started runOnce yet.
    const ac = new AbortController();
    const id = nextInstanceID(def.name);
    inFlight.set(id, ac);

    // Create the dedupe entry up front so runOnce can populate its
    // error / cancelled fields when the original dispatch settles.
    // Deduped callers read these to fire their own callbacks with the
    // actual outcome of the original.
    const dedupeEntry: DedupeEntry | null = dedupeKey !== null ? { promise: undefined as unknown as Promise<unknown> } : null;

    let result: Promise<TResult | null>;
    if (scopeKey === null) {
      result = runOnce(args, opts, ac, id, dedupeEntry);
    } else {
      const prev = scopeChains.get(scopeKey) ?? Promise.resolve();
      const next = prev.then(() => runOnce(args, opts, ac, id, dedupeEntry));
      const tail = next.catch(() => {});
      scopeChains.set(scopeKey, tail);
      void next.finally(() => {
        if (scopeChains.get(scopeKey) === tail) scopeChains.delete(scopeKey);
      }).catch(() => {});
      result = next;
    }

    // Track in dedupe map until the dispatch resolves.
    if (dedupeKey !== null && dedupeEntry !== null) {
      dedupeEntry.promise = result;
      dedupeInflight.set(dedupeKey, dedupeEntry);
      void result.finally(() => {
        // Only delete if we're still the in-flight entry (defensive
        // against another dispatch having replaced us mid-flight,
        // though that shouldn't happen since we'd have returned the
        // existing entry first).
        if (dedupeInflight.get(dedupeKey) === dedupeEntry) {
          dedupeInflight.delete(dedupeKey);
        }
      });
    }

    return result;
  }

  /** Compute the dedupe key for a dispatch, or null if dedupe is off. */
  function computeDedupeKey(args: TArgs): string | null {
    const cfg = def.dedupe;
    if (cfg === undefined || cfg === false) return null;
    const argKey = typeof cfg === "function" ? cfg(args) : safeStringify(args);
    return `${def.name}::${argKey}`;
  }

  /** Single dispatch lifecycle: optimistic → run (with retry) →
   *  success / error / cancelled. Always resolves (never rejects). */
  function runOnce(
    args: TArgs,
    opts: DispatchOptions<TArgs, TResult>,
    ac: AbortController,
    id: string,
    dedupeEntry: DedupeEntry | null,
  ): Promise<TResult | null> {
    // If already cancelled while queued in scope chain, short-circuit.
    if (ac.signal.aborted) {
      const now = Date.now();
      // Mark the dedupe entry as cancelled so deduped callers' onError
      // doesn't fire (only onSettled does, per the contract).
      if (dedupeEntry !== null) dedupeEntry.cancelled = true;
      record({
        id, name: def.name, status: "cancelled", args,
        startedAt: now, completedAt: now,
      });
      inFlight.delete(id);
      opts.onSettled?.(args);
      return Promise.resolve(null);
    }

    const startedAt = Date.now();

    // Build the per-dispatch context. The idempotency key is generated
    // once here (not per retry) so retries of the same dispatch send
    // the same key and the server can dedupe.
    const idemKey =
      typeof def.idempotencyKey === "function" ? def.idempotencyKey(args)
      : def.idempotencyKey === true ? newIdempotencyKey()
      : null;
    const ctx: ActionContext = idemKey !== null
      ? { instanceID: id, idempotencyKey: idemKey }
      : { instanceID: id };

    let optOp: OptimisticOp | undefined;
    if (def.optimistic !== undefined) {
      try {
        optOp = def.optimistic(args);
      } catch (e) {
        // Optimistic mutation threw — record + rethrow-as-error path.
        // Skip run() entirely; nothing committed yet so no rollback
        // needed (the optimistic itself failed).
        const err = toActionError(e);
        if (dedupeEntry !== null) dedupeEntry.error = err;
        record({
          id, name: def.name, status: "error", args,
          startedAt, completedAt: Date.now(), error: err,
        });
        inFlight.delete(id);
        emitErrorToast(args, err, opts);
        opts.onError?.(err, args);
        opts.onSettled?.(args);
        return Promise.resolve(null);
      }
    }

    // Record as pending after optimistic ran successfully.
    record({
      id, name: def.name, status: "pending", args,
      startedAt,
    });

    return runWithRetry(args, ac.signal, ctx).then(
      (result) => {
        // Cancellation can race success — if the signal aborted,
        // treat as cancelled even if run() resolved. Most adapters
        // throw on abort, but be defensive.
        if (ac.signal.aborted) {
          if (dedupeEntry !== null) dedupeEntry.cancelled = true;
          record({
            id, name: def.name, status: "cancelled", args,
            startedAt, completedAt: Date.now(),
          });
          inFlight.delete(id);
          emitCancelled(args, optOp);
          opts.onSettled?.(args);
          return null;
        }
        record({
          id, name: def.name, status: "success", args,
          startedAt, completedAt: Date.now(), result,
        });
        inFlight.delete(id);
        emitSuccessToast(args, result, opts);
        opts.onSuccess?.(result, args);
        opts.onSettled?.(args);
        return result;
      },
      (e: unknown) => {
        const err = toActionError(e);
        // If aborted, classify as cancelled rather than error.
        const cancelled = ac.signal.aborted;
        const status = cancelled ? "cancelled" : "error";
        // Populate dedupe entry so deduped callers receive the actual
        // outcome (real error or cancellation flag) rather than a
        // synthetic stub.
        if (dedupeEntry !== null) {
          if (cancelled) dedupeEntry.cancelled = true;
          else dedupeEntry.error = err;
        }
        record({
          id, name: def.name, status, args,
          startedAt, completedAt: Date.now(),
          ...(cancelled ? {} : { error: err }),
        });
        inFlight.delete(id);
        // Rollback the optimistic mutation regardless of cancel/error.
        if (def.rollback !== undefined) {
          try {
            const rollbackErr = cancelled
              ? { message: "cancelled", code: "cancelled" }
              : err;
            def.rollback(args, optOp, rollbackErr);
          } catch (rollbackErr) {
            console.error(`[actions] rollback for ${def.name} threw`, rollbackErr);
          }
        }
        if (!cancelled) {
          emitErrorToast(args, err, opts);
          opts.onError?.(err, args);
        }
        opts.onSettled?.(args);
        return null;
      },
    );
  }

  /** Run with auto-retry on retry-class errors. Each attempt re-runs
   *  def.run() with the same args + signal + ctx. Optimistic does NOT
   *  re-fire — it stays applied across retries (the rollback only
   *  fires once retries are exhausted). Backoff: delay * factor^attempt,
   *  capped at 5000ms. Idempotency key in ctx is stable across retries. */
  async function runWithRetry(args: TArgs, signal: AbortSignal, ctx: ActionContext): Promise<TResult> {
    const cfg = def.retry;
    const maxAttempts = (cfg?.count ?? 0) + 1;
    const baseDelay = cfg?.delay ?? 0;
    const factor = cfg?.factor ?? 2;
    let attempt = 0;
    while (true) {
      try {
        return await def.run(args, signal, ctx);
      } catch (e) {
        attempt++;
        if (signal.aborted) throw e;
        if (attempt >= maxAttempts) throw e;
        // Only retry on retry-class errors per def.retryable's
        // classifier. Mirrors computeRetry's logic.
        const err = toActionError(e);
        if (!isRetryClass(err)) throw e;
        const wait = Math.min(baseDelay * Math.pow(factor, attempt - 1), 5000);
        try {
          await sleep(wait, signal);
        } catch {
          throw e; // signal aborted during backoff
        }
      }
    }
  }

  /** True if the error matches the action's `retryable` classifier
   *  AND so qualifies for auto-retry. Same logic that computes the
   *  manual Retry button visibility. */
  function isRetryClass(err: ActionErrorLike): boolean {
    const mode = def.retryable;
    if (mode === undefined || mode === false) return false;
    const isNetworkClass =
      err.status === 0 || err.code === "network" || err.code === "timeout";
    if (mode === "network") return isNetworkClass;
    return true; // "always"
  }

  function emitSuccessToast(
    args: TArgs,
    result: TResult,
    opts: DispatchOptions<TArgs, TResult>,
  ): void {
    if (opts.silent === true) return;
    try {
      const msg = opts.successMessage ?? resolveToast(def.success, args, result);
      if (msg !== null) toastSuccess(msg);
    } catch (e) {
      // Throwing in a success toast spec is silently dropped by design —
      // success toasts are non-critical and must never disrupt the caller.
      console.error(`[actions] emitSuccessToast for ${def.name} threw`, e);
    }
  }

  function emitErrorToast(
    args: TArgs,
    err: ActionErrorLike,
    opts: DispatchOptions<TArgs, TResult>,
  ): void {
    // Errors are user-facing by default; only `error: false` in the
    // definition suppresses, never the silent flag.
    // Access def.error from closure scope (not param) so all error paths share the same spec lookup.
    const spec = def.error;
    if (spec === false) return;
    const fallbackMsg = `${defaultErrorPrefix(def.name)}: ${err.message}`;
    // Compute retry once so both happy and fallback paths include it.
    const retry = computeRetry(args, err);
    try {
      let msg: string;
      if (opts.errorPrefix !== undefined) {
        msg = `${opts.errorPrefix}: ${err.message}`;
      } else if (typeof spec === "string") {
        msg = `${spec}: ${err.message}`;
      } else if (typeof spec === "function") {
        msg = spec(args, err);
      } else {
        msg = fallbackMsg;
      }
      toastError(msg, retry);
    } catch (e) {
      console.error(`[actions] emitErrorToast for ${def.name} threw`, e);
      toastError(fallbackMsg, retry);
    }
  }

  function computeRetry(args: TArgs, err: ActionErrorLike): { onClick: () => void } | undefined {
    if (!isRetryClass(err)) return undefined;
    // Snapshot args so mutations after dispatch don't corrupt retry.
    let frozenArgs: TArgs;
    try { frozenArgs = structuredClone(args); } catch { frozenArgs = args; }
    return {
      onClick: () => { void dispatch(frozenArgs); },
    };
  }

  function emitCancelled(
    args: TArgs,
    optOp: OptimisticOp | undefined,
  ): void {
    if (def.rollback !== undefined) {
      try {
        // Build a synthetic ActionError so rollback() has something
        // to inspect — handlers may want to know cancellation vs real
        // error. We mark this with code:"cancelled".
        def.rollback(args, optOp, { message: "cancelled", code: "cancelled" });
      } catch (e) {
        console.error(`[actions] rollback (cancellation) for ${def.name} threw`, e);
      }
    }
  }

  function cancel(): void {
    for (const ac of inFlight.values()) {
      ac.abort();
    }
    // Don't clear inFlight here — the .then(reject) handler will
    // remove entries as run() rejects.
  }

  const action: Action<TArgs, TResult> = {
    name: def.name,
    dispatch,
    cancel,
  };

  // Register with the global cleanup tracker so beforeunload/teardown
  // can cancel all in-flight instances of this action.
  _registerAction(action);

  return action;
}

/** Test-only: reset the instance counter for deterministic IDs.
 *  Also clears scope chains + dedupe map so a test can dispatch in
 *  a fresh state without serializing behind a previous test's chain. */
export function _resetForTest(): void {
  instanceCounter = 0;
  scopeChains.clear();
  dedupeInflight.clear();
}

/** Test-only: expose internal map sizes for leak verification. */
export function _internalsForTest(): { scopeChains: number; dedupeInflight: number } {
  return { scopeChains: scopeChains.size, dedupeInflight: dedupeInflight.size };
}
