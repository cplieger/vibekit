// The rail's layout arithmetic: when per-turn markers fit, when they must
// compress, and how time becomes space. Driven through railRows rather than the
// DOM because the interesting part is the capacity rule, and asserting it
// through rendered pixels would test the browser instead. (The harness only
// because the module's scroll.ts import self-initialises against `document`.)
//
// The rest of the file does need the DOM. The LIFECYCLE block covers which chat
// the rail currently belongs to — module state, and both directions of getting it
// wrong were live defects; the block after it covers whether the rail is worth
// showing at all; and the last covers which turn it calls current, over a faked
// IntersectionObserver.
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest";

// scroll.ts self-initialises a singleton against #messages at import time, and
// the rail imports it to park the reader on jump and to ask how far the
// transcript can scroll. Neither is under test here, so stub it rather than
// staging the whole chat DOM.
//
// `scrollable.by` is the transcript's scroll room. It has to come through
// vi.hoisted: the factory below is hoisted above these declarations, so a plain
// const would be a ReferenceError inside it. The default is comfortably
// navigable, because every case outside the visibility block is about the index
// rather than about whether the rail is worth showing.
const { scrollable } = vi.hoisted(() => ({
  scrollable: {
    by: 500,
    viewportBottom: 600,
    onScroll: undefined as (() => void) | undefined,
    /** The rail's `onReaderGesture` registration, so a case can fire the READER
     *  gesture without a real scroller. Distinct from `onScroll` above on purpose:
     *  that one is the raw listener the rail attaches for its own re-measure, and
     *  the whole point of the seam is that the controller's own scrolls do not
     *  reach this one. Which gestures publish it — a reader scroll AND a request for
     *  the live edge — is `scroll.test.ts`'s subject, over a real scroller. */
    onReaderGesture: undefined as (() => void) | undefined,
  },
}));
vi.mock("./scroll.js", () => ({
  jumpTo: vi.fn(),
  scrollableBy: () => scrollable.by,
  onReaderGesture: (cb: () => void) => {
    scrollable.onReaderGesture = cb;
    return () => {
      scrollable.onReaderGesture = undefined;
    };
  },
  getScrollEl: () => ({
    addEventListener: (type: string, fn: EventListener) => {
      if (type === "scroll") {
        scrollable.onScroll = () => {
          fn(new Event("scroll"));
        };
      }
    },
    getBoundingClientRect: () => ({
      top: 0,
      bottom: scrollable.viewportBottom,
      height: scrollable.viewportBottom,
    }),
  }),
}));
// The session-wide index is the rail's own fetch; the lifecycle cases below
// decide what it does with the answer, not how it asks.
vi.mock("./api-client.js", () => ({ apiGet: vi.fn() }));

// The pagination door `jumpToTurn` walks when its target is off the resident
// window. Mocked because the real one is a network fetch, and what these cases
// assert is the rail's own sequencing around it: pending marker, page, re-resolve,
// scroll.
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

import {
  railRows,
  ROW_PITCH_PX,
  mountTurnRail,
  loadTurnRail,
  observeTurns,
  pointTurnRail,
  refreshTurnRail,
  resetTurnRail,
  initTurnRailCallbacks,
  type TurnSummary,
} from "./turn-rail.js";
import { apiGet } from "./api-client.js";
import { jumpTo } from "./scroll.js";
import { loadMessages } from "./store-load.js";
import { setSessions, setActive, get, bumpSyncEpoch } from "./store.js";
import type { Message, Session } from "./types.js";
import { KEY_ATTR } from "@cplieger/reactive";
import type { TurnOutcome } from "./turns.js";

const MINUTE = 60_000;

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

/** A rail tall enough for `n` rows at the pitch production lays them out in. */
function railFor(n: number): number {
  return n * ROW_PITCH_PX;
}

describe("railRows capacity", () => {
  it("renders one marker per turn while they fit", () => {
    const rows = railRows(turns(10), railFor(20));
    expect(rows).toHaveLength(10);
    expect(rows.every((r) => r.kind === "turn")).toBe(true);
  });

  it("renders nothing for a session with no turns", () => {
    expect(railRows([], railFor(20))).toEqual([]);
  });

  it("fills the rail exactly at capacity without compressing", () => {
    const rows = railRows(turns(20), railFor(20));
    expect(rows).toHaveLength(20);
    expect(rows.every((r) => r.kind === "turn")).toBe(true);
  });

  // The threshold is computed from the measured height, never a constant: the
  // rail is responsive, so any hardcoded turn count is wrong at some viewport.
  it("compresses one turn past capacity", () => {
    const rows = railRows(turns(21), railFor(20));
    expect(rows.some((r) => r.kind === "cluster")).toBe(true);
    expect(rows.length).toBeLessThanOrEqual(20);
  });

  it("moves the threshold with the rail's height", () => {
    // The same 30 turns: clustered in a short rail, direct in a tall one.
    expect(railRows(turns(30), railFor(10)).some((r) => r.kind === "cluster")).toBe(true);
    expect(railRows(turns(30), railFor(40)).every((r) => r.kind === "turn")).toBe(true);
  });

  // The arithmetic that killed the "every marker is still clickable" promise:
  // 300 turns in a 900px rail is 3px each, so a per-turn hit area is a false
  // promise however many dots get painted.
  it("never emits more rows than the rail can give a conforming target", () => {
    const height = 900;
    const rows = railRows(turns(300), height);
    expect(rows.length).toBeLessThanOrEqual(Math.floor(height / 24));
  });

  it("keeps every turn reachable through some row", () => {
    const rows = railRows(turns(300), 900);
    const covered = new Set<number>();
    for (const r of rows) {
      if (r.kind === "turn") {
        covered.add(r.s.n);
      } else if (r.kind === "cluster") {
        for (let n = r.from; n <= r.to; n++) {
          covered.add(n);
        }
      }
    }
    for (let n = 1; n <= 300; n++) {
      expect(covered.has(n), `turn ${String(n)} unreachable`).toBe(true);
    }
  });

  it("degrades to a single row rather than zero on an unmeasurably short rail", () => {
    const rows = railRows(turns(50), 0);
    expect(rows.length).toBeGreaterThan(0);
  });
});

