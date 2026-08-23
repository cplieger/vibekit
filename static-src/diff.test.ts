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

  it("drops trailing context that adjoins no later hunk", () => {
    const lines = [del(1, "x"), ctx(2, "n1"), ctx(3, "n2"), ctx(4, "n3")];
    const got = windowHunks(lines, { context: 1 });
    // Only the side touching the hunk contributes, so the run's tail is not
    // kept: there is nothing after it for the reader to orient against.
    expect(got.lines).toEqual([del(1, "x"), ctx(2, "n1")]);
  });

  it("keeps a hunk that exactly fills the remaining budget", () => {
    const lines = [del(1, "a"), add(1, "b"), ctx(2, "k1"), ctx(3, "k2"), del(4, "c"), add(4, "d")];
    // 2 rows of hunk + 2 of context + 2 of hunk === maxRows, so the last hunk
    // fits precisely. Cutting at the cap rather than past it would drop it and
    // report a hunk omitted that the reader had room for.
    const got = windowHunks(lines, { maxRows: 6, context: 1 });
    expect(got).toEqual({ lines, hunksOmitted: 0 });
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

// ---------------------------------------------------------------------------
// Minimality, not just validity.
//
// The round-trip invariants above (adds+ctx === newText, and replaying the
// script reproduces it) are satisfied by ANY valid edit script, including
// "delete everything, add everything". They therefore say nothing about the
// edit-distance table that decides WHICH lines pair up. These cases pin the
// script itself on inputs small enough to work the optimum out by hand, which
// is what catches an off-by-one in the table: the diff stays valid and turns
// silently worse.
// ---------------------------------------------------------------------------
describe("lineDiff minimal edit scripts", () => {
  const cases: {
    name: string;
    old: string;
    new: string;
    opts?: { ignoreWhitespace?: boolean };
    expected: DiffLine[];
  }[] = [
    {
      // Both ends differ, so nothing is trimmed and the whole table is walked.
      // Reaching the A/B pair costs two adds first, so the walk has to prefer
      // an add over a del twice while the table still says the pair is coming.
      name: "adds precede a matching pair when the table says the pair is worth more",
      old: "X\nA\nB\nY",
      new: "Q\nC\nA\nB\nZ",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "X" },
        { kind: "add", oldNo: 0, newNo: 1, text: "Q" },
        { kind: "add", oldNo: 0, newNo: 2, text: "C" },
        { kind: "ctx", oldNo: 2, newNo: 3, text: "A" },
        { kind: "ctx", oldNo: 3, newNo: 4, text: "B" },
        { kind: "del", oldNo: 4, newNo: 0, text: "Y" },
        { kind: "add", oldNo: 0, newNo: 5, text: "Z" },
      ],
    },
    {
      // The first row of the table is the one an "i >= 0" loop bound is easiest
      // to lose. Here the correct move at the very first cell is an add, so a
      // missing first row shows up as a deleted A instead.
      name: "a leading insertion is found from the first row of the table",
      old: "A\nB\nz",
      new: "X\nA\nB\nw",
      expected: [
        { kind: "add", oldNo: 0, newNo: 1, text: "X" },
        { kind: "ctx", oldNo: 1, newNo: 2, text: "A" },
        { kind: "ctx", oldNo: 2, newNo: 3, text: "B" },
        { kind: "del", oldNo: 3, newNo: 0, text: "z" },
        { kind: "add", oldNo: 0, newNo: 4, text: "w" },
      ],
    },
    {
      // "D" appears twice in the new text: anchoring the old D on the second
      // one costs the B match. Both choices reconstruct the new text, only one
      // is the shortest script.
      name: "a repeated line is anchored where it keeps the most context",
      old: "E\nD\nB",
      new: "D\nC\nB\nD",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "E" },
        { kind: "ctx", oldNo: 2, newNo: 1, text: "D" },
        { kind: "add", oldNo: 0, newNo: 2, text: "C" },
        { kind: "ctx", oldNo: 3, newNo: 3, text: "B" },
        { kind: "add", oldNo: 0, newNo: 4, text: "D" },
      ],
    },
    {
      // The B/A pair is only reachable by skipping past the earlier A and C
      // copies; taking the first A instead leaves one context line, not two.
      name: "duplicated old lines do not cost context",
      old: "C\nA\nC\nB\nA\nD\nB",
      new: "E\nB\nA",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "C" },
        { kind: "del", oldNo: 2, newNo: 0, text: "A" },
        { kind: "del", oldNo: 3, newNo: 0, text: "C" },
        { kind: "add", oldNo: 0, newNo: 1, text: "E" },
        { kind: "ctx", oldNo: 4, newNo: 2, text: "B" },
        { kind: "ctx", oldNo: 5, newNo: 3, text: "A" },
        { kind: "del", oldNo: 6, newNo: 0, text: "D" },
        { kind: "del", oldNo: 7, newNo: 0, text: "B" },
      ],
    },
    {
      // Two trimmed suffix lines, so the suffix loop runs twice and its line
      // numbers have to climb with it.
      name: "a two-line common suffix keeps its own line numbers",
      old: "X\nc\nd",
      new: "Y\nc\nd",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "X" },
        { kind: "add", oldNo: 0, newNo: 1, text: "Y" },
        { kind: "ctx", oldNo: 2, newNo: 2, text: "c" },
        { kind: "ctx", oldNo: 3, newNo: 3, text: "d" },
      ],
    },
    {
      // The mirror of "whitespace-only diff with ignoreWhitespace" above: there
      // the OLD line carried the extra space, so normalizing only the old side
      // was enough to pass. Here the new side is the one that needs it.
      name: "ignoreWhitespace normalizes the new side too",
      old: "a",
      new: "  a",
      opts: { ignoreWhitespace: true },
      expected: [{ kind: "ctx", oldNo: 1, newNo: 1, text: "a" }],
    },
    {
      name: "ignoreWhitespace still separates a real content change",
      old: "a",
      new: "b",
      opts: { ignoreWhitespace: true },
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "a" },
        { kind: "add", oldNo: 0, newNo: 1, text: "b" },
      ],
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(lineDiff(tc.old, tc.new, tc.opts)).toEqual(tc.expected);
    });
  }
});

