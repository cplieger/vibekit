// bindLoadingState: bind a button or input element's disabled / aria-busy
// state to a named action's pending count.
//
// Usage:
//
//   const unbind = bindLoadingState("git.commit", commitBtn);
//   // ...later, on teardown:
//   unbind();
//
// While `pendingFor("git.commit").length > 0`, the element gets:
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

import { subscribe, pendingFor } from "./registry.js";

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

  const apply = (): void => {
    const isPending = pendingFor(actionName).length > 0;
    // Snapshot the live disabled state on the pending edge (before we
    // clobber it) so we can restore it when the action completes.
    if (isPending && !wasPending) baseDisabled = el.disabled;
    if (isPending) {
      el.disabled = true;
      if (manageAriaBusy) el.setAttribute("aria-busy", "true");
      if (pendingClass) el.classList.add(pendingClass);
    } else if (wasPending) {
      // Transition pending→idle: restore element state.
      el.disabled = preserveDisabled ? baseDisabled : false;
      if (manageAriaBusy) el.removeAttribute("aria-busy");
      if (pendingClass) el.classList.remove(pendingClass);
    }
    wasPending = isPending;
  };

  /** Restore element state as if the action completed. */
  const restore = (): void => {
    if (wasPending) {
      el.disabled = preserveDisabled ? baseDisabled : false;
      if (manageAriaBusy) el.removeAttribute("aria-busy");
      if (pendingClass) el.classList.remove(pendingClass);
      wasPending = false;
    }
  };

  // Initial paint.
  apply();

  // Re-evaluate on every transition for the named action. Other action
  // transitions are ignored to avoid wasted work for fan-out cases
  // where many actions are in flight simultaneously.
  const unsubscribe = subscribe((instance) => {
    if (instance.name === actionName) apply();
  });

  // Unsubscribe restores element state if still mid-pending (B2).
  return () => { restore(); unsubscribe(); };
}
