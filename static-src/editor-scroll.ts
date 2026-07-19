// ---------------------------------------------------------------------------
// Line-jump scroll helpers for the editor pane. Kept separate so editor.ts
// stays focused on file-state + mode management. Called by `openFile(path,
// line?)` after the target file's content is rendered.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";

import { $ } from "./dom.js";

/** Smooth-scroll the editor pane so the given 1-based line lands in the
 *  upper third of the viewport. */
export function scrollToEditorLine(line: number): void {
  const scroller = $.editorHighlight.parentElement;
  if (scroller === null) {
    return;
  }
  const lh = getLineHeight();
  const pad = getPaddingTop();
  const target = pad + (line - 1) * lh;
  const top = Math.max(0, target - scroller.clientHeight / 3);
  scroller.scrollTo({ top, behavior: "smooth" });
}

/** Flash a thin highlight behind the given 1-based line for ~1.2s. Fires
 *  after scroll so the user's eye catches the target. */
export function flashEditorLine(line: number): void {
  const scroller = $.editorHighlight.parentElement;
  if (scroller === null) {
    return;
  }
  const lh = getLineHeight();
  const pad = getPaddingTop();
  const flash = el("div", { className: "editor-line-flash" });
  flash.style.top = `${String(pad + (line - 1) * lh)}px`;
  flash.style.height = `${String(lh)}px`;
  scroller.appendChild(flash);
  setTimeout(() => {
    flash.remove();
  }, 1200);
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
