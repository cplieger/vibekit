// Where a marker sits on the rail, and which turns get one. Position is a continuous
// function of the turn's own NUMBER and of how many turns the session has, rather than
// a slot in a fixed-capacity list, so a marker does not move when a page loads.
//
// TWO REGIMES, and `railSpan` is the crossover: a session that fits at the relaxed
// pitch is spread from the top at it, and one that does not takes the whole track.

import type { TurnSummary } from "./rail-merge.js";
import { severityOf } from "./turn-severity.js";

/** The clear between two adjacent hit targets. */
const MARKER_CLEAR_PX = 4;

/** The marker box for a pre-layout track: the FINE tier's own floor rather than a
 *  third number, so the next render re-reads it. */
export const MARKER_FALLBACK_PX = 24;

/** The reader-position caret's height: the app's status-mark diameter (`--dot-size`),
 *  so it reads as a tab dot's class of mark rather than as a second marker. A
 *  constant rather than a tier field because the caret is `aria-hidden` and is not a
 *  hit target, so WCAG 2.5.8 does not reach it — what follows the tier is the marker
 *  box it is centred inside, which `railMetrics` reads. */
export const HERE_PX = 8;

/** The two pixel numbers the layout needs, both off the tier's own hit floor. */
export interface RailMetrics {
  /** One marker's box: 24px on a fine pointer, 44px on a coarse one. */
  markerPx: number;
  /** The minimum separation between two markers' tops. */
  pitchPx: number;
}

/** Read the tier's marker box off the track's computed style. `--hit-floor` rather
 *  than a constant because `.rail-marker` is sized from that same token, so a
 *  hard-coded pitch would place 44px targets 28px apart on a coarse pointer. */
export function railMetrics(track: HTMLElement): RailMetrics {
  const markerPx = floorPx(track) ?? MARKER_FALLBACK_PX;
  return { markerPx, pitchPx: markerPx + MARKER_CLEAR_PX };
}

/** `--hit-floor` in PX, or null for a track the token does not reach.
 *
 *  Assigned to a real length property rather than read off the custom property: an
 *  unregistered one's computed value is its own token stream, so `--hit-floor` reads
 *  back `1.5rem` and a `parseFloat` answers 1.5. The probe is the track's SIBLING,
 *  because `.turn-rail:empty` ties the rail's own box to whether it has children. */
function floorPx(track: HTMLElement): number | null {
  const host = track.parentElement ?? track.ownerDocument.body;
  const probe = track.ownerDocument.createElement("div");
  probe.style.cssText = "position:absolute;visibility:hidden;block-size:var(--hit-floor)";
  host.appendChild(probe);
  const px = parseFloat(getComputedStyle(probe).blockSize);
  probe.remove();
  return Number.isFinite(px) && px > 0 ? px : null;
}

/** THE PITCH A YOUNG RAIL SPACES ITS MARKERS AT: one marker box of clear, against
 *  `MARKER_CLEAR_PX`'s 4px floor a full track compresses to.
 *
 *  A session shorter than the track is spread from the TOP at this pitch rather than
 *  across the whole track, which is what puts turn 2 one gap under turn 1 instead of
 *  at the foot of the column beside the resume control. The gaps then tighten as
 *  turns accumulate and reach the floor as the track fills, so the compression is
 *  continuous and the downsample below takes over from it rather than from a jump.
 *
 *  Two marker boxes is also the smallest pitch that leaves a SEAM a whole box tall:
 *  `.rail-seam` spans the gap between the markers it separates minus one box, so at
 *  the 4px floor a band is 4px and at this pitch it is a marker. */
export function relaxedPitch(markerPx: number): number {
  return markerPx * 2;
}

/** How much of the track's travel the whole marker set occupies, 0..1: the relaxed
 *  pitch while every turn fits at it, all of it once they do not.
 *
 *  This is the one value that makes a marker's position a function of the SESSION's
 *  size as well as of the turn's own number, so every `railAt` one render publishes
 *  has to carry the same one. `1` for a track with no travel, because a fraction of
 *  nothing is unused rather than wrong. */
export function railSpan(total: number, trackPx: number, markerPx: number): number {
  const travel = Math.max(0, trackPx - markerPx);
  if (travel === 0) {
    return 1;
  }
  return Math.min(1, (Math.max(0, total - 1) * relaxedPitch(markerPx)) / travel);
}

/** A turn's position on the axis: 0 at the first turn, `span` at the Nth, as a
 *  fraction of the track's travel. Published as `--rail-at`, so the arithmetic below
 *  and the rendered `top` cannot disagree.
 *
 *  `span` is REQUIRED rather than defaulted to 1, because 1 is the stretched layout
 *  this argument exists to stop: a caller that forgot it would put the last turn at
 *  the foot of the track and nothing would say so. */
