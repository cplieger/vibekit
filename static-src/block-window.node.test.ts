// The residency planner, on its own: which ORDINALS a paint may mount around the
// reader's own position, given a block and tool-card budget.
//
// In the NODE project deliberately, and that placement is AC5's whole assertion:
// the planner's graph has no DOM, so a DOM import anywhere in it throws at module
// load and this file fails loudly instead of drifting. Pure, so every case states
// the whole input. The renderer's half of the contract — a turn the window does
// not reach renders as a stub — is turn-residency.test.ts's.
import { describe, it, expect } from "vitest";
import {
  planResidency,
  sliceTurn,
  turnCost,
  turnOrdinalOf,
  OVERSCAN_BLOCKS,
  RESIDENT_BLOCKS,
  RESIDENT_TOOL_CALLS,
  type ResidencyAnchor,
  type ResidencyPlan,
  type TurnRange,
} from "./block-window.js";
import type { Turn } from "./turns.js";
import type { Message } from "./types.js";

/** One assistant message carrying `blocks` text blocks and `tools` tool calls. */
function msg(id: string, blocks: number, tools = 0): Message {
  return {
    id,
    role: "assistant",
    ts: 2,
    blocks: Array.from({ length: blocks }, (_, i) => ({ type: "text", text: `b${String(i)}` })),
    tool_calls: Array.from({ length: tools }, (_, i) => ({
      id: `${id}-tc${String(i)}`,
      title: "Read file",
      kind: "read",
      status: "completed",
    })),
  } as unknown as Message;
}

/** One assistant message of `n` TOOL CARDS: `n` `tool_use` blocks, each with the
 *  call it references. The window charges the BLOCK, so a fixture for the tool
 *  budget has to carry the blocks and not only the calls. */
function toolMsg(id: string, n: number): Message {
  const ids = Array.from({ length: n }, (_, i) => `${id}-tc${String(i)}`);
  return {
    id,
    role: "assistant",
    ts: 2,
    blocks: ids.map((tc) => ({ type: "tool_use", tool_call_id: tc })),
    tool_calls: ids.map((tc) => ({
      id: tc,
      title: "Read file",
      kind: "read",
      status: "completed",
    })),
  } as unknown as Message;
}

/** Newest LAST in every fixture, the order `projectTurns` produces. */
function turn(id: string, body: Message[]): Turn {
  return {
    id,
    n: 1,
    trigger: { id: `${id}-trigger`, role: "user", ts: 1 } as unknown as Message,
    body,
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
  };
}

function spanOf(plan: ResidencyPlan, turnID: string): number {
  const range = plan.get(turnID);
  return range === undefined ? 0 : range.to - range.from;
}

describe("turnCost", () => {
  it("counts blocks and tool calls across the body", () => {
    expect(turnCost(turn("t", [msg("a", 3, 1), msg("b", 5, 2)]))).toEqual({
      blocks: 8,
      toolCalls: 3,
    });
  });

  it("counts a blockless message as one row", () => {
    // The reconcile unit is the message, so an event or a legacy message with no
    // blocks array is still a row the paint has to build.
    const bare = { id: "bare", role: "assistant", ts: 2 } as unknown as Message;
    expect(turnCost(turn("t", [bare, bare]))).toEqual({ blocks: 2, toolCalls: 0 });
  });

  it("does not count the trigger — the header renders it whether or not the turn is resident", () => {
    expect(turnCost(turn("t", []))).toEqual({ blocks: 0, toolCalls: 0 });
  });
});

describe("the ordinal space", () => {
  it("addresses a block by its message's base plus its own index", () => {
    const t = turn("t", [msg("a", 3), msg("b", 4)]);
    expect(turnOrdinalOf(t, "a", 0)).toBe(0);
    expect(turnOrdinalOf(t, "a", 2)).toBe(2);
    expect(turnOrdinalOf(t, "b", 0)).toBe(3);
    expect(turnOrdinalOf(t, "b", 3)).toBe(6);
  });

  it("answers a message's FIRST ordinal when no block index is given", () => {
    expect(turnOrdinalOf(turn("t", [msg("a", 3), msg("b", 4)]), "b")).toBe(3);
  });

  it("prices a blockless message at one ordinal, so the row after it is not overlapped", () => {
    const bare = { id: "bare", role: "assistant", ts: 2 } as unknown as Message;
    const t = turn("t", [bare, msg("b", 2)]);
    expect(turnOrdinalOf(t, "bare")).toBe(0);
    expect(turnOrdinalOf(t, "b", 0)).toBe(1);
  });

  it("answers undefined for a message the turn does not hold", () => {
    expect(turnOrdinalOf(turn("t", [msg("a", 2)]), "gone")).toBeUndefined();
  });

  it("clamps an index past its message's own span into that message", () => {
    // A search hit's block index can outlive the block it named; the answer has to
    // stay inside the message that owns it rather than naming the next row's.
    expect(turnOrdinalOf(turn("t", [msg("a", 3), msg("b", 4)]), "a", 99)).toBe(2);
  });

  it("slices a turn range into MESSAGE-LOCAL ranges, in body order", () => {
    const t = turn("t", [msg("a", 3), msg("b", 4), msg("c", 2)]);
    expect([...sliceTurn(t, { from: 2, to: 8 })]).toEqual([
      ["a", { from: 2, to: 3 }],
      ["b", { from: 0, to: 4 }],
      ["c", { from: 0, to: 1 }],
    ]);
  });

  it("leaves a message the range does not touch ABSENT, which is how its row is not mounted", () => {
    const t = turn("t", [msg("a", 3), msg("b", 4)]);
    expect([...sliceTurn(t, { from: 3, to: 7 }).keys()]).toEqual(["b"]);
  });

  it("gives a blockless message the range touches an EMPTY range", () => {
    const bare = { id: "bare", role: "assistant", ts: 2 } as unknown as Message;
    const t = turn("t", [msg("a", 2), bare]);
    expect(sliceTurn(t, { from: 0, to: 3 }).get("bare")).toEqual({ from: 0, to: 0 });
  });

  it("round-trips an ordinal through sliceTurn back to the block it names", () => {
    const t = turn("t", [msg("a", 3), msg("b", 4)]);
    const at = turnOrdinalOf(t, "b", 2) ?? -1;
    expect([...sliceTurn(t, { from: at, to: at + 1 })]).toEqual([["b", { from: 2, to: 3 }]]);
  });
});

