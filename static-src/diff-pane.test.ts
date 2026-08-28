// Table-driven tests for diff-pane.ts countHunks pure function.
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

// ---------------------------------------------------------------------------
// The no-changes state. An all-context diff rendered as two identical file
// listings reads as broken markup, and it is the ORDINARY case for a chat's
// changed-file link: that link diffs HEAD against the working tree, so once
// the write is committed the two agree.
// ---------------------------------------------------------------------------

describe("renderDiffPane with nothing changed", () => {
  it("says so instead of laying out two identical columns", () => {
    const pane = renderDiffPane([ctx(1, 1, "same"), ctx(2, 2, "also same")], {});
    expect(pane.querySelector(".diff-none")?.textContent).toBe("No changes between these versions");
    expect(pane.querySelector(".diff-pane-body")).toBeNull();
    expect(pane.querySelectorAll(".diff-row")).toHaveLength(0);
  });

  it("distinguishes an empty file from an unchanged one", () => {
    expect(renderDiffPane([], {}).querySelector(".diff-none")?.textContent).toBe("Empty file");
  });

  it("keeps the header, so the whitespace toggle is still reachable", () => {
    // Ignoring whitespace can be what collapsed the diff to context in the
    // first place; without the header there is no way to turn it back off.
    const pane = renderDiffPane([ctx(1, 1, "same")], {
      oldLabel: "HEAD",
      newLabel: "working tree",
      source: { oldText: "same", newText: "same" },
    });
    expect(pane.querySelector(".diff-pane-header")).not.toBeNull();
    expect(pane.querySelector(".diff-pane-ws-toggle")).not.toBeNull();
    expect(pane.querySelector(".diff-none")).not.toBeNull();
  });

  it("renders rows as soon as one line differs", () => {
    const pane = renderDiffPane([ctx(1, 1, "same"), add(2, "new")], {});
    expect(pane.querySelector(".diff-none")).toBeNull();
    expect(pane.querySelector(".diff-pane-body")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Word-level marks and syntax highlighting, in BOTH shapes. The two used to
// disagree: only the unified column highlighted, so clicking a changed filename
// in chat landed on a flatter rendering than the inline peek that sent you.
// ---------------------------------------------------------------------------

function textsOf(pane: HTMLElement, sel: string): string[] {
  return Array.from(pane.querySelectorAll(sel), (n) => n.textContent ?? "");
}

const MODIFIED: DiffLine[] = [
  del(1, `fmt.Println("total", total)`),
  add(1, `fmt.Println("sum:", total)`),
];

describe("renderDiffPane word marks", () => {
  it("marks only the changed word, in the unified shape", () => {
    const pane = renderDiffPane(MODIFIED, { unified: true, lang: "main.go" });
    expect(textsOf(pane, ".diff-word-del")).toEqual(["total"]);
    expect(textsOf(pane, ".diff-word-add")).toEqual(["sum:"]);
  });

  it("marks only the changed word, in the two-pane shape", () => {
    const pane = renderDiffPane(MODIFIED, { lang: "main.go" });
    expect(textsOf(pane, ".diff-col-old .diff-word-del")).toEqual(["total"]);
    expect(textsOf(pane, ".diff-col-new .diff-word-add")).toEqual(["sum:"]);
  });

  it("marks nothing when the pair is a whole-line rewrite", () => {
    const pane = renderDiffPane([del(1, "alpha"), add(1, "omega")], { lang: "main.go" });
    expect(pane.querySelectorAll(".diff-word-add, .diff-word-del")).toHaveLength(0);
  });
});

describe("renderDiffPane syntax highlighting", () => {
  it("resolves the language from a file PATH, not just an extension", () => {
    // `normalizeLang` compares the whole string, so a path matched nothing and
    // every diff in the app rendered unhighlighted. Both callers pass a path.
    const pane = renderDiffPane([add(1, "func main() {")], {
      unified: true,
      lang: "internal/git/exec.go",
    });
    expect(textsOf(pane, ".hl-keyword")).toContain("func");
  });

  it("highlights BOTH columns of the two-pane shape", () => {
    const pane = renderDiffPane(MODIFIED, { lang: "main.go" });
    expect(pane.querySelectorAll(".diff-col-old .hl-string").length).toBeGreaterThan(0);
    expect(pane.querySelectorAll(".diff-col-new .hl-string").length).toBeGreaterThan(0);
  });

  it("falls back to plain text for a language it does not know", () => {
    const pane = renderDiffPane([add(1, "func main() {")], { unified: true, lang: "notes.zzz" });
    expect(pane.querySelectorAll(".hl-keyword")).toHaveLength(0);
    expect(textsOf(pane, ".diff-line-text")).toEqual(["func main() {"]);
  });

  it("gives every row a .diff-line-text, both shapes", () => {
    for (const opts of [{ unified: true }, {}]) {
      const pane = renderDiffPane([ctx(1, 1, "a"), ...MODIFIED], opts);
      const rows = pane.querySelectorAll(".diff-row");
      for (const row of rows) {
        expect(row.querySelector(".diff-line-text")).not.toBeNull();
      }
    }
  });
});
