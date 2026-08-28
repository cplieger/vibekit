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
  /** Callback for "Ask about this" button on diff hunks. Receives the
   *  hunk text (old + new lines). If not set, no button is shown. */
  onAskAbout?: (hunkText: string) => void;
  /** Callback for per-hunk accept. Receives hunk index and the new lines. */
  onAcceptHunk?: (hunkIndex: number, newLines: string[]) => void;
  /** Callback for per-hunk reject. Receives hunk index. */
  onRejectHunk?: (hunkIndex: number) => void;
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
}

/** Build a two-pane diff element. The caller appends it to the DOM. */
export function renderDiffPane(lines: DiffLine[], opts: DiffPaneOpts = {}): HTMLDivElement {
  const lineNumbers = opts.lineNumbers !== false;
  const syncScroll = opts.syncScroll !== false;
  const container = el("div", { className: "diff-pane" }) as HTMLDivElement;

  if (opts.oldLabel !== undefined || opts.newLabel !== undefined) {
    const header = el(
      "div",
      { className: "diff-pane-header" },
      el("span", { className: "diff-pane-label diff-pane-label-old" }, opts.oldLabel ?? ""),
      el("span", { className: "diff-pane-label diff-pane-label-new" }, opts.newLabel ?? ""),
    );
    if (opts.source !== undefined || opts.onToggleWhitespace !== undefined) {
      header.appendChild(buildWhitespaceToggle(container, opts));
    }
    container.appendChild(header);
  } else if (opts.source !== undefined || opts.onToggleWhitespace !== undefined) {
    // No labels, but callers still want the toggle. Build a minimal
    // header that only carries it.
    container.appendChild(
      el(
        "div",
        { className: "diff-pane-header diff-pane-header-toolbar" },
        buildWhitespaceToggle(container, opts),
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
  container.appendChild(el("div", { className: "diff-pane-body" }, leftCol, rightCol));

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

  // Per-hunk action buttons (accept/reject/ask).
  const hasHunkActions =
    opts.onAcceptHunk !== undefined ||
    opts.onRejectHunk !== undefined ||
    opts.onAskAbout !== undefined;
  if (hasHunkActions) {
    const hunks = identifyHunks(lines);
    for (const hunk of hunks) {
      const toolbar = el("div", { className: "diff-hunk-toolbar" });

      if (opts.onAcceptHunk !== undefined) {
        const acceptCb = opts.onAcceptHunk;
        const idx = hunk.index;
        const newLines = hunk.lines.filter((l) => l.kind === "add").map((l) => l.text);
        const btn = el(
          "button",
          { type: "button", className: "diff-hunk-btn accept" },
          "\u2713 Accept",
        ) as HTMLButtonElement;
        btn.addEventListener("click", () => {
          acceptCb(idx, newLines);
          btn.disabled = true;
          const sib = toolbar.querySelector<HTMLButtonElement>(".diff-hunk-btn.reject");
          if (sib) {
            sib.disabled = true;
          }
        });
        toolbar.appendChild(btn);
      }

      if (opts.onRejectHunk !== undefined) {
        const rejectCb = opts.onRejectHunk;
        const idx = hunk.index;
        const btn = el(
          "button",
          { type: "button", className: "diff-hunk-btn reject" },
          "\u2717 Reject",
        ) as HTMLButtonElement;
        btn.addEventListener("click", () => {
          rejectCb(idx);
          btn.disabled = true;
          const sib = toolbar.querySelector<HTMLButtonElement>(".diff-hunk-btn.accept");
          if (sib) {
            sib.disabled = true;
          }
        });
        toolbar.appendChild(btn);
      }

      if (opts.onAskAbout !== undefined) {
        const askCb = opts.onAskAbout;
        const hunkText = hunk.lines
          .filter((l) => l.kind === "add" || l.kind === "del")
          .map((l) => `${l.kind === "del" ? "-" : "+"}${l.text}`)
          .join("\n");
        if (hunkText !== "") {
          const btn = el("button", { type: "button", className: "diff-ask-btn" }, "Ask");
          btn.addEventListener("click", () => {
            askCb(hunkText);
          });
          toolbar.appendChild(btn);
        }
      }

      container.appendChild(toolbar);
    }
  }

  return container;
}

/** Append the "+N more lines" footer when rows were dropped, and return the
 *  pane. Shared by both shapes; the hunk toolbars are two-pane only (they belong
 *  to conflict resolution and the pending-diff editor, neither of which renders
 *  unified). */
function finishPane(
  container: HTMLDivElement,
  lines: DiffLine[],
  rowCount: number,
  _opts: DiffPaneOpts,
): HTMLDivElement {
  const extra = Math.max(0, lines.length - rowCount);
  if (extra > 0) {
    container.appendChild(
      el("div", { className: "diff-more" }, `+${String(extra)} more line${extra === 1 ? "" : "s"}`),
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
    row.appendChild(el("span", { className: "diff-gutter" }, no > 0 ? String(no) : ""));
  }
  const marker = line.kind === "add" ? "+" : line.kind === "del" ? "-" : " ";
  row.appendChild(
    el(
      "span",
      { className: "diff-content" },
      el("span", { className: "diff-marker" }, marker),
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

/** Identify contiguous hunks (groups of add/del lines separated by context). */
function identifyHunks(lines: DiffLine[]): { index: number; lines: DiffLine[] }[] {
  const hunks: { index: number; lines: DiffLine[] }[] = [];
  let current: DiffLine[] = [];
  let hunkIdx = 0;
  for (const line of lines) {
    if (line.kind === "add" || line.kind === "del") {
      current.push(line);
    } else if (current.length > 0) {
      hunks.push({ index: hunkIdx++, lines: current });
      current = [];
    }
  }
  if (current.length > 0) {
    hunks.push({ index: hunkIdx, lines: current });
  }
  return hunks;
}

/** Count the number of hunks in a diff. Useful for the pending-diff
 *  toolbar, which enables Apply-selected only once every hunk has
 *  been explicitly decided. Exported so callers don't have to
 *  duplicate the hunk-segmentation logic from identifyHunks — they
 *  share the same definition of what a hunk is. */
export function countHunks(lines: DiffLine[]): number {
  return identifyHunks(lines).length;
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
    row.appendChild(el("span", { className: "diff-gutter" }, lineNo > 0 ? String(lineNo) : ""));
  }
  // Marker glyph so colour-blind users still parse the row kind.
  row.appendChild(
    el(
      "span",
      { className: "diff-content" },
      el("span", { className: "diff-marker" }, kind === "add" ? "+" : kind === "del" ? "-" : " "),
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
    { className: "diff-pane-ws-toggle" },
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
      // Replace everything after the header (body + hunk toolbars +
      // "+N more" footer). Hunk toolbars are siblings of the body,
      // not children, so replacing only .diff-pane-body left stale
      // toolbar buttons referencing old hunk indices.
      const header = container.querySelector(".diff-pane-header");
      const insertionPoint = header !== null ? header.nextSibling : container.firstChild;
      while (
        insertionPoint !== null &&
        container.lastChild !== null &&
        container.lastChild !== header
      ) {
        container.removeChild(container.lastChild);
      }
      // Append all children from the re-rendered pane after its header.
      const freshHeader = rerendered.querySelector(".diff-pane-header");
      let node = freshHeader !== null ? freshHeader.nextSibling : rerendered.firstChild;
      while (node !== null) {
        const next = node.nextSibling;
        container.appendChild(node);
        node = next;
      }
    }
  });
  return wrap;
}
