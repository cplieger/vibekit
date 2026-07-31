// @vitest-environment happy-dom
import { describe, it, expect, beforeEach } from "vitest";
import { effect } from "@cplieger/reactive";
import { isPlanDraftPath, isPendingPath, parsePendingPath, routeForPath } from "./editor-core.js";
import {
  freshState,
  fileStates,
  setActiveFilePath,
  getDirtyEditorPaths,
  activeDirty,
  makePendingPath,
} from "./editor-types.js";

describe("isPlanDraftPath", () => {
  it.each([
    { input: "plan-draft:abc123", expected: true },
    { input: "plan-draft:", expected: true },
    { input: "plan-draft:with:colons", expected: true },
    { input: "pending:abc", expected: false },
    { input: "src/main.ts", expected: false },
    { input: "", expected: false },
    { input: "plan-draftx:abc", expected: false },
  ])("isPlanDraftPath($input) === $expected", ({ input, expected }) => {
    expect(isPlanDraftPath(input)).toBe(expected);
  });
});

describe("isPendingPath", () => {
  it.each([
    { input: "pending:chat1:tool1", expected: true },
    { input: "pending:", expected: true },
    { input: "pending:abc", expected: true },
    { input: "plan-draft:abc", expected: false },
    { input: "src/file.ts", expected: false },
    { input: "", expected: false },
    { input: "pendingx:abc", expected: false },
  ])("isPendingPath($input) === $expected", ({ input, expected }) => {
    expect(isPendingPath(input)).toBe(expected);
  });
});

describe("parsePendingPath", () => {
  it.each([
    { input: "pending:chat1:tool1", expected: { chatID: "chat1", toolCallID: "tool1" } },
    // Exactly-two-components rule (keyenc adoption): a path carrying fewer or
    // more components is not a key makePendingPath produced, so it degrades to
    // the empty pair — the same result callers already get for an unrecognized
    // path. "pending:chat1" used to yield chatID "chat1"; "pending:a:b:c" used
    // to yield toolCallID "b:c" because the old parser took it as
    // rest-of-string. Both now fail to parse (see parsePendingPath's doc
    // comment: a legacy persisted path with a ":" in its toolCallID loads an
    // empty tab once, and is re-encoded escaped from now on).
    { input: "pending:chat1", expected: { chatID: "", toolCallID: "" } },
    { input: "pending:", expected: { chatID: "", toolCallID: "" } },
    { input: "pending:a:b:c", expected: { chatID: "", toolCallID: "" } },
    { input: "src/file.ts", expected: { chatID: "", toolCallID: "" } },
    { input: "", expected: { chatID: "", toolCallID: "" } },
  ])("parsePendingPath($input)", ({ input, expected }) => {
    expect(parsePendingPath(input)).toEqual(expected);
  });
});

// ---------------------------------------------------------------------------
// makePendingPath / parsePendingPath under keyenc.
//
// These keys are PERSISTED: `persistOpenFiles` writes every open editor path
// into localStorage via `uiState.save({editor_files})`, so the encoding must
// stay byte-stable for ordinary ids or a reload drops the user's open tabs.
// ---------------------------------------------------------------------------

describe("makePendingPath (keyenc byte-identity)", () => {
  it.each([
    { chatID: "c-1750000000000-ab12cd", toolCallID: "tooluse_abc123" },
    { chatID: "chat1", toolCallID: "tool1" },
    { chatID: "c_1-2", toolCallID: "tc-42" },
  ])(
    "emits the pre-adoption bytes for colon-free ids ($chatID, $toolCallID)",
    ({ chatID, toolCallID }) => {
      // The exact string the old template produced. keyenc emits a component
      // containing neither ":" nor "\\" verbatim, so already-persisted
      // editor_files entries still parse after the adoption.
      expect(makePendingPath(chatID, toolCallID)).toBe(`pending:${chatID}:${toolCallID}`);
    },
  );

  it("round-trips ids that the old encoding could not represent", () => {
    // A ":" in the toolCallID used to shift the split: the old parser took
    // rest-of-string, so ("a:b", "c") and ("a", "b:c") were indistinguishable.
    const forged = makePendingPath("a:b", "c");
    const other = makePendingPath("a", "b:c");
    expect(forged).not.toBe(other);
    expect(parsePendingPath(forged)).toEqual({ chatID: "a:b", toolCallID: "c" });
    expect(parsePendingPath(other)).toEqual({ chatID: "a", toolCallID: "b:c" });
  });

  it("round-trips a backslash, the other reserved character", () => {
    const p = makePendingPath("c\\1", "tc:2");
    expect(parsePendingPath(p)).toEqual({ chatID: "c\\1", toolCallID: "tc:2" });
  });

  it("stays parseable for every path it produces", () => {
    for (const [chatID, toolCallID] of [
      ["", ""],
      ["a", ""],
      ["", "b"],
      [":", ":"],
      ["\\", "\\"],
      ["a:b\\c", "d\\e:f"],
    ] as const) {
      const p = makePendingPath(chatID, toolCallID);
      expect(isPendingPath(p)).toBe(true);
      expect(parsePendingPath(p)).toEqual({ chatID, toolCallID });
    }
  });
});

describe("parsePendingPath (total, never throws)", () => {
  it.each([
    // A dangling escape and an escape before an ordinary character are both
    // outside keyenc's accepted language — `split` throws MalformedKeyError,
    // which the parser catches and reports as "not a pending path".
    "pending:a\\",
    "pending:a\\b",
    // A hashed identity throws HashedKeyError; same total degradation.
    `pending:sha256:${"0".repeat(64)}`,
    // Structurally valid keys with the wrong component count.
    "pending:a:b:c",
    "pending:onlyone",
    "pending:",
  ])("returns the empty pair for %p instead of throwing", (path) => {
    expect(() => parsePendingPath(path)).not.toThrow();
    expect(parsePendingPath(path)).toEqual({ chatID: "", toolCallID: "" });
  });
});

describe("routeForPath", () => {
  it("routes plan-draft paths", () => {
    const r = routeForPath("plan-draft:abc123def456");
    expect(r.readURL).toBe("/api/chats/abc123def456/plan-draft");
    expect(r.writeURL).toBe("/api/chats/abc123def456/plan-draft");
    expect(r.displayPath).toBe("plan draft · abc123def456");
  });

  it("routes pending paths", () => {
    const r = routeForPath("pending:chat1:tool42");
    expect(r.readURL).toBe("/api/pending-changes/tool42");
    expect(r.writeURL).toBe("/api/pending-changes/tool42");
    expect(r.displayPath).toBe("pending change");
  });

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

describe("getDirtyEditorPaths", () => {
  beforeEach(() => {
    fileStates.clear();
    setActiveFilePath("");
  });

  it("returns only loaded files whose current differs from original", () => {
    const loadedDirty = freshState("a.ts");
    loadedDirty.loaded = true;
    loadedDirty.current.value = "changed";
    fileStates.set("a.ts", loadedDirty);

    const loadedClean = freshState("b.ts");
    loadedClean.loaded = true;
    fileStates.set("b.ts", loadedClean);

    // Dirty but not loaded → excluded.
    const unloadedDirty = freshState("c.ts");
    unloadedDirty.current.value = "changed";
    fileStates.set("c.ts", unloadedDirty);

    expect(getDirtyEditorPaths()).toEqual(["a.ts"]);
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