// ---------------------------------------------------------------------------
// The linear-space path, with an optimum that is known by CONSTRUCTION.
//
// Every case below builds the new text out of the old one by deleting lines and
// inserting lines that appear nowhere in the old text. A common subsequence can
// only use retained lines, so the longest one is exactly "old length minus
// deletions" — no second diff implementation is needed to know the answer, and
// the counts hold even when the body is full of repeated lines.
//
// Each pair is sized just past SPACE_THRESHOLD (4M cells) after the
// prefix/suffix trim, which is the only way to reach hirschbergDiff at all, and
// each keeps two shared lines at both ends so the trim is exercised and the
// recursion's line numbers are offset rather than starting at zero.
// ---------------------------------------------------------------------------
describe("lineDiff on the linear-space path", () => {
  const body = Array.from({ length: 2000 }, (_, i) => `L${String(i).padStart(4, "0")}`);
  const bodyNew = body
    .filter((l) => l !== "L0500")
    .flatMap((l) => (l === "L1500" ? [l, "NEW-LINE"] : [l]));
  const oldText = [
    "shared-1",
    "shared-2",
    "A-HEAD",
    ...body,
    "A-TAIL",
    "shared-3",
    "shared-4",
  ].join("\n");
  const newText = [
    "shared-1",
    "shared-2",
    "B-HEAD",
    ...bodyNew,
    "B-TAIL",
    "shared-3",
    "shared-4",
  ].join("\n");
  // Computed inside each test rather than in a beforeAll: code a hook runs
  // executes outside any test, and Stryker's perTest coverage then attributes
  // the mutants it reaches to no test at all — it reports them "Survived, ran
  // all tests" while running about one. Same fixture, four calls.
  const sparseDiff = (): DiffLine[] => lineDiff(oldText, newText);

  it("keeps every line the two files still share", () => {
    // 2000 body lines minus the one deletion, plus the 4 trimmed shared lines.
    expect(stats(sparseDiff())).toEqual({ adds: 3, dels: 3, ctx: 2003 });
  });

  it("numbers an interior deletion and insertion against their own files", () => {
    const sparse = sparseDiff();
    expect(sparse.find((l) => l.text === "L0500")).toEqual({
      kind: "del",
      oldNo: 504,
      newNo: 0,
      text: "L0500",
    });
    expect(sparse.find((l) => l.text === "NEW-LINE")).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 1504,
      text: "NEW-LINE",
    });
  });

  it("numbers a context line that sits at different rows in the two files", () => {
    // Past the deleted L0500 the two files are one line out of step, which is
    // the only place a single shared offset would look right and be wrong.
    expect(sparseDiff().find((l) => l.text === "L1500")).toEqual({
      kind: "ctx",
      oldNo: 1504,
      newNo: 1503,
      text: "L1500",
    });
  });

  it("numbers the trimmed prefix and suffix around the recursion", () => {
    const sparse = sparseDiff();
    expect(sparse[0]).toEqual({ kind: "ctx", oldNo: 1, newNo: 1, text: "shared-1" });
    expect(sparse.find((l) => l.text === "A-HEAD")).toEqual({
      kind: "del",
      oldNo: 3,
      newNo: 0,
      text: "A-HEAD",
    });
    expect(sparse.find((l) => l.text === "B-HEAD")).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 3,
      text: "B-HEAD",
    });
    expect(sparse.find((l) => l.text === "A-TAIL")).toEqual({
      kind: "del",
      oldNo: 2004,
      newNo: 0,
      text: "A-TAIL",
    });
    expect(sparse.find((l) => l.text === "B-TAIL")).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 2004,
      text: "B-TAIL",
    });
    expect(sparse[sparse.length - 1]).toEqual({
      kind: "ctx",
      oldNo: 2006,
      newNo: 2006,
      text: "shared-4",
    });
  });

  it("finds every match when a third of the lines changed", () => {
    // One change every three lines: 668 of 2004 body lines differ, so the
    // scratch rows are rewritten constantly instead of coasting along a long
    // diagonal of matches.
    const changedA = Array.from({ length: 2004 }, (_, i) =>
      i % 3 === 0 ? `OLD-${String(i)}` : `SAME-${String(i)}`,
    );
    const changedB = Array.from({ length: 2004 }, (_, i) =>
      i % 3 === 0 ? `NEW-${String(i)}` : `SAME-${String(i)}`,
    );
    const d = lineDiff(
      ["shared-1", "shared-2", ...changedA, "shared-3", "shared-4"].join("\n"),
      ["shared-1", "shared-2", ...changedB, "shared-3", "shared-4"].join("\n"),
    );
    expect(stats(d)).toEqual({ adds: 668, dels: 668, ctx: 1340 });
  });

  it("finds every match when the body is 40 copies of every line", () => {
    // 2000 lines drawn from 50 values, so almost every cell of the table has a
    // match to weigh against its neighbours. The three deletions and two
    // insertions are still counted exactly: FRESH-1/FRESH-2 appear nowhere in
    // the old text, so no alignment can be longer than 1997 body lines.
    const dup = Array.from({ length: 2000 }, (_, i) => `D-${String(i % 50)}`);
    const dropped = new Set([100, 700, 1300]);
    const dupNew = dup.flatMap((l, i) => {
      const kept = dropped.has(i) ? [] : [l];
      if (i === 400) {
        return [...kept, "FRESH-1"];
      }
      if (i === 1600) {
        return [...kept, "FRESH-2"];
      }
      return kept;
    });
    const d = lineDiff(
      ["shared-1", "shared-2", "A-HEAD", ...dup, "A-TAIL", "shared-3", "shared-4"].join("\n"),
      ["shared-1", "shared-2", "B-HEAD", ...dupNew, "B-TAIL", "shared-3", "shared-4"].join("\n"),
    );
    expect(stats(d)).toEqual({ adds: 4, dels: 5, ctx: 2001 });
    expect(d.find((l) => l.text === "FRESH-1")).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 404,
      text: "FRESH-1",
    });
  });

  it("reads the same edit distance whichever file is called old", () => {
    // Two unrelated bodies over the same 50 values: the alignment has thousands
    // of equally long candidates, so an exact count would only restate what the
    // implementation happens to pick. What cannot depend on the argument order
    // is the SIZE of the alignment — the longest common subsequence of two
    // files is symmetric — so swapping the arguments must swap adds and dels
    // and leave the context total alone. A scratch row that runs one line too
    // far is visible here and nowhere else: it biases the split toward whichever
    // file is walked first.
    const noise = (seed: number): string[] => {
      let state = seed;
      return Array.from({ length: 2002 }, () => {
        state = (state * 48271) % 2147483647;
        return `D-${String(state % 50)}`;
      });
    };
    const left = ["shared-1", "shared-2", "A-HEAD", ...noise(1), "A-TAIL", "shared-3"].join("\n");
    const right = ["shared-1", "shared-2", "B-HEAD", ...noise(7), "B-TAIL", "shared-3"].join("\n");
    const forward = stats(lineDiff(left, right));
    const backward = stats(lineDiff(right, left));
    expect(forward.ctx).toBe(backward.ctx);
    expect(forward.adds).toBe(backward.dels);
    expect(forward.dels).toBe(backward.adds);
    // Non-vacuous: this pair really does share and really does differ.
    expect(forward.ctx).toBeGreaterThan(100);
    expect(forward.adds).toBeGreaterThan(1000);
  });
});

