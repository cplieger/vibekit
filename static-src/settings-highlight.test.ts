// ---------------------------------------------------------------------------
// Tests for settings-highlight.ts (D115): the ?highlight=<id> deep link.
//
// The mechanism has exactly two jobs — land on the control and mark it — and
// exactly one failure mode that matters: doing something loud when the id is
// wrong. Both directions are asserted here.
//
// tabs.ts and settings-tabs.ts are mocked at the boundary: opening the Settings
// view is a command this module issues, not behaviour it owns, and importing the
// real tab store would drag the whole app graph into a DOM fixture that has none
// of it.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";

const mocks = vi.hoisted(() => ({
  getActiveTabRoute: vi.fn<() => { kind: string } | null>(),
  // Opening a singleton is a round trip, so the toggle RESOLVES: `openSetting`
  // sequences the panel swap, the URL push and the highlight in its continuation,
  // because all three address a view the open has to produce first.
  toggleSettingsView: vi.fn(() => Promise.resolve()),
  setSettingsTab: vi.fn(),
  forceSettingsTab: vi.fn(),
  pushRoute: vi.fn(),
}));

vi.mock("./tabs.js", () => ({
  getActiveTabRoute: mocks.getActiveTabRoute,
  toggleSettingsView: mocks.toggleSettingsView,
  setSettingsTab: mocks.setSettingsTab,
}));
vi.mock("./settings-tabs.js", () => ({ forceSettingsTab: mocks.forceSettingsTab }));
vi.mock("./router.js", () => ({ pushRoute: mocks.pushRoute }));

import {
  highlightControl,
  openSetting,
  flushURLHighlight,
  _setPendingTargetForTest,
} from "./settings-highlight.js";

/** Drive the rAF retry loop the module uses to wait for a laid-out target. */
async function frames(n = 3): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => {
      requestAnimationFrame(() => {
        r(null);
      });
    });
  }
}

/** A detached host has no layout, so offsetParent is null for everything and the
 *  module's "is it laid out" probe would never pass. getClientRects is the other
 *  half of that probe; stubbing it per element is how a test says "this one is
 *  on screen" without faking a layout engine. */
function laidOut(e: HTMLElement): HTMLElement {
  e.getClientRects = () => [{}] as unknown as DOMRectList;
  return e;
}

let scrolled: string[] = [];

beforeEach(() => {
  vi.clearAllMocks();
  scrolled = [];
  _setPendingTargetForTest(null);
  document.body.innerHTML = "";
  Element.prototype.scrollIntoView = function (this: Element) {
    scrolled.push(this.id);
  };
});

afterEach(() => {
  _setPendingTargetForTest(null);
});

function control(id: string): HTMLInputElement {
  const e = document.createElement("input");
  e.id = id;
  e.type = "checkbox";
  document.body.appendChild(e);
  return laidOut(e) as HTMLInputElement;
}

describe("highlightControl", { timeout: testTimeoutFor(framesBudgetMs(25)) }, () => {
  it("scrolls the control into view and flashes a ring", async () => {
    const box = control("security-profile-list");
    highlightControl("security-profile-list");
    await frames(1);
    expect(scrolled).toEqual(["security-profile-list"]);
    expect(box.classList.contains("setting-flash")).toBe(true);
  });

  it("drops the flash class when the animation ends", async () => {
    const box = control("flag-tool-search");
    highlightControl("flag-tool-search");
    await frames(1);
    expect(box.classList.contains("setting-flash")).toBe(true);
    box.dispatchEvent(new Event("animationend"));
    expect(box.classList.contains("setting-flash")).toBe(false);
  });

  // The quiet-degradation contract. An id that will never resolve must not
  // throw, must not scroll anything, and must not leave the retry loop running:
  // callers name ids that can be renamed out from under them, and a jump that
  // merely fails to find one control is a better outcome than an error.
  it("does nothing for an unknown id", async () => {
    control("real-control");
    expect(() => {
      highlightControl("no-such-control");
    }).not.toThrow();
    await frames(25);
    expect(scrolled).toEqual([]);
    expect(document.querySelectorAll(".setting-flash")).toHaveLength(0);
  });

  it("ignores an empty id without scheduling a retry", async () => {
    highlightControl("");
    await frames(25);
    expect(scrolled).toEqual([]);
  });

  // The Tools / Permissions / Instructions panels populate from an async fetch,
  // so a deep-linked control can arrive several frames after the jump. The retry
  // is what makes the link work in that window instead of silently missing.
  it("waits for a target that does not exist yet", async () => {
    highlightControl("late-control");
    await frames(2);
    expect(scrolled).toEqual([]);

    control("late-control");
    await frames(2);
    expect(scrolled).toEqual(["late-control"]);
  });

  // A control inside a panel still carrying .hidden has no box, and
  // scrollIntoView on it is a silent no-op — so "present in the DOM" is not
  // enough to jump to.
  it("waits for a present target that has no layout box", async () => {
    const box = document.createElement("input");
    box.id = "boxless-control";
    document.body.appendChild(box);
    Object.defineProperty(box, "offsetParent", { value: null, configurable: true });
    box.getClientRects = () => [] as unknown as DOMRectList;

    highlightControl("boxless-control");
    await frames(2);
    expect(scrolled).toEqual([]);

    laidOut(box);
    await frames(2);
    expect(scrolled).toEqual(["boxless-control"]);
  });

  it("restarts the flash when the same control is targeted twice", async () => {
    const box = control("diagnostics-run");
    highlightControl("diagnostics-run");
    await frames(1);
    box.dispatchEvent(new Event("animationend"));
    expect(box.classList.contains("setting-flash")).toBe(false);
    highlightControl("diagnostics-run");
    await frames(1);
    expect(box.classList.contains("setting-flash")).toBe(true);
  });
});

