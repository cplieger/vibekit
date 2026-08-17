// Renders command output into a <pre>: plain text plus the style spans the
// server parsed off it (internal/ansitext).
//
// It replaced the ansi_up dependency, and the reason is not the 2.9 KB. Every
// call site here assigns DOM NODES; none builds an HTML string. The path this
// replaced set `pre.innerHTML = ansiToHtml(text)` on agent-controlled bytes, so
// correctness rested on that library's escaper being right about `& < > " '`
// forever. Building text nodes removes the question rather than answering it.
//
// The parse itself is the server's (see internal/ansitext for why): the text has
// to stay searchable, exportable and redactable as text, so it travels as text
// with offsets beside it rather than as markup.

import { el } from "@cplieger/reactive";
import type { TextSpan } from "./types.js";

// Attribute bits, matching internal/ansitext and web-terminal-engine's
// WireRun.a so this app has one attribute vocabulary rather than two.
const ATTR_BOLD = 1;
const ATTR_ITALIC = 2;
const ATTR_UNDERLINE = 4;
const ATTR_INVERSE = 8;
const ATTR_STRIKE = 16;
const ATTR_DIM = 32;
const ATTR_HIDDEN = 64;
const ATTR_BLINK = 128;
const ATTR_OVERLINE = 256;
const ATTR_DOUBLE_UNDERLINE = 512;

/** -1 means "no colour set", distinct from black (palette index 0). */
const COLOR_DEFAULT = -1;
/** Values at or above this are packed 24-bit colour, not a palette index. */
const RGB_FLAG = 0x1000000;

// The 16 basic palette indices map to the class names in css/15-ansi.css, which
// are theme-tuned rather than the standard values. Indices 16-255 are the
// extended palette and are computed in colorValue.
const BASIC_NAMES = [
  "black",
  "red",
  "green",
  "yellow",
  "blue",
  "magenta",
  "cyan",
  "white",
  "bright-black",
  "bright-red",
  "bright-green",
  "bright-yellow",
  "bright-blue",
  "bright-magenta",
  "bright-cyan",
  "bright-white",
] as const;

/** A styled slice of the output: the text, and how to paint it. */
interface Piece {
  text: string;
  span: TextSpan | null;
}

/**
 * Split text into styled and unstyled pieces using the spans' offsets.
 *
 * Offsets are UTF-16 code units, which is what `String.prototype.slice` indexes
 * with, so no conversion is needed. Spans arrive sorted and non-overlapping (a
 * Go fuzz target asserts both), and this function is defensive about it anyway:
 * a span that reaches backwards or past the end would otherwise slice garbage
 * into the transcript, and clamping is cheaper than trusting.
 *
 * Exported for unit testing; callers use renderOutput/appendOutput.
 */
export function splitBySpans(text: string, spans: readonly TextSpan[]): Piece[] {
  if (spans.length === 0) {
    return text === "" ? [] : [{ text, span: null }];
  }
  const pieces: Piece[] = [];
  let cursor = 0;
  for (const span of spans) {
    const start = Math.max(cursor, Math.min(span.start, text.length));
    const end = Math.max(start, Math.min(span.end, text.length));
    if (start > cursor) {
      pieces.push({ text: text.slice(cursor, start), span: null });
    }
    if (end > start) {
      pieces.push({ text: text.slice(start, end), span });
    }
    cursor = end;
  }
  if (cursor < text.length) {
    pieces.push({ text: text.slice(cursor), span: null });
  }
  return pieces;
}

/** Resolve a wire colour to a CSS colour value, or null for a basic index that
 *  a class already covers.
 *
 *  The extended palette is COMPUTED rather than tabulated, because it is
 *  defined algorithmically: 16-231 is a 6x6x6 RGB cube over the levels
 *  {0,95,135,175,215,255}, and 232-255 is a 24-step greyscale ramp from 8 to
 *  238. Writing it out would be 240 CSS declarations or a 240-entry TS array,
 *  and either would be a second place for the same rule to be wrong. */
function colorValue(c: number): string | null {
  if (c >= RGB_FLAG) {
    return rgb((c >> 16) & 0xff, (c >> 8) & 0xff, c & 0xff);
  }
  if (c >= 232 && c <= 255) {
    const level = 8 + (c - 232) * 10;
    return rgb(level, level, level);
  }
  if (c >= 16 && c <= 231) {
    const n = c - 16;
    return rgb(CUBE[Math.floor(n / 36)], CUBE[Math.floor(n / 6) % 6], CUBE[n % 6]);
  }
  return null;
}

