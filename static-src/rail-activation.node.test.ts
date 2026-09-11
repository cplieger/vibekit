// WHICH TURN THE READER IS IN, answered by arithmetic against a cached table. The
// function holds no state, so it cannot freeze on a stale answer and has no
// membership delta to lose.
//
// The clamp PRECEDENCE is the case to read first: both ends can hold at once, and the
// measured position wins over the published edge verdict.
//
// Node environment: the table's entries are MEASUREMENTS the caller supplies.

import { describe, it, expect } from "vitest";
import fc from "fast-check";

import {
  activeTurnAt,
  buildOffsets,
  turnTop,
  type RailGeom,
  type TurnOffsets,
} from "./rail-activation.js";

const READING = 200;
const LIVE: RailGeom = { clientHeight: 600, atLiveEdge: true };
const PARKED: RailGeom = { clientHeight: 600, atLiveEdge: false };

/** Four cards, 500px apart, in the scroller's frame. */
function table(): TurnOffsets {
  return buildOffsets([
    { id: "t1", top: 0 },
    { id: "t2", top: 500 },
    { id: "t3", top: 1000 },
    { id: "t4", top: 1500 },
  ]);
}

describe("the table", () => {
  it("is ascending whatever order the cards arrive in", () => {
    const offsets = buildOffsets([
      { id: "t3", top: 1000 },
      { id: "t1", top: 0 },
      { id: "t2", top: 500 },
    ]);
    expect(offsets.ids).toEqual(["t1", "t2", "t3"]);
    expect(offsets.tops).toEqual([0, 500, 1000]);
  });

  it("skips a card the engine reports no box for", () => {
    // Its marker still renders and is still clickable; it simply cannot be landed
    // on by offset until a later invalidation measures it.
    const offsets = buildOffsets([
      { id: "t1", top: 0 },
      { id: "t2", top: null },
      { id: "t3", top: 1000 },
    ]);
    expect(offsets.ids).toEqual(["t1", "t3"]);
  });

  it("skips a card with an unreadable measurement or no key", () => {
    const offsets = buildOffsets([
      { id: "t1", top: 0 },
      { id: "t2", top: Number.NaN },
      { id: "", top: 500 },
      { id: "t3", top: Number.POSITIVE_INFINITY },
    ]);
    expect(offsets.ids).toEqual(["t1"]);
  });

  it("answers a turn's own top, and null for a turn it does not carry", () => {
    const offsets = table();
    expect(turnTop(offsets, "t3")).toBe(1000);
    expect(turnTop(offsets, "t9")).toBeNull();
  });
});

describe("the reading line names the turn it is in", () => {
  it("answers the turn whose top the line has passed", () => {
    const offsets = table();
    // The line sits 200px down the scrollport, so at offset 400 it is at 600 —
    // past t2's top and short of t3's.
    expect(activeTurnAt(400, offsets, READING, PARKED)).toBe("t2");
    expect(activeTurnAt(801, offsets, READING, PARKED)).toBe("t3");
  });

  it("answers the first turn while the line is above every card", () => {
    // A card can start below the line — a short first turn, or a spacer above it.
    const offsets = buildOffsets([
      { id: "t1", top: 900 },
      { id: "t2", top: 1400 },
    ]);
    expect(activeTurnAt(1, offsets, READING, PARKED)).toBe("t1");
  });

  it("answers the turn before a gap while the line is inside it", () => {
    // Two cards 4000px apart: everything between them belongs to the earlier turn,
    // because that is the turn whose box the reader is inside.
    const offsets = buildOffsets([
      { id: "t1", top: 0 },
      { id: "t2", top: 4000 },
    ]);
    expect(activeTurnAt(1000, offsets, READING, PARKED)).toBe("t1");
    expect(activeTurnAt(3000, offsets, READING, PARKED)).toBe("t1");
    expect(activeTurnAt(3801, offsets, READING, PARKED)).toBe("t2");
  });
});

