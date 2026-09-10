// ---------------------------------------------------------------------------
// Which BATCH a folded card's row changes are collected into, and the read that
// used to decide it.
//
// `applyFoldPass` splits its queued changes in two: a "head" change runs inside
// `preserveReadingPosition` (its height delta is compensated out of the reader's
// scroll position), a "tail" change runs outside it. `collectWindowMove` picked a
// side per row from `row.offsetTop < scrollTop` — and a folded card's body is
// `block-size: 0`, so every row inside one reports 0 and answered "head" whatever
// the card's real position was. Reading it also forces the browser to render the
// subtree `content-visibility: hidden` told it to skip.
//
// Two observables, because the defect had two halves and the read alone pins only
// one of them. The READ: a window move over a folded card's body must not measure
// its rows. The ANSWER: the change must be filed under the card's own side, so a
// mutant that hardcodes "head" for a skipped row is caught rather than waved
// through. The compensation itself needs geometry a mocked scroller has none of;
// what IS observable is which side the change was filed under, because a "head"
// change runs inside `preserveReadingPosition` and a "tail" change runs after it.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi } from "vitest";
import type { Message, Session } from "./types.js";
import type { ShiftKind } from "./scroll.js";

// messages.ts's graph reads the shared DOM registry at module scope, and `byId`
// throws on a missing element.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

const store = await import("./store.js");
const messages = await import("./messages.js");
const scroll = await import("./scroll.js");
const { mountedWindow, geometrySkipped } = await import("./messages-blocks.js");
const { OVERSCAN_BLOCKS } = await import("./block-window.js");
const { KEY_ATTR } = await import("./reconcile.js");

messages.mountChatView();

/** Blocks in the folded turn: several overscan windows, so a demand range around
 *  one ordinal is a strict subset and moving the pin genuinely moves the window. */
const BLOCKS = OVERSCAN_BLOCKS * 6;

let seq = 0;

function mountChat(msgs: Message[]): string {
  const chat = `c-side-${String(++seq)}`;
  store.setSessions([
    {
      id: chat,
      name: chat,
      messages: msgs,
      message_count: msgs.length,
      has_more: false,
      thinking: false,
      working_label: "",
    },
  ] as unknown as Session[]);
  store.setActive(chat);
  store.bumpMessages(chat);
  return chat;
}

function asst(id: string, count: number): Message {
  return {
    id,
    role: "assistant",
    ts: 2,
    content: "",
    blocks: Array.from({ length: count }, (_, i) => ({
      type: "text",
      text: `block ${String(i)}`,
    })),
  } as unknown as Message;
}

function turnCard(turnID: string): HTMLElement {
  const card = (messages.activeTranscriptView() ?? document.body).querySelector<HTMLElement>(
    `:scope > [${KEY_ATTR}="${turnID}"]`,
  );
  if (card === null) {
    throw new Error(`no card for turn ${turnID}`);
  }
  return card;
}

/** Count reads of a message ROW's vertical offset until `stop()`. `offsetTop` is a
 *  configurable accessor on the prototype, so the count comes from wrapping it,
 *  calling through. The window pass runs on a later task than the call that queues
 *  it, so the patch has to outlive an await — a `try`/`finally` around one
 *  synchronous call restores it before the pass being measured has run. */
function countRowOffsetReads(): { reads: number; skipped: number; stop: () => void } {
  const desc = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetTop");
  const real = desc?.get;
  if (desc === undefined || real === undefined) {
    throw new Error("offsetTop is not an accessor on HTMLElement.prototype");
  }
  const seen = {
    reads: 0,
    skipped: 0,
    stop: (): void => {
      Object.defineProperty(HTMLElement.prototype, "offsetTop", desc);
    },
  };
  Object.defineProperty(HTMLElement.prototype, "offsetTop", {
    ...desc,
    get(this: HTMLElement): number {
      if (this.classList.contains("msg-wrap")) {
        seen.reads++;
        if (geometrySkipped(this)) {
          seen.skipped++;
        }
      }
      return real.call(this) as number;
    },
  });
  return seen;
}

/** TWO turns so the policy folds the older one, a body big enough that a demand
 *  range is a strict subset of it, and the reveal grant that gives that folded card
 *  a body WITHOUT unfolding it — the "hidden build" the boot fold pass performs. */
async function arrangeFoldedBodiedCard(): Promise<{
  chat: string;
  first: ReturnType<typeof mountedWindow>;
}> {
  const chat = mountChat([
    { id: "u-fold", role: "user", ts: 1, content: "older" } as Message,
    asst("a-fold", BLOCKS),
    { id: "u-new", role: "user", ts: 3, content: "newer" } as Message,
    asst("a-new", 2),
  ]);
  expect(turnCard("u-fold").hasAttribute("data-folded")).toBe(true);

  await messages.mountTurnBody(chat, "u-fold", 0);
  expect(turnCard("u-fold").hasAttribute("data-folded")).toBe(true);
  const first = mountedWindow("a-fold");
  expect(first).toBeDefined();

  // A row of that body is inside the skipped subtree, or either case below would
  // pass for a row nothing guards.
  const rows = [
    ...turnCard("u-fold").querySelectorAll<HTMLElement>(`.msg-wrap[${KEY_ATTR}="a-fold"]`),
  ];
  expect(rows.length).toBeGreaterThan(0);
  expect(geometrySkipped(rows[0]!)).toBe(true);

  return { chat, first };
}

describe("a window move over a folded card's body", () => {
  it("moves the window without measuring a row the page is not rendering", async () => {
    const { chat, first } = await arrangeFoldedBodiedCard();

    // Move the pin deep into the body: a new wanted range, so this pass has a
    // window MOVE to collect for a card that is still folded.
    const moved = countRowOffsetReads();
    try {
      await messages.mountTurnBody(chat, "u-fold", BLOCKS - 1);
      store.bumpMessages(chat, "shape");
    } finally {
      moved.stop();
    }

    expect(mountedWindow("a-fold")).not.toEqual(first);
    expect(turnCard("u-fold").hasAttribute("data-folded")).toBe(true);
    expect(moved.skipped).toBe(0);
  });

  it("files the move under the card's own side, not the folded body's", async () => {
    const { chat, first } = await arrangeFoldedBodiedCard();

    // The card reports a non-negative offsetTop against a mocked scroller at 0, so
    // its own side is "tail" and its changes run AFTER the compensated batch. The
    // defect read the row instead, which answers "head" and runs them inside it.
    const from = (): number | undefined => mountedWindow("a-fold")?.from;
    let batches = 0;
    let movedInside = false;
    vi.mocked(scroll.preserveReadingPosition).mockImplementation(
      (mutate: () => void, kind: ShiftKind) => {
        if (kind !== "content-growth") {
          mutate();
          return;
        }
        batches++;
        const before = from();
        mutate();
        if (from() !== before) {
          movedInside = true;
        }
      },
    );

    await messages.mountTurnBody(chat, "u-fold", BLOCKS - 1);
    store.bumpMessages(chat, "shape");

    expect(mountedWindow("a-fold")).not.toEqual(first);
    // The batch ran, so a "head" filing would have been observed in it.
    expect(batches).toBeGreaterThan(0);
    expect(movedInside).toBe(false);
  });
});
