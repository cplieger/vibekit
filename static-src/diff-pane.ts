// Diff pane: one `DiffLine[]` rendered as two columns (comparing two VERSIONS of
// a file) or as one (reading a CHANGE). Unified is the cheaper path rather than an
// extra one, because `DiffLine[]` is already a flat unified array.
//
// Consumers: chat inline previews, the editor's diff mode, the conflict popup.

import { lineDiff, wordMarks, type CharRange, type DiffLine } from "./diff.js";
import { highlightMarked, resolveLangHint } from "./highlight.js";
import { el } from "@cplieger/reactive";
import { CHROME_ATTR } from "./chrome-attr.js";

export interface DiffPaneOpts {
  /** Optional max rows. When set, rows beyond the limit are dropped and a
   *  "+N more" footer row is appended. Useful for the inline chat preview. */
  maxRows?: number;
  /** Label above the old (left) column. */
  oldLabel?: string;
  /** Label above the new (right) column. */
  newLabel?: string;
  /** Whether to show gutter line numbers. Default true. */
  lineNumbers?: boolean;
  /** Lock the two columns' HORIZONTAL scroll together. Default true, ignored when
   *  `unified`. Vertical is not on this switch: the body is the one scroller. */
  syncScroll?: boolean;
  /** Render ONE column instead of two. Default false.
   *
   *  Every row is a real code line in this mode, so `lang` can syntax-highlight
   *  them — which is what makes an inline diff read like the editor. The change
   *  signal stays on the row BACKGROUND plus the `+`/`-` marker, never on the
   *  text colour: text colour is the only channel highlighting has. */
  unified?: boolean;
  /** Language hint for syntax highlighting: a file PATH, a bare extension, or a
   *  highlighter language id (`resolveLangHint` accepts all three). Applies to
   *  BOTH shapes, deletions included: "what did it replace my function with" is
   *  frequently the question, so neither side is flattened. */
  lang?: string;
  /** Source texts. When supplied, the pane grows a "Ignore whitespace"
   *  toggle in the header that re-diffs and re-renders in place. If
   *  omitted, the toggle is hidden — callers that pre-computed their
   *  diff (e.g. the conflict compare popup) can still wire up a toggle
   *  themselves by supplying onToggleWhitespace. */
  source?: { oldText: string; newText: string };
  /** Fires whenever the whitespace toggle flips. Mutually exclusive
   *  with `source`: when `source` is set, the pane handles toggling
   *  internally and this callback is ignored. */
  onToggleWhitespace?: (ignoreWhitespace: boolean) => void;
  /** Draw the change map beside the columns. Default true for two-pane, ignored
   *  for `unified`: a mark's SIDE carries its kind and needs two columns. */
  changeMap?: boolean;
}

/** What survives a whitespace re-diff. The label row does not: in the two-pane
 *  shape it is a row of the body's own grid. */
const CHROME_ROWS = ".diff-pane-toolbar";

