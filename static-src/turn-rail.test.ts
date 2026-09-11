// The rail's DOM and its click flow. The three arithmetics it consumes are pure and
// tested where they live (`rail-select`, `rail-merge`, `rail-activation`); what this
// file covers is what the renderer publishes, which chat the module belongs to,
// whether the rail is worth showing, and the whole jump pipeline.
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest";

// scroll.ts self-initialises a singleton against #messages at import time, so it is
// stubbed rather than staged. The scroller fake is deliberately more than a value
// bag: it records the epoch and the absolute landings the jump produces, and it
// fires `scrollend` for a programmatic scroll the way Chromium does, so a
// correction loop settles without waiting out its own timeout.
//
// `top: scrollTop` on the scroller's own rect is what makes a card's measured top
// INVARIANT under the fake scroll, which is the property a real scroller has (the
// card's viewport rect moves, and here the cards' rects are fixed instead).
const { scrollable } = vi.hoisted(() => {
  const handlers = new Map<string, Set<() => void>>();
  const el = {
    scrollTop: 0,
    clientHeight: 600,
    clientTop: 0,
    getBoundingClientRect: () => ({ top: el.scrollTop, bottom: el.scrollTop + 600, height: 600 }),
    addEventListener(type: string, fn: () => void) {
      const set = handlers.get(type) ?? new Set<() => void>();
      set.add(fn);
      handlers.set(type, set);
    },
    removeEventListener(type: string, fn: () => void) {
      handlers.get(type)?.delete(fn);
    },
  };
  return {
    scrollable: {
      /** The transcript's scroll room, which is the rail's own visibility gate. */
      by: 500,
      /** Where the reading line sits inside the scrollport. */
      line: 0,
      /** The scroller's published edge verdict. */
      atLiveEdge: false,
      el,
      /** Absolute landings the jump asked for, in order. */
      landings: [] as { px: number; behavior: string }[],
      /** `begin` / `end`, so a case can see the epoch bracket the whole operation. */
      epochs: [] as string[],
      readerGesture: undefined as (() => void) | undefined,
      transcriptMutate: undefined as (() => void) | undefined,
      contentResize: undefined as (() => void) | undefined,
      attach: undefined as (() => void) | undefined,
      fire(type: string) {
        for (const fn of [...(handlers.get(type) ?? [])]) {
          fn();
        }
      },
      reset() {
        this.by = 500;
        this.line = 0;
        this.atLiveEdge = false;
        this.landings.length = 0;
        this.epochs.length = 0;
        el.scrollTop = 0;
      },
    },
  };
});
vi.mock("./scroll.js", () => ({
  scrollableBy: () => scrollable.by,
  readingLineOffset: () => scrollable.line,
  atLiveEdgeNow: () => scrollable.atLiveEdge,
  getScrollEl: () => scrollable.el,
  beginSelfScroll: () => {
    scrollable.epochs.push("begin");
  },
  endSelfScroll: () => {
    scrollable.epochs.push("end");
  },
  scrollToOffset: (px: number, behavior: string) => {
    scrollable.landings.push({ px, behavior });
    scrollable.el.scrollTop = px;
    setTimeout(() => {
      scrollable.fire("scrollend");
    }, 0);
  },
  onReaderGesture: (cb: () => void) => {
    scrollable.readerGesture = cb;
    return () => {
      scrollable.readerGesture = undefined;
    };
  },
  onTranscriptMutate: (cb: () => void) => {
    scrollable.transcriptMutate = cb;
    return () => {
      scrollable.transcriptMutate = undefined;
    };
  },
  onContentResize: (cb: () => void) => {
    scrollable.contentResize = cb;
    return () => {
      scrollable.contentResize = undefined;
    };
  },
  onAttach: (cb: () => void) => {
    scrollable.attach = cb;
    return () => {
      scrollable.attach = undefined;
    };
  },
}));
// The session-wide index is the rail's own fetch; the lifecycle cases below decide
// what it does with the answer, not how it asks.
vi.mock("./api-client.js", () => ({ apiGet: vi.fn() }));

// The pagination door `navigateToTurn` walks when its target is off the resident
// window. Mocked because the real one is a network fetch, and what these cases
// assert is the rail's own sequencing around it.
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

import {
  railSeams,
  mountTurnRail,
  loadTurnRail,
  setResidentTurns,
  pointTurnRail,
  refreshTurnRail,
  resetTurnRail,
  initTurnRailCallbacks,
  type TurnSummary,
} from "./turn-rail.js";
import { railMetrics } from "./rail-select.js";
import { apiGet } from "./api-client.js";
import { loadMessages } from "./store-load.js";
import { setSessions, setActive, get } from "./store.js";
import { bumpSyncEpoch } from "./tab-freshness.js";
import type { Message, Session } from "./types.js";
import { KEY_ATTR } from "@cplieger/reactive";
import type { TurnOutcome } from "./turns.js";

const MINUTE = 60_000;
/** The pause a seam needs, in minutes (`turn-rail.ts` GAP_THRESHOLD_MS). Hardcoded:
 *  read off the module, these cases would agree with whatever it believes. */
const GAP_MINUTES = 20;

function turn(n: number, over: Partial<TurnSummary> = {}): TurnSummary {
  return {
    id: `m${String(n)}`,
    n,
    outcome: "completed",
    // One minute apart by default, so nothing trips the gap threshold unless a
    // case asks for it.
    ts: n * MINUTE,
    ...over,
  };
}

function turns(count: number, outcome: TurnOutcome = "completed"): TurnSummary[] {
  return Array.from({ length: count }, (_, i) => turn(i + 1, { outcome }));
}

/** Mount the rail and give it the box the stylesheet would.
 *
 *  Idempotent and a module SINGLETON, so on a whole-file run only the first block's
 *  host holds it — hence the document-wide resolve. Browser Mode serves no CSS and
 *  `.turn-rail` takes its height from `position: absolute; inset-block`, so without
 *  an explicit box the track measures whatever its markers occupy and holds one
 *  marker. The box is the harness standing in for the stylesheet. */
function mountRail(host: HTMLElement): HTMLElement {
  document.body.appendChild(host);
  mountTurnRail(host);
  const el = document.querySelector<HTMLElement>(".turn-rail");
  if (el === null) {
    throw new Error("rail not mounted");
  }
  el.style.height = "600px";
  el.style.display = "block";
  return el;
}

/** A rail tall enough for `n` markers at `pitchPx`. The pitch is a PARAMETER,
 *  obtained the way `railMetrics` does, so a case can drive either pointer tier. */
function railFor(n: number, pitchPx: number): number {
  return n * pitchPx;
}

function fakeRect(top: number, height: number): DOMRect {
  return {
    x: 0,
    y: top,
    top,
    bottom: top + height,
    left: 0,
    right: 100,
    width: 100,
    height,
    toJSON: () => ({}),
  } as DOMRect;
}

/** Two animation frames: one for the rail's own coalesced pick, one for whatever it
 *  renders from it. */
