// ---------------------------------------------------------------------------
// The residency ANCHOR under a reader's own scroll: one gesture, one window move.
//
// The window is grown around where the reader is, and where the reader is comes off
// the scroll position — so a pass that WRITES the scroll position is feeding its own
// input. `block-virtualization.test.ts` covers what one drag mounts on a single
// long turn; what is here is the feedback path, which needs a turn of SEVERAL
// messages (a message's ordinals do not start at zero) and several turns the window
// has to choose between.
//
// Real `scroll.ts`, the shipped stylesheet and a sized scrollport, because every
// assertion is a measurement: the shared mock's scroller has no geometry and its
// `onViewportChange` never fires, so no window pass runs under it at all.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";
import type { Message, Session } from "./types.js";

// NESTED as the shipped page nests them: `#messages-wrap` is `position: absolute`
// inside the outer wrapper, so it is the `offsetParent` of the whole transcript AND
// the scroller. Flat siblings measure every card against the body instead, which is
// the anchor ladder's coordinates in the wrong space.
for (const id of [
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
const scrollerEl = document.createElement("div");
scrollerEl.id = "messages-wrap";
document.getElementById("messages-wrap-outer")?.appendChild(scrollerEl);
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
scrollerEl.appendChild(messagesEl);

const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { mountChatView, activeTranscriptView } = await import("./messages.js");
const { scrollToBottom } = await import("./scroll.js");
const { setTurnOpen, resetFoldState } = await import("./fold-state.js");
const { KEY_ATTR } = await import("./reconcile.js");
const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");

/** Long enough to WRAP, so a mounted block measures several times the per-block
 *  estimate the spacer above it priced. One line of prose per block and the head's
 *  growth is too small for a compensation to be observable at all. */
const LONG = "the quick brown fox jumps over the lazy dog and keeps going ".repeat(6);

/** Rows per turn: several messages, because a message's ordinals start at its own base
 *  in the turn and only the first message's base is zero. */
const ROWS = 10;

/** Blocks per row in the two fixtures, which is what decides where the window's EDGE
 *  lands. SMALL makes a turn the window can hold whole, so its edges land on turn
 *  BOUNDARIES and a crossing is a handful of gestures away. BIG makes one LARGER than
 *  the whole budget, so both edges sit inside the card the reader is in — the only
 *  state in which a move can extend that card's tail underneath them. */
const SMALL_PER_ROW = 20;
const BIG_PER_ROW = 40;

/** One reader gesture. Big enough that the state a case needs is a handful of them
 *  away, and small enough to be a plausible wheel step rather than a jump. */
const STEP_PX = 2400;

let seq = 0;
function chatID(): string {
  seq++;
  return `c-anchor-${String(seq)}`;
}

/** One turn whose body is `ROWS` assistant MESSAGES of `PER_ROW` wrapping blocks.
 *
 *  Several messages is the whole point: a message's ordinals start at its own base in
 *  the turn, and only the first message's base is zero. A one-message fixture cannot
 *  tell a turn ordinal from a message-local block index. */
function heavyTurn(id: string, perRow: number): Message[] {
  const out: Message[] = [{ id, role: "user", ts: 1, content: `prompt ${id}` } as Message];
  for (let r = 0; r < ROWS; r++) {
    out.push({
      id: `${id}-a${String(r)}`,
      role: "assistant",
      ts: 2,
      content: "",
      blocks: Array.from({ length: perRow }, (_, i) => ({
        type: "text",
        text: `row ${String(r)} chunk ${String(i)}: ${LONG}`,
      })),
    } as unknown as Message);
  }
  return out;
}

function activate(chat: string, messages: Message[]): void {
  setSessions([
    {
      id: chat,
      name: "c",
      messages,
      message_count: messages.length,
      has_more: false,
      thinking: false,
      working_label: "",
    },
  ] as unknown as Session[]);
  setActive(chat);
  bumpMessages(chat);
}

function root(): HTMLElement {
  return activeTranscriptView() ?? messagesEl;
}

function scroller(): HTMLElement {
  return document.getElementById("messages-wrap")!;
}

function frame(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => resolve());
  });
}

/** One comparable reading of what the whole transcript holds: which ordinals are
 *  mounted per card, and what each spacer reserves. This is the pass's own output, so
 *  two readings that differ mean another pass ran between them. */
function residency(): string {
  const parts: string[] = [];
  for (const c of root().children) {
    const id = c.getAttribute(KEY_ATTR);
    if (id === null) {
      continue;
    }
    const idx = [...c.querySelectorAll<HTMLElement>("[data-block-index]")]
      .map((e) => `${e.dataset["blockMsg"] ?? ""}#${e.dataset["blockIndex"] ?? ""}`)
      .join(",");
    const spacers = [...c.querySelectorAll<HTMLElement>(".turn-space")]
      .map((e) => `${e.getAttribute(KEY_ATTR) ?? "?"}=${e.style.blockSize}`)
      .join("/");
    parts.push(`${id}{${idx}}[${spacers}]`);
  }
  return parts.join(" ");
}

