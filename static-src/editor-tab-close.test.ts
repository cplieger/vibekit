// ---------------------------------------------------------------------------
// Closing an editor tab, across the REAL tab store and the REAL editor openers.
//
// Both halves of one defect met here, so neither module alone could show it:
// `closeTab` fired `onClose` while the tab was still in the store, and the
// editor's `onClose` (closeEditorFile) ended in `closeTab`. The call re-entered,
// found the tab still present, fired `onClose` again, and recursed until the
// stack died — so ×, middle-click and Delete were all dead on every editor tab,
// and the file's client state was never released either.
//
// Nothing is mocked between the two: tabs.ts and editor-openers.ts are the real
// modules, and only the leaves they reach for (DOM registry, the file route, the
// router, the editor's paint surface) are stubbed. A mock of either side would
// have removed the loop along with the bug.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";

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
  ICON_PIN_FILLED: "",
}));
vi.mock("./ui-state.js", () => ({
  save: vi.fn(),
  load: vi.fn(() => ({ tab_order: [], pinned_tabs: [], active_view: "", editor_files: [] })),
}));
vi.mock("./tabs-drag.js", () => ({
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(() => Promise.resolve({ content: "hello", content_hash: "h" })),
}));
vi.mock("./editor-conflict.js", () => ({
  abortSuggestion: vi.fn(),
  clearSuggestionState: vi.fn(),
}));
vi.mock("./editor-modes.js", () => ({ restoreUI: vi.fn() }));
vi.mock("./editor-ui.js", () => ({
  showReadMode: vi.fn(),
  applyPendingLine: vi.fn(),
  fetchAgentLines: vi.fn(),
  pendingLines: new Map<string, number>(),
  clearAgentLineCache: vi.fn(),
}));
vi.mock("./actions/editor.js", () => ({
  loadDiff: { dispatch: () => ({ outcome: Promise.resolve({ status: "cancelled" }) }) },
}));
vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));

// One registry serving both modules: tabs.ts needs a document-attached #tab-list
// for its render walk, the editor needs its own paint surface, and every other
// getter is a throwaway.
vi.mock("./dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        if (prop === "tabList") {
          let tl = document.getElementById("tab-list");
          if (tl === null) {
            tl = document.createElement("div");
            tl.id = "tab-list";
            document.body.appendChild(tl);
          }
          return tl;
        }
        if (prop === "editorHighlight") {
          const pre = document.createElement("pre");
          document.createElement("div").appendChild(pre);
          return pre;
        }
        return document.createElement("div");
      },
    },
  ),
}));

const { clearAgentLineCache } = await import("./editor-ui.js");
const { openFile, closeEditorFile } = await import("./editor-openers.js");
const { closeTab, hasTab, editorTabID, getActiveTabId, _resetForTest } = await import("./tabs.js");
const { fileStates, setActiveFilePath } = await import("./editor-types.js");

const PATH = "/workspace/hello.sh";

beforeEach(() => {
  _resetForTest();
  fileStates.clear();
  setActiveFilePath("");
  document.body.innerHTML = '<div id="tab-list"></div>';
});

describe("closing an editor tab", () => {
  it("removes the tab on the first close", () => {
    expect.assertions(2);
    openFile(PATH);
    expect(hasTab(editorTabID(PATH))).toBe(true);
    closeTab(editorTabID(PATH));
    expect(hasTab(editorTabID(PATH))).toBe(false);
  });

  it("releases the file's client state exactly once", () => {
    // The recursion never reached this: the stack died before the store could
    // finish, so the buffer, the dirty-indicator effect and the pending-line
    // entry all leaked with the tab.
    expect.assertions(2);
    openFile(PATH);
    expect(fileStates.has(PATH)).toBe(true);
    closeTab(editorTabID(PATH));
    expect(fileStates.has(PATH)).toBe(false);
  });

  it("runs the teardown once, not once per re-entry", () => {
    // clearAgentLineCache is called from closeEditorFile and nowhere else on this
    // path, so its call count IS the teardown count. Under the loop it climbed
    // with the stack until the stack ran out.
    expect.assertions(1);
    openFile(PATH);
    vi.mocked(clearAgentLineCache).mockClear();
    closeTab(editorTabID(PATH));
    expect(vi.mocked(clearAgentLineCache)).toHaveBeenCalledTimes(1);
  });

  it("leaves no active tab behind when it was the only one", () => {
    expect.assertions(1);
    openFile(PATH);
    closeTab(editorTabID(PATH));
    expect(getActiveTabId()).toBe("");
  });

  it("closes only the named file when several are open", () => {
    expect.assertions(4);
    const other = "/workspace/other.ts";
    openFile(PATH);
    openFile(other);
    closeTab(editorTabID(PATH));
    expect(hasTab(editorTabID(PATH))).toBe(false);
    expect(fileStates.has(PATH)).toBe(false);
    expect(hasTab(editorTabID(other))).toBe(true);
    expect(fileStates.has(other)).toBe(true);
  });

  it("survives closeEditorFile being called directly, which closes no tab", () => {
    // The other direction of the same ownership rule: closeEditorFile drops the
    // file state and nothing else. A caller that wants the tab gone closes the
    // tab. Pinned so a future edit cannot quietly put closeTab back and reopen
    // the loop from this side.
    expect.assertions(2);
    openFile(PATH);
    closeEditorFile(PATH);
    expect(fileStates.has(PATH)).toBe(false);
    expect(hasTab(editorTabID(PATH))).toBe(true);
  });
});
