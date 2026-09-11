// ---------------------------------------------------------------------------
// Re-reading a buffer that already holds bytes.
//
// The invariant every case here circles: a DIRTY buffer is the only copy of the
// reader's text, so a refresh may move `original` and never `current`. The rest is
// the branch table — clean or dirty, hash moved or not, and the three buffer-derived
// modes. Plus the other half of that split: the OPEN path owns the never-loaded read,
// so it is the one that paints a placeholder.
// ---------------------------------------------------------------------------
import { describe, it, expect, beforeEach, vi } from "vitest";

interface FileRead {
  content?: string;
  content_hash?: string;
  error?: string;
}
interface ReadResult {
  ok: boolean;
  status: number;
  data: FileRead | null;
  error: string;
}

/** The OPEN path's read, so a case can count it against the refresh's. */
const apiGet = vi.fn((_url: string, _signal?: AbortSignal): Promise<FileRead | null> =>
  Promise.resolve({ content: "on disk", content_hash: "h1" }),
);

/** The narrow half of the library's union: the one arm in this file hands the door's
 *  teardown back, so a case can take the placeholder down again. */
type SkeletonShow = () => () => void;

/** The placeholder paints on a 150ms delay the library owns, so the arm is what a
 *  case here can see. A case wanting the MOUNT runs the recorded closure itself. */
const armedShows: SkeletonShow[] = [];
const cancelArm = vi.fn();
const armSkeleton = vi.fn((show: SkeletonShow) => {
  armedShows.push(show);
  return {
    commit: (render: () => void) => {
      render();
    },
    cancel: cancelArm,
  };
});

/** The refresh's read. Queued rather than returned, so a case can land two
 *  responses in the order it chooses. */
let queued: ReadResult[] = [];
const apiGetOrError = vi.fn((_url: string, _signal?: AbortSignal): Promise<ReadResult> => {
  const next = queued.shift();
  return Promise.resolve(next ?? { ok: false, status: 0, data: null, error: "" });
});

function ok(content: string, hash: string): ReadResult {
  return { ok: true, status: 200, data: { content, content_hash: hash }, error: "" };
}

let activeTab = "";
const minted = new Map<string, string>();

vi.mock("./tabs.js", () => ({
  openEditorView: vi.fn(() => Promise.resolve()),
  getActiveTabId: () => activeTab,
  tabIdFor: (_kind: string, ref = "") => minted.get(ref) ?? "",
  setTabDirty: vi.fn(),
}));
vi.mock("./api-client.js", () => ({
  apiGet: (url: string, signal?: AbortSignal) => apiGet(url, signal),
  apiGetOrError: (url: string, signal?: AbortSignal) => apiGetOrError(url, signal),
  apiGetTyped: vi.fn(),
}));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  skeletonTiming: (show: SkeletonShow) => armSkeleton(show),
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

const { activateFile, refreshFile } = await import("./editor-openers.js");
const { fileStates, freshState, setActiveFilePath } = await import("./editor-types.js");
const { renderEditModeUI } = await import("./editor-ui.js");
const { restoreUI } = await import("./editor-modes.js");
const { parseConflicts } = await import("./conflict.js");
const { $ } = await import("./dom.js");

const PATH = "/workspace/hello.go";

const MARKED_OLD = "a\n<<<<<<< HEAD\nours-old\n=======\ntheirs-old\n>>>>>>> other\n";
const MARKED_NEW = "a\n<<<<<<< HEAD\nours-new\n=======\ntheirs-new\n>>>>>>> other\n";

/** A buffer that has LOADED, which is the only state a refresh acts on. */
function loadedState(opts: {
  readonly content: string;
  readonly hash: string;
  readonly dirtyText?: string;
}): ReturnType<typeof freshState> {
  const state = freshState(PATH);
  state.original.value = opts.content;
  state.current.value = opts.dirtyText ?? opts.content;
  state.loaded = true;
  state.loadedHash = opts.hash;
  fileStates.set(PATH, state);
  return state;
}

beforeEach(() => {
  vi.clearAllMocks();
  fileStates.clear();
  queued = [];
  armedShows.length = 0;
  activeTab = "";
  minted.clear();
  setActiveFilePath("");
  apiGet.mockResolvedValue({ content: "on disk", content_hash: "h1" });
});

describe("refreshFile refuses a buffer the OPEN path still owns", () => {
  it("a first activation issues exactly ONE /api/file read and ends loaded", async () => {
    // `activateFile`'s `!state.loaded` branch has a read in flight through the
    // controller a refresh would otherwise reuse, so the refresh the dispatcher fires
    // immediately after must add nothing.
    setActiveFilePath("");
    activateFile(PATH);
    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(fileStates.get(PATH)?.loaded).toBe(true);
    });

    expect(apiGet).toHaveBeenCalledTimes(1);
    expect(apiGetOrError).not.toHaveBeenCalled();
  });

  it("reads nothing for a path with no state at all", () => {
    refreshFile("/workspace/never-opened.go");
    expect(apiGetOrError).not.toHaveBeenCalled();
  });
});

