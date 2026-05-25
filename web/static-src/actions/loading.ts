// bindLoadingState: bind a button or input element's disabled / aria-busy
// state to a named action's pending count.
//
// Usage:
//
//   const unbind = bindLoadingState("git.commit", commitBtn);
//   // ...later, on teardown:
//   unbind();
//
// While `isPending("git.commit")`, the element gets:
//   - disabled = true
//   - aria-busy = "true"  (omit by passing { ariaBusy: false })
//   - optionally an extra CSS class via { pendingClass: "btn-loading" }
//
// On every state transition for the named action (pending → success/
// error/cancelled or vice versa), the bound element is re-evaluated.
// Multiple in-flight instances of the same action keep the element
// disabled until ALL complete.
//
// Returns an unsubscribe function. Call it from the view's teardown
// hook to stop receiving updates and avoid leaking listeners.
// ---------------------------------------------------------------------------

import { subscribeByName, isPending, pendingForAny } from "./registry.js";

/** Element types that have a `.disabled` writable boolean. */
type DisableableElement = HTMLButtonElement | HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;

export interface BindLoadingOptions {
  /** When true (default), set `aria-busy="true"` while pending. */
  ariaBusy?: boolean;
  /** When true, don't manage aria-busy at all — lets external code own
   *  the attribute. Overrides `ariaBusy`. Default: false. */
  preserveAriaBusy?: boolean;
  /** CSS class to add while pending (in addition to the disabled prop). */
  pendingClass?: string;
  /** When true, also OR the element's existing disabled state with the
   *  pending state (i.e. element stays disabled if it was disabled for
   *  another reason). Default: false (the helper takes full ownership
   *  of disabled). Use this when the element has a separate
   *  validation-driven disabled state. */
  preserveDisabled?: boolean;
  /** Reactive disabled predicate. When provided, the element's disabled
   *  state is `pending || disabledFn()` on every transition. Solves the
   *  `preserveDisabled` limitation where external mutations during the
   *  pending phase are overwritten — the predicate is always re-evaluated.
   *  Takes precedence over `preserveDisabled` when both are set. */
  disabledFn?: () => boolean;
}

/**
 * Bind a button/input element's disabled / aria-busy state to a named
 * action's pending count.
 *
 * @param actionName - Registry action name to observe (e.g. "git.commit").
 * @param el - Button, input, select, or textarea whose `disabled` property
 *   will be toggled while the action is pending.
 * @param opts - Optional configuration for aria-busy, CSS class, and
 *   disabled-state preservation.
 * @returns An unsubscribe function that restores the element and detaches
 *   the registry listener.
 *
 * **Limitation (preserveDisabled):** External mutations to `el.disabled`
 * DURING the pending phase are overwritten on completion. Set the desired
 * disabled state AFTER the action completes if needed.
 */
export function bindLoadingState(
  actionName: string,
  el: DisableableElement,
  opts: BindLoadingOptions = {},
): () => void {
  const { ariaBusy = true, preserveAriaBusy = false, pendingClass, preserveDisabled = false, disabledFn } = opts;
  const manageAriaBusy = ariaBusy && !preserveAriaBusy;
  // Track pending transitions to snapshot disabled state lazily —
  // avoids stale bind-time capture when external code mutates disabled.
  let wasPending = false;
  let baseDisabled = el.disabled;
  let hadFocus = false;
  let disposed = false;
  let wasConnected = el.isConnected;

  /** Resolve the base disabled state: disabledFn takes precedence over
   *  the snapshot-based preserveDisabled approach. If disabledFn throws,
   *  fall back to false so the element is at least re-enabled (safe
   *  default) rather than stuck in the pending visual state. */
  const resolveBase = (): boolean => {
    if (disabledFn !== undefined) {
      try { return disabledFn(); } catch { return false; }
    }
    return preserveDisabled ? baseDisabled : false;
  };

  /** Restore element to idle state (B7: deduplicated helper). */
  const setIdle = (): void => {
    el.disabled = resolveBase();
    if (manageAriaBusy) el.removeAttribute("aria-busy");
    if (pendingClass) el.classList.remove(pendingClass);
    // Restore focus only if the user hasn't explicitly moved focus
    // elsewhere during the pending phase. When a button is disabled,
    // focus moves to <body>; if it's still there, the user didn't
    // intentionally navigate away, so restoring is correct.
    if (hadFocus && el.isConnected && !el.disabled) {
      const active = document.activeElement;
      if (active === null || active === document.body) el.focus();
    }
    hadFocus = false;
  };

  let unsub: (() => void) | undefined;

  const apply = (): void => {
    if (disposed) return;
    // Auto-dispose if the element was removed from the DOM — prevents
    // stale bindings and keeps the closure from leaking the element.
    if (wasConnected && !el.isConnected) { disposed = true; unsub?.(); return; }
    if (el.isConnected) wasConnected = true;
    const pending = isPending(actionName);
    // Snapshot the live disabled state on the pending edge (before we
    // clobber it) so we can restore it when the action completes.
    if (pending && !wasPending) {
      baseDisabled = el.disabled;
      hadFocus = document.activeElement === el;
    }
    if (pending) {
      el.disabled = true;
      if (manageAriaBusy) el.setAttribute("aria-busy", "true");
      if (pendingClass) el.classList.add(pendingClass);
    } else if (wasPending) {
      // Transition pending→idle: restore element state.
      setIdle();
    }
    wasPending = pending;
  };

  /** Restore element state as if the action completed. */
  const restore = (): void => {
    if (wasPending) {
      setIdle();
      wasPending = false;
    }
  };

  // Initial paint.
  apply();

  // Re-evaluate on every transition for the named action. Uses per-name
  // subscription to avoid O(n) fan-out across all bindLoadingState bindings.
  unsub = subscribeByName(actionName, apply);

  // Unsubscribe restores element state if still mid-pending (B2).
  return () => { disposed = true; restore(); unsub?.(); };
}

