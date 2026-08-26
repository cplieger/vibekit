// ---------------------------------------------------------------------------
// Fundamental: the assistant markdown bubble (streaming or replay).
//
// A pure view. The bubble owns its incremental markdown stream; the
// composition layer forwards deltas and calls end() at turn finalize.
//
// A LIVE bubble also owns a reveal cursor (`reveal.ts`), which decides WHEN
// text reaches the parser: the network's lumps are spread across frames at a
// rate that trails the live edge by a fixed lag, so the transcript grows
// continuously instead of five times a second. Text already present when the
// bubble mounts is history rather than a token arriving now, so it paints in one
// pass; only growth is revealed. A replay bubble has no cursor at all.
//
// `.message.assistant` is no longer a bubble in the chat-bubble sense — it is
// the turn body's PROSE container, and its ~40rem cap is the reading measure
// (evidence — diffs, tool cards, output — sits beside it uncapped).
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createMarkdownStream, renderMarkdownInto, type MarkdownStream } from "../markdown.js";
import { createReveal, type Reveal } from "../reveal.js";

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
  /** Stop the text: no more growth is coming. On a live bubble the reveal keeps
   *  running until it catches up, so the caret and the streaming wash outlive
   *  `turn_ended` by the reveal's lag — which is correct, because text is still
   *  appearing. Finalizing the markdown stream waits for that. */
  end(): void;
  /** end(), minus the wait: reveal the remainder in one write and finalize now.
   *  For a bubble being discarded (chat switch, message removed) and for any
   *  caller that needs the settled DOM synchronously. */
  finishNow(): void;
}

/**
 * Build an assistant markdown bubble. `live` primes an incremental stream plus a
 * reveal cursor and marks the bubble `.streaming` — the accent wash and the
 * blinking block caret, not a pulsing dot (css/13-messages.css). Replay renders
 * the full markdown one-shot.
 */
export function buildAssistantBubble(initial: string, live: boolean): AssistantBubble {
  const root = el("div", { className: "message assistant" }) as HTMLDivElement;
  let stream: MarkdownStream | null = null;
  let text = ""; // what the parser has been given
  let target = ""; // the full text known, revealed or not
  let ending = false;

  /** Append `delta`, opening the incremental renderer when there is not one yet.
   *
   *  Two callers reach the no-stream branch and they want the same thing. A live
   *  bubble's first delta arrives with nothing rendered. A REPLAY bubble that
   *  grows arrives with its text already rendered one-shot, which leaves no
   *  parser state to append to — so it re-renders the accumulated text through a
   *  fresh stream once, then appends normally from there. `flush` is what keeps
   *  that swap invisible: `wasRendered` says the text was on screen a moment ago,
   *  and without the flush the bubble sits blank for the stream's buffer having
   *  just thrown away what the reader was looking at.
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
    // A live bubble regulates its own cadence through the reveal, so the
    // stream's own buffering would only re-lump what the reveal spread out.
    stream = createMarkdownStream(root, live ? { flushIntervalMs: 0 } : {});
    stream.writeDelta(text);
    if (wasRendered) {
      stream.flush();
    }
  };

  /** Finalize the markdown stream and drop `.streaming` — the wash and the
   *  caret go with it. Idempotent. */
  const seal = (): void => {
    stream?.end();
    stream = null;
    root.classList.remove("streaming");
  };

  const reveal: Reveal | null = live
    ? createReveal(initial, {
        onWrite: write,
        onIdle: () => {
          if (ending) {
            seal();
          }
        },
      })
    : null;

  if (live) {
    root.classList.add("streaming");
    target = initial;
    write(initial);
  } else {
    text = initial;
    target = initial;
    if (initial !== "") {
      renderMarkdownInto(root, initial);
    }
  }

  /** Take `full` as the text now known. Growth only; shorter is ignored. */
  const grow = (full: string): void => {
    if (full.length <= target.length) {
      return;
    }
    target = full;
    if (reveal === null) {
      write(full.slice(text.length));
      return;
    }
    reveal.setText(full);
  };

  return {
    root,
    append(delta: string): void {
      if (delta === "") {
        return;
      }
      grow(target + delta);
    },
    setText: grow,
    end(): void {
      if (ending) {
        return;
      }
      ending = true;
      // Nothing left to reveal means no frame is coming to run onIdle.
      if (reveal === null || reveal.idle) {
        seal();
      }
    },
    finishNow(): void {
      ending = true;
      if (reveal === null) {
        seal();
        return;
      }
      reveal.finishNow(); // → the last write, then onIdle → seal()
    },
  };
}

// buildUserBubble is GONE with the bubbles. The user's request is the turn
// card's tinted header band now (fundamentals/turn-header.ts), which owns the
// same plain-text + path-linkification rendering plus the three-line clamp a
// free-standing bubble had no reason to want.
