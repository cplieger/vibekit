// ---------------------------------------------------------------------------
// The DOM find engine: match discovery, <mark> highlighting, and step state for
// one root element.
//
// SHARED, and that is why it is its own module. The transcript's find owns a
// position in a rendered conversation; the editor's find over a DIFF PANE or over
// RENDERED MARKDOWN owns a position in rendered prose. Both are the same problem
// — walk text nodes, wrap the hits, step between them, scroll one into view —
// and the editor's other mode is a different one: over source it maps a match to
// a LINE NUMBER, because `editor-scroll.ts` can place a line and the gutter can
// flash it.
//
// It used to live inside find-in-chat.ts, which made it unreachable: that module
// imports scroll.ts's self-initialising singleton and `$.messages`, so anything
// importing it for the engine dragged the chat transcript's DOM in behind it. A
// LEAF with no app imports is what lets the editor use it.
//
// The mark classes are `find-hit` / `find-hit-current`, not the `chat-` prefixed
// pair they were: one highlight vocabulary for every surface that highlights, so
// a reader sees the same colour mean the same thing in a transcript and in a diff.
//
// DOM-only (no scroll, no overlay, no counter chrome) so it is unit-testable
// where the API is absent.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";

const HIT_CLASS = "find-hit";
const CURRENT_CLASS = "find-hit-current";

const TEXT_NODE = 3;
const ELEMENT_NODE = 1;

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

/**
 * Format the match counter. 1-based for humans; "" when the query is empty.
 *
 * `total` is what the caller could MARK — for the transcript that is the DOM
 * pass, which prunes every hidden subtree (see `isSearchableElement`).
 * `sessionTotal` is what a session-wide enumerator found, 0 when there is none
 * (the editor's find has no second opinion and passes nothing).
 *
 * The two are reported side by side rather than subtracted. A difference has
 * several causes at once — hits inside a collapsed delegate card, hits on a page
 * that is not resident, and the server matching raw markdown where the DOM holds
 * rendered text — so "${n} hidden" would name a cause this function cannot know,
 * and the subtraction can go either way (the live streaming turn is in the DOM
 * and not yet in the chat file). Two independent true statements beat one
 * inferred one.
 *
 * The `total === 0` branch is the case that was an outright lie: "No matches"
 * while the server had answered that the text occurs N times.
 */
export function formatCount(
  total: number,
  current: number,
  query: string,
  sessionTotal = 0,
): string {
  if (query === "") {
    return "";
  }
  if (total === 0) {
    // Never "No matches" when something DID match somewhere the walker could not
    // reach. No "1 of" prefix either: nothing here is navigable, so an index
    // would point at nothing.
    return sessionTotal > 0 ? `${sessionTotal} in chat` : "No matches";
  }
  const here = `${current + 1} of ${total}`;
  // Only when it genuinely adds something. Equal counts are the common case and
  // a redundant second figure would train the reader to ignore it.
  return sessionTotal > total ? `${here} · ${sessionTotal} in chat` : here;
}

/** True when `elem` (and thus its descendant text) should be searched. Prunes
 *  script/style, already-wrapped hits, structurally-hidden subtrees (hidden
 *  attr, .hidden class, aria-hidden, closed <details>), the live-streaming
 *  bubble (its markdown writer owns those nodes), and — in a real browser —
 *  anything hidden by CSS via Element.checkVisibility(). The structural checks
 *  work where `checkVisibility` is absent (it is optional and skipped there). */
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
  if (tag === "DETAILS" && !(elem as HTMLDetailsElement).open) {
    return false;
  }
  const cv = (elem as { checkVisibility?: (opts?: unknown) => boolean }).checkVisibility;
  if (typeof cv === "function") {
    return cv.call(elem, {
      contentVisibilityAuto: true,
      visibilityProperty: true,
      opacityProperty: false,
    });
  }
  return true;
}

/** Wrap each occurrence of `needle` (length `needleLen`) in `node` with a
 *  `<mark>`, preserving original casing. Appends created marks to `out`. Only
 *  text nodes are touched — element nodes (and their listeners) are never
 *  disturbed.
 *
 *  `needle` arrives already folded when the search is case-INSENSITIVE, so the
 *  haystack is folded to match; a case-SENSITIVE search compares both verbatim.
 *  `needleLen` is passed separately because the slice below has to come out of
 *  the ORIGINAL text either way. */
