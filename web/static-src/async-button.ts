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

const RESET_MS = 1200;

const SPINNER_HTML =
  '<span class="spinner-sm btn-async-spinner" aria-hidden="true"></span>';

const CHECK_HTML =
  '<svg class="btn-async-glyph" width="14" height="14" viewBox="0 0 24 24" fill="none" ' +
  'stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" ' +
  'aria-hidden="true"><polyline points="20 6 9 17 4 12"/></svg>';

const X_HTML =
  '<svg class="btn-async-glyph" width="14" height="14" viewBox="0 0 24 24" fill="none" ' +
  'stroke="currentColor" stroke-width="3" stroke-linecap="round" ' +
  'aria-hidden="true"><path d="M18 6L6 18M6 6l12 12"/></svg>';

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
  if (btn.dataset["asyncStatus"] === "pending") return;

  const origHTML = btn.innerHTML;
  const origDisabled = btn.disabled;
  const origAriaBusy = btn.getAttribute("aria-busy");

  btn.dataset["asyncStatus"] = "pending";
  btn.disabled = true;
  btn.setAttribute("aria-busy", "true");
  btn.innerHTML = opts.keepLabel === true
    ? `${SPINNER_HTML} ${origHTML}`
    : SPINNER_HTML;

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
    return;
  }

  btn.dataset["asyncStatus"] = ok ? "success" : "error";
  btn.innerHTML = ok ? CHECK_HTML : X_HTML;

  const reset = opts.resetMs ?? RESET_MS;
  setTimeout(() => {
    if (!btn.isConnected) return;
    btn.innerHTML = origHTML;
    btn.disabled = origDisabled;
    if (origAriaBusy === null) btn.removeAttribute("aria-busy");
    else btn.setAttribute("aria-busy", origAriaBusy);
    delete btn.dataset["asyncStatus"];
  }, reset);
}
