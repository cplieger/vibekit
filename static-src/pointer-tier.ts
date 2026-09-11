// ---------------------------------------------------------------------------
// Which pointer this document is laid out for, written to `data-pointer` on
// <html>. The tier is decided ONCE per load by `resolveTier` and nothing
// afterwards moves the attribute: the observer that remains is a DETECTOR that
// writes storage for the next load and never touches the tier.
// ---------------------------------------------------------------------------

import {
  cachePointerTier,
  cachedPointerTier,
  coarseEverSeen,
  markCoarseSeen,
  pointerModeChoice,
  setPointerModeChoice,
  type PointerTier,
} from "./device-view.js";

const ATTR = "data-pointer";

/** The THIRD tier state, a second attribute beside `data-pointer` on <html>. It
 *  means "this screen has been touched at least once and the reader has not
 *  pinned a tier", and 01-tokens.css spends it on `--hit-floor` ALONE — targets
 *  grow through the floor's zero-specificity `min-*` rules, so no painted box or
 *  glyph moves. It exists because the tier is a single choice made once per load
 *  and a hybrid device is genuinely both: a Windows touchscreen laptop driven by
 *  its mouse wants the dense layout AND a finger-sized target. */
const ATTR_TOUCHED = "data-touched";

/** A pen is COARSE: a stylus on a touchscreen has no hover and its target wants
 *  finger-sized affordances, whatever its pixel precision. */
function tierFor(pointerType: string): PointerTier {
  return pointerType === "mouse" ? "fine" : "coarse";
}

/** The tier this load is laid out for: a stated choice, else this device's last
 *  observed tier, else the capability guess.
 *
 *  The guess is the LOWEST rung because these queries report the PRIMARY input and
 *  are wrong on the hybrid hardware the middle rung exists for: Chromium on a
 *  Windows 11 touchscreen laptop with a Bluetooth mouse answers `any-pointer: fine`
 *  FALSE (crbug 398065927; 394519480 for `any-pointer` generally). `maxTouchPoints`
 *  is checked beside `any-pointer: coarse` because the two disagree there too. */
export function resolveTier(): PointerTier {
  const chosen = pointerModeChoice();
  if (chosen !== null) {
    return chosen;
  }
  const observed = cachedPointerTier();
  if (observed !== null) {
    return observed;
  }
  // A NULLABLE view of the global, not the DOM lib's: read through that type,
  // `no-unnecessary-condition` proves these two guards dead and offers to cut them.
  const g = globalThis as {
    readonly matchMedia?: (q: string) => MediaQueryList;
    readonly navigator?: { readonly maxTouchPoints?: number };
  };
  const coarse = g.matchMedia?.("(any-pointer: coarse)").matches ?? false;
  const touch = (g.navigator?.maxTouchPoints ?? 0) > 0;
  return coarse || touch ? "coarse" : "fine";
}

/** The tier currently applied to the document, or null before `initPointerTier`. */
export function currentTier(): PointerTier | null {
  const v = document.documentElement.getAttribute(ATTR);
  return v === "fine" || v === "coarse" ? v : null;
}

function applyTier(tier: PointerTier): void {
  // Guarded: an attribute write on <html> forces a style recalc, a compare does not.
  if (document.documentElement.getAttribute(ATTR) === tier) {
    return;
  }
  document.documentElement.setAttribute(ATTR, tier);
}

/** Write or clear the third-tier flag. Guarded for `applyTier`'s reason: an
 *  attribute write on <html> forces a style recalc, a compare does not. */
function applyTouched(on: boolean): void {
  const has = document.documentElement.hasAttribute(ATTR_TOUCHED);
  if (has === on) {
    return;
  }
  if (on) {
    document.documentElement.setAttribute(ATTR_TOUCHED, "");
  } else {
    document.documentElement.removeAttribute(ATTR_TOUCHED);
  }
}

/** Kept so a repeat init detaches its listener rather than stacking a second. */
let observer: ((e: PointerEvent) => void) | null = null;
/** The last tier WRITTEN, so a steady mouse costs a compare, not a storage write. */
let recorded: PointerTier | null = null;
/** So the reveal callback fires at most once. */
let coarseAnnounced = false;

interface InitOptions {
  /** Called the first time a coarse pointer is observed on a device that had never
   *  reported one; a parameter rather than a registry so a repeat init resets it. */
  readonly onCoarseSeen?: () => void;
}

/** Decide the tier for this load, then watch what the device is actually driven by
 *  so the next load has an observation rather than a guess.
 *
 *  Both events are needed: `pointerdown` catches a tap on a screen the mouse has
 *  never touched, `pointermove` the person picking the mouse back up. Capture phase
 *  so a handler calling `stopPropagation` cannot hide the input; passive because
 *  neither listener cancels. */
export function initPointerTier(opts: InitOptions = {}): void {
  if (observer !== null) {
    const previous = observer;
    globalThis.removeEventListener("pointerdown", previous, true);
    globalThis.removeEventListener("pointermove", previous, true);
    observer = null;
  }

  applyTier(resolveTier());

  // Backfill the sticky flag from either stored FACT, never from the guess: a guess
  // says a coarse pointer is AVAILABLE, the flag says one has been used here.
  // Written only when unset, because `writeBlob` does not dedupe.
  let seen = coarseEverSeen();
  if (!seen && (pointerModeChoice() === "coarse" || cachedPointerTier() === "coarse")) {
    markCoarseSeen();
    seen = true;
  }
  coarseAnnounced = seen;
  recorded = cachedPointerTier();

  // THE FLAG IS WRITTEN HERE AND NOWHERE ELSE, which is what preserves the
  // freeze: `seen` is a fact from a PREVIOUS load, so nothing re-lays-out under
  // the reader mid-session. The `observe` callback below must never touch it.
  //
  // A STATED CHOICE OUTRANKS EVERY OBSERVATION, so a reader who touched once and
  // then PINNED a tier via the sidebar toggle gets no floor from this: rung 1 of
  // `resolveTier` is a preference, and raising the floor to 44px anyway would
  // overturn the dense layout they asked for. A `coarse` choice needs no flag
  // either — that arm already declares the same floor.
  applyTouched(seen && pointerModeChoice() === null);

  const observe = (e: PointerEvent): void => {
    const tier = tierFor(e.pointerType);
    if (tier !== recorded) {
      recorded = tier;
      cachePointerTier(tier);
    }
    if (tier === "coarse" && !coarseAnnounced) {
      coarseAnnounced = true;
      markCoarseSeen();
      opts.onCoarseSeen?.();
    }
  };
  observer = observe;
  const listenerOpts = { capture: true, passive: true } as const;
  globalThis.addEventListener("pointerdown", observe, listenerOpts);
  globalThis.addEventListener("pointermove", observe, listenerOpts);
}

/** Record the tier the user asked for and lay the document out for it now. The
 *  choice is the top rung of `resolveTier`, so no later load or event overturns it. */
export function setPointerMode(tier: PointerTier): void {
  setPointerModeChoice(tier);
  applyTier(tier);
}
