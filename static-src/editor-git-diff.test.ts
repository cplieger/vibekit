// ---------------------------------------------------------------------------
// #editor-git-diff-btn: the editor toolbar's diff-vs-HEAD control.
//
// A different QUESTION from #editor-diff-btn beside it, which means buffer vs
// saved — so it is a different button, and each owns its own diff kind. What is
// pinned here is the predicate (when the control is offered) and the fact that
// it has TWO triggers: the reactive one for the active path and that file's
// mode, and the imperative git-status subscription for a letter that lands after
// the file is already open. A predicate reading `statusForPath` cannot track the
// store, so a single reactive trigger would leave the button missing until the
// reader happened to switch tabs.
//
// The pane CAPTIONS the status letters imply are NOT re-asserted here: the base
// pane's three states and the working pane's two are the label contract, owned
// by `editor-diff-label.test.ts` (propagation) and `actions/editor.test.ts`
// (derivation). This file asserts which STATUSES offer the control.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";

// One element per registry key, memoized: the button has to be the SAME node
// across every `$.editorGitDiffBtn` read or a class written by the effect lands
// on a throwaway and every assertion passes vacuously.
const { els } = vi.hoisted(() => ({ els: new Map<string, HTMLElement>() }));

// Every name in dom.ts, because Browser Mode links ESM for real: a name any
// module in this graph imports has to exist here even when nothing below calls
// it. The four beside `$` are reached by siblings of the editor.
vi.mock("./dom.js", () => ({
  byId: (id: string): HTMLElement => {
    let el = els.get(`#${id}`);
    if (el === undefined) {
      el = document.createElement("div");
      els.set(`#${id}`, el);
    }
    return el;
  },
  maybeEl: () => null,
  forceReflow: () => 0,
  setBusy: vi.fn(),
  setControlBusy: vi.fn(),
  $: new Proxy(
    {},
    {
      get: (_t, key: string): HTMLElement => {
        let el = els.get(key);
        if (el === undefined) {
          el = document.createElement(key.endsWith("Btn") ? "button" : "div");
          els.set(key, el);
        }
        return el;
      },
    },
  ),
}));

// The two openers editor-core reaches. Replaced rather than spied through: the
// real `openFileGitDiff` opens a tab and dispatches a fetch, and what this file
// asserts about the click is which DIRECTION it took.
vi.mock("./editor-openers.js", () => ({
  fetchGitDiffSources: vi.fn(),
  openFileGitDiff: vi.fn(),
  // Present-but-inert for the same real-ESM-linking reason as dom.js above.
  openFile: vi.fn(),
  openFileDiff: vi.fn(),
  activateFile: vi.fn(),
  closeEditorFile: vi.fn(),
}));
vi.mock("./confirm.js", () => ({ confirm: vi.fn(() => Promise.resolve(true)) }));
vi.mock("./git.js", () => ({ markGitDirty: vi.fn() }));

const { initEditor } = await import("./editor-core.js");
const { renderDiffModeUI } = await import("./editor-diff.js");
const { openFileGitDiff } = await import("./editor-openers.js");
const { fileStates, freshState, setActiveFilePath, gitDiffSource, unsavedDiffSource } =
  await import("./editor-types.js");
const { _setReposForTest, refreshGitStatus } = await import("./git-status-store.js");
const { setWorkspaceRoot, _resetForTest: resetWorkspace } = await import("./workspace.js");
const { $ } = await import("./dom.js");
const { parseConflicts } = await import("./conflict.js");

import type { GitRepoStatus } from "./git-types.js";

const PATH = "/workspace/hello.ts";
const IMAGE = "/workspace/logo.png";

/** One repo at the workspace root carrying one status letter per path. */
function repos(entries: Record<string, string>): GitRepoStatus[] {
  return [
    {
      repo: ".",
      is_repo: true,
      branch: "main",
      remote: "origin",
      ahead: 0,
      behind: 0,
      has_dirty: Object.keys(entries).length > 0,
      stashes: 0,
      files: Object.entries(entries).map(([path, status]) => ({
        path,
        status,
        staged: false,
        display: path,
      })),
    },
  ];
}

