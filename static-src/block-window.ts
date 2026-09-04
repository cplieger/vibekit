// ---------------------------------------------------------------------------
// Residency: which turns keep a body, measured in what a paint actually costs.
//
// THE UNIT IS THE WHOLE POINT. This used to be `TURNS_WARM = 5` in
// `fold-state.ts`, and a turn COUNT is not a cost: 28 of 29 live chats hold five
// turns or fewer, so the tier excluded nothing and one paint mounted up to 731
// tool cards across 1,279 blocks. A single turn measured 580 blocks and 353 tool
// cards on its own, which is the shape any honest budget has to be able to stub.
//
// TWO BUDGETS, because the two costs are not proportional. A block is one
// reconcile unit — a bubble, a thinking block, a tool card's row — and a tool
// card is a whole disclosure with a header, a body and its own effects, so a
// turn that is mostly tool cards has to be bounded tighter than a turn of prose.
// Whichever budget runs out first ends residency.
//
// CONTIGUOUS FROM THE NEWEST. Once a turn is over budget every older turn is a
// stub too, even a small one that would still fit. A resident turn between two
// stubs would move the scroller's height around a hole nothing on screen
// explains, and "the newest N" is the only rule a reader can predict.
//
// PINNED turns are exempt and still CHARGE the budget, because their cost is
// real and it is what the turns behind them have left to spend. Which turns
// those are is the caller's policy, not this module's (messages.ts pins the
// running turn and any turn the reader revealed).
//
// Pure and DOM-free: the renderer asks which turns to mount, and this answers
// from the projection alone. Disclosure — open or closed — stays `fold-state.ts`'s
// question; a turn can be resident and folded, and a stub always renders folded
// because it has no body to show.
// ---------------------------------------------------------------------------

import type { Turn } from "./turns.js";

/** What one paint may mount. Blocks first because it bounds every turn; the tool
 *  budget is what bounds the tool-card-heavy ones the block budget alone lets
 *  through.
 *
 *  320 is ten of `messages.ts`'s `BUILD_BATCH_BLOCKS` slices — the yielded
 *  builder's unit, so the ceiling is stated in the same terms as the work. 96
 *  collapsed tool cards is about three viewports of claim lines, and it is what
 *  stubs the measured 353-card turn while leaving the 44-card one whole. */
export const RESIDENT_BLOCKS = 320;
export const RESIDENT_TOOL_CALLS = 96;

/** A paint budget, and the shape `turnCost` reports one turn's price in. */
export interface TurnCost {
  readonly blocks: number;
  readonly toolCalls: number;
}

const DEFAULT_BUDGET: TurnCost = {
  blocks: RESIDENT_BLOCKS,
  toolCalls: RESIDENT_TOOL_CALLS,
};

/** What mounting `t`'s body costs.
 *
 *  The trigger is not counted: it renders into the header, which every turn has
 *  whether or not it is resident. A message with no blocks still counts as one,
 *  matching the builder's own accounting — the reconcile unit is the message row,
 *  so an empty one is still a row. */
export function turnCost(t: Turn): TurnCost {
  let blocks = 0;
  let toolCalls = 0;
  for (const m of t.body) {
    blocks += Math.max(1, (m.blocks ?? []).length);
    toolCalls += (m.tool_calls ?? []).length;
  }
  return { blocks, toolCalls };
}

/** The turn ids whose bodies this paint may mount.
 *
 *  `turns` is the projected window, newest LAST (`projectTurns`' order).
 *  `pinned` names the turns residency may not exclude. Everything absent from
 *  the returned set is a header/footer stub, and `messages.ts mountTurnBody`
 *  builds one on demand when the reader opens it. */
export function planResidency(
  turns: readonly Turn[],
  pinned: (t: Turn) => boolean,
  budget: TurnCost = DEFAULT_BUDGET,
): Set<string> {
  const resident = new Set<string>();
  let blocks = 0;
  let toolCalls = 0;
  // Latched rather than re-tested: what makes the resident set contiguous.
  let spent = false;
  for (let i = turns.length - 1; i >= 0; i--) {
    const t = turns[i];
    if (t === undefined) {
      continue;
    }
    const cost = turnCost(t);
    if (pinned(t)) {
      resident.add(t.id);
      blocks += cost.blocks;
      toolCalls += cost.toolCalls;
      continue;
    }
    if (spent) {
      continue;
    }
    if (blocks + cost.blocks > budget.blocks || toolCalls + cost.toolCalls > budget.toolCalls) {
      spent = true;
      continue;
    }
    resident.add(t.id);
    blocks += cost.blocks;
    toolCalls += cost.toolCalls;
  }
  return resident;
}
