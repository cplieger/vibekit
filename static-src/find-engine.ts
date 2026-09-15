// ---------------------------------------------------------------------------
// The DOM find engine: match discovery, <mark> highlighting and step state for
// one root, shared by the transcript's find and the editor's find over a diff
// pane or rendered markdown. A leaf with no app imports and no scroll, overlay
// or counter chrome, so the editor can reach it and a test can run it.
//
// A match is found in a RUN, the concatenated text between two block
// boundaries (BLOCK_TAGS), scanned by textsearch/scan.ts: a phrase crossing an
// inline element is one hit, painted as <mark> pieces sharing one `data-hit`,
// and `total` counts hits, not pieces or nodes.
//
// The walker's principle: we find text hits, we do not filter; it must be
// predictable. So a context line rendered in both diff columns is two hits, and
// chrome is pruned only by its producer's own mark (CHROME_SELECTOR).
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { occurrences, prepare } from "./textsearch/scan.js";
import type { Needle } from "./textsearch/scan.js";

const HIT_CLASS = "find-hit";
const CURRENT_CLASS = "find-hit-current";

const TEXT_NODE = 3;
const ELEMENT_NODE = 1;

/** The tags that end a run on entry and on exit. Text on either side of one of
 *  these renders on its own line, so a phrase never crosses it. */
const BLOCK_TAGS = new Set([
  "P",
  "DIV",
  "LI",
  "PRE",
  "TD",
  "TH",
  "H1",
  "H2",
  "H3",
  "H4",
  "H5",
  "H6",
  "BLOCKQUOTE",
  "SUMMARY",
  "DT",
  "DD",
  "BR",
]);

/** UI chrome the walker skips. Two producers are EXEMPT because the server
 *  searches the text they render: the denial block (`tool_denial` is the
 *  refused resource, rendered nowhere else) and the MCP badge (the server name
 *  is parsed out of the raw `tc.Title` the server searches as `tool_title`,
 *  while `.tool-title` shows only the tool half). Pruning either would count a
 *  hit in `N in chat` that no mark can land on. */
const CHROME_SELECTOR = "[data-vk-chrome]:not(.tool-denial):not(.tool-mcp-badge)";

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

/** True when `elem` (and thus its descendant text) should be searched. Prunes script and style,
 *  already-wrapped hits, UI chrome (see `CHROME_SELECTOR`), structurally-hidden subtrees (hidden
 *  attr, .hidden class, aria-hidden, closed <details>), the live-streaming bubble (its markdown
 *  writer owns those nodes) and, where `checkVisibility` exists, anything CSS hides — a boxless
 *  element excepted. */
function isSearchableElement(elem: Element): boolean {
  const tag = elem.tagName;
  if (tag === "SCRIPT" || tag === "STYLE" || tag === "MARK") {
    return false;
  }
  if (elem.hasAttribute("hidden") || elem.getAttribute("aria-hidden") === "true") {
    return false;
  }
  if (elem.classList.contains("hidden")) {
    return false;
  }
  // .streaming is set on the live assistant bubble AND live reasoning block.
  if (elem.classList.contains("streaming")) {
    return false;
  }
  if (elem.matches(CHROME_SELECTOR)) {
    return false;
  }
  if (tag === "DETAILS" && !(elem as HTMLDetailsElement).open) {
    return false;
  }
  const cv = (elem as { checkVisibility?: (opts?: unknown) => boolean }).checkVisibility;
  if (
    typeof cv === "function" &&
    !cv.call(elem, {
      contentVisibilityAuto: true,
      visibilityProperty: true,
      opacityProperty: false,
    })
  ) {
    return rendersWithoutBox(elem);
  }
  return true;
}

/** Whether `elem` has no box of its own while its children still render.
 *  `display: contents` is the one shape `checkVisibility` calls invisible that is
 *  not hidden, so the walker descends THROUGH it. Text directly under a boxless
 *  element that IS hidden therefore leaks; no shipped region puts text there. */
