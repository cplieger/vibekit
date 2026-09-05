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

// NESTED as the shipped page nests them (static/index.html): `#messages-wrap` is
// `position: absolute; inset: 0` inside the outer wrapper, so it is the
// `offsetParent` of the whole transcript AND the scroller. Flat siblings give the
// anchor ladder a scroller with no content in it and card offsets measured against
// the body, which is every coordinate in the wrong space.
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

const { RESIDENT_BLOCKS, OVERSCAN_BLOCKS } = await import("./block-window.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { mountChatView, mountTurnBody, activeTranscriptView } = await import("./messages.js");
const { scrollToBottom } = await import("./scroll.js");
const { setTurnOpen } = await import("./fold-state.js");
const { KEY_ATTR } = await import("./reconcile.js");
const { projectTurns } = await import("./turns.js");
const { forgetHeights, recordRowHeight, spacerHeight } = await import("./block-heights.js");
const { toolCallSigs, toolCallSigKey } = await import("./store-signals.js");
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

/** One turn whose single assistant message is `blocks` consecutive TOOL cards: the
 *  shape the tool budget binds on, where the window is at its narrowest. */
function toolTurn(id: string, blocks: number): Message[] {
  return [
    { id, role: "user", ts: 1, content: `prompt ${id}` } as Message,
    {
      id: `${id}-a`,
      role: "assistant",
      ts: 2,
      content: "",
      blocks: Array.from({ length: blocks }, (_, i) => ({
        type: "tool_use",
        tool_call_id: `${id}-tc${String(i)}`,
      })),
      tool_calls: Array.from({ length: blocks }, (_, i) => ({
        id: `${id}-tc${String(i)}`,
        title: "Run Command",
        kind: "execute",
        status: "completed",
      })),
    } as unknown as Message,
  ];
}

/** A huge turn whose FIRST block is a todo list: the block kind whose mount arms an
 *  effect of its own, so a drop that fails to drain it leaves a signal subscribed to
 *  a detached list. */
function todoHeadTurn(id: string, blocks: number): Message[] {
  const messages = hugeTurn(id, blocks);
  const m = messages[1] as unknown as {
    blocks: Record<string, unknown>[];
    tool_calls: Record<string, unknown>[];
  };
  m.blocks[0] = { type: "tool_use", tool_call_id: `${id}-todo` };
  m.tool_calls = [
    {
      id: `${id}-todo`,
      title: "todo_list",
      kind: "other",
      status: "completed",
      input: { todos: [{ content: "first", status: "completed" }] },
    },
  ];
  return messages;
}

/** A huge turn whose first two blocks are a DELEGATE BOX: its invocation, then one
 *  block of the delegate's own prose inside it. A box, unlike a tool card, has a
 *  registry key — so what a drop has to carry here is the key's meaning at re-mount.
 */
function subagentHeadTurn(id: string, blocks: number): Message[] {
  const messages = hugeTurn(id, blocks);
  const m = messages[1] as unknown as {
    blocks: Record<string, unknown>[];
    tool_calls: Record<string, unknown>[];
  };
  m.blocks[0] = { type: "tool_use", tool_call_id: `${id}-inv`, agent_subtask_id: `${id}-sub` };
  m.blocks[1] = { type: "text", text: "delegate prose", agent_subtask_id: `${id}-sub` };
  m.tool_calls = [
    {
      id: `${id}-inv`,
      title: "Sub-agent: general-task-execution",
      kind: "other",
      status: "completed",
      agent_subtask_id: `${id}-sub`,
    },
  ];
  return messages;
}

/** `toolTurn` with OUTPUT on its first call, so that card has something to reveal and
 *  therefore a disclosure control at all. */
function outputHeadTurn(id: string, blocks: number): Message[] {
  const messages = toolTurn(id, blocks);
  const m = messages[1] as unknown as { tool_calls: Record<string, unknown>[] };
  const first = m.tool_calls[0];
  if (first !== undefined) {
    first["output"] = "line one\nline two";
  }
  return messages;
}

/** One turn whose body is `rows` assistant MESSAGES of `per` blocks each, summing past
 *  the block budget — the only shape in which a window move crosses a ROW boundary.
 *
 *  Every other fixture here is one user message and one assistant message, so its window
 *  moves inside a single body row and the row axis never runs: the rows reconcile,
 *  `placeRow`'s insert-above discipline, the spacer's row-gap term and `recordRowHeight`'s
 *  only production caller (a row LEAVING a body) all need two rows to be reachable. The
 *  server produces this on any turn a mid-turn model switch or a step fold splits. */
function multiRowTurn(id: string, rows: number, per: number): Message[] {
  const out: Message[] = [{ id, role: "user", ts: 1, content: `prompt ${id}` } as Message];
  for (let r = 0; r < rows; r++) {
    out.push({
      id: `${id}-a${String(r)}`,
      role: "assistant",
      ts: 2,
      content: "",
      blocks: Array.from({ length: per }, (_, i) => ({
        type: "text",
        // Long enough to WRAP, so a real row measures well past the per-block estimate
        // and the height a departing row records is worth something the document can be
        // short by. One line of prose per block and the difference is unobservable.
        text: `row ${String(r)} chunk ${String(i)}: ${"the quick brown fox jumps over the lazy dog and keeps going ".repeat(6)}`,
      })),
    } as unknown as Message);
  }
  return out;
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

// ---------------------------------------------------------------------------
// Scrolling moves the window, and every case below needs REAL geometry: the
// shipped stylesheet, a sized scrollport, and the real `scroll.ts` listener the
// window pass hangs off. The shared mock cannot serve any of it — its scroller is
// a detached div and its `onViewportChange` never fires.
// ---------------------------------------------------------------------------

describe("scrolling moves the window", () => {
  let style: HTMLStyleElement;

  /** The scrollport's own height. */
  const VIEWPORT_PX = 720;

  beforeAll(() => {
    style = mountAppCSS();
    // `#messages-wrap-outer` is `flex: 1` of a column this fixture does not build,
    // so without a height the absolutely-positioned scroller inside it is 0 tall
    // and the ladder answers the live edge for every position.
    const outer = document.getElementById("messages-wrap-outer");
    if (outer !== null) {
      outer.style.height = `${String(VIEWPORT_PX)}px`;
    }
  });

  afterAll(() => {
    style.remove();
  });

  function scroller(): HTMLElement {
    return document.getElementById("messages-wrap")!;
  }

  function frame(): Promise<void> {
    return new Promise((resolve) => {
      requestAnimationFrame(() => resolve());
    });
  }

  /** Wait until the cold build has FINISHED: two consecutive polls agreeing on the
   *  mounted count. The window pass refuses to move a body that is still filling, so
   *  a case that scrolls has to let the builder finish first. */
  async function settled(turnID: string, least = 2 * OVERSCAN_BLOCKS): Promise<void> {
    let last = -1;
    await vi.waitFor(() => {
      const n = mountedIndices(turnID).length;
      const was = last;
      last = n;
      expect(n).toBeGreaterThan(least);
      expect(n).toBe(was);
    });
  }

  /** Put the reader back on the live edge and wait out the bottom pin's own settle
   *  window: a gesture inside it is undone before any pass sees it.
   *
   *  The re-pin is not ceremony. `content-visibility: auto` shrinks the document as
   *  rows swap their declared `contain-intrinsic-size` for a real height, with no
   *  mutation and no controller write; the browser clamps `scrollTop` itself, the
   *  next build slice grows the page again, and the listener reads that clamp as a
   *  reader gesture. So a heavy cold load can end up Reading, partway up. */
  async function atLiveEdge(): Promise<void> {
    scrollToBottom();
    await new Promise((resolve) => {
      setTimeout(resolve, 800);
    });
  }

  /** Drag the scrollbar to `top`, and report how far the reader actually moved.
   *
   *  A drag is a READER gesture, which is what the ladder is for; a teleport by
   *  navigation goes through the demand pin instead. `behavior: "instant"`, not
   *  `scrollTop =`: the scroller declares `scroll-behavior: smooth`
   *  (css/13-messages.css), so a plain assignment starts an ANIMATION and the value
   *  does not change on the line that sets it. Measured immediately, so the number is
   *  the reader's own travel and excludes every later compensation. */
  async function dragTo(top: number): Promise<number> {
    const el = scroller();
    const was = el.scrollTop;
    el.scrollTo({ top: Math.max(0, top), behavior: "instant" });
    const moved = el.scrollTop - was;
    for (let f = 0; f < 4; f++) {
      await frame();
    }
    return moved;
  }

  /** Wait until the window stops MOVING: two consecutive polls agreeing on the
   *  ordinals mounted. Distinct from `settled`, which waits for a cold build to
   *  finish filling one window. */
  async function windowSettled(turnID: string): Promise<void> {
    // Seeded with a reading no window can produce, or an EMPTY window satisfies the
    // agreement on the first poll and the helper reports settled having seen nothing.
    let last = "<none>";
    await vi.waitFor(() => {
      const now = mountedIndices(turnID).join(",");
      const was = last;
      last = now;
      expect(now).not.toBe("");
      expect(now).toBe(was);
    });
  }

  /** The mounted block index the viewport top sits at. */
  function anchorIndex(turnID: string): number {
    const top = scroller().scrollTop;
    let at = -1;
    for (const e of card(turnID).querySelectorAll<HTMLElement>("[data-block-index]")) {
      if (e.offsetTop <= top) {
        at = Number(e.dataset["blockIndex"]);
      }
    }
    return at;
  }

  /** The block bottom of the mounted region, in the scroller's own space. */
  function mountedBottom(turnID: string): number {
    let px = 0;
    for (const e of card(turnID).querySelectorAll<HTMLElement>("[data-block-index]")) {
      px = Math.max(px, e.offsetTop + e.offsetHeight);
    }
    return px;
  }

  async function coldLoad(turnID: string, messages: Message[]): Promise<string> {
    const id = chatID();
    activate(id, messages);
    await settled(turnID);
    await atLiveEdge();
    return id;
  }

  it("mounts what the reader scrolls TO and drops what they scrolled away from", async () => {
    await coldLoad("big", hugeTurn("big", HUGE));
    const before = mountedIndices("big");
    // At the live edge the tail latches immediately and the head takes the whole
    // budget, so the window ends at the newest ordinal and the HEAD is what is absent.
    expect(before[0]).toBeGreaterThan(0);
    expect(before.at(-1)).toBe(HUGE - 1);

    await dragTo(0);
    await vi.waitFor(() => {
      expect(mountedIndices("big")[0]).toBe(0);
    });

    // Previously absent ordinals are present…
    expect(card("big").querySelector('[data-block-index="0"]')).not.toBeNull();
    const after = mountedIndices("big");
    // …and the ones the reader scrolled away from are ABSENT, by query: the window
    // MOVED rather than growing.
    expect(after.at(-1)).toBeLessThan(HUGE - 1);
    expect(card("big").querySelector(`[data-block-index="${String(HUGE - 1)}"]`)).toBeNull();
    expect(after.length).toBeLessThanOrEqual(RESIDENT_BLOCKS);
  });

  /** Every mounted ordinal's element and its viewport top, for the identity case. */
  function mountedSnapshot(turnID: string): Map<number, { el: HTMLElement; top: number }> {
    const out = new Map<number, { el: HTMLElement; top: number }>();
    for (const e of card(turnID).querySelectorAll<HTMLElement>("[data-block-index]")) {
      out.set(Number(e.dataset["blockIndex"]), { el: e, top: e.getBoundingClientRect().top });
    }
    return out;
  }

  it("keeps every ordinal the window KEPT on the same node, and the reader near it", async () => {
    await coldLoad("big", hugeTurn("big", HUGE));
    await dragTo(0);
    await vi.waitFor(() => {
      expect(mountedIndices("big")[0]).toBe(0);
    });

    // The OVERLAP is the subject, not one chosen ordinal, and that is what makes this
    // clamp-proof: `content-visibility: auto` re-measures with no mutation, the browser
    // clamps `scrollTop` itself, and the listener reads that clamp as a gesture — so
    // however many passes run and however far the window ends up, every ordinal present
    // both before and after has to be the identical node. Naming one ordinal in advance
    // instead makes the assertion depend on how far the churn travelled.
    const before = mountedSnapshot("big");
    const wasTop = scroller().scrollTop;
    const to = mountedIndices("big").at(-1) ?? 0;

    // Down into the tail spacer, which extends the tail and retracts the head.
    await dragTo(mountedBottom("big"));
    await vi.waitFor(() => {
      expect(mountedIndices("big").at(-1)).toBeGreaterThan(to);
    });
    await windowSettled("big");

    const after = mountedSnapshot("big");
    const kept = [...after.keys()].filter((i) => before.has(i)).sort((a, b) => a - b);
    // Or the identity assertion is vacuous: a move that kept nothing proves nothing about
    // rebuilding. The plan's own overscan floor guarantees this much overlap.
    expect(kept.length).toBeGreaterThanOrEqual(OVERSCAN_BLOCKS);
    for (const i of kept) {
      // A row the window still wants is neither rebuilt nor re-created, which is what
      // positional insertion buys and what keeps a selection and a reader-set disclosure
      // inside it.
      expect(after.get(i)?.el, `ordinal ${String(i)}`).toBe(before.get(i)?.el);
    }

    // And the reader followed their own travel rather than the window's height change.
    // Measured against the TOTAL `scrollTop` delta rather than one drag's, so every
    // compensation the convergence wrote is accounted for: a kept node's viewport top
    // must move exactly opposite to the scroller, and the residue is uncompensated
    // document above it. A BOUND on that residue, not zero — the head batch also grows
    // the body's tail, so a pass's `scrollHeight` delta is not purely above the reader.
    const travelled = scroller().scrollTop - wasTop;
    const mid = kept[Math.floor(kept.length / 2)] ?? 0;
    const drift = (after.get(mid)?.top ?? 0) - (before.get(mid)?.top ?? 0) + travelled;
    expect(Math.abs(drift)).toBeLessThan(0.25 * Math.abs(travelled));
  });

  it("keeps the reader's position addressable when the tail's window empties", async () => {
    await coldLoad("big", hugeTurn("big", HUGE));
    await dragTo(0);
    await vi.waitFor(() => {
      expect(mountedIndices("big")[0]).toBe(0);
    });
    expect(mountedIndices("big").at(-1)).toBeLessThan(HUGE - 1);

    // The ordinals the drop took still hold their height, so the document is long
    // enough to hold the reader where they are — the resume chip's own promise.
    expect(scroller().scrollHeight - scroller().clientHeight).toBeGreaterThanOrEqual(
      scroller().scrollTop,
    );
    expect(scroller().scrollHeight).toBeGreaterThan(10 * VIEWPORT_PX);
  });

  it("settles one drag into ONE window move, with no oscillation", async () => {
    await coldLoad("big", hugeTurn("big", HUGE));
    await dragTo(0);
    await vi.waitFor(() => {
      expect(mountedIndices("big")[0]).toBe(0);
    });

    const first = mountedIndices("big").join(",");
    const landed = scroller().scrollTop;
    // Eight frames with no further gesture. The compensation WRITES scrollTop, which
    // emits a scroll of its own, so without the re-entrancy latch and the
    // plan-equality exit the window would keep chasing its own correction.
    for (let i = 0; i < 8; i++) {
      await frame();
    }
    expect(mountedIndices("big").join(",")).toBe(first);
    expect(scroller().scrollTop).toBeCloseTo(landed, -1);
  });

  for (const shape of ["prose", "tool_use"] as const) {
    it(`keeps one overscan mounted each side of the anchor, ${shape}`, async () => {
      // TWO fixtures, because the two budgets bind on different shapes: the measured
      // prose turn is what motivates the feature, and a run of consecutive tool cards
      // at 100% `tool_use` is where the TOOL budget binds and the window is narrowest.
      await coldLoad("big", shape === "prose" ? hugeTurn("big", HUGE) : toolTurn("big", HUGE));
      // To the head, then back down past what it mounted, which is what puts the
      // anchor MID-turn: at either end of the sequence one side latches and the other
      // takes the whole budget.
      await dragTo(0);
      await vi.waitFor(() => {
        expect(mountedIndices("big")[0]).toBe(0);
      });
      const to = mountedIndices("big").at(-1) ?? 0;
      await dragTo(mountedBottom("big"));
      await vi.waitFor(() => {
        expect(mountedIndices("big").at(-1)).toBeGreaterThan(to);
      });

      // Anchor and window read TOGETHER and re-read until they agree, because the pair
      // is what the floor is a property of. `windowSettled` alone is not enough: the
      // compensation writes `scrollTop`, and `content-visibility: auto` re-measuring
      // with no mutation makes the browser clamp it again, which the listener reads as
      // a gesture — so a window can start moving after two polls agreed, and the read
      // then measures that residue rather than any plan. A retry cannot rescue a
      // MISSING floor: no settled pair would satisfy it and this times out red.
      await vi.waitFor(
        () => {
          const mounted = mountedIndices("big");
          const at = anchorIndex("big");
          expect(at).toBeGreaterThan(mounted[0] ?? 0);
          expect(at - (mounted[0] ?? 0)).toBeGreaterThanOrEqual(OVERSCAN_BLOCKS);
          expect((mounted.at(-1) ?? 0) - at).toBeGreaterThanOrEqual(OVERSCAN_BLOCKS);
        },
        { timeout: 4000 },
      );
    });
  }

  it("drops a whole message ROW when the window moves off it, and keeps the rest in order", async () => {
    // 5 assistant messages of 200 blocks, so the 320-ordinal window covers about one and
    // a half rows and a move to the head has to take whole rows OUT of the body. The row
    // axis has no other fixture: `bodyHolds`, `placeRow`'s insert-before-the-next-later-key
    // discipline, `collectWindowMove`'s per-row side split and the spacer's row-gap term
    // are all unreachable on a turn with one assistant message.
    const messages = multiRowTurn("multi", 5, 200);
    forgetHeights(messages.map((m) => m.id));
    await coldLoad("multi", messages);
    const keys = (): string[] => bodyRows("multi").map((r) => r.getAttribute(KEY_ATTR) ?? "");
    // At the live edge the window is the tail, so the LAST row is mounted and the first
    // is not — the premise, or the drop below has nothing to remove.
    expect(keys()).toContain("multi-a4");
    expect(keys()).not.toContain("multi-a0");
    expect(bodyRows("multi").length).toBeLessThan(5);

    await dragTo(0);
    await vi.waitFor(() => {
      expect(keys()).toContain("multi-a0");
    });

    // A row LEFT the body: `disposeMessage` ran for it, which is the only production
    // caller `recordRowHeight` has.
    expect(keys()).not.toContain("multi-a4");
    // And the rows that stayed read in the turn's own order — the reconcile's OUTCOME,
    // whichever arm produced it. `placeRow`'s insert-before-the-next-later-key discipline
    // has no failing observable: an `appendChild` there leaves every case green, because
    // `bodyHolds` then refuses the body and the reconcile re-orders the existing nodes.
    // The length floor is what makes "in order" expressible: a one-row survivor set
    // satisfies any sort.
    const order = keys();
    expect(order.length).toBeGreaterThanOrEqual(2);
    expect(order).toEqual([...order].sort((a, b) => a.localeCompare(b)));
    // The departed rows still hold their height, gaps included, so the document is long
    // enough to stand the reader where they are.
    expect(scroller().scrollHeight - scroller().clientHeight).toBeGreaterThanOrEqual(
      scroller().scrollTop,
    );
    expect(mountedIndices("multi").length).toBeLessThanOrEqual(RESIDENT_BLOCKS);
    // A TAIL spacer stands for the rows that left, priced at what they measured.
    const spacer = card("multi").querySelector<HTMLElement>(
      `:scope > .turn-body > [${KEY_ATTR}="__space_tail__"]`,
    );
    expect(spacer).not.toBeNull();
    expect(spacer?.getBoundingClientRect().height ?? 0).toBeGreaterThan(VIEWPORT_PX);
  });

  it("keeps every ROW the window still wants on the same node", async () => {
    const messages = multiRowTurn("keep", 5, 200);
    forgetHeights(messages.map((m) => m.id));
    await coldLoad("keep", messages);
    await dragTo(0);
    await vi.waitFor(() => {
      expect(bodyRows("keep").map((r) => r.getAttribute(KEY_ATTR))).toContain("keep-a0");
    });

    const rowNodes = (): Map<string, HTMLElement> =>
      new Map(bodyRows("keep").map((r) => [r.getAttribute(KEY_ATTR) ?? "", r]));
    const before = rowNodes();
    // Row key PLUS how many of its blocks are mounted. `mountedIndices` cannot report
    // this: a block index is per MESSAGE, so two rows of 200 blocks both number theirs
    // 0..199 and the sets collide; and the key set alone is too coarse to see a window
    // that moved inside the rows it already held.
    const shape = (): string =>
      bodyRows("keep")
        .map(
          (r) =>
            `${r.getAttribute(KEY_ATTR) ?? ""}:${String(r.querySelectorAll("[data-block-index]").length)}`,
        )
        .join(",");
    const was = shape();

    // Three fifths down, which is past the mounted third and short of the tail. Derived
    // from the DOCUMENT rather than from `mountedBottom`, because the document GROWS as
    // `content-visibility` measures rows for real — measured at +40% during this case —
    // so a target computed from the mounted region lands back inside it.
    await dragTo(Math.round(scroller().scrollHeight * 0.6));
    await vi.waitFor(() => {
      expect(shape()).not.toBe(was);
    });

    // A row the move KEPT is neither rebuilt nor re-created: the row key is what the
    // reconcile matches on, so a key carrying the window would re-create every row on
    // every move. What it does NOT pin is `placeRow`'s insert-before discipline, whose
    // `appendChild` break is green (see the drop case).
    const after = rowNodes();
    const kept = [...after.keys()].filter((k) => before.has(k));
    expect(kept.length).toBeGreaterThan(0);
    for (const key of kept) {
      expect(after.get(key), key).toBe(before.get(key));
    }
  });

  it("holds a navigation pin's grant through the flight the jump itself produces", async () => {
    const id = await coldLoad("big", hugeTurn("big", HUGE));

    // A jump to the far HEAD of the turn, from the live edge. The grant REPLACES the
    // window's slice for a turn the two contest, which is what the reader asked for.
    await mountTurnBody(id, "big", 20);
    bumpMessages(id, "shape");
    const held = `[data-block-index="20"]`;
    await vi.waitFor(() => {
      expect(card("big").querySelector(held)).not.toBeNull();
    });

    // ~50 scroll events land between here and there on a smooth flight, and clearing
    // the pin on any of them cancels the navigation the reader asked for.
    for (const at of [0.6, 0.4, 0.2]) {
      await dragTo(Math.round(scroller().scrollHeight * at));
      expect(card("big").querySelector(held), `at ${String(at)}`).not.toBeNull();
    }
  });

  it("drains a dropped block's own effect, not just its element", async () => {
    const id = await coldLoad("todo", todoHeadTurn("todo", HUGE));
    const key = toolCallSigKey(id, "todo-todo");
    // Mounted first: the head is outside the cold window, so the reader has to reach
    // it before there is anything to release.
    await dragTo(0);
    await vi.waitFor(() => {
      expect(card("todo").querySelector('[data-block-index="0"]')).not.toBeNull();
    });
    expect(toolCallSigs.get(key)).toBeDefined();

    // Back to the live edge, which retracts the head the reader left.
    await dragTo(scroller().scrollHeight);
    await vi.waitFor(() => {
      expect(card("todo").querySelector('[data-block-index="0"]')).toBeNull();
    });
    // The ELEMENT going is half of it. Its effect is subscribed to the store, so a
    // drop that leaves it armed writes into a detached list for the rest of the page.
    expect(toolCallSigs.get(key)).toBeUndefined();
  });

  it("brings a tool card back OPEN when the reader had opened it before the drop", async () => {
    await coldLoad("open", outputHeadTurn("open", HUGE));
    const toggle = (): HTMLElement | null =>
      card("open").querySelector<HTMLElement>('[data-block-index="0"] .tool-disclosure');
    await dragTo(0);
    await vi.waitFor(() => {
      expect(toggle()).not.toBeNull();
    });
    toggle()?.click();
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");

    // Away, so the card is dropped, and back. `aria-expanded` is the ONLY record a
    // reader opened a tool card — the boxes have registry keys and this has nothing —
    // so a drop that does not carry it hands back a collapsed card.
    await dragTo(scroller().scrollHeight);
    await vi.waitFor(() => {
      expect(toggle()).toBeNull();
    });
    // Out through the live edge's own settle window, or the drag back is a gesture
    // inside the bottom pin's hold and gets undone before any pass sees it.
    await atLiveEdge();
    await dragTo(0);
    await vi.waitFor(() => {
      expect(toggle()).not.toBeNull();
    });
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
  });

  it("brings a delegate box back OPEN when the reader had opened it before the drop", async () => {
    await coldLoad("sub", subagentHeadTurn("sub", HUGE));
    const box = (): HTMLElement | null =>
      card("sub").querySelector<HTMLElement>(".subagent-block[data-subtask='sub-sub']");
    const head = (): HTMLElement | null =>
      box()?.querySelector<HTMLElement>(":scope > .subagent-header") ?? null;
    await dragTo(0);
    await vi.waitFor(() => {
      expect(box()).not.toBeNull();
    });
    expect(box()?.classList.contains("collapsed")).toBe(true);
    head()?.click();
    expect(box()?.classList.contains("collapsed")).toBe(false);

    // Away, so the drop takes the box, and back. Its `openContainers` key survives the
    // drop by design — what this pins is that the re-mount READS it, which is the half
    // a default-collapsed creation site decides on its own.
    await dragTo(scroller().scrollHeight);
    await vi.waitFor(() => {
      expect(box()).toBeNull();
    });
    await atLiveEdge();
    await dragTo(0);
    await vi.waitFor(() => {
      expect(box()).not.toBeNull();
    });
    expect(box()?.classList.contains("collapsed")).toBe(false);
    expect(head()?.getAttribute("aria-expanded")).toBe("true");
  });

  it("mounts a HEAD-ward grant with no repaint behind it, which is all a rail jump gives", async () => {
    const id = await coldLoad("big", hugeTurn("big", HUGE));
    expect(card("big").querySelector('[data-block-index="20"]')).toBeNull();

    // The rail jump's exact shape: scroll first, then build, and nothing after it. No
    // `bumpMessages` here on purpose — the grant is head-ward of everything mounted, so
    // the build itself can insert nothing and the pass that does has to come from the
    // build's own settle. A flight of zero distance emits no scroll event at all.
    await mountTurnBody(id, "big", 20);
    await vi.waitFor(() => {
      expect(card("big").querySelector('[data-block-index="20"]')).not.toBeNull();
    });
    // And the window MOVED to the grant rather than growing to cover it.
    expect(mountedIndices("big").length).toBeLessThanOrEqual(RESIDENT_BLOCKS);
    expect(card("big").querySelector(`[data-block-index="${String(HUGE - 1)}"]`)).toBeNull();
  });

  it("mounts a HEAD-ward grant with a repaint landing mid-build", async () => {
    const id = await coldLoad("big", hugeTurn("big", HUGE));
    expect(card("big").querySelector('[data-block-index="20"]')).toBeNull();

    // The store bump lands while the build is in FLIGHT, which is the common case
    // during streaming. That paint refuses the building turn and must not record its
    // plan as applied — the build's own settle pass is the only thing left to insert
    // the grant, and it exits on a plan already recorded.
    const built = mountTurnBody(id, "big", 20);
    bumpMessages(id, "shape");
    await built;

    await vi.waitFor(() => {
      expect(card("big").querySelector('[data-block-index="20"]')).not.toBeNull();
    });
  });

  it("steps the anchor to the nearest OPENABLE turn while a fold sits deferred", async () => {
    const id = chatID();
    // t2 is tool-bearing, so its fold really hides something; hand-opened, so it is
    // openable, bodied and unfolded with two newer turns after it.
    setTurnOpen(id, "t2", true);
    activate(id, [...hugeTurn("t1", 8), ...toolTurn("t2", 60), ...hugeTurn("t3", HUGE)]);
    await settled("t3");
    await atLiveEdge();
    await dragTo(card("t2").offsetTop + Math.floor(card("t2").offsetHeight / 2));
    expect(card("t2").hasAttribute("data-folded")).toBe(false);

    // The reader folds it from the rail's side of the store and stays put. Reading is
    // what makes `deferWhileReading` hold `setCardFolded` and the unmount, so the card
    // is still unfolded and bodied on screen while the store has already stopped
    // calling it openable.
    setTurnOpen(id, "t2", false);
    bumpMessages(id, "shape");

    // The card the reader is on is not openable now, so the ladder steps FORWARD to
    // t3 and seeds at its HEAD. Testing `data-folded` instead would pick t2's own
    // ordinals, which `planResidency` cannot place, take its absent-turn fallback,
    // and window t3's TAIL — the live edge, hundreds of ordinals from the reader.
    await vi.waitFor(() => {
      expect(mountedIndices("t3")[0]).toBe(0);
    });
    // The case's own premise, or it would pass for the wrong reason: the viewport top
    // is inside the card the store has stopped calling openable.
    const t2 = card("t2");
    expect(scroller().scrollTop).toBeGreaterThanOrEqual(t2.offsetTop);
    expect(scroller().scrollTop).toBeLessThan(t2.offsetTop + t2.offsetHeight);
    // And the body under the reader survives the pass: its unmount is a deferred
    // REQUEST, held until they return to Following.
    expect(t2.hasAttribute("data-folded")).toBe(false);
    expect(t2.querySelector(":scope > .turn-body")).not.toBeNull();
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
      recordRowHeight(row.getAttribute(KEY_ATTR) ?? "", { from: 0, to: 1 }, row.offsetHeight);
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
