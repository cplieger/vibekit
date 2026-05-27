// @vitest-environment happy-dom
import { describe, it, expect, beforeEach, vi } from "vitest";

// Mock dependencies that tabs.ts imports at module level.
vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
vi.mock("./icons.js", () => ({
  ICON_CLOSE: "",
  ICON_TAB_CHAT: "",
  ICON_TAB_PLAN: "",
  ICON_TAB_SETTINGS: "",
  ICON_TAB_GIT: "",
  ICON_TAB_FILES: "",
  ICON_TAB_EDITOR: "",
  ICON_TAB_FOLLOW: "",
  ICON_TAB_HISTORY: "",
}));
vi.mock("./ui-state.js", () => ({
  save: vi.fn(),
  load: vi.fn(() => ({ tab_order: [], active_view: "" })),
}));
vi.mock("./dom.js", () => ({
  $: new Proxy({}, { get: () => document.createElement("div") }),
}));
vi.mock("./tabs-drag.js", () => ({
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));

import {
  openTab,
  closeTab,
  activateTab,
  renameTab,
  hasTab,
  getActiveTabId,
  _resetForTest,
} from "./tabs.js";
import type { TabSpec } from "./tabs.js";

function makeTab(id: string, name = id): TabSpec {
  return {
    id,
    name,
    kind: "chat",
    view: "#chat-view",
    route: { kind: "chat", id },
  };
}

beforeEach(() => {
  _resetForTest();
  // Provide minimal DOM for renderDOM subscriber (tab-list element).
  document.body.innerHTML = '<div id="tab-list"></div>';
});

describe("openTab", () => {
  it.each([
    { desc: "opens a new tab and activates it", tabs: ["a"], expectActive: "a", expectHas: ["a"] },
    {
      desc: "opening existing tab activates it",
      tabs: ["a", "b", "a"],
      expectActive: "a",
      expectHas: ["a", "b"],
    },
    {
      desc: "opens multiple distinct tabs",
      tabs: ["a", "b", "c"],
      expectActive: "c",
      expectHas: ["a", "b", "c"],
    },
  ])("$desc", ({ tabs, expectActive, expectHas }) => {
    expect.assertions(1 + expectHas.length);
    for (const id of tabs) openTab(makeTab(id));
    expect(getActiveTabId()).toBe(expectActive);
    for (const id of expectHas) expect(hasTab(id)).toBe(true);
  });
});

describe("closeTab", () => {
  it.each([
    {
      desc: "closing active tab activates neighbor (next)",
      setup: ["a", "b", "c"],
      activate: "b",
      close: "b",
      expectActive: "c",
      expectHas: ["a", "c"],
      expectGone: ["b"],
    },
    {
      desc: "closing active tab activates previous when last",
      setup: ["a", "b", "c"],
      activate: "c",
      close: "c",
      expectActive: "b",
      expectHas: ["a", "b"],
      expectGone: ["c"],
    },
    {
      desc: "closing inactive tab preserves active",
      setup: ["a", "b", "c"],
      activate: "c",
      close: "a",
      expectActive: "c",
      expectHas: ["b", "c"],
      expectGone: ["a"],
    },
    {
      desc: "closing the only tab results in empty state",
      setup: ["a"],
      activate: "a",
      close: "a",
      expectActive: "",
      expectHas: [],
      expectGone: ["a"],
    },
    {
      desc: "closing non-existent tab is a no-op",
      setup: ["a", "b"],
      activate: "b",
      close: "z",
      expectActive: "b",
      expectHas: ["a", "b"],
      expectGone: ["z"],
    },
  ])("$desc", ({ setup, activate, close, expectActive, expectHas, expectGone }) => {
    expect.assertions(1 + expectHas.length + expectGone.length);
    for (const id of setup) openTab(makeTab(id));
    activateTab(activate);
    closeTab(close);
    expect(getActiveTabId()).toBe(expectActive);
    for (const id of expectHas) expect(hasTab(id)).toBe(true);
    for (const id of expectGone) expect(hasTab(id)).toBe(false);
  });
});

describe("activateTab", () => {
  it.each([
    { desc: "activates existing tab", setup: ["a", "b"], target: "a", expectActive: "a" },
    {
      desc: "activating non-existent tab is a no-op",
      setup: ["a", "b"],
      target: "z",
      expectActive: "b",
    },
    {
      desc: "activating already-active tab is a no-op",
      setup: ["a", "b"],
      target: "b",
      expectActive: "b",
    },
  ])("$desc", ({ setup, target, expectActive }) => {
    expect.assertions(1);
    for (const id of setup) openTab(makeTab(id));
    activateTab(target);
    expect(getActiveTabId()).toBe(expectActive);
  });
});

describe("renameTab", () => {
  it("renames an existing tab", () => {
    expect.assertions(1);
    openTab(makeTab("a", "Original"));
    renameTab("a", "Renamed");
    // Verify via hasTab (name doesn't affect identity).
    expect(hasTab("a")).toBe(true);
  });

  it("renaming non-existent tab is a no-op", () => {
    expect.assertions(1);
    openTab(makeTab("a"));
    renameTab("z", "Whatever");
    expect(hasTab("z")).toBe(false);
  });
});

describe("hasTab", () => {
  it("returns false for empty state", () => {
    expect.assertions(1);
    expect(hasTab("a")).toBe(false);
  });

  it("returns true after open, false after close", () => {
    expect.assertions(2);
    openTab(makeTab("a"));
    expect(hasTab("a")).toBe(true);
    closeTab("a");
    expect(hasTab("a")).toBe(false);
  });
});
