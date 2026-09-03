// ---------------------------------------------------------------------------
// Which pointer is actually being used, written to `data-pointer` on <html>.
//
// The app has three interaction tiers and they sit on TWO orthogonal axes:
//
//   desktop with a mouse   fine pointer,   wide viewport   -> compact
//   iPad or touch laptop   coarse pointer, wide viewport   -> enlarged controls
//   phone                  coarse pointer, narrow viewport -> mobile layout,
//                                                             enlarged controls
//
// Width alone cannot separate the first two, which is why the middle tier was
// unserved: every control size in this app derived from a breakpoint.
//
// # A capability query cannot answer this, and that is measured rather than
// # assumed
//
// `pointer` and `hover` report the PRIMARY input; `any-pointer` and `any-hover`
// report what is available. Neither reports what the person is using now, and
// both are unreliable on exactly the hardware this tier exists for:
//
//   - A Windows 11 laptop with a touchscreen AND a connected Bluetooth mouse
//     reports `pointer: coarse` true, `pointer: fine` FALSE, `any-pointer: fine`
//     FALSE and `any-hover: hover` FALSE in Chromium. Disabling the touchscreen
//     in Device Manager changes nothing. crbug 398065927, open; a second report
//     of `any-pointer` returning wrong matches is crbug 394519480.
//   - iPadOS Safari reports `pointer: coarse` and `hover: none` whether or not a
//     Magic Keyboard trackpad is attached.
//
// So the queries are kept as a SEED and as a no-JS fallback (01-tokens.css), and
// the decision is `PointerEvent.pointerType`, which is per event and states what
// generated it.
//
// # Cost, stated
//
// One layout shift, on the first input that disagrees with the seed. It is
// bounded to once per DEVICE rather than once per load, because the observed tier
// is cached (`device-view.ts`) and the inline boot snippet applies it before the
// first paint. A person who switches between a trackpad and the screen pays a
// reflow at each switch; that is inherent to adapting at all, and it is
// preferable to serving one tier wrongly.
// ---------------------------------------------------------------------------

import { cachePointerTier, cachedPointerTier, type PointerTier } from "./device-view.js";

const ATTR = "data-pointer";

/** `pointerType` is `"mouse" | "pen" | "touch"` per the Pointer Events spec, and
 *  a pen is COARSE here: a stylus on a touchscreen has no hover and its target
 *  wants finger-sized affordances, whatever its pixel precision. */
function tierFor(pointerType: string): PointerTier {
  return pointerType === "mouse" ? "fine" : "coarse";
}

/** The tier to paint before any input has been observed: the cached observation
 *  when this device has one, otherwise the capability queries as a guess.
 *
 *  `maxTouchPoints` is checked alongside `any-pointer: coarse` because the two
 *  disagree on real hardware and either one being positive is enough for a
 *  guess that the first real event will correct. */
export function seedTier(): PointerTier {
  const cached = cachedPointerTier();
  if (cached !== null) {
    return cached;
  }
  const coarse = globalThis.matchMedia?.("(any-pointer: coarse)").matches === true;
  const touch = (globalThis.navigator?.maxTouchPoints ?? 0) > 0;
  return coarse || touch ? "coarse" : "fine";
}

/** The tier currently applied to the document, or null before `initPointerTier`. */
export function currentTier(): PointerTier | null {
  const v = document.documentElement.getAttribute(ATTR);
  return v === "fine" || v === "coarse" ? v : null;
}

function apply(tier: PointerTier): void {
  // Guarded so an ordinary pointermove costs one string compare rather than an
  // attribute write and the style recalc every write on <html> would trigger.
  if (document.documentElement.getAttribute(ATTR) === tier) {
    return;
  }
  document.documentElement.setAttribute(ATTR, tier);
  cachePointerTier(tier);
}

/** Observe the pointer in use for the page's lifetime.
 *
 *  Both events are needed and neither is redundant. `pointerdown` catches a tap
 *  on a screen the mouse has never touched; `pointermove` is what returns a
 *  hybrid device to the compact tier when the person picks the mouse back up,
 *  which a down-only listener could only do on their next click. Capture phase
 *  so a handler calling `stopPropagation` cannot hide the input from us, and
 *  passive because neither listener ever cancels. */
export function initPointerTier(): void {
  apply(seedTier());

  const observe = (e: PointerEvent): void => {
    apply(tierFor(e.pointerType));
  };
  const opts = { capture: true, passive: true } as const;
  globalThis.addEventListener("pointerdown", observe, opts);
  globalThis.addEventListener("pointermove", observe, opts);
}
