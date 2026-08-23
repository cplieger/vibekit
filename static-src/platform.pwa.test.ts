// @vitest-environment happy-dom
//
// PLATFORM DETECTION AND THE TWO PWA-ONLY GESTURES.
//
// `platform.test.ts` covers `guardAction`, which is a pure debounce. This file
// covers the other three exports, all of which read the PLATFORM rather than
// their arguments: the `isStandalone` / `isIOS` constants, the iOS keyboard
// viewport fix, and the sidebar swipe gestures.
//
// Every test here loads the module DYNAMICALLY, after stubbing the globals it
// wants. That is not a style choice: `isStandalone` and `isIOS` are computed
// once at import time, and both gesture installers gate on `isStandalone`, so a
// static import would freeze whatever the test environment happened to report
// at collection time and no stub could reach it.
//
// happy-dom does not hand you the environment you need for any of this, so
// three facts are worth stating (measured against happy-dom 20.11.6, the
// version in this package's lockfile):
//
//   * `matchMedia` DOES implement `display-mode`, and answers `false` for
//     "(display-mode: standalone)" — it models a browser tab. It is stubbed
//     here anyway, because a test that needs the query to answer `true` cannot
//     get there otherwise.
//   * `window.visualViewport` does not exist at all, so `fixIOSViewport`
//     returns `undefined` unless a viewport is stubbed in.
//   * `navigator.userAgent` and its siblings are accessor-only with no setter,
//     so `navigator` has to be replaced wholesale rather than patched.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
// Type-only: the module itself is always loaded through `await import()` below,
// never statically, so the constants it computes at import time can be aimed at
// a stubbed platform.
import type * as Platform from "./platform.js";

const IPHONE_UA =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1";
const IPAD_UA =
  "Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1";
const IPOD_UA =
  "Mozilla/5.0 (iPod touch; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148";
const MAC_UA =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15";
const WINDOWS_UA =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36";

interface PlatformEnv {
  /** iOS Safari's non-standard `navigator.standalone`. */
  standalone?: boolean;
  /** What "(display-mode: standalone)" should answer. */
  displayMode?: boolean;
  ua?: string;
  platform?: string;
  maxTouchPoints?: number;
}

/** Stub the platform, then load a fresh copy of the module that reads it. */
async function loadPlatform(env: PlatformEnv = {}): Promise<typeof Platform> {
  vi.stubGlobal("navigator", {
    standalone: env.standalone,
    userAgent: env.ua ?? MAC_UA,
    platform: env.platform ?? "MacIntel",
    maxTouchPoints: env.maxTouchPoints ?? 0,
  });
  vi.stubGlobal("matchMedia", (media: string) => ({
    matches: media === "(display-mode: standalone)" && (env.displayMode ?? false),
    media,
  }));
  vi.resetModules();
  return await import("./platform.js");
}

afterEach(() => {
  document.body.replaceChildren();
});

describe("isStandalone", () => {
  const cases = [
    {
      name: "iOS Safari's navigator.standalone flag alone marks a standalone launch",
      env: { standalone: true, displayMode: false },
      expected: true,
    },
    {
      name: "the display-mode media query alone marks a standalone launch",
      env: { displayMode: true },
      expected: true,
    },
    {
      name: "a plain browser tab is not standalone",
      env: { displayMode: false },
      expected: false,
    },
  ] as const;

  for (const tc of cases) {
    it(tc.name, async () => {
      const { isStandalone } = await loadPlatform(tc.env);
      expect(isStandalone).toBe(tc.expected);
    });
  }
});

describe("isIOS", () => {
  const cases = [
    {
      name: "an iPhone is iOS",
      env: { ua: IPHONE_UA, platform: "iPhone", maxTouchPoints: 5 },
      expected: true,
    },
    {
      name: "an iPad is iOS",
      env: { ua: IPAD_UA, platform: "iPad", maxTouchPoints: 5 },
      expected: true,
    },
    {
      name: "an iPod touch is iOS",
      env: { ua: IPOD_UA, platform: "iPod touch", maxTouchPoints: 5 },
      expected: true,
    },
    {
      // iPadOS 13+ ships a desktop Safari user agent, so the platform + touch
      // pair is the only tell left.
      name: "an iPad masquerading as a Mac is iOS",
      env: { ua: MAC_UA, platform: "MacIntel", maxTouchPoints: 5 },
      expected: true,
    },
    {
      name: "a desktop Mac with no touch screen is not iOS",
      env: { ua: MAC_UA, platform: "MacIntel", maxTouchPoints: 0 },
      expected: false,
    },
    {
      // The masquerade test needs MORE than one touch point: a Mac that reports
      // exactly one is still a Mac.
      name: "a Mac reporting a single touch point is not iOS",
      env: { ua: MAC_UA, platform: "MacIntel", maxTouchPoints: 1 },
      expected: false,
    },
    {
      name: "a Windows touch device is not iOS",
      env: { ua: WINDOWS_UA, platform: "Win32", maxTouchPoints: 10 },
      expected: false,
    },
  ] as const;

  for (const tc of cases) {
    it(tc.name, async () => {
      const { isIOS } = await loadPlatform(tc.env);
      expect(isIOS).toBe(tc.expected);
    });
  }
});

