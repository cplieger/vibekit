// ---------------------------------------------------------------------------
// Residency: which BLOCKS a paint may mount, as one contiguous window of turn-block ordinals
// grown around the reader's own position. Pure and DOM-free; WHICH turns it is grown over is
// the caller's policy.
// ---------------------------------------------------------------------------

import { parseStepSubtask } from "./step-subtask.js";
import { isInternalToolTitle, isSubagentInvocation } from "./tool-schema.js";
import type { Block, Message, ToolCall } from "./types.js";
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

/** Whether the TRANSCRIPT renders nothing for this block, so it owes neither a budget
 *  slot here nor a pixel in `block-heights.ts`. Two populations, both dropped by
 *  `messages-blocks.ts` `placeBlock`: a WORKFLOW STEP's block, and every block a
 *  DELEGATE produced except the invocation that becomes its card. The tab that owns the
 *  content renders it, out of the store rather than through this module.
 *
 *  Exported because the BUDGET and the PRICE must answer it identically: a spacer
 *  pricing ordinals the window charges nothing for is the same defect as the reverse.
 *  Keyed on the step PARSE, never the `wf:` prefix — a malformed id parses to null and
 *  falls to the delegate arm, which drops it too, because the card is the only thing
 *  `placeBlock` builds for one.
 *
 *  A `tool_use` whose call is not in the store yet counts as dropped, matching
 *  `placeBlock`, which cannot recognise an invocation it cannot resolve. */
export function isDroppedBlock(block: Block, toolCalls: readonly ToolCall[]): boolean {
  const subtask = block.agent_subtask_id ?? "";
  if (subtask === "") {
    return false;
  }
  if (parseStepSubtask(subtask) !== null || block.type !== "tool_use") {
    return true;
  }
  const tc = toolCalls.find((c) => c.id === block.tool_call_id);
  return tc === undefined || !isSubagentInvocation(tc);
}

/** Whether the transcript renders ANYTHING for `m` — the message-scoped reading of
 *  `isDroppedBlock`, which is why it lives beside it rather than being spelled again
 *  wherever it is asked. A non-assistant row always renders (an event badge, a steer
 *  note, the system fallback); an assistant one renders its plan card, or any block the
 *  dispatcher does not drop. Measured on 107 chat files: 11 body messages render nothing
 *  by this rule, each an assistant message whose every block is a workflow step, one of
 *  them 603 blocks long. */
export function messageRendersContent(m: Message): boolean {
  if (m.role !== "assistant") {
    return true;
  }
  if ((m.plan ?? []).length > 0) {
    return true;
  }
  const calls = m.tool_calls ?? [];
  return (m.blocks ?? []).some((b) => !isDroppedBlock(b, calls));
}

/** The body messages a LATER message of their own turn renders content after, which is
 *  what extends the newest-element rule from message scope to TURN scope: everything in
 *  such a message is superseded, because the whole of it precedes the whole of its
 *  successor in the card.
 *
 *  Derived here rather than at the render, because only the projection knows a message's
 *  neighbours — a `MsgRender` is per message and cannot see past its own blocks, which
 *  is exactly the gap this closes. Measured on the same 107 chats: 44% of turns hold
 *  more than one body message, so a message-scoped verdict is the common shape rather
 *  than an edge. */
export function supersededMessages(turns: readonly Turn[]): ReadonlySet<string> {
  const out = new Set<string>();
  for (const t of turns) {
    let laterContent = false;
    for (let i = t.body.length - 1; i >= 0; i -= 1) {
      const m = t.body[i];
      if (m === undefined) {
        continue;
      }
      if (laterContent) {
        out.add(m.id);
      }
      laterContent = laterContent || messageRendersContent(m);
    }
  }
  return out;
}

/** run id → the id of the tool call that HOSTS that run's card: the FIRST call in turn
 *  order naming each run, every later one rendering as an ordinary tool card.
 *
 *  Derived here for `supersededMessages`' reason — a `MsgRender` is per message and
 *  cannot see its neighbours, so it cannot tell the launch from a later mention — and
 *  as a per-pass MAP rather than a live registry, because the dispatcher's two gates
 *  run in different passes over the same message: a registry written by the paint
 *  answers "no host yet, I host" to both calls in ONE message, which is the double
 *  bind this exists to prevent.
 *
 *  It mirrors `placeBlock`'s own conditions rather than scanning `tool_calls`, so an
 *  owner is always a call that branch would build a card for: a `tool_use` BLOCK, in
 *  the parent lane (a step's or a delegate's is dropped), whose call is not internal
 *  bookkeeping. Block order, because that is the order the transcript renders in.
 *
 *  Scope is the RESIDENT window, so a run whose launch is paged out has no card at
 *  all — the ratified reading, with `run-bar.ts` carrying a live run and `/history` a
 *  finished one. */
export function runCardOwners(turns: readonly Turn[]): ReadonlyMap<string, string> {
  const out = new Map<string, string>();
  for (const t of turns) {
    for (const m of t.body) {
      // Only the calls that NAME a run, which is 0 or 1 of them on almost every
      // message, so the block walk below costs one lookup per block.
      const runByCall = new Map<string, string>();
      for (const c of m.tool_calls ?? []) {
        const runID = c.workflow_id ?? "";
        if (runID !== "" && !isInternalToolTitle(c.title)) {
          runByCall.set(c.id, runID);
        }
      }
      if (runByCall.size === 0) {
        continue;
      }
      for (const b of m.blocks ?? []) {
        if (b.type !== "tool_use" || (b.agent_subtask_id ?? "") !== "") {
          continue;
        }
        const callID = b.tool_call_id ?? "";
        const runID = runByCall.get(callID);
        if (runID !== undefined && !out.has(runID)) {
          out.set(runID, callID);
        }
      }
    }
  }
  return out;
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
  // Both charges are properties of the ORDINAL, so neither can come from
  // `turnCost`: one flat pass mints the sequence and the flags together.
  const isTool: boolean[] = [];
  const isFree: boolean[] = [];
  for (const t of turns) {
    const base = isTool.length;
    for (const m of t.body) {
      const blocks = m.blocks ?? [];
      const calls = m.tool_calls ?? [];
      const span = messageSpan(m);
      for (let j = 0; j < span; j++) {
        const b = blocks[j];
        const free = b !== undefined && isDroppedBlock(b, calls);
        isFree.push(free);
        isTool.push(!free && b?.type === "tool_use");
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
  let blocks = isFree[at] === true ? 0 : 1;
  let toolCalls = isTool[at] === true ? 1 : 0;
  let headLatched = lo === 0;
  let tailLatched = hi === total;
  let head = true;
  while (!headLatched || !tailLatched) {
    if (head ? !headLatched : !tailLatched) {
      const next = head ? lo - 1 : hi;
      const tool = isTool[next] === true ? 1 : 0;
      // A FREE ordinal is taken unconditionally and latches nothing: the transcript
      // renders no block for it, so a budget spent on one buys the reader nothing. It
      // stays an ORDINAL — the span is the renderer's own coordinate system.
      if (
        isFree[next] !== true &&
        (blocks + 1 > budget.blocks || toolCalls + tool > budget.toolCalls)
      ) {
        if (head) {
          headLatched = true;
        } else {
          tailLatched = true;
        }
      } else {
        if (isFree[next] !== true) {
          blocks++;
        }
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