function frames(): Promise<void> {
  return new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

describe("railSeams", () => {
  it("emits a seam between two turns further apart than the threshold", () => {
    const all = [turn(1, { ts: 0 }), turn(2, { ts: 60 * MINUTE }), turn(3, { ts: 61 * MINUTE })];
    const seams = railSeams(all, all);
    expect(seams).toHaveLength(1);
    expect(seams[0]?.fromN).toBe(1);
    expect(seams[0]?.toN).toBe(2);
  });

  it("leaves an ordinary pause alone", () => {
    const all = [turn(1, { ts: 0 }), turn(2, { ts: 19 * MINUTE })];
    expect(railSeams(all, all)).toEqual([]);
  });

  it("carries the elapsed time for the seam's label", () => {
    const all = [turn(1, { ts: 0 }), turn(2, { ts: 120 * MINUTE })];
    expect(railSeams(all, all)[0]?.ms).toBe(120 * MINUTE);
  });

  it("withholds a pause whose own two turns are not both shown", () => {
    // The turn that OPENS the new sitting has no marker, so there is no pair of
    // positions the band belongs between. Drawing it against the nearest survivor
    // would put the break at a turn that did not take one.
    const all = [turn(1, { ts: 0 }), turn(2, { ts: 120 * MINUTE }), turn(3, { ts: 121 * MINUTE })];
    const shown = [all[0], all[2]].filter((t) => t !== undefined);
    expect(railSeams(all, shown)).toEqual([]);
  });

  it("reads the time between two non-adjacent markers as work, not as a pause", () => {
    // Six turns five minutes apart: nobody stopped, and the first and last are
    // neighbours on a downsampled axis half an hour apart. That elapsed time is five
    // turns of continuous work, which is what the walk over SHOWN used to report.
    const all = Array.from({ length: 6 }, (_, i) => turn(i + 1, { ts: i * 5 * MINUTE }));
    const shown = [all[0], all[5]].filter((t) => t !== undefined);
    expect(shown[1]?.ts).toBeGreaterThan(GAP_MINUTES * MINUTE);
    expect(railSeams(all, shown)).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// Which chat the rail belongs to.
//
// The rail is a module singleton spanning a session while the transcript store
// holds a paginated window, so the chat it currently points at is state, and
// both directions of leaving it unset were shipped defects: a rail still holding
// the previous chat's index rendered that chat's markers over a conversation
// with no messages in it, and a refresh naming a chat the rail had never been
// handed was discarded, so the first turn of a chat started from empty got no
// marker at all.
// ---------------------------------------------------------------------------

describe("which chat the rail belongs to", () => {
  const host = document.createElement("div");

  beforeAll(() => {
    mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
  });

  function rail(): HTMLElement {
    const el = document.querySelector<HTMLElement>(".turn-rail");
    if (el === null) {
      throw new Error("rail not mounted");
    }
    return el;
  }

  /** The marker labels currently painted, in order. */
  function markers(): string[] {
    return [...rail().querySelectorAll(".rail-marker")].map((b) => b.firstChild?.textContent ?? "");
  }

  it("paints one marker per turn once the index arrives", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-a");
    expect(markers()).toEqual(["1", "2"]);
  });

  it("empties itself when handed another chat", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-a");

    pointTurnRail("c-b");

    expect(markers()).toEqual([]);
    // No child NODES, not merely no markers: `.turn-rail:empty` is what hides
    // the axis, and a text node would satisfy the selector's negation.
    expect(rail().childNodes).toHaveLength(0);
  });

  it("asks the server nothing when it is only pointed", () => {
    // An empty chat has no turns by definition, and a client-minted id has no
    // record to ask about, so the fetch is skipped rather than 404'd.
    pointTurnRail("c-b");
    expect(apiGet).not.toHaveBeenCalled();
  });

  it("adopts a later refresh for the chat it points at", async () => {
    pointTurnRail("c-b");
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });

    await refreshTurnRail("c-b");

    expect(markers()).toEqual(["1"]);
  });

  it("drops a refresh for a chat it no longer points at", async () => {
    pointTurnRail("c-a");
    vi.mocked(apiGet).mockImplementation(async () => {
      // The reader switches chats while the index is in flight.
      pointTurnRail("c-b");
      return { turns: [turn(1), turn(2)] };
    });

    await refreshTurnRail("c-a");

    expect(markers()).toEqual([]);
  });

  // The mirror of the stale-markers defect, and the reason pointing cannot be
  // skipped for an empty chat. `turn_ended` is the only moment the index is
  // re-read, so a rail that was never handed the chat discards the very refresh
  // that would have drawn its first marker, and the session stays blank until
  // the reader switches away and back.
  it("drops a refresh for a chat it was never pointed at", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });

    await refreshTurnRail("c-never-pointed");

    expect(markers()).toEqual([]);
  });

  it("keeps what it is showing when the fetch fails", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });
    await loadTurnRail("c-a");

    // A rail that empties itself on a transient failure is worse than one that
    // is briefly a turn behind.
    vi.mocked(apiGet).mockResolvedValue(null);
    await refreshTurnRail("c-a");

    expect(markers()).toEqual(["1"]);
  });
});

// ---------------------------------------------------------------------------
// When the rail is worth existing
// ---------------------------------------------------------------------------

// The rail is a NAVIGATOR, so it has nothing to offer a transcript the reader can
// already see whole — on a one-turn chat it was a column of one digit beside a
// conversation with nowhere to go. These cases pin the gate in both directions,
// including the one activation structurally cannot cover.
describe("the rail only appears once the transcript can be scrolled", () => {
  const host = document.createElement("div");

  beforeAll(() => {
    mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
  });

  function rail(): HTMLElement {
    const el = document.querySelector<HTMLElement>(".turn-rail");
    if (el === null) {
      throw new Error("rail not mounted");
    }
    return el;
  }

  function markers(): string[] {
    return [...rail().querySelectorAll(".rail-marker")].map((b) => b.firstChild?.textContent ?? "");
  }

  it("stays empty for a transcript that fits, however many turns it holds", async () => {
    scrollable.by = 0;
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });

    await loadTurnRail("c-short");

    // Empty rather than hidden by a class: `.turn-rail:empty` is what removes the
    // element, and that takes the axis line with it.
    expect(markers()).toEqual([]);
    expect(rail().children.length).toBe(0);
  });

  it("appears once a paint takes the transcript past the threshold", async () => {
    scrollable.by = 0;
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-grows");
    expect(markers()).toEqual([]);

    // The transcript grew — a streaming turn, or a page of history landing. This is
    // the case activation cannot see: the turn holding the reading line has not
    // changed, so only the paint's own re-render reaches it.
    scrollable.by = 500;
    setResidentTurns([]);

    expect(markers()).toEqual(["1", "2"]);
  });

  it("goes away again when the transcript stops being scrollable", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-shrinks");
    expect(markers()).toEqual(["1", "2"]);

    // A window the reader just made taller, or turns folding away.
    scrollable.by = 0;
    setResidentTurns([]);

    expect(markers()).toEqual([]);
  });

  it("wants real scroll room, not one stray pixel", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });

    // A transcript overflowing by a hair is not one anybody navigates, and
    // treating it as navigable would flip the rail on and off as its own content
    // settles.
    scrollable.by = 1;
    await loadTurnRail("c-hair");
    expect(markers()).toEqual([]);

    scrollable.by = 101;
    setResidentTurns([]);
    expect(markers()).toEqual(["1"]);
  });
});

// ---------------------------------------------------------------------------
// Which turn the reading line is in.
//
// The rule: the active turn is the one whose box contains the reading line, read
// from the scroll offset against a cached table. The arithmetic is
// `rail-activation.ts`'s subject; what these cases pin is the wiring — the table is
// built from the cards the paint hands over, a scroll frame re-reads it without
// measuring anything, and both end clamps survive.
// ---------------------------------------------------------------------------

