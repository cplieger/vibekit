// defineAction: the lifecycle runner. Takes an ActionDefinition,
// returns an Action whose dispatch() executes:
//
//   1. record instance as "pending"; run optimistic() if present
//   2. await run(args, signal)  (with auto-retry if def.retry is set)
//   3a. on success: record "success", fire success toast, return result
//   3b. on error:   record "error", call rollback() with the captured
//                   TOp + ActionError, fire error toast, return null
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
import { toActionError, isRetryableError } from "./error.js";
import { record } from "./registry.js";
import { _registerAction } from "./cleanup.js";
import type {
  Action,
  ActionContext,
  ActionDefinition,
  ActionErrorLike,
  ActionInstance,
  DispatchOptions,
  RetryAttemptInfo,
  ToastSpec,
} from "./types.js";


let instanceCounter = 0;

/** Invoke a lifecycle hook safely — errors are caught and logged without
 *  disrupting the dispatch lifecycle. Eliminates repetitive try/catch
 *  blocks throughout runOnce (8+ occurrences → 1 helper). */
function invokeLifecycleHook(actionName: string, hookName: string, fn: () => void): void {
  try { fn(); } catch (e) {
    console.error(`[actions] ${hookName} callback for ${actionName} threw`, e);
  }
}

/** Shared empty options object to avoid allocating {} on every dispatch call.
 *  All DispatchOptions fields are optional, so the empty object satisfies any
 *  concrete instantiation. The cast is narrower than `any`: it only widens
 *  the type-parameter slots while preserving the structural shape. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const EMPTY_OPTS: DispatchOptions<any, any> = Object.freeze({});

/** Shared no-op for .catch() handlers on fire-and-forget promises.
 *  Avoids allocating a new `() => {}` closure on every scoped dispatch. */
const NOOP = (): void => {};

/** Generate a monotonically-increasing instance ID for registry tracking.
 *  Format: `"<actionName>#<counter>"`. Not globally unique across page
 *  reloads — only unique within a single page session. */
function nextInstanceID(name: string): string {
  instanceCounter += 1;
  return `${name}#${String(instanceCounter)}`;
}

/** Header name used by apiAction when an idempotency key is generated.
 *  Servers can dedupe on this header; the value is a per-dispatch
 *  ULID-like string that survives across retries (so a retry of the
 *  same dispatch sends the same key). */
export const IDEMPOTENCY_HEADER = "Idempotency-Key";

/** Generate a unique idempotency key. Doesn't need true ULID
 *  ordering; just needs to be unique across dispatches and stable
 *  enough that a retry sends the same value (generated once per
 *  dispatch, not per retry). */
