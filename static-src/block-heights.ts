// ---------------------------------------------------------------------------
// What a range of ordinals is worth in PIXELS: the height unmounted space holds,
// so the document's height cannot depend on the window. Measured at the DROP,
// which suffices: everything ABOVE the reader has been mounted and dropped once.
// ---------------------------------------------------------------------------

import { sliceTurn, turnCost, type BlockRange, type TurnRange } from "./block-window.js";
import type { Message } from "./types.js";
import type { Turn } from "./turns.js";

/** What one block is worth before it has ever been measured, keyed on what it
 *  MOUNTS AS rather than on `block.type`, because one type mounts three shapes.
 *  Each value is the element's declared `contain-intrinsic-size` where CSS has
 *  one; a sealed thinking trace has none and is a collapsed card's claim line. */
const BLOCK_ESTIMATE_PX = {
  text: 48,
  emptyText: 0,
  thinking: 40,
  runCard: 64,
  toolCard: 40,
  row: 48,
} as const;

/** `.turn-body`'s flex `gap` (`--sp-3`), which the PARENT adds and no row's own
 *  height includes. A spacer standing in for K whole rows replaces K−1 of the gaps
 *  between them, its own box replacing the one that preceded the run. */
const ROW_GAP_PX = 12;

/** message id → block index → the height that block's element measured. */
const blockHeights = new Map<string, Map<number, number>>();

/** message id → the height the whole row measured. */
const rowHeights = new Map<string, number>();

function estimateOf(m: Message, i: number): number {
  const block = (m.blocks ?? [])[i];
  if (block === undefined) {
    return BLOCK_ESTIMATE_PX.row;
  }
  switch (block.type) {
    case "text":
      // A `padBlocks` pad mounts an `is-empty` row, which is zero-height.
      return (block.text ?? "") === "" ? BLOCK_ESTIMATE_PX.emptyText : BLOCK_ESTIMATE_PX.text;
    case "thinking":
      return BLOCK_ESTIMATE_PX.thinking;
    case "tool_use": {
      const tc = m.tool_calls?.find((c) => c.id === block.tool_call_id);
      return (tc?.workflow_id ?? "") === ""
        ? BLOCK_ESTIMATE_PX.toolCard
        : BLOCK_ESTIMATE_PX.runCard;
    }
    default:
      return BLOCK_ESTIMATE_PX.row;
  }
}

/** What `range` of `m`'s own blocks is worth. The whole-ROW number is preferred
 *  where the range covers the row: a reconcile drops a row and measures it once,
 *  while a boundary row's drop measures each block it removes. */
function rangeHeight(m: Message, range: BlockRange): number {
  const blocks = m.blocks ?? [];
  if (blocks.length === 0 || (range.from === 0 && range.to === blocks.length)) {
    const row = rowHeights.get(m.id);
    if (row !== undefined) {
      return row;
    }
  }
  if (blocks.length === 0) {
    return BLOCK_ESTIMATE_PX.row;
  }
  const per = blockHeights.get(m.id);
  let px = 0;
  for (let i = range.from; i < range.to; i++) {
    px += per?.get(i) ?? estimateOf(m, i);
  }
  return px;
}

/** Record what one block's element measured, at the moment it is dropped. */
export function recordBlockHeight(messageID: string, blockIndex: number, px: number): void {
  let per = blockHeights.get(messageID);
  if (per === undefined) {
    per = new Map<number, number>();
    blockHeights.set(messageID, per);
  }
  per.set(blockIndex, px);
}

/** Record what a whole row measured, at the moment it is dropped. */
export function recordRowHeight(messageID: string, px: number): void {
  rowHeights.set(messageID, px);
}

/** The pixel height of the ordinals one spacer stands in for: everything on
 *  `side` of `range` — the turn's MOUNTED range — plus the parent gaps the replaced
 *  rows contributed. Measured where measured, the per-outcome estimate where not: a
 *  HEAD spacer covers ordinals mounted and dropped, a tail estimate is corrected on
 *  arrival.
 *
 *  Takes a TURN range and slices it here, so no caller converts between the
 *  ordinal space and the renderer's message-local one. */
export function spacerHeight(t: Turn, range: TurnRange, side: "head" | "tail"): number {
  const span = turnCost(t).blocks;
  const stood: TurnRange =
    side === "head"
      ? { from: 0, to: Math.min(Math.max(range.from, 0), span) }
      : { from: Math.min(Math.max(range.to, 0), span), to: span };
  if (stood.from >= stood.to) {
    return 0;
  }
  const slices = sliceTurn(t, stood);
  let px = 0;
  let whole = 0;
  for (const m of t.body) {
    const covered = slices.get(m.id);
    if (covered !== undefined) {
      px += rangeHeight(m, covered);
      if (covered.from === 0 && covered.to === (m.blocks ?? []).length) {
        whole++;
      }
    }
  }
  return px + Math.max(0, whole - 1) * ROW_GAP_PX;
}

/** Drop a chat's cache (view dispose, chat delete). */
export function forgetHeights(messageIDs: Iterable<string>): void {
  for (const id of messageIDs) {
    blockHeights.delete(id);
    rowHeights.delete(id);
  }
}