describe("railRows clustering", () => {
  it("labels a cluster with its range and its size", () => {
    const rows = railRows(turns(100), railFor(10));
    const first = rows.find((r) => r.kind === "cluster");
    expect(first).toBeDefined();
    if (first?.kind !== "cluster") {
      throw new Error("expected a cluster");
    }
    expect(first.from).toBe(1);
    expect(first.to).toBeGreaterThan(first.from);
    expect(first.count).toBe(first.to - first.from + 1);
  });

  // A range holding one failure is a range you want to look at, so the cluster
  // reports its worst member rather than an average or its first.
  it("reports a cluster's worst outcome", () => {
    const all = turns(40);
    const twenty = all[19];
    if (twenty === undefined) {
      throw new Error("fixture");
    }
    twenty.outcome = "failed";
    const rows = railRows(all, railFor(4));
    const owning = rows.find((r) => r.kind === "cluster" && r.from <= 20 && r.to >= 20);
    if (owning?.kind !== "cluster") {
      throw new Error("expected a cluster covering turn 20");
    }
    expect(owning.outcome).toBe("failed");
  });

  it("prefers failed over interrupted in the same cluster", () => {
    const all = turns(40);
    const a = all[0];
    const b = all[1];
    if (a === undefined || b === undefined) {
      throw new Error("fixture");
    }
    a.outcome = "interrupted";
    b.outcome = "failed";
    const rows = railRows(all, railFor(4));
    const first = rows[0];
    if (first?.kind !== "cluster") {
      throw new Error("expected a cluster");
    }
    expect(first.outcome).toBe("failed");
  });

  it("prefers interrupted over unknown, so a cluster never reads calmer than its worst member", () => {
    // The rank table is a total ORDER and cannot be derived from `severityOf` —
    // that table's four buckets carry no order — but it MAY NOT CONTRADICT the hue
    // partition, and it did: `interrupted` ranked BELOW `unknown`, so this cluster
    // painted the neutral ink of an unreadable end (29-turns.css's stated `unknown`
    // exception) while the interrupted turn's own marker painted red.
    const all = turns(40);
    const a = all[0];
    const b = all[1];
    if (a === undefined || b === undefined) {
      throw new Error("fixture");
    }
    a.outcome = "unknown";
    b.outcome = "interrupted";
    const rows = railRows(all, railFor(4));
    const first = rows[0];
    if (first?.kind !== "cluster") {
      throw new Error("expected a cluster");
    }
    expect(first.outcome).toBe("interrupted");
  });

  it("restricts the rows to the zoomed range", () => {
    const rows = railRows(turns(300), 900, { from: 50, to: 60 });
    expect(rows).toHaveLength(11);
    expect(rows.every((r) => r.kind === "turn")).toBe(true);
    const first = rows[0];
    if (first?.kind !== "turn") {
      throw new Error("expected a turn row");
    }
    expect(first.s.n).toBe(50);
  });

  it("returns nothing for a zoom range that matches no turn", () => {
    expect(railRows(turns(10), 900, { from: 500, to: 600 })).toEqual([]);
  });
});

describe("railRows gap markers", () => {
  it("inserts a gap when turns are far apart in real time", () => {
    const rows = railRows(
      [turn(1, { ts: 0 }), turn(2, { ts: 60 * MINUTE }), turn(3, { ts: 61 * MINUTE })],
      railFor(20),
    );
    expect(rows.map((r) => r.kind)).toEqual(["turn", "gap", "turn", "turn"]);
  });

  it("leaves an ordinary pause alone", () => {
    const rows = railRows([turn(1, { ts: 0 }), turn(2, { ts: 19 * MINUTE })], railFor(20));
    expect(rows.every((r) => r.kind === "turn")).toBe(true);
  });

  it("carries the elapsed time so the row can name it", () => {
    const rows = railRows([turn(1, { ts: 0 }), turn(2, { ts: 120 * MINUTE })], railFor(20));
    const gap = rows.find((r) => r.kind === "gap");
    if (gap?.kind !== "gap") {
      throw new Error("expected a gap");
    }
    expect(gap.ms).toBe(120 * MINUTE);
  });

  // Gap rows take space a marker would otherwise have, so capacity has to
  // subtract them or the rail overflows exactly when the session has seams.
  it("charges gap rows against the marker capacity", () => {
    const height = railFor(10);
    const noGaps = railRows(turns(10), height);
    expect(noGaps.every((r) => r.kind === "turn")).toBe(true);

    const withGaps = Array.from({ length: 10 }, (_, i) => turn(i + 1, { ts: i * 60 * MINUTE }));
    const rows = railRows(withGaps, height);
    expect(rows.length).toBeLessThanOrEqual(10);
    expect(rows.some((r) => r.kind === "cluster")).toBe(true);
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
    document.body.appendChild(host);
    mountTurnRail(host);
  });

  beforeEach(() => {
    scrollable.by = 500;
    resetTurnRail();
  });

  function rail(): HTMLElement {
    const el = host.querySelector<HTMLElement>(".turn-rail");
    if (el === null) {
      throw new Error("rail not mounted");
    }
    return el;
  }

  /** The marker labels currently painted, in order. */
  function markers(): string[] {
    return [...rail().querySelectorAll(".rail-marker")].map((b) => b.textContent ?? "");
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
// including the one the IntersectionObserver structurally cannot cover.
describe("the rail only appears once the transcript can be scrolled", () => {
  const host = document.createElement("div");

  beforeAll(() => {
    document.body.appendChild(host);
    // Idempotent, and the rail is a module singleton: if a block above already
    // mounted it, this is a no-op and the element is in THAT host. So resolve it
    // from the document rather than from `host`, which keeps this block correct
    // both in file order and on its own under a `-t` filter.
    mountTurnRail(host);
  });

  beforeEach(() => {
    scrollable.by = 500;
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
    return [...rail().querySelectorAll(".rail-marker")].map((b) => b.textContent ?? "");
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

    // The transcript grew — a streaming turn, or a page of history landing. This
    // is the case the IntersectionObserver cannot see: the turn IN VIEW has not
    // changed, so only observeTurns' own check re-renders here.
    scrollable.by = 500;
    observeTurns([]);

    expect(markers()).toEqual(["1", "2"]);
  });

  it("goes away again when the transcript stops being scrollable", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-shrinks");
    expect(markers()).toEqual(["1", "2"]);

    // A window the reader just made taller, or turns folding away.
    scrollable.by = 0;
    observeTurns([]);

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
    observeTurns([]);
    expect(markers()).toEqual(["1"]);
  });

  it("re-renders only when the answer actually changed", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-stable");
    const first = rail().querySelector(".rail-marker");

    // Same navigability, so the paint must not rebuild the rows: a rebuild per
    // streamed chunk would discard the node under the reader's pointer.
    observeTurns([]);

    expect(rail().querySelector(".rail-marker")).toBe(first);
  });
});

// ---------------------------------------------------------------------------
// Which turn the rail calls current.
//
// The rule: the turn occupying the most VERTICAL pixels of the transcript
// viewport is active. A 20px sliver never beats a 500px turn, whichever one is
// newer. If two turns occupy the same height, a fully visible footer wins; if
// that is also equal, the later turn wins so several short turns settle on the
// last one the reader can see.
//
// Two properties are pinned here rather than one, because they fail
// independently. The geometry pick is the reported defect. The KEPT visible
// map is the other half: an IntersectionObserver callback carries only the
// cards whose state CHANGED, so a scroll that changes one card cannot erase the
// geometry of every incumbent.
// ---------------------------------------------------------------------------

/** One notification's geometry for a single card. */
interface FakeEntry {
  target: Element;
  isIntersecting: boolean;
  top?: number;
  height?: number;
  footerTop?: number;
  footerHeight?: number;
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
  };
}

