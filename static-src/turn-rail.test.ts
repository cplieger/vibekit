// The rail's layout arithmetic: when per-turn markers fit, when they must
// compress, and how time becomes space. Driven through railRows rather than the
// DOM because the interesting part is the capacity rule, and asserting it
// through rendered pixels would test the browser instead. (The harness only
// because the module's scroll.ts import self-initialises against `document`.)
import { describe, it, expect, vi } from "vitest";

// scroll.ts self-initialises a singleton against #messages at import time, and
// the rail imports it to park the reader on jump. Neither is under test
// here, so stub it rather than staging the whole chat DOM.
vi.mock("./scroll.js", () => ({ jumpTo: vi.fn() }));

import { railRows, ROW_PITCH_PX, type TurnSummary } from "./turn-rail.js";
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