describe("a card's own reflow, with no DOM change and no scroll", () => {
  it("moves the MARK once the table is rebuilt", () => {
    // `content-visibility: auto` on `.msg-row` lets a card swap its estimated height
    // for its real one: every top below it moves, with no mutation for
    // `onTranscriptMutate` to see and no scroll event to re-read on. So the seam that
    // invalidates the table (`onContentResize`) has to re-ANSWER as well as clear,
    // and what the reader would otherwise see is the mark frozen on the turn they
    // have left — defect 4's own shape.
    const answer = (offsets: TurnOffsets): string => activeTurnAt(500, offsets, READING, PARKED);
    const before = buildOffsets([
      { id: "t1", top: 0 },
      { id: "t2", top: 500 },
      { id: "t3", top: 1000 },
    ]);
    expect(answer(before)).toBe("t2");

    // t1 grew by 400 while the reader sat still, so the line is back inside it.
    const after = buildOffsets([
      { id: "t1", top: 0 },
      { id: "t2", top: 900 },
      { id: "t3", top: 1400 },
    ]);

    expect(answer(after)).toBe("t1");
    // The stale table is what an invalidation with no re-answer leaves on screen.
    expect(answer(after)).not.toBe(answer(before));
  });
});

describe("both ends clamp, and the TOP clamp wins", () => {
  it("answers the first resident turn at the top of the transcript", () => {
    expect(activeTurnAt(0, table(), READING, PARKED)).toBe("t1");
    expect(activeTurnAt(-40, table(), READING, PARKED)).toBe("t1");
  });

  it("answers the last resident turn at the live edge", () => {
    expect(activeTurnAt(4000, table(), READING, LIVE)).toBe("t4");
  });

  it("answers the FIRST turn at offset 0 even while the edge verdict says live", () => {
    // The precedence case. `atLiveEdge` is a published field that can be stale;
    // offset 0 is a position the scroller measured. Reversing the two answers "t4"
    // — the other end of the transcript — for a reader sitting at the top.
    expect(activeTurnAt(0, table(), READING, LIVE)).toBe("t1");
  });
});

describe("no answer is its own answer", () => {
  it("says nothing for an empty table, so the mark that stands is kept", () => {
    // Clearing the mark instead would blink it off on an ordinary repaint.
    expect(activeTurnAt(400, buildOffsets([]), READING, PARKED)).toBe("");
  });

  it("says nothing for a pre-layout scroller", () => {
    const offsets = table();
    expect(activeTurnAt(400, offsets, READING, { clientHeight: 0, atLiveEdge: false })).toBe("");
    // Including at the ends, so a clamp cannot answer off a scroller with no box.
    expect(activeTurnAt(0, offsets, READING, { clientHeight: 0, atLiveEdge: true })).toBe("");
  });
});

describe("properties", () => {
  const idsOf = (n: number): TurnOffsets =>
    buildOffsets(Array.from({ length: n }, (_, i) => ({ id: `t${String(i)}`, top: i * 400 })));

  it("is total over an arbitrary scroll offset", () => {
    fc.assert(
      fc.property(
        fc.double({ min: -1e7, max: 1e7, noNaN: true }),
        fc.integer({ min: 0, max: 40 }),
        fc.boolean(),
        (scrollTop, count, atLiveEdge) => {
          const offsets = idsOf(count);
          const answer = activeTurnAt(scrollTop, offsets, READING, {
            clientHeight: 600,
            atLiveEdge,
          });
          expect(answer === "" || offsets.ids.includes(answer)).toBe(true);
          expect(count > 0 || answer === "").toBe(true);
        },
      ),
    );
  });

  it("is monotone non-decreasing in the scroll offset", () => {
    fc.assert(
      fc.property(
        fc.integer({ min: 1, max: 40 }),
        fc.boolean(),
        fc.array(fc.integer({ min: -500, max: 20000 }), { minLength: 2, maxLength: 20 }),
        (count, atLiveEdge, raw) => {
          const offsets = idsOf(count);
          const geom: RailGeom = { clientHeight: 600, atLiveEdge };
          const sorted = [...raw].sort((a, b) => a - b);
          let last = -1;
          for (const scrollTop of sorted) {
            const at = offsets.ids.indexOf(activeTurnAt(scrollTop, offsets, READING, geom));
            expect(at).toBeGreaterThanOrEqual(last);
            last = at;
          }
        },
      ),
    );
  });
});
