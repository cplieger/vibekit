// debouncedDispatch: wrap an Action so that rapid calls coalesce into
// a single dispatch after a quiet window. Replaces ad-hoc setTimeout
// + clearTimeout chains scattered across consumer modules (autocomplete,
// search-as-you-type, slash-command option fetches).
//
// Behavior:
//   - Each call resets the timer with the latest args.
//   - When the timer fires, the most-recent args are dispatched.
//   - Returns a `flush()` to fire immediately (e.g. on Enter key)
//     and a `cancel()` to drop the pending dispatch (e.g. on blur).
//
// Note: the debounced function does NOT return a Promise — by design,
// since most callsites care about the SIDE-EFFECT (registry update,
// store mutation) rather than the per-call result. If you need the
// result, subscribe via `subscribeToActions` filtered by the action's
// name. This keeps the helper minimal; complexity belongs in the
// caller, not the primitive.
// ---------------------------------------------------------------------------

import type { Action } from "./types.js";

export interface DebouncedDispatch<TArgs> {
  /** Schedule a dispatch with the given args. Replaces any pending
   *  dispatch's args. */
  (args: TArgs): void;

  /** Fire immediately with the most-recent args (or args supplied
   *  here, overriding the pending). No-op if nothing is pending and
   *  no args supplied. */
  flush(args?: TArgs): void;

  /** Discard any pending dispatch without firing. Useful on blur,
   *  navigation, or when a different code path makes the pending
   *  args stale. */
  cancel(): void;

  /** True if there's a scheduled dispatch waiting for the timer. */
  isPending(): boolean;
}

export interface DebounceOptions {
  /** Quiet window in ms. */
  readonly wait: number;
  /** Fire on the leading edge instead of the trailing edge. With
   *  leading: true, the first call fires immediately and subsequent
   *  calls within `wait` are absorbed (no trailing fire). Default false. */
  readonly leading?: boolean;
}

/** Wrap an action with a debounce timer. */
export function debouncedDispatch<TArgs, TResult>(
  action: Action<TArgs, TResult>,
  opts: DebounceOptions,
): DebouncedDispatch<TArgs> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let lastArgs: TArgs | undefined;
  let pending = false;
  let lastFiredAt = 0;

  const fn = ((args: TArgs): void => {
    if (opts.leading === true) {
      const now = Date.now();
      // Suppress within the cooldown window regardless of cancel().
      // Without this guard, a cancel followed by a quick re-dispatch
      // would fire two concurrent runs of the action within `wait` ms.
      if (now - lastFiredAt < opts.wait) {
        // Track the most-recent suppressed args so flush() can fire them.
        // Schedule a trailing timer to fire them automatically once the
        // cooldown expires; otherwise suppressed args would be lost
        // when flush() isn't explicitly called.
        lastArgs = args;
        pending = true;
        if (timer === undefined) {
          const remaining = opts.wait - (now - lastFiredAt);
          timer = setTimeout(() => {
            timer = undefined;
            pending = false;
            const a = lastArgs;
            lastArgs = undefined;
            if (a !== undefined) {
              lastFiredAt = Date.now();
              void action.dispatch(a);
            }
          }, remaining);
        }
        return;
      }
      // Leading-edge: fire immediately, then suppress until quiet.
      void action.dispatch(args);
      lastFiredAt = now;
      lastArgs = undefined;
      pending = true;
      if (timer !== undefined) clearTimeout(timer);
      timer = setTimeout(() => {
        timer = undefined;
        pending = false;
      }, opts.wait);
      return;
    }
    lastArgs = args;
    pending = true;
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = undefined;
      pending = false;
      const a = lastArgs;
      lastArgs = undefined;
      if (a !== undefined) void action.dispatch(a);
    }, opts.wait);
  }) as DebouncedDispatch<TArgs>;

  fn.flush = (args?: TArgs): void => {
    if (timer !== undefined) {
      clearTimeout(timer);
      timer = undefined;
    }
    const a = args !== undefined ? args : lastArgs;
    lastArgs = undefined;
    pending = false;
    if (a !== undefined) {
      lastFiredAt = Date.now();
      void action.dispatch(a);
    }
  };

  fn.cancel = (): void => {
    if (timer !== undefined) {
      clearTimeout(timer);
      timer = undefined;
    }
    lastArgs = undefined;
    pending = false;
    // NOTE: do NOT reset lastFiredAt — the cooldown window must still
    // apply after cancel to prevent immediate re-fire within `wait` ms.
  };

  fn.isPending = (): boolean => pending;

  return fn;
}
