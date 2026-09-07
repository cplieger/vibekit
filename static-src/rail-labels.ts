// ---------------------------------------------------------------------------
// What a timeline-rail row SAYS, in words.
//
// The rail had five visual states on one 24px box — tertiary ink, violet for
// running, yellow for a stop, red for a failure, an accent fill for the current
// turn, a dashed italic border for an agent-initiated one, a pulsing opacity for a
// pending fetch, a yellow corner dot for a search hit — and a legend for none of
// them. Its only hover text was the request's first line, which is the marker's
// IDENTITY and never its state. So a reader looking at a dashed red marker had no
// way to learn what either channel meant, and a screen-reader user was told
// nothing at all: the accessible name was `Go to turn 14` and the `title` fell
// back to `Turn 14`, because the server leaves `first_line` empty for a non-user
// turn.
//
// The fix is one state sentence per row, delivered through `data-tooltip` — which
// `@cplieger/ui-primitives`' tooltip publishes as `aria-describedby`, and whose
// focus trigger is `:focus-visible`-gated, so a keyboard user tabbing the rail
// gets it and a programmatic focus does not pop one — and MIRRORED into
// `aria-label`, because a description can be skipped by some AT configurations
// while the name cannot. An `aria-label` also wins over a button's own text, so
// the digit never reaches a screen reader anyway and the label was the only
// channel already there.
//
// There is NO in-block state label to go with it. The turn card already carries
// the outcome in words twice — `.turn-notice`'s sentence and the footer ledger's
// lead word — and a legend has to be reachable from the thing it explains, which
// for a dashed marker is the rail rather than a card the reader has not scrolled
// to.
//
// PURE AND DOM-FREE, for the reason `turns.ts` and `turn-severity.ts` are: the
// renderer reads it and so do the tests, and neither may drag a document into the
// other's. The outcome vocabulary comes from `turn-severity.ts` and is never
// re-derived here.
// ---------------------------------------------------------------------------

import { OUTCOME_LABEL, OUTCOME_TOOLTIP, severityOf } from "./turn-severity.js";
import type { TurnOutcome } from "./turns.js";

/** The two channels one row publishes. `tooltip` is the styled hover/description
 *  (`data-tooltip`); `ariaLabel` is the accessible NAME. */
export interface RailLabel {
  tooltip: string;
  ariaLabel: string;
}

/** What a marker needs to describe itself. Structural rather than an import of
 *  `TurnSummary`, which lives in `turn-rail.ts` — the consumer — so typing the
 *  parameter as that interface would close a cycle. A `TurnSummary` satisfies it. */
export interface MarkerSubject {
  n: number;
  outcome: TurnOutcome;
  first_line?: string;
  agent_initiated?: boolean;
}

/** The transient facts the SUBJECT cannot carry: they are the rail's own view
 *  state, not the turn's.
 *
 *  Deliberately no `current`. Currency travels on `aria-current`, which is what
 *  that attribute means, and naming it here too would be the "two renderings of one
 *  fact" defect this app has recorded repeatedly (the header dot against the tab
 *  dot, the todo block's glyphs against the task pill's). */
export interface MarkerState {
  /** The jump is paging history in, so the click has not visibly done anything
   *  yet. Before this it was a pulsing opacity and nothing else. */
  pending: boolean;
  /** A live search matches inside this turn. */
  hit: boolean;
}

/** The app's own separator, matching `turn-footer.ts`'s ledger line. */
const SEP = " \u00b7 ";

/** Compose a marker's two labels.
 *
 *  `completed` contributes NOTHING to either, which is the same rule the header dot
 *  and the footer glyph already follow by hiding themselves on a clean turn: a mark
 *  on every row of the transcript communicates nothing, and "Completed" on every
 *  marker of a healthy session is noise a reader learns to skip.
 *
 *  Order is identity first, then state, because the reader hovering a marker is
 *  usually looking for "which turn was that" and only sometimes for "why is it
 *  red". */
export function markerLabel(s: MarkerSubject, state: MarkerState): RailLabel {
  const agentInitiated = s.agent_initiated === true;
  const line = (s.first_line ?? "").trim();

  const tip: string[] = [];
  // An agent-initiated turn has no request to name — the server sets `first_line`
  // only inside its user-role branch — so the marker says what it IS instead of
  // falling back to `Turn 14`, which restates the digit already on the button.
  tip.push(line !== "" ? line : agentInitiated ? "Agent-initiated turn" : `Turn ${String(s.n)}`);
  if (s.outcome !== "completed") {
    tip.push(OUTCOME_TOOLTIP[s.outcome]);
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
    // The one fact that separated a dashed italic marker from a plain one, and it
    // reached no reader who could not see the border. Dashed-plus-italic stays as
    // a supplement; this is the channel that does not depend on sight.
    name.push("agent-initiated");
  }

  return { tooltip: tip.join(SEP), ariaLabel: name.join(", ") };
}

/** What a cluster row needs to describe itself. */
export interface ClusterSubject {
  from: number;
  to: number;
  /** The range's WORST outcome, which is ink-only on the rail itself. */
  outcome: TurnOutcome;
  count: number;
}

export interface ClusterState {
  /** The reader's current turn falls inside this range. A cluster is the only row
   *  that can hold the current turn without being it, and past capacity every turn
   *  is inside one — so without this the rail shows no position at all. */
  containsCurrent: boolean;
}

/** Compose a cluster's two labels.
 *
 *  It names its worst outcome, which the marker vocabulary carries as ink and
 *  nothing else, and it says how many turns it stands for — a `30\u201338` label
 *  states its bounds and leaves the reader to subtract. */
export function clusterLabel(row: ClusterSubject, state: ClusterState): RailLabel {
  const range = `Turns ${String(row.from)}\u2013${String(row.to)}`;
  const tip: string[] = [range, `${String(row.count)} turn${row.count === 1 ? "" : "s"}`];
  if (severityOf(row.outcome) !== "clean") {
    tip.push(`Worst outcome: ${OUTCOME_LABEL[row.outcome]}`);
  }
  if (state.containsCurrent) {
    tip.push("Contains the current turn");
  }

  const name: string[] = [`Zoom to turns ${String(row.from)} to ${String(row.to)}`];
  if (severityOf(row.outcome) !== "clean") {
    name.push(`worst outcome ${OUTCOME_LABEL[row.outcome].toLowerCase()}`);
  }
  if (state.containsCurrent) {
    name.push("contains the current turn");
  }

  return { tooltip: tip.join(SEP), ariaLabel: name.join(", ") };
}

/** Compose the zoom-out row's two labels for the range the rail is currently
 *  limited to.
 *
 *  THE TWO CHANNELS SAY DIFFERENT THINGS, and that is the whole reason this row
 *  needs a composer at all rather than two literals at the button. The tooltip
 *  controller publishes its text as the anchor's `aria-describedby` on show, so a
 *  keyboard user hears BOTH — and both were the same sentence, which is a name read
 *  twice rather than a name plus a description.
 *
 *  So the NAME carries the action, which is the fact a three-character `all` button
 *  cannot state on its own and the one channel that is durable (a description is
 *  added on show and removed on hide). The TOOLTIP carries the state the name
 *  deliberately leaves out: which range the rail is limited to. That is additive in
 *  both directions — a mouse user learns the rail is narrowed rather than short, and
 *  a screen-reader user hears the bounds their eyes are not reading off the column. */
export function zoomOutLabel(range: { from: number; to: number }): RailLabel {
  return {
    tooltip: `Showing turns ${String(range.from)}\u2013${String(range.to)}`,
    ariaLabel: "Show the whole session",
  };
}
