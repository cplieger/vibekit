// ---------------------------------------------------------------------------
// What a range of ordinals is worth in PIXELS: the height unmounted space holds, so the
// document's height cannot depend on the window. Measured at the DROP, which suffices:
// everything ABOVE the reader has been mounted and dropped once.
// ---------------------------------------------------------------------------

import {
  isDroppedBlock,
  sliceTurn,
  turnCost,
  type BlockRange,
  type TurnRange,
} from "./block-window.js";
import { isSubagentInvocation } from "./tool-schema.js";
// The stage-to-driver join, off the stage's own tool-call id. Imported rather than
// re-derived: that id format already has two owners (`messages-blocks.ts`
// `stagePipelineID` and this one), and `subagent-slice.ts` is a leaf whose own comment
// records the duplication as deliberate — so reading it here adds no third owner and no
// DOM to this module's graph.
import { pipelineOf } from "./subagent-slice.js";
import type { Message, ToolCall } from "./types.js";
import type { Turn } from "./turns.js";
// Type-only, so this costs no import edge at runtime: `tierNow` below reads the
// attribute rather than calling `pointer-tier.ts`, and this is only the vocabulary.
import type { PointerTier } from "./device-view.js";

/** What one block is worth before it has ever been measured, keyed on what it
 *  MOUNTS AS rather than on `block.type`, because one type mounts four shapes — a
 *  `tool_use` block resolves to a run card, a delegate card, a pipeline's container or
 *  a tool row, and to NOTHING at all when its own tool-call id names a pipeline DRIVER.
 *  That zero is every stage, not only the ones inside a container: a PROMOTED single
 *  stage renders its card at the TOP LEVEL and is priced 0 there too, with the driver's
 *  block carrying the price.
 *
 *  EACH VALUE IS THE RENDERED BOX the element resolves to while its contents are
 *  skipped, NOT its declared `contain-intrinsic-size`. That claim was the unit error
 *  this table carried: the property states the CONTENT box, and the box model then
 *  adds padding, border and `min-height` on top — so a value written as the declaration
 *  under-states every element that has any of the three. `text`/`row` never exposed it
 *  because `.msg-row` has no padding, border or `min-height`, so its content box IS its
 *  border box. The unit has to be the border box for a second reason: what an estimate
 *  substitutes for is a MEASUREMENT (`recordBlockHeight` / `recordRowHeight` are fed
 *  `offsetHeight`), and `rangeHeight` sums the two interchangeably.
 *
 *  TIER-KEYED because four of the seven entries move on the pointer tier, by 8, 8, 19
 *  and 28px per block, so a single-tier table is measurably wrong on the other tier.
 *  Measured against the assembled stylesheet in Chromium 151, and held to it by
 *  `block-heights-css.test.ts`, which is what makes these literals a SHADOW of the CSS
 *  rather than a second source of truth for it. The reserves themselves cannot be read
 *  here: `getPropertyValue` on an unregistered custom property answers the substituted
 *  token stream (`--btn-h` is `2.25rem`, `--run-card-content` the whole `calc()`), and
 *  resolving either to px needs a probe element plus layout, which a pure pricing
 *  module cannot take.
 *
 *  Which rule each entry shadows:
 *    text        13-messages.css `.msg-row`            — `auto 3rem`, no padding/border
 *    emptyText   13-messages.css `.msg-row.is-empty`   — `display: none`
 *    thinking    NONE — see below
 *    toolCard    14-tools.css `.tool-call`             — `auto var(--btn-h)` + 2px
 *    runCard     27-run-card.css `.run-card`           — `auto var(--run-card-content)` + 2px
 *    subagentCard 14-tools.css `.subagent-block`       — `auto var(--subagent-content)` + 2px,
 *                                                        one rule serving a delegate's
 *                                                        CARD and a pipeline's CONTAINER
 *    row         13-messages.css `.msg-row`            — a blockless message is one row
 *
 *  `thinking` IS THE ONE ENTRY WITH NO CSS COUNTERPART: `.reasoning-block` declares no
 *  `content-visibility`, so there is no reserve to shadow. The value is the REAL
 *  collapsed height of a sealed trace — its `<summary>` row — measured at 25px on the
 *  fine tier and 44px on the coarse one, tier-dependent because 61-mcp-tools.css's
 *  universal hit floor includes `summary`. The 40 it replaced traced to nothing and was
 *  wrong on both tiers, in opposite directions. The FINE value is that row's own line
 *  box rather than the floor, so it moves with the font stack (24px on a CI runner) —
 *  hence the one pixel of slack its guard allows. */
export const BLOCK_ESTIMATE_PX: Readonly<Record<PointerTier, BlockEstimates>> = {
  fine: {
    text: 48,
    emptyText: 0,
    thinking: 25,
    runCard: 79,
    toolCard: 38,
    subagentCard: 71,
    row: 48,
  },
  coarse: {
    text: 48,
    emptyText: 0,
    thinking: 44,
    runCard: 87,
    toolCard: 46,
    subagentCard: 99,
    row: 48,
  },
} as const;

