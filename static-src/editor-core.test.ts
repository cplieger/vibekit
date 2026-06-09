// @vitest-environment happy-dom
import { describe, it, expect } from "vitest";
import { isPlanDraftPath, isPendingPath, parsePendingPath, routeForPath } from "./editor-core.js";

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
    { input: "pending:chat1", expected: { chatID: "chat1", toolCallID: "" } },
    { input: "pending:", expected: { chatID: "", toolCallID: "" } },
    { input: "pending:a:b:c", expected: { chatID: "a", toolCallID: "b:c" } },
    { input: "src/file.ts", expected: { chatID: "", toolCallID: "" } },
    { input: "", expected: { chatID: "", toolCallID: "" } },
  ])("parsePendingPath($input)", ({ input, expected }) => {
    expect(parsePendingPath(input)).toEqual(expected);
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