/** The layout engine's half of the observer contract: a recorder plus a trigger.
 *  Nothing in a harness-built DOM scrolls, so with the real observer this
 *  module's selection is unobservable — no notification would ever fire. Same
 *  discipline as scroll.observers.test.ts's FakeResizeObserver. */
class FakeIntersectionObserver {
  static instances: FakeIntersectionObserver[] = [];
  readonly targets = new Set<Element>();
  private readonly cb: (entries: IntersectionObserverEntry[]) => void;

  constructor(cb: (entries: IntersectionObserverEntry[]) => void) {
    this.cb = cb;
    FakeIntersectionObserver.instances.push(this);
  }

  observe(el: Element): void {
    this.targets.add(el);
  }

  /** Stop watching one target. The real API fires NO callback for it, and that
   *  silence is the whole reason the module has a departure path — so this must
   *  not helpfully deliver a not-intersecting entry. */
  unobserve(el: Element): void {
    this.targets.delete(el);
  }

  disconnect(): void {
    this.targets.clear();
  }

  /** Deliver one notification after applying the card and footer geometry the
   *  real layout engine would expose at that scroll position. */
  fire(states: FakeEntry[]): void {
    for (const s of states) {
      const top = s.top ?? 0;
      const height = s.height ?? 300;
      Object.defineProperty(s.target, "getBoundingClientRect", {
        configurable: true,
        value: () => fakeRect(top, height),
      });
      const footer = s.target.querySelector<HTMLElement>(":scope > .turn-footer");
      if (footer !== null && s.footerTop !== undefined) {
        const footerHeight = s.footerHeight ?? 40;
        Object.defineProperty(footer, "getBoundingClientRect", {
          configurable: true,
          value: () => fakeRect(s.footerTop ?? 0, footerHeight),
        });
      }
    }
    this.cb(
      states.map((s) => {
        const top = s.top ?? 0;
        const height = s.height ?? 300;
        const visibleTop = Math.max(0, top);
        const visibleBottom = Math.min(scrollable.viewportBottom, top + height);
        const visibleHeight = s.isIntersecting ? Math.max(0, visibleBottom - visibleTop) : 0;
        return {
          target: s.target,
          isIntersecting: s.isIntersecting,
          boundingClientRect: fakeRect(top, height),
          intersectionRect: fakeRect(visibleTop, visibleHeight),
        } as unknown as IntersectionObserverEntry;
      }),
    );
  }
}

