// Scroll state tracker.
//
// Tracks whether the user has manually scrolled up (away from the
// bottom) and exposes the scroll helpers the render layer uses.
//
// Auto-follow model (single-container, see term-render.ts): the shell
// terminal is one flat list of rows — the live screen zone pinned at the
// bottom, frozen history above it. "Following" means the viewport is
// stuck to the bottom (the live zone). term-render.ts enforces this once
// per frame via stickToBottomIfFollowing(): after any DOM mutation, if
// the user hasn't scrolled up, pin to the bottom. When the user IS
// scrolled up, term-render.ts instead compensates scrollTop so their
// position holds as history is inserted above. Browser scroll-anchoring
// is disabled (overflow-anchor:none in CSS) so these two explicit paths
// fully own the scroll position.

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
