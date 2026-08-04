// Table-driven tests for diff.ts — lineDiff and windowHunks.
import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { lineDiff, windowHunks, stats, type DiffLine } from "./diff.js";

describe("lineDiff", () => {
  const cases: {
    name: string;
    old: string;
    new: string;
    opts?: { ignoreWhitespace?: boolean };
    expected: DiffLine[];
  }[] = [
    {
      name: "identical texts produce all context",
      old: "a\nb\nc",
      new: "a\nb\nc",
      expected: [
        { kind: "ctx", oldNo: 1, newNo: 1, text: "a" },
        { kind: "ctx", oldNo: 2, newNo: 2, text: "b" },
        { kind: "ctx", oldNo: 3, newNo: 3, text: "c" },
      ],
    },
    {
      name: "completely different texts",
      old: "a\nb",
      new: "x\ny",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "a" },
        { kind: "del", oldNo: 2, newNo: 0, text: "b" },
        { kind: "add", oldNo: 0, newNo: 1, text: "x" },
        { kind: "add", oldNo: 0, newNo: 2, text: "y" },
      ],
    },
    {
      name: "single add",
      old: "a\nc",
      new: "a\nb\nc",
      expected: [
        { kind: "ctx", oldNo: 1, newNo: 1, text: "a" },
        { kind: "add", oldNo: 0, newNo: 2, text: "b" },
        { kind: "ctx", oldNo: 2, newNo: 3, text: "c" },
      ],
    },
    {
      name: "single del",
      old: "a\nb\nc",
      new: "a\nc",
      expected: [
        { kind: "ctx", oldNo: 1, newNo: 1, text: "a" },
        { kind: "del", oldNo: 2, newNo: 0, text: "b" },
        { kind: "ctx", oldNo: 3, newNo: 2, text: "c" },
      ],
    },
    {
      name: "interleaved changes",
      old: "a\nb\nc\nd",
      new: "a\nX\nc\nY",
      expected: [
        { kind: "ctx", oldNo: 1, newNo: 1, text: "a" },
        { kind: "del", oldNo: 2, newNo: 0, text: "b" },
        { kind: "add", oldNo: 0, newNo: 2, text: "X" },
        { kind: "ctx", oldNo: 3, newNo: 3, text: "c" },
        { kind: "del", oldNo: 4, newNo: 0, text: "d" },
        { kind: "add", oldNo: 0, newNo: 4, text: "Y" },
      ],
    },
    {
      name: "empty old text (all adds)",
      old: "",
      new: "a\nb",
      expected: [
        { kind: "add", oldNo: 0, newNo: 1, text: "a" },
        { kind: "add", oldNo: 0, newNo: 2, text: "b" },
      ],
    },
    {
      name: "empty new text (all dels)",
      old: "a\nb",
      new: "",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "a" },
        { kind: "del", oldNo: 2, newNo: 0, text: "b" },
      ],
    },
    {
      name: "both empty",
      old: "",
      new: "",
      expected: [],
    },
    {
      name: "whitespace-only diff without ignoreWhitespace",
      old: "  a",
      new: "a",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "  a" },
        { kind: "add", oldNo: 0, newNo: 1, text: "a" },
      ],
    },
    {
      name: "whitespace-only diff with ignoreWhitespace",
      old: "  a",
      new: "a",
      opts: { ignoreWhitespace: true },
      expected: [{ kind: "ctx", oldNo: 1, newNo: 1, text: "  a" }],
    },
    {
      name: "internal whitespace diff with ignoreWhitespace",
      old: "a  b",
      new: "a b",
      opts: { ignoreWhitespace: true },
      expected: [{ kind: "ctx", oldNo: 1, newNo: 1, text: "a  b" }],
    },
    {
      name: "single line identical",
      old: "hello",
      new: "hello",
      expected: [{ kind: "ctx", oldNo: 1, newNo: 1, text: "hello" }],
    },
    {
      name: "CRLF input: \\r stripped from line text (EOL owned by consumers)",
      old: "a\r\nold\r\nc",
      new: "a\r\nnew\r\nc",
      expected: [
        { kind: "ctx", oldNo: 1, newNo: 1, text: "a" },
        { kind: "del", oldNo: 2, newNo: 0, text: "old" },
        { kind: "add", oldNo: 0, newNo: 2, text: "new" },
        { kind: "ctx", oldNo: 3, newNo: 3, text: "c" },
      ],
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      const result = lineDiff(tc.old, tc.new, tc.opts);
      expect(result).toEqual(tc.expected);
    });
  }

  it("falls back to a coarse but valid script past the time budget", () => {
    // 5100×5100 = 26M cells > TIME_BUDGET_CELLS (25M); every line differs
    // so prefix/suffix trimming removes nothing.
    const n = 5100;
    const oldText = Array.from({ length: n }, (_, i) => `left ${String(i)}`).join("\n");
    const newText = Array.from({ length: n }, (_, i) => `right ${String(i)}`).join("\n");
    const result = lineDiff(oldText, newText);
    const s = stats(result);
    // The fallback is still a VALID edit script: old = dels + ctx,
    // new = adds + ctx, and replaying ctx+add reproduces the new text.
    expect(s.dels + s.ctx).toBe(n);
    expect(s.adds + s.ctx).toBe(n);
    const reconstructed = result.filter((l) => l.kind !== "del").map((l) => l.text);
    expect(reconstructed).toEqual(newText.split("\n"));
  });

  it("bounded time: shared prefix/suffix keeps huge similar files on the exact path", () => {
    // Two 100k-line files differing in one middle line: the trim reduces
    // the exact-diff middle to 1×1, so this must produce a minimal script
    // (no coarse fallback) and return quickly.
    const n = 100_000;
    const lines = Array.from({ length: n }, (_, i) => `line ${String(i)}`);
    const oldText = lines.join("\n");
    const changed = [...lines];
    changed[n / 2] = "CHANGED";
    const newText = changed.join("\n");
    const result = lineDiff(oldText, newText);
    const s = stats(result);
    expect(s.dels).toBe(1);
    expect(s.adds).toBe(1);
    expect(s.ctx).toBe(n - 1);
  });
});

