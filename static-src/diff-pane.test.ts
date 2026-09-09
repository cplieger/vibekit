// Tests for diff-pane.ts: row windowing, the no-changes state, word marks and
// syntax highlighting.
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { renderDiffPane } from "./diff-pane.js";
import { lineDiff, type DiffLine } from "./diff.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

function ctx(oldNo: number, newNo: number, text = ""): DiffLine {
  return { kind: "ctx", oldNo, newNo, text };
}
function add(newNo: number, text = ""): DiffLine {
  return { kind: "add", oldNo: 0, newNo, text };
}
function del(oldNo: number, text = ""): DiffLine {
  return { kind: "del", oldNo, newNo: 0, text };
}

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

  it("keeps the chrome, so the whitespace toggle is still reachable", () => {
    // Ignoring whitespace can be what collapsed the diff to context in the
    // first place; without the toolbar there is no way to turn it back off.
    const pane = renderDiffPane([ctx(1, 1, "same")], {
      oldLabel: "HEAD",
      newLabel: "working tree",
      source: { oldText: "same", newText: "same" },
    });
    expect(pane.querySelector(".diff-pane-header")).not.toBeNull();
    expect(pane.querySelector(".diff-pane-ws-toggle")).not.toBeNull();
    expect(pane.querySelector(".diff-none")).not.toBeNull();
    // No rows, so nothing scrolls and there is no position to map.
    expect(pane.querySelector(".diff-map")).toBeNull();
  });

  it("renders rows as soon as one line differs", () => {
    const pane = renderDiffPane([ctx(1, 1, "same"), add(2, "new")], {});
    expect(pane.querySelector(".diff-none")).toBeNull();
    expect(pane.querySelector(".diff-pane-body")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// The reported symptom, pinned at the pane rather than at the split: a
// newline-terminated file used to render one added row too many, because
// `splitLines` kept the empty element the final newline produces and this pane
// draws a row per `DiffLine`. Measured on the live instance: 28 `.diff-row-add`
// against `git diff`'s 27, and 380 for a 379-line untracked file.
//
// This is the case that fails if the drop is ever reverted at the renderer's
// expense — and the pane is deliberately NOT where it could be fixed, since it
// holds `DiffLine[]` and cannot tell a genuine trailing empty line from the
// artifact ("a\n\n" yields two empty elements and only the second is one).
// ---------------------------------------------------------------------------

describe("renderDiffPane over a real lineDiff", () => {
  it("draws no phantom trailing row for a newline-terminated file", () => {
    const pane = renderDiffPane(lineDiff("", "a\nb\n"), { unified: true });
    expect(Array.from(pane.querySelectorAll(".diff-row-add"), (r) => r.textContent)).toHaveLength(
      2,
    );
    expect(pane.querySelectorAll(".diff-row")).toHaveLength(2);
    expect(
      Array.from(pane.querySelectorAll(".diff-row-add .diff-line-text"), (n) => n.textContent),
      "no empty-text row at the end",
    ).toEqual(["a", "b"]);
  });

  it("renders the same pane for a file that does not end in a newline", () => {
    const pane = renderDiffPane(lineDiff("", "a\nb"), { unified: true });
    expect(
      Array.from(pane.querySelectorAll(".diff-row-add .diff-line-text"), (n) => n.textContent),
    ).toEqual(["a", "b"]);
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

// ---------------------------------------------------------------------------
// The header's captions, and the reported symptom: "working tree" sat left of
// the working-tree column. The toggle used to share the labels' flex line, so
// both labels shrank around it while the BODY split at 50% regardless — the
// caption boundary and the column boundary were two different numbers. Measured
// against the real assembled cascade, because that is the only instrument that
// can tell an aligned caption from a nearly-aligned one.
// ---------------------------------------------------------------------------

describe("the diff pane's column captions", () => {
  let css: HTMLStyleElement;
  let host: HTMLDivElement;

  beforeAll(() => {
    css = mountAppCSS();
    host = document.createElement("div");
    // A width the pane can split, and a height its columns can scroll in.
    host.style.cssText = "position:fixed;inset:0;width:800px;height:300px";
    document.body.appendChild(host);
  });
  afterAll(() => {
    css.remove();
    host.remove();
  });

  function paneWithLabels(): HTMLDivElement {
    host.replaceChildren();
    const pane = renderDiffPane(MODIFIED, {
      oldLabel: "HEAD",
      newLabel: "working tree",
      source: { oldText: 'a := "one"\n', newText: 'a := "two"\n' },
    });
    host.appendChild(pane);
    return pane;
  }

  function leftEdge(sel: string, pane: HTMLDivElement): number {
    const el = pane.querySelector(sel);
    expect(el, sel).not.toBeNull();
    return (el as HTMLElement).getBoundingClientRect().left;
  }

  it("starts each caption's cell at its own column's left edge", () => {
    const pane = paneWithLabels();
    // The label carries its own `padding-inline`, so the CELL edge is the number
    // that has to agree with the column; compare the cell's box, not the text's.
    const oldCell = leftEdge(".diff-pane-label-old", pane);
    const newCell = leftEdge(".diff-pane-label-new", pane);
    expect(oldCell).toBeCloseTo(leftEdge(".diff-col-old", pane), 0);
    expect(newCell).toBeCloseTo(leftEdge(".diff-col-new", pane), 0);
  });

  it("keeps the whitespace toggle out of that row entirely", () => {
    const pane = paneWithLabels();
    expect(pane.querySelector(".diff-pane-header .diff-pane-ws-toggle")).toBeNull();
    expect(pane.querySelector(".diff-pane-toolbar .diff-pane-ws-toggle")).not.toBeNull();
  });

  it("gives each map mark a SIDE, so the strip is not colour alone", () => {
    // A deletion belongs to the left column and an addition to the right, so a
    // mark on that half says which side moved without reading its hue — the
    // channel a reader who cannot separate red from green still has.
    host.replaceChildren();
    const pane = renderDiffPane(
      [
        ctx(1, 1, "a"),
        del(2, "gone"),
        ctx(3, 2, "b"),
        // A rewrite: both sides in one run, so the mark spans both halves.
        del(4, "x"),
        add(3, "y"),
        ctx(5, 4, "c"),
        add(5, "z"),
      ],
      { oldLabel: "HEAD", newLabel: "working tree" },
    );
    host.appendChild(pane);
    const map = pane.querySelector<HTMLElement>(".diff-map");
    expect(map).not.toBeNull();
    // A mark's containing block is the map's PADDING box, so measure that
    // rather than the border box — the strip carries a left border.
    const outer = map!.getBoundingClientRect();
    const trackLeft = outer.left + map!.clientLeft;
    const trackWidth = map!.clientWidth;
    expect(trackWidth).toBeGreaterThan(6);
    const box = (sel: string): DOMRect => {
      const el = pane.querySelector<HTMLElement>(sel);
      expect(el, sel).not.toBeNull();
      return el!.getBoundingClientRect();
    };
    const half = trackWidth / 2;
    expect(box(".diff-map-mark-del").left).toBeCloseTo(trackLeft, 0);
    expect(box(".diff-map-mark-del").width).toBeCloseTo(half, 0);
    expect(box(".diff-map-mark-add").right).toBeCloseTo(trackLeft + trackWidth, 0);
    expect(box(".diff-map-mark-add").width).toBeCloseTo(half, 0);
    expect(box(".diff-map-mark-mod").width).toBeCloseTo(trackWidth, 0);
  });

  it("survives a whitespace re-render, control included", () => {
    // The toolbar is the pane's FIRST row, so the old "remove every sibling
    // after the header" swap would have deleted the checkbox mid-click.
    const pane = paneWithLabels();
    const box = pane.querySelector<HTMLInputElement>(".diff-pane-ws-toggle input");
    expect(box).not.toBeNull();
    box!.checked = true;
    box!.dispatchEvent(new Event("change"));
    expect(pane.querySelector(".diff-pane-ws-toggle")).not.toBeNull();
    expect(pane.querySelectorAll(".diff-pane-toolbar")).toHaveLength(1);
    expect(pane.querySelectorAll(".diff-pane-header")).toHaveLength(1);
    // Both sides only differ in the string, so ignoring whitespace changes
    // nothing and the rebuilt body is still a real diff rather than an empty box.
    expect(pane.querySelector(".diff-pane-body")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// The whitespace toggle's explanation. The label names the switch and cannot
// say what flipping it does, which is the question a reader has about it.
// ---------------------------------------------------------------------------

describe("the whitespace toggle's tooltip", () => {
  it("says what ignoring whitespace does to a line", () => {
    const pane = renderDiffPane(MODIFIED, {
      source: { oldText: "a", newText: "b" },
    });
    const tip = pane.querySelector(".diff-pane-ws-toggle")?.getAttribute("data-tooltip") ?? "";
    expect(tip).toContain("unchanged");
    expect(tip.length).toBeGreaterThan(20);
  });
});

// ---------------------------------------------------------------------------
// The change map: where the edits are, without scrolling the file to find out.
// One mark per contiguous run of changed rows, positioned as a percentage of the
// row count — every row is the same height, so an index maps linearly onto the
// scroller and no measurement is involved.
// ---------------------------------------------------------------------------

describe("the change map", () => {
  // Percentages come back through the CSSOM, which normalizes the text ("10%"
  // for "10.0000%"), so read them as numbers.
  function marks(pane: HTMLDivElement): { cls: string; top: number; height: number }[] {
    return [...pane.querySelectorAll<HTMLElement>(".diff-map-mark")].map((m) => ({
      cls: m.className,
      top: Number.parseFloat(m.style.top),
      height: Number.parseFloat(m.style.height),
    }));
  }

  it("puts one mark on each run of changed rows, in order", () => {
    // 10 rows: a deletion at 1, a rewrite at 4-5, an addition at 8.
    const lines: DiffLine[] = [
      ctx(1, 1, "a"),
      del(2, "gone"),
      ctx(3, 2, "b"),
      ctx(4, 3, "c"),
      del(5, "old"),
      add(4, "new"),
      ctx(6, 5, "d"),
      ctx(7, 6, "e"),
      add(7, "extra"),
      ctx(8, 8, "f"),
    ];
    const pane = renderDiffPane(lines, { oldLabel: "HEAD", newLabel: "working tree" });
    const got = marks(pane);
    expect(got).toHaveLength(3);
    expect(got.map((m) => m.cls)).toEqual([
      "diff-map-mark diff-map-mark-del",
      "diff-map-mark diff-map-mark-mod",
      "diff-map-mark diff-map-mark-add",
    ]);
    // Row 1 of 10, one row tall.
    expect(got[0]!.top).toBe(10);
    expect(got[0]!.height).toBe(10);
    // The rewrite is one run of two rows, not two runs of one.
    expect(got[1]!.top).toBe(40);
    expect(got[1]!.height).toBe(20);
    expect(got[2]!.top).toBe(80);
  });

  it("carries a viewport box", () => {
    const pane = renderDiffPane(MODIFIED, {});
    expect(pane.querySelector(".diff-map-view")).not.toBeNull();
    expect(pane.classList.contains("diff-pane-mapped")).toBe(true);
  });

  it("measures against the RENDERED rows when the diff is truncated", () => {
    // The map drives the scroller, and the scroller holds the rows that were
    // rendered — so a percentage over the whole diff would point past its end.
    const lines = [add(1, "one"), ctx(2, 2, "two"), add(3, "three"), add(4, "four")];
    const pane = renderDiffPane(lines, { maxRows: 2 });
    const got = marks(pane);
    expect(got).toHaveLength(1);
    expect(got[0]!.top).toBe(0);
    expect(got[0]!.height).toBe(50);
  });

  it("draws no map in the unified shape", () => {
    // That column is not the vertical scroller (the card around it is), so
    // there is no scroll position for a viewport box to report.
    const pane = renderDiffPane(MODIFIED, { unified: true });
    expect(pane.querySelector(".diff-map")).toBeNull();
    expect(pane.classList.contains("diff-pane-mapped")).toBe(false);
  });

  it("stays out of the accessibility tree", () => {
    // The rows themselves state what changed; this is a pointer shortcut to a
    // position they already carry, so it takes no tab stop and no hit target.
    const pane = renderDiffPane(MODIFIED, {});
    expect(pane.querySelector(".diff-map")?.getAttribute("aria-hidden")).toBe("true");
    expect(pane.querySelectorAll(".diff-map [tabindex], .diff-map button")).toHaveLength(0);
  });

  it("can be turned off", () => {
    const pane = renderDiffPane(MODIFIED, { changeMap: false });
    expect(pane.querySelector(".diff-map")).toBeNull();
  });
});