function rendersWithoutBox(elem: Element): boolean {
  return getComputedStyle(elem).display === "contents";
}

/** One slice of a text node that belongs to a hit. */
interface Piece {
  readonly from: number;
  readonly to: number;
  readonly hit: number;
}

/** Find the needle in one run and wrap what it covers. `hits` grows by one entry
 *  per occurrence, each holding that hit's mark pieces in document order. The
 *  offsets index the ORIGINAL text: `occurrences` reports them there, because the
 *  fold it compares under preserves length. */
function markRun(run: readonly Text[], needle: Needle, hits: HTMLElement[][]): void {
  const starts: number[] = [];
  let text = "";
  for (const node of run) {
    starts.push(text.length);
    text += node.nodeValue ?? "";
  }
  const found = occurrences(text, needle);
  if (found.length === 0) {
    return;
  }
  const pieces: Piece[][] = run.map(() => []);
  let first = 0;
  for (const at of found) {
    const hit = hits.length;
    hits.push([]);
    const end = at + needle.text.length;
    while (first + 1 < run.length && (starts[first + 1] ?? 0) <= at) {
      first++;
    }
    for (let i = first; i < run.length; i++) {
      const nodeStart = starts[i] ?? 0;
      if (nodeStart >= end) {
        break;
      }
      const nodeEnd = nodeStart + (run[i]?.length ?? 0);
      const from = Math.max(at, nodeStart) - nodeStart;
      const to = Math.min(end, nodeEnd) - nodeStart;
      if (to > from) {
        pieces[i]?.push({ from, to, hit });
      }
    }
  }
  for (let i = 0; i < run.length; i++) {
    const node = run[i];
    const nodePieces = pieces[i];
    if (node !== undefined && nodePieces !== undefined && nodePieces.length > 0) {
      splitNode(node, nodePieces, hits);
    }
  }
}

/** Replace `node` with its text around and between the pieces plus one `<mark>`
 *  per piece, preserving original casing. Only text nodes are touched — element
 *  nodes (and their listeners) are never disturbed. */
function splitNode(node: Text, pieces: readonly Piece[], hits: HTMLElement[][]): void {
  const text = node.nodeValue ?? "";
  const frag = document.createDocumentFragment();
  let last = 0;
  for (const p of pieces) {
    if (p.from > last) {
      frag.appendChild(document.createTextNode(text.slice(last, p.from)));
    }
    const mark = el(
      "mark",
      { className: HIT_CLASS, "data-hit": String(p.hit) },
      text.slice(p.from, p.to),
    );
    frag.appendChild(mark);
    hits[p.hit]?.push(mark);
    last = p.to;
  }
  if (last < text.length) {
    frag.appendChild(document.createTextNode(text.slice(last)));
  }
  node.parentNode?.replaceChild(frag, node);
}

/** Replace a `<mark>` with a plain text node of its content and merge adjacent
 *  text nodes so the DOM returns to its pre-highlight shape. */
function unwrapMark(mark: HTMLElement): void {
  const parent = mark.parentNode;
  if (parent === null) {
    return;
  }
  parent.replaceChild(document.createTextNode(mark.textContent), mark);
  parent.normalize();
}

// ---------------------------------------------------------------------------
// The engine
// ---------------------------------------------------------------------------

export class FindEngine {
  /** The element the walker scans. Public so a caller that re-roots per open
   *  (the transcript's find, whose root is the ACTIVE view) can tell whether
   *  its engine still points at the current root. */
  readonly root: HTMLElement;
  /** One entry per hit: its `<mark>` pieces in document order. */
  private hits: HTMLElement[][] = [];
  private current = -1;
  private lastQuery = "";

  constructor(root: HTMLElement) {
    this.root = root;
  }

  get total(): number {
    return this.hits.length;
  }