/** Park a file in this module's state and make it the active one, which is what
 *  the effect tracks. */
function stage(
  path: string,
  opts: { error?: string; mode?: ReturnType<typeof freshState>["mode"]["value"] } = {},
): ReturnType<typeof freshState> {
  const state = freshState(path);
  state.loaded = true;
  state.error.value = opts.error ?? "";
  if (opts.mode !== undefined) {
    state.mode.value = opts.mode;
  }
  fileStates.set(path, state);
  setActiveFilePath(path);
  return state;
}

function shown(): boolean {
  return !$.editorGitDiffBtn.classList.contains("hidden");
}

let wired = false;

beforeEach(async () => {
  resetWorkspace();
  setWorkspaceRoot("/workspace");
  fileStates.clear();
  setActiveFilePath("");
  if (!wired) {
    wired = true;
    initEditor();
    // The first activation arms the store, whose one boot read would otherwise
    // resolve mid-test and overwrite whatever `_setReposForTest` injected.
    // Awaiting it here joins that in-flight read (the action dedupes per scope)
    // so every later injection is the last write.
    stage(PATH);
    await refreshGitStatus();
  }
  _setReposForTest([]);
  setActiveFilePath("");
});

describe("when the control is offered", () => {
  it("stays hidden for a clean file", () => {
    expect.assertions(1);
    _setReposForTest(repos({}));
    stage(PATH);
    expect(shown()).toBe(false);
  });

  it("appears for a modified file", () => {
    expect.assertions(2);
    _setReposForTest(repos({ "hello.ts": "M" }));
    stage(PATH);
    expect(shown()).toBe(true);
    expect($.editorGitDiffBtn.getAttribute("aria-pressed")).toBe("false");
  });

  it("appears for an untracked file, which has no revision at the ref at all", () => {
    // The case the `absent` marker exists for: the diff renders as all-add, so
    // the control is as useful here as for an 'M'. `handleShow` answering empty
    // content is what makes it a diff rather than an error.
    expect.assertions(1);
    _setReposForTest(repos({ "hello.ts": "?" }));
    stage(PATH);
    expect(shown()).toBe(true);
  });

  it("appears for a deleted file, whose diff is all removals", () => {
    expect.assertions(1);
    _setReposForTest(repos({ "hello.ts": "D" }));
    stage(PATH);
    expect(shown()).toBe(true);
  });

  it("stays hidden for an image, which has no text buffer to compare", () => {
    // Image mode hides every other text affordance for the same reason, and a
    // two-pane text diff over a PNG compares nothing. Guarded DOUBLY — by the
    // extension test and by the mode clause — so no single-clause mutant kills
    // this one; the two cases below are what pin each clause on its own. Kept
    // because this is the route an image actually opens by.
    expect.assertions(1);
    _setReposForTest(repos({ "logo.png": "M" }));
    stage(IMAGE, { mode: { kind: "image" } });
    expect(shown()).toBe(false);
  });

  it("stays hidden for an image opened straight into a git diff", () => {
    // The case the extension test is the ONLY guard for, and it is reachable:
    // the file browser's status letter calls openFileGitDiff on whatever it is
    // sitting on, a .png included, so between that click and the load resolving
    // the file is in a fromGit diff with no error yet. Image mode covers the
    // ordinary route and the error state covers a settled binary; neither
    // reaches this window.
    expect.assertions(1);
    _setReposForTest(repos({ "logo.png": "M" }));
    stage(IMAGE, { mode: { kind: "diff", diffSource: gitDiffSource("HEAD", "", "") } });
    expect(shown()).toBe(false);
  });

  it("stays hidden for a conflicted file, whose surface is the conflict overlay", () => {
    // A conflicted file is git-dirty by construction ('U'), so only the MODE
    // clause hides it — and it must, because conflict mode's affordance is the
    // per-hunk overlay and a diff-vs-HEAD pane is not the question being asked.
    expect.assertions(1);
    _setReposForTest(repos({ "hello.ts": "U" }));
    stage(PATH, {
      mode: {
        kind: "conflict",
        conflict: parseConflicts("<<<<<<< ours\na\n=======\nb\n>>>>>>> theirs\n"),
        editing: true,
      },
    });
    expect(shown()).toBe(false);
  });

  it("stays hidden while the buffer is being edited", () => {
    // `startEditing` withdraws every sideways exit so edit mode is left through
    // Cancel, which confirms a discard, or through Save. This control belongs in
    // that set: clicking it mid-edit swaps the textarea for a two-pane diff on
    // one click, which reads as losing the edit even though the buffer survives.
    // The single-writer rule is why the clause lives in the predicate rather
    // than in `startEditing`'s own hand-hiding.
    expect.assertions(2);
    _setReposForTest(repos({ "hello.ts": "M" }));
    const state = stage(PATH);
    expect(shown()).toBe(true);
    state.mode.value = { kind: "edit", editing: true };
    expect(shown()).toBe(false);
  });

  it("stays hidden in the error state, which is also what covers a binary", () => {
    // Verified rather than assumed: /api/file answers 415 for a binary, apiGet
    // collapses every non-2xx to null, and loadFile's null branch sets the
    // error — so a git-dirty .zip lands here and needs no clause of its own.
    expect.assertions(1);
    _setReposForTest(repos({ "hello.ts": "M" }));
    stage(PATH, { error: "Failed to load file" });
    expect(shown()).toBe(false);
  });

  it("stays hidden when no file is active", () => {
    expect.assertions(1);
    _setReposForTest(repos({ "hello.ts": "M" }));
    setActiveFilePath("");
    expect(shown()).toBe(false);
  });
});

