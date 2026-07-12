// @vitest-environment happy-dom
// Tests for scroll.ts — drives the REAL ScrollController via its public API.
//
// Earlier revisions of this file imported nothing from ./scroll and instead
// re-implemented isAtBottom / trimOldMessages / the debounce guards inline,
// then asserted those copies against themselves (e.g. `expect(150).toBe(150)`
// and `expect(controllerResult).toBe(atBottom)` where both sides were the same
// expression). That exercised zero production code. These tests set up the DOM
// the controller binds to and exercise setLoadMore and the scroll-event →
// scroll-bottom toggle (which runs the private isAtBottom).

import { describe, it, expect, beforeEach } from "vitest";

// happy-dom may not implement ResizeObserver; scroll.ts's init() observes for
// image/code-block resize, so provide a no-op only when it is missing.
if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = class {
    observe(): void {
      /* no-op */
    }
    unobserve(): void {
      /* no-op */
    }
    disconnect(): void {
      /* no-op */
    }
  } as unknown as typeof ResizeObserver;
}

// scroll.ts constructs its singleton at import time, binding to #messages /
// #messages-wrap / #scroll-bottom, so the DOM must exist before the import.
document.body.innerHTML = `
  <div id="messages-wrap"><div id="messages"></div></div>
  <button id="scroll-bottom" class="hidden"></button>
`;

const scroll = await import("./scroll.js");
const messagesEl = document.getElementById("messages") as HTMLElement;
const scrollWrap = document.getElementById("messages-wrap") as HTMLElement;
const scrollBtn = document.getElementById("scroll-bottom") as HTMLElement;

/** Define the three scroll-geometry props happy-dom does not compute. */
function setGeometry(scrollTop: number, clientHeight: number, scrollHeight: number): void {
  // writable so the controller's observer-driven scrollTo() (which assigns
  // scrollTop) doesn't throw on these stubbed-geometry elements.
  Object.defineProperty(scrollWrap, "scrollTop", {
    value: scrollTop,
    writable: true,
    configurable: true,
  });
  Object.defineProperty(scrollWrap, "clientHeight", {
    value: clientHeight,
    writable: true,
    configurable: true,
  });
  Object.defineProperty(scrollWrap, "scrollHeight", {
    value: scrollHeight,
    writable: true,
    configurable: true,
  });
}

beforeEach(() => {
  scroll.resetScrollState();
  scroll.setLoadMore(null, false);
  messagesEl.replaceChildren();
  scrollBtn.classList.add("hidden");
});

describe("setLoadMore indicator", () => {
  it("inserts a load-more indicator when there are more messages", () => {
    scroll.setLoadMore(() => undefined, true);
    const indicator = document.getElementById("load-more-indicator");
    expect(indicator).not.toBeNull();
    expect(indicator!.textContent).toContain("older messages");
  });

  it("removes the indicator when hasMore becomes false", () => {
    scroll.setLoadMore(() => undefined, true);
    expect(document.getElementById("load-more-indicator")).not.toBeNull();
    scroll.setLoadMore(null, false);
    expect(document.getElementById("load-more-indicator")).toBeNull();
  });

  it("does not insert an indicator when hasMore is false", () => {
    scroll.setLoadMore(() => undefined, false);
    expect(document.getElementById("load-more-indicator")).toBeNull();
  });
});

describe("scroll event toggles the scroll-to-bottom button (isAtBottom)", () => {
  it("hides the button when the user is at the bottom (within 100px tolerance)", () => {
    // 600 + 400 = 1000 >= 1000 - 100 → at bottom.
    setGeometry(600, 400, 1000);
    scrollWrap.dispatchEvent(new Event("scroll"));
    expect(scrollBtn.classList.contains("hidden")).toBe(true);
  });

  it("shows the button when the user has scrolled up past the tolerance", () => {
    // 100 + 400 = 500 < 1000 - 100 → not at bottom.
    setGeometry(100, 400, 1000);
    scrollWrap.dispatchEvent(new Event("scroll"));
    expect(scrollBtn.classList.contains("hidden")).toBe(false);
  });

  it("treats exactly 100px from the bottom as still at bottom (inclusive boundary)", () => {
    // 500 + 400 = 900 >= 1000 - 100 (== 900) → at bottom (>= boundary).
    setGeometry(500, 400, 1000);
    scrollWrap.dispatchEvent(new Event("scroll"));
    expect(scrollBtn.classList.contains("hidden")).toBe(true);
  });

  it("treats 101px from the bottom as scrolled up", () => {
    // 499 + 400 = 899 < 900 → not at bottom.
    setGeometry(499, 400, 1000);
    scrollWrap.dispatchEvent(new Event("scroll"));
    expect(scrollBtn.classList.contains("hidden")).toBe(false);
  });
});
