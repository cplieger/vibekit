// What a rail row SAYS, in words. PURE AND DOM-FREE, so the renderer and the tests
// can both read it without either dragging a document into the other's. The outcome
// vocabulary is `turn-severity.ts`'s and is never re-derived here.

import { formatElapsed } from "./strings.js";
import { OUTCOME_LABEL, OUTCOME_TOOLTIP } from "./turn-severity.js";
import type { TurnOutcome } from "./turns.js";

/** The two channels one row publishes. `tooltip` is the styled hover/description
 *  (`data-tooltip`); `ariaLabel` is the accessible NAME. */
export interface RailLabel {
  tooltip: string;
  ariaLabel: string;
}

/** What a marker needs to describe itself: four of an index row's six fields and no
 *  more, so a label cannot come to depend on the row's identity or its timestamp. A
 *  `TurnSummary` satisfies it. */
export interface MarkerSubject {
  n: number;
  outcome: TurnOutcome;
  first_line?: string;
  agent_initiated?: boolean;
}

/** The facts the SUBJECT cannot carry: the rail's own view state, plus the one
 *  property of the turn that its own feed does not report.
 *
 *  Deliberately no `current`. Currency travels on `aria-current`, which is what
 *  that attribute means, and naming it here too would be the "two renderings of one
 *  fact" defect this app has recorded repeatedly (the header dot against the tab
 *  dot, the todo block's glyphs against the task pill's). */
export interface MarkerState {
  /** The jump is paging history in, so the click has not visibly done anything yet. */
  pending: boolean;
  /** A live search matches inside this turn. */
  hit: boolean;
  /** How long the turn took. ABSENT rather than zero for a turn the transcript store
   *  does not hold, because a duration nobody stamped is not a duration of zero. */
  elapsedMs?: number | undefined;
  /** The pause this turn opens a new sitting after, already worded. The seam's band
   *  paints no text, so this is the only channel that pause reaches a reader. */
  gapBefore?: string | undefined;
}

/** The app's own separator, matching `turn-footer.ts`'s ledger line. */
const SEP = " \u00b7 ";

/** Compose a marker's two labels. `completed` contributes NOTHING to either, the
 *  same rule the header dot and the footer glyph follow by hiding themselves on a
 *  clean turn: a mark on every row communicates nothing.
 *
 *  Order is identity, then the turn's own durable state, then the two transient
 *  facts — the reader hovering a marker is usually looking for "which turn was
 *  that" and only sometimes for "why is it red". */
export function markerLabel(s: MarkerSubject, state: MarkerState): RailLabel {
  const agentInitiated = s.agent_initiated === true;
  const line = (s.first_line ?? "").trim();

  const tip: string[] = [];
  // An agent-initiated turn has no request to name — the server sets `first_line`
  // only inside its user-role branch — so the marker says what it IS instead.
  tip.push(line !== "" ? line : agentInitiated ? "Agent-initiated turn" : `Turn ${String(s.n)}`);
  if (s.outcome !== "completed") {
    tip.push(OUTCOME_TOOLTIP[s.outcome]);
  }
  // The DESCRIPTION channel and not the name: an `aria-label` wins over a button's
  // own text, so a name carrying this would spend every focus reading it out.
  if (state.elapsedMs !== undefined && state.elapsedMs > 0) {
    tip.push(formatElapsed(state.elapsedMs));
  }
  if (state.gapBefore !== undefined && state.gapBefore !== "") {
    tip.push(`${state.gapBefore} pause before this turn`);
  }
  if (state.pending) {
    tip.push("Loading this turn\u2026");
  }
  if (state.hit) {
    tip.push("Contains a search match");
  }

  // Comma-separated and short: this is read out on every focus, where the tooltip
  // is read once on request.
  const name: string[] = [`Go to turn ${String(s.n)}`];
  if (s.outcome !== "completed") {
    name.push(OUTCOME_LABEL[s.outcome].toLowerCase());
  }
  if (agentInitiated) {
    // The dashed italic border carries this too, and reaches nobody who cannot see
    // it, so the name is the channel that does not depend on sight.
    name.push("agent-initiated");
  }

  return { tooltip: tip.join(SEP), ariaLabel: name.join(", ") };
}

/** Name a seam: the pause it stands for and the two turns it separates. Takes the
 *  gap already worded, because the coarse `2h` vocabulary has one owner in the
 *  renderer and a second formatter here could disagree with it. */
export function seamLabel(gap: string, fromN: number, toN: number): string {
  return `${gap} pause between turn ${String(fromN)} and turn ${String(toN)}`;
}

/** The rail's own accessible name. It states the set the rail SHOWS whenever that is
 *  smaller than the session, so the reader is told what the column leaves out rather
 *  than being promised a row per turn. STABLE, because the count is a function of the
 *  same four inputs `selectMarkers` takes and so cannot move as the reader scrolls. */
export function railLabel(shown: number, total: number): string {
  if (total === 0 || shown >= total) {
    return "Turn timeline";
  }
  return `Turn timeline, showing ${String(shown)} of ${String(total)} turns`;
}
