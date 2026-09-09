// What a block the transcript DROPS is worth, in both halves of the arithmetic:
// nothing. Two populations, and `isDroppedBlock` is the one owner of the question —
// a WORKFLOW STEP's blocks, and every block a DELEGATE produced except the
// invocation that becomes its card.
//
// A separate file from `block-heights.node.test.ts` because the subject is a
// different one — that file prices what the transcript MOUNTS, and these blocks are
// the population it never mounts at all (`messages-blocks.ts` `placeBlock` drops
// them, the tab that owns the content rendering it instead). In the NODE project for
// the same reason as its sibling: the arithmetic is pure.
import { describe, it, expect } from "vitest";
import { spacerHeight } from "./block-heights.js";
import {
  planResidency,
  sliceTurn,
  turnCost,
  RESIDENT_BLOCKS,
  RESIDENT_TOOL_CALLS,
  type TurnRange,
} from "./block-window.js";
import type { Turn } from "./turns.js";
import type { Message } from "./types.js";

/** What an unmeasured text block costs, and the number every case here is the
 *  absence of. */
const TEXT_PX = 48;

/** `.turn-body`'s flex `gap`, which no child's own height carries. */
const GAP_PX = 12;

/** What an unmeasured DELEGATE CARD costs — its element is a `.subagent-block`, whose
 *  reserve renders at 71px on this project's tier. Not the tool card's 38: the
 *  invocation is the one block of a delegate's the transcript mounts, and what it
 *  mounts is the card. */
const CARD_PX = 71;

/** A step's subtask id, in the spelling the server stamps
 *  (`vibekit.StepSubtaskID`: `wf:<workflowId>:<nodePath>`). */
const STEP = "wf:run-7:seq/coder";

/** A DELEGATE's subtask id: a bare uuid, which is what makes it not a step's. */
const DELEGATE = "sub-9";

/** One of the four titles `tool-schema.ts` `isSubagentInvocation` accepts. */
const INVOKE = "invoke_sub_agent";

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

