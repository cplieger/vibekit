// ---------------------------------------------------------------------------
// Platform detection & iOS workarounds.
// Runs once at import time; everything is a static read after that.
// ---------------------------------------------------------------------------

export const isStandalone: boolean =
  (navigator as { standalone?: boolean }).standalone === true ||
  window.matchMedia("(display-mode: standalone)").matches;

export const isIOS: boolean =
  /iPad|iPhone|iPod/.test(navigator.userAgent) ||
  (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);

// ---------------------------------------------------------------------------
// guardDuplicateActivation — absorbs duplicated pointer→click dispatch of ONE
// physical activation; the 300 ms tap-delay class is already eliminated by
// `touch-action: manipulation`. Keyboard activations are never filtered.
// ---------------------------------------------------------------------------

// A duplicate dispatch replays one gesture within a few ms; no deliberate human
// input falls under 50 ms (fastest sustained clicking is ~60 ms+ apart).
export const GHOST_CLICK_MS = 50;

/**
 * Wraps an activation handler, absorbing only mechanical duplicates: same
 * pointerType, pointer-initiated (detail > 0), arriving within GHOST_CLICK_MS
 * of the previously accepted pointer activation. A keyboard activation
 * (detail 0 / no pointerType) always dispatches. `now` is a test seam.
 */
export function guardDuplicateActivation(
  fn: (e: MouseEvent) => void,
  now: () => number = () => performance.now(),
): (e: MouseEvent) => void {
  let lastPointerType = "";
  let lastAcceptedAt = -Infinity;
  return (e: MouseEvent): void => {
    const pointerType = e instanceof PointerEvent ? e.pointerType : "";
    if (e.detail > 0 && pointerType !== "") {
      const t = now();
      if (pointerType === lastPointerType && t - lastAcceptedAt < GHOST_CLICK_MS) {
        return;
      }
      lastPointerType = pointerType;
      lastAcceptedAt = t;
    }
    fn(e);
  };
}

// ---------------------------------------------------------------------------
// fixIOSViewport — keeps a focused input visible when the iOS virtual
// keyboard resizes the visual viewport.  Debounced so it fires once after
// the keyboard animation settles, and never blurs the input.
// ---------------------------------------------------------------------------

export function fixIOSViewport(input: HTMLElement): (() => void) | undefined {
  if (window.visualViewport == null || !isStandalone) {
    return undefined;
  }
  let timer = 0;
  const handler = (): void => {
    clearTimeout(timer);
    timer = window.setTimeout(() => {
      if (document.activeElement === input) {
        input.scrollIntoView({ block: "nearest" });
      }
    }, 120);
  };
  window.visualViewport.addEventListener("resize", handler);
  return () => {
    clearTimeout(timer);
    window.visualViewport?.removeEventListener("resize", handler);
  };
}

// ---------------------------------------------------------------------------
// initSidebarSwipe — edge-swipe-to-open and swipe-left-to-close gestures for
// the PWA sidebar. Only active in standalone (PWA) mode; browser Safari's
// back-gesture conflicts with this otherwise.
// ---------------------------------------------------------------------------

export function initSidebarSwipe(chatArea: HTMLElement, sidebar: HTMLElement): void {
  if (!isStandalone) {
    return;
  }

  let startX = 0;
  let startY = 0;
  let tracking = false;

  const reset = (): void => {
    tracking = false;
  };

  chatArea.addEventListener(
    "touchstart",
    (e: TouchEvent) => {
      const t = e.touches[0];
      if (t === undefined) {
        return;
      }
      if (t.clientX <= 30) {
        startX = t.clientX;
        startY = t.clientY;
        tracking = true;
      }
    },
    { passive: true },
  );
  chatArea.addEventListener(
    "touchmove",
    (e: TouchEvent) => {
      if (!tracking) {
        return;
      }
      const t = e.touches[0];
      if (t === undefined) {
        return;
      }
      const dx = t.clientX - startX;
      const dy = Math.abs(t.clientY - startY);
      if (dx > 50 && dx > dy) {
        sidebar.classList.add("open");
        tracking = false;
      }
    },
    { passive: true },
  );
  chatArea.addEventListener("touchend", reset, { passive: true });

  sidebar.addEventListener(
    "touchstart",
    (e: TouchEvent) => {
      const t = e.touches[0];
      if (t === undefined) {
        return;
      }
      if (sidebar.classList.contains("open")) {
        startX = t.clientX;
        startY = t.clientY;
        tracking = true;
      }
    },
    { passive: true },
  );
  sidebar.addEventListener(
    "touchmove",
    (e: TouchEvent) => {
      if (!tracking) {
        return;
      }
      const t = e.touches[0];
      if (t === undefined) {
        return;
      }
      const dx = startX - t.clientX;
      const dy = Math.abs(t.clientY - startY);
      if (dx > 50 && dx > dy) {
        sidebar.classList.remove("open");
        tracking = false;
      }
    },
    { passive: true },
  );
  sidebar.addEventListener("touchend", reset, { passive: true });
}
