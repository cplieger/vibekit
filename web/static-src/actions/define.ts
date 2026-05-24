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
 *  "chat.delete" -> "Chat delete failed", "files.create" -> "Files
 *  create failed". Callers usually override via the `error` field. */
function defaultErrorPrefix(name: string): string {
  const parts = name.split(".");
  const tail = parts[parts.length - 1] ?? name;
  // Capitalise first char only — keep the rest as-authored.
  return tail.charAt(0).toUpperCase() + tail.slice(1) + " failed";
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
          inFlight.delete(id);
          emitCancelled(id, def, args, optOp, startedAt);
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
            def.rollback(args, optOp, err);
          } catch (rollbackErr) {
            console.error(`[actions] rollback for ${def.name} threw`, rollbackErr);
          }
        }
        if (!cancelled) emitErrorToast(args, err, opts, def);
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
    const msg = opts.successMessage ?? resolveToast(d.success, args, result);
    if (msg !== null) toastSuccess(msg);
  }

  function emitErrorToast(
    args: TArgs,
    err: ActionErrorLike,
    opts: DispatchOptions,
    d?: ActionDefinition<TArgs, TResult>,
  ): void {
    // Errors are user-facing by default; only `error: false` in the
    // definition suppresses, never the silent flag.
    const spec = d?.error;
    if (spec === false) return;
    const fallback = `${defaultErrorPrefix(def.name)}: ${err.message}`;
    let msg: string | null;
    if (opts.errorPrefix !== undefined) {
      msg = `${opts.errorPrefix}: ${err.message}`;
    } else if (typeof spec === "string") {
      msg = `${spec}: ${err.message}`;
    } else if (typeof spec === "function") {
      msg = spec(args, err);
    } else {
      msg = fallback;
    }
    if (msg !== null) toastError(msg);
  }

  function emitCancelled(
    _id: string,
    d: ActionDefinition<TArgs, TResult>,
    args: TArgs,
    optOp: OptimisticOp | undefined,
    _startedAt: number,
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

  return {
    name: def.name,
    dispatch,
    cancel,
  };
}

/** Test-only: reset the instance counter for deterministic IDs. */
export function _resetForTest(): void {
  instanceCounter = 0;
}