/** Build a two-pane diff element. The caller appends it to the DOM. */
export function renderDiffPane(lines: DiffLine[], opts: DiffPaneOpts = {}): HTMLDivElement {
  const lineNumbers = opts.lineNumbers !== false;
  const syncScroll = opts.syncScroll !== false;
  const container = el("div", { className: "diff-pane" }) as HTMLDivElement;

  // The toggle is a toolbar control, not a caption: sharing the label row made
  // both labels flex-shrink around it, so a caption stopped sitting over the
  // column it names. Measurement in `vibekit-ui.md` "Diff viewer".
  if (opts.source !== undefined || opts.onToggleWhitespace !== undefined) {
    container.appendChild(
      el("div", { className: "diff-pane-toolbar" }, buildWhitespaceToggle(container, opts)),
    );
  }
  // Appended late: the two-pane shape puts it inside the body, so a caption's
  // cell and its column are one grid track and cannot drift.
  const header =
    opts.oldLabel !== undefined || opts.newLabel !== undefined
      ? (el(
          "div",
          { className: "diff-pane-header" },
          el("span", { className: "diff-pane-label diff-pane-label-old" }, opts.oldLabel ?? ""),
          el("span", { className: "diff-pane-label diff-pane-label-new" }, opts.newLabel ?? ""),
        ) as HTMLDivElement)
      : null;

  const unified = opts.unified === true;
  const limit = opts.maxRows ?? Number.POSITIVE_INFINITY;
  const lang = opts.lang !== undefined && opts.lang !== "" ? resolveLangHint(opts.lang) : "";
  let rowCount = 0;

  // An all-context diff is not an empty diff, and rendering it as two identical
  // file listings says nothing — the reader sees a wall of unmarked code and
  // reads it as broken markup. This is reachable on the ordinary path: a chat's
  // changed-file link diffs HEAD against the working tree, so once the write is
  // committed the two agree.
  if (!lines.some((l) => l.kind !== "ctx")) {
    if (header !== null) {
      container.appendChild(header);
    }
    container.appendChild(
      el(
        "div",
        { className: "diff-none" },
        lines.length === 0 ? "Empty file" : "No changes between these versions",
      ),
    );
    return container;
  }

  // Word-level marks pair each modified line with its counterpart, so a
  // one-character edit reads as a one-character edit rather than as two whole
  // changed lines. Computed once for the whole diff, before any windowing.
  const marks = wordMarks(lines);

  if (unified) {
    container.classList.add("diff-pane-unified");
    if (header !== null) {
      container.appendChild(header);
    }
    const col = el("div", { className: "diff-col diff-col-unified" }) as HTMLDivElement;
    container.appendChild(el("div", { className: "diff-pane-body" }, col));
    for (const line of lines) {
      if (rowCount >= limit) {
        break;
      }
      col.appendChild(makeUnifiedRow(line, lineNumbers, lang, marks.get(line)));
      rowCount++;
    }
    return finishPane(container, lines, rowCount, opts);
  }

  // The body is the one vertical scroller and the columns are cells of its grid,
  // so the two sides cannot shear. Reasoning: `vibekit-ui.md` "Diff viewer".
  //
  // A column is a tab stop because it is a scroll container: arrows take its own
  // axis and bubble to the body for the other, so one stop reaches both and
  // neither region is keyboard-unreachable (WCAG 2.1.1).
  const colAttrs = (side: string): Record<string, string> => ({
    className: `diff-col diff-col-${side}`,
    tabindex: "0",
  });
  const leftCol = el("div", colAttrs("old")) as HTMLDivElement;
  const rightCol = el("div", colAttrs("new")) as HTMLDivElement;
  const body = el("div", { className: "diff-pane-body diff-pane-split" }) as HTMLDivElement;
  if (header !== null) {
    body.appendChild(header);
  }
  body.appendChild(leftCol);
  body.appendChild(rightCol);

  // The map and the horizontal bar are the scroller's SIBLINGS: a cell of its grid
  // is as tall as the file, and both have to sit at the scrollport's edge.
  const viewport = el("div", { className: "diff-pane-viewport" }, body) as HTMLDivElement;
  container.appendChild(viewport);

  for (const line of lines) {
    if (rowCount >= limit) {
      break;
    }
    appendRow(leftCol, rightCol, line, lineNumbers, lang, marks.get(line));
    rowCount++;
  }
  finishPane(container, lines, rowCount, opts);

  if (syncScroll) {
    wireHorizontalScroll(viewport, leftCol, rightCol);
  }

  if (opts.changeMap !== false && rowCount > 0) {
    container.classList.add("diff-pane-mapped");
    const map = buildChangeMap(lines, rowCount);
    viewport.appendChild(map);
    wireChangeMap(map, body);
  }

  return container;
}

/** Append the "+N more lines" footer when rows were dropped, and return the
 *  pane. Shared by both shapes. */
