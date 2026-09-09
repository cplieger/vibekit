// What unmounted ordinals are worth in pixels, on its own.
//
// In the NODE project because the arithmetic is pure: the measurement lives at the
// drop, which is why this module can be tested with no browser at all. Every case
// states the whole cache, so a number here is never inherited from a fixture.
import { describe, it, expect } from "vitest";
import {
  BLOCK_ESTIMATE_PX,
  forgetHeights,
  recordBlockHeight,
  recordRowHeight,
  spacerHeight,
} from "./block-heights.js";
import type { Turn } from "./turns.js";
import type { Message } from "./types.js";

/** The row estimate `.msg-row` declares, and what an unmeasured text block costs. */
const TEXT_PX = 48;

/** The flex `gap` (`--sp-3`) `.turn-body` puts between rows and `.msg-wrap` puts
 *  between one row's blocks, which no child's own height carries. */
const GAP_PX = 12;

/** THE PREMISE for every number below: the table is TIER-KEYED, and this project has
 *  no `document` and no `matchMedia`, so `tierNow`'s fallback arm resolves to FINE.
 *  Stated rather than assumed, because a runtime that grew either global would move
 *  four of the seven prices silently — Node has already grown a `navigator` this way.
 *  Read off the table so a retune moves the assertions with it. */
const FINE = BLOCK_ESTIMATE_PX.fine;

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

/** A pipeline's DRIVER id, and one STAGE id per stage in the shape KAS mints —
 *  `invoke_subagent_<driverId>_stage_<name>`, which is how a stage names its parent. */
const DRIVER_ID = "orc-1";
const STAGE_NAMES = ["plan", "build", "review"] as const;

function stageID(name: string): string {
  return `invoke_subagent_${DRIVER_ID}_stage_${name}`;
}

/** The driver's block followed by one INVOCATION block per stage: the blocks the
 *  transcript still mounts something for. A stage's own output blocks are dropped by
 *  `isDroppedBlock` and priced in `block-heights-steps.node.test.ts`. */
function pipelineBlocks(stages: number): unknown[] {
  const out: unknown[] = [{ type: "tool_use", tool_call_id: DRIVER_ID }];
  for (const name of STAGE_NAMES.slice(0, stages)) {
    out.push({
      type: "tool_use",
      tool_call_id: stageID(name),
      agent_subtask_id: `sub-${name}`,
    });
  }
  return out;
}

/** Their tool calls. The stage title is the `Sub-agent:` PREFIX form, which is what
 *  `isSubagentInvocation` matches and what 385 of 386 live matches carry. */
function pipelineCalls(stages: number): unknown[] {
  const out: unknown[] = [
    { id: DRIVER_ID, title: "Orchestrate Sub-agent", kind: "other", status: "completed" },
  ];
  for (const name of STAGE_NAMES.slice(0, stages)) {
    out.push({
      id: stageID(name),
      title: `Sub-agent: ${name}`,
      kind: "other",
      status: "completed",
      agent_subtask_id: `sub-${name}`,
    });
  }
  return out;
}

