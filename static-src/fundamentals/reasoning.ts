// ---------------------------------------------------------------------------
// Fundamental: ReasoningBlock — the collapsible "Thinking…" / "Thinking
// completed" trace for a `thinking` block.
//
// Pure view: composition feeds deltas (streaming) or a full string (replay)
// and seals it when the trace ends or the first sibling text arrives. The
// summary row carries a right-aligned word count (number only, no preview —
// nothing of the reasoning content should leak into a collapsed summary).
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { preserveReadingPosition } from "../scroll.js";

/** One whitespace character. No `g` flag, so `test` stays stateless. */
const SPACE = /\s/u;

/**
 * Words `chunk` adds to a trace ending `openWord`, plus whether it ends
 * mid-word after it. Whitespace-separated tokens only — a CJK trace with no
 * inter-word spaces reads as "1 word"; a character-count fallback was tried
 * and removed because it misjudged a URL/hash/stack-frame token as its
 * character count. `Intl.Segmenter` would fix both; separate work.
 *
 * Added up PER DELTA rather than recounted from the whole string: a real
 * trace reaches tens of KB (measured: 1.2 MB thinking over 3133 blocks), so
 * recounting per delta is O(length²) per block, re-driven on every repaint
 * even for a collapsed trace nobody is looking at.
 */
function foldWords(chunk: string, openWord: boolean): { added: number; openWord: boolean } {
  if (chunk === "") {
    return { added: 0, openWord };
  }
  let added = chunk.split(/\s+/u).filter((w) => w !== "").length;
  if (added > 0 && openWord && !SPACE.test(chunk.charAt(0))) {
    added -= 1;
  }
  return { added, openWord: !SPACE.test(chunk.charAt(chunk.length - 1)) };
}

/** A mounted reasoning block plus its imperative update handle. */
export interface ReasoningView {
  /** The `<details>` root to insert into the DOM. */
  readonly root: HTMLDetailsElement;
  /** Append a streamed delta to the body (text-node append + word recount). */
  append(delta: string): void;
  /** Replace-to-full: append only the tail beyond what's rendered. */
  setText(full: string): void;
  /** Seal the trace: flip the summary, drop the pulse, collapse. Idempotent. */
  seal(): void;
}

/**
 * Build a reasoning block. `live` opens it with the streaming pulse; replay
 * mounts it collapsed and labelled "Reasoning".
 */
export function buildReasoning(initial: string, live: boolean): ReasoningView {
  const root = el("details", {
    className: "reasoning-block msg-reasoning",
  }) as HTMLDetailsElement;
  // Own element so seal() can rewrite it without touching the chevron beside it.
  const label = el("span", { className: "reasoning-label" }, live ? "Thinking…" : "Reasoning");
  const count = el("span", { className: "reasoning-count" });
  // `<summary>`'s accessible name is computed from its descendants, and it is
  // focusable — repainting a number in it on every chunk would rename a
  // focusable control dozens of times per trace and re-announce on each
  // rename. Hidden permanently (not just while live), since a screen reader
  // needs the label's STATE, not a footnote it costs nothing to miss.
  count.setAttribute("aria-hidden", "true");
  const summary = el("summary", { className: "reasoning-summary" }, chevronEl(), label, count);
  const body = el("blockquote", { className: "reasoning-body" }, initial);
  root.append(summary, body);
  if (live) {
    root.open = true;
    root.classList.add("streaming");
  }

  // Also the watermark setText() slices against.
  let text = initial;
  let sealed = false;
  let words = 0;
  let openWord = false;

  /** Fold one appended chunk into the running count. */
  function countIn(chunk: string): void {
    const r = foldWords(chunk, openWord);
    words += r.added;
    openWord = r.openWord;
  }

  /** Repaint the count. A count of zero renders NOTHING, not "0 words". */
  function showCount(): void {
    count.textContent =
      words === 0 ? "" : `${words.toLocaleString()} ${words === 1 ? "word" : "words"}`;
  }
  countIn(initial);
  showCount();

  return {
    root,
    append(delta: string): void {
      if (delta === "") {
        return;
      }
      body.appendChild(document.createTextNode(delta));
      text += delta;
      countIn(delta);
      showCount();
    },
    setText(full: string): void {
      if (full.length <= text.length) {
        return;
      }
      const tail = full.slice(text.length);
      body.appendChild(document.createTextNode(tail));
      text = full;
      countIn(tail);
      showCount();
    },
    seal(): void {
      if (sealed) {
        return;
      }
      sealed = true;
      label.textContent = "Thinking completed";
      root.classList.remove("streaming");
      showCount();
      // No animation on a bare `open = false`, so compensate scroll position.
      preserveReadingPosition(() => {
        root.open = false;
      }, "content-growth");
    },
  };
}