function finishPane(
  container: HTMLDivElement,
  lines: DiffLine[],
  rowCount: number,
  _opts: DiffPaneOpts,
): HTMLDivElement {
  const extra = Math.max(0, lines.length - rowCount);
  if (extra > 0) {
    container.appendChild(
      el(
        "div",
        { className: "diff-more", [CHROME_ATTR]: "" },
        `+${String(extra)} more line${extra === 1 ? "" : "s"}`,
      ),
    );
  }
  return container;
}

/** One unified row: gutter, marker, then the line itself.
 *
 *  The line is syntax-highlighted when a `lang` is known, because in this shape
 *  every row IS a code line. Deleted lines are highlighted too — "what did it
 *  replace my function with" is frequently the actual question, so they stay
 *  fully legible rather than being dimmed to a strikethrough. */
function makeUnifiedRow(
  line: DiffLine,
  lineNumbers: boolean,
  lang: string,
  marks?: readonly CharRange[],
): HTMLDivElement {
  const row = el("div", { className: `diff-row diff-row-${line.kind}` }) as HTMLDivElement;
  if (lineNumbers) {
    // The NEW number where there is one, else the old: a unified row belongs to
    // the post-change file except for deletions, which only exist in the pre.
    const no = line.kind === "del" ? line.oldNo : line.newNo;
    row.appendChild(
      el("span", { className: "diff-gutter", [CHROME_ATTR]: "" }, no > 0 ? String(no) : ""),
    );
  }
  const marker = line.kind === "add" ? "+" : line.kind === "del" ? "-" : " ";
  row.appendChild(
    el(
      "span",
      { className: "diff-content" },
      el("span", { className: "diff-marker", [CHROME_ATTR]: "" }, marker),
      lineText(line, lang, marks),
    ),
  );
  return row;
}

/** The code half of a row: syntax-highlighted, with the word-level changes
 *  marked. Shared by both shapes so a click through from the inline preview
 *  cannot land on a plainer rendering than the peek that sent the reader. */
function lineText(
  line: DiffLine,
  lang: string,
  marks: readonly CharRange[] | undefined,
): HTMLSpanElement {
  const text = el("span", { className: "diff-line-text" });
  const spans = marks ?? [];
  if (lang === "" && spans.length === 0) {
    text.textContent = line.text;
    return text;
  }
  const wordClass = line.kind === "del" ? "diff-word-del" : "diff-word-add";
  text.innerHTML = highlightMarked(line.text, lang, spans, wordClass);
  return text;
}

function appendRow(
  leftCol: HTMLDivElement,
  rightCol: HTMLDivElement,
  line: DiffLine,
  lineNumbers: boolean,
  lang: string,
  marks?: readonly CharRange[],
): void {
  // Each row occupies the same vertical slot on both sides, even if one
  // side is empty — that keeps scroll-sync correct.
  const [leftRow, rightRow] = makeRowPair(line, lineNumbers, lang, marks);
  leftCol.appendChild(leftRow);
  rightCol.appendChild(rightRow);
}

function makeRowPair(
  line: DiffLine,
  lineNumbers: boolean,
  lang: string,
  marks?: readonly CharRange[],
): [HTMLDivElement, HTMLDivElement] {
  const left = el("div", { className: "diff-row" }) as HTMLDivElement;
  const right = el("div", { className: "diff-row" }) as HTMLDivElement;

  if (line.kind === "ctx") {
    populateRow(left, line.oldNo, "ctx", lineNumbers, lineText(line, lang, undefined));
    populateRow(right, line.newNo, "ctx", lineNumbers, lineText(line, lang, undefined));
  } else if (line.kind === "del") {
    populateRow(left, line.oldNo, "del", lineNumbers, lineText(line, lang, marks));
    populateRow(right, 0, "empty", lineNumbers, null);
  } else {
    populateRow(left, 0, "empty", lineNumbers, null);
    populateRow(right, line.newNo, "add", lineNumbers, lineText(line, lang, marks));
  }
  return [left, right];
}

