// Table-driven tests for diff-pane.ts countHunks pure function.
// @vitest-environment happy-dom
import { describe, it, expect } from "vitest";
import { countHunks, renderDiffPane } from "./diff-pane.js";
import type { DiffLine } from "./diff.js";

function ctx(oldNo: number, newNo: number, text = ""): DiffLine {
  return { kind: "ctx", oldNo, newNo, text };
}
function add(newNo: number, text = ""): DiffLine {
  return { kind: "add", oldNo: 0, newNo, text };
}
function del(oldNo: number, text = ""): DiffLine {
  return { kind: "del", oldNo, newNo: 0, text };
}

describe("countHunks", () => {
  const cases: { name: string; lines: DiffLine[]; want: number }[] = [
    { name: "empty array", lines: [], want: 0 },
    { name: "all context lines", lines: [ctx(1, 1), ctx(2, 2), ctx(3, 3)], want: 0 },
    { name: "single add line", lines: [add(1)], want: 1 },
    { name: "single del line", lines: [del(1)], want: 1 },
    {
      name: "contiguous add+del block counts as one hunk",
      lines: [add(1), del(1), add(2)],
      want: 1,
    },
    {
      name: "two blocks separated by context",
      lines: [add(1), ctx(2, 2), del(3)],
      want: 2,
    },
    {
      name: "alternating add/ctx/del/ctx pattern",
      lines: [add(1), ctx(2, 2), del(3), ctx(4, 4)],
      want: 2,
    },
    {
      name: "trailing hunk with no final context",
      lines: [ctx(1, 1), add(2), del(3)],
      want: 1,
    },
    {
      name: "multiple context-separated hunks",
      lines: [del(1), ctx(2, 2), add(3), ctx(4, 4), del(5), add(6)],
      want: 3,
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(countHunks(tc.lines)).toBe(tc.want);
    });
  }
});

describe("renderDiffPane maxRows truncation", () => {
  function makeLines(n: number): DiffLine[] {
    return Array.from({ length: n }, (_, i) => add(i + 1, `line ${i + 1}`));
  }

  it("maxRows=5 with 10 lines renders 5 rows + footer", () => {
    const lines = makeLines(10);
    const el = renderDiffPane(lines, { maxRows: 5 });
    const footer = el.querySelector(".diff-more");
    expect(footer).not.toBeNull();
    expect(footer!.textContent).toBe("+5 more lines");
  });

  it("maxRows=undefined renders all lines without footer", () => {
    const lines = makeLines(10);
    const el = renderDiffPane(lines, {});
    const footer = el.querySelector(".diff-more");
    expect(footer).toBeNull();
  });

  it("maxRows larger than line count renders all without footer", () => {
    const lines = makeLines(3);
    const el = renderDiffPane(lines, { maxRows: 10 });
    const footer = el.querySelector(".diff-more");
    expect(footer).toBeNull();
  });

  it("maxRows=1 with 2 lines shows +1 more line (singular)", () => {
    const lines = makeLines(2);
    const el = renderDiffPane(lines, { maxRows: 1 });
    const footer = el.querySelector(".diff-more");
    expect(footer).not.toBeNull();
    expect(footer!.textContent).toBe("+1 more line");
  });
});
