// The residency planner, on its own: which turns a paint may mount, given a
// block and tool-card budget.
//
// Pure and DOM-free, so every case states the whole input. The renderer's half of
// the contract — that a non-resident turn renders as a stub, that a stub offers
// the toggle that reveals it — is turn-residency.test.ts's.
import { describe, it, expect } from "vitest";
import { planResidency, turnCost, RESIDENT_BLOCKS, RESIDENT_TOOL_CALLS } from "./block-window.js";
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

/** Newest LAST, the order `projectTurns` produces. */
const never_pinned = (): boolean => false;

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

describe("planResidency", () => {
  it("keeps every turn when the whole window fits", () => {
    const turns = [turn("t1", [msg("a1", 4)]), turn("t2", [msg("a2", 4)])];
    expect([...planResidency(turns, never_pinned)]).toEqual(["t2", "t1"]);
  });

  it("stubs a SINGLE over-budget turn, which is the case a turn count cannot reach", () => {
    // The measured shape: one turn holding 580 blocks / 353 tool cards. Under
    // `TURNS_WARM = 5` it was inside the window and mounted whole.
    const turns = [turn("huge", [msg("a", RESIDENT_BLOCKS + 1)])];
    expect(planResidency(turns, never_pinned).size).toBe(0);
  });

  it("spends the budget newest-first", () => {
    const turns = [
      turn("old", [msg("a1", 200)]),
      turn("mid", [msg("a2", 200)]),
      turn("new", [msg("a3", 100)]),
    ];
    // 100 fits, +200 fits (300 <= 320), +200 does not.
    expect([...planResidency(turns, never_pinned)].sort()).toEqual(["mid", "new"]);
  });

  it("stubs the tool-heavy turn the block budget alone would let through", () => {
    const turns = [turn("t", [msg("a", 100, RESIDENT_TOOL_CALLS + 1)])];
    expect(planResidency(turns, never_pinned).size).toBe(0);
  });

  it("is contiguous: a cheap turn behind an over-budget one is a stub too", () => {
    const turns = [
      turn("cheap", [msg("a1", 1)]),
      turn("huge", [msg("a2", RESIDENT_BLOCKS + 1)]),
      turn("newest", [msg("a3", 1)]),
    ];
    // Without the latch `cheap` would fit in what `huge` did not spend, and the
    // reader would get a resident turn between two stubs.
    expect([...planResidency(turns, never_pinned)]).toEqual(["newest"]);
  });

  it("keeps a pinned turn whatever the budget says", () => {
    const turns = [
      turn("pinned", [msg("a1", RESIDENT_BLOCKS * 2)]),
      turn("newest", [msg("a2", 1)]),
    ];
    expect([...planResidency(turns, (t) => t.id === "pinned")].sort()).toEqual([
      "newest",
      "pinned",
    ]);
  });

  it("charges a pinned turn's cost to the budget, so the turns behind it stub", () => {
    const turns = [
      turn("behind", [msg("a1", 8)]),
      turn("pinned", [msg("a2", RESIDENT_BLOCKS)]),
      turn("newest", [msg("a3", 8)]),
    ];
    // newest (8) + pinned (320) is already past the budget, so `behind` cannot
    // be afforded even though it is tiny.
    expect([...planResidency(turns, (t) => t.id === "pinned")].sort()).toEqual([
      "newest",
      "pinned",
    ]);
  });

  it("takes the budget as a parameter, so a caller can state a different one", () => {
    const turns = [turn("t1", [msg("a1", 4)]), turn("t2", [msg("a2", 4)])];
    expect([...planResidency(turns, never_pinned, { blocks: 4, toolCalls: 8 })]).toEqual(["t2"]);
  });

  it("answers empty for an empty window", () => {
    expect(planResidency([], never_pinned).size).toBe(0);
  });
});
