// Does every outcome the wire can send have a visual treatment?
//
// `TurnOutcome` gained `refused`, `cancelled` and `unknown`, and the stylesheet
// was never extended, so three of the six persisted values fell through to a
// resting state authored for a different meaning. Two defects came out of that,
// and the second is the one worth the test: the header dot painted the same
// neutral grey for a model refusal and for an end vibekit could not read, and the
// footer's ledger glyph — whose resting state is a GREEN RING meaning "clean" —
// carried the mark of a clean turn on a cancelled, refused or unknown one. A
// status glyph may fall back to ambiguous; it may never fall back to reassuring.
//
// Source facts rather than computed ones, for the reason `css-rules.ts` records:
// the test page links no app stylesheet, so `getComputedStyle` has no cascade to
// report on. Every case below asks which rule owns a selector and what that rule
// declares, which is the association a file-wide grep cannot make.
import { describe, it, expect } from "vitest";

import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import type { TurnOutcome } from "./turns.js";

const turns = loadCSS("29-turns.css");

/** Every outcome that is PERSISTED and did not finish cleanly.
 *
 *  `running` is excluded because it is never persisted, and `completed` because
 *  its treatment is deliberately to show nothing at all — a marker on every row
 *  of the transcript communicates nothing, which is what the header dot's own
 *  comment records. */
const marked: TurnOutcome[] = ["cancelled", "interrupted", "failed", "refused", "unknown"];

describe("every outcome the wire can send has a treatment", () => {
  it("gives each one a header dot rule of its own", () => {
    for (const outcome of marked) {
      const rule = ruleContaining(turns, `.turn-header[data-outcome="${outcome}"] .turn-dot`);
      expect(rule.body, `${outcome} dot declares a background`).toMatch(/background:/u);
    }
  });

  it("does not paint a refusal the same as an unreadable end", () => {
    // gpt's minimum on this finding, and the pair it named: before the rules
    // existed both resolved to `.turn-dot`'s neutral default, so a model
    // declining to continue and a stop reason vibekit has never measured were
    // one colour. A refusal takes the refusal callout's own quiet red (see
    // 13-messages.css); `unknown` keeps the neutral, which is the honest hue for
    // a state nothing explains, and states it rather than inheriting it.
    const refused = ruleContaining(turns, '.turn-header[data-outcome="refused"] .turn-dot');
    const unknown = ruleContaining(turns, '.turn-header[data-outcome="unknown"] .turn-dot');
    expect(refused.body.trim()).not.toBe(unknown.body.trim());
  });

  it("never leaves the ledger glyph reading as a clean turn", () => {
    // The resting glyph is `border: 1.5px solid var(--c-green)` with no fill — a
    // green ring. So an outcome with no rule of its own does not render as
    // "unknown", it renders as "this worked", which is the one direction a status
    // mark must not fail in. Every marked outcome overrides BOTH channels: the
    // border colour and a fill, so the shape differs from a clean turn's too.
    for (const outcome of marked) {
      const rule = ruleContaining(
        turns,
        `.turn-footer[data-outcome="${outcome}"] .turn-ledger-glyph`,
      );
      expect(rule.body, `${outcome} glyph re-colours the ring`).toMatch(/border-color:/u);
      expect(rule.body, `${outcome} glyph is filled, not a ring`).toMatch(/background:/u);
    }
  });
});
