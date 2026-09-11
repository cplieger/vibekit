// THE MARKER SET, and the one property that makes it worth having: it is a function
// of its four arguments and of nothing else. A set that moves with the reader is
// presence churn — a marker beside the reading position winking out and back on every
// scroll, with the accessible name's dropped count moving with it.
//
// The separation invariant is WCAG 2.5.8's, so the properties run over BOTH pointer
// tiers. Node environment: no DOM is reached, which is why `railMetrics` is NOT here —
// it resolves a token against a real track, and a stubbed `getComputedStyle` answering
// a constant is what let it read `1.5rem` as the number 1.5 unnoticed.
// `rail-position-css.test.ts` measures it over real layout instead.

import { describe, it, expect } from "vitest";
import fc from "fast-check";

import type { TurnSummary } from "./rail-merge.js";
import { HERE_PX, markerPosition, maxMarkers, railAt, selectMarkers } from "./rail-select.js";
import type { TurnOutcome } from "./turns.js";

/** The two tier pitches, from `--hit-floor` plus the 4px clear. */
const FINE = 28;
const COARSE = 48;
const TRACK = 800;
const NO_HITS: ReadonlySet<number> = new Set();

function turn(n: number, over: Partial<TurnSummary> = {}): TurnSummary {
  return { id: `m-${String(n)}`, n, ts: n * 1000, outcome: "completed", ...over };
}

function turns(count: number, outcome: TurnOutcome = "completed"): TurnSummary[] {
  return Array.from({ length: count }, (_, i) => turn(i + 1, { outcome }));
}

function ns(rows: readonly TurnSummary[]): number[] {
  return rows.map((r) => r.n);
}

/** The marker box the pitch was derived from, which is what the rendered `top`
 *  travels within. */
function markerOf(pitchPx: number): number {
  return pitchPx - 4;
}

describe("position is a function of the turn's own number", () => {
  it("puts the first turn at the top and the Nth at the foot of the travel", () => {
    expect(railAt(1, 400)).toBe(0);
    expect(railAt(400, 400)).toBe(1);
    expect(markerPosition(1, 400, 800, 24)).toBe(0);
    expect(markerPosition(400, 400, 800, 24)).toBe(776);
  });

  it("puts a one-turn session at the top rather than dividing by zero", () => {
    expect(railAt(1, 1)).toBe(0);
    expect(markerPosition(1, 1, 800, 24)).toBe(0);
  });

  it("centres the reader's caret inside the marker box it stands on", () => {
    // The caret is a decoration and takes no pointer tier of its own, so the box it
    // is centred in is what follows the tier.
    expect((44 - HERE_PX) / 2).toBe(18);
    expect((24 - HERE_PX) / 2).toBe(8);
  });
});

describe("the first and last turn always have a marker", () => {
  it("keeps both ends on a session far past the track's capacity", () => {
    const shown = ns(selectMarkers(turns(400), TRACK, FINE, NO_HITS));
    expect(shown[0]).toBe(1);
    expect(shown[shown.length - 1]).toBe(400);
  });

  it("keeps both ends on a track with room for one marker", () => {
    // The rank-1 exemption from the separation pass, which is the only reason two
    // markers can sit closer than the pitch.
    const shown = ns(selectMarkers(turns(9), 60, COARSE, NO_HITS));
    expect(maxMarkers(60, COARSE)).toBe(1);
    expect(shown).toEqual([1, 9]);
  });

  it("answers a one-turn session with that turn, once", () => {
    expect(ns(selectMarkers(turns(1), TRACK, FINE, NO_HITS))).toEqual([1]);
  });

  it("answers an empty set with an empty set", () => {
    expect(selectMarkers([], TRACK, FINE, NO_HITS)).toEqual([]);
  });
});

describe("rank decides which turns survive the separation pass", () => {
  it("keeps a turn that did not end clean where the stride had dropped it", () => {
    const clean = turns(400);
    const shown = ns(selectMarkers(clean, TRACK, FINE, NO_HITS));
    expect(shown).not.toContain(150);

    const withFailure = clean.map((t) => (t.n === 150 ? turn(150, { outcome: "failed" }) : t));
    expect(ns(selectMarkers(withFailure, TRACK, FINE, NO_HITS))).toContain(150);
  });

  it("keeps a turn holding a live search hit, and drops it again when the query moves", () => {
    const rows = turns(400);
    expect(ns(selectMarkers(rows, TRACK, FINE, NO_HITS))).not.toContain(150);
    expect(ns(selectMarkers(rows, TRACK, FINE, new Set([150])))).toContain(150);
  });

  it("drops a candidate the pitch cannot separate from a higher-ranked neighbour", () => {
    // Five turns on a 200px track at the coarse pitch sit 39px apart, so NO pair
    // clears 48px and the pass has to choose every time. Both ends are exempt, so
    // the stride candidates beside them are what go.
    expect(ns(selectMarkers(turns(5), 200, COARSE, NO_HITS))).toEqual([1, 5]);
  });

  it("spends the one separable slot on rank rather than on the stride", () => {
    // Turn 3 is the only position on that track that clears the pitch from both
    // ends, and it takes the slot for either reason a turn can outrank the stride.
    const rows = turns(5);
    expect(ns(selectMarkers(rows, 200, COARSE, new Set([3])))).toEqual([1, 3, 5]);
    const failed = rows.map((t) => (t.n === 3 ? turn(3, { outcome: "failed" }) : t));
    expect(ns(selectMarkers(failed, 200, COARSE, NO_HITS))).toEqual([1, 3, 5]);
  });

  it("reports a dropped count on a downsampled session and none on a short one", () => {
    const long = turns(400);
    const shown = selectMarkers(long, TRACK, FINE, NO_HITS);
    expect(long.length - shown.length).toBeGreaterThan(300);

    const short = turns(6);
    expect(selectMarkers(short, TRACK, FINE, NO_HITS)).toHaveLength(6);
  });
});