describe("which turn the reading line is in", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;
  /** How many times a card has been measured, so a scroll frame's layout cost is
   *  observable rather than argued about. */
  let measures = 0;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
    measures = 0;
  });

  /** A turn card at a known top in the scroller's own frame. Detached and rect-faked:
   *  what the module needs from a card is its key and its box. */
  function card(n: number, top: number): HTMLElement {
    const e = document.createElement("div");
    e.className = "turn";
    e.setAttribute(KEY_ATTR, `m${String(n)}`);
    const rect = (): DOMRect => {
      measures++;
      return fakeRect(top, 400);
    };
    Object.defineProperty(e, "getBoundingClientRect", { configurable: true, value: rect });
    Object.defineProperty(e, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(top, 400)],
    });
    return e;
  }

  /** The label of the marker the rail marks current, or "" when none is. */
  function current(): string {
    const el = rail.querySelector<HTMLElement>(".rail-marker[data-current]");
    return el?.firstChild?.textContent ?? "";
  }

  async function seat(id: string, ns: number[]): Promise<Map<number, HTMLElement>> {
    vi.mocked(apiGet).mockResolvedValue({ turns: ns.map((n) => turn(n)) });
    await loadTurnRail(id);
    const cards = new Map(ns.map((n, i) => [n, card(n, i * 400)]));
    // A view attaches at its own restored offset, so seating a chat starts at the top
    // rather than inheriting whatever the previous case scrolled to.
    scrollable.el.scrollTop = 0;
    setResidentTurns([...cards.values()]);
    await frames();
    return cards;
  }

  function at(cards: Map<number, HTMLElement>, n: number): HTMLElement {
    const c = cards.get(n);
    if (c === undefined) {
      throw new Error(`no card for turn ${String(n)}`);
    }
    return c;
  }

  async function scrollTo(px: number): Promise<void> {
    scrollable.el.scrollTop = px;
    scrollable.fire("scroll");
    await frames();
  }

  it("names the turn the reading line has entered", async () => {
    await seat("c-a", [1, 2, 3]);

    await scrollTo(450);

    expect(current()).toBe("2");
  });

  // Written as the top CLAMP, not as "all three are visible, expect 1": at the top
  // of a transcript the reading line can sit above the first card entirely.
  it("names the first turn at the very top of the transcript", async () => {
    await seat("c-a", [1, 2, 3]);

    expect(current()).toBe("1");
  });

  it("names the last turn at the very bottom of the transcript", async () => {
    await seat("c-a", [1, 2, 3]);
    scrollable.atLiveEdge = true;

    await scrollTo(500);

    expect(current()).toBe("3");
  });

  it("answers from the cached table with no layout read per scroll frame", async () => {
    await seat("c-a", [1, 2, 3]);
    const afterBuild = measures;
    expect(afterBuild).toBeGreaterThan(0);

    for (const px of [100, 420, 460, 830, 900]) {
      await scrollTo(px);
    }

    // The table is a cache invalidated by mutation and reflow, never by a scroll: a
    // position stored in the scroller's own frame does not move when it scrolls.
    expect(measures).toBe(afterBuild);
    expect(current()).toBe("3");
  });

  it("drops the previous chat's cards when re-pointed", async () => {
    const a = await seat("c-a", [1, 2, 3]);
    await scrollTo(830);
    expect(current()).toBe("3");
    expect(a.size).toBe(3);

    pointTurnRail("c-b");
    await seat("c-b", [1, 2]);

    // Never "3": chat B has no turn 3, and a leftover card would name a turn it
    // does not have.
    expect(current()).toBe("1");
  });

  it("drops a departed turn from the table and re-answers without it", async () => {
    const cards = await seat("c-a", [1, 2, 3]);
    await scrollTo(830);
    expect(current()).toBe("3");

    // Turn 3's card leaves the transcript. Nothing else re-derives the mark, so the
    // paint's own invalidation has to re-answer or the marker stays on a turn that
    // is no longer mounted.
    setResidentTurns([at(cards, 1), at(cards, 2)]);
    await frames();

    expect(current()).toBe("2");
  });

  it("re-answers after a reflow that moved a top with no scroll behind it", async () => {
    const cards = await seat("c-a", [1, 2, 3]);
    await scrollTo(830);
    expect(current()).toBe("3");

    // `content-visibility: auto` on `.msg-row` makes a card swapping its estimated
    // height for its real one move every top below it, with no DOM change and no
    // scroll. The reading line then sits in a different turn.
    Object.defineProperty(at(cards, 3), "getClientRects", {
      configurable: true,
      value: () => [fakeRect(2000, 400)],
    });
    Object.defineProperty(at(cards, 3), "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(2000, 400),
    });
    scrollable.contentResize?.();
    await frames();

    expect(current()).toBe("2");
  });

  it("re-answers when a parked view takes the scroller back", async () => {
    const cards = await seat("c-a", [1, 2, 3]);
    await scrollTo(830);
    expect(current()).toBe("3");

    // The reader switches away and back. An unpark restores a saved scrollTop
    // against cards the residency pass re-measured while the view was parked, and it
    // is neither a DOM mutation nor a card resize, so nothing else re-asks.
    Object.defineProperty(at(cards, 3), "getClientRects", {
      configurable: true,
      value: () => [fakeRect(2000, 400)],
    });
    Object.defineProperty(at(cards, 3), "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(2000, 400),
    });
    scrollable.attach?.();
    await frames();

    expect(current()).toBe("2");
  });
});

// ---------------------------------------------------------------------------
// The per-chat record: what makes a switch back to a loaded chat cost zero
// fetches. The rail keeps each chat's fetched index alongside the sync epoch
// and the message count captured BEFORE the request, and an activation fetches
// only when that record cannot stand in — missing, from before a transport
// gap, from before the count moved, or overruled by the caller's `force` (the
// stale-transcript activation, whose verdict the rail cannot re-derive after
// the messages heal re-stamps the session fresh).
// ---------------------------------------------------------------------------

describe("the rail record gates the activation fetch", () => {
  const host = document.createElement("div");

  beforeAll(() => {
    mountRail(host);
  });

  function markers(): string[] {
    const rail = document.querySelector<HTMLElement>(".turn-rail");
    return [...(rail?.querySelectorAll(".rail-marker") ?? [])].map(
      (b) => b.firstChild?.textContent ?? "",
    );
  }

  function session(id: string, messageCount: number): Session {
    return {
      id,
      name: id,
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      usage: {
        context_pct: 0,
        context_size: 0,
        credits: 0,
        turn_count: 0,
        last_turn_ms: 0,
        has_real_data: false,
      },
      message_count: messageCount,
      messages: [],
      has_more: false,
      thinking: false,
      working_label: "Thinking",
    };
  }

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
    setSessions([session("c-a", 2), session("c-b", 0)]);
    // Per-chat answers: c-a is the two-turn session under test, c-b an empty
    // sibling — a blanket answer would paint c-b's first fetch with c-a's turns.
    vi.mocked(apiGet).mockImplementation((path: string) =>
      Promise.resolve(path.includes("c-a") ? { turns: [turn(1), turn(2)] } : { turns: [] }),
    );
  });

  it("paints a recorded chat from memory, with no fetch", async () => {
    await loadTurnRail("c-a");
    expect(markers()).toEqual(["1", "2"]);

    pointTurnRail("c-b");
    expect(markers()).toEqual([]);

    vi.mocked(apiGet).mockClear();
    await loadTurnRail("c-a");
    expect(markers()).toEqual(["1", "2"]);
    expect(apiGet).not.toHaveBeenCalled();
  });

  it("force refetches even when the record is current", async () => {
    // The caller's arm of the gate: an activation that found the TRANSCRIPT
    // stale (eviction, first load) refetches the rail with it, however current
    // the rail's own record looks.
    await loadTurnRail("c-a");
    vi.mocked(apiGet).mockClear();

    await loadTurnRail("c-a", { force: true });
    expect(apiGet).toHaveBeenCalledTimes(1);
  });

  it("a moved message count invalidates the record", async () => {
    await loadTurnRail("c-a");
    // A background turn lands while the rail points elsewhere: SSE ingest moves
    // the chat's count, and the record now describes an older session.
    get("c-a")!.message_count = 3;
    pointTurnRail("c-b");

    vi.mocked(apiGet).mockClear();
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });
    await loadTurnRail("c-a");
    expect(apiGet).toHaveBeenCalledTimes(1);
    expect(markers()).toEqual(["1", "2", "3"]);
  });

  it("a transport gap invalidates the record", async () => {
    await loadTurnRail("c-a");
    bumpSyncEpoch();

    vi.mocked(apiGet).mockClear();
    await loadTurnRail("c-a");
    expect(apiGet).toHaveBeenCalledTimes(1);
  });

  it("a fetch that raced a gap records a claim that already reads stale", async () => {
    // The rail's half of the fetch-races-gap rule: the epoch is captured before
    // the request, so an answer that may predate the gap's lost turn_endeds
    // cannot claim to have survived it, and the next activation refetches.
    vi.mocked(apiGet).mockImplementation(async () => {
      bumpSyncEpoch();
      return { turns: [turn(1), turn(2)] };
    });
    await loadTurnRail("c-a");

    vi.mocked(apiGet).mockClear();
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-a");
    expect(apiGet).toHaveBeenCalledTimes(1);
  });

  it("records a background refresh without painting it, so the next activation is free", async () => {
    // The turn_ended door for a chat the rail points away from: the fetched
    // index is recorded for that chat's next activation but must not paint over
    // the pointed chat's rail.
    await loadTurnRail("c-b");
    expect(markers()).toEqual([]);

    await refreshTurnRail("c-a");
    expect(markers()).toEqual([]);

    vi.mocked(apiGet).mockClear();
    await loadTurnRail("c-a");
    expect(markers()).toEqual(["1", "2"]);
    expect(apiGet).not.toHaveBeenCalled();
  });

  it("keeps the stale record on a failed refetch, so the next activation retries", async () => {
    await loadTurnRail("c-a");
    get("c-a")!.message_count = 3;
    pointTurnRail("c-b");

    // The refetch the moved count demands fails; the record must not be
    // rewritten into currency by it.
    vi.mocked(apiGet).mockClear();
    vi.mocked(apiGet).mockResolvedValue(null);
    await loadTurnRail("c-a");
    expect(apiGet).toHaveBeenCalledTimes(1);

    pointTurnRail("c-b");
    await loadTurnRail("c-a");
    expect(apiGet).toHaveBeenCalledTimes(2);
  });

  it("renders a purged chat empty rather than from its dead record", async () => {
    await loadTurnRail("c-a");
    expect(markers()).toEqual(["1", "2"]);

    // The chat leaves the store (closed tab, deleted record) while its rail
    // record survives; re-pointing prunes the record rather than painting it.
    setSessions([session("c-b", 0)]);
    pointTurnRail("c-b");
    pointTurnRail("c-a");
    expect(markers()).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// The set comes from what the transcript HOLDS, extended backwards by the index.
//
// The index is refetched at three moments — turn end, chat activation, a transport
// gap — and none of them is turn START, so a rail assembled from the index alone
// cannot show the turn running now. The merge is `rail-merge.ts`'s subject; this is
// the DOM-level property it buys.
// ---------------------------------------------------------------------------

describe("the newest turn needs no fetch", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
  });

  function markers(): string[] {
    return [...rail.querySelectorAll(".rail-marker")].map((b) => b.firstChild?.textContent ?? "");
  }

  function msg(id: string, role: Message["role"]): Message {
    return { id, role, content: "x", ts: 1 };
  }

  it("paints a resident turn the index has never seen", async () => {
    // Turn 3 is streaming, so the index the rail holds names only turns 1 and 2.
    setSessions([
      {
        id: "c-live",
        name: "c-live",
        model: "",
        acp_session_id: "",
        current_mode_id: "",
        usage: {
          context_pct: 0,
          context_size: 0,
          credits: 0,
          turn_count: 0,
          last_turn_ms: 0,
          has_real_data: false,
        },
        message_count: 6,
        messages: [
          msg("m1", "user"),
          msg("a1", "assistant"),
          msg("m2", "user"),
          msg("a2", "assistant"),
          msg("m3", "user"),
          msg("a3", "assistant"),
        ],
        has_more: false,
        thinking: false,
        working_label: "Thinking",
      },
    ]);
    setActive("c-live");
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });

    await loadTurnRail("c-live");

    expect(markers()).toEqual(["1", "2", "3"]);
  });
});

