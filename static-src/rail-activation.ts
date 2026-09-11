// Which turn the reader is in, from the scroll offset alone. The transcript has one
// READING LINE and a jump lands the target's top ON it, so a click cannot mark turn N
// and then have activation re-mark N+1. Computed from a cached table rather than from
// an IntersectionObserver, which reports membership CHANGES and drops entries under
// fast scroll. Pure and DOM-free: the CALLER measures, this module owns the answer.

/** The resident cards' tops in the SCROLLER's frame, ascending. Two parallel arrays,
 *  because the read is a binary search over `tops` on every scroll frame. */
export interface TurnOffsets {
  readonly ids: readonly string[];
  readonly tops: readonly number[];
}

/** One measured card. `null` is a real answer for a card the engine reports no box
 *  for. Measure with rects, NEVER `offsetTop`: `content-visibility: auto` on
 *  `.msg-row` makes the row a containing block, so an offsetParent-relative read
 *  returned 0 for a block whose true position was 2203. */
export interface CardTop {
  readonly id: string;
  readonly top: number | null;
}

/** The scroller facts no table can carry. `atLiveEdge` is the PUBLISHED verdict, not
 *  a fresh bottom test: a wheel-up inside the tolerance band parks the reader while a
 *  raw test still answers true, so only the published one is about the READER. */
export interface RailGeom {
  clientHeight: number;
  atLiveEdge: boolean;
}

/** No answer. Not a turn id, so a caller reads it as "keep the mark you have". */
const KEEP = "";

/** Build the table from measured cards, ascending. A card with no box is SKIPPED: its
 *  marker still renders and is still clickable, it just cannot be landed on. */
export function buildOffsets(cards: Iterable<CardTop>): TurnOffsets {
  const rows: CardTop[] = [];
  for (const card of cards) {
    if (card.id === "" || card.top === null || !Number.isFinite(card.top)) {
      continue;
    }
    rows.push(card);
  }
  rows.sort((a, b) => (a.top ?? 0) - (b.top ?? 0));
  return { ids: rows.map((r) => r.id), tops: rows.map((r) => r.top ?? 0) };
}

/** A turn's own top, or `null` for a turn the table does not carry. A jump's landing
 *  is this minus the reading line, which is what puts the turn's top ON that line.
 *  Takes the table, because the cache and its invalidation are the scroller's. */
export function turnTop(offsets: TurnOffsets, id: string): number | null {
  const i = offsets.ids.indexOf(id);
  return i < 0 ? null : (offsets.tops[i] ?? null);
}

/** The turn the reading line is in, or `KEEP` when there is no answer. Pure and
 *  total: it holds no state, so it cannot drift and cannot skip.
 *
 *  BOTH ENDS CLAMP AND THE TOP CLAMP WINS. `scrollTop <= 0` is a position the
 *  scroller MEASURED, while the edge verdict is a published field that can be stale:
 *  it is initialised true and corrected only by a scroll event or the edge sentinel,
 *  so a render before either on a view at offset 0 reads both as true. Nothing may
 *  rely on the two being mutually exclusive. */
export function activeTurnAt(
  scrollTop: number,
  offsets: TurnOffsets,
  line: number,
  geom: RailGeom,
): string {
  const n = offsets.ids.length;
  if (n === 0 || geom.clientHeight === 0) {
    return KEEP;
  }
  if (!(scrollTop > 0)) {
    return offsets.ids[0] ?? KEEP;
  }
  if (geom.atLiveEdge) {
    return offsets.ids[n - 1] ?? KEEP;
  }
  const target = scrollTop + line;
  let lo = 0;
  let hi = n - 1;
  let best = 0;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if ((offsets.tops[mid] ?? 0) <= target) {
      best = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return offsets.ids[best] ?? KEEP;
}