// ---------------------------------------------------------------------------
// Past the time budget the module stops looking for context on purpose. The
// existing budget test above uses two files with nothing in common, where the
// coarse script and the minimal one are the same thing; these two use files
// that share 5000 lines, so going coarse is observable — and it must, because
// the alternative is an unresponsive tab.
// ---------------------------------------------------------------------------
describe("lineDiff past the time budget", () => {
  const shared = Array.from({ length: 5000 }, (_, i) => `M${String(i).padStart(4, "0")}`);
  // 5002x5002 = 25,020,004 cells after the trim, just past TIME_BUDGET_CELLS.
  const coarseDiff = (): DiffLine[] =>
    lineDiff(
      ["OLD-FIRST", ...shared, "OLD-LAST"].join("\n"),
      ["NEW-FIRST", ...shared, "NEW-LAST"].join("\n"),
    );

  it("gives up on context it could have found, rather than the main thread", () => {
    expect(stats(coarseDiff())).toEqual({ adds: 5002, dels: 5002, ctx: 0 });
  });

  it("still numbers the coarse script from the top of each file", () => {
    const coarse = coarseDiff();
    expect(coarse[0]).toEqual({ kind: "del", oldNo: 1, newNo: 0, text: "OLD-FIRST" });
    expect(coarse[5001]).toEqual({ kind: "del", oldNo: 5002, newNo: 0, text: "OLD-LAST" });
    expect(coarse[5002]).toEqual({ kind: "add", oldNo: 0, newNo: 1, text: "NEW-FIRST" });
    expect(coarse[coarse.length - 1]).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 5002,
      text: "NEW-LAST",
    });
  });
});