describe("the branch table", () => {
  it("clean and unmoved: writes nothing and repaints nothing", async () => {
    const state = loadedState({ content: "same", hash: "h1" });
    queued.push(ok("same", "h1"));

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(apiGetOrError).toHaveBeenCalledTimes(1);
    });

    expect(state.original.value).toBe("same");
    expect(restoreUI).not.toHaveBeenCalled();
  });

  it("clean and moved: adopts the disk bytes into both halves", async () => {
    const state = loadedState({ content: "old", hash: "h1" });
    queued.push(ok("new", "h2"));

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(state.loadedHash).toBe("h2");
    });

    expect(state.original.value).toBe("new");
    expect(state.current.value).toBe("new");
    expect(restoreUI).toHaveBeenCalled();
  });

  it("an absent hash on BOTH sides reads as unmoved", async () => {
    // `loadedHash` initialises to "" and the response's is `?? ""`, so this is the
    // clean-and-unchanged row's own arithmetic: a refresh never replaces a buffer it
    // cannot prove moved.
    const state = loadedState({ content: "old", hash: "" });
    queued.push({ ok: true, status: 200, data: { content: "new" }, error: "" });

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(apiGetOrError).toHaveBeenCalledTimes(1);
    });

    expect(state.original.value).toBe("old");
  });

  it("dirty and moved: moves original, keeps current, shows the unsaved diff", async () => {
    const state = loadedState({ content: "old", hash: "h1", dirtyText: "my edits" });
    queued.push(ok("new", "h2"));

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(state.loadedHash).toBe("h2");
    });

    expect(state.original.value).toBe("new");
    expect(state.current.value).toBe("my edits");
    expect(state.mode.value).toEqual({
      kind: "diff",
      diffSource: {
        oldContent: "new",
        newContent: "my edits",
        oldLabel: "saved",
        newLabel: "unsaved",
        fromGit: false,
      },
    });
  });

  it("dirty and moved: repaints explicitly and leaves the pane's error channel empty", async () => {
    // `state.mode` has no painting subscriber, and `restoreUI` reads a non-empty
    // `state.error.value` as a FAILED pane — it would blank the diff this arm exists
    // to show — so the sentence goes to the other channel.
    const state = loadedState({ content: "old", hash: "h1", dirtyText: "my edits" });
    queued.push(ok("new", "h2"));

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(renderEditModeUI).toHaveBeenCalledTimes(1);
    });

    expect(state.error.value).toBe("");
    expect($.editorError.textContent).toContain("changed on disk");
    expect($.editorError.classList.contains("hidden")).toBe(false);
  });

  it("dirty and unmoved: writes nothing", async () => {
    const state = loadedState({ content: "old", hash: "h1", dirtyText: "my edits" });
    queued.push(ok("old", "h1"));

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(apiGetOrError).toHaveBeenCalledTimes(1);
    });

    expect(state.original.value).toBe("old");
    expect(state.current.value).toBe("my edits");
    expect(renderEditModeUI).not.toHaveBeenCalled();
  });
});

describe("a read that answers ABOUT the file rather than failing", () => {
  it("a 404 says the file is gone and KEEPS the buffer", async () => {
    const state = loadedState({ content: "old", hash: "h1", dirtyText: "my edits" });
    queued.push({ ok: false, status: 404, data: null, error: "not found" });

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(state.error.value).toContain("no longer on disk");
    });

    expect(state.current.value).toBe("my edits");
    expect(restoreUI).toHaveBeenCalled();
  });

  it("a 415 says the file is no longer text", async () => {
    const state = loadedState({ content: "old", hash: "h1" });
    queued.push({ ok: false, status: 415, data: null, error: "binary file" });

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(state.error.value).toContain("no longer text");
    });
  });

  it("a network failure says nothing and leaves the content standing", async () => {
    const state = loadedState({ content: "old", hash: "h1" });
    queued.push({ ok: false, status: 0, data: null, error: "network" });

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(apiGetOrError).toHaveBeenCalledTimes(1);
    });

    expect(state.error.value).toBe("");
    expect(state.original.value).toBe("old");
    expect(restoreUI).not.toHaveBeenCalled();
  });
});

