// ---------------------------------------------------------------------------
// The toolbar's find affordance must RE-DERIVE when a page registers and when
// the active tab changes, because both happen after it has already painted.
//
// `tabs.activateTab` announces the switch BEFORE it calls `onShow`, and every
// page registers its find from inside its own lazily-imported module — so the
// button is painted from the registry one module-fetch before the page arrives
// to fill it. Two defects came out of that ordering, and both were invisible to
// the existing find-dispatch tests, which call the derivation directly:
//
//   - /docs: the funnel was absent on the first open of a page session, and
//     present only after a tab round-trip forced a repaint through the
//     BUS_TAB_CHANGED fallback. It read as random.
//   - /git/sources: BUS_TAB_CHANGED is deduped on the active tab ID, and a
//     Changes -> Sources switch keeps `__git__` active, so nothing repainted.
//     The button stayed visible over a tab that registers no box, and clicking
//     it did nothing. The reverse was worse: deep-link to Sources, switch to
//     Changes, and the filter was unreachable from the button.
//
// So these assert the DEPENDENCY, not the answer: a derivation run inside an
// effect must re-run on a registration and on a tab change. Anything that reads
// the registry only inside the `page` branch fails the first, and anything that
// leans on the bus for the tab fails the second.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import { effect, signal } from "@cplieger/reactive";
import type { TabKind } from "./tabs.js";
import type { PageFind } from "./find-registry.js";

/** The active tab kind, and the signal the real tabs module trips on a switch.
 *  Mocked rather than driving the real store, so the test states its own
 *  dependency instead of inheriting the whole tab system. */
let activeKind: TabKind | null = "chat";
const tabSignal = signal(0);

vi.mock("./tabs.js", () => ({
  getActiveTabKind: () => {
    // Mirrors the production getter: subscribe, then read.
    void tabSignal.value;
    return activeKind;
  },
}));
vi.mock("./find-in-chat.js", () => ({
  handleFindHotkey: () => false,
  toggleChatFind: () => undefined,
}));
vi.mock("./files-search.js", () => ({
  handleFindInFilesHotkey: () => false,
  toggleFilesSearch: () => undefined,
}));
vi.mock("./editor-find.js", () => ({
  handleEditorFindHotkey: () => false,
  toggleEditorFind: () => undefined,
  editorFindAvailable: () => false,
}));

const { findAffordanceForActiveTab } = await import("./find-dispatch.js");
const { registerFind, _resetFindRegistry } = await import("./find-registry.js");

function setActive(kind: TabKind | null): void {
  activeKind = kind;
  tabSignal.value = tabSignal.value + 1;
}

/** A find whose availability the test controls. */
function fakeFind(available: boolean): PageFind {
  return {
    open: () => true,
    toggle: () => undefined,
    focused: () => false,
    kind: () => "filter",
    available: () => available,
  };
}

/** Run the derivation inside an effect and record every value it produced. */
function trackAffordance(): { runs: { available: boolean; kind: string }[]; stop: () => void } {
  const runs: { available: boolean; kind: string }[] = [];
  const stop = effect(() => {
    runs.push(findAffordanceForActiveTab());
  });
  return { runs, stop };
}

describe("the find affordance re-derives when a page registers", () => {
  beforeEach(() => {
    _resetFindRegistry();
    activeKind = "chat";
  });

  it("repaints when a page registers AFTER the first paint, from a chat tab", () => {
    // The boot shape: a chat is active, so the derivation never takes the `page`
    // branch. If the registry read is inside that branch the effect subscribes to
    // nothing and this registration is invisible to it.
    const { runs, stop } = trackAffordance();
    expect(runs).toHaveLength(1);

    setActive("docs");
    registerFind("docs", fakeFind(true));

    expect(runs.at(-1)?.available).toBe(true);
    stop();
  });

  it("repaints when the page registers while it is ALREADY the active tab", () => {
    // activateTab announces before onShow, so this is the real ordering.
    setActive("docs");
    const { runs, stop } = trackAffordance();
    expect(runs.at(-1)?.available).toBe(false); // nothing registered yet

    registerFind("docs", fakeFind(true));
    expect(runs.at(-1)?.available).toBe(true);
    stop();
  });

  it("re-registering the SAME find object does not churn the effect", () => {
    // A page may register on every mount; that must not repaint the toolbar.
    const find = fakeFind(true);
    setActive("docs");
    registerFind("docs", find);
    const { runs, stop } = trackAffordance();
    const before = runs.length;

    registerFind("docs", find);
    registerFind("docs", find);
    expect(runs).toHaveLength(before);
    stop();
  });
});

describe("the find affordance re-derives when the active tab changes", () => {
  beforeEach(() => {
    _resetFindRegistry();
    activeKind = "chat";
  });

  it("follows a tab switch without any bus event", () => {
    registerFind("docs", fakeFind(true));
    const { runs, stop } = trackAffordance();

    setActive("docs");
    expect(runs.at(-1)?.available).toBe(true);

    setActive("settings"); // registers nothing
    expect(runs.at(-1)?.available).toBe(false);
    stop();
  });

  it("follows an `available` predicate that flips under one tab id", () => {
    // The git view's shape: ONE tab whose sub-tab decides the answer, so no tab
    // id changes and the bus fallback is silent. The predicate is a signal read
    // in production (readGitTab); here the tab signal stands in for it.
    let sourcesActive = false;
    registerFind("git", {
      open: () => true,
      toggle: () => undefined,
      focused: () => false,
      kind: () => "filter",
      available: () => {
        void tabSignal.value;
        return !sourcesActive;
      },
    });
    setActive("git");
    const { runs, stop } = trackAffordance();
    expect(runs.at(-1)?.available).toBe(true); // Changes

    sourcesActive = true;
    tabSignal.value = tabSignal.value + 1; // the sub-tab switch
    expect(runs.at(-1)?.available).toBe(false); // Sources: no box, no button

    sourcesActive = false;
    tabSignal.value = tabSignal.value + 1;
    expect(runs.at(-1)?.available).toBe(true); // and back, reachable again
    stop();
  });
});
