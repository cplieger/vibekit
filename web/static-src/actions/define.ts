// defineAction: the lifecycle runner. Takes an ActionDefinition,
// returns an Action whose dispatch() executes:
//
//   1. record instance as "pending"; run optimistic() if present
//   2. await run(args, signal)
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
// Toast wiring: integrates with toast.ts. Defaults:
//   - success: NO toast unless `success` is set in the definition or
//     overridden via dispatch({ successMessage })
//   - error:   ALWAYS toast unless `error: false` is explicitly set.
//     Default error message is "<HumanName> failed: <serverMessage>".
// ---------------------------------------------------------------------------

import { error as toastError, success as toastSuccess } from "../toast.js";
import { toActionError } from "./error.js";
import { record } from "./registry.js";
import { _registerAction } from "./cleanup.js";
import type {
  Action,
  ActionDefinition,
  ActionErrorLike,
  ActionInstance,
  DispatchOptions,
  OptimisticOp,
  ToastSpec,
} from "./types.js";

let instanceCounter = 0;

function nextInstanceID(name: string): string {
  instanceCounter += 1;
  return `${name}#${String(instanceCounter)}`;
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

export function defineAction<TArgs, TResult>(
  def: ActionDefinition<TArgs, TResult>,
): Action<TArgs, TResult> {
  // Track in-flight controllers so action.cancel() can abort them.
  const inFlight = new Map<string, AbortController>();

  function dispatch(args: TArgs, opts: DispatchOptions = {}): Promise<TResult | null> {
    const id = nextInstanceID(def.name);
    const startedAt = Date.now();
    const ac = new AbortController();
    inFlight.set(id, ac);

    let optOp: OptimisticOp | undefined;
    if (def.optimistic !== undefined) {
      try {
        optOp = def.optimistic(args);
      } catch (e) {
        // Optimistic mutation threw — record + rethrow-as-error path.
        // Skip run() entirely; nothing committed yet so no rollback
        // needed (the optimistic itself failed).
        const err = toActionError(e);
        const inst: ActionInstance<TArgs, TResult> = {
          id, name: def.name, status: "error", args,
          startedAt, completedAt: Date.now(), error: err,
        };
        record(inst);
        inFlight.delete(id);
        emitErrorToast(args, err, opts);
        return Promise.resolve(null);
      }
    }

    // Record as pending after optimistic ran successfully.
    record({
      id, name: def.name, status: "pending", args,
      startedAt,
    });

    return def.run(args, ac.signal).then(
      (result) => {
        // Cancellation can race success — if the signal aborted,
        // treat as cancelled even if run() resolved. Most adapters
        // throw on abort, but be defensive.
        if (ac.signal.aborted) {
          record({
            id, name: def.name, status: "cancelled", args,
            startedAt, completedAt: Date.now(),
          });
          inFlight.delete(id);
          emitCancelled(def, args, optOp);
          return null;
        }
        record({
          id, name: def.name, status: "success", args,
          startedAt, completedAt: Date.now(), result,
        });
        inFlight.delete(id);
        emitSuccessToast(args, result, opts, def);
        return result;
      },
      (e: unknown) => {
        const err = toActionError(e);
        // If aborted, classify as cancelled rather than error.
        const cancelled = ac.signal.aborted;
        const status = cancelled ? "cancelled" : "error";
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
        if (!cancelled) emitErrorToast(args, err, opts);
        return null;
      },
    );
  }

  function emitSuccessToast(
    args: TArgs,
    result: TResult,
    opts: DispatchOptions,
    d: ActionDefinition<TArgs, TResult>,
  ): void {
    if (opts.silent === true) return;
    try {
      const msg = opts.successMessage ?? resolveToast(d.success, args, result);
      if (msg !== null) toastSuccess(msg);
    } catch (e) {
      // Throwing in a success toast spec is silently dropped by design —
      // success toasts are non-critical and must never disrupt the caller.
      console.error(`[actions] emitSuccessToast for ${d.name} threw`, e);
    }
  }

  function emitErrorToast(
    args: TArgs,
    err: ActionErrorLike,
    opts: DispatchOptions,
  ): void {
    // Errors are user-facing by default; only `error: false` in the
    // definition suppresses, never the silent flag. The `def` is in
    // closure scope — pulling it as a parameter previously caused the
    // optimistic-throw path to silently bypass `error: false` because
    // the callsite forgot to pass it.
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
    const mode = def.retryable;
    if (mode === undefined || mode === false) return undefined;
    // Only transport-class failures: explicit code or status 0.
    // Don't match undefined status (programming errors like TypeError).
    const isNetworkClass =
      err.status === 0 || err.code === "network" || err.code === "timeout";
    if (mode === "network" && !isNetworkClass) return undefined;
    // Snapshot args so mutations after dispatch don't corrupt retry.
    let frozenArgs: TArgs;
    try { frozenArgs = structuredClone(args); } catch { frozenArgs = args; }
    return {
      onClick: () => { void dispatch(frozenArgs); },
    };
  }

  function emitCancelled(
    d: ActionDefinition<TArgs, TResult>,
    args: TArgs,
    optOp: OptimisticOp | undefined,
  ): void {
    if (d.rollback !== undefined) {
      try {
        // Build a synthetic ActionError so rollback() has something
        // to inspect — handlers may want to know cancellation vs real
        // error. We mark this with code:"cancelled".
        d.rollback(args, optOp, { message: "cancelled", code: "cancelled" });
      } catch (e) {
        console.error(`[actions] rollback (cancellation) for ${d.name} threw`, e);
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

/** Test-only: reset the instance counter for deterministic IDs. */
export function _resetForTest(): void {
  instanceCounter = 0;
}
