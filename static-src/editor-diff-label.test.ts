// ---------------------------------------------------------------------------
// The diff panes' captions.
//
// A diff's pane makes a CLAIM about what it holds. When git owns no revision of
// the file — a workspace file outside every repo, or a workspace root that is not
// itself a repo — the left pane is empty, and captioning it "HEAD" asserts that
// HEAD has the file and that the file is empty there. The RIGHT pane carries the
// same problem in the other direction: a deleted file has no working copy, and an
// empty pane captioned "working tree" says the file is there and empty. The load
// reports what it found on each side (`internal/git.KindNotInRepo` vs a real
// failure; a 404 from /api/file vs a real read failure), and both captions are
// taken from the load rather than from what was asked for.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";

interface DiffResult {
  oldContent: string;
  newContent: string;
  error: string;
  baseLabel: string;
  workingLabel: string;
}

let diffResult: DiffResult = {
  oldContent: "",
  newContent: "",
  error: "",
  baseLabel: "HEAD",
  workingLabel: "working tree",
};

vi.mock("./tabs.js", () => ({
  openEditorView: vi.fn(() => Promise.resolve()),
  // `editorTabID` is gone with the `editor:<path>` id convention: ids are opaque
  // and server-minted, and this is the one lookup from a path to one.
  tabIdFor: () => "",
  getActiveTabId: () => "",
  setTabDirty: vi.fn(),
}));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(() => Promise.resolve(null)) }));
vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
vi.mock("./editor-conflict.js", () => ({
  abortSuggestion: vi.fn(),
  clearSuggestionState: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGetTyped: vi.fn(),
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
  loadDiff: {
    dispatch: () => ({
      outcome: Promise.resolve({ status: "success", value: diffResult }),
    }),
  },
}));
vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));
vi.mock("./dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: () => document.createElement("div"),
    },
  ),
}));

const { fetchGitDiffSources } = await import("./editor-openers.js");
const { fileStates, freshState } = await import("./editor-types.js");

const PATH = "/workspace/hello.sh";

/** A file parked in diff mode against a ref, the state `open` leaves behind when
 *  it routes a `fromGit` source into the fetch. */
function stageDiffState(ref: string): ReturnType<typeof freshState> {
  const state = freshState(PATH);
  state.mode.value = {
    kind: "diff",
    diffSource: {
      oldContent: "",
      newContent: "",
      oldLabel: ref,
      newLabel: "working tree",
      fromGit: true,
    },
  };
  fileStates.set(PATH, state);
  return state;
}

function labelOf(state: ReturnType<typeof freshState>): string {
  const m = state.mode.value;
  return m.kind === "diff" ? m.diffSource.oldLabel : "";
}

function workingLabelOf(state: ReturnType<typeof freshState>): string {
  const m = state.mode.value;
  return m.kind === "diff" ? m.diffSource.newLabel : "";
}

beforeEach(() => {
  fileStates.clear();
});

describe("the base pane's caption", () => {
  it("says the ref when git holds a revision", async () => {
    expect.assertions(1);
    diffResult = {
      oldContent: "old",
      newContent: "new",
      error: "",
      baseLabel: "HEAD",
      workingLabel: "working tree",
    };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(labelOf(state)).toBe("HEAD");
  });

  it("says 'not in git' when no repo owns the file", async () => {
    expect.assertions(2);
    diffResult = {
      oldContent: "",
      newContent: "new",
      error: "",
      baseLabel: "not in git",
      workingLabel: "working tree",
    };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(labelOf(state)).toBe("not in git");
    // Still a correct all-add diff: the file exists, it just has no "before".
    expect(state.error).toBe("");
  });

  it("carries a non-HEAD ref through unchanged", async () => {
    expect.assertions(1);
    diffResult = {
      oldContent: "old",
      newContent: "new",
      error: "",
      baseLabel: "origin/main",
      workingLabel: "working tree",
    };
    const state = stageDiffState("origin/main");
    await fetchGitDiffSources(state, "", "origin/main");
    expect(labelOf(state)).toBe("origin/main");
  });
});

describe("the working pane's caption", () => {
  it("says 'working tree' for an ordinary change", async () => {
    expect.assertions(1);
    diffResult = {
      oldContent: "old",
      newContent: "new",
      error: "",
      baseLabel: "HEAD",
      workingLabel: "working tree",
    };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(workingLabelOf(state)).toBe("working tree");
  });

  it("says 'deleted' when the working copy is gone", async () => {
    // The placeholder the opener staged says "working tree", so the load has to
    // overwrite it: an empty right pane under that caption claims the file is
    // still there and empty.
    expect.assertions(3);
    diffResult = {
      oldContent: "gone\n",
      newContent: "",
      error: "",
      baseLabel: "HEAD",
      workingLabel: "deleted",
    };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(workingLabelOf(state)).toBe("deleted");
    expect(labelOf(state)).toBe("HEAD");
    // An all-deletions diff is a correct rendering, not an error state.
    expect(state.error).toBe("");
  });
});