/**
 * Bind a button/input element's disabled / aria-busy state to MULTIPLE
 * action names. The element is disabled while ANY of the named actions
 * is pending (OR semantics via `pendingForAny`).
 *
 * Use this instead of stacking multiple `bindLoadingState` calls on the
 * same element — stacking causes the first unsubscribe to restore the
 * element even if another action is still pending.
 *
 * @param actionNames - Array of registry action names to observe.
 * @param el - Button, input, select, or textarea whose `disabled` property
 *   will be toggled while any named action is pending.
 * @param opts - Same options as `bindLoadingState`.
 * @returns An unsubscribe function that restores the element and detaches
 *   all registry listeners.
 */
export function bindLoadingStateMulti(
  actionNames: readonly string[],
  el: DisableableElement,
  opts: BindLoadingOptions = {},
): () => void {
  if (actionNames.length === 0) return () => {};
  if (actionNames.length === 1) return bindLoadingState(actionNames[0]!, el, opts);

  const { ariaBusy = true, preserveAriaBusy = false, pendingClass, preserveDisabled = false, disabledFn } = opts;
  const manageAriaBusy = ariaBusy && !preserveAriaBusy;
  let wasPending = false;
  let baseDisabled = el.disabled;
  let hadFocus = false;
  let disposed = false;
  let wasConnected = el.isConnected;

  const resolveBase = (): boolean => {
    if (disabledFn !== undefined) {
      try { return disabledFn(); } catch { return false; }
    }
    return preserveDisabled ? baseDisabled : false;
  };

  const setIdle = (): void => {
    el.disabled = resolveBase();
    if (manageAriaBusy) el.removeAttribute("aria-busy");
    if (pendingClass) el.classList.remove(pendingClass);
    if (hadFocus && el.isConnected && !el.disabled) {
      const active = document.activeElement;
      if (active === null || active === document.body) el.focus();
    }
    hadFocus = false;
  };

  let unsubs: (() => void)[] | undefined;

  const apply = (): void => {
    if (disposed) return;
    if (wasConnected && !el.isConnected) { disposed = true; if (unsubs) for (const u of unsubs) u(); return; }
    if (el.isConnected) wasConnected = true;
    const pending = pendingForAny(actionNames);
    if (pending && !wasPending) {
      baseDisabled = el.disabled;
      hadFocus = document.activeElement === el;
    }
    if (pending) {
      el.disabled = true;
      if (manageAriaBusy) el.setAttribute("aria-busy", "true");
      if (pendingClass) el.classList.add(pendingClass);
    } else if (wasPending) {
      setIdle();
    }
    wasPending = pending;
  };

  const restore = (): void => {
    if (wasPending) { setIdle(); wasPending = false; }
  };

  apply();

  unsubs = actionNames.map((name) => subscribeByName(name, apply));

  return () => {
    disposed = true;
    restore();
    if (unsubs) for (const u of unsubs) u();
  };
}

/** Options for `bindDisabledPattern`. */
export interface DisabledPatternOptions {
  /** Action names whose pending state contributes to disabled. */
  readonly actions: readonly string[];
  /** Manual disabled predicate. Re-evaluated on every action transition
   *  AND when `recheck()` is called. The element is disabled when
   *  `pending || disabledWhen()`. */
  readonly disabledWhen: () => boolean;
  /** CSS class to add while any action is pending (optional). */
  readonly pendingClass?: string;
  /** When true (default), set `aria-busy="true"` while pending. */
  readonly ariaBusy?: boolean;
}

