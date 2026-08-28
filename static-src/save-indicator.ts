// ---------------------------------------------------------------------------
// Per-setting save indicators: a spinner while a write is in flight, then a
// tick or a cross, in a slot next to the setting's own label.
//
// A caller names the KEYS it wrote and never a slot — an AppSettings key for a
// PATCH /api/settings, the dotted kiro-cli key for PUT /api/kiro-settings, and
// STEERING_SAVE_KEY for the global-instructions textarea. The markup binds each
// key to a slot with `data-save-status`, which takes a space-separated list so
// one slot can serve a value that has two controls.
//
// A key with no slot is a silent no-op, which is what `theme`, `fb_path`,
// `last_model` and `last_effort` are: each is written from outside Settings, and
// the control the user just moved is the confirmation.
//
// Usage: `showSaving(keys)` before the async write, then `showSaved(keys)` or
// `showError(keys)` when it answers.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { ICON_SAVE_OK, ICON_SAVE_FAIL } from "./icons.js";
import { iconEl } from "./icon-el.js";

/** The slot token for the global-instructions textarea. Not an AppSettings key —
 *  that content is its own PUT /api/steering — so it is named here. */
export const STEERING_SAVE_KEY = "steering";

/** One key, or the set a single write carried. */
export type SaveKeys = string | readonly string[];

/** How long a settled face stays before it fades. The error holds longer
 *  because it is the one a user has to notice. */
const SAVED_HOLD_MS = 1200;
const ERROR_HOLD_MS = 2400;
/** Matches `.settings-save-status`'s opacity transition in 17-settings.css; the
 *  element is hidden once the fade it starts has finished. */
const FADE_MS = 400;
/** A success arriving on an error's heels waits this long, so a retry does not
 *  blink the ✗ away before it was read. */
const MIN_ERROR_DISPLAY_MS = 1500;

interface SlotState {
  fadeTimer: ReturnType<typeof setTimeout> | undefined;
  hideTimer: ReturnType<typeof setTimeout> | undefined;
  pendingSuccessTimer: ReturnType<typeof setTimeout> | undefined;
  /** When this slot last painted a ✗, or 0. Drives the credit above. */
  lastErrorAt: number;
}

/** Timer state per slot. A plain Map rather than a WeakMap because the slots are
 *  permanent page elements and enumerating them is what lets a test clear their
 *  timers. */
const slotStates = new Map<HTMLElement, SlotState>();

function stateOf(target: HTMLElement): SlotState {
  let s = slotStates.get(target);
  if (s === undefined) {
    s = {
      fadeTimer: undefined,
      hideTimer: undefined,
      pendingSuccessTimer: undefined,
      lastErrorAt: 0,
    };
    slotStates.set(target, s);
  }
  return s;
}

/** The slots any of these keys names, each at most once. Read out of the DOM
 *  rather than built into a selector so a key can never be a selector. */
function slotsFor(keys: SaveKeys): HTMLElement[] {
  const want = new Set(typeof keys === "string" ? [keys] : keys);
  if (want.size === 0) {
    return [];
  }
  const out: HTMLElement[] = [];
  for (const target of document.querySelectorAll<HTMLElement>("[data-save-status]")) {
    const tokens = (target.dataset["saveStatus"] ?? "").split(/\s+/);
    if (tokens.some((t) => want.has(t))) {
      out.push(target);
    }
  }
  return out;
}

function spinnerNode(): HTMLDivElement {
  return el("div", { className: "spinner-sm" }) as HTMLDivElement;
}

function clearTimers(s: SlotState): void {
  clearTimeout(s.fadeTimer);
  clearTimeout(s.hideTimer);
  clearTimeout(s.pendingSuccessTimer);
  s.fadeTimer = undefined;
  s.hideTimer = undefined;
  s.pendingSuccessTimer = undefined;
}

function paint(target: HTMLElement, face: Node): void {
  target.replaceChildren(face);
  target.classList.remove("hidden", "fade-out");
}

/** Paint a settled face, then fade it out after `holdMs`. */
function settle(target: HTMLElement, s: SlotState, face: Node, holdMs: number): void {
  clearTimers(s);
  paint(target, face);
  s.fadeTimer = setTimeout(() => {
    s.fadeTimer = undefined;
    target.classList.add("fade-out");
    s.hideTimer = setTimeout(() => {
      s.hideTimer = undefined;
      target.classList.add("hidden");
    }, FADE_MS);
  }, holdMs);
}

function paintSaved(target: HTMLElement, s: SlotState): void {
  s.lastErrorAt = 0;
  settle(target, s, iconEl(ICON_SAVE_OK), SAVED_HOLD_MS);
}

export function showSaving(keys: SaveKeys): void {
  for (const target of slotsFor(keys)) {
    const s = stateOf(target);
    clearTimers(s);
    // A new write for this setting supersedes the last error, so it no longer
    // holds the next success back: the spinner already says something changed.
    s.lastErrorAt = 0;
    paint(target, spinnerNode());
  }
}

export function showSaved(keys: SaveKeys): void {
  for (const target of slotsFor(keys)) {
    const s = stateOf(target);
    const sinceError = Date.now() - s.lastErrorAt;
    if (s.lastErrorAt > 0 && sinceError < MIN_ERROR_DISPLAY_MS) {
      clearTimeout(s.pendingSuccessTimer);
      s.pendingSuccessTimer = setTimeout(() => {
        s.pendingSuccessTimer = undefined;
        paintSaved(target, s);
      }, MIN_ERROR_DISPLAY_MS - sinceError);
      continue;
    }
    paintSaved(target, s);
  }
}

export function showError(keys: SaveKeys): void {
  for (const target of slotsFor(keys)) {
    const s = stateOf(target);
    settle(target, s, iconEl(ICON_SAVE_FAIL), ERROR_HOLD_MS);
    s.lastErrorAt = Date.now();
  }
}

/** @internal — test-only reset for module-level state. */
export function _resetForTest(): void {
  for (const s of slotStates.values()) {
    clearTimers(s);
  }
  slotStates.clear();
}