export function railAt(n: number, total: number, span: number): number {
  return ((n - 1) / Math.max(1, total - 1)) * span;
}

/** A marker's own top. The travel span is the track minus one marker box, so both
 *  ends sit fully inside it. It resolves the session's own span, so the separation
 *  pass below reasons about the positions the render produces. */
export function markerPosition(
  n: number,
  total: number,
  trackPx: number,
  markerPx: number,
): number {
  const travel = Math.max(0, trackPx - markerPx);
  return railAt(n, total, railSpan(total, trackPx, markerPx)) * travel;
}

/** How many markers a track of this height holds at the tier's separation. */
export function maxMarkers(trackPx: number, pitchPx: number): number {
  return Math.max(1, Math.floor(trackPx / pitchPx));
}

/** Which turns get a marker. IT TAKES NO SCROLL STATE, and that is the invariant
 *  rather than an omission: a set that moves with the reader is presence churn they
 *  see. `hits` is the search-hit rank passed IN, read once per render, so a
 *  module-level read of the search state cannot put presence back on their keystrokes.
 *
 *  Ranks, worst dropped first: a uniform stride, a live search hit, a turn that did
 *  not end clean, then the FIRST and LAST turn, never dropped. One separation pass
 *  drops the lower-ranked of a pair closer than `pitchPx`, rank 1 exempt. */
export function selectMarkers(
  turns: readonly TurnSummary[],
  trackPx: number,
  pitchPx: number,
  hits: ReadonlySet<number>,
): TurnSummary[] {
  const last = turns.length - 1;
  if (last < 0) {
    return [];
  }
  const total = turns[last]?.n ?? 1;
  // N is the LAST turn's own `n`, which `mergeTurnSets` makes the session count, and
  // the marker box is the pitch minus the clear `railMetrics` added to it.
  const markerPx = pitchPx - MARKER_CLEAR_PX;
  const cap = maxMarkers(trackPx, pitchPx);

  const rank = new Map<number, number>();
  const claim = (i: number, r: number): void => {
    const held = rank.get(i);
    if (held === undefined || r < held) {
      rank.set(i, r);
    }
  };
  claim(0, 1);
  claim(last, 1);
  for (let i = 0; i <= last; i++) {
    const t = turns[i];
    if (t === undefined) {
      continue;
    }
    if (severityOf(t.outcome) !== "clean") {
      claim(i, 2);
    } else if (hits.has(t.n)) {
      claim(i, 3);
    }
  }
  for (const i of stride(turns.length, rank, cap)) {
    claim(i, 4);
  }

  const picked = [...rank.keys()].sort((a, b) => a - b);
  const kept: number[] = [];
  for (const i of picked) {
    const r = rank.get(i) ?? 4;
    const pos = markerPosition(turns[i]?.n ?? 1, total, trackPx, markerPx);
    while (kept.length > 0) {
      const prevIndex = kept[kept.length - 1] ?? 0;
      const prevRank = rank.get(prevIndex) ?? 4;
      const prevPos = markerPosition(turns[prevIndex]?.n ?? 1, total, trackPx, markerPx);
      if (pos - prevPos >= pitchPx || prevRank === 1) {
        break;
      }
      if (r < prevRank) {
        kept.pop();
        continue;
      }
      break;
    }
    const prevIndex = kept[kept.length - 1];
    if (prevIndex === undefined) {
      kept.push(i);
      continue;
    }
    const prevPos = markerPosition(turns[prevIndex]?.n ?? 1, total, trackPx, markerPx);
    if (pos - prevPos >= pitchPx || r === 1) {
      kept.push(i);
    }
  }

  const out: TurnSummary[] = [];
  for (const i of kept) {
    const t = turns[i];
    if (t !== undefined) {
      out.push(t);
    }
  }
  return out;
}

/** A uniform stride over the turns no higher rank claimed, filling what is left of
 *  the track's slots. */
function stride(count: number, rank: ReadonlyMap<number, number>, cap: number): number[] {
  const rest: number[] = [];
  for (let i = 0; i < count; i++) {
    if (!rank.has(i)) {
      rest.push(i);
    }
  }
  const slots = cap - rank.size;
  if (slots <= 0 || rest.length === 0) {
    return [];
  }
  if (slots >= rest.length) {
    return rest;
  }
  if (slots === 1) {
    return [rest[Math.floor((rest.length - 1) / 2)] ?? 0];
  }
  const out = new Set<number>();
  for (let k = 0; k < slots; k++) {
    const at = Math.round((k * (rest.length - 1)) / (slots - 1));
    const i = rest[at];
    if (i !== undefined) {
      out.add(i);
    }
  }
  return [...out];
}
