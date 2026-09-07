// ---------------------------------------------------------------------------
// Which pointer this document is laid out for, written to `data-pointer` on
// <html>.
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
// # THE TIER IS DECIDED ONCE PER LOAD
//
// `resolveTier()` runs at init and nothing afterwards moves the attribute. Three
// rungs, highest first:
//
//   1. the tier the user CHOSE with the toggle (`pointer-mode.ts`)
//   2. the tier this device was last OBSERVED being driven by
//   3. the capability guess, which is only ever reached on a device that has
//      never been driven at all
//
// No input event changes the tier. It used to: every `pointerdown` and
// `pointermove` re-applied the attribute, so a hybrid device re-laid-out its whole
// control vocabulary each time the person moved between the trackpad and the
// screen — and a reader who wanted the enlarged tier on a machine with a mouse
// attached had no way to ask for it and keep it. The button is the only way to
// change the mode now, and the observer that remains is a DETECTOR: it writes
// storage so rung 2 is a real observation next time, and it never touches the
// attribute.
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
// So the queries are the LOWEST rung and a no-JS fallback (01-tokens.css), never
// evidence about what is in the reader's hand. That is also why rung 2 is kept
// distinct from them: the resolution deliberately does NOT cache what it applies,
// or a guess would be indistinguishable from an observation on the next load and
// the ladder would collapse to two rungs.
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

/** `pointerType` is `"mouse" | "pen" | "touch"` per the Pointer Events spec, and
 *  a pen is COARSE here: a stylus on a touchscreen has no hover and its target
 *  wants finger-sized affordances, whatever its pixel precision. */
function tierFor(pointerType: string): PointerTier {
  return pointerType === "mouse" ? "fine" : "coarse";
}

/** The tier this load is laid out for: a stated choice, else this device's last
 *  observed tier, else the capability guess. See the header for the ladder.
 *
 *  `maxTouchPoints` is checked alongside `any-pointer: coarse` because the two
 *  disagree on real hardware and either one being positive is enough for a guess
 *  the reader can overrule with the toggle. */
export function resolveTier(): PointerTier {
  const chosen = pointerModeChoice();
  if (chosen !== null) {
    return chosen;
  }
  const observed = cachedPointerTier();
  if (observed !== null) {
    return observed;
  }
  // Read through a NULLABLE view of the global rather than the DOM lib's, which
  // declares both of these always present — true of a browser and of nothing
  // else. Read through that type and `no-unnecessary-condition` proves the guards
  // dead and offers to delete the two the non-browser runtimes need: a vitest
  // `node` environment has a global `navigator` (Node 21+) carrying no
  // `maxTouchPoints`, and no `matchMedia` at all. Same shape and same reason as
  // `typescript.md` "Read the capability off the object".
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

/** Write the attribute, and nothing else. The one mutation of the tier, called by
 *  init and by `setPointerMode`; it caches nothing, for the reason in the header. */
function applyTier(tier: PointerTier): void {
  // Guarded so a repeat call costs one string compare rather than an attribute
  // write and the style recalc every write on <html> would trigger.
  if (document.documentElement.getAttribute(ATTR) === tier) {
    return;
  }
  document.documentElement.setAttribute(ATTR, tier);
}

/** The registered observer, kept so a repeat init can detach it rather than stack
 *  a second one. */
let observer: ((e: PointerEvent) => void) | null = null;
/** The last tier the observer WROTE, so a steady mouse costs one compare per
 *  event instead of a localStorage read-modify-write. */
let recorded: PointerTier | null = null;
/** Whether this device's coarse-seen flag is already accounted for, so the reveal
 *  callback fires at most once. */
let coarseAnnounced = false;

interface InitOptions {
  /** Called the first time a coarse pointer is observed on a device that had
   *  never reported one. The composition root passes the toggle's reveal; it is a
   *  parameter rather than a subscriber registry so a repeat init resets it. */
  readonly onCoarseSeen?: () => void;
}

/** Decide the tier for this load, then watch what the device is actually driven
 *  by so the next load has an observation rather than a guess.
 *
 *  The observer RECORDS ONLY. Both events are needed and neither is redundant:
 *  `pointerdown` catches a tap on a screen the mouse has never touched, and
 *  `pointermove` catches the person picking the mouse back up, which a down-only
 *  listener could only notice on their next click. Capture phase so a handler
 *  calling `stopPropagation` cannot hide the input from us, and passive because
 *  neither listener ever cancels. */
export function initPointerTier(opts: InitOptions = {}): void {
  if (observer !== null) {
    const previous = observer;
    globalThis.removeEventListener("pointerdown", previous, true);
    globalThis.removeEventListener("pointermove", previous, true);
    observer = null;
  }

  applyTier(resolveTier());

  // Backfill the sticky flag from either stored FACT, never from the guess. It
  // covers devices that were being touched before the flag existed, with no
  // migration code — and it closes a real hole: a blob holding `pointer: "coarse"`
  // and no flag would reveal the toggle, then hide it again on the session's first
  // mouse move once the detector overwrote that field. The guess is excluded
  // because `any-pointer: coarse` says a coarse pointer is AVAILABLE, and the flag
  // means one has actually been used on this screen. Read the flag ONCE and write
  // only when it is unset: `writeBlob` does not dedupe, so an unconditional
  // backfill costs a touched device a whole-blob read-modify-write on every load
  // for a value that cannot change back — the same reason `recorded` is a latch.
  let seen = coarseEverSeen();
  if (!seen && (pointerModeChoice() === "coarse" || cachedPointerTier() === "coarse")) {
    markCoarseSeen();
    seen = true;
  }
  coarseAnnounced = seen;
  recorded = cachedPointerTier();

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

/** Record the tier the user asked for and lay the document out for it now.
 *
 *  The choice is the top rung of `resolveTier`, so it survives every later load
 *  and no input event can overturn it. */
export function setPointerMode(tier: PointerTier): void {
  setPointerModeChoice(tier);
  applyTier(tier);
}
