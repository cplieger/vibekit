// ---------------------------------------------------------------------------
// Global settings save indicator. Shows a spinner → checkmark → fade
// sequence in the settings header whenever any setting is persisted.
//
// Usage: call `showSaving()` before the async save, then `showSaved()`
// on success or `showError()` on failure. Multiple rapid calls coalesce
// naturally because each call resets the state machine.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { iconEl, ICON_SAVE_OK, ICON_SAVE_FAIL } from "./icons.js";
import { subscribeToActions, pendingForAny } from "./actions/index.js";

function spinnerNode(): HTMLDivElement {
  const d = document.createElement("div");
  d.className = "spinner-sm";
  return d;
}

let fadeTimer: ReturnType<typeof setTimeout> | undefined;
let hideTimer: ReturnType<typeof setTimeout> | undefined;
let lastShownAt = 0;

function clearTimers(): void {
  if (fadeTimer !== undefined) clearTimeout(fadeTimer);
  if (hideTimer !== undefined) clearTimeout(hideTimer);
  fadeTimer = undefined;
  hideTimer = undefined;
}

export function showSaving(): void {
  lastShownAt = Date.now();
  clearTimers();
  const el = $.settingsSaveStatus;
  el.replaceChildren(spinnerNode());
  el.classList.remove("hidden", "fade-out");
}

export function showSaved(): void {
  lastShownAt = Date.now();
  clearTimers();
  const el = $.settingsSaveStatus;
  el.replaceChildren(iconEl(ICON_SAVE_OK));
  el.classList.remove("hidden", "fade-out");
  fadeTimer = setTimeout(() => {
    el.classList.add("fade-out");
    hideTimer = setTimeout(() => el.classList.add("hidden"), 400);
  }, 1200);
}

/** Show a red ✗ in the save indicator. Used by failed settings writes
 *  so the spinner doesn't stay forever; the toast already carries the
 *  detailed message, this is just the inline visual signal. */
export function showError(): void {
  lastShownAt = Date.now();
  clearTimers();
  const el = $.settingsSaveStatus;
  el.replaceChildren(iconEl(ICON_SAVE_FAIL));
  el.classList.remove("hidden", "fade-out");
  fadeTimer = setTimeout(() => {
    el.classList.add("fade-out");
    hideTimer = setTimeout(() => el.classList.add("hidden"), 400);
  }, 2400);  // longer than success — error deserves more eye time
}

// ---------------------------------------------------------------------------
// Hybrid registry subscription: if any settings action is in flight,
// ensure the spinner is visible. The imperative showSaving/showSaved/
// showError API remains canonical (it handles the debounce timer +
// generation counter in persist.ts / settings.ts). This subscription
// is a safety net so the spinner also shows if a settings action is
// dispatched from a path that doesn't call showSaving() explicitly.
// ---------------------------------------------------------------------------
const SETTINGS_ACTIONS = ["settings.patch", "settings.save_steering", "settings.set_kiro_setting"] as const;
const SETTINGS_NAMES: ReadonlySet<string> = new Set<string>(SETTINGS_ACTIONS);

// Track whether any settings action in the current batch has errored.
// Reset on the rising edge (first action goes pending) and consumed
// when the last action settles. Without this, "last status wins" —
// if action A errors at t=3000 and action B succeeds at t=3500, we'd
// show ✓ even though A failed.
let batchHadError = false;

subscribeToActions((instance) => {
  if (!SETTINGS_NAMES.has(instance.name)) return;
  if (instance.status === "pending") {
    // Reset error flag when starting a fresh batch (no settings actions
    // were pending before this one became pending).
    // Note: pendingForAny includes the just-recorded instance, so we
    // can't use it here to detect "first in batch". Instead we reset
    // on every pending — if a previous error was set, it'd be picked
    // up by the next completion. The trade-off: a long-running batch
    // sees error reset when a new dispatch starts, but that's acceptable
    // because the new dispatch is now the "current" save attempt.
    // For tighter semantics, we'd need to scan log status, which is
    // O(n); skip that optimization for now.
    if (Date.now() - lastShownAt < 500) return;
    showSaving();
  } else {
    if (instance.status === "error") batchHadError = true;
    if (!pendingForAny(SETTINGS_ACTIONS)) {
      // Batch fully settled. Show error if ANY action in the batch
      // errored, otherwise show success.
      if (batchHadError || instance.status === "error") showError();
      else if (instance.status === "success") showSaved();
      batchHadError = false;
    }
  }
});
