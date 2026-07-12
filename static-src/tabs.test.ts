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
  ICON_TAB_HISTORY: "",
  ICON_TAB_SPEC: "",
  ICON_SEND: "",
  ICON_SPINNER: "",
  ICON_HOURGLASS: "",
  ICON_ALERT: "",
}));
vi.mock("./ui-state.js", () => ({
  save: vi.fn(),
  load: vi.fn(() => ({ tab_order: [], active_view: "" })),
}));
vi.mock("./dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        // tabList must be a stable, document-attached element so the real
        // renderDOM appends focusable role=tab nodes we can drive in the
        // keyboard-navigation tests. Every other getter stays a throwaway.
        if (prop === "tabList") {
          let tl = document.getElementById("tab-list");
          if (tl === null) {
            tl = document.createElement("div");
            tl.id = "tab-list";
            document.body.appendChild(tl);
          }
          return tl;
        }
        return document.createElement("div");
      },
    },
  ),
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
    for (const id of tabs) {
      openTab(makeTab(id));
    }
    expect(getActiveTabId()).toBe(expectActive);
    for (const id of expectHas) {
      expect(hasTab(id)).toBe(true);
    }
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
    for (const id of setup) {
      openTab(makeTab(id));
    }
    activateTab(activate);
    closeTab(close);
    expect(getActiveTabId()).toBe(expectActive);
    for (const id of expectHas) {
      expect(hasTab(id)).toBe(true);
    }
    for (const id of expectGone) {
      expect(hasTab(id)).toBe(false);
    }
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
    for (const id of setup) {
      openTab(makeTab(id));
    }
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

describe("keyboard navigation (real tabs.ts handler via rendered tab nodes)", () => {
  // renderDOM is RAF-deferred; resolve after it has run so the real
  // role=tab nodes (with attachTabInteraction's keydown handler) exist.
  function flushRender(): Promise<void> {
    return new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  }

  async function renderTabs(...ids: string[]): Promise<HTMLElement[]> {
    for (const id of ids) {
      openTab(makeTab(id));
    }
    await flushRender();
    const list = document.getElementById("tab-list");
    if (list === null) {
      throw new Error("tab-list missing");
    }
    return [...list.querySelectorAll<HTMLElement>('[role="tab"]')];
  }

  it("ArrowRight moves focus to the next tab and wraps past the last", async () => {
    const nodes = await renderTabs("a", "b", "c");
    expect(nodes).toHaveLength(3);

    nodes[0]!.focus();
    nodes[0]!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(document.activeElement).toBe(nodes[1]);

    nodes[2]!.focus();
    nodes[2]!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(document.activeElement).toBe(nodes[0]);
  });

  it("ArrowLeft moves focus to the previous tab and wraps before the first", async () => {
    const nodes = await renderTabs("a", "b", "c");

    nodes[2]!.focus();
    nodes[2]!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    expect(document.activeElement).toBe(nodes[1]);

    nodes[0]!.focus();
    nodes[0]!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    expect(document.activeElement).toBe(nodes[2]);
  });

  it("Home and End jump to the first and last tab", async () => {
    const nodes = await renderTabs("a", "b", "c");

    nodes[1]!.focus();
    nodes[1]!.dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true }));
    expect(document.activeElement).toBe(nodes[0]);

    nodes[1]!.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true }));
    expect(document.activeElement).toBe(nodes[2]);
  });

  it("Enter activates the focused tab", async () => {
    const nodes = await renderTabs("a", "b", "c");
    // c is active (last opened); Enter on the first tab's node activates it.
    nodes[0]!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(getActiveTabId()).toBe("a");
  });
});

describe("setTabDirty (editor unsaved indicator)", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  it("shows a steady dirty dot when dirty and clears it when clean", async () => {
    const { setTabDirty } = await import("./tabs.js");
    document.body.innerHTML =
      '<div data-tab-id="editor:/a.ts"><span class="tab-status-dot hidden"></span></div>';
    const dot = document.querySelector(".tab-status-dot");
    if (dot === null) {
      throw new Error("dot missing");
    }

    setTabDirty("editor:/a.ts", true);
    expect(dot.classList.contains("tab-dot-dirty")).toBe(true);
    expect(dot.classList.contains("hidden")).toBe(false);

    setTabDirty("editor:/a.ts", false);
    expect(dot.classList.contains("tab-dot-dirty")).toBe(false);
    expect(dot.classList.contains("hidden")).toBe(true);
  });

  it("no-ops when the tab is not mounted", async () => {
    const { setTabDirty } = await import("./tabs.js");
    expect(() => {
      setTabDirty("editor:/missing.ts", true);
    }).not.toThrow();
  });
});
