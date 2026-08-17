// ---------------------------------------------------------------------------
// Deep-link to one Settings CONTROL: `?highlight=<element-id>`.
//
// The tab segment was already deep-linkable (`/settings/permissions`), so this
// is the last mile — scroll the control into view and flash a ring around it.
// There is deliberately NO registry, no generator and no search box: vibekit has
// four settings panels, so the ids the panels already carry ARE the index, and a
// caller naming one that no longer exists degrades to doing nothing rather than
// to a stale table nobody reads.
//
// The point is not the URL. It is that a message NAMING a setting can be a link
// to it, so `openSetting` (an in-app jump, no URL round trip) is the surface
// most callers use; `?highlight=` exists so such a link survives being copied,
// bookmarked or pasted into a chat.
//
// Two mechanics that are easy to get wrong here:
//
//   1. The query string is read at MODULE LOAD, not at boot. `applyShareTarget`
//      strips `location.search` and `pushRoute` compares only pathname + hash,
//      so the parameter is gone by the time any route is applied. Reading it at
//      import time is what makes it survive both.
//   2. A target may not be reachable the instant it is asked for: the panel swap
//      runs through a view transition and the Tools / Permissions / Instructions
//      panels populate from an async fetch. So the flash retries across a bounded
//      number of frames and waits for the element to be laid out — an element
//      inside a `.hidden` panel has no box, so scrollIntoView on it is a silent
//      no-op.
// ---------------------------------------------------------------------------

import { getActiveTabRoute, toggleSettingsView } from "./tabs.js";
import { forceSettingsTab } from "./settings-tabs.js";
import { pushRoute } from "./router.js";
import type { SettingsTab } from "./router.js";

/** The flash class; the keyframes live in css/17-settings.css. */
const FLASH_CLASS = "setting-flash";

/** How many frames to keep looking for a target that is not there yet. About a
 *  third of a second at 60fps: longer than the panel swap and a local fetch,
 *  short enough that an id which will never exist stops costing frames. */
const MAX_FRAMES = 20;

/** Backstop for stripping the flash class, comfortably past the 1.6s keyframes.
 *  Required rather than defensive: under `prefers-reduced-motion` the animation
 *  is suppressed, so `animationend` never fires and the ring would otherwise
 *  stay on that control for the rest of the session. */
const FLASH_CLEAR_MS = 2500;

/** The pending flash-clear timeout PER TARGET, so re-highlighting a control
 *  cancels the deadline the previous flash installed.
 *
 *  Without it the first timeout stripped the second flash's class partway
 *  through: re-adding the class restarts the animation but does nothing to a
 *  timer that is already counting. Worst under `prefers-reduced-motion`, where
 *  the timeout is the only cleanup path and the flash would just vanish. Keyed
 *  weakly so a removed control is not held alive by its own timer entry. */
const flashTimers = new WeakMap<HTMLElement, ReturnType<typeof setTimeout>>();

/** The `?highlight=` value this page load carried, read once at import time
 *  (see mechanic 1 above). Consumed at most once. */
let pendingTarget: string | null = readTargetFromURL();

function readTargetFromURL(): string | null {
  try {
    const raw = new URLSearchParams(location.search).get("highlight");
    return raw === null || raw.trim() === "" ? null : raw.trim();
  } catch {
    // A malformed search string is not worth failing a boot over.
    return null;
  }
}

/** Is the element laid out, i.e. can it actually be scrolled to? False while
 *  its panel still carries `.hidden`. */
function isLaidOut(e: HTMLElement): boolean {
  return e.offsetParent !== null || e.getClientRects().length > 0;
}

/** Scroll a Settings control into view and flash a ring around it.
 *
 *  Quiet on an unknown id by design: a caller's target may have been renamed or
 *  removed, and a jump that lands on the right PANEL having merely failed to
 *  find one control is a better outcome than an error the reader cannot act on.
 *  Returns nothing — there is no success signal to branch on. */
export function highlightControl(id: string): void {
  if (id === "") {
    return;
  }
  let frames = 0;
  const attempt = (): void => {
    const target = document.getElementById(id);
    if (target === null || !isLaidOut(target)) {
      frames++;
      if (frames <= MAX_FRAMES) {
        requestAnimationFrame(attempt);
      }
      return;
    }
    target.scrollIntoView({ block: "center", behavior: "smooth" });
    // End any flash still live on this control, deadline included. The map entry
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
    // Re-add rather than toggle: a second jump to the same control while the
    // first flash is still running must restart the animation, and removing the
    // class only takes effect after a reflow.
    target.classList.remove(FLASH_CLASS);
    void target.offsetWidth;
    target.classList.add(FLASH_CLASS);
    target.addEventListener("animationend", clear, { once: true });
    flashTimers.set(target, setTimeout(clear, FLASH_CLEAR_MS));
  };
  attempt();
}

/** Open Settings on `tab` and highlight `controlID`. The in-app form of the deep
 *  link: what a message naming a setting calls.
 *
 *  Never routes through `toggleSettingsView` when Settings is already the active
 *  view — that helper CLOSES an active singleton, so a link clicked from inside
 *  Settings would dismiss the panel it was pointing at. */
export function openSetting(tab: SettingsTab, controlID: string): void {
  if (getActiveTabRoute()?.kind !== "settings") {
    toggleSettingsView(tab);
  }
  // Swaps the panel and fires the tab's lazy loader; the URL is pushed here
  // rather than by forceSettingsTab, which is the router's own callee.
  forceSettingsTab(tab);
  pushRoute({ kind: "settings", tab });
  highlightControl(controlID);
}

/** Fire the `?highlight=` this page load carried, if any. Called from the
 *  router's settings branch once the panel's data loader has run. One-shot: a
 *  later popstate back to the same URL must not re-flash a control the reader
 *  has already been shown. */
export function flushURLHighlight(): void {
  const id = pendingTarget;
  if (id === null) {
    return;
  }
  pendingTarget = null;
  highlightControl(id);
}

/** Test-only: restore the module to its pre-boot state with a chosen target. */
export function _setPendingTargetForTest(id: string | null): void {
  pendingTarget = id;
}
