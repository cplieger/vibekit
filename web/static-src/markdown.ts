// ---------------------------------------------------------------------------
// Markdown rendering for chat bubbles, history replay, and any other
// surface that renders markdown to DOM. One append-only streaming parser
// (smd-parser) backs both the live-streaming path and the one-shot path.
//
//   - Streaming assistant messages use `MarkdownRenderer`. Each write()
//     feeds only the delta to the parser; already-mounted DOM nodes are
//     never touched. New text fragments are wrapped in
//     `<span data-vk-stream>` so CSS fades each chunk in once on mount.
//
//   - Non-streaming contexts (history replay, previews) use
//     `renderMarkdown(md)` — the same parser in non-animate mode.
//
// Decoration (inline file-path linkify + code-block syntax highlighting
// + copy/run buttons) is a post-process pass on the already-produced DOM.
// The streaming path runs it at finalize; the one-shot path wraps it in
// renderBubble. Neither path does an innerHTML swap after streaming.
// ---------------------------------------------------------------------------

import { parser, parser_end, parser_write } from "./smd-parser.js";
import type { Parser } from "./smd-parser.js";
import { domRenderer, unwrapStreamSpans } from "./smd-renderer.js";
import { linkifyPaths } from "./linkify.js";
import { decorateCodeBlocks } from "./code-blocks.js";

// ---------------------------------------------------------------------------
// Streaming renderer — for live assistant bubbles.
// ---------------------------------------------------------------------------

/** Lifecycle wrapper around a single assistant `<div class="message
 *  assistant streaming">`. Each write() receives the FULL raw
 *  markdown-so-far; the renderer parses only the delta vs. the
 *  previous call and appends DOM nodes at the current parse position.
 *
 *  Input invariant: raw markdown is strictly append-only. kiro-cli's
 *  streaming respects this; any violation throws so callers get a
 *  clear signal rather than silent retroactive DOM rebuilds. */
export class MarkdownRenderer {
  private lastRaw = "";
  private parser: Parser;

  constructor(private readonly el: HTMLElement) {
    this.parser = parser(domRenderer(el, true));
  }

  /** Feed the latest raw markdown. Called on every debounced flush
   *  from messages.ts. */
  write(rawMarkdown: string): void {
    if (rawMarkdown === this.lastRaw) return;
    if (!rawMarkdown.startsWith(this.lastRaw)) {
      throw new Error("MarkdownRenderer.write: input is not append-only");
    }
    parser_write(this.parser, rawMarkdown.slice(this.lastRaw.length));
    this.lastRaw = rawMarkdown;
  }

  /** Close the stream. Parser finalizes pending tokens, the per-chunk
   *  fade-in spans are unwrapped (merged into their parents via
   *  Node.normalize), and decoration (linkify + code-block highlight
   *  + copy buttons) runs in-place. No innerHTML swap. */
  finalize(): void {
    parser_end(this.parser);
    unwrapStreamSpans(this.el);
    linkifyPaths(this.el);
    decorateCodeBlocks(this.el);
  }

  get element(): HTMLElement { return this.el; }
}

// ---------------------------------------------------------------------------
// One-shot rendering — for history replay, tool outputs, previews.
// ---------------------------------------------------------------------------

/** Render markdown synchronously to an HTML string. Uses the same
 *  parser as the streaming path, in non-animate mode (no fade-in
 *  spans). */
export function renderMarkdown(md: string): string {
  const el = document.createElement("div");
  const p = parser(domRenderer(el, false));
  parser_write(p, md);
  parser_end(p);
  return el.innerHTML;
}

/** Render markdown into an existing element and apply decoration in
 *  place. For history replay — write() on a fresh MarkdownRenderer
 *  would over-engineer this path since no streaming is happening. */
export function renderBubble(el: HTMLElement, md: string): void {
  el.dataset["raw"] = md;
  el.innerHTML = renderMarkdown(md);
  linkifyPaths(el);
  decorateCodeBlocks(el);
}
