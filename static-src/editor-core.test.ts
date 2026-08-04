// @vitest-environment happy-dom
import { describe, it, expect, beforeEach } from "vitest";
import { effect } from "@cplieger/reactive";
import { routeForPath } from "./editor-core.js";
import { freshState, fileStates, setActiveFilePath, activeDirty } from "./editor-types.js";

// There are no isPendingPath / parsePendingPath tests: the `pending:` virtual
// path family is gone with vibekit's staging store. A path is a real file path
// or it is not routable.
describe("routeForPath", () => {
  it("routes plain file paths", () => {
    const r = routeForPath("src/main.ts");
    expect(r.readURL).toBe("/api/file?path=src%2Fmain.ts");
    expect(r.writeURL).toBe("/api/file?path=src%2Fmain.ts");
    expect(r.displayPath).toBe("src/main.ts");
  });

  it("encodes special characters in file paths", () => {
    const r = routeForPath("path with spaces/file#1.ts");
    expect(r.readURL).toBe("/api/file?path=path%20with%20spaces%2Ffile%231.ts");
  });

  it("handles empty path", () => {
    const r = routeForPath("");
    expect(r.readURL).toBe("/api/file?path=");
    expect(r.displayPath).toBe("");
  });
});

describe("getCachedDiff", () => {
  it("edit mode returns an empty array", async () => {
    const { getCachedDiff, freshState } = await import("./editor-types.js");
    const state = freshState("test.ts");
    state.mode.value = { kind: "edit", editing: false };
    expect(getCachedDiff(state)).toEqual([]);
  });

  it("diff mode computes the line diff of the diff source", async () => {
    const { getCachedDiff, freshState } = await import("./editor-types.js");
    const state = freshState("test.ts");
    state.mode.value = {
      kind: "diff",
      diffSource: {
        oldContent: "a\nb\n",
        newContent: "a\nc\n",
        oldLabel: "old",
        newLabel: "new",
        fromGit: false,
      },
    };
    const diff = getCachedDiff(state);
    // "b" deleted, "c" added; "a" stays as context.
    expect(diff.some((l) => l.kind === "del" && l.text === "b")).toBe(true);
    expect(diff.some((l) => l.kind === "add" && l.text === "c")).toBe(true);
    expect(diff.some((l) => l.kind === "ctx" && l.text === "a")).toBe(true);
  });

  it("memoizes: two reads with mode unchanged return the same array reference", async () => {
    const { getCachedDiff, freshState } = await import("./editor-types.js");
    const state = freshState("test.ts");
    state.mode.value = {
      kind: "diff",
      diffSource: {
        oldContent: "a\nb\n",
        newContent: "a\nc\n",
        oldLabel: "old",
        newLabel: "new",
        fromGit: false,
      },
    };
    const first = getCachedDiff(state);
    expect(first.length).toBeGreaterThan(0);
    const second = getCachedDiff(state);
    expect(second).toBe(first); // same reference — the computed caches
  });

  it("auto-invalidates: reassigning mode (no manual cache reset) yields the new diff", async () => {
    const { getCachedDiff, freshState } = await import("./editor-types.js");
    const state = freshState("test.ts");
    state.mode.value = {
      kind: "diff",
      diffSource: {
        oldContent: "a\nb",
        newContent: "a\nc",
        oldLabel: "old",
        newLabel: "new",
        fromGit: false,
      },
    };
    const before = getCachedDiff(state);
    expect(before.some((l) => l.kind === "add" && l.text === "c")).toBe(true);

    // Reassign mode ONLY. The pre-reactive code required an explicit
    // `state.cachedDiff = null` here; the computed must refresh on its own.
    state.mode.value = {
      kind: "diff",
      diffSource: {
        oldContent: "a\nb",
        newContent: "a\nZ",
        oldLabel: "old",
        newLabel: "new",
        fromGit: false,
      },
    };
    const after = getCachedDiff(state);
    expect(after).not.toBe(before); // recomputed, not the stale cached array
    expect(after.some((l) => l.kind === "add" && l.text === "Z")).toBe(true);
    expect(after.some((l) => l.kind === "add" && l.text === "c")).toBe(false);
  });

  it("pendingHunkCount auto-recomputes when mode changes", async () => {
    const { freshState } = await import("./editor-types.js");
    const state = freshState("test.ts");

    // One changed line between two context lines → exactly one hunk.
    state.mode.value = {
      kind: "diff",
      diffSource: {
        oldContent: "a\nb\nc",
        newContent: "a\nX\nc",
        oldLabel: "old",
        newLabel: "new",
        fromGit: false,
      },
    };
    expect(state.pendingHunkCount.value).toBe(1);

    // Three changed lines separated by unchanged context → three hunks.
    state.mode.value = {
      kind: "diff",
      diffSource: {
        oldContent: "1\n2\n3\n4\n5",
        newContent: "A\n2\nB\n4\nC",
        oldLabel: "old",
        newLabel: "new",
        fromGit: false,
      },
    };
    expect(state.pendingHunkCount.value).toBe(3);

    // Leaving diff mode → no hunks, with no manual reset.
    state.mode.value = { kind: "edit", editing: false };
    expect(state.pendingHunkCount.value).toBe(0);
  });
});

