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
  ICON_TAB_RUN: "",
  ICON_TAB_EDITOR: "",
  ICON_TAB_HISTORY: "",
  ICON_TAB_DOCS: "",
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
  editorTabID,
  isEditorTabID,
  openEditorView,
  _resetForTest,
} from "./tabs.js";
import { attachDrag } from "./tabs-drag.js";
import { save as uiSave } from "./ui-state.js";
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

  // The store REMOVES a tab before notifying, and this is the property that
  // depends on it. onClose is a teardown callback, so a callback that closes
  // its own tab must find it already gone; notifying first made every such
  // callback an infinite loop. The editor's teardown did exactly that
  // (closeEditorFile ended in closeTab), so an editor tab could not be closed
  // by ×, middle-click or Delete — the click recursed until the stack died.
  it("a teardown that closes its own tab does not recurse", () => {
    expect.assertions(3);
    let closes = 0;
    openTab({
      ...makeTab("self-closing"),
      onClose: () => {
        closes++;
        closeTab("self-closing");
      },
    });
    closeTab("self-closing");
    expect(closes).toBe(1);
    expect(hasTab("self-closing")).toBe(false);
    expect(getActiveTabId()).toBe("");
  });

  it("a teardown observes the tab as already gone", () => {
    expect.assertions(1);
    let presentDuringTeardown = true;
    openTab({
      ...makeTab("t"),
      onClose: () => {
        presentDuringTeardown = hasTab("t");
      },
    });
    closeTab("t");
    expect(presentDuringTeardown).toBe(false);
  });

  it("a child teardown that closes the parent leaves the parent's onClose run once", () => {
    expect.assertions(3);
    let parentCloses = 0;
    openTab({ ...makeTab("p"), onClose: () => void parentCloses++ });
    openTab({
      ...makeTab("c"),
      parentId: "p",
      onClose: () => {
        closeTab("p");
      },
    });
    closeTab("p");
    expect(parentCloses).toBe(1);
    expect(hasTab("p")).toBe(false);
    expect(hasTab("c")).toBe(false);
  });
});