// ---------------------------------------------------------------------------
// fixIOSViewport
// ---------------------------------------------------------------------------

type ViewportListener = () => void;

/**
 * A stand-in for `window.visualViewport` that really adds and removes its
 * listeners, so "the disposer detached the listener" is a behaviour this file
 * can observe rather than a call it has to take on trust.
 */
function fakeVisualViewport(): {
  addEventListener: (type: string, fn: ViewportListener) => void;
  removeEventListener: (type: string, fn: ViewportListener) => void;
  resize: () => void;
  types: string[];
} {
  const listeners = new Set<ViewportListener>();
  const types: string[] = [];
  return {
    addEventListener(type: string, fn: ViewportListener): void {
      types.push(type);
      listeners.add(fn);
    },
    removeEventListener(_type: string, fn: ViewportListener): void {
      listeners.delete(fn);
    },
    resize(): void {
      for (const fn of [...listeners]) {
        fn();
      }
    },
    types,
  };
}

/** The keyboard-animation settle delay the module waits out. */
const SETTLE_MS = 120;

describe("fixIOSViewport", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  async function install(env: PlatformEnv = { standalone: true }): Promise<{
    input: HTMLInputElement;
    other: HTMLInputElement;
    viewport: ReturnType<typeof fakeVisualViewport>;
    scrollIntoView: ReturnType<typeof vi.spyOn>;
    dispose: (() => void) | undefined;
  }> {
    const viewport = fakeVisualViewport();
    vi.stubGlobal("visualViewport", viewport);
    const { fixIOSViewport } = await loadPlatform(env);
    const input = document.createElement("input");
    const other = document.createElement("input");
    document.body.append(input, other);
    input.focus();
    const scrollIntoView = vi.spyOn(input, "scrollIntoView");
    return { input, other, viewport, scrollIntoView, dispose: fixIOSViewport(input) };
  }

  it("subscribes to viewport resizes in a standalone app", async () => {
    const { viewport, dispose } = await install();
    expect(dispose).toBeTypeOf("function");
    expect(viewport.types).toStrictEqual(["resize"]);
  });

  it("scrolls the focused input back into view once the keyboard animation settles", async () => {
    const { viewport, scrollIntoView } = await install();
    viewport.resize();
    expect(scrollIntoView).not.toHaveBeenCalled();
    vi.advanceTimersByTime(SETTLE_MS);
    expect(scrollIntoView).toHaveBeenCalledTimes(1);
  });

  it("reveals the input with the least scrolling instead of pinning it to the top", async () => {
    // `block: "nearest"` against scrollIntoView's own default of "start": the
    // default would yank a mid-screen input to the top of its scroller every
    // time the keyboard opens. No DOM implementation exposes the resulting
    // scroll offset, so the option is only observable where it is passed.
    const { viewport, scrollIntoView } = await install();
    viewport.resize();
    vi.advanceTimersByTime(SETTLE_MS);
    expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" });
  });

  it("scrolls once for a burst of resizes rather than once per resize", async () => {
    const { viewport, scrollIntoView } = await install();
    viewport.resize();
    vi.advanceTimersByTime(SETTLE_MS - 20);
    viewport.resize();
    vi.advanceTimersByTime(SETTLE_MS);
    expect(scrollIntoView).toHaveBeenCalledTimes(1);
  });

  it("leaves an input that no longer holds focus alone", async () => {
    const { other, viewport, scrollIntoView } = await install();
    other.focus();
    viewport.resize();
    vi.advanceTimersByTime(SETTLE_MS);
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it("drops a scroll that is still pending when it is disposed", async () => {
    const { viewport, scrollIntoView, dispose } = await install();
    viewport.resize();
    dispose?.();
    vi.advanceTimersByTime(SETTLE_MS);
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it("stops reacting to resizes after it is disposed", async () => {
    const { viewport, scrollIntoView, dispose } = await install();
    dispose?.();
    viewport.resize();
    vi.advanceTimersByTime(SETTLE_MS);
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it("does nothing in a browser tab, where the keyboard does not resize the viewport", async () => {
    const { viewport, dispose } = await install({ standalone: false, displayMode: false });
    expect(dispose).toBeUndefined();
    expect(viewport.types).toStrictEqual([]);
  });

  it("does nothing on a browser with no visual viewport at all", async () => {
    vi.stubGlobal("visualViewport", undefined);
    const { fixIOSViewport } = await loadPlatform({ standalone: true });
    const input = document.createElement("input");
    document.body.append(input);
    expect(fixIOSViewport(input)).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// initSidebarSwipe
// ---------------------------------------------------------------------------

function sendTouch(el: HTMLElement, type: "touchstart" | "touchmove", x: number, y: number): void {
  el.dispatchEvent(
    new TouchEvent(type, {
      touches: [new Touch({ identifier: 0, target: el, clientX: x, clientY: y })],
    }),
  );
}

function endTouch(el: HTMLElement): void {
  el.dispatchEvent(new TouchEvent("touchend", { touches: [] }));
}

describe("initSidebarSwipe", () => {
  async function install(env: PlatformEnv = { standalone: true }): Promise<{
    chatArea: HTMLElement;
    sidebar: HTMLElement;
  }> {
    const { initSidebarSwipe } = await loadPlatform(env);
    const chatArea = document.createElement("div");
    const sidebar = document.createElement("aside");
    document.body.append(chatArea, sidebar);
    initSidebarSwipe(chatArea, sidebar);
    return { chatArea, sidebar };
  }

  describe("opening by swiping in from the left edge", () => {
    it("opens the sidebar on a rightward drag that starts at the edge", async () => {
      const { chatArea, sidebar } = await install();
      sendTouch(chatArea, "touchstart", 10, 100);
      sendTouch(chatArea, "touchmove", 70, 100); // 60px right, 0px vertical
      expect(sidebar.classList.contains("open")).toBe(true);
    });

    it("arms the gesture for a touch that lands exactly on the edge band", async () => {
      // The band is 30px wide inclusive; a touch at 30 is still an edge touch.
      const { chatArea, sidebar } = await install();
      sendTouch(chatArea, "touchstart", 30, 100);
      sendTouch(chatArea, "touchmove", 90, 100);
      expect(sidebar.classList.contains("open")).toBe(true);
    });

    it("ignores a drag that starts away from the left edge", async () => {
      const { chatArea, sidebar } = await install();
      sendTouch(chatArea, "touchstart", 200, 100);
      sendTouch(chatArea, "touchmove", 400, 100); // 200px right, and still ignored
      expect(sidebar.classList.contains("open")).toBe(false);
    });

    it("holds the sidebar shut for a drag that stops short of the commit distance", async () => {
      const { chatArea, sidebar } = await install();
      sendTouch(chatArea, "touchstart", 10, 100);
      sendTouch(chatArea, "touchmove", 55, 100); // 45px, under the 50px commit
      expect(sidebar.classList.contains("open")).toBe(false);
    });

    it("holds the sidebar shut for a drag of exactly the commit distance", async () => {
      // The commit distance is exclusive: it takes MORE than 50px.
      const { chatArea, sidebar } = await install();
      sendTouch(chatArea, "touchstart", 10, 100);
      sendTouch(chatArea, "touchmove", 60, 100);
      expect(sidebar.classList.contains("open")).toBe(false);
    });

    it("treats a mostly-vertical drag as a scroll, not a swipe", async () => {
      const { chatArea, sidebar } = await install();
      sendTouch(chatArea, "touchstart", 10, 100);
      sendTouch(chatArea, "touchmove", 70, 300); // 60px right, 200px down
      expect(sidebar.classList.contains("open")).toBe(false);
    });

    it("treats an evenly diagonal drag as ambiguous and does not open", async () => {
      const { chatArea, sidebar } = await install();
      sendTouch(chatArea, "touchstart", 10, 100);
      sendTouch(chatArea, "touchmove", 70, 160); // 60px right, 60px down
      expect(sidebar.classList.contains("open")).toBe(false);
    });

    it("ignores movement that no touchstart armed", async () => {
      const { chatArea, sidebar } = await install();
      sendTouch(chatArea, "touchmove", 200, 0);
      expect(sidebar.classList.contains("open")).toBe(false);
    });

    it("disarms the gesture when the touch ends", async () => {
      const { chatArea, sidebar } = await install();
      sendTouch(chatArea, "touchstart", 10, 100);
      endTouch(chatArea);
      sendTouch(chatArea, "touchmove", 70, 100);
      expect(sidebar.classList.contains("open")).toBe(false);
    });

    it("opens once per gesture, however far the finger keeps travelling", async () => {
      const { chatArea, sidebar } = await install();
      const add = vi.spyOn(sidebar.classList, "add");
      sendTouch(chatArea, "touchstart", 10, 100);
      sendTouch(chatArea, "touchmove", 70, 100);
      sendTouch(chatArea, "touchmove", 130, 100);
      expect(add).toHaveBeenCalledTimes(1);
    });
  });

  describe("closing by swiping the open sidebar left", () => {
    it("closes the sidebar on a leftward drag across it", async () => {
      const { sidebar } = await install();
      sidebar.classList.add("open");
      sendTouch(sidebar, "touchstart", 200, 100);
      sendTouch(sidebar, "touchmove", 100, 100); // 100px left, 0px vertical
      expect(sidebar.classList.contains("open")).toBe(false);
    });

    it("keeps the sidebar open for a drag that stops short of the commit distance", async () => {
      const { sidebar } = await install();
      sidebar.classList.add("open");
      sendTouch(sidebar, "touchstart", 200, 100);
      sendTouch(sidebar, "touchmove", 170, 100); // 30px left
      expect(sidebar.classList.contains("open")).toBe(true);
    });

    it("keeps the sidebar open for a drag of exactly the commit distance", async () => {
      const { sidebar } = await install();
      sidebar.classList.add("open");
      sendTouch(sidebar, "touchstart", 200, 100);
      sendTouch(sidebar, "touchmove", 150, 100); // exactly 50px left
      expect(sidebar.classList.contains("open")).toBe(true);
    });

    it("treats a mostly-vertical drag across the sidebar as a scroll", async () => {
      const { sidebar } = await install();
      sidebar.classList.add("open");
      sendTouch(sidebar, "touchstart", 200, 100);
      sendTouch(sidebar, "touchmove", 100, 400); // 100px left, 300px down
      expect(sidebar.classList.contains("open")).toBe(true);
    });

    it("treats an evenly diagonal drag across the sidebar as ambiguous", async () => {
      const { sidebar } = await install();
      sidebar.classList.add("open");
      sendTouch(sidebar, "touchstart", 200, 100);
      sendTouch(sidebar, "touchmove", 100, 200); // 100px left, 100px down
      expect(sidebar.classList.contains("open")).toBe(true);
    });

    it("disarms the close gesture when the touch ends", async () => {
      const { sidebar } = await install();
      sidebar.classList.add("open");
      sendTouch(sidebar, "touchstart", 200, 100);
      endTouch(sidebar);
      sendTouch(sidebar, "touchmove", 100, 100);
      expect(sidebar.classList.contains("open")).toBe(true);
    });

    it("closes once per gesture, however far the finger keeps travelling", async () => {
      const { sidebar } = await install();
      sidebar.classList.add("open");
      const remove = vi.spyOn(sidebar.classList, "remove");
      sendTouch(sidebar, "touchstart", 200, 100);
      sendTouch(sidebar, "touchmove", 100, 100);
      sendTouch(sidebar, "touchmove", 50, 100);
      expect(remove).toHaveBeenCalledTimes(1);
    });

    it("arms nothing when the sidebar is touched while closed", async () => {
      // The two gestures share one tracking flag, so a touch that wrongly armed
      // on a closed sidebar would let the NEXT drag anywhere open it — without
      // ever starting at the screen edge.
      const { chatArea, sidebar } = await install();
      sendTouch(sidebar, "touchstart", 200, 100);
      sendTouch(chatArea, "touchmove", 300, 100);
      expect(sidebar.classList.contains("open")).toBe(false);
    });
  });

  describe("installation", () => {
    it("installs no gestures in a browser tab, where they fight Safari's back swipe", async () => {
      const { chatArea, sidebar } = await install({ standalone: false, displayMode: false });
      sendTouch(chatArea, "touchstart", 10, 100);
      sendTouch(chatArea, "touchmove", 70, 100);
      expect(sidebar.classList.contains("open")).toBe(false);
    });

    it("registers every touch listener passively so a gesture can never block scrolling", async () => {
      // A non-passive touch listener makes the browser wait for JS before it
      // scrolls; these handlers never call preventDefault, so passive is the
      // whole point of them. Registration is the only place it is observable —
      // no DOM implementation reports whether a scroll was blocked.
      const { initSidebarSwipe } = await loadPlatform({ standalone: true });
      const chatArea = document.createElement("div");
      const sidebar = document.createElement("aside");
      document.body.append(chatArea, sidebar);
      const chatAreaAdd = vi.spyOn(chatArea, "addEventListener");
      const sidebarAdd = vi.spyOn(sidebar, "addEventListener");

      initSidebarSwipe(chatArea, sidebar);

      const registrations = [...chatAreaAdd.mock.calls, ...sidebarAdd.mock.calls];
      expect(registrations.map((call) => call[0])).toStrictEqual([
        "touchstart",
        "touchmove",
        "touchend",
        "touchstart",
        "touchmove",
        "touchend",
      ]);
      for (const call of registrations) {
        expect(call[2]).toStrictEqual({ passive: true });
      }
    });
  });
});
