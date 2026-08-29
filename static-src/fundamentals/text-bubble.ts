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
export interface AssistantBubbleOpts {
  /** Fired exactly once, when the bubble seals — the moment `.streaming` (the
   *  wash and the caret) drops. The live-anchor registry hangs off this: the
   *  composition layer registers a top-level live bubble at mount and must
   *  clear it at the same moment the class goes, whichever path sealed it
   *  (tail moved, turn finalized, unmount). */
  onSeal?: (root: HTMLElement) => void;
  /** Reports whether the bubble is BLANK — nothing to show and not streaming —
   *  once with the initial state during build, then on every transition. The
   *  row wrapper hides on it (`.msg-row.is-empty`, css/13-messages.css): a
   *  reserved block slot must keep its DOM position but cost no row until its
   *  text arrives, while a live bubble stays visible to carry its caret. */
  onBlankChange?: (blank: boolean) => void;
}

export function buildAssistantBubble(
  initial: string,
  live: boolean,
  opts?: AssistantBubbleOpts,
): AssistantBubble {
  const root = el("div", { className: "message assistant" }) as HTMLDivElement;
  let stream: MarkdownStream | null = null;
  let text = ""; // what the parser has been given
  let targetLen = 0; // length of the full text known, revealed or not
  let ending = false;

  /** Blank = nothing to show and not streaming. Initialized after the initial
   *  render below; write/seal fire transitions through onBlankChange. */
  let blank = false;
  const setBlank = (b: boolean): void => {
    if (b !== blank) {
      blank = b;
      opts?.onBlankChange?.(b);
    }
  };

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
    // Accepted text is committed to appear (the stream flushes it), so the row
    // un-hides now rather than at the stream's own flush.
    setBlank(false);
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
   *  caret go with it. Idempotent; `onSeal` fires on the first call only. */
  let sealed = false;
  const seal = (): void => {
    stream?.end();
    stream = null;
    root.classList.remove("streaming");
    // The wash is gone, so an empty bubble is a blank one from here on (a
    // reserved slot whose text never arrived).
    setBlank(root.firstChild === null);
    if (!sealed) {
      sealed = true;
      opts?.onSeal?.(root);
    }
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
    targetLen = initial.length;
    write(initial);
  } else {
    text = initial;
    targetLen = initial.length;
    if (initial !== "") {
      renderMarkdownInto(root, initial);
    }
  }

  // The initial report. DOM truth rather than `initial === ""`: whitespace-only
  // markdown renders no node, and the row keys on what is visible.
  blank = !live && root.firstChild === null;
  opts?.onBlankChange?.(blank);

  /** Take `delta` as pure growth past everything known so far. */
  const feed = (delta: string): void => {
    targetLen += delta.length;
    if (reveal === null) {
      write(delta);
      return;
    }
    reveal.append(delta);
  };

  /** Take `full` as the text now known. Growth only; shorter is ignored. */
  const grow = (full: string): void => {
    if (full.length <= targetLen) {
      return;
    }
    // The caller's stream is append-only, so the tail past `targetLen` is the
    // whole growth — which is what makes a resync from full text and a
    // streamed delta one path.
    feed(full.slice(targetLen));
  };

  return {
    root,
    append(delta: string): void {
      if (delta === "") {
        return;
      }
      feed(delta);
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
