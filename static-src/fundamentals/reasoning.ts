// ---------------------------------------------------------------------------
// Fundamental: ReasoningBlock — the collapsible "Thinking…" / "Thinking
// completed" trace for a `thinking` block.
//
// Pure view: no store, no signals. The composition layer feeds it deltas
// (streaming) or a full string (replay) and seals it when the trace ends or
// the first sibling text arrives (the IDE auto-collapses reasoning the moment
// regular output starts). Reuses the existing `.reasoning-block` CSS vocab.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { preserveReadingPosition } from "../scroll.js";

/** A mounted reasoning block plus its imperative update handle. */
export interface ReasoningView {
  /** The `<details>` root to insert into the DOM. */
  readonly root: HTMLDetailsElement;
  /** Append a streamed delta to the body (cheap text-node append). */
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
  const summary = el("summary", { className: "reasoning-summary" }, chevronEl(), label);
  const body = el("blockquote", { className: "reasoning-body" }, initial);
  root.append(summary, body);
  if (live) {
    root.open = true;
    root.classList.add("streaming");
  }

  let rendered = initial.length;
  let sealed = false;

  return {
    root,
    append(delta: string): void {
      if (delta === "") {
        return;
      }
      body.appendChild(document.createTextNode(delta));
      rendered += delta.length;
    },
    setText(full: string): void {
      if (full.length <= rendered) {
        return;
      }
      body.appendChild(document.createTextNode(full.slice(rendered)));
      rendered = full.length;
    },
    seal(): void {
      if (sealed) {
        return;
      }
      sealed = true;
      label.textContent = "Thinking completed";
      root.classList.remove("streaming");
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
