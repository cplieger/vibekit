// Scroll state tracker.
//
// Tracks whether the user has manually scrolled up (away from the
// auto-follow position) and exposes scroll helpers. Unlike vibekit's
// equivalent, we DON'T auto-scroll on DOM mutations — terminal output
// often draws at the top (e.g. CSI [H + redraw) and snapping to bottom
// would hide it. Instead, render.ts calls scrollIntoView on the cursor
// row after each frame, which keeps the cursor visible regardless of
// whether new content is at the top, bottom, or middle.

const BOTTOM_TOLERANCE_PX = 100;
const USER_SCROLL_DEBOUNCE_MS = 150;

let scrollEl: HTMLElement | null = null;
let userScrolledUp = false;
let userScrollingUntil = 0;
let suppressUntil = 0;
let onUserScrollChange: ((scrolledUp: boolean) => void) | null = null;

function isAtBottom(): boolean {
  if (!scrollEl) {
    return true;
  }
  return scrollEl.scrollTop + scrollEl.clientHeight >= scrollEl.scrollHeight - BOTTOM_TOLERANCE_PX;
}

export function init(opts: {
  scrollEl: HTMLElement;
  onUserScrollChange?: (scrolledUp: boolean) => void;
}): void {
  scrollEl = opts.scrollEl;
  onUserScrollChange = opts.onUserScrollChange ?? null;

  scrollEl.addEventListener(
    "scroll",
    () => {
      if (Date.now() < suppressUntil) {
        return;
      }
      userScrollingUntil = Date.now() + USER_SCROLL_DEBOUNCE_MS;
      const wasScrolledUp = userScrolledUp;
      userScrolledUp = !isAtBottom();
      if (wasScrolledUp !== userScrolledUp && onUserScrollChange) {
        onUserScrollChange(userScrolledUp);
      }
    },
    { passive: true },
  );

  // Touch-driven scroll on iOS doesn't always fire 'scroll' events
  // synchronously; flushes that race with the touch can snap the
  // viewport back to bottom mid-drag. Treat any active touch on the
  // scroll container as "user is scrolling" so auto-follow disengages
  // immediately and only re-engages 150ms after touch ends.
  scrollEl.addEventListener(
    "touchstart",
    () => {
      userScrollingUntil = Date.now() + 60_000; // refreshed by touchend
    },
    { passive: true },
  );
  scrollEl.addEventListener(
    "touchend",
    () => {
      userScrollingUntil = Date.now() + USER_SCROLL_DEBOUNCE_MS;
    },
    { passive: true },
  );
  scrollEl.addEventListener(
    "touchcancel",
    () => {
      userScrollingUntil = Date.now() + USER_SCROLL_DEBOUNCE_MS;
    },
    { passive: true },
  );
}

/** Force scroll-to-bottom and re-engage auto-follow. */
export function scrollToBottom(): void {
  if (!scrollEl) {
    return;
  }
  userScrolledUp = false;
  userScrollingUntil = 0;
  if (onUserScrollChange) {
    onUserScrollChange(false);
  }
  // Synchronous scroll — must not be deferred to rAF during burst
  // insertions (e.g. /chat load) because the next batch arrives before
  // the rAF fires, pushing the viewport up again.
  scrollEl.scrollTop = scrollEl.scrollHeight;
}

/** Suppress user-scroll detection for the next `ms` milliseconds —
 *  used during keyboard transitions where browser-driven scroll events
 *  shouldn't be misread as user intent. */
export function suppressScroll(ms: number): void {
  suppressUntil = Date.now() + ms;
}

export function isUserScrolledUp(): boolean {
  return userScrolledUp;
}

export function isInUserScroll(): boolean {
  return Date.now() < userScrollingUntil;
}
