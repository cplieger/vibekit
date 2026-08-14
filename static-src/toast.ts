// ---------------------------------------------------------------------------
// Ephemeral toast notifications — adopted from @cplieger/ui-primitives.
//
// The hand-rolled toast.ts + toast-engine.ts (timer/queue state machine + DOM
// view) were replaced by the library's default `toast` singleton, whose
// defaults already match vibekit's: up to 3 visible (rest queued, cap 20),
// info/success auto-dismiss after 4s, error sticky, hover OR focus pauses and
// resumes only once BOTH end, click / Escape (newest first) / Enter / Space
// dismiss. Screen-reader announcement is decoupled into the shared announce()
// live region (error = assertive, info/success = polite) instead of a
// role/aria-live on the stack — strictly better a11y (no nested live regions).
//
// This module is the thin vibekit wrapper preserving the public surface
// (`info` / `success` / `error` / `showToast` + the `ToastLevel` / `ToastRetry`
// types) so the ~hundreds of call sites (and the @cplieger/actions boot wiring
// in actions/boot.ts) are unchanged. Visuals live in the .uip-toast skin
// (css/04-uip-skin.css), ported 1:1 from the old .vk-toast (bottom-right stack,
// solid error/success variants, the countdown progress bar, vibekit's motion).
// ---------------------------------------------------------------------------

import { toast, _resetForTest as uipResetToast } from "@cplieger/ui-primitives/toast";
import type { ToastLevel, ToastRetry } from "@cplieger/ui-primitives/toast";

export type { ToastLevel, ToastRetry };

/** Show an info-level toast. Auto-dismisses after 4s (paused on hover/focus). */
export function info(message: string): () => void {
  return toast.info(message);
}

/** Show a success-level toast. Auto-dismisses after 4s (paused on hover/focus). */
export function success(message: string): () => void {
  return toast.success(message);
}

/** How long an error toast stays up. The library's default for `error` is 0
 *  (sticky), which vibekit overrides: an error nobody dismisses never leaves,
 *  so two or three of them stack and stay stacked for the rest of the session.
 *  12s is well past a comfortable read for a one-line failure, and the stack
 *  still pauses on hover/focus, so a user reading one is never cut off.
 *
 *  A RETRYABLE error keeps the sticky behaviour: its button is the only way to
 *  take the offered action, and timing that out would silently discard it. */
const ERROR_DURATION_MS = 12_000;

/** Show an error-level toast. Auto-dismisses after 12s (paused on hover/focus);
 *  click or press Escape to dismiss sooner. Optionally accepts a retry config;
 *  the toast renders a button that invokes onClick + dismisses, and stays put
 *  until answered. */
export function error(message: string, retry?: ToastRetry): () => void {
  return toast.show(
    message,
    retry !== undefined
      ? { level: "error", retry }
      : { level: "error", duration: ERROR_DURATION_MS },
  );
}

/** Show a toast with explicit level + duration. Use durationMs=0 for a sticky
 *  toast that requires manual dismissal. Pass undefined to use vibekit's level
 *  default (4s for info/success, 12s for error — see error() above). */
export function showToast(
  message: string,
  level: ToastLevel = "info",
  durationMs?: number,
): () => void {
  if (durationMs !== undefined) {
    return toast.show(message, { level, duration: durationMs });
  }
  return level === "error" ? error(message) : toast.show(message, { level });
}

/** Test-only: clear all visible + queued toasts and remove the stack. */
export function _resetForTest(): void {
  uipResetToast();
}
