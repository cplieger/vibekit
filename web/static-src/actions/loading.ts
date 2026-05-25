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
  const { ariaBusy = true, preserveAriaBusy = false, pendingClass, preserveDisabled = false } = opts;
  const manageAriaBusy = ariaBusy && !preserveAriaBusy;
  // Track pending transitions to snapshot disabled state lazily —
  // avoids stale bind-time capture when external code mutates disabled.
  let wasPending = false;
  let baseDisabled = el.disabled;
  let hadFocus = false;
  let disposed = false;
  let wasConnected = el.isConnected;

  /** Restore element to idle state (B7: deduplicated helper). */
  const setIdle = (): void => {
    el.disabled = preserveDisabled ? baseDisabled : false;
    if (manageAriaBusy) el.removeAttribute("aria-busy");
    if (pendingClass) el.classList.remove(pendingClass);
    // Restore focus if the element had it before being disabled.
    if (hadFocus && el.isConnected && !el.disabled) el.focus();
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

  const { ariaBusy = true, preserveAriaBusy = false, pendingClass, preserveDisabled = false } = opts;
  const manageAriaBusy = ariaBusy && !preserveAriaBusy;
  let wasPending = false;
  let baseDisabled = el.disabled;
  let hadFocus = false;
  let disposed = false;
  let wasConnected = el.isConnected;

  const setIdle = (): void => {
    el.disabled = preserveDisabled ? baseDisabled : false;
    if (manageAriaBusy) el.removeAttribute("aria-busy");
    if (pendingClass) el.classList.remove(pendingClass);
    if (hadFocus && el.isConnected && !el.disabled) el.focus();
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
