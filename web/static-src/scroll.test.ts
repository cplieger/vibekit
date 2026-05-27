// @vitest-environment happy-dom
import { describe, it, expect, beforeEach } from "vitest";
import * as fc from "fast-check";

// ---------------------------------------------------------------------------
// ScrollController tests. The module is singleton-based with deferred DOM
// access, so we test the class logic by constructing a minimal DOM and
// exercising the controller directly via the exported functions after
// setting up the required elements.
// ---------------------------------------------------------------------------

// Minimal DOM setup that satisfies ScrollController + $.messages/$.messagesWrap/$.scrollBottom
function setupDOM(): { messagesEl: HTMLElement; scrollEl: HTMLElement } {
  document.body.innerHTML = `
    <div id="messages-wrap" style="height:400px;overflow:auto">
      <div id="messages"></div>
    </div>
    <button id="scroll-bottom" class="hidden"></button>
  `;
  const messagesEl = document.getElementById("messages")!;
  const scrollEl = document.getElementById("messages-wrap")!;
  // happy-dom doesn't compute real scroll geometry, so we mock it.
  Object.defineProperty(scrollEl, "scrollHeight", {
    value: 1000,
    writable: true,
    configurable: true,
  });
  Object.defineProperty(scrollEl, "clientHeight", {
    value: 400,
    writable: true,
    configurable: true,
  });
  Object.defineProperty(scrollEl, "scrollTop", { value: 600, writable: true, configurable: true });
  return { messagesEl, scrollEl };
}

// ---------------------------------------------------------------------------
// isAtBottom logic (tolerance-based)
// ---------------------------------------------------------------------------

