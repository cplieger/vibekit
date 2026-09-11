// THE MERGE, and the two things that make it more than a concatenation: it keys on
// `n` rather than on id, because the two sides disagree about ONE turn's identity;
// and it validates a payload that arrives through an unchecked cast, because a
// non-finite `n` reaches `calc(NaN * …)` — an invalid declaration the browser drops,
// so the failure is a marker silently pinned to the top rather than a wrong one.
//
// Node environment: no DOM is reached.

import { describe, it, expect, beforeEach, vi } from "vitest";

import { mergeTurnSets, validateTurnIndex, type TurnSummary } from "./rail-merge.js";
import { WHOLE_SESSION, type Turn, type TurnOutcome, type TurnWindowBase } from "./turns.js";
import type { Message } from "./types.js";

function msg(id: string, content: string, ts = 1000): Message {
  return { id, role: "user", content, ts };
}

function residentTurn(n: number, over: Partial<Turn> = {}): Turn {
  return {
    id: `m-${String(n)}`,
    n,
    trigger: msg(`m-${String(n)}`, `prompt ${String(n)}`, n * 1000),
    body: [],
    ts: n * 1000,
    outcome: "completed",
    rewindTo: undefined,
    ...over,
  };
}

function indexRow(n: number, over: Partial<TurnSummary> = {}): TurnSummary {
  return {
    id: `m-${String(n)}`,
    n,
    ts: n * 1000,
    outcome: "completed",
    first_line: `prompt ${String(n)}`,
    agent_initiated: false,
    ...over,
  };
}

/** A window that starts mid-session, which is the only state the fragment rule can
 *  be reached from. */
const PAGED: TurnWindowBase = { offset: 7, closed: false };

describe("resident wins for anything it can answer", () => {
  it("takes the resident outcome over the index's for a turn in the window", () => {
    const index = [indexRow(1), indexRow(2, { outcome: "unknown" })];
    const resident = [residentTurn(2, { outcome: "running" })];
    const { turns } = mergeTurnSets(resident, index, WHOLE_SESSION);
    expect(turns.map((t) => [t.n, t.outcome])).toEqual([
      [1, "completed"],
      [2, "running"],
    ]);
  });

  it("carries a turn the index has never seen, so the newest turn needs no fetch", () => {
    const index = [indexRow(1), indexRow(2)];
    const resident = [residentTurn(2), residentTurn(3, { outcome: "running" })];
    const { turns, total } = mergeTurnSets(resident, index, WHOLE_SESSION);
    expect(turns.map((t) => t.n)).toEqual([1, 2, 3]);
    expect(total).toBe(3);
  });

  it("derives the hover label from the trigger, collapsed to one line", () => {
    const trigger = msg("m-4", "  fix   the\n  rail\t please  ", 4000);
    const { turns } = mergeTurnSets([residentTurn(4, { trigger })], [], WHOLE_SESSION);
    expect(turns[0]?.first_line).toBe("fix the rail please");
  });

  it("truncates a pasted block on a rune boundary", () => {
    const long = "\u00e9".repeat(200);
    const { turns } = mergeTurnSets(
      [residentTurn(4, { trigger: msg("m-4", long) })],
      [],
      WHOLE_SESSION,
    );
    const line = turns[0]?.first_line ?? "";
    expect(Array.from(line)).toHaveLength(121);
    expect(line.endsWith("\u2026")).toBe(true);
  });

  it("reports a turn with no trigger as agent-initiated and unlabelled", () => {
    const { turns } = mergeTurnSets([residentTurn(4, { trigger: undefined })], [], WHOLE_SESSION);
    expect(turns[0]?.agent_initiated).toBe(true);
    // ABSENT rather than "": the field is `omitempty` on the wire, so an
    // agent-initiated turn carries no label there either and every reader defaults.
    expect(turns[0]?.first_line).toBeUndefined();
  });
});