describe("the SET is independent of scroll position, by signature", () => {
  it("takes exactly four arguments, none of them scroll state", () => {
    // The invariant read off the function itself. A variant that ranked the active
    // turn first would carry a fifth parameter and fail here.
    expect(selectMarkers.length).toBe(4);
  });

  it("answers identically whatever the rail's active turn is", () => {
    // Reached through a signature the production function deliberately does not
    // have, so a variant that DID accept a protected turn would answer differently
    // per active turn and this case would go red.
    const call = selectMarkers as unknown as (
      t: readonly TurnSummary[],
      trackPx: number,
      pitchPx: number,
      hits: ReadonlySet<number>,
      protect?: number,
    ) => TurnSummary[];
    const rows = turns(400);
    const baseline = ns(call(rows, TRACK, FINE, NO_HITS));
    for (const activeN of [1, 7, 150, 151, 200, 399, 400]) {
      expect(ns(call(rows, TRACK, FINE, NO_HITS, activeN))).toEqual(baseline);
    }
  });
});

describe("properties, over both pointer tiers", () => {
  const tiers = [
    { name: "fine", pitchPx: FINE },
    { name: "coarse", pitchPx: COARSE },
  ] as const;

  for (const tier of tiers) {
    it(`selects strictly increasing turns on a ${tier.name} pointer`, () => {
      fc.assert(
        fc.property(
          fc.integer({ min: 1, max: 500 }),
          fc.integer({ min: 200, max: 1200 }),
          (count, trackPx) => {
            const shown = ns(selectMarkers(turns(count), trackPx, tier.pitchPx, NO_HITS));
            for (let i = 1; i < shown.length; i++) {
              expect(shown[i] ?? 0).toBeGreaterThan(shown[i - 1] ?? 0);
            }
          },
        ),
      );
    });

    it(`keeps every pair but the two ends at least a pitch apart on a ${tier.name} pointer`, () => {
      fc.assert(
        fc.property(
          fc.integer({ min: 1, max: 500 }),
          fc.integer({ min: 200, max: 1200 }),
          fc.array(fc.integer({ min: 1, max: 500 }), { maxLength: 12 }),
          (count, trackPx, hitNs) => {
            const rows = turns(count);
            const shown = selectMarkers(rows, trackPx, tier.pitchPx, new Set(hitNs));
            const total = rows[rows.length - 1]?.n ?? 1;
            const markerPx = markerOf(tier.pitchPx);
            for (let i = 1; i < shown.length; i++) {
              const prev = shown[i - 1];
              const cur = shown[i];
              if (prev === undefined || cur === undefined) {
                continue;
              }
              const gap =
                markerPosition(cur.n, total, trackPx, markerPx) -
                markerPosition(prev.n, total, trackPx, markerPx);
              // The one exempt pair: both ends are guaranteed a marker, so on a
              // track too short for the pitch they are allowed to be the exception.
              const ends = prev.n === 1 && cur.n === total;
              expect(gap >= tier.pitchPx || ends).toBe(true);
            }
          },
        ),
      );
    });

    it(`stays within what the pitch can fit on a ${tier.name} pointer`, () => {
      fc.assert(
        fc.property(
          fc.integer({ min: 1, max: 500 }),
          fc.integer({ min: 200, max: 1200 }),
          fc.array(fc.integer({ min: 1, max: 500 }), { maxLength: 12 }),
          (count, trackPx, hitNs) => {
            const rows = turns(count);
            const shown = selectMarkers(rows, trackPx, tier.pitchPx, new Set(hitNs));
            // The SEPARATION bound rather than `maxMarkers`, and it is at most one
            // more: a marker travels the track minus its own box, while
            // `maxMarkers` divides the whole track. Both ends are guaranteed, so 2
            // is the floor.
            const span = Math.max(0, trackPx - markerOf(tier.pitchPx));
            const fits = Math.max(2, Math.floor(span / tier.pitchPx) + 1);
            expect(shown.length).toBeLessThanOrEqual(Math.min(count, fits));
            expect(shown.length).toBeLessThanOrEqual(maxMarkers(trackPx, tier.pitchPx) + 1);
          },
        ),
      );
    });

    it(`selects only real turns, in order, on a ${tier.name} pointer`, () => {
      fc.assert(
        fc.property(
          fc.integer({ min: 1, max: 500 }),
          fc.integer({ min: 200, max: 1200 }),
          (count, trackPx) => {
            const rows = turns(count);
            const shown = selectMarkers(rows, trackPx, tier.pitchPx, NO_HITS);
            for (const row of shown) {
              expect(rows).toContain(row);
            }
          },
        ),
      );
    });
  }
});