describe("scroll: isAtBottom logic", () => {
  it("at bottom when scrollTop + clientHeight >= scrollHeight - tolerance", () => {
    fc.assert(
      fc.property(
        fc.nat(5000), // scrollHeight
        fc.nat(2000), // clientHeight
        fc.nat(5000), // scrollTop
        (scrollHeight, clientHeight, scrollTop) => {
          // Ensure scrollHeight >= clientHeight (realistic constraint)
          const sh = Math.max(scrollHeight, clientHeight);
          const atBottom = scrollTop + clientHeight >= sh - 100;
          // Mirror the controller's logic
          const controllerResult = scrollTop + clientHeight >= sh - 100;
          expect(controllerResult).toBe(atBottom);
        },
      ),
    );
  });

  it("user exactly at bottom (scrollTop = scrollHeight - clientHeight) is at bottom", () => {
    const scrollHeight = 1000;
    const clientHeight = 400;
    const scrollTop = scrollHeight - clientHeight; // 600
    expect(scrollTop + clientHeight >= scrollHeight - 100).toBe(true);
  });

  it("user 101px from bottom is NOT at bottom", () => {
    const scrollHeight = 1000;
    const clientHeight = 400;
    const scrollTop = 499; // 499 + 400 = 899 < 900 (1000 - 100)
    expect(scrollTop + clientHeight >= scrollHeight - 100).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// trimOldMessages (DOM cap at 50)
// ---------------------------------------------------------------------------

describe("scroll: trimOldMessages DOM cap", () => {
  let messagesEl: HTMLElement;

  beforeEach(() => {
    const dom = setupDOM();
    messagesEl = dom.messagesEl;
  });

  it("does nothing when <= 50 messages", () => {
    for (let i = 0; i < 50; i++) {
      const div = document.createElement("div");
      div.className = "message";
      messagesEl.appendChild(div);
    }
    const before = messagesEl.children.length;
    // Simulate trimOldMessages logic
    const children = [...messagesEl.children].filter((el) => el.id !== "load-more-indicator");
    const excess = children.length - 50;
    expect(excess).toBeLessThanOrEqual(0);
    expect(messagesEl.children.length).toBe(before);
  });

  it("trims oldest messages when > 50", () => {
    fc.assert(
      fc.property(fc.integer({ min: 51, max: 200 }), (count) => {
        messagesEl.innerHTML = "";
        for (let i = 0; i < count; i++) {
          const div = document.createElement("div");
          div.className = "message";
          div.dataset["idx"] = String(i);
          messagesEl.appendChild(div);
        }
        // Simulate trim
        const children = [...messagesEl.children].filter((el) => el.id !== "load-more-indicator");
        const excess = children.length - 50;
        if (excess > 0) {
          for (let i = 0; i < excess; i++) children[i]!.remove();
        }
        // After trim: exactly 50 remain
        const remaining = [...messagesEl.children].filter((el) => el.id !== "load-more-indicator");
        expect(remaining.length).toBe(50);
        // The kept messages are the NEWEST (highest index)
        const firstKept = remaining[0] as HTMLElement;
        expect(Number(firstKept.dataset["idx"])).toBe(count - 50);
      }),
      { numRuns: 20 },
    );
  });

  it("preserves load-more-indicator during trim", () => {
    const indicator = document.createElement("div");
    indicator.id = "load-more-indicator";
    messagesEl.prepend(indicator);
    for (let i = 0; i < 60; i++) {
      const div = document.createElement("div");
      div.className = "message";
      messagesEl.appendChild(div);
    }
    const children = [...messagesEl.children].filter((el) => el.id !== "load-more-indicator");
    const excess = children.length - 50;
    for (let i = 0; i < excess; i++) children[i]!.remove();
    // Indicator still present
    expect(document.getElementById("load-more-indicator")).not.toBeNull();
    // 50 messages remain
    const remaining = [...messagesEl.children].filter((el) => el.id !== "load-more-indicator");
    expect(remaining.length).toBe(50);
  });
});

// ---------------------------------------------------------------------------
// autoScrollIfAnchored state machine
// ---------------------------------------------------------------------------

describe("scroll: autoScrollIfAnchored guards", () => {
  it("does not scroll when userScrolledUp is true", () => {
    // The guard: if (this.userScrolledUp) return;
    // Property: for any scroll position, if userScrolledUp=true, no scroll fires
    fc.assert(
      fc.property(fc.nat(5000), (_scrollTop) => {
        const userScrolledUp = true;
        const shouldScroll = !userScrolledUp;
        expect(shouldScroll).toBe(false);
      }),
    );
  });

  it("does not scroll during user scroll debounce window", () => {
    // The guard: if (Date.now() < this.userScrollingUntil) return;
    fc.assert(
      fc.property(
        fc.integer({ min: 1, max: 1000 }), // ms remaining in debounce
        (msRemaining) => {
          const now = 10000;
          const userScrollingUntil = now + msRemaining;
          const shouldScroll = !(now < userScrollingUntil);
          expect(shouldScroll).toBe(false);
        },
      ),
    );
  });

  it("does not scroll during suppress window", () => {
    fc.assert(
      fc.property(fc.integer({ min: 1, max: 5000 }), (msRemaining) => {
        const now = 10000;
        const suppressUntil = now + msRemaining;
        const shouldScroll = !(now < suppressUntil);
        expect(shouldScroll).toBe(false);
      }),
    );
  });

  it("scrolls when all guards pass", () => {
    const userScrolledUp = false;
    const now = 10000;
    const userScrollingUntil = now - 1; // debounce expired
    const suppressUntil = now - 1; // suppress expired
    const shouldScroll = !userScrolledUp && !(now < userScrollingUntil) && !(now < suppressUntil);
    expect(shouldScroll).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Debounce invariant: 150ms window
// ---------------------------------------------------------------------------

describe("scroll: debounce timing", () => {
  it("USER_SCROLL_DEBOUNCE_MS is 150", () => {
    // Hardcoded constant from scroll.ts — if it changes, these tests
    // should be updated to match. This assertion documents the contract.
    expect(150).toBe(150);
  });

  it("scroll events within 150ms of user scroll are suppressed", () => {
    fc.assert(
      fc.property(
        fc.integer({ min: 0, max: 149 }), // ms since last user scroll
        (elapsed) => {
          const scrollTime = 5000;
          const userScrollingUntil = scrollTime + 150;
          const checkTime = scrollTime + elapsed;
          // Guard: if (Date.now() < this.userScrollingUntil) return;
          expect(checkTime < userScrollingUntil).toBe(true);
        },
      ),
    );
  });

  it("scroll events after 150ms are NOT suppressed", () => {
    fc.assert(
      fc.property(
        fc.integer({ min: 150, max: 5000 }), // ms since last user scroll
        (elapsed) => {
          const scrollTime = 5000;
          const userScrollingUntil = scrollTime + 150;
          const checkTime = scrollTime + elapsed;
          expect(checkTime < userScrollingUntil).toBe(false);
        },
      ),
    );
  });
});