describe("conflict mode, the third buffer-derived mode", () => {
  function conflictState(dirtyText?: string): ReturnType<typeof freshState> {
    const opts =
      dirtyText === undefined
        ? { content: MARKED_OLD, hash: "h1" }
        : { content: MARKED_OLD, hash: "h1", dirtyText };
    const state = loadedState(opts);
    state.mode.value = {
      kind: "conflict",
      conflict: parseConflicts(MARKED_OLD),
      editing: true,
    };
    return state;
  }

  it("a CLEAN conflict buffer whose disk bytes still hold markers re-parses to the NEW hunks", async () => {
    const state = conflictState();
    queued.push(ok(MARKED_NEW, "h2"));

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(state.loadedHash).toBe("h2");
    });

    const m = state.mode.value;
    expect(m.kind).toBe("conflict");
    if (m.kind !== "conflict") {
      return;
    }
    expect(m.conflict.hunks[0]?.oursLines).toEqual(["ours-new"]);
    expect(m.conflict.hunks[0]?.theirsLines).toEqual(["theirs-new"]);
  });

  it("a CLEAN conflict buffer resolved ON DISK lands in edit mode", async () => {
    const state = conflictState();
    queued.push(ok("a\nresolved\n", "h2"));

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(state.loadedHash).toBe("h2");
    });

    expect(state.mode.value).toEqual({ kind: "edit", editing: false });
    expect(state.current.value).toBe("a\nresolved\n");
  });

  it("a DIRTY conflict buffer keeps its mode and its text, with original moved", async () => {
    // The reader's markers are their own text: they are resolving the conflict in the
    // buffer, and the unsaved diff against the new disk bytes is the honest surface.
    const state = conflictState("a\nmy resolution\n");
    queued.push(ok(MARKED_NEW, "h2"));

    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(state.loadedHash).toBe("h2");
    });

    expect(state.mode.value.kind).toBe("conflict");
    expect(state.current.value).toBe("a\nmy resolution\n");
    expect(state.original.value).toBe(MARKED_NEW);
  });
});

describe("two refreshes for one path", () => {
  it("the older answer is discarded even when it lands last", async () => {
    // Nothing else in the module can say which is newer: `activeLoadController` is
    // aborted only by `activateFile`, and a refresh deliberately must not abort it.
    const state = loadedState({ content: "old", hash: "h1" });
    let releaseFirst = (): void => {
      /* replaced below */
    };
    const first = new Promise<ReadResult>((resolve) => {
      releaseFirst = () => {
        resolve(ok("FIRST", "hA"));
      };
    });
    apiGetOrError.mockReturnValueOnce(first);
    apiGetOrError.mockReturnValueOnce(Promise.resolve(ok("SECOND", "hB")));

    refreshFile(PATH);
    refreshFile(PATH);
    await vi.waitFor(() => {
      expect(state.original.value).toBe("SECOND");
    });

    releaseFirst();
    await first;
    // A macrotask, so the superseded continuation has provably run before the
    // assertion rather than merely not having been scheduled yet.
    await new Promise((r) => setTimeout(r, 0));

    expect(state.original.value).toBe("SECOND");
    expect(state.loadedHash).toBe("hB");
  });
});

describe("the document placeholder on the OPEN path", () => {
  it("arms one for a buffer that has never loaded", async () => {
    setActiveFilePath("");
    activateFile(PATH);
    await vi.waitFor(() => {
      expect(fileStates.get(PATH)?.loaded).toBe(true);
    });

    expect(armSkeleton).toHaveBeenCalledTimes(1);
    expect(cancelArm).toHaveBeenCalledTimes(1);
  });

  it("arms none for a buffer that already holds bytes", () => {
    loadedState({ content: "on disk", hash: "h1" });
    activateFile(PATH);

    expect(armSkeleton).not.toHaveBeenCalled();
    expect(apiGet).not.toHaveBeenCalled();
  });

  it("CLEARS the pane rather than writing a line into it", () => {
    // The pane still holds the outgoing file's highlight spans, and no child selector
    // can tell one file's content from another's, so the clear is what makes the pane
    // empty for the door.
    $.editorCode.appendChild(document.createElement("span"));
    setActiveFilePath("");
    activateFile(PATH);

    expect($.editorCode.textContent).toBe("");
    expect($.editorCode.children).toHaveLength(0);
  });

  it("mounts the document skeleton into the code pane and takes it back down", () => {
    setActiveFilePath("");
    activateFile(PATH);
    const teardown = armedShows.at(-1)?.();
    expect($.editorCode.querySelector(".editor-skeleton")).not.toBeNull();

    expect(typeof teardown).toBe("function");
    teardown?.();
    expect($.editorCode.querySelector(".editor-skeleton")).toBeNull();
  });

  it("arms nothing over a diff that paints from the state it already holds", () => {
    const state = freshState(PATH);
    state.mode.value = {
      kind: "diff",
      diffSource: {
        oldContent: "before",
        newContent: "after",
        oldLabel: "saved",
        newLabel: "unsaved",
        fromGit: false,
      },
    };
    fileStates.set(PATH, state);
    setActiveFilePath("");
    activateFile(PATH);

    expect(armSkeleton).not.toHaveBeenCalled();
  });
});
