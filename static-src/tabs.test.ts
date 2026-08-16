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
  ICON_PIN_FILLED: "",
}));
vi.mock("./ui-state.js", () => ({
  save: vi.fn(),
  load: vi.fn(() => ({ tab_order: [], pinned_tabs: [], active_view: "" })),
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
  openEditorView,
  closeTab,
  activateTab,
  renameTab,
  hasTab,
  getActiveTabId,
  setTabPinned,
  promoteTab,
  restoreTabState,
  _resetForTest,
} from "./tabs.js";
import { attachDrag, setReorderCallback } from "./tabs-drag.js";
import { save as uiSave, load as uiLoad } from "./ui-state.js";
import type { TabSpec } from "./tabs.js";

// The store's own reorderTabs, captured HERE because tabs.ts registers it once
// at module load and every suite below clears mocks in its beforeEach. Driving it
// is exactly what a completed drag does.
const commitDrop = vi.mocked(setReorderCallback).mock.calls[0]?.[0];

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

// Editor tabs are MULTI-INSTANCE, and nothing else makes them so: openTab dedupes
// on spec.id alone, and openEditorView derives that id from the path. That is the
// whole mechanism behind "N attachments open N tabs" — attachments.test.ts pins
// the other half, that each pill asks for its own path.
describe("openEditorView (multi-instance by path)", () => {
  const noop = (): void => undefined;

  // renderDOM is RAF-deferred, same as the keyboard suite below.
  function renderedTabIDs(): Promise<string[]> {
    return new Promise<string[]>((resolve) => {
      requestAnimationFrame(() => {
        const list = document.getElementById("tab-list");
        resolve(
          [...(list?.querySelectorAll<HTMLElement>("[data-tab-id]") ?? [])].map(
            (n) => n.dataset["tabId"] ?? "",
          ),
        );
      });
    });
  }

  it("opens one tab per distinct path", async () => {
    for (const p of ["src/a.ts", "docs/b.md", "out/shot.png"]) {
      openEditorView(p, noop);
    }
    expect(hasTab("editor:src/a.ts")).toBe(true);
    expect(hasTab("editor:docs/b.md")).toBe(true);
    expect(hasTab("editor:out/shot.png")).toBe(true);
    expect(getActiveTabId()).toBe("editor:out/shot.png");
    expect(await renderedTabIDs()).toEqual([
      "editor:src/a.ts",
      "editor:docs/b.md",
      "editor:out/shot.png",
    ]);
  });

  // Two pills for one file is the same file: re-activate, never a second tab.
  it("re-activates rather than duplicating when the same path opens twice", async () => {
    openEditorView("src/a.ts", noop);
    openEditorView("docs/b.md", noop);
    openEditorView("src/a.ts", noop);
    expect(getActiveTabId()).toBe("editor:src/a.ts");
    expect(await renderedTabIDs()).toEqual(["editor:src/a.ts", "editor:docs/b.md"]);
  });

  // The path travels in the ROUTE, never parsed back out of the id.
  it("names the tab by basename while the id keeps the whole path", async () => {
    openEditorView("a/b/c.ts", noop);
    expect(await renderedTabIDs()).toEqual(["editor:a/b/c.ts"]);
    const tab = document.querySelector('[data-tab-id="editor:a/b/c.ts"]');
    expect(tab?.textContent).toContain("c.ts");
    expect(tab?.textContent).not.toContain("a/b");
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

  // `skipOnClose` says the ROOT id was already deleted remotely (handlers/chat.ts
  // passes it on a chat_deleted event). It says nothing about a child, which is
  // its own persisted chat with its own bridge — so forwarding it left the child
  // tab gone while its turn and its agent process kept running unreachable.
  it("skips only the closed tab's teardown, not an owning child's", () => {
    const parentClose = vi.fn();
    const childClose = vi.fn();
    openTab({ ...makeTab("p"), onClose: parentClose });
    openTab({ ...childTab("c", "p"), onClose: childClose });
    closeTab("p", { skipOnClose: true });
    expect(hasTab("p")).toBe(false);
    expect(hasTab("c")).toBe(false);
    expect(parentClose).not.toHaveBeenCalled();
    expect(childClose).toHaveBeenCalledTimes(1);
  });

  // The flag must not reach a grandchild either, and an owns:false child still
  // skips its own teardown — through its ownership check, not through the flag.
  it("tears down every owning descendant of a remotely deleted parent", () => {
    const order: string[] = [];
    const viewClose = vi.fn();
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
    openTab({
      ...childTab("gc", "c"),
      onClose: () => {
        order.push("gc");
      },
    });
    openTab({ ...childTab("view", "p"), owns: false, onClose: viewClose });
    closeTab("p", { skipOnClose: true });
    expect(order).toEqual(["gc", "c"]);
    expect(viewClose).not.toHaveBeenCalled();
    for (const id of ["p", "c", "gc", "view"]) {
      expect(hasTab(id)).toBe(false);
    }
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

// ---------------------------------------------------------------------------
// Pinned tabs.
//
// A pin is a sort key plus a marker, and the sort lives in the ARRAY: the drag
// subsystem reads the dropped order back out of the DOM and the keyboard arrows
// walk the rendered children, so a render-time sort would make both disagree
// with what is stored.
// ---------------------------------------------------------------------------

describe("pinned tabs", () => {
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

  function lastSaved(): { tab_order?: string[]; pinned_tabs?: string[] } | undefined {
    const calls = vi.mocked(uiSave).mock.calls;
    return calls[calls.length - 1]?.[0] as
      { tab_order?: string[]; pinned_tabs?: string[] } | undefined;
  }

  beforeEach(() => {
    document.body.innerHTML = '<div id="tab-list"></div>';
    vi.clearAllMocks();
  });

  it("moves a pinned tab ahead of every unpinned one", async () => {
    openTab(makeTab("a"));
    openTab(makeTab("b"));
    openTab(makeTab("c"));
    setTabPinned("c", true);
    await expect(rowIDs()).resolves.toEqual(["c", "a", "b"]);
  });

  it("does not reshuffle the pinned run when another tab joins it", async () => {
    openTab(makeTab("a"));
    openTab(makeTab("b"));
    openTab(makeTab("c"));
    setTabPinned("c", true);
    await expect(rowIDs()).resolves.toEqual(["c", "a", "b"]);
    setTabPinned("a", true);
    // The partition is stable, so pinning `a` only guarantees it sits above the
    // unpinned tabs — it does not overtake a pin that was already ahead of it.
    // Reordering WITHIN the run is what drag is for.
    await expect(rowIDs()).resolves.toEqual(["c", "a", "b"]);
  });

  it("leaves a tab where it is when the pin is removed", async () => {
    openTab(makeTab("a"));
    openTab(makeTab("b"));
    setTabPinned("b", true);
    await expect(rowIDs()).resolves.toEqual(["b", "a"]);
    setTabPinned("b", false);
    await expect(rowIDs()).resolves.toEqual(["b", "a"]);
  });

  it("opens a new unpinned tab after the pinned run", async () => {
    openTab(makeTab("a"));
    setTabPinned("a", true);
    openTab(makeTab("b"));
    await expect(rowIDs()).resolves.toEqual(["a", "b"]);
  });

  // The partition is what enforces this: a drop commits through reorderTabs,
  // which re-partitions, so the illegal position snaps back rather than being
  // refused mid-drag inside the drag subsystem's index arithmetic.
  it("cannot be dragged below an unpinned tab", async () => {
    openTab(makeTab("a"));
    openTab(makeTab("b"));
    openTab(makeTab("c"));
    setTabPinned("a", true);
    expect(commitDrop).toBeTypeOf("function");
    commitDrop?.(["b", "c", "a"]);
    await expect(rowIDs()).resolves.toEqual(["a", "b", "c"]);
  });

  it("still honours a drag that reorders within the pinned run", async () => {
    openTab(makeTab("a"));
    openTab(makeTab("b"));
    openTab(makeTab("c"));
    setTabPinned("a", true);
    setTabPinned("b", true);
    commitDrop?.(["b", "a", "c"]);
    await expect(rowIDs()).resolves.toEqual(["b", "a", "c"]);
  });

  it("carries a parent's children with it", async () => {
    openTab(makeTab("p"));
    openTab(childTab("kid", "p"));
    openTab(makeTab("other"));
    setTabPinned("p", true);
    await expect(rowIDs()).resolves.toEqual(["p", "kid", "other"]);
    setTabPinned("other", true);
    // `other` is pinned second, so it lands after the whole `p` group rather
    // than between a parent and its child.
    await expect(rowIDs()).resolves.toEqual(["p", "kid", "other"]);
  });

  // Nested side chats are reachable: the transcript context menu parents a new
  // side conversation on the ACTIVE chat, which may itself be one. A grouping
  // that only recognised a DIRECT child made the grandchild an orphan top-level
  // group, so pinning any other tab could sort it away from the tab its own
  // parentId names.
  it("carries a whole descendant tree, not just direct children", async () => {
    openTab(makeTab("p"));
    openTab(childTab("kid", "p"));
    openTab(childTab("grandkid", "kid"));
    openTab(makeTab("other"));
    setTabPinned("p", true);
    await expect(rowIDs()).resolves.toEqual(["p", "kid", "grandkid", "other"]);
    // The second pin is what exposed it: an orphaned grandchild group sat
    // BETWEEN two pinned groups, so the partition hoisted `other` over it and
    // the grandchild ended up behind a stranger, away from the tab it names.
    setTabPinned("other", true);
    await expect(rowIDs()).resolves.toEqual(["p", "kid", "grandkid", "other"]);
  });

  // Same assumption, second site: a drop (and the boot restore) names top-level
  // ids only, so the walk that reproduces the strip has to descend.
  it("keeps a nested descendant behind its parent through a reorder", async () => {
    openTab(makeTab("p"));
    openTab(childTab("kid", "p"));
    openTab(childTab("grandkid", "kid"));
    openTab(makeTab("other"));
    commitDrop?.(["p", "other"]);
    await expect(rowIDs()).resolves.toEqual(["p", "kid", "grandkid", "other"]);
  });

  // A sub-tab's position is its parent's, the same rule that denies it a drag
  // handle.
  it("refuses to pin a sub-tab", async () => {
    openTab(makeTab("p"));
    openTab(childTab("kid", "p"));
    openTab(makeTab("other"));
    setTabPinned("kid", true);
    await expect(rowIDs()).resolves.toEqual(["p", "kid", "other"]);
    expect(lastSaved()?.pinned_tabs).toEqual([]);
  });

  it("marks the pinned row for the pin glyph", async () => {
    openTab(makeTab("a"));
    setTabPinned("a", true);
    await paint();
    const row = document.querySelector<HTMLElement>('[data-tab-id="a"]');
    expect(row?.classList.contains("tab-pinned")).toBe(true);
    // The glyph itself is decorative; the announced state is the .sr-only word.
    expect(row?.querySelector(".tab-pin .sr-only")?.textContent).toBe("Pinned");
    setTabPinned("a", false);
    await paint();
    expect(
      document.querySelector<HTMLElement>('[data-tab-id="a"]')?.classList.contains("tab-pinned"),
    ).toBe(false);
  });

  it("persists pins as their own list beside tab_order", () => {
    openTab(makeTab("a"));
    openTab(makeTab("b"));
    setTabPinned("b", true);
    expect(lastSaved()?.pinned_tabs).toEqual(["b"]);
    expect(lastSaved()?.tab_order).toEqual(["b", "a"]);
  });

  // The reload path: pins are stamped from the snapshot BEFORE the reorder, or
  // they would be recorded without moving their tabs.
  it("survives a reload", async () => {
    vi.mocked(uiLoad).mockReturnValueOnce({
      tab_order: ["a", "b", "c"],
      pinned_tabs: ["c"],
      active_view: "",
    } as never);
    _resetForTest();
    document.body.innerHTML = '<div id="tab-list"></div>';
    openTab(makeTab("a"));
    openTab(makeTab("b"));
    openTab(makeTab("c"));
    restoreTabState();
    await expect(rowIDs()).resolves.toEqual(["c", "a", "b"]);
    expect(lastSaved()?.pinned_tabs).toEqual(["c"]);
  });

  it("restores a pin even when no order was saved", async () => {
    vi.mocked(uiLoad).mockReturnValueOnce({
      tab_order: [],
      pinned_tabs: ["b"],
      active_view: "",
    } as never);
    _resetForTest();
    document.body.innerHTML = '<div id="tab-list"></div>';
    openTab(makeTab("a"));
    openTab(makeTab("b"));
    restoreTabState();
    await expect(rowIDs()).resolves.toEqual(["b", "a"]);
  });
});

// ---------------------------------------------------------------------------
// Promote: a sub-tab becomes a top-level tab.
// ---------------------------------------------------------------------------

describe("promoteTab", () => {
  function paint(): Promise<void> {
    return new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  }

  beforeEach(() => {
    document.body.innerHTML = '<div id="tab-list"></div>';
    vi.clearAllMocks();
  });

  it("clears the parent and joins tab_order", () => {
    openTab(makeTab("p"));
    openTab(childTab("kid", "p"));
    const before = vi.mocked(uiSave).mock.calls.at(-1)?.[0] as { tab_order?: string[] };
    expect(before.tab_order).toEqual(["p"]);
    promoteTab("kid");
    const after = vi.mocked(uiSave).mock.calls.at(-1)?.[0] as { tab_order?: string[] };
    expect(after.tab_order).toEqual(["p", "kid"]);
  });

  it("survives its former parent's close", () => {
    openTab(makeTab("p"));
    openTab(childTab("kid", "p"));
    promoteTab("kid");
    closeTab("p");
    expect(hasTab("kid")).toBe(true);
  });

  it("drops the indent and gains a drag handle", async () => {
    openTab(makeTab("p"));
    openTab(childTab("kid", "p"));
    await paint();
    // One handle so far: the parent's. The child had none.
    expect(vi.mocked(attachDrag)).toHaveBeenCalledTimes(1);
    promoteTab("kid");
    await paint();
    const row = document.querySelector<HTMLElement>('[data-tab-id="kid"]');
    expect(row).not.toBeNull();
    expect(row?.classList.contains("tab-child")).toBe(false);
    // The node is rebuilt on promote precisely because attachTabInteraction
    // decides draggability once, at element creation.
    expect(vi.mocked(attachDrag)).toHaveBeenCalledTimes(2);
  });

  it("is a no-op on a top-level tab", () => {
    openTab(makeTab("a"));
    const before = vi.mocked(uiSave).mock.calls.length;
    promoteTab("a");
    expect(vi.mocked(uiSave).mock.calls.length).toBe(before);
  });
});