describe("the per-outcome estimate", () => {
  it("resolves the FINE tier here, with no document and no matchMedia to read", () => {
    // The premise the whole file rests on, in two halves. The ABSENCE is the condition
    // `tierNow`'s fallback arm keys on, and it is asserted rather than assumed because a
    // runtime that grew either global would move four of the seven prices silently —
    // Node has already grown a `navigator` this way. The PRICE then proves the arm
    // actually landed on fine, and it uses `thinking`, which is one of the four entries
    // the two tiers disagree about (25 against 44), so a coarse resolution fails here.
    const g = globalThis as { readonly document?: unknown; readonly matchMedia?: unknown };
    expect({ document: g.document, matchMedia: g.matchMedia }).toEqual({
      document: undefined,
      matchMedia: undefined,
    });
    expect(FINE.thinking, "the two tiers disagree, so this price is a tier witness").not.toBe(
      BLOCK_ESTIMATE_PX.coarse.thinking,
    );
    const t = turn([assistant("a", [{ type: "thinking", thinking: "considering" }])]);
    expect(wholeTurn(t)).toBe(FINE.thinking);
  });

  it("prices an unmeasured text block at the row height CSS declares", () => {
    expect(wholeTurn(turn([assistant("a", [text("hello")])]))).toBe(TEXT_PX);
  });

  it("prices an EMPTY text block at nothing, GAP included — a pad is display:none", () => {
    // Both blocks are covered, so a non-zero answer for the pad would show up as
    // double the real block's height. It costs no gap either: `.msg-row.is-empty` is
    // removed from the flex flow, and `gap` counts items rather than heights.
    expect(wholeTurn(turn([assistant("a", [text(""), text("hello")])]))).toBe(TEXT_PX);
  });

  it("prices a sealed thinking trace at its measured collapsed height", () => {
    // NOT a reserve: `.reasoning-block` declares no `content-visibility`, so there is
    // nothing to shadow. The value is the real collapsed height of a sealed trace — its
    // `<summary>` row, 25px on this tier — which is why it is the one entry in the table
    // with no CSS counterpart beside it.
    const t = turn([assistant("a", [{ type: "thinking", thinking: "considering" }])]);
    expect(wholeTurn(t)).toBe(25);
  });

  it("prices a tool_use block at the tool card's claim line PLUS its border", () => {
    // `.tool-call` reserves `auto var(--btn-h)` — 36px of CONTENT — and the box model
    // adds the card's 2px border to a skipped card and a rendered one alike, so the
    // rendered box is 38.
    const t = turn([
      assistant(
        "a",
        [{ type: "tool_use", tool_call_id: "tc" }],
        [{ id: "tc", title: "Read file", kind: "read", status: "completed" }],
      ),
    ]);
    expect(wholeTurn(t)).toBe(38);
  });

  it("prices a DELEGATE INVOCATION at the subagent card, not at the tool row", () => {
    // Its element is a `.subagent-block`, and `isDroppedBlock` keeps exactly one of a
    // delegate's blocks — this one — so the card's height is reserved here or nowhere.
    // The invocation is identified by TITLE (`isSubagentInvocation`), which is what
    // separates it from the delegate's own nested calls sharing its subtask id.
    const t = turn([
      assistant(
        "a",
        [{ type: "tool_use", tool_call_id: "tc-inv", agent_subtask_id: "sub-1" }],
        [{ id: "tc-inv", title: "invoke_sub_agent", kind: "other", status: "completed" }],
      ),
    ]);
    expect(wholeTurn(t)).toBe(71);
  });

  it("prices a PIPELINE DRIVER at the box it mounts, not at a tool row", () => {
    // Its block mounts a `.subagent-container`, which RESTS at the same 71px a card
    // does: the collapsed body holding its stages is out of layout. `Orchestrate
    // Sub-agent` is deliberately absent from `isSubagentInvocation` — one title with two
    // owners makes a classification unpredictable — so this is what stops a driver
    // falling through to the tool card's 38.
    const t = turn([
      assistant(
        "a",
        [{ type: "tool_use", tool_call_id: "orc-1" }],
        [{ id: "orc-1", title: "Orchestrate Sub-agent", kind: "other", status: "completed" }],
      ),
    ]);
    expect(wholeTurn(t)).toBe(71);
  });

  it("prices a whole PIPELINE at ONE card, its three stages included", () => {
    // The container rests at one card's height whatever it holds — measured at 71px per
    // box for 1, 3 and 8 stages alike (`block-heights-css.test.ts`) — so the pipeline's
    // price is its DRIVER's alone and a stage is worth nothing. A stage names its driver
    // in its own tool-call id, which is the join this reads.
    const t = turn([assistant("a", pipelineBlocks(3), pipelineCalls(3))]);
    expect(wholeTurn(t)).toBe(71);
  });

  it("prices a PROMOTED single-stage pipeline at one card too", () => {
    // One stage, so the renderer promotes its card to where the container would have
    // gone and the DRIVER's block renders nothing at all (`driverNeedsBox` refuses a box
    // at a count of 1). The two blocks' prices are swapped against that — 71 at the
    // driver, 0 at the stage — and what is exact is the pipeline's TOTAL.
    const t = turn([assistant("a", pipelineBlocks(1), pipelineCalls(1))]);
    expect(wholeTurn(t)).toBe(71);
  });

  it("prices a `Sub-agent:`-TITLED call with no stage id at a full card", () => {
    // The control that keeps this keyed on the ID rather than the title, and a real
    // population rather than a contrived one: measured over the 111 chat files on one
    // live volume, 392 of 393 delegate invocations carry the `Sub-agent:` prefix while
    // only 369 carry the `_stage_` id shape. The other 23 look like this —
    // `invoke_subagent_<driver>-sub-agent-start` — and `stagePipelineID` answers "" for
    // one, so the renderer seats its card at the TOP LEVEL, where it costs a whole card.
    // A title-keyed rule would price every one of them at nothing.
    const id = `invoke_subagent_${DRIVER_ID}-sub-agent-start`;
    const t = turn([
      assistant(
        "a",
        [{ type: "tool_use", tool_call_id: id, agent_subtask_id: "sub-flat" }],
        [
          {
            id,
            title: "Sub-agent: wf-coder",
            kind: "other",
            status: "completed",
            agent_subtask_id: "sub-flat",
          },
        ],
      ),
    ]);
    expect(wholeTurn(t)).toBe(71);
  });

  it("prices a pipeline STAGE at nothing on its own, the driver holding the price", () => {
    // The stage without its driver's block: 0, because inside a rendered container its
    // card sits at height 0. The control that stops this collapsing into "any delegate
    // invocation is free" is the PLAIN-delegate case above, whose id names no driver.
    const t = turn([assistant("a", pipelineBlocks(1).slice(1), pipelineCalls(1).slice(1))]);
    expect(wholeTurn(t)).toBe(0);
  });

  it("charges ONE box for a pipeline, so its stages add no GAP", () => {
    // `gap` counts items rather than heights, and the container is one item in the block
    // lane however many stage cards it holds. Two zero-priced stages between two rows
    // must therefore add nothing — the rule an empty pad already follows.
    const t = turn([
      assistant("a", [text("before"), ...pipelineBlocks(2), text("after")], pipelineCalls(2)),
    ]);
    expect(wholeTurn(t)).toBe(TEXT_PX + GAP_PX + 71 + GAP_PX + TEXT_PX);
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
    expect(wholeTurn(t)).toBe(79);
  });

  it("prices a blockless message at one row", () => {
    const bare = { id: "bare", role: "assistant", ts: 2 } as unknown as Message;
    expect(wholeTurn(turn([bare]))).toBe(TEXT_PX);
  });

  it("sums across every message the spacer covers, and the gaps at BOTH levels", () => {
    const t = turn([
      assistant("a", [text("x"), text("y")]),
      assistant("b", [{ type: "thinking", thinking: "z" }]),
    ]);
    // Two whole rows behind ONE spacer, three boxes in total: the parent separated
    // the rows with a gap and `a`'s own two blocks with another, and no child's
    // height includes either — so the spacer is what has to carry both.
    expect(spacerHeight(t, { from: 3, to: 3 }, "head")).toBe(
      TEXT_PX + GAP_PX + TEXT_PX + 25 + GAP_PX,
    );
  });

  it("adds no gap for ONE row behind the spacer, whose own box replaces its gap", () => {
    const t = turn([assistant("a", [text("x")]), assistant("b", [text("y")])]);
    expect(spacerHeight(t, { from: 1, to: 2 }, "head")).toBe(TEXT_PX);
  });

  it("charges a row the spacer only PARTLY covers one row-level gap, not two", () => {
    // The windowed long message: one whole row above it and the head of the row
    // the window cuts into. That row STAYS mounted, so its own gap to the spacer
    // stays in the body — but the gap the dropped block held INSIDE it leaves, which
    // is why the boundary row counts as a unit rather than as nothing.
    const t = turn([assistant("a", [text("x")]), assistant("b", [text("y"), text("z")])]);
    expect(spacerHeight(t, { from: 2, to: 3 }, "head")).toBe(TEXT_PX + TEXT_PX + GAP_PX);
  });
});