function wrapMatchesInNode(
  node: Text,
  needleLen: number,
  needle: string,
  caseSensitive: boolean,
  out: HTMLElement[],
): void {
  const text = node.nodeValue ?? "";
  if (text === "") {
    return;
  }
  const hay = caseSensitive ? text : text.toLowerCase();
  let idx = hay.indexOf(needle);
  if (idx < 0) {
    return;
  }
  const frag = document.createDocumentFragment();
  let last = 0;
  while (idx >= 0) {
    if (idx > last) {
      frag.appendChild(document.createTextNode(text.slice(last, idx)));
    }
    const hit = el("mark", { className: HIT_CLASS }, text.slice(idx, idx + needleLen));
    frag.appendChild(hit);
    out.push(hit);
    last = idx + needleLen;
    idx = hay.indexOf(needle, last);
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
  private readonly root: HTMLElement;
  private marks: HTMLElement[] = [];
  private current = -1;
  private lastQuery = "";

  constructor(root: HTMLElement) {
    this.root = root;
  }

  get total(): number {
    return this.marks.length;
  }

  get currentIndex(): number {
    return this.current;
  }

  get query(): string {
    return this.lastQuery;
  }

  /** Re-highlight `query` across the root. Clears any prior highlight first.
   *  Resets the current match to the first (index 0), or -1 when there are
   *  none. Returns the total match count. */
  search(query: string, caseSensitive = false): number {
    this.clear();
    this.lastQuery = query;
    if (query === "") {
      return 0;
    }
    const needle = caseSensitive ? query : query.toLowerCase();
    const marks: HTMLElement[] = [];
    for (const node of this.collectTextNodes()) {
      wrapMatchesInNode(node, query.length, needle, caseSensitive, marks);
    }
    this.marks = marks;
    this.current = marks.length > 0 ? 0 : -1;
    this.applyCurrentClass();
    return marks.length;
  }

  /** Remove all highlight marks and restore the original text nodes. */
  clear(): void {
    for (const mark of this.marks) {
      unwrapMark(mark);
    }
    // Defensive sweep in case an external DOM change stranded marks we no
    // longer track (e.g. a reconcile pass replaced a message element).
    for (const mark of [...this.root.querySelectorAll<HTMLElement>(`mark.${HIT_CLASS}`)]) {
      unwrapMark(mark);
    }
    this.marks = [];
    this.current = -1;
    this.lastQuery = "";
  }

  next(): void {
    if (this.marks.length === 0) {
      return;
    }
    this.current = (this.current + 1) % this.marks.length;
    this.applyCurrentClass();
  }

  prev(): void {
    if (this.marks.length === 0) {
      return;
    }
    this.current = (this.current - 1 + this.marks.length) % this.marks.length;
    this.applyCurrentClass();
  }

  /** Best-effort restore of the current index (used after a live re-run so the
   *  highlight doesn't jump back to match 1 on every streamed chunk). Clamped
   *  to the valid range; no-op when out of range. */
  setCurrent(index: number): void {
    if (index < 0 || index >= this.marks.length) {
      return;
    }
    this.current = index;
    this.applyCurrentClass();
  }

  currentMark(): HTMLElement | null {
    return this.marks[this.current] ?? null;
  }

  private applyCurrentClass(): void {
    for (let i = 0; i < this.marks.length; i++) {
      this.marks[i]?.classList.toggle(CURRENT_CLASS, i === this.current);
    }
  }

  private collectTextNodes(): Text[] {
    const out: Text[] = [];
    const visit = (node: Node): void => {
      if (node.nodeType === TEXT_NODE) {
        if ((node.nodeValue ?? "").length > 0) {
          out.push(node as Text);
        }
        return;
      }
      if (node.nodeType !== ELEMENT_NODE) {
        return;
      }
      if (!isSearchableElement(node as Element)) {
        return;
      }
      for (const child of node.childNodes) {
        visit(child);
      }
    };
    for (const child of this.root.childNodes) {
      visit(child);
    }
    return out;
  }
}
