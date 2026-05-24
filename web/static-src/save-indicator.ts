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
import { subscribeToActions, pendingFor } from "./actions/index.js";

function spinnerNode(): HTMLDivElement {
  const d = document.createElement("div");
  d.className = "spinner-sm";
  return d;
}

let fadeTimer: ReturnType<typeof setTimeout> | undefined;
let hideTimer: ReturnType<typeof setTimeout> | undefined;

function clearTimers(): void {
  if (fadeTimer !== undefined) clearTimeout(fadeTimer);
  if (hideTimer !== undefined) clearTimeout(hideTimer);
  fadeTimer = undefined;
  hideTimer = undefined;
}

export function showSaving(): void {
  clearTimers();
  const el = $.settingsSaveStatus;
  el.replaceChildren(spinnerNode());
  el.classList.remove("hidden", "fade-out");
}

export function showSaved(): void {
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
// Hybrid registry subscription: if any pendingFor('settings.patch') is
// in flight, ensure the spinner is visible. The imperative showSaving/
// showSaved/showError API remains canonical (it handles the debounce
// timer + generation counter in persist.ts). This subscription is a
// safety net so the spinner also shows if a settings.patch is dispatched
// from a path that doesn't call showSaving() explicitly.
// ---------------------------------------------------------------------------
subscribeToActions((instance) => {
  if (instance.name !== "settings.patch") return;
  if (instance.status === "pending" && pendingFor("settings.patch").length > 0) {
    showSaving();
  } else if (pendingFor("settings.patch").length === 0) {
    if (instance.status === "success") showSaved();
    else if (instance.status === "error") showError();
  }
});