/** One tier's prices. */
export interface BlockEstimates {
  readonly text: number;
  readonly emptyText: number;
  readonly thinking: number;
  readonly runCard: number;
  readonly toolCard: number;
  readonly subagentCard: number;
  readonly row: number;
}

/** The flex `gap` (`--sp-3`) that `.turn-body` puts between rows and `.msg-wrap`
 *  puts between one row's blocks — one value for both levels (css/13-messages.css
 *  `.msg-wrap`, css/29-turns.css `.turn-body`), which is itself an assertion, and
 *  `block-heights-css.test.ts` is what holds it. Tier-invariant: `--sp-3` reads no
 *  pointer query.
 *
 *  The PARENT adds it and no child's own height includes it, so a run of K replaced
 *  children carries K−1 of them, its own box replacing the one that preceded the
 *  run. */
export const ROW_GAP_PX = 12;

/** Which tier the document is laid out for, READ off the attribute rather than
 *  imported from `pointer-tier.ts`: `currentTier()` is this same one-property read
 *  behind a module whose import chain reaches `device-view.ts` and `localStorage`,
 *  and this module has to keep answering with no DOM at all — both of its suites are
 *  in the node project, where the absent-`document` arm resolves to `fine`.
 *
 *  The absent-attribute arm MIRRORS THE CASCADE rather than guessing: `01-tokens.css`
 *  carries a no-JS fallback (`:root:not([data-pointer="fine"])` under
 *  `@media (width <= 48rem)`), so an unset attribute takes the coarse values exactly
 *  when that query matches. Both reads are one property access and no layout. */
function tierNow(): PointerTier {
  // A NULLABLE view of the global, not the DOM lib's: read through that type,
  // `no-unnecessary-condition` proves these guards dead and offers to cut them.
  const g = globalThis as {
    readonly document?: { readonly documentElement?: Element };
    readonly matchMedia?: (q: string) => { readonly matches: boolean };
  };
  const attr = g.document?.documentElement?.getAttribute("data-pointer");
  if (attr === "fine" || attr === "coarse") {
    return attr;
  }
  return (g.matchMedia?.("(width <= 48rem)").matches ?? false) ? "coarse" : "fine";
}

/** message id → block index → the height that block's element measured. */
const blockHeights = new Map<string, Map<number, number>>();

/** message id → the range a whole-row measurement covered and what it measured.
 *  Range-keyed because a row dropped under a PARTIAL window measured that slice
 *  only, and answering the whole row with it prices the rest at zero. */
const rowHeights = new Map<string, { range: BlockRange; px: number }>();

/** The tool call that OPENS a subagent-orchestration pipeline, whose block mounts the
 *  pipeline's BOX rather than a tool row. Deliberately absent from
 *  `isSubagentInvocation`, because one title with two owners makes a classification
 *  unpredictable.
 *
 *  LOCAL rather than imported, and the reason is mechanical rather than stylistic:
 *  `messages-blocks.ts` owns `isPipelineInvocation` and already imports
 *  `recordBlockHeight` from here, so reading it back closes a cycle — and MEASURED, the
 *  import fails outright rather than merely offending a rule, because that module's
 *  graph reaches `router.ts`, which registers a `window` listener at module load, while
 *  both of this module's suites run in the node project (`ReferenceError: window is not
 *  defined` at router.ts:416). `subagent-slice.ts` carries the same literal as its own
 *  unexported `PIPELINE_TITLE` for its own version of that reason, so this is the third
 *  copy; the shape that removes all three is the predicate living in `tool-schema.ts`
 *  beside `isSubagentInvocation`, whose comment already names this title as another
 *  owner's. NOTHING cross-checks the three in the meantime, this file's own suites
 *  included: `block-heights-css.test.ts` builds its subject with
 *  `buildSubagentContainer` directly and pins the 71 / 99 VALUE against the CSS,
 *  evaluating no title at all, and `block-heights.node.test.ts`'s fixtures hard-code the
 *  same string as the module under test. So changing the literal in
 *  `messages-blocks.ts` alone stops the renderer building a box while this arm keeps
 *  charging a card for it, with every suite green. */
function isPipelineDriver(tc: ToolCall): boolean {
  return tc.title === "Orchestrate Sub-agent";
}

