// ---------------------------------------------------------------------------
// The base pane's caption.
//
// A diff's left pane makes a CLAIM about what it holds. When git owns no
// revision of the file — a workspace file outside every repo, or a workspace root
// that is not itself a repo — the pane is empty, and captioning it "HEAD" asserts
// that HEAD has the file and that the file is empty there. The load reports which
// it found (`internal/git.KindNotInRepo` vs a real failure), and the caption is
// taken from the load rather than from the ref that was asked for.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";

interface DiffResult {
  oldContent: string;
  newContent: string;
  error: string;
  baseLabel: string;
}

let diffResult: DiffResult = {
  oldContent: "",
  newContent: "",
  error: "",
  baseLabel: "HEAD",
};

vi.mock("./tabs.js", () => ({
  openEditorView: vi.fn(),
  editorTabID: (path: string) => `editor:${path}`,
  getActiveTabId: () => "",
  setTabDirty: vi.fn(),
}));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(() => Promise.resolve(null)) }));
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
}));
vi.mock("./ui-state.js", () => ({ save: vi.fn(), load: () => ({ editor_files: [] }) }));
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

beforeEach(() => {
  fileStates.clear();
});

describe("the base pane's caption", () => {
  it("says the ref when git holds a revision", async () => {
    expect.assertions(1);
    diffResult = { oldContent: "old", newContent: "new", error: "", baseLabel: "HEAD" };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(labelOf(state)).toBe("HEAD");
  });

  it("says 'not in git' when no repo owns the file", async () => {
    expect.assertions(2);
    diffResult = { oldContent: "", newContent: "new", error: "", baseLabel: "not in git" };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(labelOf(state)).toBe("not in git");
    // Still a correct all-add diff: the file exists, it just has no "before".
    expect(state.error).toBe("");
  });

  it("carries a non-HEAD ref through unchanged", async () => {
    expect.assertions(1);
    diffResult = { oldContent: "old", newContent: "new", error: "", baseLabel: "origin/main" };
    const state = stageDiffState("origin/main");
    await fetchGitDiffSources(state, "", "origin/main");
    expect(labelOf(state)).toBe("origin/main");
  });
});
