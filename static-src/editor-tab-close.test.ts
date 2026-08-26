// ---------------------------------------------------------------------------
// Closing an editor tab, across the REAL tab store and the REAL editor openers.
//
// Both halves of one defect met here, so neither module alone could show it:
// `closeTab` fired the teardown while the tab was still in the store, and the
// editor's teardown (closeEditorFile) ended in `closeTab`. The call re-entered,
// found the tab still present, tore down again, and recursed until the stack died
// — so ×, middle-click and Delete were all dead on every editor tab, and the
// file's client state was never released either.
//
// Nothing is mocked between the two: tabs.ts and editor-openers.ts are the real
// modules, and only the leaves they reach for (DOM registry, the file route, the
// router, the editor's paint surface) are stubbed. A mock of either side would
// have removed the loop along with the bug.
//
// The tab set is server-owned now, so the round trip is real too: the fake
// collection in `__test-helpers__/tabs-server.ts` answers the mutations and emits
// the frames, and the editor's teardown is reached through the same
// `registerTabOpeners` seam the composition root uses rather than through a
// callback a caller passes.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
vi.mock("./icons.js", () => ({
  ICON_CLOSE: "",
  ICON_TAB_CHAT: "",
  ICON_TAB_SETTINGS: "",
  ICON_TAB_GIT: "",
  ICON_TAB_FILES: "",
  ICON_TAB_RUN: "",
  ICON_TAB_EDITOR: "",
  ICON_TAB_HISTORY: "",
  ICON_TAB_DOCS: "",
  ICON_PIN_FILLED: "",
  ICON_ALERT: "",
  ICON_SEND: "",
  ICON_SPINNER: "",
  ICON_HOURGLASS: "",
}));
vi.mock("./tabs-drag.js", () => ({
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
// `apiGet` is the file read; `apiGetTyped` is tabs-sync's collection read, which
// the harness answers off the fake collection.
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => ({
    apiGet: vi.fn(() => Promise.resolve({ content: "hello", content_hash: "h" })),
    apiGetTyped: m.tabListRead(),
  })),
);
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./device-view.js", () => {
  let active = "";
  return {
    activeView: vi.fn(() => active),
    setActiveView: vi.fn((id: string) => {
      active = id;
    }),
  };
});
// The two leaf stores the tab factory reads for a display NAME. An editor tab's
// label is its path's last segment, so neither is consulted for this suite's tabs.
vi.mock("./store.js", () => ({ get: vi.fn(() => undefined) }));
vi.mock("./run-store.js", () => ({ peekRunState: vi.fn(() => undefined) }));
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));
vi.mock("./editor-conflict.js", () => ({
  abortSuggestion: vi.fn(),
  clearSuggestionState: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./editor-modes.js", () => ({ restoreUI: vi.fn() }));
vi.mock("./editor-ui.js", () => ({
  showReadMode: vi.fn(),
  applyPendingLine: vi.fn(),
  fetchAgentLines: vi.fn(),
  pendingLines: new Map<string, number>(),
  clearAgentLineCache: vi.fn(),
  updateGutter: vi.fn(),
}));
vi.mock("./actions/editor.js", () => ({
  loadDiff: { dispatch: () => ({ outcome: Promise.resolve({ status: "cancelled" }) }) },
}));

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
const { openFile, closeEditorFile, activateFile } = await import("./editor-openers.js");
const { closeTab, hasTab, tabIdFor, getActiveTabId, _resetForTest } = await import("./tabs.js");
const { registerTabOpeners, _resetTabOpenersForTest } = await import("./tab-materialize.js");
const { ingestTabsChanged, listTabs, _resetTabsSyncForTest } = await import("./tabs-sync.js");
const { resetActionFramework } = await import("./actions/__test-helpers__/action-test-setup.js");
const { bindTabsSync, tabServer, settleTabs } = await import("./__test-helpers__/tabs-server.js");
const { fileStates, setActiveFilePath } = await import("./editor-types.js");

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

const PATH = "/workspace/hello.sh";

/** The projection's id for a path, read back rather than composed: ids are opaque
 *  and server-minted, so `editor:<path>` no longer names anything. */
function editorID(path: string): string {
  return tabIdFor("editor", path);
}

/** Open a file and let the round trip land. `openFile` is fire-and-forget by
 *  design (the mode, the repo and the pending line are written before the tab
 *  exists), so the wait is the test's. */
async function open(path: string): Promise<void> {
  openFile(path);
  await settleTabs();
}

beforeEach(() => {
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  // The registration the composition root performs. This is the ONLY teardown
  // channel: a spec's onClose is the factory's, so the editor's half arrives here
  // rather than as an argument at the door.
  registerTabOpeners({
    chat: { show: vi.fn(), close: vi.fn(), dot: () => "" },
    editor: { show: activateFile, close: closeEditorFile },
    run: { show: vi.fn(), cancel: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
  fileStates.clear();
  setActiveFilePath("");
  document.body.innerHTML = '<div id="tab-list"></div>';
});

describe("closing an editor tab", () => {
  it("removes the tab on the first close", async () => {
    expect.assertions(2);
    await open(PATH);
    expect(hasTab("editor", PATH)).toBe(true);
    await closeTab(editorID(PATH));
    expect(hasTab("editor", PATH)).toBe(false);
  });

  it("releases the file's client state exactly once", async () => {
    // The recursion never reached this: the stack died before the store could
    // finish, so the buffer, the dirty-indicator effect and the pending-line
    // entry all leaked with the tab.
    expect.assertions(2);
    await open(PATH);
    expect(fileStates.has(PATH)).toBe(true);
    await closeTab(editorID(PATH));
    expect(fileStates.has(PATH)).toBe(false);
  });

  it("runs the teardown once, not once per re-entry", async () => {
    // clearAgentLineCache is called from closeEditorFile and nowhere else on this
    // path, so its call count IS the teardown count. Under the loop it climbed
    // with the stack until the stack ran out.
    expect.assertions(1);
    await open(PATH);
    vi.mocked(clearAgentLineCache).mockClear();
    await closeTab(editorID(PATH));
    expect(vi.mocked(clearAgentLineCache)).toHaveBeenCalledTimes(1);
  });

  it("leaves no active tab behind when it was the only one", async () => {
    expect.assertions(1);
    await open(PATH);
    await closeTab(editorID(PATH));
    expect(getActiveTabId()).toBe("");
  });

  it("closes only the named file when several are open", async () => {
    expect.assertions(4);
    const other = "/workspace/other.ts";
    await open(PATH);
    await open(other);
    await closeTab(editorID(PATH));
    expect(hasTab("editor", PATH)).toBe(false);
    expect(fileStates.has(PATH)).toBe(false);
    expect(hasTab("editor", other)).toBe(true);
    expect(fileStates.has(other)).toBe(true);
  });

  it("survives closeEditorFile being called directly, which closes no tab", async () => {
    // The other direction of the same ownership rule: closeEditorFile drops the
    // file state and nothing else. A caller that wants the tab gone closes the
    // tab. Pinned so a future edit cannot quietly put closeTab back and reopen
    // the loop from this side.
    expect.assertions(2);
    await open(PATH);
    closeEditorFile(PATH);
    expect(fileStates.has(PATH)).toBe(false);
    expect(hasTab("editor", PATH)).toBe(true);
  });

  // The refused close is the other half of "nothing renders optimistically": the
  // strip changes when the frame arrives, so a close the server refuses leaves the
  // tab exactly where it was AND leaves the file's client state alone. Under an
  // optimistic close the file would be released for a tab still on screen.
  it("keeps the tab and its file state when the close fails", async () => {
    expect.assertions(3);
    await open(PATH);
    tabServer.failNext("close_tab");
    await closeTab(editorID(PATH));
    expect(hasTab("editor", PATH)).toBe(true);
    expect(fileStates.has(PATH)).toBe(true);
    expect(vi.mocked(clearAgentLineCache)).not.toHaveBeenCalled();
  });
});