describe("the two triggers", () => {
  it("appears when a status scan lands after the file is already open", () => {
    // The imperative trigger's whole reason: the predicate reads a plain Map, so
    // no reactive dependency exists on the store. A scan is the commonest way a
    // letter arrives — the file was clean when opened and the agent then wrote
    // it — and with one trigger the button stayed missing until a tab switch.
    expect.assertions(2);
    _setReposForTest(repos({}));
    stage(PATH);
    expect(shown()).toBe(false);
    _setReposForTest(repos({ "hello.ts": "M" }));
    expect(shown()).toBe(true);
  });

  it("stays visible during a git diff after the letter clears", () => {
    // Once the reader is looking at the diff this control is the way OUT, so a
    // commit landing underneath them must not withdraw it.
    expect.assertions(2);
    _setReposForTest(repos({ "hello.ts": "M" }));
    stage(PATH, { mode: { kind: "diff", diffSource: gitDiffSource("HEAD", "old", "new") } });
    expect(shown()).toBe(true);
    _setReposForTest(repos({}));
    expect(shown()).toBe(true);
  });
});

describe("an error that lands after the file is already active", () => {
  // The PRODUCTION ordering, which the staged-error case above structurally
  // cannot reach: it assigns the error and only then sets the active path, so the
  // effect reads it on its first and only run. Here the file is activated clean —
  // `activateFile` sets the path, `loadFile` resolves later — which is the order
  // every real failure arrives in.

  it("withdraws the control when the load fails on a dirty file", () => {
    // The headline case: a git-dirty BINARY. /api/file answers 415, apiGet
    // collapses it to null and `loadFile` assigns the error, then `restoreUI`
    // hides every sibling control by hand — and cannot hide this one, because it
    // has exactly one writer. So the predicate has to observe the field itself.
    expect.assertions(2);
    _setReposForTest(repos({ "hello.ts": "M" }));
    const state = stage(PATH);
    expect(shown()).toBe(true);
    state.error.value = "Failed to load file";
    expect(shown()).toBe(false);
  });

  it("restores the control when the error clears on the same path", () => {
    // The mirror, and it needs its own case: re-activating the same path writes an
    // equal value to the active-path signal, which a signal dedupes, so nothing
    // else re-runs the effect for that file.
    expect.assertions(2);
    _setReposForTest(repos({ "hello.ts": "M" }));
    const state = stage(PATH, { error: "Failed to load file" });
    expect(shown()).toBe(false);
    state.error.value = "";
    expect(shown()).toBe(true);
  });
});