/** The block element the viewport top currently sits in, or null when the top is over
 *  a spacer or a header. This is the reader's own position, in the space the anchor
 *  ladder reads it in. */
function blockAtViewportTop(): HTMLElement | null {
  const top = scroller().scrollTop;
  const frameTop = scroller().getBoundingClientRect().top;
  let found: HTMLElement | null = null;
  for (const e of root().querySelectorAll<HTMLElement>("[data-block-msg][data-block-index]")) {
    if (e.getBoundingClientRect().top + top - frameTop <= top) {
      found = e;
    }
  }
  return found;
}

/** The turn whose card holds the block the viewport top sits in. */
function turnAtViewportTop(): string {
  return blockAtViewportTop()?.closest(`[${KEY_ATTR}].turn`)?.getAttribute(KEY_ATTR) ?? "";
}

describe("the residency anchor under a reader's own scroll", () => {
  const VIEWPORT_PX = 720;

  beforeAll(() => {
    // Left mounted for the file's lifetime: every case in it measures real boxes, and
    // the stylesheet is what gives them any.
    mountAppCSS();
    // `#messages-wrap-outer` is `flex: 1` of a column this fixture does not build, so
    // without a height the absolutely-positioned scroller inside it is 0 tall and the
    // ladder answers the live edge for every position.
    const outer = document.getElementById("messages-wrap-outer");
    if (outer !== null) {
      outer.style.height = `${String(VIEWPORT_PX)}px`;
    }
  });

  beforeEach(() => {
    mountChatView();
    localStorage.clear();
    resetFoldState();
    setSessions([] as unknown as Session[]);
    setActive("");
  });

  /** `count` heavy turns of `perRow` blocks each, all but the newest explicitly OPEN so
   *  every one is `openable` and the window has to choose between them. Left to the
   *  fold policy only the newest would be, and then the window never moves across a
   *  turn boundary at all.
   *
   *  `perRow` is what decides where the window's EDGE lands, and the two cases below
   *  need different answers: turns the window can hold whole put its edge on turn
   *  boundaries, and turns it cannot put the edge mid-turn. */
  async function openTurns(count: number, perRow: number): Promise<void> {
    const msgs: Message[] = [];
    for (let i = 1; i <= count; i++) {
      msgs.push(...heavyTurn(`t${String(i)}`, perRow));
    }
    const chat = chatID();
    activate(chat, msgs);
    for (let i = 1; i < count; i++) {
      setTurnOpen(chat, `t${String(i)}`, true);
    }
    bumpMessages(chat, "shape");
    // The cold build yields per slice, and the window pass refuses to move a body that
    // is still filling — so every case has to let the build finish first. Two
    // consecutive agreeing readings, seeded with one no transcript can produce.
    let last = "<none>";
    await vi.waitFor(
      () => {
        const now = residency();
        const was = last;
        last = now;
        expect(now).not.toBe("");
        expect(now).toBe(was);
      },
      { timeout: 10000, interval: 60 },
    );
    // Back to the live edge, then out past the bottom pin's own settle window: a
    // gesture inside it is undone before any pass sees it.
    scrollToBottom();
    await new Promise((resolve) => {
      setTimeout(resolve, 900);
    });
  }

  /** Scroll UP by `px` as a reader does, and let the frame settle.
   *
   *  The wheel's DIRECTION is load-bearing rather than decoration: the controller
   *  enters Reading from the aim of the reader's input, and a bare positional write is
   *  the shape of the platform's own clamp, which stays Following on purpose.
   *  `behavior: "instant"`, not `scrollTop =`, because the scroller declares
   *  `scroll-behavior: smooth` and an assignment only starts an animation. */
  async function readerScrollsUp(px: number, frames = 6): Promise<number> {
    const el = scroller();
    const asked = Math.max(0, el.scrollTop - px);
    el.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
    el.scrollTo({ top: asked, behavior: "instant" });
    for (let f = 0; f < frames; f++) {
      await frame();
    }
    return asked;
  }

  /** Walk up in `STEP_PX` gestures until the viewport top has CROSSED from one turn's
   *  card into the previous one `crossings` times, and report the last such gesture.
   *
   *  The crossing is the state every case here needs: inside one turn the anchor's
   *  message is often that turn's first, whose ordinals start at zero, and a turn
   *  ordinal and a message-local block index coincide there. Walking a fixed number of
   *  steps instead would make the state a case reaches depend on the fixture's
   *  rendered height, which the font decides.
   *
   *  TWO by default, because the FIRST crossing still has the window's tail LATCHED at
   *  the transcript's end: a backward anchor error cannot retract a latched tail, so
   *  that one absorbs it and reports nothing. Measured: drift 0 at the first crossing
   *  and 5,876px at the second, with the defect present in both. */
  async function scrollUpAcrossTurnBoundaries(
    crossings = 2,
  ): Promise<{ asked: number; landed: number }> {
    let from = turnAtViewportTop();
    expect(from, "the viewport top must start inside a turn's mounted block").not.toBe("");
    let seen = 0;
    for (let s = 0; s < 40; s++) {
      const asked = await readerScrollsUp(STEP_PX);
      const now = turnAtViewportTop();
      if (now !== from && now !== "") {
        from = now;
        seen++;
        if (seen === crossings) {
          return { asked, landed: scroller().scrollTop };
        }
      }
      if (scroller().scrollTop === 0) {
        break;
      }
    }
    throw new Error(
      `the walk crossed ${String(seen)} turn boundaries, wanted ${String(crossings)}`,
    );
  }

  /** Walk up until the reader is INSIDE a card whose own window is partial at the tail.
   *
   *  That is the one state in which a window move extends a card's tail while the reader
   *  sits in it, which is the work the compensation attributes to the content above them.
   *  A card the window holds whole cannot produce it, and neither can one the reader is
   *  not in — so a step count cannot name the state and the condition has to. */
  async function readerReachesRetractingTail(): Promise<void> {
    for (let s = 0; s < 40; s++) {
      const card = blockAtViewportTop()?.closest(`[${KEY_ATTR}].turn`);
      if (card?.querySelector(`.turn-space[${KEY_ATTR}="__space_tail__"]`) != null) {
        return;
      }
      if (scroller().scrollTop === 0) {
        break;
      }
      await readerScrollsUp(STEP_PX / 2, 3);
    }
    throw new Error("the walk never put the reader in a card with a tail spacer");
  }

  it("moves the reader by what they asked when their scroll crosses into an earlier turn", async () => {
    // Turns the window can hold WHOLE, so its edges land on turn boundaries and a
    // crossing is a handful of gestures away.
    await openTurns(6, SMALL_PER_ROW);

    const { asked, landed } = await scrollUpAcrossTurnBoundaries();

    // The crossing is where the anchor's message is a turn's LAST rather than its
    // first, so the message's base and the message-local block index differ. Read in
    // the wrong space the anchor lands `base` ordinals early, the window recentres
    // itself a turn's worth backwards, and the head it mounts above the reader is paid
    // for out of their scroll position: measured on this fixture, one gesture upward
    // moved them 5,875px DOWN. The bound is the gesture itself, so a correction can
    // never exceed the travel it is correcting.
    expect(Math.abs(landed - asked)).toBeLessThan(STEP_PX);
  }, 90000);

  it("does not schedule a window pass from the scroll the last pass wrote", async () => {
    // Turns LARGER than the whole budget, so the window sits inside one card and both
    // its edges are in the card the reader is in — which is where a move extends that
    // card's tail underneath them and the compensation carries a correction for content
    // below the reader.
    await openTurns(3, BIG_PER_ROW);
    await readerReachesRetractingTail();

    // SCROLL EVENTS, not residency readings: the loop's own edge is a write to
    // `scrollTop`, so counting the writes measures the edge directly rather than what
    // it did to the window. One reader gesture may produce two — the reader's own, and
    // the pass's one compensation.
    const el = scroller();
    const writes: number[] = [];
    const onScroll = (): void => {
      writes.push(Math.round(el.scrollTop));
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    const gestures = 4;
    for (let g = 0; g < gestures; g++) {
      await readerScrollsUp(600);
    }
    // Then twenty-four frames with NO input at all. A loop that feeds itself writes about
    // once a frame and does not settle when the reader stops, so the idle frames are what
    // separate a pass correcting the reader once from a pass chasing its own correction.
    // Twenty-four rather than ninety because every frame here is a real one: the longer
    // wait made this case the slowest in the file and timed it out under a loaded suite.
    for (let f = 0; f < 24; f++) {
      await frame();
    }
    el.removeEventListener("scroll", onScroll);

    // With the pass hanging off every scroll frame rather than off a reader gesture, this
    // is what the writes look like: sixteen for four gestures, alternating over the same
    // few hundred pixels, because each compensation re-planned the window from the
    // position it had just corrected.
    expect(writes.length, `scroll writes: ${writes.join(",")}`).toBeLessThanOrEqual(2 * gestures);
  }, 90000);
});
