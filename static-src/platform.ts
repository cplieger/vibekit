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
// guardAction — debounce wrapper that prevents double-fire on iOS.
// Returns a function that calls `fn` at most once per `ms` milliseconds.
// ---------------------------------------------------------------------------

export function guardAction(fn: () => void, ms = 400): () => void {
  let last = 0;
  return () => {
    const now = Date.now();
    if (now - last < ms) {
      return;
    }
    last = now;
    fn();
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
