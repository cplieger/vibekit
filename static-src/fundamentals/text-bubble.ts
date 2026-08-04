// ---------------------------------------------------------------------------
// Fundamental: the assistant markdown bubble (streaming or replay).
//
// A pure view. The bubble owns its incremental markdown stream; the
// composition layer forwards deltas and calls end() at turn finalize.
//
// `.message.assistant` is no longer a bubble in the chat-bubble sense — it is
// the turn body's PROSE container, and its ~40rem cap is the reading measure
// (evidence — diffs, tool cards, output — sits beside it uncapped).
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createMarkdownStream, renderMarkdownInto, type MarkdownStream } from "../markdown.js";

/** A mounted assistant text bubble plus its streaming handle. */
export interface AssistantBubble {
  /** The `.message.assistant` element to insert into the DOM. */
  readonly root: HTMLDivElement;
  /** Feed a streamed markdown delta through the incremental parser. */
  append(delta: string): void;
  /** Flush + finalize the markdown stream and drop the streaming pulse. */
  end(): void;
}

/**
 * Build an assistant markdown bubble. `live` primes an incremental stream and
 * adds the streaming pulse; replay renders the full markdown one-shot.
 */
export function buildAssistantBubble(initial: string, live: boolean): AssistantBubble {
  const root = el("div", { className: "message assistant" }) as HTMLDivElement;
  let stream: MarkdownStream | null = null;
  const ensureStream = (): MarkdownStream => {
    stream ??= createMarkdownStream(root);
    return stream;
  };

  if (initial !== "") {
    if (live) {
      ensureStream().writeDelta(initial);
    } else {
      renderMarkdownInto(root, initial);
    }
  }
  if (live) {
    root.classList.add("streaming");
  }

  return {
    root,
    append(delta: string): void {
      if (delta !== "") {
        ensureStream().writeDelta(delta);
      }
    },
    end(): void {
      stream?.end();
      stream = null;
      root.classList.remove("streaming");
    },
  };
}

// buildUserBubble is GONE with the bubbles. The user's request is the turn
// card's tinted header band now (fundamentals/turn-header.ts), which owns the
// same plain-text + path-linkification rendering plus the three-line clamp a
// free-standing bubble had no reason to want.
