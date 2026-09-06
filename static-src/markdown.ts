// ---------------------------------------------------------------------------
// Markdown rendering — three surfaces, one parser.
//
// Streaming (live assistant turn):
//   const r = createMarkdownStream(bubble);
//   r.writeDelta("Hello "); r.writeDelta("world"); r.end();
//   → blocks decorate (code-block highlight, path linkify) AND animate
//     (data-vk-block-enter for the per-block fade) as they complete.
//   → large flushes (e.g. a 50KB code block dumped at once) are split
//     across tasks via MessageChannel so they don't block the main
//     thread; r.end() drains synchronously.
//
// Replay (chat-switch, history scrollback):
//   renderMarkdownInto(bubble, fullMarkdown);
//   → blocks decorate but DON'T animate (the user has seen this before).
//
// Structural string output (tests, previews):
//   const html = renderMarkdown(md);
//   → no decoration, no animation. Pure parser output.
//
// All three paths use the same smd-parser; the only differences are
// (a) the per-block hook installed on the renderer and (b) whether
// large writes get yielded across tasks.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { parser, parser_end, parser_write } from "./smd-parser.js";
import type { Parser } from "./smd-parser.js";
import { domRenderer } from "./smd-renderer.js";
import { linkifyPaths } from "./linkify.js";
import { decorateCodeBlocks, decorateStreamingCodeTail } from "./code-blocks.js";
import { renderSvgBlock } from "./svg-block.js";

/** Incremental-write flush throttle while streaming: buffered deltas are
 *  fed to the append-only streaming parser at most once per interval.
 *  Nothing re-parses — previously rendered DOM stays untouched. 200ms is
 *  the sweet spot from vibekit-ui.md (Vercel ai-chatbot pattern). */
const FLUSH_INTERVAL_MS = 200;

/** Maximum bytes parsed per task slice. Tuned so a single slice
 *  completes in well under a frame budget on a slow device. Large
 *  flushes are split into N slices yielded across tasks. */
const PARSE_SLICE_BYTES = 4096;

/** Task-yielding scheduler. MessageChannel.postMessage is the
 *  cheapest way to yield to the browser between tasks (browsers
 *  schedule it as a fresh macro-task, allowing paint between
 *  callbacks). React's scheduler uses the same trick. */
const yieldChannel = new MessageChannel();
const yieldQueue: (() => void)[] = [];
const MAX_DRAIN_PER_TICK = 4;
yieldChannel.port1.onmessage = () => {
  const count = Math.min(MAX_DRAIN_PER_TICK, yieldQueue.length);
  const fns = yieldQueue.splice(0, count);
  for (const fn of fns) {
    fn();
  }
  if (yieldQueue.length > 0) {
    yieldChannel.port2.postMessage(null);
  }
};
function nextTick(fn: () => void): void {
  const wasEmpty = yieldQueue.length === 0;
  yieldQueue.push(fn);
  if (wasEmpty) {
    yieldChannel.port2.postMessage(null);
  }
}

/** Decorate a freshly-completed block. Idempotent. Returns the element now
 *  standing in the document, which is not always the one passed in — an `svg`
 *  fence is REPLACED by its rendered diagram. */
function decorate(block: HTMLElement): HTMLElement {
  if (block.tagName === "PRE") {
    // Ahead of decorateCodeBlocks: a converted diagram is no longer a code
    // block, so it must not collect a language label, a Copy button or
    // highlighting for source the reader can no longer see.
    const diagram = renderSvgBlock(block);
    if (diagram !== null) {
      return diagram;
    }
    if (block.parentElement !== null) {
      decorateCodeBlocks(block.parentElement);
    }
    return block;
  }
  linkifyPaths(block);
  return block;
}

/** Decorate + tag for the per-block entry animation. */
function decorateAndAnimate(block: HTMLElement): void {
  // The tag goes on whatever decorate left in the document: setting it on a
  // `<pre>` that has just been replaced by a diagram would animate a detached
  // node and leave the visible one un-faded.
  decorate(block).setAttribute("data-vk-block-enter", "");
}

export interface MarkdownStream {
  /** Append a markdown fragment. Parser sees the cumulative effect via
   *  the internal flush; large flushes yield across tasks. Returns
   *  immediately; parsing may complete asynchronously. */
  writeDelta(delta: string): void;
  /** Parse everything written so far, now, without finalizing.
   *
   *  `writeDelta` buffers by default, which is right while text arrives a token
   *  at a time and wrong for a write the reader is already looking at — a bubble
   *  re-rendered from its accumulated text would otherwise sit blank for the
   *  interval. The first 4KB lands synchronously and any remainder yields across
   *  tasks, same as the streaming path. */
  flush(): void;
  /** Finalize the stream: flush + drain any pending tasks
   *  synchronously, then end the parser. Idempotent — safe to call
   *  multiple times; subsequent calls are no-ops. */
  end(): void;
}

