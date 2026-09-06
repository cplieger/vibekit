// ---------------------------------------------------------------------------
// How badly a turn ended, as ONE table.
//
// This module exists because five surfaces each carried their own partial answer
// to that one question and disagreed with each other. `interrupted` was the
// measured case: `messages-events.ts` gave it a red `boundary: "failed"` divider,
// `turns.ts`'s face lookup put its text in the collapsed face, `29-turns.css` gave its
// footer glyph a filled yellow mark and a lead word, and `fold-state.ts`'s own
// header comment promised it would never auto-fold — while `store.ts`'s outcome
// latch mapped it to NOTHING, so the tab dot fell through to `idle` and painted the
// hollow ring that means "nothing is happening here", and `attention.ts`'s favicon
// cue raised nothing at all. One fault, five surfaces, two answers.
//
// So a surface asks for a SEVERITY and never enumerates outcomes again. Nothing in
// this app may re-derive the mapping: a second copy is what the disagreement was.
//
// CROSS-LANGUAGE CONTRACT. The identical table is `vibekit.SeverityOf` +
// `vibekit.DefaultFailureReason` in `internal/vibekit/turns.go`, and both halves
// are pinned against one shared fixture — `internal/vibekit/testdata/
// turn_severity.json`, read by Go's TestTurnSeverityContract and by
// `turn-severity.node.test.ts`. Change the rule in one language and the other
// language's test fails, which is the only thing keeping the two honest.
//
// Pure and DOM-free, like `turns.ts` beside it, because the fold rule, the store's
// latch, the favicon cue and the renderer all need it and none of them may drag a
// document into the others' tests.
// ---------------------------------------------------------------------------

import type { TurnOutcome, TurnSeverity } from "./wire/types.gen.js";

/** How badly a turn ended.
 *
 *  RE-EXPORTED from the generated wire types for `TurnOutcome`'s reason: the rule
 *  producing it is implemented in both languages, so a hand-written union here
 *  would be a second spelling of one vocabulary with nothing holding the two
 *  together — and the five branches over it have to be TOTAL. */
export type { TurnSeverity };

/** Grade a turn outcome.
 *
 *  Total over the seven outcomes and MECE, and total in BOTH directions: the
 *  `default` arm assigns `outcome` to a `never`, so an eighth member added to the
 *  generated union is a COMPILE error here rather than a value silently graded
 *  `stopped`. The runtime fallback stays behind that check, because the compiler
 *  cannot see a value the decoder let through.
 *
 *  `stopped` rather than `clean` is the direction of both: a value the wire adds
 *  later must not read as a turn that worked. The DECODER is what makes an unknown
 *  value unreachable in practice (`TurnOutcome` is a generated union, so a subject
 *  carrying something else fails at the boundary), and the arm is what makes the
 *  failure direction safe if it ever is reached. `undefined` gets its own case so
 *  it does not consume the exhaustiveness check the eighth-member guard needs.
 *
 *  Two rulings, both of which the surfaces above had got wrong between them:
 *
 *   - `interrupted` is BROKEN. A fault nobody chose stopped the turn, four
 *     surfaces already said so, and the latch that fed the tab dot is the one that
 *     did not. THIS LINE is the fix for the hollow dot.
 *   - `unknown` is STOPPED, never broken. `ConcludeStopReason`'s ruling is the
 *     authority: an unmeasured stop reason says nothing about whether the work
 *     succeeded, so grading it broken would report a working turn as failed. It is
 *     equally not clean — a status mark may fall back to ambiguous and may never
 *     fall back to reassuring. */
export function severityOf(outcome: TurnOutcome | undefined): TurnSeverity {
  switch (outcome) {
    case "running":
      return "running";
    case "completed":
      return "clean";
    case "cancelled":
    case "unknown":
      return "stopped";
    case "interrupted":
    case "failed":
    case "refused":
      return "broken";
    case undefined:
      return "stopped";
    default: {
      const _never: never = outcome;
      void _never;
      return "stopped";
    }
  }
}

