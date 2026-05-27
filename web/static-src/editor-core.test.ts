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
  it("first call computes and caches diff, second returns same reference", async () => {
    const { getCachedDiff, freshState } = await import("./editor-types.js");
    const state = freshState("test.ts");
    state.mode = {
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
    expect(second).toBe(first); // same reference — cached
  });

  it("non-diff mode returns empty array", async () => {
    const { getCachedDiff, freshState } = await import("./editor-types.js");
    const state = freshState("test.ts");
    state.mode = { kind: "edit", editing: false };
    expect(getCachedDiff(state)).toEqual([]);
  });
});
