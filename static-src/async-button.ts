// ---------------------------------------------------------------------------
// Per-button async feedback: spinner during the operation, ✓ on
// success, ✗ on error, then revert. Disables the button while
// pending and guards against re-entry.
//
// Works for both icon-only buttons (action pills) and text buttons
// (Clone, Commit). The button's original innerHTML and disabled
// state are saved and restored when the feedback cycle resets.
//
// State is exposed as `data-async-status` on the button so CSS or
// tests can observe it without parsing innerHTML.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";

import { iconEl } from "./icon-el.js";

const RESET_MS = 1200;

const CHECK_HTML =
  '<svg class="btn-async-glyph" width="14" height="14" viewBox="0 0 24 24" fill="none" ' +
  'stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" ' +
  'aria-hidden="true"><polyline points="20 6 9 17 4 12"/></svg>';

const X_HTML =
  '<svg class="btn-async-glyph" width="14" height="14" viewBox="0 0 24 24" fill="none" ' +
  'stroke="currentColor" stroke-width="3" stroke-linecap="round" ' +
  'aria-hidden="true"><path d="M18 6L6 18M6 6l12 12"/></svg>';

/** WeakMap to track pending reset timers per button. */
const resetTimers = new WeakMap<HTMLButtonElement, ReturnType<typeof setTimeout>>();

/** Lazily-created live region for announcing async button outcomes. */
let liveRegion: HTMLElement | null = null;

function announce(message: string): void {
  if (liveRegion === null) {
    liveRegion = el("span", {
      className: "sr-only",
      "aria-live": "polite",
      "aria-atomic": "true",
    });
    document.body.appendChild(liveRegion);
  }
  const region = liveRegion;
  // Clear then set to ensure re-announcement of identical messages.
  region.textContent = "";
  setTimeout(() => {
    region.textContent = message;
  }, 50);
}

export interface AsyncFeedbackOptions {
  /** Override the post-completion glyph hold (ms). Default 1200. */
  resetMs?: number;
  /** When true, restore the original innerHTML inside the spinner so
   *  the button still says e.g. "Cloning…" (or just "Clone"). The
   *  default ("icon-only") replaces the entire content with just the
   *  spinner glyph — best for action-pill icon buttons. */
  keepLabel?: boolean;
}

/** Run an async function with consistent button feedback. The button
 *  is disabled during the call. Re-entrant calls (clicking again
 *  while pending) are ignored. */
export async function withAsyncFeedback(
  btn: HTMLButtonElement,
  fn: () => Promise<unknown>,
  opts: AsyncFeedbackOptions = {},
): Promise<void> {
  // Guard: reject re-entry while any status is active (pending, success, error display)
  if (btn.dataset["asyncStatus"] !== undefined) {
    return;
  }

  // Cancel any pending reset timer from a prior cycle to avoid stale restores
  const prevTimer = resetTimers.get(btn);
  if (prevTimer !== undefined) {
    clearTimeout(prevTimer);
    resetTimers.delete(btn);
  }

  const origNodes = [...btn.childNodes].map((n) => n.cloneNode(true));
  const origDisabled = btn.disabled;
  const origAriaBusy = btn.getAttribute("aria-busy");

  const spinnerEl = el("span", {
    className: "spinner-sm btn-async-spinner",
    "aria-hidden": "true",
  });

  btn.dataset["asyncStatus"] = "pending";
  btn.disabled = true;
  btn.setAttribute("aria-busy", "true");
  if (opts.keepLabel === true) {
    btn.prepend(spinnerEl, document.createTextNode(" "));
  } else {
    btn.replaceChildren(spinnerEl);
  }

  let ok = true;
  try {
    await fn();
  } catch {
    ok = false;
  }

  // The button may have been removed from the DOM by the async
  // operation (e.g. picker rows are re-rendered after a successful
  // clone). Skip the success/error visual in that case — the new
  // DOM already reflects the result.
  if (!btn.isConnected) {
    if (origAriaBusy === null) {
      btn.removeAttribute("aria-busy");
    } else {
      btn.setAttribute("aria-busy", origAriaBusy);
    }
    delete btn.dataset["asyncStatus"];
    return;
  }

  btn.dataset["asyncStatus"] = ok ? "success" : "error";
  btn.replaceChildren(iconEl(ok ? CHECK_HTML : X_HTML));
  if (origAriaBusy === null) {
    btn.removeAttribute("aria-busy");
  } else {
    btn.setAttribute("aria-busy", origAriaBusy);
  }
  announce(ok ? "Action completed" : "Action failed");

  const reset = opts.resetMs ?? RESET_MS;
  const timerId = setTimeout(() => {
    resetTimers.delete(btn);
    if (!btn.isConnected) {
      return;
    }
    btn.replaceChildren(...origNodes.map((n) => n.cloneNode(true)));
    btn.disabled = origDisabled;
    delete btn.dataset["asyncStatus"];
  }, reset);
  resetTimers.set(btn, timerId);
}