describe("which turn the rail calls current", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;

  beforeAll(() => {
    document.body.appendChild(host);
    // Idempotent and a module SINGLETON, so on a whole-file run this no-ops and
    // the rail is still inside the block above's host. Resolve it from the
    // document rather than from `host`, which holds it only when this block runs
    // first.
    mountTurnRail(host);
    const mounted = document.querySelector<HTMLElement>(".turn-rail");
    if (mounted === null) {
      throw new Error("rail not mounted");
    }
    rail = mounted;
    // Browser Mode serves no stylesheet, and `.turn-rail` takes its height from
    // `position: absolute; inset-block: …` (29-turns.css), so the mounted nav
    // measures whatever its markers happen to occupy — ~21px. That makes
    // `capacity` 1, the whole session renders as ONE cluster, and there is no
    // `.rail-marker` to carry `data-current`, so every assertion below would read
    // "". An explicit box is the harness standing in for the stylesheet, not a
    // workaround for the module.
    rail.style.height = "600px";
    rail.style.display = "block";
  });

  beforeEach(() => {
    // These cases predate the navigability gate, so none of them sets scroll
    // room. Every assertion below reads `.rail-marker[data-current]`, which only
    // exists while `navigable()` is true — so restore the comfortable default
    // rather than inheriting whatever the visibility block above left behind.
    scrollable.by = 500;
    scrollable.viewportBottom = 600;
    resetTurnRail();
    FakeIntersectionObserver.instances.length = 0;
    // Inside the test, never at module scope: `unstubGlobals` restores the
    // global between tests, and turn-rail.ts checks `typeof
    // IntersectionObserver` at CALL time rather than at import.
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
  });

  /** A mounted turn card, carrying the reconcile key `numberOf` resolves. */
  function card(n: number): HTMLElement {
    const e = document.createElement("div");
    e.className = "turn";
    e.setAttribute(KEY_ATTR, `m${String(n)}`);
    const footer = document.createElement("div");
    footer.className = "turn-footer";
    e.appendChild(footer);
    return e;
  }

  /** The label of the marker the rail marks current, or "" when none is. */
  function current(): string {
    const el = rail.querySelector<HTMLElement>(".rail-marker[data-current]");
    return el?.textContent ?? "";
  }

  /** Seat the session-wide index, then observe one card per turn. Returns the
   *  cards by turn number and the observer the rail just built. */
  async function seat(
    id: string,
    ns: number[],
  ): Promise<{ cards: Map<number, HTMLElement>; io: FakeIntersectionObserver }> {
    vi.mocked(apiGet).mockResolvedValue({ turns: ns.map((n) => turn(n)) });
    await loadTurnRail(id);
    const cards = new Map(ns.map((n) => [n, card(n)]));
    observeTurns([...cards.values()]);
    const io = FakeIntersectionObserver.instances.at(-1);
    if (io === undefined) {
      throw new Error("no observer built");
    }
    return { cards, io };
  }

  function at(cards: Map<number, HTMLElement>, n: number): HTMLElement {
    const c = cards.get(n);
    if (c === undefined) {
      throw new Error(`no card for turn ${String(n)}`);
    }
    return c;
  }

  // The reported scenario: only the bottom edge of turn 1 remains while turn 2
  // fills the viewport. The older sliver cannot win.
  it("ignores an older sliver beside a fully visible lower turn", async () => {
    const { cards, io } = await seat("c-a", [1, 2]);

    io.fire([
      { target: at(cards, 1), isIntersecting: true, top: -280, height: 300 },
      {
        target: at(cards, 2),
        isIntersecting: true,
        top: 100,
        height: 400,
        footerTop: 460,
      },
    ]);

    expect(current()).toBe("2");
  });

  it("does not let a newer sliver beat the dominant turn above it", async () => {
    const { cards, io } = await seat("c-a", [1, 2]);

    io.fire([
      { target: at(cards, 1), isIntersecting: true, top: 20, height: 500 },
      { target: at(cards, 2), isIntersecting: true, top: 580, height: 300 },
    ]);

    expect(current()).toBe("1");
  });

  it("uses a fully visible footer to break an equal-height tie", async () => {
    const { cards, io } = await seat("c-a", [1, 2]);

    io.fire([
      {
        target: at(cards, 1),
        isIntersecting: true,
        top: 0,
        height: 300,
        footerTop: 260,
      },
      { target: at(cards, 2), isIntersecting: true, top: 300, height: 300, footerTop: 590 },
    ]);

    expect(current()).toBe("1");
  });

  it("uses the later turn when equal-height cards have the same footer state", async () => {
    const { cards, io } = await seat("c-a", [1, 2]);

    io.fire([
      { target: at(cards, 1), isIntersecting: true, top: 0, height: 300 },
      { target: at(cards, 2), isIntersecting: true, top: 300, height: 300 },
    ]);

    expect(current()).toBe("2");
  });

  it("re-measures the visible cards while the transcript scrolls", async () => {
    const { cards, io } = await seat("c-a", [1, 2]);
    const one = at(cards, 1);
    const two = at(cards, 2);

    io.fire([
      { target: one, isIntersecting: true, top: 0, height: 400 },
      { target: two, isIntersecting: true, top: 400, height: 200 },
    ]);
    expect(current()).toBe("1");

    // Both cards remain intersecting, so a threshold-0 observer sends no new
    // membership callback. The scroll listener must still move the current
    // marker when their visible heights cross.
    Object.defineProperty(one, "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(-300, 400),
    });
    Object.defineProperty(two, "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(100, 500),
    });
    scrollable.onScroll?.();
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    });

    expect(current()).toBe("2");
  });

  // Written as "turn 1 is the only turn in the viewport", NOT "all three are
  // visible, expect 1" — the latter would contradict the rule. At the top of a
  // transcript the first turn is active because it is the lowest one on screen,
  // not because it is first.
  it("names the first turn at the very top of the transcript", async () => {
    const { cards, io } = await seat("c-a", [1, 2, 3]);

    io.fire([
      { target: at(cards, 1), isIntersecting: true, top: 0 },
      { target: at(cards, 2), isIntersecting: false, top: 700 },
      { target: at(cards, 3), isIntersecting: false, top: 1400 },
    ]);

    expect(current()).toBe("1");
  });

  it("names the last turn at the very bottom of the transcript", async () => {
    const { cards, io } = await seat("c-a", [1, 2, 3, 4, 5]);

    io.fire([
      { target: at(cards, 1), isIntersecting: false, top: -1400 },
      { target: at(cards, 2), isIntersecting: false, top: -1000 },
      { target: at(cards, 3), isIntersecting: false, top: -600 },
      { target: at(cards, 4), isIntersecting: true, top: -100 },
      { target: at(cards, 5), isIntersecting: true, top: 300 },
    ]);
    expect(current()).toBe("5");

    // A departure must not move the mark, in either direction.
    io.fire([{ target: at(cards, 4), isIntersecting: false, top: -500 }]);
    expect(current()).toBe("5");
  });

  it("keeps the lower turn when a partial callback reports only an arrival", async () => {
    const { cards, io } = await seat("c-a", [4, 5]);

    io.fire([{ target: at(cards, 5), isIntersecting: true, top: 100 }]);
    expect(current()).toBe("5");

    // Nothing about turn 5 changed, so it is absent from this callback. Deriving
    // the pick from these entries alone would name 4.
    io.fire([{ target: at(cards, 4), isIntersecting: true, top: -300 }]);
    expect(current()).toBe("5");
  });

  it("drops the previous chat's visible turns when re-pointed", async () => {
    const a = await seat("c-a", [1, 2, 3]);
    a.io.fire([{ target: at(a.cards, 3), isIntersecting: true, top: 0 }]);
    expect(current()).toBe("3");

    pointTurnRail("c-b");
    const b = await seat("c-b", [1, 2]);
    b.io.fire([{ target: at(b.cards, 1), isIntersecting: true, top: 0 }]);

    // Never "3": chat B has no turn 3, and a leftover member would outrank
    // every turn it does have.
    expect(current()).toBe("1");
  });

  // -------------------------------------------------------------------------
  // The observer is built ONCE and its target set is diffed.
  //
  // `paint()` calls observeTurns on every repaint, and a repaint fires at SSE
  // frame rate, so this used to disconnect and construct a fresh
  // IntersectionObserver over every turn card many times a second. Dropping the
  // rebuild is what forces the departure handling: a fresh observer re-reported
  // every target it was given, and a persistent one reports nothing for a target
  // it already watches.
  // -------------------------------------------------------------------------

  it("builds ONE observer however many paints re-observe", async () => {
    const { cards } = await seat("c-a", [1, 2, 3]);
    observeTurns([...cards.values()]);
    observeTurns([...cards.values()]);

    expect(FakeIntersectionObserver.instances).toHaveLength(1);
  });

  it("keeps the visible-card map across a paint", async () => {
    const { cards, io } = await seat("c-a", [2, 3]);
    io.fire([{ target: at(cards, 3), isIntersecting: true, top: 0 }]);
    expect(current()).toBe("3");

    // A repaint with the same cards. Nothing re-reports an already-watched
    // target, so a `visible.clear()` here is never refilled.
    observeTurns([...cards.values()]);
    expect(current()).toBe("3");

    // The assertion above is NOT what pins the map, and the difference matters:
    // pickDominant deliberately leaves the marker alone on an empty map, so an
    // accidental clear still reads "3" until something fires. What it loses is
    // the incumbent geometry; the next PARTIAL callback then becomes the whole
    // map and can name the arrival incorrectly. Turn 2 entering above turn 3
    // must not become current.
    io.fire([{ target: at(cards, 2), isIntersecting: true, top: -100 }]);

    expect(current()).toBe("3");
  });

  it("observes an arrival and leaves the incumbents alone", async () => {
    const { cards, io } = await seat("c-a", [1, 2]);
    const arrival = card(3);

    observeTurns([...cards.values(), arrival]);

    expect(io.targets.has(arrival)).toBe(true);
    expect(io.targets.size).toBe(3);
    expect(FakeIntersectionObserver.instances).toHaveLength(1);
  });

  it("unobserves a departure and stops calling its turn current", async () => {
    const { cards, io } = await seat("c-a", [1, 2, 3]);
    io.fire([
      { target: at(cards, 2), isIntersecting: true, top: -100 },
      { target: at(cards, 3), isIntersecting: true, top: 200 },
    ]);
    expect(current()).toBe("3");

    // Turn 3's card leaves the transcript. `unobserve` fires no callback, so
    // without an explicit delete AND a re-pick its number stays in the set and
    // keeps naming a turn that is no longer mounted.
    //
    // This is the scenario a rebuild-per-paint observer also covered, and the
    // assertion is deliberately stronger than that version's: a rebuild emptied
    // the set and could only refill it from the fresh observer's first callback,
    // so the marker stayed on the departed turn until the browser delivered one.
    // Resolving the departure here corrects it AT the paint, with no callback in
    // between and no second observer to fire through.
    observeTurns([at(cards, 1), at(cards, 2)]);

    expect(io.targets.has(at(cards, 3))).toBe(false);
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
    document.body.appendChild(host);
    // Idempotent: if a block above already mounted the rail, the element lives
    // in that host; resolve markers from the document.
    mountTurnRail(host);
  });

  function markers(): string[] {
    const rail = document.querySelector<HTMLElement>(".turn-rail");
    return [...(rail?.querySelectorAll(".rail-marker") ?? [])].map((b) => b.textContent ?? "");
  }

  function session(id: string, messageCount: number): Session {
    return {
      id,
      name: id,
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      available_modes: [],
      available_models: [],
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
    scrollable.by = 500;
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
// A card whose number the index does not know YET.
//
// The rail's index is refetched at three moments — turn end, chat activation, a
// transport gap — and none of them is turn START. So for the whole duration of a
// running turn the newest card's reconcile key is absent from the index and its
// absolute number is unknowable, which used to mean every intersection entry for
// it was DISCARDED. Nothing recovered it: an IntersectionObserver reports
// membership CHANGES, `observeTurns` keeps the card in its observed set so it is
// never re-observed, and the pick re-measures only what is already in the visible
// map. The card entered the map only by leaving the viewport and coming back — so
// on the common shape, the newest turn fully on screen and staying there, the rail
// marked the PREVIOUS turn for the rest of the session.
// ---------------------------------------------------------------------------

describe("a card the index cannot place yet", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;

  beforeAll(() => {
    document.body.appendChild(host);
    mountTurnRail(host);
    const mounted = document.querySelector<HTMLElement>(".turn-rail");
    if (mounted === null) {
      throw new Error("rail not mounted");
    }
    rail = mounted;
    rail.style.height = "600px";
    rail.style.display = "block";
  });

  beforeEach(() => {
    scrollable.by = 500;
    scrollable.viewportBottom = 600;
    resetTurnRail();
    FakeIntersectionObserver.instances.length = 0;
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
  });

  function card(n: number): HTMLElement {
    const e = document.createElement("div");
    e.className = "turn";
    e.setAttribute(KEY_ATTR, `m${String(n)}`);
    return e;
  }

  function current(): string {
    return rail.querySelector<HTMLElement>(".rail-marker[data-current]")?.textContent ?? "";
  }

  function io(): FakeIntersectionObserver {
    const built = FakeIntersectionObserver.instances.at(-1);
    if (built === undefined) {
      throw new Error("no observer built");
    }
    return built;
  }

  it("adopts it the moment the index arrives, with no scroll and no second callback", async () => {
    // THE REGRESSION CASE. Turn 3 is streaming, so the index the rail holds names
    // only turns 1 and 2.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-live");
    const settled = card(2);
    const streaming = card(3);
    observeTurns([settled, streaming]);

    // The streaming card is dominant — it fills the viewport — and unplaceable.
    io().fire([
      { target: settled, isIntersecting: true, top: -280, height: 300 },
      { target: streaming, isIntersecting: true, top: 20, height: 500 },
    ]);
    expect(current()).toBe("2");

    // `turn_ended` lands. NOTHING else happens: no scroll, no intersection change,
    // no repaint of the cards. Pre-fix the card had been thrown away and only a
    // trip out of the viewport and back could put it in the map.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });
    await refreshTurnRail("c-live");

    expect(current()).toBe("3");
  });

  it("drops it on departure by KEY, not by the number it does not have", async () => {
    // The mirror half. `visible.delete(numberOf(c))` resolved an unplaceable card
    // to 0 and deleted key 0, leaving the real entry to keep winning from a node
    // that had left the DOM.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-live");
    const settled = card(2);
    const streaming = card(3);
    observeTurns([settled, streaming]);
    io().fire([
      { target: settled, isIntersecting: true, top: 200, height: 200 },
      { target: streaming, isIntersecting: true, top: 400, height: 300 },
    ]);

    // The streaming card unmounts (a chat switch's dispose, an eviction) while it
    // is still unplaceable, then the index catches up. A leftover entry would name
    // turn 3 from a detached element.
    observeTurns([settled]);
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });
    await refreshTurnRail("c-live");

    expect(current()).toBe("2");
  });

  it("re-picks when the index itself moves, with no scroll", async () => {
    // The rail's numbering is the SERVER's, so a rewind or a compaction can change
    // which absolute number a resident card carries. `refreshTurnRail` ended in
    // `render()` alone, so the marker stayed on the old number until a scroll frame.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-live");
    const one = card(1);
    const two = card(2);
    observeTurns([one, two]);
    io().fire([
      { target: one, isIntersecting: true, top: -280, height: 300 },
      { target: two, isIntersecting: true, top: 20, height: 500 },
    ]);
    expect(current()).toBe("2");

    // The same two cards, renumbered: m2 is now absolute turn 7.
    vi.mocked(apiGet).mockResolvedValue({
      turns: [turn(6, { id: "m1" }), turn(7, { id: "m2" })],
    });
    await refreshTurnRail("c-live");

    expect(current()).toBe("7");
  });

  it("marks the CLUSTER holding the current turn, and no sibling", async () => {
    // Past capacity every turn is inside a cluster, so `data-current` on markers
    // alone left a long session's rail with no statement of position at all.
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(60) });
    await loadTurnRail("c-long");
    // 4 rows at the pitch production uses, so 60 turns cluster into 4 ranges of 15.
    rail.style.height = `${String(railFor(4))}px`;
    const at30 = card(30);
    observeTurns([at30]);
    io().fire([{ target: at30, isIntersecting: true, top: 0, height: 400 }]);

    const clusters = [...rail.querySelectorAll<HTMLElement>(".rail-cluster")];
    expect(clusters.length).toBeGreaterThan(1);
    const marked = clusters.filter((c) => c.dataset["current"] !== undefined);
    expect(marked).toHaveLength(1);
    expect(marked[0]?.textContent).toBe("16\u201330");
    expect(marked[0]?.getAttribute("aria-label")).toContain("contains the current turn");
    // No marker to carry it at this height, so the cluster is the only channel.
    expect(rail.querySelector(".rail-marker[data-current]")).toBeNull();
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
    document.body.appendChild(host);
    mountTurnRail(host);
    const mounted = document.querySelector<HTMLElement>(".turn-rail");
    if (mounted === null) {
      throw new Error("rail not mounted");
    }
    rail = mounted;
    rail.style.height = "600px";
    rail.style.display = "block";
  });

  beforeEach(() => {
    scrollable.by = 500;
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
      (b) => b.textContent === String(n),
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
      available_modes: [],
      available_models: [],
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
    await Promise.resolve();

    expect(vi.mocked(jumpTo)).toHaveBeenCalledTimes(1);
    const target = vi.mocked(jumpTo).mock.calls[0]?.[0];
    expect(target).toBe(wanted);
    expect(target?.getAttribute(KEY_ATTR)).toBe("m6");
    // And the id it does NOT use, spelled out so a reader sees the two spaces:
    // pre-fix this element was the target.
    expect(view.querySelector("#turn-6")?.getAttribute(KEY_ATTR)).toBe("m10");
  });

  it("scrolls instantly", async () => {
    // A smooth scroll freezes its target at flight start and emits ~50 unmarked
    // scroll events, which scroll.ts reads as reader gestures — that is what
    // revokes the selection the same click just made.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-paged");
    residentCard(1, "m1");
    residentCard(2, "m2");

    marker(2).click();
    await Promise.resolve();

    expect(vi.mocked(jumpTo).mock.calls[0]?.[1]).toEqual({ block: "start", behavior: "instant" });
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
    await Promise.resolve();

    expect(vi.mocked(jumpTo).mock.calls[0]?.[0]).toBe(wanted);
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
    // POLLED, not slept. The jump awaits two dynamic imports, the page, and a frame,
    // and a fixed wait that is long enough on an idle box is not long enough on a
    // loaded one — which is exactly the flake shape `testing.md` names. Waiting on
    // the observable the jump produces is deterministic at any load.
    await vi.waitFor(() => {
      expect(vi.mocked(jumpTo)).toHaveBeenCalled();
    });

    expect(pendingWhileWaiting).toBe(true);
    expect(vi.mocked(loadMessages)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(jumpTo).mock.calls[0]?.[0]?.getAttribute(KEY_ATTR)).toBe("m6");
    // The pending state is a fetch in flight, so it has to be gone afterwards.
    expect(marker(6).dataset["pending"]).toBeUndefined();
    // And the landing turn's body is built on demand, because a paginated landing
    // is a tier-3 stub.
    expect(mountedBodies).toEqual(["m6"]);
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
    // The assertions after this are NEGATIVE, so the wait cannot poll for them. It
    // polls for the state the jump passes THROUGH instead: pending is set on the
    // click and cleared in the `finally`, so its disappearance is the jump having
    // run to completion at any load.
    await vi.waitFor(() => {
      expect(marker(1).dataset["pending"]).toBeUndefined();
    });

    expect(vi.mocked(jumpTo)).not.toHaveBeenCalled();
    expect(vi.mocked(loadMessages)).not.toHaveBeenCalled();
    // The pending state means a fetch is in flight, so a dead end has to clear it —
    // a marker left pulsing forever is the same silence the state exists to break.
    expect(marker(1).dataset["pending"]).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// A click always produces a reaction.
//
// `currentN` had one writer — the geometry pick — and a marker click called
// `jumpToTurn` and nothing else. With the target already at the reader's scroll
// position `scrollIntoView` is a no-op: no scroll event, no intersection change, no
// pick, no render. So clicking turn 3 while turns 2 and 3 were both fully visible
// produced NOTHING observable, and because dominance is by visible pixels the
// taller turn 2 kept the mark — the rail contradicting the reader's own choice.
// ---------------------------------------------------------------------------

describe("a click always produces a reaction", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;
  let view: HTMLElement;

  beforeAll(() => {
    document.body.appendChild(host);
    mountTurnRail(host);
    const mounted = document.querySelector<HTMLElement>(".turn-rail");
    if (mounted === null) {
      throw new Error("rail not mounted");
    }
    rail = mounted;
    rail.style.height = "600px";
    rail.style.display = "block";
  });

  beforeEach(() => {
    scrollable.by = 500;
    scrollable.viewportBottom = 600;
    resetTurnRail();
    FakeIntersectionObserver.instances.length = 0;
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
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

  function residentCard(n: number): HTMLElement {
    const e = document.createElement("div");
    e.className = "turn";
    e.id = `turn-${String(n)}`;
    e.setAttribute(KEY_ATTR, `m${String(n)}`);
    const footer = document.createElement("div");
    footer.className = "turn-footer";
    e.appendChild(footer);
    view.appendChild(e);
    return e;
  }

  function marker(n: number): HTMLButtonElement {
    const btn = [...rail.querySelectorAll<HTMLButtonElement>(".rail-marker")].find(
      (b) => b.textContent === String(n),
    );
    if (btn === undefined) {
      throw new Error(`no marker for turn ${String(n)}`);
    }
    return btn;
  }

  /** Two turns both fully on screen, turn 2 taller — so the geometry pick names
   *  turn 2 and a click on turn 3 has nothing to scroll to. The reported scene. */
  async function bothVisible(): Promise<{ two: HTMLElement; three: HTMLElement }> {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });
    await loadTurnRail("c-both");
    const two = residentCard(2);
    const three = residentCard(3);
    observeTurns([two, three]);
    const io = FakeIntersectionObserver.instances.at(-1);
    if (io === undefined) {
      throw new Error("no observer built");
    }
    io.fire([
      { target: two, isIntersecting: true, top: 0, height: 400, footerTop: 360 },
      { target: three, isIntersecting: true, top: 400, height: 200, footerTop: 560 },
    ]);
    return { two, three };
  }

  it("marks the clicked marker even when the scroll cannot move", async () => {
    // THE REGRESSION CASE. `jumpTo` is a mock that scrolls nowhere, which is exactly
    // what the real one does for a target already at the reader's position.
    await bothVisible();
    expect(rail.querySelector(".rail-marker[data-current]")?.textContent).toBe("2");

    marker(3).click();
    await Promise.resolve();

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
    await Promise.resolve();
    expect(rail.querySelectorAll("[data-current], [data-selected]")).toHaveLength(1);
    expect(rail.querySelector("[data-selected]")?.textContent).toBe("3");

    scrollable.onReaderGesture?.();
    expect(rail.querySelectorAll("[data-current], [data-selected]")).toHaveLength(1);
    expect(rail.querySelector("[data-current]")?.textContent).toBe("2");
  });

  it("keeps exactly one marker claiming to be current", async () => {
    await bothVisible();
    marker(3).click();
    await Promise.resolve();

    expect(rail.querySelectorAll("[aria-current='true']")).toHaveLength(1);
  });

  it("marks the pick even when it IS the dominant turn", async () => {
    // The coincident case, and the reason the two marks are exclusive rather than
    // additive: clicking the marker the scroll already named has to read as a pick,
    // or the rail stops tracking and shows nothing to say why.
    await bothVisible();
    marker(2).click();
    await Promise.resolve();

    expect(marker(2).dataset["selected"]).toBe("");
    expect(marker(2).dataset["current"]).toBeUndefined();
    expect(marker(2).getAttribute("aria-current")).toBe("true");
    expect(rail.querySelectorAll("[data-current], [data-selected]")).toHaveLength(1);
  });

  it("flashes the landing card, then takes the ring away", async () => {
    vi.useFakeTimers();
    try {
      await bothVisible();
      const three = view.querySelector<HTMLElement>('[data-reconcile-key="m3"]');

      marker(3).click();
      await Promise.resolve();
      expect(three?.dataset["railTarget"]).toBe("");

      vi.advanceTimersByTime(1001);
      expect(three?.dataset["railTarget"]).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("moves the ring to the newest landing rather than letting the first timer strip it", async () => {
    vi.useFakeTimers();
    try {
      await bothVisible();
      marker(3).click();
      await Promise.resolve();
      vi.advanceTimersByTime(600);
      marker(2).click();
      await Promise.resolve();
      // Past the FIRST click's deadline. A shared timer would have fired here and
      // cleared the card the reader just landed on.
      vi.advanceTimersByTime(600);

      expect(
        view.querySelector('[data-reconcile-key="m2"]')?.getAttribute("data-rail-target"),
      ).toBe("");
      expect(
        view.querySelector('[data-reconcile-key="m3"]')?.getAttribute("data-rail-target"),
      ).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("hands tracking back on a reader gesture", async () => {
    await bothVisible();
    marker(3).click();
    await Promise.resolve();
    expect(marker(3).dataset["selected"]).toBe("");

    // Through the real seam: the rail subscribed at mount, and this is the callback
    // scroll.ts publishes for a reader gesture. The rail cannot tell WHICH gesture
    // it was and must not care — a scroll and a request for the live edge are both
    // the reader stating a position — so which writes publish it is pinned in
    // `scroll.test.ts`, over a real scroller, rather than restated here.
    scrollable.onReaderGesture?.();

    expect(marker(3).dataset["selected"]).toBeUndefined();
    expect(rail.querySelector(".rail-marker[data-current]")?.textContent).toBe("2");
    expect(marker(2).getAttribute("aria-current")).toBe("true");
  });

  it("drops a pick the arriving index no longer names", async () => {
    // THE REWIND. Rewind lives in the turn footer, so picking a marker and then
    // reverting the session is two clicks apart — and the pick is held by the
    // turn's opening-message id, which that index no longer carries. Without the
    // drop the rail marks NO position on any row: `rowNode` withholds `data-current`
    // while a pick stands, and matches `data-selected` on a turn that is gone.
    await bothVisible();
    marker(3).click();
    await Promise.resolve();
    expect(rail.querySelector("[data-selected]")?.textContent).toBe("3");

    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await refreshTurnRail("c-both");

    expect(rail.querySelector("[data-selected]")).toBeNull();
    expect(rail.querySelectorAll("[data-current], [data-selected]")).toHaveLength(1);
    expect(rail.querySelector("[data-current]")?.textContent).toBe("2");
    expect(rail.querySelectorAll("[aria-current='true']")).toHaveLength(1);
  });

  it("keeps a pick the arriving index still names", async () => {
    // The control, and the reason the case above cannot pass for the wrong reason: a
    // refresh must not revoke the reader's pick just for arriving. The index is
    // refetched at every turn end, so a rail that dropped the pick per index would
    // lose it on the next turn of the very conversation being read.
    await bothVisible();
    marker(3).click();
    await Promise.resolve();

    // A fourth turn arrives — a later index that still carries `m3`.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3), turn(4)] });
    await refreshTurnRail("c-both");

    expect(rail.querySelector("[data-selected]")?.textContent).toBe("3");
    expect(rail.querySelector("[data-current]")).toBeNull();
  });

  it("follows the picked turn through a renumbering rather than the number it wore", async () => {
    // The pick is an id, so an index that renumbers the same turns — an older turn
    // dropping off a session the server recounted — moves the mark WITH the turn.
    // Held by number, `data-selected` would have stayed on whatever now wears 3.
    await bothVisible();
    marker(3).click();
    await Promise.resolve();
    expect(rail.querySelector("[data-selected]")?.textContent).toBe("3");

    vi.mocked(apiGet).mockResolvedValue({
      turns: [turn(1, { id: "m2" }), turn(2, { id: "m3" })],
    });
    await refreshTurnRail("c-both");

    expect(rail.querySelector("[data-selected]")?.textContent).toBe("2");
    expect(rail.querySelectorAll("[aria-current='true']")).toHaveLength(1);
    expect(rail.querySelector("[aria-current='true']")?.textContent).toBe("2");
  });

  it("survives a re-measure that finds a different dominant turn", async () => {
    // Deliberately NOT cleared by the pick: a streaming turn's own growth moves
    // dominance with no reader gesture behind it, and dropping the selection there
    // would revoke the reader's choice while they sit perfectly still.
    const { two, three } = await bothVisible();
    marker(3).click();
    await Promise.resolve();

    Object.defineProperty(two, "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(-300, 400),
    });
    Object.defineProperty(three, "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(100, 500),
    });
    scrollable.onScroll?.();
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    });

    expect(marker(3).dataset["selected"]).toBe("");
  });

  it("drops the selection on a chat switch", async () => {
    await bothVisible();
    marker(3).click();
    await Promise.resolve();

    pointTurnRail("c-other");
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });
    await refreshTurnRail("c-other");

    expect(rail.querySelector(".rail-marker[data-selected]")).toBeNull();
  });

  it("drops the selection when a cluster is zoomed, since a range is not a turn", async () => {
    // Tall enough for 60 direct markers at the pitch production uses, so a marker
    // exists to pick before the rail is shrunk into ranges.
    rail.style.height = `${String(railFor(60))}px`;
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(60) });
    await loadTurnRail("c-cluster");
    residentCard(3);
    marker(3).click();
    await Promise.resolve();
    expect(rail.querySelector(".rail-marker[data-selected]")).not.toBeNull();

    // The reader narrows the window; the same 60 turns now compress into ranges.
    rail.style.height = `${String(railFor(4))}px`;
    await refreshTurnRail("c-cluster");
    const clustered = rail.querySelector<HTMLButtonElement>(".rail-cluster");
    expect(clustered).not.toBeNull();
    clustered?.click();

    expect(rail.querySelector("[data-selected]")).toBeNull();
    expect(rail.querySelector(".rail-zoom-out")).not.toBeNull();
    // Restore the block's default box for the cases after this one.
    rail.style.height = "600px";
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
    document.body.appendChild(host);
    mountTurnRail(host);
    const mounted = document.querySelector<HTMLElement>(".turn-rail");
    if (mounted === null) {
      throw new Error("rail not mounted");
    }
    rail = mounted;
    rail.style.height = "600px";
    rail.style.display = "block";
  });

  beforeEach(() => {
    scrollable.by = 500;
    resetTurnRail();
  });

  function rows(sel: string): HTMLElement[] {
    return [...rail.querySelectorAll<HTMLElement>(sel)];
  }

  it("carries data-tooltip and no native title, on every row kind", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(60) });
    await loadTurnRail("c-say");
    rail.style.height = `${String(railFor(4))}px`;
    // Re-render at the clustered height, then zoom so the zoom-out row exists too.
    observeTurns([]);
    rail.querySelector<HTMLButtonElement>(".rail-cluster")?.click();

    const all = rows(".rail-marker, .rail-cluster, .rail-zoom-out");
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

  it("gives the zoom-out row a description that is not a second copy of its name", async () => {
    // The tooltip controller publishes its text as the anchor's `aria-describedby`
    // on show, so two identical sentences reach a keyboard user as one name read
    // twice. The NAME carries the action `all` cannot state; the tooltip carries the
    // range, which is what the name deliberately leaves out.
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(60) });
    await loadTurnRail("c-zoom");
    rail.style.height = `${String(railFor(4))}px`;
    observeTurns([]);
    const cluster = rail.querySelector<HTMLButtonElement>(".rail-cluster");
    const range = cluster?.textContent ?? "";
    cluster?.click();

    const [out] = rows(".rail-zoom-out");
    expect(out).not.toBeUndefined();
    expect(out?.getAttribute("aria-label")).toBe("Show the whole session");
    expect(out?.getAttribute("data-tooltip")).not.toBe(out?.getAttribute("aria-label"));
    // The range the row will leave, read off the cluster that was clicked, so the
    // tooltip is asserted to carry live state rather than any second sentence.
    expect(out?.getAttribute("data-tooltip")).toBe(`Showing turns ${range}`);
  });
});
