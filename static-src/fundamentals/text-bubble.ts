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
  /** Replace-to-full: render only the tail beyond what's rendered. The twin of
   *  ReasoningView.setText, and the same reason — it lets a caller holding the
   *  block's whole text bring a bubble up to date without knowing how much of it
   *  already landed. A no-op when nothing grew, so a settled block costs one
   *  integer comparison. */
  setText(full: string): void;
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
  let text = "";

  /** Append `delta`, opening the incremental renderer when there is not one yet.
   *
   *  Two callers reach the no-stream branch and they want the same thing. A live
   *  bubble's first delta arrives with nothing rendered. A REPLAY bubble that
   *  grows arrives with its text already rendered one-shot, which leaves no
   *  parser state to append to — so it re-renders the accumulated text through a
   *  fresh stream once, then appends normally from there. `flush` is what keeps
   *  that swap invisible: `wasRendered` says the text was on screen a moment ago,
   *  and without the flush the bubble sits blank for the stream's 200ms buffer
   *  having just thrown away what the reader was looking at.
   *
   *  The replay branch runs when the caller's live/replay judgement was wrong
   *  about this block, and a bubble frozen at its first chunk is what it
   *  replaces. */
  const write = (delta: string): void => {
    if (delta === "") {
      return;
    }
    text += delta;
    if (stream !== null) {
      stream.writeDelta(delta);
      return;
    }
    const wasRendered = root.firstChild !== null;
    root.replaceChildren();
    stream = createMarkdownStream(root);
    stream.writeDelta(text);
    if (wasRendered) {
      stream.flush();
    }
  };

  if (live) {
    root.classList.add("streaming");
    write(initial);
  } else {
    text = initial;
    if (initial !== "") {
      renderMarkdownInto(root, initial);
    }
  }

  return {
    root,
    append: write,
    setText(full: string): void {
      if (full.length <= text.length) {
        return;
      }
      write(full.slice(text.length));
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