/** Is this turn a failure? THE predicate, and the only question most surfaces
 *  have. Its own function rather than an inline comparison so a grep for the
 *  question finds every asker, and so the five surfaces cannot each spell the
 *  comparison differently. */
export function isBroken(outcome: TurnOutcome | undefined): boolean {
  return severityOf(outcome) === "broken";
}

/** The outcome as ONE WORD, for an accessible NAME — read on every focus, so it
 *  has to be short. Total over `TurnOutcome`.
 *
 *  THE TABLE LIVES HERE BECAUSE TWO SURFACES READ IT: the turn header's dot and
 *  the timeline rail's marker. It was the header's private const, and the rail
 *  then had no words for outcome at all — a marker's whole state vocabulary was
 *  colour plus border style, with nothing anywhere saying what any of it meant.
 *  Copying the table into the rail is the defect this module exists to prevent,
 *  which is why it moved rather than being duplicated.
 *
 *  Not part of the cross-language severity contract: the fixture pins `severityOf`
 *  and `defaultFailureReason`, and these two are display strings with no Go twin. */
export const OUTCOME_LABEL: Record<TurnOutcome, string> = {
  running: "Running",
  completed: "Completed",
  cancelled: "Cancelled",
  interrupted: "Interrupted",
  refused: "Refused",
  unknown: "Unknown",
  failed: "Failed",
};

/** The outcome as a SENTENCE, for hover and description text — what the state
 *  MEANS rather than its one-word name. Total over `TurnOutcome`, and read by the
 *  same two surfaces as `OUTCOME_LABEL` for the same reason.
 *
 *  Distinct from `defaultFailureReason` on purpose: that answers "why did this turn
 *  not finish" for a turn whose own account is missing, is byte-identical to Go's,
 *  and is empty for the two outcomes that are not failures. This answers "what does
 *  this mark mean" and is total. */
export const OUTCOME_TOOLTIP: Record<TurnOutcome, string> = {
  running: "This turn is still running",
  completed: "This turn finished normally",
  cancelled: "You stopped this turn",
  interrupted: "This turn was interrupted before it finished",
  refused: "The model declined to continue",
  unknown: "This turn's end could not be read",
  failed: "This turn failed",
};

/** What a turn says when nothing upstream said anything.
 *
 *  The server stamps `turn_failure_reason` on the message that finalized the turn,
 *  so a turn persisted since that field existed carries its own account and this is
 *  never consulted for it. This is the fallback for the ones that DO NOT: every
 *  turn already on disk, which is the population symptom 1 was reported against —
 *  a `failed` turn with 26 blocks, a red footer mark and no sentence anywhere in
 *  the record.
 *
 *  Keyed per OUTCOME rather than per severity because a reader wants the
 *  distinction the severity throws away: a refusal and a dropped connection are
 *  both broken and want different words. Empty for `completed` and `running`, so a
 *  caller can read "" as "there is nothing to say" without asking the severity
 *  again — and those two are spelled out as cases rather than left to the default,
 *  so an eighth outcome is a compile error here too and cannot silently inherit "".
 *
 *  The strings are BYTE-IDENTICAL to `vibekit.DefaultFailureReason`'s and pinned
 *  that way by the shared fixture. Two hand-written copies of one sentence is
 *  exactly the drift a shared fixture exists to make impossible, and the two
 *  populations are genuinely one thing — the server's sentence goes on disk for a
 *  new turn, this one stands in for an old turn's missing copy of it, and a reader
 *  scrolling one transcript must not see two wordings for one cause. */
export function defaultFailureReason(outcome: TurnOutcome | undefined): string {
  switch (outcome) {
    case "failed":
      return "The agent reported an error and the turn stopped.";
    case "interrupted":
      return "The turn was interrupted before the agent finished.";
    case "refused":
      return "The model declined to continue.";
    case "cancelled":
      return "The turn was cancelled.";
    case "unknown":
      return "The turn ended for a reason vibekit could not read.";
    case "completed":
    case "running":
    case undefined:
      return "";
    default: {
      const _never: never = outcome;
      void _never;
      return "";
    }
  }
}
