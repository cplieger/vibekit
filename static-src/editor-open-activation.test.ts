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
import { effect } from "@cplieger/reactive";

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

// The return type is the route's real one, `| null` included, so a case can hand
// back the refusal `apiGet` collapses a non-2xx to.
type FileRead = { content?: string; content_hash?: string; error?: string } | null;
const apiGet = vi.fn((_url: string, _signal?: AbortSignal): Promise<FileRead> =>
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
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
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
  renderEditModeUI: vi.fn(),
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

const { openFile, openFileDiff, openFileGitDiff, activateFile } =
  await import("./editor-openers.js");
const { fileStates, setActiveFilePath, getActiveFilePath } = await import("./editor-types.js");
const { restoreUI } = await import("./editor-modes.js");
const { showReadMode } = await import("./editor-ui.js");

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

// A card's `+N -M` opens a diff whose two sides are already in hand, so the pane
// owes the file read nothing. It was nonetheless routed through the read like any
// text buffer, and the read's failure branch writes `state.error`, which restoreUI
// renders by blanking the pane — so a diff of a file the agent later deleted was
// replaced by "Failed to load file" with both sides sitting in memory.
describe("a diff that carries its own pair", () => {
  const OLD = "one\ntwo";
  const NEW = "one\nTWO";

  beforeEach(() => {
    vi.clearAllMocks();
    fileStates.clear();
    setActiveFilePath("");
    activeTab = "";
    minted.clear();
  });

  it("paints before the read instead of behind it", () => {
    // Asserted synchronously, which is the point: the read has not resolved yet.
    // Without this the pane holds the file the reader came from for a round trip.
    expect.assertions(2);
    openFileDiff("/workspace/a.go", OLD, NEW);
    expect(vi.mocked(restoreUI)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(showReadMode)).not.toHaveBeenCalled();
  });

  it("keeps the diff when the read fails, and withholds the buffer", async () => {
    expect.assertions(2);
    apiGet.mockResolvedValueOnce(null);
    openFileDiff("/workspace/gone.go", OLD, NEW);
    await Promise.resolve();
    await Promise.resolve();
    const state = fileStates.get("/workspace/gone.go");
    // An empty `error` is what keeps restoreUI off its blanking branch, and an
    // unloaded buffer is what keeps Edit away from a file that is not there.
    expect(state?.error.value).toBe("");
    expect(state?.loaded).toBe(false);
  });

  it("reports a read error the same way for a plain file", async () => {
    // The blanking branch is correct where the pane had nothing else to show.
    expect.assertions(2);
    apiGet.mockResolvedValueOnce(null);
    openFile("/workspace/gone.go");
    await Promise.resolve();
    await Promise.resolve();
    const state = fileStates.get("/workspace/gone.go");
    expect(state?.error.value).toBe("Failed to load file");
    expect(state?.loaded).toBe(true);
  });

  it("fills the buffer from the FILE, never from the diff's own after-side", async () => {
    // A ToolDiff's newText can be a hunk rather than whole-file contents, so
    // seeding the editable buffer from it would put a fragment behind Edit and a
    // save would write the fragment over the file.
    expect.assertions(2);
    openFileDiff("/workspace/a.go", OLD, NEW);
    await Promise.resolve();
    await Promise.resolve();
    const state = fileStates.get("/workspace/a.go");
    expect(state?.loaded).toBe(true);
    expect(state?.current.value).toBe("hello");
  });

  it("leaves a git diff on its own fetch, with the placeholder it does need", () => {
    // The other diff kind has NOTHING until loadDiff answers, so its branch keeps
    // both the placeholder and its exemption from the file read.
    expect.assertions(2);
    openFileGitDiff("/workspace/a.go", "HEAD");
    expect(vi.mocked(showReadMode)).toHaveBeenCalled();
    expect(fileReads()).toBe(0);
  });
});

describe("what an activation has in place before it moves the active path", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fileStates.clear();
    setActiveFilePath("");
    activeTab = "";
    minted.clear();
  });

  it("has the file's own state in the map on the run the path change triggers", () => {
    // The contract editor-core's single-writer effect depends on: it tracks the
    // active path and then that file's `error` and `mode` signals, so it can only
    // subscribe to the second pair if the state exists on the run the path write
    // triggers. `activateFile` is the editor tab's `onShow`, so it runs for a tab
    // RESTORED from the server's set with nothing in the map yet — and an effect
    // that found no state there never re-runs for the load that follows, which
    // froze the control's answer until an unrelated git-status scan moved the
    // store. `open()` has always created the state first, which is why only the
    // restored-tab route was affected.
    expect.assertions(1);
    const seen: boolean[] = [];
    const dispose = effect(() => {
      const path = getActiveFilePath();
      if (path !== "") {
        seen.push(fileStates.has(path));
      }
    });
    activateFile("/workspace/a.go");
    dispose();
    expect(seen).toEqual([true]);
  });
});
