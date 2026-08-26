// ---------------------------------------------------------------------------
// How many times opening a file activates it.
//
// `open` hands activateFile to openEditorView as the tab's onShow AND called it
// again unconditionally afterwards, on the reasoning that openEditorView may skip
// the callback for a tab that was already active. It does skip it — but on a
// FIRST open the tab becomes active, so the callback fires and the unconditional
// call made it twice. Each activation aborts the previous one's AbortController
// and issues a fresh GET /api/file, so every editor open cost a request that was
// cancelled the moment it was made.
//
// The observable is the request count, which is the cost the caller pays; the
// call count of a module-private function is not reachable and would be the wrong
// thing to pin anyway.
// ---------------------------------------------------------------------------
import { describe, it, expect, beforeEach, vi } from "vitest";

/** The tab store reduced to the one fact `open` reads and the one rule
 *  activateTab enforces: the tab's activation hook fires only when the ACTIVE tab
 *  changes.
 *
 *  Ids are opaque and server-minted, so this mock mints one per path and hands it
 *  back through `tabIdFor` — the only lookup production has. A test that composed
 *  `editor:<path>` would be reaching a row by a route the app cannot.
 *
 *  `openEditorView` RESOLVES rather than returning void, because every open is a
 *  round trip through `open_tab` now, and the tab's `onShow` is the factory's:
 *  `activateFile` is registered by the composition root, so this mock calls it
 *  through the same registration rather than through a callback argument that no
 *  longer exists. */
let activeTab = "";
const minted = new Map<string, string>();

function idFor(path: string): string {
  return minted.get(path) ?? "";
}

const openEditorView = vi.fn((path: string) => {
  let id = minted.get(path);
  if (id === undefined) {
    id = `tb_${String(minted.size + 1).padStart(3, "0")}`;
    minted.set(path, id);
  }
  if (activeTab === id) {
    return Promise.resolve();
  }
  activeTab = id;
  editorShow(path);
  return Promise.resolve();
});

/** The editor half of the registered openers, stubbed here and pointed at the
 *  real `activateFile` once it is imported. */
let editorShow: (path: string) => void = () => {
  /* replaced below */
};

const apiGet = vi.fn((_url: string, _signal?: AbortSignal) =>
  Promise.resolve({ content: "hello", content_hash: "h" }),
);

vi.mock("./tabs.js", () => ({
  openEditorView: (path: string) => openEditorView(path),
  getActiveTabId: () => activeTab,
  // Replaced `editorTabID`: an id is not composed from a path any more, so the
  // lookup runs the other way round.
  tabIdFor: (_kind: string, ref = "") => idFor(ref),
  setTabDirty: vi.fn(),
}));
vi.mock("./api-client.js", () => ({
  apiGet: (url: string, signal?: AbortSignal) => apiGet(url, signal),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGetTyped: vi.fn(),
}));
vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
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
  updateGutter: vi.fn(),
}));
vi.mock("./actions/editor.js", () => ({
  loadDiff: { dispatch: () => ({ outcome: Promise.resolve({ status: "cancelled" }) }) },
}));
vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));

// Every element activateFile touches, none of them shared with another module.
vi.mock("./dom.js", () => {
  const make = (tag: string): HTMLElement => document.createElement(tag);
  const highlight = make("pre");
  make("div").appendChild(highlight);
  return {
    $: {
      editorFilename: make("span"),
      editorError: make("div"),
      editorHighlight: highlight,
      editorCode: make("code"),
      editorContent: make("textarea"),
      editorEditBtn: make("button"),
      editorGutter: make("pre"),
      editorDiffPane: make("div"),
      editorMarkdown: make("div"),
      editorImage: make("img"),
    },
  };
});

const { openFile, activateFile } = await import("./editor-openers.js");
const { fileStates, setActiveFilePath } = await import("./editor-types.js");

// The registration the composition root performs: the editor tab's activation
// hook IS activateFile, so the mock above drives it through the same seam
// production does.
editorShow = activateFile;

/** GETs against the file read route, which is what an activation costs. */
function fileReads(): number {
  return apiGet.mock.calls.filter((c) => c[0].startsWith("/api/file")).length;
}

describe("opening a file activates it once", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fileStates.clear();
    setActiveFilePath("");
    activeTab = "";
    minted.clear();
  });

  it("issues ONE file read on a first open", async () => {
    expect.assertions(1);
    openFile("/workspace/a.go");
    await Promise.resolve();
    expect(fileReads()).toBe(1);
  });

  it("still activates when the tab was already active and the hook is skipped", async () => {
    // Re-opening the file whose tab is already active: activateTab returns early,
    // so without the fallback call nothing would load the file at all. The
    // fallback now runs in the OPEN's continuation, because the open is a round
    // trip.
    expect.assertions(1);
    minted.set("/workspace/a.go", "tb_001");
    activeTab = "tb_001";
    openFile("/workspace/a.go");
    await Promise.resolve();
    expect(fileReads()).toBe(1);
  });

  it("does not abort the read it just issued", async () => {
    expect.assertions(1);
    openFile("/workspace/a.go");
    await Promise.resolve();
    const signal = apiGet.mock.calls.at(-1)?.[1];
    // The second activation's controller swap aborted the first one's request,
    // which is why the wasted round trip was invisible.
    expect(signal?.aborted).toBe(false);
  });

  it("reads once per open across two different files", async () => {
    expect.assertions(1);
    openFile("/workspace/a.go");
    await Promise.resolve();
    openFile("/workspace/b.go");
    await Promise.resolve();
    expect(fileReads()).toBe(2);
  });
});