describe("the index extends the set backwards", () => {
  it("supplies the turns outside the window and the session's count", () => {
    const index = Array.from({ length: 10 }, (_, i) => indexRow(i + 1));
    const resident = [residentTurn(8), residentTurn(9), residentTurn(10)];
    const { turns, total } = mergeTurnSets(resident, index, PAGED);
    expect(turns.map((t) => t.n)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    expect(total).toBe(10);
  });

  it("falls back to the highest resident n when no index has landed", () => {
    const resident = [residentTurn(40), residentTurn(41), residentTurn(42)];
    const { turns, total } = mergeTurnSets(resident, [], PAGED);
    expect(turns.map((t) => t.n)).toEqual([40, 41, 42]);
    expect(total).toBe(42);
  });
});

describe("the merge keys on n, never on id", () => {
  it("counts the window's fragment first turn once, not twice", () => {
    // The one genuine id divergence: the fragment's own first resident message is
    // not the turn's opening message, so the two sides name it differently.
    const index = [indexRow(7), indexRow(8, { id: "m-8-open" })];
    const resident = [residentTurn(8, { id: "m-8-tail", trigger: undefined })];
    const { turns } = mergeTurnSets(resident, index, PAGED);
    expect(turns.map((t) => t.n)).toEqual([7, 8]);
  });
});

describe("the fragment exemption", () => {
  it("takes the index's id, ts, first_line and agent_initiated for a fragment first turn", () => {
    const index = [indexRow(8, { id: "m-8-open", ts: 500, first_line: "the real prompt" })];
    const resident = [
      residentTurn(8, { id: "m-8-tail", trigger: undefined, ts: 9999, outcome: "unknown" }),
    ];
    const { turns } = mergeTurnSets(resident, index, PAGED);
    expect(turns[0]).toEqual({
      id: "m-8-open",
      n: 8,
      ts: 500,
      outcome: "completed",
      first_line: "the real prompt",
      agent_initiated: false,
    });
  });

  it("keeps the resident outcome for a fragment first turn only while it is running", () => {
    const index = [indexRow(8, { id: "m-8-open", outcome: "completed" })];
    const running = mergeTurnSets(
      [residentTurn(8, { id: "m-8-tail", trigger: undefined, outcome: "running" })],
      index,
      PAGED,
    );
    expect(running.turns[0]?.outcome).toBe("running");
    // The index's id survives either way, because that is what a jump pages towards.
    expect(running.turns[0]?.id).toBe("m-8-open");

    const settled = mergeTurnSets(
      [residentTurn(8, { id: "m-8-tail", trigger: undefined, outcome: "unknown" })],
      index,
      PAGED,
    );
    expect(settled.turns[0]?.outcome).toBe("completed");
  });

  it("does not exempt a first turn that carries its own trigger", () => {
    // A window can legitimately open ON a turn boundary, and then the resident row
    // can answer for every field.
    const index = [indexRow(8, { id: "m-8-open", first_line: "stale" })];
    const resident = [residentTurn(8, { id: "m-8-own", trigger: msg("m-8-own", "live prompt") })];
    const { turns } = mergeTurnSets(resident, index, PAGED);
    expect(turns[0]?.id).toBe("m-8-own");
    expect(turns[0]?.first_line).toBe("live prompt");
  });

  it("does not exempt a fragment when the window starts at the session's own head", () => {
    const index = [indexRow(1, { id: "m-1-open" })];
    const resident = [residentTurn(1, { id: "m-1-tail", trigger: undefined })];
    const { turns } = mergeTurnSets(resident, index, WHOLE_SESSION);
    expect(turns[0]?.id).toBe("m-1-tail");
  });

  it("exempts only the FIRST resident turn", () => {
    const index = [indexRow(8, { id: "m-8-open" }), indexRow(9, { id: "m-9-open" })];
    const resident = [
      residentTurn(8, { id: "m-8-tail", trigger: undefined }),
      residentTurn(9, { id: "m-9-tail", trigger: undefined }),
    ];
    const { turns } = mergeTurnSets(resident, index, PAGED);
    expect(turns.map((t) => t.id)).toEqual(["m-8-open", "m-9-tail"]);
  });
});

describe("validating the index", () => {
  // A malformed index warns once per fetch, so every case here silences it and the
  // one case about the warn reads the spy.
  let warn: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
  });

  it("answers an empty set for a payload that is not an array", () => {
    expect(validateTurnIndex(undefined)).toEqual({ turns: [], dropped: 0 });
    expect(validateTurnIndex({ turns: [] })).toEqual({ turns: [], dropped: 0 });
  });

  it("drops a row whose id cannot be read", () => {
    const { turns, dropped } = validateTurnIndex([
      { id: 7, n: 1, ts: 1, outcome: "completed" },
      { id: "", n: 2, ts: 2, outcome: "completed" },
      { n: 3, ts: 3, outcome: "completed" },
      { id: "m-4", n: 4, ts: 4, outcome: "completed" },
    ]);
    expect(turns.map((t) => t.n)).toEqual([4]);
    expect(dropped).toBe(3);
  });

  it("drops a row whose n cannot be read", () => {
    const { turns, dropped } = validateTurnIndex([
      { id: "a", n: Number.NaN, ts: 1, outcome: "completed" },
      { id: "b", n: Number.POSITIVE_INFINITY, ts: 1, outcome: "completed" },
      { id: "c", n: 0, ts: 1, outcome: "completed" },
      { id: "d", n: -3, ts: 1, outcome: "completed" },
      { id: "e", n: 1.5, ts: 1, outcome: "completed" },
      { id: "f", n: "2", ts: 1, outcome: "completed" },
      { id: "g", n: 2, ts: 1, outcome: "completed" },
    ]);
    expect(turns.map((t) => t.id)).toEqual(["g"]);
    expect(dropped).toBe(6);
  });

  it("drops a row that is not an object at all", () => {
    const { turns, dropped } = validateTurnIndex([null, "row", 4, { id: "m-1", n: 1, ts: 1 }]);
    expect(turns).toHaveLength(1);
    expect(dropped).toBe(3);
  });

  it("keeps a row whose ts cannot be read, and reads no pause at it", () => {
    const { turns, dropped } = validateTurnIndex([
      { id: "a", n: 1, ts: 1000, outcome: "completed" },
      { id: "b", n: 2, ts: "soon", outcome: "completed" },
      { id: "c", n: 3, ts: 3000, outcome: "completed" },
    ]);
    expect(turns.map((t) => t.id)).toEqual(["a", "b", "c"]);
    expect(dropped).toBe(0);
    // Sat ON its predecessor, so it opens no seam of its own and the real pause
    // between the rows around it survives.
    expect(turns[1]?.ts).toBe(1000);
  });

  it("fills a LEADING unreadable ts from the row after it", () => {
    const { turns } = validateTurnIndex([
      { id: "a", n: 1, ts: -1, outcome: "completed" },
      { id: "b", n: 2, ts: 2000, outcome: "completed" },
    ]);
    expect(turns[0]?.ts).toBe(2000);
  });

  it("coerces an outcome the wire does not carry to unknown", () => {
    const { turns, dropped } = validateTurnIndex([
      { id: "a", n: 1, ts: 1, outcome: "exploded" },
      { id: "b", n: 2, ts: 2, outcome: 7 },
      { id: "c", n: 3, ts: 3 },
      { id: "d", n: 4, ts: 4, outcome: "refused" },
      // An inherited member name is not a member. `Object.hasOwn` is what makes
      // this a miss rather than the prototype's answer.
      { id: "e", n: 5, ts: 5, outcome: "constructor" },
    ]);
    expect(turns.map((t) => t.outcome)).toEqual([
      "unknown",
      "unknown",
      "unknown",
      "refused",
      "unknown",
    ]);
    expect(dropped).toBe(0);
  });

  it("coerces a first_line that is not a string to no label", () => {
    const { turns } = validateTurnIndex([
      { id: "a", n: 1, ts: 1, outcome: "completed", first_line: 12 },
      { id: "b", n: 2, ts: 2, outcome: "completed", first_line: "" },
      { id: "c", n: 3, ts: 3, outcome: "completed", first_line: "real" },
    ]);
    expect(turns[0]?.first_line).toBeUndefined();
    expect(turns[1]?.first_line).toBeUndefined();
    expect(turns[2]?.first_line).toBe("real");
  });

  it("coerces an agent_initiated that is not a boolean to false", () => {
    const { turns } = validateTurnIndex([
      { id: "a", n: 1, ts: 1, outcome: "completed", agent_initiated: "yes" },
      { id: "b", n: 2, ts: 2, outcome: "completed", agent_initiated: 1 },
      { id: "c", n: 3, ts: 3, outcome: "completed", agent_initiated: true },
    ]);
    expect(turns.map((t) => t.agent_initiated)).toEqual([false, false, true]);
  });

  it("logs once per fetch and stays quiet when nothing was dropped", () => {
    validateTurnIndex([
      { id: "", n: 1, ts: 1 },
      { id: "b", n: 0, ts: 1 },
      { id: "c", n: 3, ts: 1 },
    ]);
    expect(warn).toHaveBeenCalledTimes(1);
    warn.mockClear();
    validateTurnIndex([{ id: "c", n: 3, ts: 1 }]);
    expect(warn).not.toHaveBeenCalled();
  });

  it("derives the session count from the surviving rows", () => {
    const { turns } = validateTurnIndex([
      { id: "a", n: 1, ts: 1, outcome: "completed" },
      { id: "b", n: Number.NaN, ts: 2, outcome: "completed" },
      { id: "c", n: 3, ts: 3, outcome: "completed" },
    ]);
    const { total } = mergeTurnSets([], turns, WHOLE_SESSION);
    expect(total).toBe(3);
  });
});

describe("an outcome the merge cannot grade is still a real value", () => {
  it("keeps a resident outcome the wire declares", () => {
    const outcomes: TurnOutcome[] = [
      "running",
      "completed",
      "cancelled",
      "interrupted",
      "failed",
      "refused",
      "unknown",
    ];
    const resident = outcomes.map((outcome, i) => residentTurn(i + 1, { outcome }));
    const { turns } = mergeTurnSets(resident, [], WHOLE_SESSION);
    expect(turns.map((t) => t.outcome)).toEqual(outcomes);
  });
});
