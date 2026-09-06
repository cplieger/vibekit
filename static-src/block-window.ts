// ---------------------------------------------------------------------------
// Residency: which BLOCKS a paint may mount, as one contiguous window of turn-block ordinals
// grown around the reader's own position. Pure and DOM-free; WHICH turns it is grown over is
// the caller's policy.
// ---------------------------------------------------------------------------

import type { Message } from "./types.js";
import type { Turn } from "./turns.js";

/** What one paint's WINDOW may mount. TWO budgets, because a tool card is a whole
 *  disclosure where a text block is one row, so whichever runs out first ends the
 *  side that asked.
 *
 *  The tool budget charges one per `tool_use` BLOCK, which is not `turnCost`'s
 *  per-message `tool_calls.length` and can be smaller than it: a collided index
 *  leaves a tool call behind with no block of its own. The window's unit has to be
 *  the block, because the window's unit is the ordinal. */
export const RESIDENT_BLOCKS = 320;
export const RESIDENT_TOOL_CALLS = 96;

/** The depth the window guarantees each side of its anchor, asserted as a FLOOR
 *  on it rather than fed in as an input. Two more consumers, both on the DEMAND
 *  side: `demandRange`'s half-width, shared by the pin and the walk, and
 *  `demandPin`'s arrival tolerance. One name, because all three are the same
 *  distance and separate constants are what would let them drift apart. */
export const OVERSCAN_BLOCKS = 24;

/** A paint budget, and the shape `turnCost` reports one turn's price in. */
export interface TurnCost {
  readonly blocks: number;
  readonly toolCalls: number;
}

const DEFAULT_BUDGET: TurnCost = {
  blocks: RESIDENT_BLOCKS,
  toolCalls: RESIDENT_TOOL_CALLS,
};

/** A half-open range of TURN-BLOCK ordinals: the plan's unit. */
export interface TurnRange {
  readonly from: number;
  readonly to: number;
}

/** A half-open range of indices into ONE message's `blocks`: the renderer's
 *  unit, and what `renderRange` has always taken. */
export interface BlockRange {
  readonly from: number;
  readonly to: number;
}

/** Where the reader is, in turn-block ordinals. */
export interface ResidencyAnchor {
  readonly turnID: string;
  readonly at: number;
}

/** turn id → the ordinals that turn's body may hold. Only turns the window
 *  TOUCHES are present. */
export type ResidencyPlan = ReadonlyMap<string, TurnRange>;

/** How many ordinals `m` occupies. A blockless message is still one row — the
 *  reconcile unit is the message, so an empty one is a row the paint builds —
 *  and this is the one place that rule is spelled. */
function messageSpan(m: Message): number {
  return Math.max(1, (m.blocks ?? []).length);
}

/** What mounting `t`'s body costs, and the LENGTH of its ordinal span.
 *
 *  The trigger is not counted: it renders into the header, which every turn has
 *  whether or not it is resident. */
export function turnCost(t: Turn): TurnCost {
  let blocks = 0;
  let toolCalls = 0;
  for (const m of t.body) {
    blocks += messageSpan(m);
    toolCalls += (m.tool_calls ?? []).length;
  }
  return { blocks, toolCalls };
}

/** Decompose a turn range into per-message MESSAGE-LOCAL ranges, in body order.
 *
 *  A message the range does not touch is ABSENT, which is how the renderer knows
 *  not to mount its row. A blockless message it touches is present with an empty
 *  range: that row holds no block of its own. */
export function sliceTurn(t: Turn, range: TurnRange): ReadonlyMap<string, BlockRange> {
  const out = new Map<string, BlockRange>();
  let base = 0;
  for (const m of t.body) {
    const span = messageSpan(m);
    const from = Math.max(range.from, base);
    const to = Math.min(range.to, base + span);
    if (from < to) {
      const blockless = (m.blocks ?? []).length === 0;
      out.set(m.id, blockless ? { from: 0, to: 0 } : { from: from - base, to: to - base });
    }
    base += span;
  }
  return out;
}

