// ---------------------------------------------------------------------------
// Tests for settings-highlight.ts (D115): the ?highlight=<id> deep link.
//
// What this module still owns after the scroll-and-mark primitive moved to
// flash-target.ts: the one-shot `?highlight=` read, `openSetting`'s single path
// (open the view, then swap the panel, push the URL and mark the control), and
// `highlightControl`'s own `id === ""` guard — an empty id means the CALLER had
// no target, which `flashTarget` would spend its whole frame budget failing to
// find. The marking itself, and its failure modes, are flash-target.test.ts's.
//
// tabs.ts and settings-tabs.ts are mocked at the boundary: opening the Settings
// view is a command this module issues, not behaviour it owns, and importing the
// real tab store would drag the whole app graph into a DOM fixture that has none
// of it.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";

const mocks = vi.hoisted(() => ({
  // Opening a singleton is a round trip, so the open RESOLVES: `openSetting`
  // sequences the panel swap, the URL push and the highlight in its continuation,
  // because all three address a view the open has to produce first.
  openSettingsView: vi.fn(() => Promise.resolve()),
  setSettingsTab: vi.fn(),
  forceSettingsTab: vi.fn(),
  pushRoute: vi.fn(),
}));

vi.mock("./tabs.js", () => ({
  openSettingsView: mocks.openSettingsView,
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

/** Drive the rAF retry loop the primitive uses to wait for a laid-out target. */
async function frames(n = 3): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => {
      requestAnimationFrame(() => {
        r(null);
      });
    });
  }
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
  return e;
}

describe("highlightControl", { timeout: testTimeoutFor(framesBudgetMs(25)) }, () => {
  it("ignores an empty id without scheduling a retry", async () => {
    highlightControl("");
    await frames(25);
    expect(scrolled).toEqual([]);
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
    control("security-profile-list");

    openSetting("permissions", "security-profile-list");

    expect(mocks.openSettingsView).toHaveBeenCalledWith("permissions");
    // Nothing has reached the panel yet: the open has not resolved.
    expect(mocks.forceSettingsTab).not.toHaveBeenCalled();

    await mocks.openSettingsView.mock.results[0]?.value;
    expect(mocks.forceSettingsTab).toHaveBeenCalledWith("permissions");
    expect(mocks.pushRoute).toHaveBeenCalledWith({ kind: "settings", tab: "permissions" });
    await frames(1);
    expect(scrolled).toEqual(["security-profile-list"]);
  });

  // ONE path, and this is what pins it: the function reads no route and takes no
  // already-there branch, so a jump made while Settings is the active view runs the
  // whole sequence again rather than returning early. It used to consult
  // `getActiveTabRoute` and skip the open, which is what let a link to a named
  // setting dismiss the panel it pointed at.
  it("runs the same sequence on a second call", async () => {
    control("chat-retention-days");

    openSetting("general", "chat-retention-days");
    await mocks.openSettingsView.mock.results[0]?.value;
    await frames(1);
    expect(scrolled).toEqual(["chat-retention-days"]);

    openSetting("general", "chat-retention-days");
    await mocks.openSettingsView.mock.results[1]?.value;
    await frames(1);
    expect(mocks.openSettingsView).toHaveBeenCalledTimes(2);
    expect(mocks.forceSettingsTab).toHaveBeenCalledTimes(2);
    expect(mocks.pushRoute).toHaveBeenCalledTimes(2);
    expect(scrolled).toEqual(["chat-retention-days", "chat-retention-days"]);
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
