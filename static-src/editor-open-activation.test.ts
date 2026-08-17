// @vitest-environment happy-dom
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
 *  activateTab enforces: onShow fires only when the ACTIVE tab changes. */
let activeTab = "";
// The parameter lists mirror the real signatures (tabs.ts openEditorView takes
// onClose third; api-client.ts apiGet takes the AbortSignal second) because the
// assertions below READ the recorded arguments — a mock declaring fewer than it
// is handed types its own call log as having no such argument.
const openEditorView = vi.fn((path: string, onShow: () => void, _onClose?: () => void) => {
  const id = `editor:${path}`;
  if (activeTab === id) {
    return;
  }
  activeTab = id;
  onShow();
});

const apiGet = vi.fn((_url: string, _signal?: AbortSignal) =>
  Promise.resolve({ content: "hello", content_hash: "h" }),
);

vi.mock("./tabs.js", () => ({
  openEditorView: (path: string, onShow: () => void, onClose?: () => void) =>
    openEditorView(path, onShow, onClose),
  getActiveTabId: () => activeTab,
  editorTabID: (path: string) => `editor:${path}`,
  setTabDirty: vi.fn(),
}));
vi.mock("./api-client.js", () => ({
  apiGet: (url: string, signal?: AbortSignal) => apiGet(url, signal),
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
vi.mock("./ui-state.js", () => ({ save: vi.fn(), load: () => ({ editor_files: [] }) }));
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

const { openFile } = await import("./editor-openers.js");
const { fileStates, setActiveFilePath } = await import("./editor-types.js");

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
  });

  it("issues ONE file read on a first open", () => {
    openFile("/workspace/a.go");
    expect(fileReads()).toBe(1);
  });

  it("still activates when the tab was already active and onShow is skipped", () => {
    // Re-opening the file whose tab is already active: activateTab returns early,
    // so without the fallback call nothing would load the file at all.
    activeTab = "editor:/workspace/a.go";
    openFile("/workspace/a.go");
    expect(fileReads()).toBe(1);
  });

  it("does not abort the read it just issued", () => {
    openFile("/workspace/a.go");
    const signal = apiGet.mock.calls.at(-1)?.[1];
    // The second activation's controller swap aborted the first one's request,
    // which is why the wasted round trip was invisible.
    expect(signal?.aborted).toBe(false);
  });

  it("reads once per open across two different files", () => {
    openFile("/workspace/a.go");
    openFile("/workspace/b.go");
    expect(fileReads()).toBe(2);
  });
});
