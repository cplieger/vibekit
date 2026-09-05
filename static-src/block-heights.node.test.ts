// What unmounted ordinals are worth in pixels, on its own.
//
// In the NODE project because the arithmetic is pure: the measurement lives at the
// drop, which is why this module can be tested with no browser at all. Every case
// states the whole cache, so a number here is never inherited from a fixture.
import { describe, it, expect } from "vitest";
import {
  forgetHeights,
  recordBlockHeight,
  recordRowHeight,
  spacerHeight,
} from "./block-heights.js";
import type { Turn } from "./turns.js";
import type { Message } from "./types.js";

/** The row estimate `.msg-row` declares, and what an unmeasured text block costs. */
const TEXT_PX = 48;

function text(t: string): unknown {
  return { type: "text", text: t };
}

function assistant(id: string, blocks: unknown[], toolCalls: unknown[] = []): Message {
  return { id, role: "assistant", ts: 2, blocks, tool_calls: toolCalls } as unknown as Message;
}

function turn(body: Message[]): Turn {
  return {
    id: "t",
    n: 1,
    trigger: undefined,
    body,
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
  };
}

/** The whole turn, priced: nothing is mounted, so the tail spacer stands in for
 *  every ordinal it has. */
function wholeTurn(t: Turn): number {
  return spacerHeight(t, { from: 0, to: 0 }, "tail");
}

describe("the per-outcome estimate", () => {
  it("prices an unmeasured text block at the row height CSS declares", () => {
    expect(wholeTurn(turn([assistant("a", [text("hello")])]))).toBe(TEXT_PX);
  });

  it("prices an EMPTY text block at nothing — a pad mounts a zero-height row", () => {
    // Both blocks are covered, so a non-zero answer for the pad would show up as
    // double the real block's height.
    expect(wholeTurn(turn([assistant("a", [text(""), text("hello")])]))).toBe(TEXT_PX);
  });

  it("prices a sealed thinking trace at a collapsed card's claim line", () => {
    const t = turn([assistant("a", [{ type: "thinking", thinking: "considering" }])]);
    expect(wholeTurn(t)).toBe(40);
  });

  it("prices a tool_use block at the tool card's claim line", () => {
    const t = turn([
      assistant(
        "a",
        [{ type: "tool_use", tool_call_id: "tc" }],
        [{ id: "tc", title: "Read file", kind: "read", status: "completed" }],
      ),
    ]);
    expect(wholeTurn(t)).toBe(40);
  });

  it("prices a WORKFLOW LAUNCH higher, because it mounts a run card and not a tool row", () => {
    const t = turn([
      assistant(
        "a",
        [{ type: "tool_use", tool_call_id: "tc" }],
        [
          {
            id: "tc",
            title: "Run Workflow",
            kind: "other",
            status: "completed",
            workflow_id: "wf-1",
          },
        ],
      ),
    ]);
    expect(wholeTurn(t)).toBe(64);
  });

  it("prices a blockless message at one row", () => {
    const bare = { id: "bare", role: "assistant", ts: 2 } as unknown as Message;
    expect(wholeTurn(turn([bare]))).toBe(TEXT_PX);
  });

  it("sums across every message the spacer covers", () => {
    const t = turn([
      assistant("a", [text("x"), text("y")]),
      assistant("b", [{ type: "thinking", thinking: "z" }]),
    ]);
    expect(spacerHeight(t, { from: 3, to: 3 }, "head")).toBe(TEXT_PX + TEXT_PX + 40);
  });
});

describe("which ordinals a spacer stands in for", () => {
  const four = (): Turn => turn([assistant("a", [text("w"), text("x"), text("y"), text("z")])]);

  it("prices the ordinals BEFORE the mounted range for the head spacer", () => {
    expect(spacerHeight(four(), { from: 3, to: 4 }, "head")).toBe(3 * TEXT_PX);
  });

  it("prices the ordinals AFTER the mounted range for the tail spacer", () => {
    expect(spacerHeight(four(), { from: 0, to: 1 }, "tail")).toBe(3 * TEXT_PX);
  });

  it("stands in for nothing once the mounted range reaches the turn's own edge", () => {
    expect(spacerHeight(four(), { from: 0, to: 4 }, "head")).toBe(0);
    expect(spacerHeight(four(), { from: 0, to: 4 }, "tail")).toBe(0);
  });
});

describe("what a drop measured", () => {
  it("prefers a measured block height over that block's estimate", () => {
    recordBlockHeight("m-measured", 1, 300);
    const t = turn([assistant("m-measured", [text("x"), text("y")])]);
    expect(wholeTurn(t)).toBe(TEXT_PX + 300);
  });

  it("prefers the whole-ROW measurement for a row entirely outside the window", () => {
    recordRowHeight("m-row", 500);
    const t = turn([assistant("m-row", [text("x"), text("y")])]);
    expect(wholeTurn(t)).toBe(500);
  });

  it("falls back to per-block numbers when only PART of a row is outside the window", () => {
    // The row number prices two blocks and one of them is still mounted, so using
    // it here would count height the body is already holding.
    recordRowHeight("m-part", 500);
    recordBlockHeight("m-part", 1, 300);
    const t = turn([assistant("m-part", [text("x"), text("y")])]);
    expect(spacerHeight(t, { from: 0, to: 1 }, "tail")).toBe(300);
  });

  it("forgets a message's measurements, so the estimate answers again", () => {
    recordRowHeight("m-forget", 500);
    recordBlockHeight("m-forget", 0, 300);
    const t = turn([assistant("m-forget", [text("x")])]);
    expect(wholeTurn(t)).toBe(500);
    forgetHeights(["m-forget"]);
    expect(wholeTurn(t)).toBe(TEXT_PX);
  });
});
