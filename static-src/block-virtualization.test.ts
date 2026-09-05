// ---------------------------------------------------------------------------
// Residency measured in BLOCKS: what a paint mounts inside ONE long message.
//
// A turn-id set cannot refuse part of one message, so a cold load of a 700-block
// turn built all 700. The plan names a RANGE now, and the ordinals outside it are
// ABSENT from the DOM rather than unpainted. The real `scroll.ts` is attached, not
// the shared mock, whose scroller has no geometry to read.
// ---------------------------------------------------------------------------

import { describe, it, expect, afterAll, beforeAll, beforeEach, vi } from "vitest";
import type { Message, Session } from "./types.js";
import type { Turn } from "./turns.js";

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

const { RESIDENT_BLOCKS } = await import("./block-window.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { mountChatView, mountTurnBody, activeTranscriptView } = await import("./messages.js");
const { KEY_ATTR } = await import("./reconcile.js");
const { projectTurns } = await import("./turns.js");
const { forgetHeights, recordRowHeight, spacerHeight } = await import("./block-heights.js");
const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");

/** Blocks in the fixture message: comfortably past the measured 580 and past the
 *  block budget, so a window is the only way it can mount. */
const HUGE = 700;

/** `messages.ts`' own per-slice cap, restated here because the module keeps it
 *  private and the frame assertion below is about that number. */
const BUILD_BATCH_BLOCKS = 32;

let seq = 0;
function chatID(): string {
  seq++;
  return `c-virt-${String(seq)}`;
}

/** One turn whose single assistant message holds `blocks` text blocks. */
function hugeTurn(id: string, blocks: number): Message[] {
  return [
    { id, role: "user", ts: 1, content: `prompt ${id}` } as Message,
    {
      id: `${id}-a`,
      role: "assistant",
      ts: 2,
      content: "",
      blocks: Array.from({ length: blocks }, (_, i) => ({
        type: "text",
        text: `chunk ${String(i)}`,
      })),
    } as unknown as Message,
  ];
}

/** One turn whose body is `rows` separate one-block messages, so `.turn-body`'s
 *  own flex `gap` separates them and a spacer standing in for the lot has to
 *  carry those gaps. */
function rowTurn(id: string, rows: number): Message[] {
  const out: Message[] = [{ id, role: "user", ts: 1, content: `prompt ${id}` } as Message];
  for (let i = 0; i < rows; i++) {
    out.push({
      id: `${id}-r${String(i)}`,
      role: "assistant",
      ts: 2,
      content: "",
      blocks: [{ type: "text", text: `row ${String(i)}` }],
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
  return activeTranscriptView() ?? document.getElementById("messages")!;
}

function card(turnID: string): HTMLElement {
  for (const child of root().children) {
    if (child.getAttribute(KEY_ATTR) === turnID) {
      return child as HTMLElement;
    }
  }
  throw new Error(`no card for turn ${turnID}`);
}

/** The block indices this turn's body actually holds, ascending. */
function mountedIndices(turnID: string): number[] {
  return [...card(turnID).querySelectorAll<HTMLElement>("[data-block-index]")]
    .map((e) => Number(e.dataset["blockIndex"]))
    .sort((a, b) => a - b);
}

/** This turn's mounted MESSAGE rows, in document order. */
function bodyRows(turnID: string): HTMLElement[] {
  return [
    ...card(turnID).querySelectorAll<HTMLElement>(`:scope > .turn-body > .msg-wrap[${KEY_ATTR}]`),
  ];
}

beforeEach(() => {
  mountChatView();
  localStorage.clear();
  setSessions([] as unknown as Session[]);
  setActive("");
});

describe("a cold load of a 700-block turn mounts a WINDOW", () => {
  it("bounds the mounted block count by the budget, far below the turn's own", async () => {
    const id = chatID();
    activate(id, hugeTurn("big", HUGE));
    await vi.waitFor(() => {
      expect(mountedIndices("big").length).toBeGreaterThan(RESIDENT_BLOCKS / 2);
    });
    const mounted = mountedIndices("big");
    expect(mounted.length).toBeLessThanOrEqual(RESIDENT_BLOCKS);
    expect(mounted.length).toBeLessThan(HUGE / 2);
  });

  it("leaves the blocks OUTSIDE the window absent from the DOM, by query", async () => {
    const id = chatID();
    activate(id, hugeTurn("big", HUGE));
    await vi.waitFor(() => {
      expect(mountedIndices("big").length).toBeGreaterThan(RESIDENT_BLOCKS / 2);
    });
    const mounted = mountedIndices("big");
    const first = mounted[0] ?? 0;
    // Absence by QUERY, not by visibility: the head of the message is not in the
    // document at all, so no `content-visibility` rule is doing this work.
    expect(first).toBeGreaterThan(0);
    for (const i of [0, 1, Math.floor(first / 2), first - 1]) {
      expect(
        card("big").querySelector(`[data-block-index="${String(i)}"]`),
        `block ${String(i)}`,
      ).toBeNull();
    }
    // And the window is one contiguous run ending at the live edge.
    expect(mounted.at(-1)).toBe(HUGE - 1);
    expect(mounted).toEqual(Array.from({ length: mounted.length }, (_, k) => first + k));
  });

  it("takes at most ONE slice on the paint frame, even where the window is one row", async () => {
    const id = chatID();
    activate(id, hugeTurn("big", HUGE));
    // The window is 320 ordinals of ONE message, so a builder that could only cut
    // between rows would mount all of them in the paint that created the card —
    // the frame the yielded builder exists to protect.
    expect(mountedIndices("big").length).toBeLessThanOrEqual(BUILD_BATCH_BLOCKS);
    // Nothing is lost: the drain finishes the window off the frame.
    await vi.waitFor(() => {
      expect(mountedIndices("big").length).toBeGreaterThan(BUILD_BATCH_BLOCKS);
    });
  });

  it("stands the unmounted ordinals behind a keyed spacer, so the body keeps its height", async () => {
    const id = chatID();
    activate(id, hugeTurn("big", HUGE));
    await vi.waitFor(() => {
      expect(mountedIndices("big").length).toBeGreaterThan(RESIDENT_BLOCKS / 2);
    });
    const spacer = card("big").querySelector<HTMLElement>(":scope > .turn-body > .turn-space");
    expect(spacer).not.toBeNull();
    expect(spacer?.getAttribute(KEY_ATTR)).toBe("__space_head__");
    // Real geometry: the spacer stands in for ~380 dropped ordinals, so it is
    // taller than one row rather than a zero-height placeholder.
    expect(spacer?.getBoundingClientRect().height ?? 0).toBeGreaterThan(100);
  });

  it("settles a demand build whose grant reaches BELOW the mounted window", async () => {
    const id = chatID();
    activate(id, hugeTurn("big", HUGE));
    await vi.waitFor(() => {
      expect(mountedIndices("big").length).toBeGreaterThan(RESIDENT_BLOCKS / 2);
    });
    // A rail jump calls this on a turn that already HAS a body, and a caller that
    // named no ordinal asks for ordinal 0 — so the grant is the message's head,
    // which no slice can mount until positional insertion lands. The build has to
    // give up rather than spin: a wedged loop holds `hasPendingBuild` true for the
    // turn's lifetime, and its yields starve the timer queue, so a spin takes the
    // whole run down instead of reporting here.
    const settled = await Promise.race([
      mountTurnBody(id, "big").then(() => "settled"),
      new Promise((resolve) => {
        setTimeout(() => resolve("spinning"), 500);
      }),
    ]);
    expect(settled).toBe("settled");
  });
});

describe("unmounted ordinals hold the height their rows occupied", () => {
  let style: HTMLStyleElement;

  // The shipped stylesheet, so the gap `.turn-body` declares is MEASURED here
  // rather than restated. Scoped to this block: the cases above assert DOM
  // presence and want no cascade at all.
  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  it("prices a spacer at the run its rows occupy, the parent's gaps included", async () => {
    const id = chatID();
    const messages = rowTurn("gaps", 4);
    forgetHeights(messages.map((m) => m.id));
    activate(id, messages);
    await vi.waitFor(() => {
      expect(bodyRows("gaps")).toHaveLength(4);
    });
    const rows = bodyRows("gaps");
    for (const row of rows) {
      recordRowHeight(row.getAttribute(KEY_ATTR) ?? "", row.offsetHeight);
    }
    const first = rows[0];
    const second = rows[1];
    const gap = (second?.offsetTop ?? 0) - ((first?.offsetTop ?? 0) + (first?.offsetHeight ?? 0));
    // Or the case proves nothing: a parent adding no gap loses none.
    expect(gap).toBeGreaterThan(0);
    const t = projectTurns(messages, false).find((x) => x.id === "gaps");
    expect(t).not.toBeUndefined();
    const own = rows.reduce((sum, row) => sum + row.offsetHeight, 0);
    expect(spacerHeight(t as Turn, { from: 0, to: 0 }, "tail")).toBe(own + 3 * gap);
  });
});