describe("windowHunks", () => {
  // The unit is the HUNK, which is the whole point of the replacement: keeping
  // the first three CHANGED LINES showed a 12-line rewrite as a quarter of
  // itself, cut mid-thought.
  const ctx = (n: number, t: string): DiffLine => ({ kind: "ctx", oldNo: n, newNo: n, text: t });
  const del = (n: number, t: string): DiffLine => ({ kind: "del", oldNo: n, newNo: 0, text: t });
  const add = (n: number, t: string): DiffLine => ({ kind: "add", oldNo: 0, newNo: n, text: t });

  it("a diff with no changes yields nothing — there is no hunk to show", () => {
    const got = windowHunks([ctx(1, "a"), ctx(2, "b")]);
    expect(got.lines).toEqual([]);
    expect(got.hunksOmitted).toBe(0);
  });

  it("empty input is empty", () => {
    expect(windowHunks([])).toEqual({ lines: [], hunksOmitted: 0 });
  });

  it("keeps a whole hunk even when it exceeds maxRows, rather than cutting it", () => {
    const hunk = [del(1, "a"), del(2, "b"), add(1, "c"), add(2, "d"), add(3, "e")];
    const got = windowHunks(hunk, { maxRows: 2 });
    // The first hunk always goes in whole: a half-hunk is the failure mode this
    // function exists to remove.
    expect(got.lines).toEqual(hunk);
    expect(got.hunksOmitted).toBe(0);
  });

  it("drops a LATER hunk whole when the cap is reached, and counts it", () => {
    const lines = [del(1, "a"), add(1, "b"), ctx(2, "keep"), del(3, "c"), add(3, "d")];
    const got = windowHunks(lines, { maxRows: 2, context: 1 });
    expect(got.hunksOmitted).toBe(1);
    // Nothing from the dropped hunk survives.
    expect(got.lines.some((l) => l.text === "c" || l.text === "d")).toBe(false);
  });

  it("keeps context adjoining a hunk and elides a long run between two", () => {
    const lines = [
      ctx(1, "far-before"),
      ctx(2, "near-before"),
      del(3, "changed"),
      ctx(4, "near-after"),
      ctx(5, "middle"),
      ctx(6, "middle2"),
      ctx(7, "near-before2"),
      add(8, "changed2"),
    ];
    const got = windowHunks(lines, { maxRows: 40, context: 1 });
    const texts = got.lines.map((l) => l.text);
    expect(texts).toContain("near-before");
    expect(texts).toContain("near-after");
    expect(texts).toContain("near-before2");
    // The interior of a long context run is elided.
    expect(texts).not.toContain("middle");
    // A context run touching no hunk on its far side contributes nothing there.
    expect(texts).not.toContain("far-before");
    expect(got.hunksOmitted).toBe(0);
  });

  it("keeps a short context run whole rather than double-counting its ends", () => {
    const lines = [del(1, "x"), ctx(2, "only"), add(3, "y")];
    const got = windowHunks(lines, { context: 2 });
    expect(got.lines.filter((l) => l.text === "only")).toHaveLength(1);
  });
});

