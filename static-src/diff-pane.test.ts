// Tests for diff-pane.ts: row windowing, the no-changes state, word marks and
// syntax highlighting.
import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";
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
        // A rewrite. The map breaks it into TWO marks, one per side, because
        // that is what the columns render; see "reads a rewrite as two marks".
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
    // No mark spans the whole track: a full-width band would be a kind the rows
    // never show, so every mark commits to one side.
    for (const mark of map!.querySelectorAll<HTMLElement>(".diff-map-mark")) {
      expect(mark.getBoundingClientRect().width).toBeCloseTo(half, 0);
    }
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

// Which element scrolls. The two axes fight: `overflow-x` on a column makes it a
// scroll container, and a scroll container contributes no content height to the
// body's `auto` grid row, so without an explicit one the column clips the file at
// one scrollport. Only the real cascade sees this.

describe("the diff pane's scrollers", () => {
  let css: HTMLStyleElement;
  let host: HTMLDivElement;

  beforeAll(() => {
    css = mountAppCSS();
    host = document.createElement("div");
    host.style.cssText = "position:fixed;inset:0;width:800px;height:300px;display:flex";
    document.body.appendChild(host);
  });
  afterAll(() => {
    css.remove();
    host.remove();
  });

  function tallPane(): HTMLDivElement {
    host.replaceChildren();
    const lines: DiffLine[] = [];
    for (let i = 0; i < 200; i++) {
      lines.push(i % 5 === 0 ? add(i + 1, `added ${i}`) : ctx(i + 1, i + 1, `context ${i}`));
    }
    const pane = renderDiffPane(lines, { oldLabel: "HEAD", newLabel: "working tree" });
    pane.style.flex = "1";
    host.appendChild(pane);
    return pane;
  }

  it("scrolls the BODY and clips neither column", () => {
    const pane = tallPane();
    const body = pane.querySelector<HTMLElement>(".diff-pane-split");
    expect(body).not.toBeNull();
    expect(body!.scrollHeight).toBeGreaterThan(body!.clientHeight + 2);
    for (const sel of [".diff-col-old", ".diff-col-new"]) {
      const col = pane.querySelector<HTMLElement>(sel);
      expect(col, sel).not.toBeNull();
      // As tall as its own rows, so the body's scroll range covers the file.
      expect(col!.scrollHeight, sel).toBeCloseTo(col!.clientHeight, -1);
    }
    const lastRow = pane.querySelector<HTMLElement>(".diff-col-new .diff-row:last-child");
    expect(body!.scrollHeight).toBeGreaterThanOrEqual(lastRow!.offsetTop);
  });

  it("gives the columns no vertical scrollbar of their own", () => {
    const pane = tallPane();
    for (const sel of [".diff-col-old", ".diff-col-new"]) {
      const col = pane.querySelector<HTMLElement>(sel);
      // Only the 1px column divider, never a reserved bar.
      expect(col!.offsetWidth - col!.clientWidth, sel).toBeLessThanOrEqual(1);
    }
  });

  it("makes each column a tab stop, so the diff is reachable by keyboard", () => {
    const pane = tallPane();
    expect([...pane.querySelectorAll("[tabindex='0']")].map((e) => e.className)).toEqual([
      "diff-col diff-col-old",
      "diff-col diff-col-new",
    ]);
  });

  // The horizontal bar. A column is as tall as the file, so its own bar sits below
  // every scroll position but the last; this one is a sibling of the scroller at
  // the scrollport's bottom edge.
  //
  // Every assertion here POLLS rather than waiting a fixed number of frames: the
  // bar is sized from a ResizeObserver and the columns follow it on a scroll
  // event, so a frame count is a guess that a loaded full-suite run loses.
  function settles(check: () => void): Promise<void> {
    return vi.waitFor(check, { timeout: 3000, interval: 20 });
  }

  function widePane(long: boolean): HTMLDivElement {
    host.replaceChildren();
    const wide = "x".repeat(long ? 400 : 4);
    const lines: DiffLine[] = [];
    for (let i = 0; i < 200; i++) {
      lines.push(i % 5 === 0 ? add(i + 1, wide) : ctx(i + 1, i + 1, wide));
    }
    const pane = renderDiffPane(lines, { oldLabel: "HEAD", newLabel: "working tree" });
    pane.style.flex = "1";
    host.appendChild(pane);
    return pane;
  }

  it("gives the bar the COLUMN's scroll range, not the bar's own width", async () => {
    const pane = widePane(true);
    const bar = pane.querySelector<HTMLElement>(".diff-pane-hbar")!;
    const col = pane.querySelector<HTMLElement>(".diff-col-old")!;
    // The bar spans both columns, so sizing its spacer to the content width would
    // leave it with no range at all.
    await settles(() => {
      const range = col.scrollWidth - col.clientWidth;
      expect(range).toBeGreaterThan(0);
      expect(bar.scrollWidth - bar.clientWidth).toBe(range);
    });
    expect(bar.classList.contains("is-idle")).toBe(false);
  });

  it("moves both columns when it is dragged, and follows a column", async () => {
    const pane = widePane(true);
    const bar = pane.querySelector<HTMLElement>(".diff-pane-hbar")!;
    const left = pane.querySelector<HTMLElement>(".diff-col-old")!;
    const right = pane.querySelector<HTMLElement>(".diff-col-new")!;
    await settles(() => {
      expect(bar.scrollWidth - bar.clientWidth).toBeGreaterThan(100);
    });
    bar.scrollLeft = 90;
    await settles(() => {
      expect(left.scrollLeft).toBe(90);
      expect(right.scrollLeft).toBe(90);
    });
    left.scrollLeft = 30;
    await settles(() => {
      expect(bar.scrollLeft).toBe(30);
      expect(right.scrollLeft).toBe(30);
    });
  });

  it("gives both columns one scroll range, even when only one side is wide", async () => {
    // Without the shared width a drag to the far end clamps at the narrower
    // side's maximum and snaps back.
    host.replaceChildren();
    const lines: DiffLine[] = [];
    for (let i = 0; i < 200; i++) {
      lines.push(i % 2 === 0 ? del(i + 1, "y".repeat(400)) : add(i + 1, "short"));
    }
    const pane = renderDiffPane(lines, {});
    pane.style.flex = "1";
    host.appendChild(pane);
    const range = (sel: string): number => {
      const col = pane.querySelector<HTMLElement>(sel)!;
      return col.scrollWidth - col.clientWidth;
    };
    await settles(() => {
      expect(range(".diff-col-old")).toBeGreaterThan(0);
      // Within the 1px column divider.
      expect(range(".diff-col-new")).toBeCloseTo(range(".diff-col-old"), -0.5);
    });
  });

  it("withdraws itself when no line overflows", async () => {
    const pane = widePane(false);
    const bar = pane.querySelector<HTMLElement>(".diff-pane-hbar")!;
    await settles(() => {
      expect(bar.classList.contains("is-idle")).toBe(true);
    });
    expect(getComputedStyle(bar).display).toBe("none");
  });

  it("keeps it out of the accessibility tree and out of the tab order", () => {
    const bar = widePane(true).querySelector<HTMLElement>(".diff-pane-hbar")!;
    // A pointer duplicate of scrolling the focusable columns already provide.
    expect(bar.getAttribute("aria-hidden")).toBe("true");
    expect(bar.hasAttribute("tabindex")).toBe(false);
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
    // FOUR marks, not three: the rewrite at 4-5 is a deletion above an addition,
    // which is what the columns render.
    expect(got).toHaveLength(4);
    expect(got.map((m) => m.cls)).toEqual([
      "diff-map-mark diff-map-mark-del",
      "diff-map-mark diff-map-mark-del",
      "diff-map-mark diff-map-mark-add",
      "diff-map-mark diff-map-mark-add",
    ]);
    // Row 1 of 10, one row tall.
    expect(got[0]!.top).toBe(10);
    expect(got[0]!.height).toBe(10);
    // The rewrite's deletion row and addition row are separate marks on opposite
    // sides, mirroring the two columns.
    expect(got[1]!.top).toBe(40);
    expect(got[1]!.height).toBe(10);
    expect(got[2]!.top).toBe(50);
    expect(got[2]!.height).toBe(10);
    expect(got[3]!.top).toBe(80);
  });

  it("breaks a run where the KIND changes, not only on a context row", () => {
    // A multi-line replacement is one contiguous block of changed rows, so
    // without the kind break it is a single mark naming both sides at once.
    const lines: DiffLine[] = [
      ctx(1, 1, "a"),
      del(2, "x"),
      del(3, "y"),
      add(2, "X"),
      ctx(4, 3, "b"),
    ];
    const got = marks(renderDiffPane(lines, {}));
    expect(got.map((m) => m.cls)).toEqual([
      "diff-map-mark diff-map-mark-del",
      "diff-map-mark diff-map-mark-add",
    ]);
    // Two deletion rows above one addition row, in row order.
    expect(got[0]!.top).toBe(20);
    expect(got[0]!.height).toBe(40);
    expect(got[1]!.top).toBe(60);
    expect(got[1]!.height).toBe(20);
  });

  it("draws no viewport box, because the scrollbar thumb is one", () => {
    const pane = renderDiffPane(MODIFIED, {});
    expect(pane.classList.contains("diff-pane-mapped")).toBe(true);
    expect(pane.querySelector(".diff-map")).not.toBeNull();
    expect(pane.querySelector(".diff-map-view")).toBeNull();
  });

  it("sits beside the scroller rather than inside it", () => {
    // A cell of the scroller's grid is as tall as the file; the map has to be as
    // tall as the scrollport.
    const pane = renderDiffPane(MODIFIED, {});
    const map = pane.querySelector<HTMLElement>(".diff-map");
    expect(map?.parentElement?.className).toBe("diff-pane-viewport");
    expect(map?.parentElement?.querySelector(".diff-pane-split")).not.toBeNull();
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
