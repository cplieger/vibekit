// ---------------------------------------------------------------------------
// Position cues inside the editor pane's scroller: take a line into view, flash
// it, and mark a span of it. Kept apart from editor-core.ts so that module stays
// on file state and modes; this one owns the line geometry (`pad + (line-1) *
// lh`, true for the highlighted source pre and for the textarea that shares its
// metrics) and the two elements it positions with it.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";

import { $ } from "./dom.js";

/** Scroll the editor pane so the given 1-based line lands in the upper third of
 *  the viewport. A link or deep link jumps smoothly; a find step is `instant`,
 *  because a held Enter restarts a smooth scroll before it arrives (measured at
 *  ~1.8s to settle for a 900-line jump) and the mark placed on the line would be
 *  off screen for the whole animation. */
export function scrollToEditorLine(line: number, behavior: ScrollBehavior = "smooth"): void {
  const scroller = $.editorHighlight.parentElement;
  if (scroller === null) {
    return;
  }
  const lh = getLineHeight();
  const pad = getPaddingTop();
  const target = pad + (line - 1) * lh;
  const top = Math.max(0, target - scroller.clientHeight / 3);
  scroller.scrollTo({ top, behavior });
}

/** ONE flash element, re-positioned per call: a reader holding Enter on a find
 *  step made forty of them in a second, each alive for its own 1.2s. */
let flash: HTMLElement | null = null;
let flashTimer: ReturnType<typeof setTimeout> | undefined;

/** Flash a thin highlight behind the given 1-based line for ~1.2s. Fires
 *  after scroll so the user's eye catches the target. */
export function flashEditorLine(line: number): void {
  const scroller = $.editorHighlight.parentElement;
  if (scroller === null) {
    return;
  }
  const lh = getLineHeight();
  const pad = getPaddingTop();
  const box = flash ?? el("div", { className: "editor-line-flash" });
  flash = box;
  // Re-inserting the element is what restarts its CSS animation.
  box.remove();
  box.style.top = `${String(pad + (line - 1) * lh)}px`;
  box.style.height = `${String(lh)}px`;
  scroller.appendChild(box);
  clearTimeout(flashTimer);
  flashTimer = setTimeout(() => {
    box.remove();
  }, 1200);
}

/** One mark element, like the flash. */
let mark: HTMLElement | null = null;

/** Mark `match` on the given 1-based line, where `prefix` is the line's text
 *  before it, and pan the pane so the mark is on screen. The inline geometry is
 *  MEASURED on a hidden twin of the source surface (a `<pre>` carrying its class,
 *  so font, padding and tab stops are the surface's), never `column × advance`:
 *  a tab expands from the line's start and a wide glyph is wider, so the advance
 *  is wrong on exactly the lines a reader searches. Over the textarea the mark
 *  sits 2px right (its start border) and follows the textarea's OWN inline
 *  scroller; over the pre it is `.editor-body` that pans. */
export function markEditorSpan(line: number, prefix: string, match: string): void {
  const scroller = $.editorHighlight.parentElement;
  if (scroller === null) {
    return;
  }
  const editing = !$.editorContent.classList.contains("hidden");
  const surface: HTMLElement = editing ? $.editorContent : $.editorHighlight;
  const span = measureSpan(scroller, prefix, match);
  const lh = getLineHeight();
  const top = getPaddingTop() + (line - 1) * lh;

  if (editing) {
    revealInTextarea($.editorContent, span);
  } else {
    revealInScroller(scroller, surface.offsetLeft, span);
  }
  const left =
    surface.offsetLeft + surface.clientLeft + span.start - (editing ? surface.scrollLeft : 0);

  const box = mark ?? el("div", { className: "editor-find-mark" });
  mark = box;
  box.style.top = `${String(top)}px`;
  box.style.left = `${String(left)}px`;
  box.style.width = `${String(span.width)}px`;
  box.style.height = `${String(lh)}px`;
  if (box.parentNode !== scroller) {
    scroller.appendChild(box);
  }
}

export function clearEditorMark(): void {
  mark?.remove();
}

/** Where a span of one line starts and how wide it is, in the surface's own
 *  box: `start` is measured from the surface's border edge and includes its
 *  padding. */
interface SpanBox {
  readonly start: number;
  readonly width: number;
}

function measureSpan(scroller: HTMLElement, prefix: string, match: string): SpanBox {
  const before = el("span", {}, prefix);
  const hit = el("span", {}, match);
  const probe = el(
    "pre",
    { className: "editor-highlight editor-find-probe", "aria-hidden": "true" },
    before,
    hit,
  );
  scroller.appendChild(probe);
  const probeRect = probe.getBoundingClientRect();
  const hitRect = hit.getBoundingClientRect();
  probe.remove();
  return { start: hitRect.left - probeRect.left, width: hitRect.width };
}

/** The textarea pans a long line inside itself, so its own `scrollLeft` is what
 *  brings a far column on screen. The third is the block axis's rule too. */
function revealInTextarea(area: HTMLTextAreaElement, span: SpanBox): void {
  const from = area.scrollLeft;
  const to = from + area.clientWidth;
  if (span.start < from || span.start + span.width > to) {
    area.scrollLeft = Math.max(0, span.start - area.clientWidth / 3);
  }
}

/** The pre grows to its longest line and `.editor-body` pans; the gutter is
 *  sticky at the start edge, so the visible text begins past `surfaceLeft`. */
function revealInScroller(scroller: HTMLElement, surfaceLeft: number, span: SpanBox): void {
  const x = surfaceLeft + span.start;
  const visible = scroller.clientWidth - surfaceLeft;
  const from = scroller.scrollLeft + surfaceLeft;
  const to = scroller.scrollLeft + scroller.clientWidth;
  if (x < from || x + span.width > to) {
    scroller.scrollTo({ left: Math.max(0, x - surfaceLeft - visible / 3), behavior: "instant" });
  }
}

// Metrics are re-read per call, deliberately uncached: line jumps are
// rare (link click / deep link), and a module-global cache captured at
// first use mis-placed every later jump after a zoom or font-size
// change (there was no invalidation path).

function getLineHeight(): number {
  const style = getComputedStyle($.editorCode);
  const lh = parseFloat(style.lineHeight);
  if (Number.isFinite(lh) && lh > 0) {
    return lh;
  }
  const fs = parseFloat(style.fontSize);
  return Number.isFinite(fs) && fs > 0 ? fs * 1.5 : 18;
}

function getPaddingTop(): number {
  const pad = parseFloat(getComputedStyle($.editorHighlight).paddingTop);
  return Number.isFinite(pad) && pad >= 0 ? pad : 0;
}