describe("the toggle's two states", () => {
  it("keeps one accessible name and moves the state onto aria-pressed", () => {
    // A name that flips announces as "Exit diff view, pressed", which attaches a
    // state to a phrase describing the next press. The tooltip is the surface
    // with no state channel beside it, so it carries state plus action.
    expect.assertions(4);
    _setReposForTest(repos({ "hello.ts": "M" }));
    const state = stage(PATH);
    const restingName = $.editorGitDiffBtn.getAttribute("aria-label");
    const restingTip = $.editorGitDiffBtn.getAttribute("data-tooltip");
    // The MODE signal of the already-active file, not a re-stage: re-staging the
    // same path leaves the active-path signal unmoved, so the effect would not
    // re-run and every assertion below would read the resting state.
    state.mode.value = { kind: "diff", diffSource: gitDiffSource("HEAD", "old", "new") };
    expect($.editorGitDiffBtn.getAttribute("aria-label")).toBe(restingName);
    expect($.editorGitDiffBtn.getAttribute("aria-pressed")).toBe("true");
    expect($.editorGitDiffBtn.getAttribute("data-tooltip")).not.toBe(restingTip);
    expect($.editorGitDiffBtn.getAttribute("data-tooltip")).toContain("Exit");
  });

  it("opens the diff on a click at rest and leaves it on a click while pressed", () => {
    expect.assertions(3);
    _setReposForTest(repos({ "hello.ts": "M" }));
    const state = stage(PATH);
    $.editorGitDiffBtn.click();
    expect(vi.mocked(openFileGitDiff)).toHaveBeenCalledWith(PATH, "HEAD");

    state.mode.value = { kind: "diff", diffSource: gitDiffSource("HEAD", "old", "new") };
    $.editorGitDiffBtn.click();
    // The same exit `toggleDiffMode` takes, so the two cannot diverge.
    expect(state.mode.value.kind).toBe("edit");
    expect(vi.mocked(openFileGitDiff)).toHaveBeenCalledTimes(1);
  });
});

describe("which button exits which diff", () => {
  it("hides #editor-diff-btn for a git diff, so there is one way out", () => {
    // Both visible would make "enter with B, exit with A" spellable. The add is
    // not redundant: renderEditModeUI un-hides that button whenever the buffer
    // is dirty, so a dirty file entering a git diff arrives with it visible.
    expect.assertions(2);
    _setReposForTest(repos({ "hello.ts": "M" }));
    const state = stage(PATH);
    $.editorDiffBtn.classList.remove("hidden");
    state.mode.value = { kind: "diff", diffSource: gitDiffSource("HEAD", "old", "new") };
    renderDiffModeUI(state);
    expect($.editorDiffBtn.classList.contains("hidden")).toBe(true);
    expect(shown()).toBe(true);
  });

  it("still shows #editor-diff-btn for an unsaved-buffer diff", () => {
    expect.assertions(2);
    _setReposForTest(repos({}));
    const state = stage(PATH);
    $.editorDiffBtn.classList.add("hidden");
    state.mode.value = { kind: "diff", diffSource: unsavedDiffSource("saved", "unsaved") };
    renderDiffModeUI(state);
    expect($.editorDiffBtn.classList.contains("hidden")).toBe(false);
    // A buffer-vs-saved diff is not a git diff, so the git control has no part
    // in it — and this file is clean besides.
    expect(shown()).toBe(false);
  });
});
