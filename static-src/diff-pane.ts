// ---------------------------------------------------------------------------
// Diff pane: renders a DiffLine[] in one of two shapes, from the same data.
//
//   two-pane (default) — old on the left, new on the right, scroll-synced.
//     For comparing two VERSIONS of a whole file: the git panel's changed-file
//     click and the editor's diff-vs-saved.
//   unified (`unified: true`) — one column. For reading a CHANGE, which is what
//     a chat transcript does.
//
// Unified is the cheaper path, not an extra one: DiffLine[] is already a flat
// unified array, and the two-pane form is derived from it by pushing every line
// into both columns and wiring scroll sync. So this is the same loop into one
// column with the sync dropped.
//
// Shared by chat inline previews, the editor's diff mode, and the conflict
// compare popup.
// ---------------------------------------------------------------------------

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
  /** Whether to synchronize scroll between the two panes. Default true.
   *  Turn off for the inline preview (no scrolling). Ignored when `unified`. */
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
   *  BOTH shapes.
   *
   *  It used to be unified-only, on the reasoning that highlighting one side of
   *  a deletion is misleading. That contradicted its own sibling: the unified
   *  shape highlights deletions deliberately, because "what did it replace my
   *  function with" is frequently the actual question, and a reader who clicked
   *  through from an inline preview landed on a flatter rendering than the peek
   *  that sent them. Both shapes highlight both sides now. */
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
  /** Draw the change map beside the columns. Default true for the two-pane
   *  shape, ignored for `unified` (whose column is not the vertical scroller,
   *  so there is no scroll position for a map to report). */
  changeMap?: boolean;
}

/** The rows `renderDiffPane` keeps across a whitespace re-diff: the toolbar and
 *  the label row. Everything else — the body, the "+N more" footer, the
 *  no-changes state — is derived from the diff and is rebuilt. */
const CHROME_ROWS = ".diff-pane-toolbar, .diff-pane-header";

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
  if (opts.oldLabel !== undefined || opts.newLabel !== undefined) {
    container.appendChild(
      el(
        "div",
        { className: "diff-pane-header" },
        el("span", { className: "diff-pane-label diff-pane-label-old" }, opts.oldLabel ?? ""),
        el("span", { className: "diff-pane-label diff-pane-label-new" }, opts.newLabel ?? ""),
      ),
    );
  }

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

  const leftCol = el("div", { className: "diff-col diff-col-old" }) as HTMLDivElement;
  const rightCol = el("div", { className: "diff-col diff-col-new" }) as HTMLDivElement;
  const body = el("div", { className: "diff-pane-body" }, leftCol, rightCol) as HTMLDivElement;
  container.appendChild(body);

  for (const line of lines) {
    if (rowCount >= limit) {
      break;
    }
    appendRow(leftCol, rightCol, line, lineNumbers, lang, marks.get(line));
    rowCount++;
  }
  finishPane(container, lines, rowCount, opts);

  if (syncScroll) {
    wireSyncScroll(leftCol, rightCol);
  }

  if (opts.changeMap !== false && rowCount > 0) {
    container.classList.add("diff-pane-mapped");
    const map = buildChangeMap(lines, rowCount);
    body.appendChild(map);
    wireChangeMap(map, leftCol, rightCol);
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

function wireSyncScroll(left: HTMLDivElement, right: HTMLDivElement): void {
  let locked = false;
  const sync = (src: HTMLDivElement, dst: HTMLDivElement) => (): void => {
    if (locked) {
      return;
    }
    locked = true;
    dst.scrollTop = src.scrollTop;
    dst.scrollLeft = src.scrollLeft;
    requestAnimationFrame(() => {
      locked = false;
    });
  };
  left.addEventListener("scroll", sync(left, right));
  right.addEventListener("scroll", sync(right, left));
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

/** One contiguous run of changed rows, as the map paints it. `mod` is a run
 *  holding both sides of a rewrite. */
interface ChangeRun {
  readonly start: number;
  readonly len: number;
  readonly kind: "add" | "del" | "mod";
}

/** Group the changed rows into runs. Row INDEX is the unit rather than a line
 *  number: every row is the same height (`white-space: pre`, so nothing wraps)
 *  and both columns hold one row per `DiffLine`, so an index maps linearly onto
 *  the scroller and one run set describes both sides. */
function changeRuns(lines: readonly DiffLine[], rowCount: number): ChangeRun[] {
  const runs: ChangeRun[] = [];
  const end = Math.min(lines.length, rowCount);
  let start = -1;
  let adds = 0;
  let dels = 0;
  const flush = (at: number): void => {
    if (start < 0) {
      return;
    }
    runs.push({
      start,
      len: at - start,
      kind: adds > 0 && dels > 0 ? "mod" : adds > 0 ? "add" : "del",
    });
    start = -1;
    adds = 0;
    dels = 0;
  };
  for (let i = 0; i < end; i++) {
    const kind = lines[i]?.kind;
    if (kind === "add" || kind === "del") {
      if (start < 0) {
        start = i;
      }
      if (kind === "add") {
        adds++;
      } else {
        dels++;
      }
      continue;
    }
    flush(i);
  }
  flush(end);
  return runs;
}

/** Build the map: one mark per run, plus the viewport box `wireChangeMap` drives.
 *  `aria-hidden` and not focusable, because the rows are the accessible statement
 *  of what changed and this is a pointer shortcut to a position they carry. The
 *  side-carries-kind rule is in `vibekit-ui.md` "Diff viewer". */
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
  map.appendChild(el("div", { className: "diff-map-view" }));
  return map;
}

/** Track the columns' scroll position in the viewport box, and let a press on
 *  the map move it. `left` is the scroller the map reads and writes; sync scroll
 *  carries the move to `right`, and both are listened to so a pane with sync off
 *  still reports whichever the reader scrolled. */
function wireChangeMap(map: HTMLDivElement, left: HTMLDivElement, right: HTMLDivElement): void {
  const view = map.querySelector<HTMLDivElement>(".diff-map-view");
  if (view === null) {
    return;
  }
  let queued = false;
  const paint = (): void => {
    if (queued) {
      return;
    }
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      const total = left.scrollHeight;
      if (total <= 0) {
        return;
      }
      const visible = Math.min(1, left.clientHeight / total);
      // Nothing scrolls, so a box spanning the whole track would claim a
      // position the reader cannot leave.
      view.style.display = visible >= 1 ? "none" : "";
      view.style.top = `${((left.scrollTop / total) * 100).toFixed(4)}%`;
      view.style.height = `${(visible * 100).toFixed(4)}%`;
    });
  };
  left.addEventListener("scroll", paint);
  right.addEventListener("scroll", paint);
  paint();

  const jumpTo = (clientY: number): void => {
    const box = map.getBoundingClientRect();
    if (box.height <= 0) {
      return;
    }
    const frac = Math.min(1, Math.max(0, (clientY - box.top) / box.height));
    // Centre the landing on the press: a reader aiming at a mark wants it in
    // view, not pinned to the top edge where its context above is cut off.
    left.scrollTop = Math.max(0, frac * left.scrollHeight - left.clientHeight / 2);
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
