// ---------------------------------------------------------------------------
// Fundamental: text bubbles — the user prompt bubble and the assistant
// markdown bubble (streaming or replay).
//
// Pure views. The assistant bubble owns its incremental markdown stream; the
// composition layer forwards deltas and calls end() at turn finalize. Reuses
// the existing `.message.user` / `.message.assistant` CSS vocab.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createMarkdownStream, renderMarkdownInto, type MarkdownStream } from "../markdown.js";
import { linkifyPaths } from "../linkify.js";

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

/** Build the user prompt bubble (plain text + path linkification). */
export function buildUserBubble(text: string): HTMLDivElement {
  const bubble = el("div", { className: "message user" }, text) as HTMLDivElement;
  linkifyPaths(bubble);
  return bubble;
}