function populateRow(
  row: HTMLDivElement,
  lineNo: number,
  kind: "add" | "del" | "ctx" | "empty",
  lineNumbers: boolean,
  text: HTMLSpanElement | null,
): void {
  row.classList.add(`diff-row-${kind}`);
  if (lineNumbers) {
    row.appendChild(
      el("span", { className: "diff-gutter", [CHROME_ATTR]: "" }, lineNo > 0 ? String(lineNo) : ""),
    );
  }
  // Marker glyph so colour-blind users still parse the row kind.
  row.appendChild(
    el(
      "span",
      { className: "diff-content" },
      el(
        "span",
        { className: "diff-marker", [CHROME_ATTR]: "" },
        kind === "add" ? "+" : kind === "del" ? "-" : " ",
      ),
      text ?? el("span", { className: "diff-line-text" }),
    ),
  );
}

/** Give the columns one shared horizontal scrollbar at the bottom of the
 *  SCROLLPORT: a column is as tall as the file, so its own bar would sit below
 *  every scroll position but the last. Contract, and the two widths that make it
 *  correct: `vibekit-ui.md` "Diff viewer". */
function wireHorizontalScroll(
  viewport: HTMLDivElement,
  left: HTMLDivElement,
  right: HTMLDivElement,
): void {
  const spacer = el("div", { className: "diff-pane-hbar-spacer" }) as HTMLDivElement;
  // A pointer duplicate of scrolling the focusable columns already provide.
  const bar = el(
    "div",
    { className: "diff-pane-hbar", "aria-hidden": "true" },
    spacer,
  ) as HTMLDivElement;
  viewport.appendChild(bar);

  // Guarded on the values differing, so a write's own scroll event writes nothing
  // and no lock has to be held across a frame.
  const drive =
    (from: HTMLElement, ...targets: HTMLElement[]) =>
    (): void => {
      for (const to of targets) {
        if (to.scrollLeft !== from.scrollLeft) {
          to.scrollLeft = from.scrollLeft;
        }
      }
    };
  bar.addEventListener("scroll", drive(bar, left, right));
  left.addEventListener("scroll", drive(left, right, bar));
  right.addEventListener("scroll", drive(right, left, bar));

  const measure = (): void => {
    const span = Math.max(left.scrollWidth, right.scrollWidth);
    const range = span - left.clientWidth;
    // A track with no thumb is a control that does nothing.
    bar.classList.toggle("is-idle", range <= 1);
    // The bar spans BOTH columns while the range is one column's, so the spacer
    // buys it that RANGE rather than that width.
    spacer.style.inlineSize = `${String(bar.clientWidth + Math.max(0, range))}px`;
    viewport.style.setProperty("--diff-hspan", `${String(span)}px`);
  };
  // Fires once on observe, which is the first real measurement: the pane is
  // detached while it is built, so every width reads 0 until the caller appends it.
  new ResizeObserver(measure).observe(left);
}

// --- Whitespace toggle ---

/** Build the "Ignore whitespace" checkbox. When `opts.source` is
 *  supplied, toggling re-diffs and re-renders the pane in place
 *  without the caller needing to participate; the pane becomes
 *  self-contained for the common case. */
