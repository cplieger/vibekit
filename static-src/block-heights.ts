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

/** The flex `gap` (`--sp-3`) that `.turn-body` puts between rows and `.msg-wrap`
 *  puts between one row's blocks — one value for both levels (css/13-messages.css).
 *  The PARENT adds it and no child's own height includes it, so a run of K replaced
 *  children carries K−1 of them, its own box replacing the one that preceded the
 *  run. */
const ROW_GAP_PX = 12;

/** message id → block index → the height that block's element measured. */
const blockHeights = new Map<string, Map<number, number>>();

/** message id → the range a whole-row measurement covered and what it measured.
 *  Range-keyed because a row dropped under a PARTIAL window measured that slice
 *  only, and answering the whole row with it prices the rest at zero. */
const rowHeights = new Map<string, { range: BlockRange; px: number }>();

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

/** What `range` of `m`'s own blocks is worth, the gaps BETWEEN those blocks
 *  included — so a caller adds only the gaps between whole rows. The row number is
 *  preferred where a measurement covered exactly this range: a reconcile drops a row
 *  and measures it once, while a boundary row's drop measures each block it removes.
 */
function rangeHeight(m: Message, range: BlockRange): number {
  const row = rowHeights.get(m.id);
  if (row?.range.from === range.from && row.range.to === range.to) {
    return row.px;
  }
  const blocks = m.blocks ?? [];
  if (blocks.length === 0) {
    return BLOCK_ESTIMATE_PX.row;
  }
  const per = blockHeights.get(m.id);
  let px = 0;
  let boxes = 0;
  for (let i = range.from; i < range.to; i++) {
    const h = per?.get(i) ?? estimateOf(m, i);
    px += h;
    if (h > 0) {
      boxes++;
    }
  }
  return px + gapsBetween(boxes);
}

/** The gaps a run of `n` boxes carries. Zero-height ones are excluded by the caller:
 *  a blank row is `display: none` (css/13-messages.css), and `gap` counts an item
 *  rather than a height. */
function gapsBetween(n: number): number {
  return Math.max(0, n - 1) * ROW_GAP_PX;
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

/** Record what a whole row measured, at the moment it is dropped. `range` is what
 *  the row HELD: its height answers for those ordinals and no others. */
export function recordRowHeight(messageID: string, range: BlockRange, px: number): void {
  rowHeights.set(messageID, { range, px });
}

/** The pixel height of the ordinals one spacer stands in for: everything on
 *  `side` of `range` — the turn's MOUNTED range — plus the ROW gaps the rows it
 *  replaces contributed. Measured where measured, the per-outcome estimate where not.
 *
 *  Every covered message counts as one row-level unit, a partial slice included: that
 *  row stays in place, so K units replace K−1 gaps and the spacer's box replaces the
 *  K-th. Takes a TURN range and slices it here, so no caller converts between the
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
  let rows = 0;
  for (const m of t.body) {
    const covered = slices.get(m.id);
    if (covered === undefined) {
      continue;
    }
    const own = rangeHeight(m, covered);
    px += own;
    if (own > 0) {
      rows++;
    }
  }
  return px + gapsBetween(rows);
}

/** Drop a chat's cache (view dispose, chat delete). */
export function forgetHeights(messageIDs: Iterable<string>): void {
  for (const id of messageIDs) {
    blockHeights.delete(id);
    rowHeights.delete(id);
  }
}