// ---------------------------------------------------------------------------
// Which CARD a marker jumps to.
//
// Two numbering spaces, both spelled `turn-{n}`: the rail's `TurnSummary.n` is
// session-absolute (the server owns it) while a card's DOM id is window-local
// (`Turn.n`, an ordinal inside the paginated store). The jump addressed the card by
// the marker's number, so it landed on whichever card happened to hold that WINDOW
// ordinal — wrong by exactly the number of turns paged out, which is zero on a
// short chat and grows with every page, hence "sometimes".
// ---------------------------------------------------------------------------

describe("which card a marker jumps to", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;
  /** The transcript the rail scopes its lookup to. */
  let view: HTMLElement;
  const mountedBodies: string[] = [];

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
    mountedBodies.length = 0;
    view = document.createElement("div");
    view.className = "transcript-view";
    document.body.appendChild(view);
    initTurnRailCallbacks({
      mountTurnBody: (_chat, turnID) => {
        mountedBodies.push(turnID);
        return Promise.resolve();
      },
      activeView: () => view,
    });
  });

  afterEach(() => {
    view.remove();
  });

  /** A resident card in the transcript: the WINDOW-LOCAL permalink id `messages.ts`
   *  writes, plus the reconcile key the rail joins on. The two disagreeing is the
   *  whole subject of this block. */
  function residentCard(windowN: number, messageID: string): HTMLElement {
    const e = document.createElement("div");
    e.className = "turn";
    e.id = `turn-${String(windowN)}`;
    e.setAttribute(KEY_ATTR, messageID);
    view.appendChild(e);
    return e;
  }

  function marker(n: number): HTMLButtonElement {
    const btn = [...rail.querySelectorAll<HTMLButtonElement>(".rail-marker")].find(
      (b) => b.firstChild?.textContent === String(n),
    );
    if (btn === undefined) {
      throw new Error(`no marker for turn ${String(n)}`);
    }
    return btn;
  }

  function msg(id: string): Message {
    return { id, role: "assistant", content: "", ts: 1 };
  }

  function paged(id: string, messages: Message[], hasMore: boolean): Session {
    return {
      id,
      name: id,
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      usage: {
        context_pct: 0,
        context_size: 0,
        credits: 0,
        turn_count: 0,
        last_turn_ms: 0,
        has_real_data: false,
      },
      message_count: messages.length,
      messages,
      has_more: hasMore,
      thinking: false,
      working_label: "Thinking",
    };
  }

  /** Wait out the whole operation: the body build, one frame, the scroll and its
   *  settle. The epoch's close is the one observable every exit reaches, including
   *  the exits that scroll nowhere. */
  async function settleJump(): Promise<void> {
    await vi.waitFor(() => {
      expect(scrollable.epochs).toContain("end");
    });
  }

  it("lands on the clicked turn's own card, not the one holding that window ordinal", async () => {
    // THE REGRESSION CASE. The session has 10 turns; the store holds absolute 5..10
    // as window ordinals 1..6. So `#turn-6` exists and is absolute turn TEN.
    vi.mocked(apiGet).mockResolvedValue({
      turns: Array.from({ length: 10 }, (_, i) => turn(i + 1)),
    });
    await loadTurnRail("c-paged");
    const wanted = residentCard(2, "m6");
    for (const [i, key] of ["m5", "m7", "m8", "m9", "m10"].entries()) {
      residentCard(i === 0 ? 1 : i + 2, key);
    }

    marker(6).click();
    await settleJump();

    expect(mountedBodies).toEqual(["m6"]);
    expect(wanted.dataset["railTarget"]).toBe("");
    // And the id it does NOT use, spelled out so a reader sees the two spaces:
    // pre-fix this element was the target.
    expect(view.querySelector("#turn-6")?.getAttribute(KEY_ATTR)).toBe("m10");
  });

  it("scrolls with the platform's own animation, and instantly under reduced motion", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-paged");
    residentCard(1, "m1");
    residentCard(2, "m2");

    marker(2).click();
    await settleJump();

    // ONE animation, and a correction never starts a second: `auto` is the only
    // behavior a correction may use.
    expect(scrollable.landings[0]?.behavior).toBe("smooth");
    expect(scrollable.landings.slice(1).every((l) => l.behavior === "auto")).toBe(true);
  });

  it("resolves inside the ACTIVE view, not a parked one", async () => {
    // Under the multiplexer a parked chat's cards stay resident, so the same
    // reconcile key exists once per view and a document-wide query answers in
    // document order — which is the parked one when it was mounted first.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-paged");
    const parked = document.createElement("div");
    parked.className = "transcript-view";
    const decoy = document.createElement("div");
    decoy.className = "turn";
    decoy.setAttribute(KEY_ATTR, "m2");
    parked.appendChild(decoy);
    // BEFORE the active view in document order, which is what makes the case bite.
    document.body.insertBefore(parked, view);
    const wanted = residentCard(2, "m2");

    marker(2).click();
    await settleJump();

    expect(wanted.dataset["railTarget"]).toBe("");
    expect(decoy.dataset["railTarget"]).toBeUndefined();
    parked.remove();
  });

  it("pages history in first, and says so while it waits", async () => {
    // The path the wrong-card lookup made unreachable: a resident neighbour always
    // resolved, so the fetch never ran and the pending marker never appeared for
    // the one case it exists for.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(5), turn(6), turn(7), turn(8)] });
    await loadTurnRail("c-page-in");
    setSessions([paged("c-page-in", [msg("m8")], true)]);
    setActive("c-page-in");
    residentCard(1, "m8");

    let pendingWhileWaiting = false;
    vi.mocked(loadMessages).mockImplementation(async () => {
      pendingWhileWaiting = marker(6).dataset["pending"] !== undefined;
      const s = get("c-page-in");
      if (s !== undefined) {
        s.messages = [msg("m6"), msg("m7"), ...s.messages];
      }
      residentCard(2, "m6");
      await Promise.resolve();
      // The real signature's answer: whether a page landed. The rail does not read
      // it — it re-inspects the store instead — but the mock has to be honest about
      // the shape or the type gate cannot check the call site.
      return true;
    });

    marker(6).click();
    await settleJump();

    expect(pendingWhileWaiting).toBe(true);
    expect(vi.mocked(loadMessages)).toHaveBeenCalledTimes(1);
    // The landing turn's body is built on demand, because a paginated landing is a
    // tier-3 stub — and it is built BEFORE anything scrolls, so the build's own
    // scroller write cannot land mid-animation.
    expect(mountedBodies).toEqual(["m6"]);
    expect(scrollable.landings.length).toBeGreaterThan(0);
    // The pending state is a fetch in flight, so it has to be gone afterwards.
    expect(marker(6).dataset["pending"]).toBeUndefined();
  });

  it("scrolls nowhere when the turn is neither resident nor reachable", async () => {
    // `has_more: false` and the target absent: the store cannot produce it, so the
    // loop stops rather than spinning, and the marker must not be left pending.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-gone");
    setSessions([paged("c-gone", [msg("m2")], false)]);
    setActive("c-gone");
    residentCard(1, "m2");

    marker(1).click();
    // The click's own render, so the wait below has something real to wait FOR — a
    // `waitFor` on a state that was never entered passes on its first poll and
    // asserts nothing.
    expect(marker(1).dataset["pending"]).toBe("");
    await settleJump();

    expect(scrollable.landings).toEqual([]);
    expect(vi.mocked(loadMessages)).not.toHaveBeenCalled();
    // The pending state means a fetch is in flight, so a dead end has to clear it —
    // a marker left pulsing forever is the same silence the state exists to break.
    expect(marker(1).dataset["pending"]).toBeUndefined();
  });

  it("does not let a superseded jump close the epoch the second one opened", async () => {
    // TWO CLICKS INSIDE ONE PAGING BUDGET. The first jump is still waiting on its
    // paging door when the second starts, and its own exit then runs while the second
    // is mid-flight. Unguarded, that exit's `endSelfScroll` closed the SECOND's
    // epoch — after which `autoScrollIfAnchored` re-pins to the live edge and the
    // reader is taken off the turn they clicked.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-overlap");
    setSessions([paged("c-overlap", [msg("m2")], true)]);
    setActive("c-overlap");
    const target = residentCard(2, "m2");

    // The FIRST jump's turn is off the resident window, so it holds inside the paging
    // door until the test releases it — and its page then reports no progress, which
    // is the early return that takes it straight to its own `finally`.
    let releaseFirst: (() => void) | undefined;
    const paging = new Promise<void>((resolve) => {
      releaseFirst = () => {
        resolve();
      };
    });
    vi.mocked(loadMessages).mockImplementation(async () => {
      await paging;
      return false;
    });

    // The SECOND jump's card is measured as MOVING, so its correction loop is still
    // running when the first one comes back. Releasing the first at the second
    // measurement is what puts its exit INSIDE the second's flight, and the third
    // measurement is a point where that exit has provably happened: everything left
    // of the first jump is microtasks, and a correction waits out a whole task.
    const epochsWhenFirstExited: string[] = [];
    let measured = 0;
    Object.defineProperty(target, "getBoundingClientRect", {
      configurable: true,
      value: () => {
        measured += 1;
        if (measured === 2) {
          releaseFirst?.();
        }
        if (measured === 3) {
          epochsWhenFirstExited.push(...scrollable.epochs);
        }
        // Settles from the fourth measurement, so the loop converges rather than
        // spending its whole budget.
        return fakeRect(Math.min(measured, 3) * 1000, 400);
      },
    });
    Object.defineProperty(target, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(0, 400)],
    });

    marker(1).click();
    marker(2).click();
    await vi.waitFor(() => {
      expect(epochsWhenFirstExited.length).toBeGreaterThan(0);
    });

    // The second jump's epoch is still the only one, and still open.
    expect(epochsWhenFirstExited).toEqual(["begin"]);

    await settleJump();
    // One bracket for the two clicks, and the landing the second click asked for.
    expect(scrollable.epochs).toEqual(["begin", "end"]);
    expect(target.dataset["railTarget"]).toBe("");
    expect(marker(2).dataset["selected"]).toBe("");
  });

  it("lets a second click on a paging marker be the jump already in flight", async () => {
    // The generation counter's own hazard: an impatient second click on a marker that
    // is still fetching would otherwise CLAIM the generation, be refused by the
    // pending gate, and leave the jump it was waiting on superseded — so the page
    // lands and nothing ever scrolls to it.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-double-click");
    setSessions([paged("c-double-click", [msg("m2")], true)]);
    setActive("c-double-click");
    residentCard(2, "m2");
    vi.mocked(loadMessages).mockImplementation(async () => {
      const s = get("c-double-click");
      if (s !== undefined) {
        s.messages = [msg("m1"), ...s.messages];
      }
      const landed = residentCard(1, "m1");
      await Promise.resolve();
      return landed !== null;
    });

    marker(1).click();
    marker(1).click();
    await settleJump();

    expect(vi.mocked(loadMessages)).toHaveBeenCalledTimes(1);
    expect(mountedBodies).toEqual(["m1"]);
    expect(scrollable.landings.length).toBeGreaterThan(0);
    expect(
      view.querySelector<HTMLElement>('[data-reconcile-key="m1"]')?.dataset["railTarget"],
    ).toBe("");
    expect(marker(1).dataset["pending"]).toBeUndefined();
  });

  it("stops a superseded jump's corrections from writing the scroller", async () => {
    // The other half of the same guard. A correction loop that keeps running after a
    // second click re-measures ITS card and writes a landing the reader has already
    // left, so the two jumps fight over the scroller for the rest of the budget.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-overlap-corrections");
    const first = residentCard(1, "m1");
    const second = residentCard(2, "m2");
    let measured = 0;
    Object.defineProperty(first, "getBoundingClientRect", {
      configurable: true,
      value: () => {
        measured += 1;
        // The second click lands INSIDE the first jump's correction loop, which is
        // the only place a stale correction can be observed at all.
        if (measured === 2) {
          marker(2).click();
        }
        return fakeRect(measured * 1000, 400);
      },
    });
    Object.defineProperty(first, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(0, 400)],
    });
    // A landing of its own that nothing else can produce, so the sequence below says
    // which jump wrote what.
    Object.defineProperty(second, "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(7000, 400),
    });
    Object.defineProperty(second, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(7000, 400)],
    });

    marker(1).click();
    await vi.waitFor(() => {
      expect(scrollable.landings.some((l) => l.px === 7000)).toBe(true);
    });
    await settleJump();

    // The first jump's aim and the correction it was already inside both stand; its
    // third measurement never happens.
    expect(scrollable.landings.map((l) => l.px)).toEqual([1000, 2000, 7000]);
  });

  it("closes the epoch on every exit, including the one that scrolls nowhere", async () => {
    // An epoch left open suspends pagination and silences every reader gesture, so
    // the close sits in a `finally` rather than after the scroll.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-epoch");
    setSessions([paged("c-epoch", [msg("m2")], false)]);
    setActive("c-epoch");

    marker(1).click();
    await settleJump();

    expect(scrollable.epochs).toEqual(["end"]);
  });
});