describe("editor tab ids", () => {
  it("round-trips a path and recognises its own ids", () => {
    expect.assertions(3);
    const id = editorTabID("/workspace/hello.sh");
    expect(id).toBe("editor:/workspace/hello.sh");
    expect(isEditorTabID(id)).toBe(true);
    expect(isEditorTabID("__settings__")).toBe(false);
  });

  it("openEditorView keys its tab by editorTabID", () => {
    expect.assertions(1);
    openEditorView("/workspace/hello.sh", () => undefined);
    expect(hasTab(editorTabID("/workspace/hello.sh"))).toBe(true);
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

// ---------------------------------------------------------------------------
// Sub-tabs.
//
// A sub-tab is a generic property of the tab system (`TabSpec.parentId`), not a
// chat feature: the indent used to key off the chat store's `parent_chat_id`,
// which meant only chats could ever have children and coupled the tab module to
// one data model. Nothing below mentions a chat, a run or a rewind.
// ---------------------------------------------------------------------------

function childTab(id: string, parentId: string, over: Partial<TabSpec> = {}): TabSpec {
  return { ...makeTab(id), parentId, ...over };
}

describe("sub-tabs", () => {
  // renderDOM is RAF-deferred, so a DOM assertion has to wait for it.
  function paint(): Promise<void> {
    return new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  }

  async function rowIDs(): Promise<string[]> {
    await paint();
    return [...document.querySelectorAll<HTMLElement>("#tab-list .tab")].map(
      (e) => e.dataset["tabId"] ?? "",
    );
  }

  beforeEach(() => {
    document.body.innerHTML = '<div id="tab-list"></div>';
    vi.clearAllMocks();
  });

  it("sorts a child immediately after its parent, not at the end", async () => {
    openTab(makeTab("p"));
    openTab(makeTab("other"));
    openTab(childTab("c", "p"));
    await expect(rowIDs()).resolves.toEqual(["p", "c", "other"]);
  });

  it("keeps several children in creation order under one parent", async () => {
    openTab(makeTab("p"));
    openTab(makeTab("other"));
    openTab(childTab("c1", "p"));
    openTab(childTab("c2", "p"));
    await expect(rowIDs()).resolves.toEqual(["p", "c1", "c2", "other"]);
  });

  it("indents a child and leaves a top-level tab flat", async () => {
    openTab(makeTab("p"));
    openTab(childTab("c", "p"));
    await paint();
    const rows = [...document.querySelectorAll<HTMLElement>("#tab-list .tab")];
    const child = rows.find((r) => r.dataset["tabId"] === "c");
    const parent = rows.find((r) => r.dataset["tabId"] === "p");
    expect(child?.classList.contains("tab-child")).toBe(true);
    expect(parent?.classList.contains("tab-child")).toBe(false);
  });

  // A tab nobody can see is worse than a tab in the wrong place.
  it("falls back to top-level when the parent is not open", async () => {
    openTab(childTab("orphan", "missing"));
    expect(hasTab("orphan")).toBe(true);
    await expect(rowIDs()).resolves.toEqual(["orphan"]);
  });

  it("closes children with their parent", () => {
    openTab(makeTab("p"));
    openTab(childTab("c1", "p"));
    openTab(childTab("c2", "p"));
    closeTab("p");
    expect(hasTab("p")).toBe(false);
    expect(hasTab("c1")).toBe(false);
    expect(hasTab("c2")).toBe(false);
  });

  // Children first, so a child's teardown never runs against a parent that has
  // already gone.
  it("tears down children before the parent", () => {
    const order: string[] = [];
    openTab({
      ...makeTab("p"),
      onClose: () => {
        order.push("p");
      },
    });
    openTab({
      ...childTab("c", "p"),
      onClose: () => {
        order.push("c");
      },
    });
    closeTab("p");
    expect(order).toEqual(["c", "p"]);
  });

  it("closing a child leaves the parent alone", () => {
    openTab(makeTab("p"));
    openTab(childTab("c", "p"));
    closeTab("c");
    expect(hasTab("p")).toBe(true);
    expect(hasTab("c")).toBe(false);
  });

  // `owns: false` is the view case: dismissing a view must not kill the work it
  // was watching.
  it("does not tear down a tab that owns nothing", () => {
    const onClose = vi.fn();
    openTab({ ...makeTab("view"), owns: false, onClose });
    closeTab("view");
    expect(hasTab("view")).toBe(false);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("still removes an owns:false child when its parent closes", () => {
    const onClose = vi.fn();
    openTab(makeTab("p"));
    openTab({ ...childTab("c", "p"), owns: false, onClose });
    closeTab("p");
    expect(hasTab("c")).toBe(false);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("tears down an owning tab as before", () => {
    const onClose = vi.fn();
    openTab({ ...makeTab("t"), onClose });
    closeTab("t");
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // A child's position is derived from its parent, so persisting it would let a
  // restore place a child away from its parent. Asserted against what the
  // persistence subscriber WRITES, not against getSavedTabState — that reads the
  // boot snapshot, which is deliberately frozen before the first save.
  it("persists top-level ids only", () => {
    openTab(makeTab("p"));
    openTab(childTab("c", "p"));
    openTab(makeTab("other"));
    const calls = vi.mocked(uiSave).mock.calls;
    const last = calls[calls.length - 1]?.[0] as { tab_order?: string[] } | undefined;
    expect(last?.tab_order).toEqual(["p", "other"]);
  });

  it("is not independently draggable", async () => {
    openTab(makeTab("p"));
    openTab(childTab("c", "p"));
    await paint();
    // attachDrag runs once per DRAGGABLE row; the child must not get a handle,
    // because its position is its parent's rather than its own.
    expect(vi.mocked(attachDrag)).toHaveBeenCalledTimes(1);
  });
});