export interface MarkdownStreamOptions {
  /** Milliseconds to buffer a `writeDelta` before parsing. Defaults to
   *  FLUSH_INTERVAL_MS; `0` parses on write.
   *
   *  A caller that regulates its own write cadence wants 0 — see `reveal.ts`,
   *  which spreads the network's lumps across frames on purpose. Holding a
   *  frame's worth for another 200ms would re-lump exactly what it spread, so
   *  the smoothing would be undone one stage downstream of itself. */
  flushIntervalMs?: number;
}

/** Streaming markdown renderer for live assistant bubbles. Owns its
 *  own write buffer, flush schedule, and per-block decoration +
 *  animation hooks. Large writes split across tasks. */
export function createMarkdownStream(
  host: HTMLElement,
  options: MarkdownStreamOptions = {},
): MarkdownStream {
  const flushAfter = options.flushIntervalMs ?? FLUSH_INTERVAL_MS;
  const p: Parser = parser(
    domRenderer(host, {
      onBlockComplete: decorateAndAnimate,
      animateText: true,
    }),
  );
  let buffer = "";
  let pendingParse = ""; // text queued for chunked parsing
  let flushTimer: ReturnType<typeof setTimeout> | undefined;
  let draining = false;
  let ended = false;

  const drain = (): void => {
    if (pendingParse === "") {
      draining = false;
      return;
    }
    const sliceEnd = Math.min(PARSE_SLICE_BYTES, pendingParse.length);
    const slice = pendingParse.slice(0, sliceEnd);
    pendingParse = pendingParse.slice(sliceEnd);
    parser_write(p, slice);
    // A fence that is still open reaches no per-block callback, so the sweep
    // after each slice is what gives a streaming block its language label and
    // its Copy button while it arrives.
    decorateStreamingCodeTail(host);
    if (pendingParse !== "") {
      nextTick(drain);
    } else {
      draining = false;
    }
  };

  const flush = (): void => {
    if (buffer === "") {
      return;
    }
    pendingParse += buffer;
    buffer = "";
    if (flushTimer !== undefined) {
      clearTimeout(flushTimer);
      flushTimer = undefined;
    }
    if (!draining) {
      draining = true;
      drain();
    }
  };

  return {
    writeDelta(delta: string): void {
      if (ended || delta === "") {
        return;
      }
      buffer += delta;
      if (flushAfter <= 0) {
        flush();
        return;
      }
      flushTimer ??= setTimeout(flush, flushAfter);
    },
    flush(): void {
      if (ended) {
        return;
      }
      flush();
    },
    end(): void {
      if (ended) {
        return;
      }
      ended = true;
      // Move any unflushed buffered text into pendingParse.
      if (buffer !== "") {
        pendingParse += buffer;
        buffer = "";
      }
      if (flushTimer !== undefined) {
        clearTimeout(flushTimer);
        flushTimer = undefined;
      }
      // Drain everything synchronously — end() is a "complete now"
      // contract from the caller's perspective. Yielding only applies
      // during normal streaming.
      while (pendingParse !== "") {
        const sliceEnd = Math.min(PARSE_SLICE_BYTES, pendingParse.length);
        parser_write(p, pendingParse.slice(0, sliceEnd));
        pendingParse = pendingParse.slice(sliceEnd);
      }
      draining = false;
      parser_end(p);
      // parser_end does NOT close an open fence, so a block the model never
      // terminated is still provisional here. Finalize it: highlighting and the
      // Run button are the half a streaming pass deliberately withholds, and
      // withholding them forever is the bug this closes.
      decorateCodeBlocks(host);
    },
  };
}

/** Replay-path render: parse the full markdown into `el` with
 *  decoration but no entry animation. Synchronous — replay paths
 *  aren't time-critical (they happen on chat-switch, not in the
 *  streaming hot path). */
export function renderMarkdownInto(host: HTMLElement, md: string): void {
  const p = parser(domRenderer(host, { onBlockComplete: decorate }));
  parser_write(p, md);
  parser_end(p);
  // Same reason as the streaming path's end(): an unterminated fence in stored
  // content never reaches the per-block callback, and a reloaded transcript must
  // not be the one place a code block loses its chrome.
  decorateCodeBlocks(host);
}

/** Pure parser output — no decoration, no animation. Used by tests,
 *  hover previews, and any other surface that wants the structural
 *  HTML without the decoration overlay. */
export function renderMarkdown(md: string): string {
  const tmp = el("div");
  const p = parser(domRenderer(tmp));
  parser_write(p, md);
  parser_end(p);
  return tmp.innerHTML;
}