// ---------------------------------------------------------------------------
// A click always produces a reaction.
//
// `currentN` had one writer — the geometry pick — and a marker click called
// `jumpToTurn` and nothing else. With the target already at the reader's scroll
// position `scrollIntoView` is a no-op: no scroll event, no intersection change, no
// pick, no render. So clicking turn 3 while turns 2 and 3 were both fully visible
// produced NOTHING observable, and because dominance was by visible pixels the
// taller turn 2 kept the mark — the rail contradicting the reader's own choice.
// ---------------------------------------------------------------------------

describe("a click always produces a reaction", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;
  let view: HTMLElement;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
    view = document.createElement("div");
    view.className = "transcript-view";
    document.body.appendChild(view);
    initTurnRailCallbacks({
      mountTurnBody: () => Promise.resolve(),
      activeView: () => view,
    });
  });

  afterEach(() => {
    view.remove();
  });

  function residentCard(n: number, top: number): HTMLElement {
    const e = document.createElement("div");
    e.className = "turn";
    e.id = `turn-${String(n)}`;
    e.setAttribute(KEY_ATTR, `m${String(n)}`);
    Object.defineProperty(e, "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(top, 400),
    });
    Object.defineProperty(e, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(top, 400)],
    });
    view.appendChild(e);
    return e;
  }

  function marker(n: number): HTMLButtonElement {
    const btn = [...rail.querySelectorAll<HTMLButtonElement>(".rail-marker")].find(
      (b) => b.firstChild?.textContent === String(n),
    );
    if (btn === undefined) {
      throw new Error(`no marker for turn ${String(n)}`);
    }
    return btn;
  }

  async function settleJump(): Promise<void> {
    await vi.waitFor(() => {
      expect(scrollable.epochs).toContain("end");
    });
  }

  /** Turns 2 and 3 resident, the reading line at the top — so activation names turn
   *  2 and a click on turn 3 has almost nothing to scroll to. The reported scene. */
  async function bothVisible(): Promise<{ two: HTMLElement; three: HTMLElement }> {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });
    await loadTurnRail("c-both");
    const two = residentCard(2, 0);
    const three = residentCard(3, 400);
    setResidentTurns([two, three]);
    await frames();
    return { two, three };
  }

  it("marks the clicked marker even when the scroll cannot move", async () => {
    await bothVisible();
    expect(rail.querySelector(".rail-marker[data-current]")?.firstChild?.textContent).toBe("2");

    marker(3).click();
    await settleJump();

    const three = marker(3);
    const two = marker(2);
    expect(three.dataset["selected"]).toBe("");
    expect(three.getAttribute("aria-current")).toBe("true");
    expect(two.dataset["selected"]).toBeUndefined();
    expect(two.getAttribute("aria-current")).toBeNull();
    // And the scroll-derived mark is WITHHELD while the pick stands, because the two
    // share one filled treatment and the rail may claim only one position. They stay
    // separate attributes so a rule and a test can tell them apart.
    expect(two.dataset["current"]).toBeUndefined();
  });

  it("claims exactly one position, on exactly one marker", async () => {
    // The property behind the case above, stated so a future edit cannot write both
    // marks and paint two filled markers.
    await bothVisible();
    expect(rail.querySelectorAll("[data-current], [data-selected]")).toHaveLength(1);

    marker(3).click();
    await settleJump();
    expect(rail.querySelectorAll("[data-current], [data-selected]")).toHaveLength(1);
    expect(rail.querySelector("[data-selected]")?.firstChild?.textContent).toBe("3");

    scrollable.readerGesture?.();
    expect(rail.querySelectorAll("[data-current], [data-selected]")).toHaveLength(1);
  });

  it("keeps exactly one marker claiming to be current", async () => {
    await bothVisible();
    marker(3).click();
    await settleJump();

    expect(rail.querySelectorAll("[aria-current='true']")).toHaveLength(1);
  });

  it("marks the pick even when it IS the turn activation already names", async () => {
    // The coincident case, and the reason the two marks are exclusive rather than
    // additive: clicking the marker the reading line already named has to read as a
    // pick, or the rail stops tracking and shows nothing to say why.
    await bothVisible();
    marker(2).click();
    await settleJump();

    expect(marker(2).dataset["selected"]).toBe("");
    expect(marker(2).dataset["current"]).toBeUndefined();
    expect(marker(2).getAttribute("aria-current")).toBe("true");
    expect(rail.querySelectorAll("[data-current], [data-selected]")).toHaveLength(1);
  });

  it("flashes the landing card, then takes the ring away", async () => {
    await bothVisible();
    const three = view.querySelector<HTMLElement>('[data-reconcile-key="m3"]');

    marker(3).click();
    await settleJump();
    expect(three?.dataset["railTarget"]).toBe("");

    await vi.waitFor(() => {
      expect(three?.dataset["railTarget"]).toBeUndefined();
    });
  });

  it("moves the ring to the newest landing rather than letting the first timer strip it", async () => {
    await bothVisible();
    marker(3).click();
    await settleJump();
    marker(2).click();
    await vi.waitFor(() => {
      expect(
        view.querySelector<HTMLElement>('[data-reconcile-key="m2"]')?.dataset["railTarget"],
      ).toBe("");
    });

    // A shared timer would have fired on the FIRST click's deadline and cleared the
    // card the reader just landed on.
    expect(
      view.querySelector<HTMLElement>('[data-reconcile-key="m3"]')?.dataset["railTarget"],
    ).toBeUndefined();
  });

  it("hands tracking back on a reader gesture", async () => {
    await bothVisible();
    marker(3).click();
    await settleJump();
    expect(marker(3).dataset["selected"]).toBe("");

    // Through the real seam: the rail subscribed at mount, and this is the callback
    // scroll.ts publishes for a reader gesture. The rail cannot tell WHICH gesture
    // it was and must not care — a scroll and a request for the live edge are both
    // the reader stating a position — so which writes publish it is pinned in
    // `scroll.test.ts`, over a real scroller, rather than restated here.
    scrollable.readerGesture?.();

    expect(marker(3).dataset["selected"]).toBeUndefined();
    expect(rail.querySelector(".rail-marker[data-current]")).not.toBeNull();
  });

  it("drops a pick the arriving index no longer names", async () => {
    // THE REWIND. Rewind lives in the turn footer, so picking a marker and then
    // reverting the session is two clicks apart — and the pick is held by the
    // turn's opening-message id, which that index no longer carries. Without the
    // drop the rail marks NO position on any row: `markerNode` withholds
    // `data-current` while a pick stands, and matches `data-selected` on a turn that
    // is gone.
    const { two } = await bothVisible();
    marker(3).click();
    await settleJump();
    expect(rail.querySelector("[data-selected]")?.firstChild?.textContent).toBe("3");

    // A rewind truncates the session, so the turn's card leaves the transcript and
    // the next index no longer names it.
    setResidentTurns([two]);
    await frames();
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await refreshTurnRail("c-both");

    expect(rail.querySelector("[data-selected]")).toBeNull();
    expect(rail.querySelectorAll("[data-current], [data-selected]")).toHaveLength(1);
    expect(rail.querySelectorAll("[aria-current='true']")).toHaveLength(1);
  });

  it("keeps a pick the arriving index still names", async () => {
    // The control, and the reason the case above cannot pass for the wrong reason: a
    // refresh must not revoke the reader's pick just for arriving. The index is
    // refetched at every turn end, so a rail that dropped the pick per index would
    // lose it on the next turn of the very conversation being read.
    await bothVisible();
    marker(3).click();
    await settleJump();

    // A fourth turn arrives — a later index that still carries `m3`.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3), turn(4)] });
    await refreshTurnRail("c-both");

    expect(rail.querySelector("[data-selected]")?.firstChild?.textContent).toBe("3");
    expect(rail.querySelector("[data-current]")).toBeNull();
  });

  it("follows the picked turn through a renumbering rather than the number it wore", async () => {
    // The pick is an id, so an index that renumbers the same turns — an older turn
    // dropping off a session the server recounted — moves the mark WITH the turn.
    // Held by number, `data-selected` would have stayed on whatever now wears 3.
    await bothVisible();
    marker(3).click();
    await settleJump();
    expect(rail.querySelector("[data-selected]")?.firstChild?.textContent).toBe("3");

    setSessions([]);
    vi.mocked(apiGet).mockResolvedValue({
      turns: [turn(1, { id: "m2" }), turn(2, { id: "m3" })],
    });
    await refreshTurnRail("c-both");

    expect(rail.querySelector("[data-selected]")?.firstChild?.textContent).toBe("2");
    expect(rail.querySelectorAll("[aria-current='true']")).toHaveLength(1);
    expect(rail.querySelector("[aria-current='true']")?.firstChild?.textContent).toBe("2");
  });

  it("survives a table rebuild that moves activation to a different turn", async () => {
    // Deliberately NOT cleared by activation moving: a streaming turn's own growth
    // moves the reading line's answer with no reader gesture behind it, and dropping
    // the pick there would revoke the reader's choice while they sit perfectly still.
    const { two, three } = await bothVisible();
    marker(2).click();
    await settleJump();
    expect(marker(2).dataset["selected"]).toBe("");

    scrollable.el.scrollTop = 500;
    setResidentTurns([two, three]);
    await frames();

    expect(marker(2).dataset["selected"]).toBe("");
    expect(rail.querySelector("[data-current]")).toBeNull();
  });

  it("drops the selection on a chat switch", async () => {
    await bothVisible();
    marker(3).click();
    await settleJump();

    pointTurnRail("c-other");
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });
    await refreshTurnRail("c-other");

    expect(rail.querySelector(".rail-marker[data-selected]")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// What a rail row SAYS. The composition is `rail-labels.test.ts`'s subject; these
// cases pin that the renderer publishes it, on both channels, and that the native
// `title` is gone — as a UA tooltip it missed the styled treatment every other
// hover in the app uses AND published no `aria-describedby`, so it reached mouse
// users only.
// ---------------------------------------------------------------------------

describe("what a rail row says", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
    // The track is the module's own element, so a case that shortens it to force a
    // downsample hands the next one a four-row rail if it fails before restoring it.
    rail.style.height = "600px";
  });

  function rows(sel: string): HTMLElement[] {
    return [...rail.querySelectorAll<HTMLElement>(sel)];
  }

  it("carries data-tooltip and no native title on every marker", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(12) });
    await loadTurnRail("c-say");

    const all = rows(".rail-marker");
    expect(all.length).toBeGreaterThan(1);
    for (const row of all) {
      expect(row.getAttribute("title"), row.className).toBeNull();
      expect(row.getAttribute("data-tooltip"), row.className).not.toBe("");
      expect(row.getAttribute("aria-label"), row.className).not.toBe("");
    }
  });

  it("names an agent-initiated turn in the accessible NAME, not only in a border style", async () => {
    // The defect: `data-trigger="system"` rendered as a dashed italic border and
    // nothing else, and the server leaves `first_line` empty for a non-user turn, so
    // the hover fell back to `Turn 4` and said nothing either.
    vi.mocked(apiGet).mockResolvedValue({
      turns: [turn(1, { first_line: "do it" }), turn(2, { agent_initiated: true })],
    });
    await loadTurnRail("c-agent");
    const [user, agent] = rows(".rail-marker");

    expect(agent?.dataset["trigger"]).toBe("system");
    expect(agent?.getAttribute("aria-label")).toBe("Go to turn 2, agent-initiated");
    expect(agent?.getAttribute("data-tooltip")).toBe("Agent-initiated turn");
    expect(user?.getAttribute("aria-label")).toBe("Go to turn 1");
    expect(user?.getAttribute("data-tooltip")).toBe("do it");
  });

  it("names a non-clean outcome and stays quiet about a clean one", async () => {
    vi.mocked(apiGet).mockResolvedValue({
      turns: [turn(1), turn(2, { outcome: "failed" }), turn(3, { outcome: "unknown" })],
    });
    await loadTurnRail("c-outcomes");
    const [clean, failed, unknown] = rows(".rail-marker");

    expect(clean?.getAttribute("aria-label")).toBe("Go to turn 1");
    expect(failed?.getAttribute("aria-label")).toBe("Go to turn 2, failed");
    expect(failed?.getAttribute("data-tooltip")).toContain("This turn failed");
    expect(unknown?.getAttribute("aria-label")).toBe("Go to turn 3, unknown");
    expect(unknown?.getAttribute("data-tooltip")).toContain("could not be read");
  });

  it("names the seam's pause and the two turns it separates, and paints no text", async () => {
    vi.mocked(apiGet).mockResolvedValue({
      turns: [turn(1, { ts: 0 }), turn(2, { ts: 120 * MINUTE })],
    });
    await loadTurnRail("c-seam");

    const [seam] = rows(".rail-seam");
    expect(seam).not.toBeUndefined();
    expect(seam?.textContent).toBe("");
    expect(seam?.getAttribute("role")).toBe("separator");
    expect(seam?.getAttribute("aria-label")).toBe("2h pause between turn 1 and turn 2");
    // A band on the axis rather than a row, so it charges nothing against the
    // markers the track can hold.
    expect(rows(".rail-marker")).toHaveLength(2);
  });

  it("puts the pause on the marker BELOW the seam, and on no other", async () => {
    // The band paints no text, so a reader hovering the turn that opens the new
    // sitting is the only one the pause reaches. Keyed by the turn below it: on the
    // turn above, the sentence would describe a pause that has not happened yet.
    vi.mocked(apiGet).mockResolvedValue({
      turns: [turn(1, { ts: 0 }), turn(2, { ts: 120 * MINUTE }), turn(3, { ts: 121 * MINUTE })],
    });
    await loadTurnRail("c-seam-tip");

    const tips = rows(".rail-marker").map((m) => m.getAttribute("data-tooltip"));
    expect(tips).toEqual(["Turn 1", "Turn 2 \u00b7 2h pause before this turn", "Turn 3"]);
  });

  it("bands no pause between two markers a downsample left non-adjacent", async () => {
    // THE REGRESSION CASE, and it needs a rail past its own capacity to bite: 60
    // turns five minutes apart on a four-row track, so every SURVIVING pair is more
    // than the threshold apart in time while nobody ever stopped. The one real pause
    // is between 30 and 31, which the downsample drops — so it is drawn nowhere
    // rather than claimed between whichever markers happen to bracket it.
    const pitchPx = railMetrics(rail).pitchPx;
    rail.style.height = `${String(railFor(4, pitchPx))}px`;
    vi.mocked(apiGet).mockResolvedValue({
      turns: Array.from({ length: 60 }, (_, i) =>
        turn(i + 1, { ts: i * 5 * MINUTE + (i >= 30 ? 120 * MINUTE : 0) }),
      ),
    });
    await loadTurnRail("c-seam-downsampled");

    const shown = rows(".rail-marker").map((m) => Number(m.firstChild?.textContent));
    expect(shown.length).toBeLessThan(60);
    // The premise, or the case could pass for a reason that has nothing to do with
    // adjacency: the two markers really are far enough apart in TIME to have earned a
    // band under the old rule.
    const [first = 0, second = 0] = shown;
    expect((second - first) * 5).toBeGreaterThan(GAP_MINUTES);
    expect(rows(".rail-seam")).toHaveLength(0);
    // And the marker channel says nothing either: the sentence is keyed by the seam's
    // own `toN`, so a withheld band withholds it too.
    expect(
      rows(".rail-marker")
        .map((m) => m.getAttribute("data-tooltip") ?? "")
        .join(" "),
    ).not.toContain("pause");
    rail.style.height = "600px";
  });

  it("still bands a real pause on a rail that shows every turn", async () => {
    // The control the case above cannot pass without: a fix that emits nothing would
    // satisfy it. Here the pause's own two turns both have markers and are neighbours
    // on the axis, which is the whole condition.
    vi.mocked(apiGet).mockResolvedValue({
      turns: Array.from({ length: 12 }, (_, i) =>
        turn(i + 1, { ts: i * MINUTE + (i >= 6 ? 120 * MINUTE : 0) }),
      ),
    });
    await loadTurnRail("c-seam-whole");

    expect(rows(".rail-marker")).toHaveLength(12);
    const seams = rows(".rail-seam");
    expect(seams).toHaveLength(1);
    expect(seams[0]?.getAttribute("aria-label")).toBe("2h pause between turn 6 and turn 7");
  });

  it("states the set it shows once the rail is downsampled", async () => {
    const pitchPx = railMetrics(rail).pitchPx;
    rail.style.height = `${String(railFor(4, pitchPx))}px`;
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(60) });
    await loadTurnRail("c-many");

    const shown = rows(".rail-marker").length;
    expect(shown).toBeLessThan(60);
    expect(rail.getAttribute("aria-label")).toBe(
      `Turn timeline, showing ${String(shown)} of 60 turns`,
    );
    rail.style.height = "600px";
  });

  it("says nothing about a set it shows whole", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(3) });
    await loadTurnRail("c-few");

    expect(rail.getAttribute("aria-label")).toBe("Turn timeline");
  });
});