function buildWhitespaceToggle(container: HTMLDivElement, opts: DiffPaneOpts): HTMLLabelElement {
  const input = el("input", { type: "checkbox" }) as HTMLInputElement;
  const wrap = el(
    "label",
    {
      className: "diff-pane-ws-toggle",
      // The label names the switch; this says what flipping it does, which
      // "whitespace" alone cannot — a re-indented block reads as unchanged.
      "data-tooltip": "Treat a line that differs only in spacing or indentation as unchanged",
    },
    input,
    el("span", {}, "Ignore whitespace"),
  ) as HTMLLabelElement;
  input.addEventListener("change", () => {
    const ignore = input.checked;
    if (opts.source === undefined && opts.onToggleWhitespace !== undefined) {
      opts.onToggleWhitespace(ignore);
    }
    if (opts.source !== undefined) {
      // Re-diff and re-render in place. Strip the source from the
      // cloned opts so the re-rendered pane doesn't attach a second
      // whitespace toggle to its header (we keep the outer header).
      const source = opts.source;
      const { source: _, ...freshOpts } = opts;
      const freshDiffOpts: DiffPaneOpts = freshOpts;
      const fresh = lineDiff(source.oldText, source.newText, { ignoreWhitespace: ignore });
      const rerendered = renderDiffPane(fresh, freshDiffOpts);
      // Swap the DERIVED rows and keep the chrome, identified by what it is
      // rather than by position: the toolbar this checkbox lives in is the
      // pane's FIRST row, so "everything after the header" would delete it.
      for (const child of [...container.children]) {
        if (!child.matches(CHROME_ROWS)) {
          child.remove();
        }
      }
      container.classList.toggle(
        "diff-pane-mapped",
        rerendered.classList.contains("diff-pane-mapped"),
      );
      for (const child of [...rerendered.children]) {
        if (!child.matches(CHROME_ROWS)) {
          container.appendChild(child);
        }
      }
    }
  });
  return wrap;
}

// --- Change map ---

/** One contiguous run of changed rows of a single kind. */
interface ChangeRun {
  readonly start: number;
  readonly len: number;
  readonly kind: "add" | "del";
}

/** Group the changed rows into runs, breaking on a KIND change as well as on a
 *  context row: the map may name only kinds the rows show. Row INDEX is the unit
 *  because every row is the same height. */
function changeRuns(lines: readonly DiffLine[], rowCount: number): ChangeRun[] {
  const runs: ChangeRun[] = [];
  const end = Math.min(lines.length, rowCount);
  let start = -1;
  let open: "add" | "del" | null = null;
  const flush = (at: number): void => {
    if (start >= 0 && open !== null) {
      runs.push({ start, len: at - start, kind: open });
    }
    start = -1;
    open = null;
  };
  for (let i = 0; i < end; i++) {
    const kind = lines[i]?.kind;
    if (kind === "add" || kind === "del") {
      if (kind !== open) {
        flush(i);
        start = i;
        open = kind;
      }
      continue;
    }
    flush(i);
  }
  flush(end);
  return runs;
}

/** Build the map: one mark per run. `aria-hidden` and not focusable, because the
 *  rows are the accessible statement of what changed and this is a pointer
 *  shortcut to a position they carry. Contract: `vibekit-ui.md` "Diff viewer". */
function buildChangeMap(lines: readonly DiffLine[], rowCount: number): HTMLDivElement {
  const map = el("div", {
    className: "diff-map",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  const pct = (rows: number): string => `${((rows / rowCount) * 100).toFixed(4)}%`;
  for (const run of changeRuns(lines, rowCount)) {
    const mark = el("div", {
      className: `diff-map-mark diff-map-mark-${run.kind}`,
    }) as HTMLDivElement;
    mark.style.top = pct(run.start);
    mark.style.height = pct(run.len);
    map.appendChild(mark);
  }
  return map;
}

/** Let a press or drag on the map scroll the body. The map REPORTS nothing:
 *  `body`'s own scrollbar thumb is where the reader is. */
function wireChangeMap(map: HTMLDivElement, body: HTMLDivElement): void {
  const jumpTo = (clientY: number): void => {
    const box = map.getBoundingClientRect();
    if (box.height <= 0) {
      return;
    }
    const frac = Math.min(1, Math.max(0, (clientY - box.top) / box.height));
    // Centre the landing on the press: a reader aiming at a mark wants it in
    // view, not pinned to the top edge where its context above is cut off.
    body.scrollTop = Math.max(0, frac * body.scrollHeight - body.clientHeight / 2);
  };
  map.addEventListener("pointerdown", (e: PointerEvent) => {
    map.setPointerCapture(e.pointerId);
    jumpTo(e.clientY);
  });
  map.addEventListener("pointermove", (e: PointerEvent) => {
    if (map.hasPointerCapture(e.pointerId)) {
      jumpTo(e.clientY);
    }
  });
}
