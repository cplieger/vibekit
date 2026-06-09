// ---------------------------------------------------------------------------
// Global settings save indicator. Shows a spinner → checkmark → fade
// sequence in the settings header whenever any setting is persisted.
//
// Usage: call `showSaving()` before the async save, then `showSaved()`
// on success or `showError()` on failure. Multiple rapid calls coalesce
// naturally because each call resets the state machine.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { iconEl, ICON_SAVE_OK, ICON_SAVE_FAIL } from "./icons.js";
import { subscribeToActions, pendingCount } from "./actions/index.js";

function spinnerNode(): HTMLDivElement {
  return el("div", { className: "spinner-sm" }) as HTMLDivElement;
}

let fadeTimer: ReturnType<typeof setTimeout> | undefined;
let hideTimer: ReturnType<typeof setTimeout> | undefined;
let lastShownAt = 0;
// Tracks the time at which the last error was displayed. If a success
// arrives within MIN_ERROR_DISPLAY_MS of an error, we delay the ✓
// override so the user actually sees the error. Without this guard,
// a rapid error→success sequence (e.g. retry click after error)
// causes a near-imperceptible ✗→✓ blink.
let lastErrorAt = 0;
const MIN_ERROR_DISPLAY_MS = 1500;
let pendingSuccessTimer: ReturnType<typeof setTimeout> | undefined;

function clearTimers(): void {
  if (fadeTimer !== undefined) {
    clearTimeout(fadeTimer);
  }
  if (hideTimer !== undefined) {
    clearTimeout(hideTimer);
  }
  if (pendingSuccessTimer !== undefined) {
    clearTimeout(pendingSuccessTimer);
  }
  fadeTimer = undefined;
  hideTimer = undefined;
  pendingSuccessTimer = undefined;
}

export function showSaving(): void {
  lastShownAt = Date.now();
  // Reset error-display credit: the user is starting a new operation,
  // they're aware of the new in-flight save (spinner is visible). The
  // next showSaved should not be delayed by an old error.
  lastErrorAt = 0;
  clearTimers();
  const statusEl = $.settingsSaveStatus;
  statusEl.replaceChildren(spinnerNode());
  statusEl.classList.remove("hidden", "fade-out");
}

export function showSaved(): void {
  // Tradeoff 3: if an error was just displayed, delay the ✓ override
  // until the error has had at least MIN_ERROR_DISPLAY_MS visibility.
  // Without this, a rapid error→success would cause a ✗→✓ blink.
  const elapsedSinceError = Date.now() - lastErrorAt;
  if (lastErrorAt > 0 && elapsedSinceError < MIN_ERROR_DISPLAY_MS) {
    if (pendingSuccessTimer !== undefined) {
      clearTimeout(pendingSuccessTimer);
    }
    const remaining = MIN_ERROR_DISPLAY_MS - elapsedSinceError;
    pendingSuccessTimer = setTimeout(() => {
      pendingSuccessTimer = undefined;
      doShowSaved();
    }, remaining);
    return;
  }
  doShowSaved();
}

function doShowSaved(): void {
  lastShownAt = Date.now();
  if (fadeTimer !== undefined) {
    clearTimeout(fadeTimer);
  }
  if (hideTimer !== undefined) {
    clearTimeout(hideTimer);
  }
  fadeTimer = undefined;
  hideTimer = undefined;
  const statusEl = $.settingsSaveStatus;
  statusEl.replaceChildren(iconEl(ICON_SAVE_OK));
  statusEl.classList.remove("hidden", "fade-out");
  fadeTimer = setTimeout(() => {
    statusEl.classList.add("fade-out");
    hideTimer = setTimeout(() => {
      statusEl.classList.add("hidden");
    }, 400);
  }, 1200);
}

/** Show a red ✗ in the save indicator. Used by failed settings writes
 *  so the spinner doesn't stay forever; the toast already carries the
 *  detailed message, this is just the inline visual signal. */
export function showError(): void {
  lastShownAt = Date.now();
  lastErrorAt = Date.now();
  clearTimers();
  const statusEl = $.settingsSaveStatus;
  statusEl.replaceChildren(iconEl(ICON_SAVE_FAIL));
  statusEl.classList.remove("hidden", "fade-out");
  fadeTimer = setTimeout(() => {
    statusEl.classList.add("fade-out");
    hideTimer = setTimeout(() => {
      statusEl.classList.add("hidden");
    }, 400);
  }, 2400); // longer than success — error deserves more eye time
}

// ---------------------------------------------------------------------------
// Hybrid registry subscription: if any settings action is in flight,
// ensure the spinner is visible. The imperative showSaving/showSaved/
// showError API remains canonical (it handles the debounce timer +
// generation counter in persist.ts / settings.ts). This subscription
// is a safety net so the spinner also shows if a settings action is
// dispatched from a path that doesn't call showSaving() explicitly.
// ---------------------------------------------------------------------------
const SETTINGS_ACTIONS = [
  "settings.patch",
  "settings.save_steering",
  "settings.set_kiro_setting",
] as const;
const SETTINGS_NAMES: ReadonlySet<string> = new Set<string>(SETTINGS_ACTIONS);

// Track whether any settings action in the current batch has errored.
// Reset when a new batch starts (this dispatch is the first pending
// settings action) and consumed when the last action settles.
// Without this, "last status wins" — if action A errors at t=3000
// and action B succeeds at t=3500, we'd show ✓ even though A failed.
let batchHadError = false;
// Track whether a settings batch is currently active so we can
// detect the rising edge (false → true) cleanly.
let batchActive = false;

subscribeToActions((instance) => {
  if (!SETTINGS_NAMES.has(instance.name)) {
    return;
  }
  if (instance.status === "pending") {
    // Rising edge: this is the first pending in a new batch.
    // Reset the error flag so a stale error from a previous batch
    // doesn't mask this batch's success outcome.
    if (!batchActive) {
      batchHadError = false;
      batchActive = true;
    }
    if (Date.now() - lastShownAt < 500) {
      return;
    }
    showSaving();
  } else {
    if (instance.status === "error") {
      batchHadError = true;
    }
    if (pendingCount(SETTINGS_ACTIONS) === 0) {
      // Batch fully settled. Show error if ANY action in the batch
      // errored, otherwise show success.
      if (batchHadError) {
        showError();
      } else if (instance.status === "success") {
        showSaved();
      }
      batchHadError = false;
      batchActive = false;
    }
  }
});

/** @internal — test-only reset for module-level state. */
export function _resetForTest(): void {
  clearTimers();
  lastShownAt = 0;
  lastErrorAt = 0;
  batchHadError = false;
  batchActive = false;
}