/** Return type of `bindDisabledPattern`. */
export interface DisabledPatternHandle {
  /** Force a re-evaluation of the disabled state. Call this when the
   *  external condition (`disabledWhen`) may have changed outside of
   *  an action transition (e.g. after form input validation). */
  recheck(): void;
  /** Unsubscribe and restore the element to its natural state. */
  dispose(): void;
}

/**
 * Declaratively bind a button's disabled state to the combination of
 * action-pending state AND a manual predicate. Solves the common pattern:
 *
 *   `btn.disabled = actionPending || !formValid`
 *
 * without requiring the caller to manually subscribe to the registry AND
 * re-evaluate on every form change.
 *
 * The element is disabled when:
 *   - ANY of the named actions is pending, OR
 *   - `disabledWhen()` returns true
 *
 * Re-evaluation happens automatically on every action state transition.
 * For external state changes (form validation), call `handle.recheck()`.
 *
 * @param el - Button, input, select, or textarea to manage.
 * @param opts - Configuration: action names + disabled predicate.
 * @returns A handle with `recheck()` and `dispose()` methods.
 *
 * @example
 * ```ts
 * const handle = bindDisabledPattern(saveBtn, {
 *   actions: ["settings.patch", "settings.save_steering"],
 *   disabledWhen: () => !formValid || content === original,
 * });
 * // After form input changes:
 * handle.recheck();
 * // On teardown:
 * handle.dispose();
 * ```
 */
export function bindDisabledPattern(
  el: HTMLButtonElement | HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement,
  opts: DisabledPatternOptions,
): DisabledPatternHandle {
  const { actions, disabledWhen, pendingClass, ariaBusy = true } = opts;
  let disposed = false;

  const apply = (): void => {
    if (disposed) return;
    const pending = actions.length > 0 && pendingForAny(actions);
    let manualDisabled: boolean;
    try { manualDisabled = disabledWhen(); } catch { manualDisabled = false; }
    const shouldDisable = pending || manualDisabled;
    el.disabled = shouldDisable;
    if (pending) {
      if (ariaBusy) el.setAttribute("aria-busy", "true");
      if (pendingClass) el.classList.add(pendingClass);
    } else {
      if (ariaBusy) el.removeAttribute("aria-busy");
      if (pendingClass) el.classList.remove(pendingClass);
    }
  };

  apply();

  const unsubs = actions.map((name) => subscribeByName(name, apply));

  return {
    recheck(): void { apply(); },
    dispose(): void {
      if (disposed) return;
      disposed = true;
      for (const u of unsubs) u();
    },
  };
}

/** Snapshot passed to the `bindLoadingCluster` onChange callback. */
export interface ClusterState {
  /** True when at least one action in the cluster is pending. */
  readonly pending: boolean;
  /** Names of the actions currently pending within the cluster. */
  readonly activeNames: readonly string[];
}

/**
 * Observe a cluster of action names and invoke a callback on every
 * state transition. Unlike `bindLoadingStateMulti` (which manages a
 * single element's disabled state), this provides raw state so callers
 * can implement complex UI: show which action is active, combine with
 * form validation, drive spinners on multiple elements, etc.
 *
 * @param actionNames - Action names in the cluster.
 * @param onChange - Invoked synchronously on every transition with the
 *   current cluster state. Called once immediately with the initial state.
 * @returns An unsubscribe function.
 *
 * @example
 * ```ts
 * const unbind = bindLoadingCluster(
 *   ["settings.patch", "settings.save_steering"],
 *   ({ pending, activeNames }) => {
 *     saveBtn.disabled = pending || !formValid;
 *     spinner.hidden = !pending;
 *     statusLabel.textContent = pending
 *       ? `Saving ${activeNames[0]}…`
 *       : "Idle";
 *   },
 * );
 * ```
 */
export function bindLoadingCluster(
  actionNames: readonly string[],
  onChange: (state: ClusterState) => void,
): () => void {
  if (actionNames.length === 0) {
    onChange({ pending: false, activeNames: [] });
    return () => {};
  }

  let disposed = false;

  const notify = (): void => {
    if (disposed) return;
    const activeNames: string[] = [];
    for (let i = 0; i < actionNames.length; i++) {
      if (isPending(actionNames[i]!)) activeNames.push(actionNames[i]!);
    }
    onChange({ pending: activeNames.length > 0, activeNames });
  };

  // Initial state.
  notify();

  const unsubs = actionNames.map((name) => subscribeByName(name, notify));

  return () => {
    disposed = true;
    for (const u of unsubs) u();
  };
}