describe("a workflow step's blocks in the transcript's arithmetic", () => {
  it("prices a step's text block at nothing", () => {
    // The transcript renders no element for it, so no measurement can ever correct
    // an estimate here: whatever it reserves, it reserves permanently.
    const t = turn([
      assistant("a", [{ type: "text", text: "step prose", agent_subtask_id: STEP }]),
    ]);
    expect(wholeTurn(t)).toBe(0);
  });

  it("prices a step's reasoning and tool blocks at nothing either", () => {
    const t = turn([
      assistant(
        "a",
        [
          { type: "thinking", thinking: "considering", agent_subtask_id: STEP },
          { type: "tool_use", tool_call_id: "tc-step", agent_subtask_id: STEP },
        ],
        [{ id: "tc-step", title: "Read file", kind: "read", status: "completed" }],
      ),
    ]);
    expect(wholeTurn(t)).toBe(0);
  });

  it("charges no GAP for one, so a step between two rendered blocks costs nothing", () => {
    // Two boxes carry one gap between them. A third block that renders nowhere must
    // not add a second, the same rule an empty pad already follows.
    const t = turn([
      assistant("a", [
        { type: "text", text: "before" },
        { type: "text", text: "step prose", agent_subtask_id: STEP },
        { type: "text", text: "after" },
      ]),
    ]);
    expect(wholeTurn(t)).toBe(2 * TEXT_PX + GAP_PX);
  });

  it("prices a DELEGATE's own prose at nothing too", () => {
    // The transcript draws a delegate's CARD and none of its output, so this block is
    // in the same position a step's is. OVERTURNED: this case used to assert TEXT_PX on
    // the premise that "a subagent page renders these same blocks, so a drop here would
    // price a real surface at zero". The page reaches no price — it slices through
    // `subagent-slice.ts` and imports neither this module nor `block-window.ts` — and
    // `spacerHeight` has exactly one consumer, the transcript's own spacer.
    const t = turn([
      assistant("a", [{ type: "text", text: "delegate prose", agent_subtask_id: DELEGATE }]),
    ]);
    expect(wholeTurn(t)).toBe(0);
  });

  it("keeps pricing the delegate's INVOCATION, which becomes its card", () => {
    // The control that stops the rule collapsing into "any subtask id is worth
    // nothing": the invocation is the ONE block of a delegate's the transcript mounts,
    // and the card's height is reserved here or nowhere.
    const t = turn([
      assistant(
        "a",
        [{ type: "tool_use", tool_call_id: "tc-inv", agent_subtask_id: DELEGATE }],
        [{ id: "tc-inv", title: INVOKE, kind: "other", status: "completed" }],
      ),
    ]);
    expect(wholeTurn(t)).toBe(CARD_PX);
  });

  it("prices a delegate's NESTED tool call at nothing, invocation title or not", () => {
    // A delegate's nested calls share its subtask id and never carry an invocation
    // title, which is the whole of how `isSubagentInvocation` separates them. Keyed on
    // the title rather than on being the first such block, so ordering cannot decide it.
    const t = turn([
      assistant(
        "a",
        [{ type: "tool_use", tool_call_id: "tc-nested", agent_subtask_id: DELEGATE }],
        [{ id: "tc-nested", title: "Read file", kind: "read", status: "completed" }],
      ),
    ]);
    expect(wholeTurn(t)).toBe(0);
  });

  it("keeps pricing a block with NO subtask id at all", () => {
    // The outer control: the chat's own work is what the transcript exists to render,
    // and it must survive every arm above.
    const t = turn([assistant("a", [{ type: "text", text: "the agent's own answer" }])]);
    expect(wholeTurn(t)).toBe(TEXT_PX);
  });

  it("prices a block whose step id is MALFORMED at nothing, via the delegate arm", () => {
    // OVERTURNED: this case used to assert TEXT_PX because `placeBlock` "falls back to
    // the delegate box rather than losing the block — so it has a destination and earns
    // its height". That box has no body any more; the fallback seats a CARD and renders
    // no block, so the fallback is itself a drop. `parseStepSubtask` still answers null
    // for one — the two sides read the same parser — it just no longer changes the price.
    const t = turn([
      assistant("a", [{ type: "text", text: "prose", agent_subtask_id: "wf:no-node-path" }]),
    ]);
    expect(wholeTurn(t)).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// The other half of the same fact: what a step's ordinals cost the residency
// BUDGET. Both halves have to agree, or the window spends its budget on ordinals
// the spacer above prices at zero — which is the state this file's first half
// landed in on its own.
// ---------------------------------------------------------------------------

/** Comfortably past `RESIDENT_BLOCKS`, so a window is the only way anything mounts. */
const PAST_BUDGET = RESIDENT_BLOCKS + 200;

/** One turn of `steps` step blocks with `prose` renderable ones after them: the live
 *  shape, where the ordinals outnumber the rendered blocks by two orders of magnitude. */
function stepTurn(steps: number, prose: number): Turn {
  const blocks: unknown[] = [];
  for (let i = 0; i < steps; i++) {
    blocks.push({ type: "text", text: `step ${String(i)}`, agent_subtask_id: STEP });
  }
  for (let i = 0; i < prose; i++) {
    blocks.push({ type: "text", text: `prose ${String(i)}` });
  }
  return turn([assistant("a", blocks)]);
}

/** The ordinals `planResidency` gives `t`'s body, grown from the live edge. */
function windowOf(t: Turn): TurnRange {
  return planResidency([t], undefined).get(t.id) ?? { from: -1, to: -1 };
}

describe("the residency budget over a workflow step's ordinals", () => {
  it("keeps every RENDERABLE block resident on a turn whose ordinals are mostly steps", () => {
    // The whole turn fits, because only the prose is charged. Before this, the window
    // stopped `RESIDENT_BLOCKS` ordinals from the live edge and the prose sat behind a
    // head spacer the reader could not reach without scrolling through nothing.
    const t = stepTurn(PAST_BUDGET, 20);
    expect(windowOf(t)).toEqual({ from: 0, to: PAST_BUDGET + 20 });
  });

  it("still bounds a turn of RENDERABLE blocks at the budget", () => {
    // The control. Without it the case above passes for a planner that charges
    // nothing at all, which would put the budget back where it was before residency.
    const t = turn([
      assistant(
        "a",
        Array.from({ length: PAST_BUDGET }, (_, i) => ({ type: "text", text: `p ${String(i)}` })),
      ),
    ]);
    const got = windowOf(t);
    expect(got.to - got.from).toBe(RESIDENT_BLOCKS);
  });

  it("charges the TOOL budget nothing for a step's tool block", () => {
    // The tool budget is the narrower of the two, so a step's `tool_use` blocks are what
    // latch a window first. The turn's own PROSE sits on the far side of the step run
    // from the live edge, which is what makes the charge observable: an accumulated tool
    // count is not tested while the window is inside the free run, and it latches the
    // moment the window reaches a block the reader can see.
    const steps = RESIDENT_TOOL_CALLS + 40;
    const blocks: unknown[] = [{ type: "text", text: "what the turn set out to do" }];
    const calls: unknown[] = [];
    for (let i = 0; i < steps; i++) {
      blocks.push({ type: "tool_use", tool_call_id: `tc${String(i)}`, agent_subtask_id: STEP });
      calls.push({ id: `tc${String(i)}`, title: "Read file", kind: "read", status: "completed" });
    }
    blocks.push({ type: "text", text: "the turn's own answer" });
    const t = turn([assistant("a", blocks, calls)]);
    expect(windowOf(t)).toEqual({ from: 0, to: steps + 2 });
  });

  it("charges nothing for a DELEGATE's blocks either, so the reader reaches real content", () => {
    // The mirror of the step case, and the reason the budget half exists: measured over
    // the 104 chats on one live volume, 21,326 blocks are a delegate's dropped output —
    // 25.5% of all 83,749 — and one chat holds 8,569 of them against 2,626 the transcript
    // renders. Charged, they crowd out the content the window is grown to reach.
    const blocks: unknown[] = [];
    for (let i = 0; i < PAST_BUDGET; i++) {
      blocks.push({ type: "text", text: `delegate ${String(i)}`, agent_subtask_id: DELEGATE });
    }
    for (let i = 0; i < 20; i++) {
      blocks.push({ type: "text", text: `prose ${String(i)}` });
    }
    const t = turn([assistant("a", blocks)]);
    expect(windowOf(t)).toEqual({ from: 0, to: PAST_BUDGET + 20 });
  });

  it("still CHARGES the delegate's invocation, so a wall of cards is bounded", () => {
    // The budget-side control matching the pricing one: a card is a real box, so a turn
    // that is nothing but invocations must still latch at the budget. Without this the
    // free arm could widen to every subtask-bearing block and mount cards without bound.
    const blocks: unknown[] = [];
    const calls: unknown[] = [];
    for (let i = 0; i < PAST_BUDGET; i++) {
      blocks.push({
        type: "tool_use",
        tool_call_id: `tc${String(i)}`,
        agent_subtask_id: `sub-${String(i)}`,
      });
      calls.push({ id: `tc${String(i)}`, title: INVOKE, kind: "other", status: "completed" });
    }
    const got = windowOf(turn([assistant("a", blocks, calls)]));
    expect(got.to - got.from).toBe(RESIDENT_TOOL_CALLS);
  });

  it("keeps a step block's ORDINAL, so a mounted range is still a range of block indices", () => {
    // The reason this is a budget change and not a span change: `sliceTurn` hands the
    // renderer a range of indices into `m.blocks`, so an ordinal space that skipped a
    // dropped block would address the wrong blocks in every message that holds one.
    const t = turn([
      assistant("a", [
        { type: "text", text: "first" },
        { type: "text", text: "step", agent_subtask_id: STEP },
        { type: "text", text: "third" },
      ]),
    ]);
    expect(turnCost(t).blocks).toBe(3);
    expect(sliceTurn(t, { from: 0, to: 3 }).get("a")).toEqual({ from: 0, to: 3 });
  });
});