function estimateOf(m: Message, i: number, est: BlockEstimates): number {
  const block = (m.blocks ?? [])[i];
  if (block === undefined) {
    return est.row;
  }
  // A block the transcript mounts nowhere is never measured, so a price here is
  // reserved forever. Measured over the 104 chats on one live volume: 45,104 workflow
  // step blocks and 21,326 delegate blocks, together 79.3% of all 83,749, and 863,176px
  // of the phantom height was the delegate half alone.
  if (isDroppedBlock(block, m.tool_calls ?? [])) {
    return 0;
  }
  switch (block.type) {
    case "text":
      // A `padBlocks` pad mounts an `is-empty` row, which is zero-height.
      return (block.text ?? "") === "" ? est.emptyText : est.text;
    case "thinking":
      return est.thinking;
    case "tool_use": {
      const tc = m.tool_calls?.find((c) => c.id === block.tool_call_id);
      // The `workflow_id` test stays FIRST: a launch carries no subtask id, so the two
      // arms are disjoint, but ordering them makes that independent of the titles.
      if ((tc?.workflow_id ?? "") !== "") {
        return est.runCard;
      }
      if (tc === undefined) {
        return est.toolCard;
      }
      // A PIPELINE IS WORTH ONE CARD however many stages it has, and its DRIVER's block
      // is where that price sits. Measured over 120 collapsed containers per shape
      // (`block-heights-css.test.ts`, Chromium 151): a container holding 1, 3 or 8
      // settled stage cards renders at 71px on the fine tier and 99 on the coarse one —
      // the same as a bare card, to the pixel — because
      // `.subagent-block.collapsed > .subagent-body` is `content-visibility: hidden` at
      // the disclosure controller's inline height 0, so a stage inside one contributes
      // nothing to the box it sits in.
      if (isPipelineDriver(tc)) {
        return est.subagentCard;
      }
      // A DELEGATE INVOCATION mounts a `.subagent-block`, not a `.tool-call`, and
      // `isDroppedBlock` keeps exactly one of a delegate's blocks — this one — so the
      // card's height is reserved here or nowhere. Pricing a card that RENDERS as a tool
      // row is 33 / 53px short, which is `subagentCard` less `toolCard` on each tier.
      //
      // A STAGE names its driver in its own id, and the driver above holds its price:
      // inside a container the stage's card renders at 0, and where the pipeline
      // PROMOTES its single stage the DRIVER's block is the one that renders nothing
      // (`messages-blocks.ts` `driverNeedsBox` refuses a box at a count of 1). So for a
      // pipeline whose driver and stages sit in ONE MESSAGE the total is exact in both
      // shapes, and in the promoted one the two blocks' prices are swapped — which costs
      // one card, and only where a window edge falls between a driver and its promoted
      // stage.
      //
      // SPLIT ACROSS TWO MESSAGES it is UNDER by one card, because the renderer's join
      // is per message as well: `indexPipelines` reads `m.tool_calls`, so a message
      // holding stage blocks whose driver's call is elsewhere counts only the stages IT
      // sees, builds a container of its own through `stageHostFor`, and prices every one
      // of them 0 with no block left to pay for that box — plus the ROW gap that box
      // would have carried, since `rangeHeight` charges gaps only between the boxes it
      // priced. Measured at BLOCK level over the 111 chat files on one live volume: 8 of
      // 374 stage blocks sit in a message their driver's call is absent from, in two
      // fragments of four stages each, and both fragments hold other priced blocks, so
      // each is 71 + 12 fine / 99 + 12 coarse short. Charging the fragment's FIRST stage
      // is the exact rule and needs a scan of the earlier blocks per block, which is the
      // O(n²) shape that alternative was rejected on.
      //
      // KEYED ON THE ID, NOT THE TITLE, because the two disagree on real data. Measured
      // over the 111 chat files on one live volume (a different question from the
      // title-keyed census at the CSS rule, so a separate reading rather than a restated
      // one): of `isSubagentInvocation`'s 393 matches, 392 carry the `Sub-agent:` title
      // prefix while only 369 carry the `_stage_` id shape. The other 23 are
      // `invoke_subagent_<driver>-sub-agent-start`, a second KAS shape naming no stage —
      // `stagePipelineID` answers "" for it too, so the renderer seats those cards at the
      // TOP LEVEL where they cost a full card, and their driver keeps a box of its own
      // for having dispatched no stage it could see. A title-keyed test would price all
      // 23 at zero against boxes that render. The 393rd match is a plain
      // `Sub-agent execution` delegate, which names no driver either.
      if (isSubagentInvocation(tc)) {
        return pipelineOf(tc.id) === "" ? est.subagentCard : 0;
      }
      return est.toolCard;
    }
    default:
      return est.row;
  }
}

/** What `range` of `m`'s own blocks is worth, the gaps BETWEEN those blocks included — so a
 *  caller adds only the gaps between whole rows. The row number is preferred where a
 *  measurement covered exactly this range: a reconcile drops a row and measures it once, while
 *  a boundary row's drop measures each block it removes. */
function rangeHeight(m: Message, range: BlockRange, est: BlockEstimates): number {
  const row = rowHeights.get(m.id);
  if (row?.range.from === range.from && row.range.to === range.to) {
    return row.px;
  }
  const blocks = m.blocks ?? [];
  if (blocks.length === 0) {
    return est.row;
  }
  const per = blockHeights.get(m.id);
  let px = 0;
  let boxes = 0;
  for (let i = range.from; i < range.to; i++) {
    const h = per?.get(i) ?? estimateOf(m, i, est);
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
  // Resolved ONCE per call, not per block: the answer cannot change inside one
  // spacer's arithmetic, and a per-block read would put an attribute lookup in a loop
  // that runs over every ordinal the spacer stands in for.
  const est = BLOCK_ESTIMATE_PX[tierNow()];
  let px = 0;
  let rows = 0;
  for (const m of t.body) {
    const covered = slices.get(m.id);
    if (covered === undefined) {
      continue;
    }
    const own = rangeHeight(m, covered, est);
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