describe("FileState.dirty (reactive)", () => {
  it("is false when current === original, true after an edit, false again after save", () => {
    const state = freshState("d.ts");
    // Both signals start "" → clean.
    expect(state.dirty.value).toBe(false);

    // Edit diverges current from original → dirty.
    state.current.value = "x";
    expect(state.dirty.value).toBe(true);

    // Save converges original to current → clean again.
    state.original.value = "x";
    expect(state.dirty.value).toBe(false);
  });
});

describe("activeDirty (reactive, tab-switch aware)", () => {
  beforeEach(() => {
    fileStates.clear();
    setActiveFilePath("");
  });

  it("tracks the active file's dirty flag and re-tracks on tab switch", () => {
    const dirtyFile = freshState("dirty.ts");
    dirtyFile.current.value = "edited"; // current !== original ("")
    fileStates.set("dirty.ts", dirtyFile);

    const cleanFile = freshState("clean.ts"); // current === original
    fileStates.set("clean.ts", cleanFile);

    // No active file → not dirty.
    expect(activeDirty.value).toBe(false);

    // Activate the dirty file → activeDirty true.
    setActiveFilePath("dirty.ts");
    expect(activeDirty.value).toBe(true);

    // Switch active to the clean file → activeDirty re-tracks the newly
    // active file's dirty signal → false (proves tab-switch reactivity).
    setActiveFilePath("clean.ts");
    expect(activeDirty.value).toBe(false);
  });

  it("flips when the active file's content changes (edit then save)", () => {
    const state = freshState("f.ts");
    fileStates.set("f.ts", state);
    setActiveFilePath("f.ts");
    expect(activeDirty.value).toBe(false);

    state.current.value = "typed";
    expect(activeDirty.value).toBe(true);

    state.original.value = "typed"; // save
    expect(activeDirty.value).toBe(false);
  });
});

describe("save-button effect logic (activeDirty drives disabled)", () => {
  beforeEach(() => {
    fileStates.clear();
    setActiveFilePath("");
  });

  it("disabled tracks activeDirty: clean → disabled, dirty → enabled", () => {
    // Replicates the editor-core effect WITHOUT the in-flight term — no save
    // is dispatched in this harness, so isPending("editor.save_file") is
    // false and `disabled = !activeDirty.value || isPending(...)` reduces to
    // `disabled = !activeDirty.value`. Asserts the dirty half the effect is a
    // thin wrapper over. The reactive effect re-runs synchronously on each
    // signal write (the signal setter flushes effects in its own batch).
    const btn = document.createElement("button");
    const dispose = effect(() => {
      btn.disabled = !activeDirty.value;
    });
    // No active file → clean → disabled.
    expect(btn.disabled).toBe(true);

    const state = freshState("e.ts");
    fileStates.set("e.ts", state);
    setActiveFilePath("e.ts");
    expect(btn.disabled).toBe(true); // clean file → still disabled

    state.current.value = "edit"; // dirty
    expect(btn.disabled).toBe(false); // enabled exactly when dirty

    state.original.value = "edit"; // save → clean
    expect(btn.disabled).toBe(true); // disabled again

    dispose();
  });
});