// The flash has two cleanup paths — an animationend listener and a 2.5s backstop
// timeout — and under `prefers-reduced-motion` the timeout is the ONLY one, since
// the animation is suppressed and animationend never fires. No animationend is
// dispatched below, so this is that path.
describe("highlightControl repeat flashes", () => {
  beforeEach(() => {
    // setTimeout only: the retry loop runs on requestAnimationFrame, which
    // frames() awaits for real.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("does not let an older deadline clear a newer flash", async () => {
    const box = control("chat-retention-days");
    highlightControl("chat-retention-days");
    await frames(1);
    expect(box.classList.contains("setting-flash")).toBe(true);

    // Re-highlight while the first flash is still counting down.
    vi.advanceTimersByTime(1000);
    highlightControl("chat-retention-days");
    await frames(1);
    expect(box.classList.contains("setting-flash")).toBe(true);

    // Past the FIRST flash's deadline and inside the second's: the older timer
    // used to strip the ring here, 1.5s early.
    vi.advanceTimersByTime(1600);
    expect(box.classList.contains("setting-flash")).toBe(true);

    // The newer deadline still clears it, so nothing is left flashing forever.
    vi.advanceTimersByTime(1000);
    expect(box.classList.contains("setting-flash")).toBe(false);
  });
});

describe("openSetting", () => {
  // Opening the tab is a ROUND TRIP now, so everything that addresses the panel it
  // produces runs in the open's continuation: the panel swap, the URL push and the
  // highlight are all DOM writes against a view that has to exist first. Awaiting
  // the promise is therefore what a caller has to do, and this case asserts the
  // ordering rather than only the calls.
  it("opens the Settings view, then selects the tab, pushes the URL and marks the control", async () => {
    expect.assertions(5);
    mocks.getActiveTabRoute.mockReturnValue({ kind: "chat" });
    control("security-profile-list");

    openSetting("permissions", "security-profile-list");

    expect(mocks.toggleSettingsView).toHaveBeenCalledWith("permissions");
    // Nothing has reached the panel yet: the open has not resolved.
    expect(mocks.forceSettingsTab).not.toHaveBeenCalled();

    await mocks.toggleSettingsView.mock.results[0]?.value;
    expect(mocks.forceSettingsTab).toHaveBeenCalledWith("permissions");
    expect(mocks.pushRoute).toHaveBeenCalledWith({ kind: "settings", tab: "permissions" });
    await frames(1);
    expect(scrolled).toEqual(["security-profile-list"]);
  });

  // toggleSettingsView CLOSES an active singleton, so calling it from inside
  // Settings would dismiss the panel the link points at. That path stays
  // SYNCHRONOUS: reaching the panel is a round trip only when the tab has to be
  // opened.
  it("does not toggle the view when Settings is already active", async () => {
    expect.assertions(3);
    mocks.getActiveTabRoute.mockReturnValue({ kind: "settings" });
    control("notify-toggle");

    openSetting("general", "notify-toggle");

    expect(mocks.toggleSettingsView).not.toHaveBeenCalled();
    expect(mocks.forceSettingsTab).toHaveBeenCalledWith("general");
    await frames(1);
    expect(scrolled).toEqual(["notify-toggle"]);
  });
});

describe("flushURLHighlight", () => {
  it("fires the captured target exactly once", async () => {
    control("flag-debug-logs");
    _setPendingTargetForTest("flag-debug-logs");

    flushURLHighlight();
    await frames(1);
    expect(scrolled).toEqual(["flag-debug-logs"]);

    // A later popstate back to the same URL must not re-flash a control the
    // reader has already been shown.
    flushURLHighlight();
    await frames(2);
    expect(scrolled).toEqual(["flag-debug-logs"]);
  });

  it("is a no-op when the page load carried no highlight", async () => {
    control("flag-debug-logs");
    flushURLHighlight();
    await frames(2);
    expect(scrolled).toEqual([]);
  });
});