describe("planResidency", () => {
  it("keeps every turn at its full range when the whole window fits", () => {
    const turns = [turn("t1", [msg("a1", 4)]), turn("t2", [msg("a2", 4)])];
    expect([...planResidency(turns, undefined)]).toEqual([
      ["t1", { from: 0, to: 4 }],
      ["t2", { from: 0, to: 4 }],
    ]);
  });

  it("windows a SINGLE over-budget turn to its TAIL at the live edge, which a turn count could not reach", () => {
    // AC1. The measured shape: one turn holding 580 blocks / 353 tool cards.
    // `TURNS_WARM = 5` mounted it whole; a turn-set budget made it a stub with no
    // route in but the toggle. The live edge is where a RUNNING turn's anchor
    // sits, so its window is its tail and its newest block is always in it.
    const turns = [turn("huge", [msg("a", RESIDENT_BLOCKS + 1)])];
    expect(planResidency(turns, undefined).get("huge")).toEqual({
      from: 1,
      to: RESIDENT_BLOCKS + 1,
    });
  });

  it("names an INTERIOR range for a turn the reader is parked inside", () => {
    // AC2. The anchor is the reader's own ordinal, so the window has ordinals to
    // spend on BOTH sides of it and the range touches neither end of the turn.
    const anchor: ResidencyAnchor = { turnID: "huge", at: 500 };
    const range = planResidency([turn("huge", [msg("a", 1000)])], anchor).get("huge");
    expect(range).toEqual({ from: 340, to: 660 });
  });

  it("grows both sides of an interior anchor by at least one overscan", () => {
    // What sizes a demand grant: a grant handed over on arrival must land in a
    // region at least as large, or the reader sees blocks leave as they get there.
    const anchor: ResidencyAnchor = { turnID: "huge", at: 500 };
    const range = planResidency([turn("huge", [msg("a", 1000)])], anchor).get("huge");
    expect(anchor.at - (range?.from ?? 0)).toBeGreaterThanOrEqual(OVERSCAN_BLOCKS);
    expect((range?.to ?? 0) - anchor.at).toBeGreaterThanOrEqual(OVERSCAN_BLOCKS);
  });

  it("spends the budget from the live edge back", () => {
    const turns = [
      turn("old", [msg("a1", 200)]),
      turn("mid", [msg("a2", 200)]),
      turn("new", [msg("a3", 200)]),
    ];
    // The tail latches at the sequence end, so the head spends the whole budget:
    // `new` whole, then 120 ordinals of `mid`, and nothing reaches `old`.
    const plan = planResidency(turns, undefined);
    expect(plan.get("old")).toBeUndefined();
    expect(plan.get("mid")).toEqual({ from: 80, to: 200 });
    expect(plan.get("new")).toEqual({ from: 0, to: 200 });
  });

  it("cuts the window at the TOOL budget, short of what the block budget would allow", () => {
    const turns = [turn("t", [toolMsg("a", RESIDENT_TOOL_CALLS + 8)])];
    // Every ordinal is a tool card, so 96 of them is the whole window — far short
    // of the 320 blocks the block budget alone would have admitted.
    expect(spanOf(planResidency(turns, undefined), "t")).toBe(RESIDENT_TOOL_CALLS);
  });

  it("is contiguous: a cheap turn behind an over-budget one gets no ordinal", () => {
    const turns = [
      turn("cheap", [msg("a1", 1)]),
      turn("huge", [msg("a2", RESIDENT_BLOCKS + 1)]),
      turn("newest", [msg("a3", 1)]),
    ];
    // Without per-side latching from one seed `cheap` would fit in what `huge` did
    // not spend, and the reader would get a mounted turn between two holes.
    const plan = planResidency(turns, undefined);
    expect(plan.get("cheap")).toBeUndefined();
    expect(plan.get("huge")).toEqual({ from: 2, to: RESIDENT_BLOCKS + 1 });
    expect(plan.get("newest")).toEqual({ from: 0, to: 1 });
  });

  it("holds ONE contiguous run across three turns that each exceed the budget", () => {
    // AC3. Both halves of contiguity at once: each turn's range is a single run,
    // and the turns holding ordinals are adjacent in the sequence.
    const turns = [
      turn("t1", [msg("a1", RESIDENT_BLOCKS + 1)]),
      turn("t2", [msg("a2", RESIDENT_BLOCKS + 1)]),
      turn("t3", [msg("a3", RESIDENT_BLOCKS + 1)]),
    ];
    const plan = planResidency(turns, { turnID: "t2", at: 200 });
    expect(plan.get("t1")).toBeUndefined();
    expect(plan.get("t2")).toEqual({ from: 40, to: RESIDENT_BLOCKS + 1 });
    expect(plan.get("t3")).toEqual({ from: 0, to: 39 });
  });

  it("bounds a 700-block turn to the same order of blocks as a 20-block turn", () => {
    // AC4. The point of the whole feature: what a paint costs stops tracking how
    // big the turn the reader is looking at happens to be.
    const big = planResidency([turn("big", [msg("a", 700)])], { turnID: "big", at: 350 });
    const small = planResidency([turn("small", [msg("a", 20)])], { turnID: "small", at: 10 });
    expect(spanOf(big, "big")).toBe(RESIDENT_BLOCKS);
    expect(spanOf(small, "small")).toBe(20);
  });

  it("takes the budget as a parameter, so a caller can state a different one", () => {
    const turns = [turn("t1", [msg("a1", 4)]), turn("t2", [msg("a2", 4)])];
    expect([...planResidency(turns, undefined, { blocks: 4, toolCalls: 8 })]).toEqual([
      ["t2", { from: 0, to: 4 }],
    ]);
  });

  it("clamps an anchor whose ordinal is outside its own turn", () => {
    const turns = [turn("t1", [msg("a1", 4)]), turn("t2", [msg("a2", 4)])];
    const plan = planResidency(turns, { turnID: "t1", at: 99 }, { blocks: 2, toolCalls: 8 });
    expect([...plan]).toEqual([["t1", { from: 2, to: 4 }]]);
  });

  it("falls back to the live edge for an anchor naming a turn the sequence does not hold", () => {
    // A stale id — a rewind or a page eviction between the read and the pass. It
    // has no position in this sequence, so there is no nearest turn to step to.
    const turns = [turn("t1", [msg("a1", 4)]), turn("t2", [msg("a2", 4)])];
    const plan = planResidency(turns, { turnID: "gone", at: 0 }, { blocks: 2, toolCalls: 8 });
    expect([...plan]).toEqual([["t2", { from: 2, to: 4 }]]);
  });

  it("seeds at the NEXT turn's first ordinal for an anchor on a turn holding no ordinal", () => {
    // A bodyless card has no ordinal of its own to seed on, and the reader is
    // looking at it where it sits — so the window must open around that position
    // rather than being thrown to the live edge.
    const turns = [turn("t1", [msg("a1", 4)]), turn("waiting", []), turn("t2", [msg("a2", 4)])];
    const plan = planResidency(turns, { turnID: "waiting", at: 0 }, { blocks: 3, toolCalls: 8 });
    expect([...plan]).toEqual([
      ["t1", { from: 3, to: 4 }],
      ["t2", { from: 0, to: 2 }],
    ]);
  });

  it("keeps the seed inside the sequence for an anchor on a TRAILING turn holding no ordinal", () => {
    // The same turn with nothing after it: its base is one PAST the last ordinal,
    // and the answer is the live edge rather than an ordinal that does not exist.
    const turns = [turn("t1", [msg("a1", 4)]), turn("waiting", [])];
    const plan = planResidency(turns, { turnID: "waiting", at: 0 }, { blocks: 2, toolCalls: 8 });
    expect([...plan]).toEqual([["t1", { from: 2, to: 4 }]]);
  });

  it("gives no entry to anything holding no ordinal", () => {
    // A zero-length entry would tell the renderer to mount a body with no rows;
    // absence is what leaves the card a header with nothing under it.
    const nothing: ResidencyPlan = planResidency([], undefined);
    expect(nothing.size).toBe(0);
    const waiting = planResidency([turn("waiting", []), turn("t", [msg("a", 4)])], undefined);
    expect([...waiting.keys()]).toEqual(["t"]);
  });

  it("answers ranges the renderer can slice without subtracting a base itself", () => {
    // The two coordinate spaces meet here and nowhere else: what the plan hands
    // back is turn-local, which is exactly what `sliceTurn` takes.
    const t = turn("t", [msg("a", 4), msg("b", 4)]);
    const range = planResidency([t], undefined, { blocks: 3, toolCalls: 8 }).get("t") as TurnRange;
    expect(range).toEqual({ from: 5, to: 8 });
    expect([...sliceTurn(t, range)]).toEqual([["b", { from: 1, to: 4 }]]);
  });
});