/** The six levels of the 6x6x6 colour cube, per the xterm palette. */
const CUBE = [0, 95, 135, 175, 215, 255] as const;

function rgb(r: number | undefined, g: number | undefined, b: number | undefined): string {
  return `rgb(${String(r ?? 0)} ${String(g ?? 0)} ${String(b ?? 0)})`;
}

/** Build the class list and inline colour styles for one span. */
function applySpan(node: HTMLElement, span: TextSpan): void {
  const classes: string[] = [];
  const { attrs } = span;
  if ((attrs & ATTR_BOLD) !== 0) {
    classes.push("ansi-bold");
  }
  if ((attrs & ATTR_DIM) !== 0) {
    classes.push("ansi-dim");
  }
  if ((attrs & ATTR_ITALIC) !== 0) {
    classes.push("ansi-italic");
  }
  if ((attrs & ATTR_UNDERLINE) !== 0) {
    classes.push("ansi-underline");
  }
  if ((attrs & ATTR_DOUBLE_UNDERLINE) !== 0) {
    classes.push("ansi-double-underline");
  }
  if ((attrs & ATTR_OVERLINE) !== 0) {
    classes.push("ansi-overline");
  }
  if ((attrs & ATTR_STRIKE) !== 0) {
    classes.push("ansi-strike");
  }
  if ((attrs & ATTR_BLINK) !== 0) {
    classes.push("ansi-blink");
  }
  if ((attrs & ATTR_HIDDEN) !== 0) {
    classes.push("ansi-hidden");
  }
  // Inverse swaps foreground and background. Done by swapping the values here
  // rather than with a CSS filter, because only this code knows what the two
  // colours actually are once defaults are involved.
  const inverse = (attrs & ATTR_INVERSE) !== 0;
  const fg = inverse ? span.bg : span.fg;
  const bg = inverse ? span.fg : span.bg;
  // Under inverse, a side that is DEFAULT has to resolve to something concrete
  // or the swap loses half of itself. Two single-property classes do that, one
  // per side, so each side is handled independently: a rule setting both would
  // override the swapped palette class on the explicit side, which is how
  // inverse red-on-blue used to render as the default inverse pair.

  if (fg !== COLOR_DEFAULT) {
    const v = colorValue(fg);
    if (v === null) {
      classes.push(`ansi-${BASIC_NAMES[fg] ?? "white"}-fg`);
    } else {
      node.style.color = v;
    }
  } else if (inverse) {
    classes.push("ansi-inverse-fg");
  }
  if (bg !== COLOR_DEFAULT) {
    const v = colorValue(bg);
    if (v === null) {
      classes.push(`ansi-${BASIC_NAMES[bg] ?? "black"}-bg`);
    } else {
      node.style.backgroundColor = v;
    }
  } else if (inverse) {
    classes.push("ansi-inverse-bg");
  }
  if (classes.length > 0) {
    node.className = classes.join(" ");
  }
}

/** Build a DocumentFragment for text + spans. Text nodes for unstyled runs,
 *  one <span> per styled run. Nothing is ever parsed as HTML. */
export function outputFragment(text: string, spans: readonly TextSpan[]): DocumentFragment {
  const frag = document.createDocumentFragment();
  for (const piece of splitBySpans(text, spans)) {
    if (piece.span === null) {
      frag.appendChild(document.createTextNode(piece.text));
      continue;
    }
    const node = el("span");
    node.textContent = piece.text;
    applySpan(node, piece.span);
    frag.appendChild(node);
  }
  return frag;
}

/** Replace an element's content with the rendered output. */
export function renderOutput(host: HTMLElement, text: string, spans: readonly TextSpan[]): void {
  host.replaceChildren(outputFragment(text, spans));
}

/**
 * Append a live chunk to an element already holding earlier output.
 *
 * `base` is the offset the chunk's text starts at in the accumulated output, so
 * the chunk's absolute span offsets can be rebased onto it. The caller passes
 * the payload's `offset` field, which the server sets from the terminal's own
 * accumulated length; that makes a dropped chunk detectable rather than silently
 * shifting every later span.
 */
export function appendOutput(
  host: HTMLElement,
  text: string,
  spans: readonly TextSpan[],
  base: number,
): void {
  const local = spans.map((s) => ({ ...s, start: s.start - base, end: s.end - base }));
  host.appendChild(outputFragment(text, local));
}