describe("which ordinals a spacer stands in for", () => {
  const four = (): Turn => turn([assistant("a", [text("w"), text("x"), text("y"), text("z")])]);

  it("prices the ordinals BEFORE the mounted range for the head spacer", () => {
    expect(spacerHeight(four(), { from: 3, to: 4 }, "head")).toBe(3 * TEXT_PX + 2 * GAP_PX);
  });

  it("prices the ordinals AFTER the mounted range for the tail spacer", () => {
    expect(spacerHeight(four(), { from: 0, to: 1 }, "tail")).toBe(3 * TEXT_PX + 2 * GAP_PX);
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
    expect(wholeTurn(t)).toBe(TEXT_PX + GAP_PX + 300);
  });

  it("prefers the whole-ROW measurement for a row entirely outside the window", () => {
    // A row's own height already carries the gaps between its blocks, so nothing is
    // added on top of a measurement that covers the range being priced.
    recordRowHeight("m-row", { from: 0, to: 2 }, 500);
    const t = turn([assistant("m-row", [text("x"), text("y")])]);
    expect(wholeTurn(t)).toBe(500);
  });

  it("refuses a row measurement taken over a DIFFERENT range than the one priced", () => {
    // The row was dropped holding two blocks and only one is being priced, so its
    // height answers for ordinals the body is still holding. Reachable whenever the
    // window narrows a row before the row leaves whole.
    recordRowHeight("m-part", { from: 0, to: 2 }, 500);
    recordBlockHeight("m-part", 1, 300);
    const t = turn([assistant("m-part", [text("x"), text("y")])]);
    expect(spacerHeight(t, { from: 0, to: 1 }, "tail")).toBe(300);
  });

  it("forgets a message's measurements, so the estimate answers again", () => {
    recordRowHeight("m-forget", { from: 0, to: 1 }, 500);
    recordBlockHeight("m-forget", 0, 300);
    const t = turn([assistant("m-forget", [text("x")])]);
    expect(wholeTurn(t)).toBe(500);
    forgetHeights(["m-forget"]);
    expect(wholeTurn(t)).toBe(TEXT_PX);
  });
});