/** The turn-block ordinal of `messageID`'s block `blockIndex`, or the message's
 *  FIRST ordinal when the index is absent. Undefined when the message is not in
 *  `t.body`; an index past that message's own span is clamped into it, so the
 *  answer always names an ordinal the message owns.
 *
 *  The inverse of `sliceTurn`, and in this module because a second decoder
 *  elsewhere would be free to disagree with the one that defines the space. */
export function turnOrdinalOf(t: Turn, messageID: string, blockIndex?: number): number | undefined {
  let base = 0;
  for (const m of t.body) {
    const span = messageSpan(m);
    if (m.id === messageID) {
      return base + Math.min(Math.max(blockIndex ?? 0, 0), span - 1);
    }
    base += span;
  }
  return undefined;
}

/** The ordinals each turn's body may hold this paint.
 *
 *  `turns` is the sequence the window is grown over, newest LAST, ALREADY
 *  FILTERED to the turns that would render open if bodied. `anchor` says where the
 *  reader is; absent, or naming a turn the sequence does not hold, is the live
 *  edge, and one naming a turn that holds NO ordinal seeds at the nearest ordinal
 *  the sequence does. A turn no ordinal reaches is absent from the answer. */
export function planResidency(
  turns: readonly Turn[],
  anchor: ResidencyAnchor | undefined,
  budget: TurnCost = DEFAULT_BUDGET,
): ResidencyPlan {
  const plan = new Map<string, TurnRange>();
  const bases: number[] = [];
  const spans: number[] = [];
  // The tool charge is a property of the ORDINAL, so it cannot come from
  // `turnCost`: one flat pass mints the sequence and the flag together.
  const isTool: boolean[] = [];
  for (const t of turns) {
    const base = isTool.length;
    for (const m of t.body) {
      const blocks = m.blocks ?? [];
      const span = messageSpan(m);
      for (let j = 0; j < span; j++) {
        isTool.push(blocks[j]?.type === "tool_use");
      }
    }
    bases.push(base);
    spans.push(isTool.length - base);
  }
  const total = isTool.length;
  if (total === 0) {
    return plan;
  }

  let at = total - 1;
  if (anchor !== undefined) {
    const i = turns.findIndex((t) => t.id === anchor.turnID);
    const base = bases[i] ?? -1;
    const span = spans[i] ?? 0;
    if (base >= 0) {
      // A zero-span turn's own base IS the next turn's first ordinal, and the
      // clamp is what answers a trailing one, whose base is past the end.
      at =
        span === 0 ? Math.min(base, total - 1) : base + Math.min(Math.max(anchor.at, 0), span - 1);
    }
  }

  // ONE seed, one ordinal per side per step: what makes an island unrepresentable.
  // The budget is SHARED, so a side latched at the sequence end reserves nothing.
  let lo = at;
  let hi = at + 1;
  let blocks = 1;
  let toolCalls = isTool[at] === true ? 1 : 0;
  let headLatched = lo === 0;
  let tailLatched = hi === total;
  let head = true;
  while (!headLatched || !tailLatched) {
    if (head ? !headLatched : !tailLatched) {
      const next = head ? lo - 1 : hi;
      const tool = isTool[next] === true ? 1 : 0;
      if (blocks + 1 > budget.blocks || toolCalls + tool > budget.toolCalls) {
        if (head) {
          headLatched = true;
        } else {
          tailLatched = true;
        }
      } else {
        blocks++;
        toolCalls += tool;
        if (head) {
          lo = next;
          headLatched = lo === 0;
        } else {
          hi = next + 1;
          tailLatched = hi === total;
        }
      }
    }
    head = !head;
  }

  for (const [i, t] of turns.entries()) {
    const base = bases[i] ?? 0;
    const from = Math.max(lo, base);
    const to = Math.min(hi, base + (spans[i] ?? 0));
    if (from < to) {
      plan.set(t.id, { from: from - base, to: to - base });
    }
  }
  return plan;
}
