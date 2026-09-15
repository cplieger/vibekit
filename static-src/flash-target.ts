// ---------------------------------------------------------------------------
// Scroll a deep-linked target into view and flash a ring around it.
//
// TWO unrelated element families are marked this way — a Settings control named
// by `?highlight=<id>`, and a pull-request row named by a notification's
// `#pr=<identity>` — so the mechanism lives here rather than in either of them,
// and this module is the one owner of the `deep-link-flash` class.
//
// A target may not be reachable the instant it is asked for: a panel swap runs
// through a view transition, the Settings panels populate from an async fetch,
// and a PR row arrives with its section's paint. So the flash retries across a
// bounded number of frames and waits for the element to be laid out — an element
// inside a `.hidden` panel or a collapsed disclosure has no box, so
// scrollIntoView on it is a silent no-op.
// ---------------------------------------------------------------------------

import { forceReflow } from "./dom.js";

/** The flash class; the keyframes live in css/30-utilities.css. */
const FLASH_CLASS = "deep-link-flash";

/** Both consumers centre their target in its own scroll container, so the options
 *  are the module's rather than a parameter with one value at every call site. */
const SCROLL_OPTIONS: ScrollIntoViewOptions = { block: "center", behavior: "smooth" };

/** How many frames to keep looking for a target that is not there yet. About a
 *  third of a second at 60fps: longer than the panel swap and a local fetch,
 *  short enough that a target which will never exist stops costing frames. */
const MAX_FRAMES = 20;

/** Backstop for stripping the flash class, comfortably past the 1.6s keyframes.
 *  Required rather than defensive: under `prefers-reduced-motion` the animation
 *  is suppressed, so `animationend` never fires and the ring would otherwise
 *  stay on that element for the rest of the session. */
const FLASH_CLEAR_MS = 2500;

/** The pending flash-clear timeout PER TARGET, so re-flashing an element
 *  cancels the deadline the previous flash installed.
 *
 *  Without it the first timeout stripped the second flash's class partway
 *  through: re-adding the class restarts the animation but does nothing to a
 *  timer that is already counting. Worst under `prefers-reduced-motion`, where
 *  the timeout is the only cleanup path and the flash would just vanish. Keyed
 *  weakly so a removed element is not held alive by its own timer entry. */
const flashTimers = new WeakMap<HTMLElement, ReturnType<typeof setTimeout>>();

/** Is the element laid out, i.e. can it actually be scrolled to? False while
 *  its panel still carries `.hidden`. */
function isLaidOut(e: HTMLElement): boolean {
  return e.offsetParent !== null || e.getClientRects().length > 0;
}

/** Scroll the element `find` resolves to into view and flash a ring around it.
 *
 *  Quiet on a target that never resolves by design: a caller's target may have
 *  been renamed or removed, and a jump that lands on the right VIEW having merely
 *  failed to find one element is a better outcome than an error the reader cannot
 *  act on. Returns nothing — there is no success signal to branch on. */
export function flashTarget(find: () => HTMLElement | null): void {
  let frames = 0;
  const attempt = (): void => {
    const target = find();
    if (target === null || !isLaidOut(target)) {
      frames++;
      if (frames <= MAX_FRAMES) {
        requestAnimationFrame(attempt);
      }
      return;
    }
    target.scrollIntoView(SCROLL_OPTIONS);
    // End any flash still live on this element, deadline included. The map entry
    // is the single record of "a flash is running", which is also what makes
    // this safe to call from a listener a PREVIOUS flash left attached: with no
    // entry it does nothing, and with one it is firing at that flash's own
    // animation end.
    const clear = (): void => {
      const pending = flashTimers.get(target);
      if (pending === undefined) {
        return;
      }
      clearTimeout(pending);
      flashTimers.delete(target);
      target.classList.remove(FLASH_CLASS);
    };
    clear();
    // Re-add rather than toggle: a second jump to the same element while the
    // first flash is still running must restart the animation, and removing the
    // class only takes effect after a reflow.
    target.classList.remove(FLASH_CLASS);
    forceReflow(target);
    target.classList.add(FLASH_CLASS);
    target.addEventListener("animationend", clear, { once: true });
    flashTimers.set(target, setTimeout(clear, FLASH_CLEAR_MS));
  };
  attempt();
}