function newIdempotencyKey(): string {
  // Format: base36 timestamp + "-" + 14-char random base36 suffix.
  // Collision-resistant for our scale (up to ~36^14 unique keys per ms).
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
  promise: Promise<unknown> | undefined;
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
  if (signal.aborted) return Promise.reject(new DOMException("aborted", "AbortError"));
  if (ms <= 0) return Promise.resolve();
  return new Promise<void>((resolve, reject) => {
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

/** Attach attempt count to a thrown error so the catch block in runOnce
 *  can record it in the registry. Uses a non-enumerable property to avoid
 *  polluting serialization. Safe against frozen/sealed objects — silently
 *  skips if the property can't be defined (the attempt count is best-effort
 *  metadata, not critical to the error flow). */
function attachAttempts(e: unknown, attempts: number): void {
  if (typeof e === "object" && e !== null) {
    try {
      Object.defineProperty(e, "_attempts", { value: attempts, configurable: true });
    } catch { /* frozen/sealed object — skip */ }
  }
}

/** Read the attempt count attached by runWithRetry, or undefined. */
function readAttempts(e: unknown): number | undefined {
  try {
    if (typeof e === "object" && e !== null && "_attempts" in e) {
      const val = (e as { _attempts: unknown })._attempts;
      return typeof val === "number" ? val : undefined;
    }
  } catch { /* Proxy or getter threw — skip */ }
  return undefined;
}

/**
 * Create an action from a declarative definition. The returned action
 * manages the full lifecycle: optimistic UI → run (with optional retry)
 * → success/error/cancel, including toast emission, registry recording,
 * scope serialization, and dedupe collapsing.
 *
 * @param def - Declarative action definition (name + run are required;
 *   all other fields opt into framework features).
 * @returns An {@link Action} whose `dispatch()` executes the lifecycle
 *   and `cancel()` aborts all in-flight instances.
 *
 * @example
 * ```ts
 * const deleteChat = defineAction<string, void>({
 *   name: "chat.delete",
 *   run: async (id, signal) => {
 *     await fetch(`/api/chats/${id}`, { method: "DELETE", signal });
 *   },
 *   error: "Couldn't delete chat",
 *   retryable: "network",
 * });
 * await deleteChat.dispatch(chatId);
 * ```
 */
export function defineAction<TArgs, TResult, TOp = unknown>(
  def: ActionDefinition<TArgs, TResult, TOp>,
): Action<TArgs, TResult> {
  // Track in-flight controllers so action.cancel() can abort them.
  const inFlight = new Map<string, AbortController>();
  // Track which instances have entered runOnce. Instances NOT in this
  // set are still scope-queued. cancel() eagerly removes scope-queued
  // instances from inFlight so isInflight reflects cancellation
  // immediately rather than waiting for the scope chain to advance.
  const started = new Set<string>();
  // Per-instance scope-skip resolvers. When cancel() fires for a
  // scope-queued instance, it triggers the resolver so the scope chain
  // tail resolves immediately (unblocking subsequent entries).
  const scopeSkipResolvers = new Map<string, () => void>();
  // Track active dedupe keys for this action so cancel() can eagerly
  // clear them from the module-level dedupeInflight map. Without this,
  // a cancel() + immediate re-dispatch with the same dedupe key would
  // collapse onto the cancelled promise instead of starting fresh.
  const activeDedupeKeys = new Set<string>();

  function dispatch(
    args: TArgs,
    opts: DispatchOptions<TArgs, TResult> = EMPTY_OPTS,
  ): Promise<TResult | null> {
    // Dedupe: if a dispatch with a matching dedupe key is already
    // in flight, return its promise instead of starting a new one.
    // Different from scope (which queues sequentially) — dedupe
    // collapses to a single shared dispatch.
    const dedupeKey = dedupeKeyFor(args);
    if (dedupeKey !== null) {
      const entry = dedupeInflight.get(dedupeKey);
      if (entry !== undefined) {
        // Wrap so the second caller's per-call callbacks fire with the
        // ACTUAL outcome of the original dispatch (not a synthetic
        // stub). entry.error / entry.cancelled are populated by
        // runOnce when the original dispatch settles.
        const shared = entry.promise;
        if (shared === undefined) {
          if (opts.onSettled) invokeLifecycleHook(def.name, "onSettled", () => opts.onSettled!(args));
          return Promise.resolve(null);
        }
        return (shared as Promise<TResult | null>).then(
          (v) => {
            if (v !== null) {
              if (opts.onSuccess) invokeLifecycleHook(def.name, "onSuccess", () => opts.onSuccess!(v, args));
            } else if (entry.error !== undefined) {
              const capturedErr = entry.error;
              if (opts.onError) invokeLifecycleHook(def.name, "onError", () => opts.onError!(capturedErr, args));
            } else if (entry.cancelled === true) {
              // Original was cancelled — fire onCancel (not onError).
              if (opts.onCancel) invokeLifecycleHook(def.name, "onCancel", () => opts.onCancel!(args));
            } else {
              if (opts.onError) invokeLifecycleHook(def.name, "onError", () => opts.onError!({ message: "deduped dispatch did not succeed", code: "dedupe" }, args));
            }
            if (opts.onSettled) invokeLifecycleHook(def.name, "onSettled", () => opts.onSettled!(args));
            return v;
          },
          () => {
            // Defensive: runOnce never rejects, but guarantee onSettled fires.
            if (opts.onSettled) invokeLifecycleHook(def.name, "onSettled", () => opts.onSettled!(args));
            return null;
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
    const dispatchedAt = Date.now();

    // Create the dedupe entry up front so runOnce can populate its
    // error / cancelled fields when the original dispatch settles.
    // Deduped callers read these to fire their own callbacks with the
    // actual outcome of the original. The promise field is assigned
    // immediately below (before the entry is visible in dedupeInflight
    // via the set() call after this block).
    const dedupeEntry: DedupeEntry | null = dedupeKey !== null ? { promise: undefined } : null;

    let result: Promise<TResult | null>;
    if (scopeKey === null) {
      result = runOnce(args, opts, ac, id, dedupeEntry, dedupeKey, dispatchedAt);
    } else {
      const prev = scopeChains.get(scopeKey) ?? Promise.resolve();
      const next = prev.then(() => runOnce(args, opts, ac, id, dedupeEntry, dedupeKey, dispatchedAt));
      // The tail is what subsequent scope entries wait on. It resolves
      // when either: (a) next settles (normal path), or (b) cancel()
      // triggers the skip resolver (cancelled-while-queued path).
      let tailResolve!: () => void;
      const tail = new Promise<void>((r) => { tailResolve = r; });
      scopeSkipResolvers.set(id, tailResolve);
      void next.then(tailResolve, tailResolve);
      scopeChains.set(scopeKey, tail);
      // Cleanup: delete the scope chain entry when this is the last
      // entry. Use next.finally (not tail.then) to preserve the same
      // microtick timing as the original code.
      void next.finally(() => {
        scopeSkipResolvers.delete(id);
        if (scopeChains.get(scopeKey) === tail) scopeChains.delete(scopeKey);
      }).catch(NOOP);
      result = next;
    }

    // Track in dedupe map until the dispatch resolves.
    if (dedupeKey !== null && dedupeEntry !== null) {
      dedupeEntry.promise = result;
      dedupeInflight.set(dedupeKey, dedupeEntry);
      activeDedupeKeys.add(dedupeKey);
      void result.finally(() => {
        // Only delete if we're still the in-flight entry (defensive
        // against another dispatch having replaced us mid-flight,
        // though that shouldn't happen since we'd have returned the
        // existing entry first).
        if (dedupeInflight.get(dedupeKey) === dedupeEntry) {
          dedupeInflight.delete(dedupeKey);
          activeDedupeKeys.delete(dedupeKey);
        }
      });
    }

    return result;
  }

  /** Compute the dedupe key for a dispatch, or null if dedupe is off.
   *  When a key is returned and matches an in-flight entry in the
   *  module-level `dedupeInflight` map, the framework collapses the
   *  new dispatch onto the existing promise (no second run() call,
   *  no duplicate optimistic mutation). The key is scoped by action
   *  name so different actions with identical args don't collide. */
  function dedupeKeyFor(args: TArgs): string | null {
    const cfg = def.dedupe;
    if (cfg === undefined || cfg === false) return null;
    const argKey = typeof cfg === "function" ? cfg(args) : safeStringify(args);
    return `${def.name}::${argKey}`;
  }

  /** Core single-dispatch lifecycle orchestrator. Executes the full
   *  optimistic → run (with retry) → success/error/cancel pipeline for
   *  one dispatch instance. Handles:
   *  - Early exit if already cancelled while queued in a scope chain
   *  - Idempotency key generation (stable across retries)
   *  - Optimistic mutation with error-path rollback
   *  - Registry recording at each state transition
   *  - Toast emission and per-dispatch callback invocation
   *  - Populating the dedupe entry's error/cancelled fields so
   *    collapsed callers receive the actual outcome
   *
   *  Always resolves (never rejects) — errors are captured internally
   *  and surfaced via callbacks + toasts. Returns TResult on success,
   *  null on error or cancellation. */
  /** Eagerly clear the dedupe entry so dispatches from within
   *  onSuccess/onError callbacks see a clean map and start fresh
   *  rather than collapsing onto this (now-settled) promise. The async
   *  .finally() cleanup in dispatch() remains as a safety net. */
  function clearDedupe(dk: string | null, entry: DedupeEntry | null): void {
    if (dk !== null && entry !== null && dedupeInflight.get(dk) === entry) {
      dedupeInflight.delete(dk);
    }
  }

  async function runOnce(
    args: TArgs,
    opts: DispatchOptions<TArgs, TResult>,
    ac: AbortController,
    id: string,
    dedupeEntry: DedupeEntry | null,
    dedupeKey: string | null,
    dispatchedAt: number,
  ): Promise<TResult | null> {
    started.add(id);
    /** Settle this dispatch: remove from inFlight/started + fire onSettled.
     *  Called exactly once at each exit point — replaces the outer
     *  try/finally so the control flow is explicit and flat. */
    const settle = (): void => {
      inFlight.delete(id);
      started.delete(id);
      if (opts.onSettled) invokeLifecycleHook(def.name, "onSettled", () => opts.onSettled!(args));
    };

    // If already cancelled while queued in scope chain, short-circuit.
    if (ac.signal.aborted) {
      const now = Date.now();
      // Mark the dedupe entry as cancelled so deduped callers' onError
      // doesn't fire (only onSettled does, per the contract).
      if (dedupeEntry !== null) dedupeEntry.cancelled = true;
      clearDedupe(dedupeKey, dedupeEntry);
      record({
        id, name: def.name, status: "cancelled", args,
        dispatchedAt, startedAt: now, completedAt: now,
      });
      if (opts.onCancel) invokeLifecycleHook(def.name, "onCancel", () => opts.onCancel!(args));
      settle();
      return null;
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

    let optOp: TOp | undefined;
    if (def.optimistic !== undefined) {
      try {
        optOp = def.optimistic(args);
      } catch (e) {
        // Optimistic mutation threw — record + rethrow-as-error path.
        // Skip run() entirely; nothing committed yet so no rollback
        // needed (the optimistic itself failed).
        const raw = toActionError(e);
        // Enrich with a canonical code so downstream can distinguish
        // optimistic failures from run() failures (e.g. for telemetry).
        const err: ActionErrorLike = raw.code !== undefined
          ? raw
          : { ...raw, code: "optimistic_failed" };
        if (dedupeEntry !== null) dedupeEntry.error = err;
        clearDedupe(dedupeKey, dedupeEntry);
        record({
          id, name: def.name, status: "error", args,
          dispatchedAt, startedAt, completedAt: Date.now(), error: err,
        });
        emitErrorToast(args, err, opts);
        if (opts.onError) invokeLifecycleHook(def.name, "onError", () => opts.onError!(err, args));
        settle();
        return null;
      }
    }

    // Record as pending after optimistic ran successfully.
    record({
      id, name: def.name, status: "pending", args,
      dispatchedAt, startedAt,
    });

    try {
      const { result, attempts } = await runWithRetry(args, ac.signal, ctx, opts.onRetryAttempt);
      // Cancellation can race success — if the signal aborted,
      // treat as cancelled even if run() resolved. Most adapters
      // throw on abort, but be defensive.
      if (ac.signal.aborted) {
        if (dedupeEntry !== null) dedupeEntry.cancelled = true;
        clearDedupe(dedupeKey, dedupeEntry);
        record({
          id, name: def.name, status: "cancelled", args,
          dispatchedAt, startedAt, completedAt: Date.now(), attempts,
        });
        if (def.rollback !== undefined) {
          try {
            def.rollback(args, optOp, { message: "cancelled", code: "cancelled" });
          } catch (e) {
            console.error(`[actions] rollback (cancellation) for ${def.name} threw`, e);
          }
        }
        if (opts.onCancel) invokeLifecycleHook(def.name, "onCancel", () => opts.onCancel!(args));
        settle();
        return null;
      }
      record({
        id, name: def.name, status: "success", args,
        dispatchedAt, startedAt, completedAt: Date.now(), result, attempts,
      });
      // Clear dedupe BEFORE callbacks so dispatches from onSuccess
      // with the same key start a fresh run instead of collapsing
      // onto this (now-settled) promise.
      clearDedupe(dedupeKey, dedupeEntry);
      emitSuccessToast(args, result, opts);
      if (opts.onSuccess) invokeLifecycleHook(def.name, "onSuccess", () => opts.onSuccess!(result, args));
      settle();
      return result;
    } catch (e: unknown) {
      const err = toActionError(e);
      const attempts = readAttempts(e);
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
      // Clear dedupe BEFORE callbacks so dispatches from onError
      // with the same key start a fresh run.
      clearDedupe(dedupeKey, dedupeEntry);
      const errRecord: ActionInstance = {
        id, name: def.name, status, args,
        dispatchedAt, startedAt, completedAt: Date.now(),
        ...(!cancelled ? { error: err } : undefined),
        ...(attempts !== undefined ? { attempts } : undefined),
      };
      record(errRecord);
      // Rollback the optimistic mutation regardless of cancel/error.
      if (def.rollback !== undefined) {
        try {
          const rbError = cancelled
            ? { message: "cancelled", code: "cancelled" }
            : err;
          def.rollback(args, optOp, rbError);
        } catch (rbCaught) {
          console.error(`[actions] rollback for ${def.name} threw`, rbCaught);
        }
      }
      if (!cancelled) {
        emitErrorToast(args, err, opts);
        if (opts.onError) invokeLifecycleHook(def.name, "onError", () => opts.onError!(err, args));
      } else {
        if (opts.onCancel) invokeLifecycleHook(def.name, "onCancel", () => opts.onCancel!(args));
      }
      settle();
      return null;
    }
  }

  /** Run with auto-retry on retry-class errors. Each attempt re-runs
   *  def.run() with the same args + signal + ctx. Optimistic does NOT
   *  re-fire — it stays applied across retries (the rollback only
   *  fires once retries are exhausted). Backoff: delay * factor^attempt,
   *  capped at 5000ms. Idempotency key in ctx is stable across retries.
   *  Returns { result, attempts } where attempts is total run() calls.
   *  On failure, attaches `_attempts` to the thrown error so the caller
   *  can record the count in the registry. */
  async function runWithRetry(args: TArgs, signal: AbortSignal, ctx: ActionContext, onRetryAttempt?: (info: RetryAttemptInfo, args: TArgs) => void): Promise<{ result: TResult; attempts: number }> {
    const cfg = def.retry;
    const maxAttempts = (cfg?.count ?? 0) + 1;
    const baseDelay = cfg?.delay ?? 0;
    const factor = cfg?.factor ?? 2;
    let attempt = 0;
    let lastRetryError: ActionErrorLike | undefined;
    while (true) {
      if (signal.aborted) {
        const abortErr = new DOMException("aborted", "AbortError");
        attachAttempts(abortErr, attempt);
        throw abortErr;
      }
      try {
        attempt++;
        if (attempt > 1 && onRetryAttempt !== undefined) {
          invokeLifecycleHook(def.name, "onRetryAttempt", () => onRetryAttempt({ attempt, maxAttempts, error: lastRetryError! }, args));
        }
        const result = await def.run(args, signal, ctx);
        return { result, attempts: attempt };
      } catch (e) {
        if (signal.aborted) { attachAttempts(e, attempt); throw e; }
        if (attempt >= maxAttempts) { attachAttempts(e, attempt); throw e; }
        const err = toActionError(e);
        if (!shouldRetry(err)) { attachAttempts(e, attempt); throw e; }
        lastRetryError = err;
        const wait = Math.min(baseDelay * Math.pow(factor, attempt - 1), 5000);
        try {
          await sleep(wait, signal);
        } catch {
          attachAttempts(e, attempt); throw e;
        }
      }
    }
  }

  /** True if the error matches the action's `retryable` classifier
   *  AND so qualifies for auto-retry. Delegates to the shared
   *  isRetryableError() in error.ts so auto-retry and the manual
   *  Retry button use identical classification (including transient
   *  HTTP statuses like 429/503). */
  function shouldRetry(err: ActionErrorLike): boolean {
    return isRetryableError(err, def.retryable);
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
    const retry = buildRetryButton(args, err);
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

  /** Build the retry button config for error toasts. Returns an
   *  `{ onClick }` object when the error is retry-eligible (per
   *  `def.retryable`), which toast.ts renders as a "Retry" button.
   *  When def.retryArgs is set, the retry computes fresh args at click
   *  time (avoiding stale DOM refs). Otherwise args are structuredClone'd
   *  so post-dispatch mutations don't corrupt the retry payload.
   *  Returns undefined (no button) when the error doesn't qualify
   *  for retry. */
  function buildRetryButton(args: TArgs, err: ActionErrorLike): { onClick: () => void } | undefined {
    if (!shouldRetry(err)) return undefined;
    // When retryArgs is provided, defer arg computation to click time
    // so the retry always uses fresh state (e.g. live DOM refs, current
    // array contents). The original args are still cloned as a fallback
    // reference for the retryArgs function.
    if (def.retryArgs !== undefined) {
      const retryArgsFn = def.retryArgs;
      // Clone original args as a stable reference for retryArgs to read
      // identifiers from (e.g. chatID, pattern). Best-effort clone.
      let refArgs: TArgs;
      try { refArgs = structuredClone(args); } catch {
        if (args === null || args === undefined || typeof args !== "object") {
          refArgs = args;
        } else {
          try { refArgs = (Array.isArray(args) ? [...args] : { ...args }) as TArgs; } catch { refArgs = args; }
        }
      }
      return {
        onClick: () => {
          let fresh: TArgs | null;
          try { fresh = retryArgsFn(refArgs); } catch { return; }
          if (fresh !== null) void dispatch(fresh);
        },
      };
    }
    // Snapshot args so mutations after dispatch don't corrupt retry.
    // structuredClone handles deep cloning; on failure (DOM refs,
    // functions) fall back to a shallow copy which at least isolates
    // top-level property mutations from the retry payload.
    let frozenArgs: TArgs;
    try {
      frozenArgs = structuredClone(args);
    } catch {
      // Shallow copy only makes sense for objects/arrays. Primitives
      // (string, number, boolean, null, undefined) are immutable and
      // need no cloning — use them directly.
      if (args === null || args === undefined || typeof args !== "object") {
        frozenArgs = args;
      } else {
        try {
          frozenArgs = (Array.isArray(args) ? [...args] : { ...args }) as TArgs;
        } catch {
          frozenArgs = args;
        }
      }
    }
    return {
      onClick: () => { void dispatch(frozenArgs); },
    };
  }

  function cancel(): void {
    if (inFlight.size === 0) return;
    // Eagerly clear dedupe entries so a re-dispatch with the same key
    // after cancel() starts a fresh run instead of collapsing onto the
    // (now-cancelled) promise. The async .finally() cleanup remains as
    // a safety net for the normal (non-cancel) path.
    for (const dk of activeDedupeKeys) {
      const entry = dedupeInflight.get(dk);
      if (entry !== undefined) entry.cancelled = true;
      dedupeInflight.delete(dk);
    }
    activeDedupeKeys.clear();
    // Eagerly remove scope-queued instances (not yet started) from
    // inFlight so isInflight reflects cancellation immediately. Started
    // instances are removed by runOnce's settle() helper when they complete.
    for (const [id, controller] of [...inFlight.entries()]) {
      controller.abort();
      if (!started.has(id)) {
        inFlight.delete(id);
        // Trigger the scope-skip resolver so the cancelled entry's
        // tail resolves immediately, unblocking subsequent entries.
        const skip = scopeSkipResolvers.get(id);
        if (skip !== undefined) {
          scopeSkipResolvers.delete(id);
          skip();
        }
      }
    }
  }

  const action: Action<TArgs, TResult> = {
    name: def.name,
    dispatch,
    cancel,
    get isInflight() { return inFlight.size > 0; },
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