// ---------------------------------------------------------------------------
// The prefix and suffix trims walk toward each other over the SHORTER file, so
// their shared bound is min(a, b). With max, a repeated line at the join is
// counted twice — once as trimmed prefix and once as trimmed suffix — and the
// edit that is really there is reported as context: the reader is shown a line
// as unchanged when it was added, and one file's line numbers repeat.
// ---------------------------------------------------------------------------
describe("lineDiff prefix and suffix trimming", () => {
  it("does not count a repeated line as both prefix and suffix on an append", () => {
    expect(lineDiff("x\nx", "x\nx\nx")).toEqual([
      { kind: "ctx", oldNo: 1, newNo: 1, text: "x" },
      { kind: "ctx", oldNo: 2, newNo: 2, text: "x" },
      { kind: "add", oldNo: 0, newNo: 3, text: "x" },
    ]);
  });

  it("does not count a repeated line as both prefix and suffix on a truncation", () => {
    expect(lineDiff("x\nx\nx", "x\nx")).toEqual([
      { kind: "ctx", oldNo: 1, newNo: 1, text: "x" },
      { kind: "ctx", oldNo: 2, newNo: 2, text: "x" },
      { kind: "del", oldNo: 3, newNo: 0, text: "x" },
    ]);
  });
});

// ---------------------------------------------------------------------------
// The linear-space recursion bottoms out at ONE old line against a range of new
// ones, and that base case emits three groups in order: the new lines before the
// match, the match itself, and the new lines AFTER it. The fixtures above reach
// the base case two thousand times over but almost always with the match at the
// end of its range, so the third group is never emitted and its line numbering
// is never read. One insertion every fourth line puts a new line behind a
// matched one at the deepest level.
//
// 2001 old lines against 2502 new ones is 5.0M cells after the trim, past
// SPACE_THRESHOLD, so this is the recursion and not the dense table.
// ---------------------------------------------------------------------------
describe("lineDiff on the linear-space path, one old line against many new", () => {
  const oldLines = Array.from({ length: 2001 }, (_, i) => `L${String(i).padStart(4, "0")}`);
  const newLines = oldLines.flatMap((l, i) => (i % 4 === 0 ? [l, `INS-${String(i)}`] : [l]));
  const oldText = oldLines.join("\n");
  const newText = newLines.join("\n");
  // Computed per test rather than in a hook: a hook's work is attributed to no
  // test under perTest coverage (see the note on sparseDiff above).
  const scatteredDiff = (): DiffLine[] => lineDiff(oldText, newText);

  it("keeps every insertion, including the one behind the last match", () => {
    // 501 of the 2001 lines are followed by an insertion, nothing is deleted, and
    // every old line survives.
    expect(stats(scatteredDiff())).toEqual({ adds: 501, dels: 0, ctx: 2001 });
  });

  it("numbers an insertion that follows its matched line", () => {
    // The final insertion is the one the base case emits AFTER the match rather
    // than before it, so it is the only entry in the whole diff whose number
    // comes from that third group.
    expect(scatteredDiff().find((l) => l.text === "INS-2000")).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 2502,
      text: "INS-2000",
    });
  });

  it("gives every entry the line number of the line its text came from", () => {
    // The invariant behind every hand-checked number above, over a diff too long
    // to enumerate: an entry addresses its own file at its own number, and the
    // two files are walked strictly forward. A recursion that offsets one group
    // by a constant stays a valid edit script and lands the reader on the wrong
    // line.
    let lastOld = 0;
    let lastNew = 0;
    for (const l of scatteredDiff()) {
      if (l.kind === "add") {
        expect(l.oldNo).toBe(0);
      } else {
        expect(l.oldNo).toBeGreaterThan(lastOld);
        expect(oldLines[l.oldNo - 1]).toBe(l.text);
        lastOld = l.oldNo;
      }
      if (l.kind === "del") {
        expect(l.newNo).toBe(0);
      } else {
        expect(l.newNo).toBeGreaterThan(lastNew);
        expect(newLines[l.newNo - 1]).toBe(l.text);
        lastNew = l.newNo;
      }
    }
    expect([lastOld, lastNew]).toEqual([2001, 2502]);
  });
});

// ---------------------------------------------------------------------------
// The budget is a ceiling the input may reach: the guard is `>`, so an input of
// exactly TIME_BUDGET_CELLS still gets the exact algorithm. The describe above
// covers one cell past it; this covers the last input that is not past it, which
// is the only place the comparison itself is visible.
// ---------------------------------------------------------------------------
describe("lineDiff at the time budget", () => {
  it("still finds the shared body at exactly the budget", () => {
    // 5000x5000 = 25,000,000 cells after the trim, which trims nothing: both
    // files differ on their first and last line.
    const body = Array.from({ length: 4998 }, (_, i) => `B${String(i).padStart(4, "0")}`);
    const d = lineDiff(
      ["A-HEAD", ...body, "A-TAIL"].join("\n"),
      ["B-HEAD", ...body, "B-TAIL"].join("\n"),
    );
    expect(stats(d)).toEqual({ adds: 2, dels: 2, ctx: 4998 });
  });
});