describe("stats", () => {
  it("counts adds, dels, and ctx", () => {
    const lines: DiffLine[] = [
      { kind: "add", oldNo: 0, newNo: 1, text: "a" },
      { kind: "del", oldNo: 1, newNo: 0, text: "b" },
      { kind: "ctx", oldNo: 2, newNo: 2, text: "c" },
      { kind: "add", oldNo: 0, newNo: 3, text: "d" },
    ];
    expect(stats(lines)).toEqual({ adds: 2, dels: 1, ctx: 1 });
  });

  it("returns zeros for empty input", () => {
    expect(stats([])).toEqual({ adds: 0, dels: 0, ctx: 0 });
  });
});

describe("lineDiff property-based invariants", () => {
  /** Helper: count lines in a string (matching splitLines logic). */
  function countLines(s: string): number {
    if (s === "") {
      return 0;
    }
    return s.split("\n").length;
  }

  /** Reconstruct the new text from diff output by taking add+ctx text in order. */
  function reconstructNew(lines: DiffLine[]): string {
    const parts: string[] = [];
    for (const l of lines) {
      if (l.kind === "add" || l.kind === "ctx") {
        parts.push(l.text);
      }
    }
    return parts.length === 0 ? "" : parts.join("\n");
  }

  // Arbitrary for multi-line strings (small inputs — dense LCS path)
  const smallText = fc
    .array(fc.string({ minLength: 0, maxLength: 20 }), { minLength: 0, maxLength: 30 })
    .map((lines) => lines.join("\n"));

  it("invariant 1: adds + ctx === countLines(newText)", () => {
    fc.assert(
      fc.property(smallText, smallText, (a, b) => {
        const d = lineDiff(a, b);
        const s = stats(d);
        expect(s.adds + s.ctx).toBe(countLines(b));
      }),
    );
  });

  it("invariant 2: dels + ctx === countLines(oldText)", () => {
    fc.assert(
      fc.property(smallText, smallText, (a, b) => {
        const d = lineDiff(a, b);
        const s = stats(d);
        expect(s.dels + s.ctx).toBe(countLines(a));
      }),
    );
  });

  it("invariant 3: applying diff reconstructs newText", () => {
    fc.assert(
      fc.property(smallText, smallText, (a, b) => {
        const d = lineDiff(a, b);
        expect(reconstructNew(d)).toBe(b);
      }),
    );
  });

  it("invariant 4: lineDiff(a, a) produces only ctx entries", () => {
    fc.assert(
      fc.property(smallText, (a) => {
        const d = lineDiff(a, a);
        for (const l of d) {
          expect(l.kind).toBe("ctx");
        }
      }),
    );
  });

  it("invariant 5: lineDiff('', b) produces only add entries", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 1, maxLength: 100 }), (b) => {
        const d = lineDiff("", b);
        for (const l of d) {
          expect(l.kind).toBe("add");
        }
      }),
    );
  });

  it("invariant 6: lineDiff(a, '') produces only del entries", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 1, maxLength: 100 }), (a) => {
        const d = lineDiff(a, "");
        for (const l of d) {
          expect(l.kind).toBe("del");
        }
      }),
    );
  });

  // Hirschberg path: generate inputs exceeding SPACE_THRESHOLD (4M cells)
  // 2001 lines × 2001 lines = ~4M+ cells
  const largeText = fc
    .array(
      fc
        .array(fc.constantFrom("a", "b", "c", "d"), { minLength: 0, maxLength: 5 })
        .map((cs) => cs.join("")),
      { minLength: 2001, maxLength: 2001 },
    )
    .map((lines) => lines.join("\n"));

  // The two large-input properties override the global 10s fast-check
  // interrupt: under Stryker's instrumentation a single 2001x2001-line run
  // takes ~5s+, so 3 runs tripped interruptAfterTimeLimit and (via
  // markInterruptAsFailure) failed the mutation dry run. 60s keeps the
  // hang-safety bound without penalizing instrumented runs; the vitest
  // per-test cap in vitest.stryker.config.ts sits above it.
  it("Hirschberg path: invariants 1-3 hold for large inputs", () => {
    fc.assert(
      fc.property(largeText, largeText, (a, b) => {
        const d = lineDiff(a, b);
        const s = stats(d);
        expect(s.adds + s.ctx).toBe(countLines(b));
        expect(s.dels + s.ctx).toBe(countLines(a));
        expect(reconstructNew(d)).toBe(b);
      }),
      { numRuns: 3, interruptAfterTimeLimit: 60_000 },
    ); // fewer runs due to cost
  });

  it("Hirschberg path: lineDiff(a, a) produces only ctx entries", () => {
    fc.assert(
      fc.property(largeText, (a) => {
        const d = lineDiff(a, a);
        for (const l of d) {
          expect(l.kind).toBe("ctx");
        }
      }),
      { numRuns: 3, interruptAfterTimeLimit: 60_000 },
    );
  });
});
