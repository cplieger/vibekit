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
  const { ariaBusy = true, pendingClass, preserveDisabled = false } = opts;
  // Snapshot the element's pre-bind disabled state so preserveDisabled
  // can OR it with the pending state. Without this, a re-evaluation
  // after a successful action would re-enable an element that was
  // originally disabled for another reason.
  const originalDisabled = el.disabled;

  const apply = (): void => {
    const isPending = pendingFor(actionName).length > 0;
    el.disabled = preserveDisabled ? (originalDisabled || isPending) : isPending;
    if (ariaBusy) {
      if (isPending) {
        el.setAttribute("aria-busy", "true");
      } else {
        el.removeAttribute("aria-busy");
      }
    }
    if (pendingClass !== undefined && pendingClass !== "") {
      el.classList.toggle(pendingClass, isPending);
    }
  };

  // Initial paint.
  apply();

  // Re-evaluate on every transition for the named action. Other action
  // transitions are ignored to avoid wasted work for fan-out cases
  // where many actions are in flight simultaneously.
  return subscribe((instance) => {
    if (instance.name === actionName) apply();
  });
}
