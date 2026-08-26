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

/**
 * Words in the accumulated trace: whitespace-separated tokens, nothing cleverer.
 *
 * Recounted from the WHOLE string on every update rather than added up per
 * delta, because a chunk boundary can fall inside a word ("reas" + "oning" is
 * one word, not two). A trace is a few thousand characters, so the recount is
 * cheaper than being wrong.
 */
function wordCount(text: string): number {
  return text
    .trim()
    .split(/\s+/u)
    .filter((w) => w !== "").length;
}

/** A mounted reasoning block plus its imperative update handle. */
export interface ReasoningView {
  /** The `<details>` root to insert into the DOM. */
  readonly root: HTMLDetailsElement;
  /** Append a streamed delta to the body (text-node append + word recount). */
  append(delta: string): void;
  /** Replace-to-full: append only the tail beyond what's rendered. */
  setText(full: string): void;
  /** Seal the trace: flip the summary, collapse. Idempotent. */
  seal(): void;
}

/**
 * Build a reasoning block. `live` mounts it open and labelled "Thinking…";
 * replay mounts it collapsed and labelled "Reasoning".
 *
 * A live trace carries NO pulse dot, deliberately: the open disclosure with text
 * growing into it, the "Thinking…" label and the turn header's own running dot
 * are the live cues (see 13-messages.css above `.reasoning-summary`). Nothing
 * reads a `streaming` class off this root, so none is set.
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
  const summary = el("summary", { className: "reasoning-summary" }, chevronEl(), label, count);
  const body = el("blockquote", { className: "reasoning-body" }, initial);
  root.append(summary, body);
  if (live) {
    root.open = true;
  }

  // The accumulated trace. Its length is also the watermark `setText` slices
  // against, so this replaced a separate `rendered` counter rather than joining
  // it — two names for one number is how they drift apart.
  let text = initial;
  let sealed = false;

  /** Repaint the count. A count of zero renders NOTHING, not "0 words". */
  function showCount(): void {
    const n = wordCount(text);
    // toLocaleString so a long trace reads "1,204 words" rather than "1204".
    count.textContent = n === 0 ? "" : `${n.toLocaleString()} ${n === 1 ? "word" : "words"}`;
  }
  showCount();

  return {
    root,
    append(delta: string): void {
      if (delta === "") {
        return;
      }
      body.appendChild(document.createTextNode(delta));
      text += delta;
      showCount();
    },
    setText(full: string): void {
      if (full.length <= text.length) {
        return;
      }
      body.appendChild(document.createTextNode(full.slice(text.length)));
      text = full;
      showCount();
    },
    seal(): void {
      if (sealed) {
        return;
      }
      sealed = true;
      label.textContent = "Thinking completed";
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