  get currentIndex(): number {
    return this.current;
  }

  get query(): string {
    return this.lastQuery;
  }

  /** Re-highlight `query` across the root. Clears any prior highlight first.
   *  Resets the current match to the first (index 0), or -1 when there are
   *  none. Returns the total hit count. */
  search(query: string, caseSensitive = false): number {
    this.clear();
    this.lastQuery = query;
    if (query === "") {
      return 0;
    }
    const needle = prepare(query, caseSensitive);
    const hits: HTMLElement[][] = [];
    for (const run of this.collectRuns()) {
      markRun(run, needle, hits);
    }
    this.hits = hits;
    this.current = hits.length > 0 ? 0 : -1;
    this.applyCurrentClass();
    return hits.length;
  }

  /** Remove all highlight marks and restore the original text nodes. */
  clear(): void {
    for (const pieces of this.hits) {
      for (const mark of pieces) {
        unwrapMark(mark);
      }
    }
    // Defensive sweep in case an external DOM change stranded marks we no
    // longer track (e.g. a reconcile pass replaced a message element).
    for (const mark of [...this.root.querySelectorAll<HTMLElement>(`mark.${HIT_CLASS}`)]) {
      unwrapMark(mark);
    }
    this.hits = [];
    this.current = -1;
    this.lastQuery = "";
  }

  next(): void {
    if (this.hits.length === 0) {
      return;
    }
    this.current = (this.current + 1) % this.hits.length;
    this.applyCurrentClass();
  }

  prev(): void {
    if (this.hits.length === 0) {
      return;
    }
    this.current = (this.current - 1 + this.hits.length) % this.hits.length;
    this.applyCurrentClass();
  }

  /** Best-effort restore of the current index (used after a live re-run so the
   *  highlight doesn't jump back to match 1 on every streamed chunk). Clamped
   *  to the valid range; no-op when out of range. */
  setCurrent(index: number): void {
    if (index < 0 || index >= this.hits.length) {
      return;
    }
    this.current = index;
    this.applyCurrentClass();
  }

  /** Drop the current hit while keeping every highlight: the cursor has moved to
   *  a list this engine does not hold. The editor's conflict mode steps one
   *  cursor through the overlay's marks and then the buffer's hits, and a hit
   *  here still styled current would be a second "you are here". */
  clearCurrent(): void {
    this.current = -1;
    this.applyCurrentClass();
  }

  /** The current hit's first piece, which is where a scroll lands. */
  currentMark(): HTMLElement | null {
    return this.hits[this.current]?.[0] ?? null;
  }

  private applyCurrentClass(): void {
    for (let i = 0; i < this.hits.length; i++) {
      for (const mark of this.hits[i] ?? []) {
        mark.classList.toggle(CURRENT_CLASS, i === this.current);
      }
    }
  }

  /** The searchable text nodes under the root, grouped into runs. A block tag
   *  ends the run whether or not its own subtree is searched, so the boundary
   *  depends on the markup alone. */
  private collectRuns(): Text[][] {
    const runs: Text[][] = [];
    let run: Text[] = [];
    const flush = (): void => {
      if (run.length > 0) {
        runs.push(run);
        run = [];
      }
    };
    const visit = (node: Node): void => {
      if (node.nodeType === TEXT_NODE) {
        if ((node.nodeValue ?? "").length > 0) {
          run.push(node as Text);
        }
        return;
      }
      if (node.nodeType !== ELEMENT_NODE) {
        return;
      }
      const elem = node as Element;
      const bounds = BLOCK_TAGS.has(elem.tagName);
      if (bounds) {
        flush();
      }
      if (isSearchableElement(elem)) {
        for (const child of elem.childNodes) {
          visit(child);
        }
      }
      if (bounds) {
        flush();
      }
    };
    for (const child of this.root.childNodes) {
      visit(child);
    }
    flush();
    return runs;
  }
}