// ---------------------------------------------------------------------------
// The reader's own POSITION is a different kind of thing from a turn marker, so on a
// downsampled rail it gets its own element: past the track's capacity the turn the
// reader is in may carry no marker at all, and without the caret their position
// would be marked nowhere. It is drawn from the SET rather than from the scroll
// offset, so nothing appears or disappears as they scroll.
// ---------------------------------------------------------------------------

describe("the reader's position on a downsampled rail", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
  });

  /** Seat one resident card, so the reading line names a turn. */
  async function seatAt(id: string, index: TurnSummary[], n: number): Promise<void> {
    vi.mocked(apiGet).mockResolvedValue({ turns: index });
    await loadTurnRail(id);
    const e = document.createElement("div");
    e.className = "turn";
    e.setAttribute(KEY_ATTR, `m${String(n)}`);
    Object.defineProperty(e, "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(0, 400),
    });
    Object.defineProperty(e, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(0, 400)],
    });
    setResidentTurns([e]);
    await frames();
  }

  it("draws the caret at the marked turn's own fraction", async () => {
    rail.style.height = `${String(railFor(4, railMetrics(rail).pitchPx))}px`;
    await seatAt("c-here", turns(60), 30);

    const here = rail.querySelector<HTMLElement>(".rail-here");
    expect(here).not.toBeNull();
    // The same `at()` the markers read, so the caret and a marker for that turn
    // cannot claim two positions.
    expect(here?.style.getPropertyValue("--rail-at")).toBe(String(29 / 59));
    rail.style.height = "600px";
  });

  it("is not a hit target, so it competes for no slot", async () => {
    rail.style.height = `${String(railFor(4, railMetrics(rail).pitchPx))}px`;
    await seatAt("c-here-a11y", turns(60), 30);

    const here = rail.querySelector<HTMLElement>(".rail-here");
    expect(here?.tagName).toBe("DIV");
    expect(here?.getAttribute("aria-hidden")).toBe("true");
    rail.style.height = "600px";
  });

  it("is absent while every turn has a marker of its own", async () => {
    await seatAt("c-here-none", turns(3), 2);

    expect(rail.querySelectorAll(".rail-marker")).toHaveLength(3);
    expect(rail.querySelector(".rail-here")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// THE TURN'S DURATION on the rail.
//
// The user asked for the turn's time to move into the turn's own box on hover. On the
// rail that value does not exist on the rail's feed: `GET /api/chats/{id}/turns`
// carries no duration, and the number the footer renders is `turn_elapsed_ms` on a
// turn's final assistant message, summed across the body. So the rail derives it from
// the transcript STORE, which is a paginated window — and the honest consequence is
// that a turn outside that window gets no slot rather than a guessed one.
//
// These cases pin the derivation and the gap. The reveal itself (reserved box,
// opacity, keyboard reach, nothing else moves) is `rail-mark-css.test.ts`'s subject,
// over real layout.
// ---------------------------------------------------------------------------

describe("the duration a rail marker can show", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
  });

  /** A turn as the STORE holds it: the user message that opens it (whose id is what
   *  the rail's index joins on) plus one assistant message carrying the stamp. The
   *  prompt text is a parameter because the merge takes the RESIDENT turn's label
   *  over the index's, which is the whole point of the merge. */
  function storedTurn(
    n: number,
    opts: { elapsedMs?: number; prompt?: string; outcome?: TurnOutcome } = {},
  ): Message[] {
    const opener: Message = {
      id: `m${String(n)}`,
      role: "user",
      content: opts.prompt ?? "ask",
      ts: n * 1000,
    };
    const reply: Message = {
      id: `a${String(n)}`,
      role: "assistant",
      content: "answer",
      ts: n * 1000 + 1,
      ...(opts.elapsedMs === undefined ? {} : { turn_elapsed_ms: opts.elapsedMs }),
      ...(opts.outcome === undefined ? {} : { turn_outcome: opts.outcome }),
    };
    return [opener, reply];
  }

  function seed(chatID: string, messages: Message[]): void {
    setSessions([
      {
        id: chatID,
        name: chatID,
        model: "",
        acp_session_id: "",
        current_mode_id: "",
        usage: {
          context_pct: 0,
          context_size: 0,
          credits: 0,
          turn_count: 0,
          last_turn_ms: 0,
          has_real_data: false,
        },
        message_count: messages.length,
        messages,
        has_more: true,
        thinking: false,
        working_label: "Thinking",
      },
    ]);
    setActive(chatID);
  }

  function marker(n: number): HTMLElement {
    const found = [...rail.querySelectorAll<HTMLElement>(".rail-marker")].find(
      (b) => b.firstChild?.textContent === String(n),
    );
    if (found === undefined) {
      throw new Error(`no marker for turn ${String(n)}`);
    }
    return found;
  }

  function slot(n: number): HTMLElement | null {
    return marker(n).querySelector<HTMLElement>(".rail-marker-time");
  }

  it("renders the turn's own duration, in both spellings", async () => {
    seed("c-dur", storedTurn(1, { elapsedMs: 92_000 }));
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });
    await loadTurnRail("c-dur");

    const time = slot(1);
    expect(time).not.toBeNull();
    // Hardcoded rather than computed through the formatters the renderer uses, or the
    // case would assert the code against itself. 92s is `1m 32s` / `PT1M32S`.
    expect(time?.textContent).toBe("1m 32s");
    expect(time?.getAttribute("datetime")).toBe("PT1M32S");
    // A `<time>`, because the two spellings are the machine and human forms of one
    // value and the turn footer's own slot already made that pairing the convention.
    expect(time?.tagName).toBe("TIME");
  });

  it("sums the turn's body rather than reading one message", async () => {
    // A turn splits across two assistant messages when the model is switched mid-turn,
    // and each carries its own stamp. `turnLedger` owns the sum; this is the case that
    // proves the rail goes through it rather than taking the last value it sees.
    const messages = storedTurn(1, { elapsedMs: 60_000 });
    messages.push({
      id: "a1b",
      role: "assistant",
      content: "more",
      ts: 1002,
      turn_elapsed_ms: 32_000,
    });
    seed("c-sum", messages);
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });
    await loadTurnRail("c-sum");

    expect(slot(1)?.textContent).toBe("1m 32s");
  });

  it("shows nothing for a turn the store does not hold", async () => {
    // THE HONEST GAP. The rail spans the session; the store holds a window. Turn 1 is
    // resident, turn 2 is not, and the rail cannot know turn 2's duration without a
    // wire field it has not got — so that marker carries no slot at all.
    seed("c-window", storedTurn(1, { elapsedMs: 92_000 }));
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-window");

    expect(slot(1)).not.toBeNull();
    expect(slot(2)).toBeNull();
  });

  it("shows nothing for a resident turn nobody stamped", async () => {
    // A duration nobody stamped is not a duration of zero — the rule the turn footer's
    // own slot follows — so an unstamped turn gets no element rather than `0.0s`.
    seed("c-unstamped", storedTurn(1));
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });
    await loadTurnRail("c-unstamped");

    expect(marker(1).firstChild?.textContent).toBe("1");
    expect(slot(1)).toBeNull();
  });

  it("reads the store at RENDER time, so a turn that pages in gains its slot", async () => {
    // The map is rebuilt per render rather than captured with the fetch: `ingestMessage`
    // upserts in place, so an array identity is not a version and a cached answer would
    // go stale exactly when history arrives.
    seed("c-late", []);
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });
    await loadTurnRail("c-late");
    expect(slot(1)).toBeNull();

    const session = get("c-late");
    if (session === undefined) {
      throw new Error("session gone");
    }
    session.messages = storedTurn(1, { elapsedMs: 92_000 });
    await refreshTurnRail("c-late");

    expect(slot(1)?.textContent).toBe("1m 32s");
  });

  it("puts the duration in the DESCRIPTION channel and keeps the name short", async () => {
    // `aria-label` wins over a button's own text, so the slot's words never reach a
    // screen reader; the tooltip is republished as `aria-describedby`, which is the
    // channel the footer's own hover-revealed slot uses for the same reason. The NAME
    // is read on every focus and stays what it was, and the two channels stay
    // different, which is the rule the seam's own labels also state.
    seed(
      "c-channels",
      storedTurn(1, { elapsedMs: 92_000, prompt: "do the thing", outcome: "failed" }),
    );
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });
    await loadTurnRail("c-channels");

    const btn = marker(1);
    expect(btn.getAttribute("data-tooltip")).toBe(
      "do the thing \u00b7 This turn failed \u00b7 1m 32s",
    );
    expect(btn.getAttribute("aria-label")).toBe("Go to turn 1, failed");
    expect(btn.getAttribute("data-tooltip")).not.toBe(btn.getAttribute("aria-label"));
  });

  it("says nothing about a duration it does not have", async () => {
    seed("c-quiet", storedTurn(1, { prompt: "do the thing" }));
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });
    await loadTurnRail("c-quiet");

    expect(marker(1).getAttribute("data-tooltip")).toBe("do the thing");
  });
});
