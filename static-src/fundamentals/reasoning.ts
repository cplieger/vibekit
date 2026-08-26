// ---------------------------------------------------------------------------
// Fundamental: ReasoningBlock — the collapsible "Thinking…" / "Thinking
// completed" trace for a `thinking` block.
//
// Pure view: no store, no signals. The composition layer feeds it deltas
// (streaming) or a full string (replay) and seals it when the trace ends or
// the first sibling text arrives (the IDE auto-collapses reasoning the moment
// regular output starts). Reuses the existing `.reasoning-block` CSS vocab.
//
// The summary row also carries the trace's word count, right-aligned: collapsed,
// "Thinking completed" alone says nothing about how much is folded behind it.
// A number only — no preview text, so nothing of the reasoning CONTENT leaks
// into the summary of a collapsed block.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { preserveReadingPosition } from "../scroll.js";

/** One whitespace character. Module scope and no `g` flag, so `test` is
 *  stateless and the object is allocated once rather than per delta. */
const SPACE = /\s/u;

/**
 * Words `chunk` adds to a trace that currently ends `openWord`, plus whether it
 * ends mid-word after it. Whitespace-separated tokens, nothing cleverer.
 *
 * ADDED UP PER DELTA rather than recounted from the whole string, which reverses
 * an earlier decision here, so it owes that decision an answer. The objection was
 * that a chunk boundary can fall inside a word ("reas" + "oning" is one word, not
 * two) — and that is exactly what `openWord` carries: a chunk that starts on a
 * non-space while the trace already ends on one is continuing a word this counter
 * has counted, so its first run is not a new word. The premise that went with the
 * objection is what did not hold. "A trace is a few thousand characters, so the
 * recount is cheaper than being wrong" was measured false: across the real chat
 * files, thinking is 1.2 MB over 3133 blocks and a single trace reaches tens of
 * kilobytes. Recounting per delta made this O(length squared) per block over the
 * largest content bucket in the app, and `syncMountedText` re-drove it on every
 * repaint — for COLLAPSED delegate traces nobody is looking at, too.
 *
 * Whitespace tokens are the whole rule, and a character fallback for scripts
 * that write without inter-word spaces was tried and removed rather than left
 * unwritten. Switching units past a characters-per-token threshold cannot tell a
 * CJK trace from an English one holding a single long token, so a trace that was
 * one URL, one hash or one stack frame reported its character count as though it
 * were a different language. A Chinese trace reading "1 word" is a known and
 * accepted limitation of a whitespace measure; a URL reading "44 characters" was
 * a wrong answer in the common case, which is worse. A real fix is
 * `Intl.Segmenter`, and it is a separate piece of work.
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
  // The label is its own element so `seal()` can rewrite it without touching the
  // chevron beside it. A bare `summary.textContent = …` would delete the glyph,
  // which is why the chevron replaced a `::before` rather than becoming a plain
  // child of the summary.
  const label = el("span", { className: "reasoning-label" }, live ? "Thinking…" : "Reasoning");
  // Its own element beside the label for the same reason the label is its own
  // element: `seal()` rewrites the label and nothing else in the row. It also
  // keeps the label's text exactly "Thinking…" / "Thinking completed" — the
  // state, not the state plus a measurement.
  const count = el("span", { className: "reasoning-count" });
  // `<summary>` is focusable and its accessible name is computed from its
  // DESCENDANTS, so a number living in the row is part of that name: repainting
  // it on every streamed chunk renames a focusable control dozens of times per
  // trace, and a screen reader re-announces the control on each rename. Hidden
  // from the name PERMANENTLY, live and sealed alike — an `aria-hidden` that
  // came off at `seal()` would still rename the row one final time, under a
  // reader whose focus may be sitting on it, and it is the renaming rather than
  // the number that is the defect. The trade is deliberate: the label carries
  // the STATE, which is the part that has to be announced, and the count is a
  // footnote on it that costs a screen-reader user nothing to miss.
  count.setAttribute("aria-hidden", "true");
  const summary = el("summary", { className: "reasoning-summary" }, chevronEl(), label, count);
  const body = el("blockquote", { className: "reasoning-body" }, initial);
  root.append(summary, body);
  if (live) {
    root.open = true;
    root.classList.add("streaming");
  }

  // The accumulated trace. Its length is also the watermark `setText` slices
  // against, so this replaced a separate `rendered` counter rather than joining
  // it — two names for one number is how they drift apart.
  let text = initial;
  let sealed = false;
  // The running count and the one bit of state that makes it exact across a
  // chunk boundary: whether the trace so far ends inside a word.
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
    // toLocaleString so a long trace reads "1,204 words" rather than "1204".
    count.textContent =
      words === 0 ? "" : `${words.toLocaleString()} ${words === 1 ? "word" : "words"}`;
  }
  // The mount's own text goes through the same fold, so there is one counting
  // path rather than a seed that could disagree with the increments after it.
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
      // The TAIL, not `full`: this path is replace-to-full on the wire but
      // append-only in effect, and counting `full` would double every word.
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
      // Every path that can change the trace ends in a repaint, this one
      // included: the collapsed summary is what the reader is left with, so the
      // number on it is asserted rather than assumed.
      showCount();
      // An IMMEDIATE geometry change — a bare `open = false` on a native
      // <details>, no animation — which still removes height above the reader.
      // Compensated for the same reason as tool-group's animated collapse, by
      // the same helper; the two behave differently enough to be worth testing
      // separately, which is why §3.4 names them apart.
      preserveReadingPosition(() => {
        root.open = false;
      }, "content-growth");
    },
  };
}
