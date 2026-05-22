// @vitest-environment happy-dom
import { describe, it, expect } from "vitest";
import { buildPartialMergeText } from "./editor-pending.js";
import type { FileState } from "./editor-core.js";
import type { DiffLine } from "./diff.js";

function makeState(diff: DiffLine[]): FileState {
  return {
    path: "test.ts",
    original: "",
    current: "",
    loaded: true,
    error: "",
    mode: { kind: "edit", editing: false },
    suggestions: new Map(),
    returnToGitDiff: null,
    pendingHunkDecisions: new Map(),
    pendingHunkCount: null,
    cachedDiff: diff,
  };
}

function ctx(text: string, oldNo = 0, newNo = 0): DiffLine {
  return { kind: "ctx", text, oldNo, newNo };
}
function del(text: string, oldNo = 0): DiffLine {
  return { kind: "del", text, oldNo, newNo: 0 };
}
function add(text: string, newNo = 0): DiffLine {
  return { kind: "add", text, oldNo: 0, newNo };
}

describe("buildPartialMergeText", () => {
  it("single hunk accept → uses new lines", () => {
    const diff: DiffLine[] = [
      ctx("line1"), del("old"), add("new"), ctx("line3"),
    ];
    const decisions = new Map([[0, "accept" as const]]);
    expect(buildPartialMergeText(makeState(diff), decisions)).toBe("line1\nnew\nline3");
  });

  it("single hunk reject → uses old lines", () => {
    const diff: DiffLine[] = [
      ctx("line1"), del("old"), add("new"), ctx("line3"),
    ];
    const decisions = new Map([[0, "reject" as const]]);
    expect(buildPartialMergeText(makeState(diff), decisions)).toBe("line1\nold\nline3");
  });

  it("multi-hunk mixed decisions", () => {
    const diff: DiffLine[] = [
      ctx("a"), del("b"), add("B"), ctx("c"), del("d"), add("D"), ctx("e"),
    ];
    const decisions = new Map([
      [0, "accept" as const],
      [1, "reject" as const],
    ]);
    expect(buildPartialMergeText(makeState(diff), decisions)).toBe("a\nB\nc\nd\ne");
  });

  it("empty diff → empty string", () => {
    expect(buildPartialMergeText(makeState([]), new Map())).toBe("");
  });

  it("all-context diff → context lines joined", () => {
    const diff: DiffLine[] = [ctx("x"), ctx("y"), ctx("z")];
    expect(buildPartialMergeText(makeState(diff), new Map())).toBe("x\ny\nz");
  });

  it("consecutive hunks with no context between them", () => {
    // Two hunks back-to-back (no ctx separator) — each del/add pair is one hunk
    // Actually, without a ctx line between them, they form ONE hunk
    const diff: DiffLine[] = [
      del("a"), add("A"), del("b"), add("B"),
    ];
    // This is actually one hunk (no ctx to separate), so hunk 0 gets all
    const decisions = new Map([[0, "accept" as const]]);
    expect(buildPartialMergeText(makeState(diff), decisions)).toBe("A\nB");
  });

  it("hunk at EOF (no trailing context)", () => {
    const diff: DiffLine[] = [ctx("start"), del("old"), add("new")];
    const decisions = new Map([[0, "accept" as const]]);
    expect(buildPartialMergeText(makeState(diff), decisions)).toBe("start\nnew");
  });

  it("undecided hunks default to reject", () => {
    const diff: DiffLine[] = [ctx("a"), del("old"), add("new"), ctx("b")];
    // No decision for hunk 0 → defaults to "reject"
    expect(buildPartialMergeText(makeState(diff), new Map())).toBe("a\nold\nb");
  });
});
