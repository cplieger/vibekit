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

  it("paints the transcript's in-flight marks with the TAB STRIP's ink", () => {
    // One fact, one violet. Both of these used to take `--c-accent`, a
    // near-neighbour of the tab dot's `--c-dot-working`, so "a turn is running"
    // carried two colours depending on which surface you read it from. The two
    // marks are asserted together because that agreement is the whole point —
    // repointing one and not the other would leave the transcript disagreeing
    // with itself.
    const dot = ruleContaining(turns, '.turn-header[data-outcome="running"] .turn-dot');
    expect(dot.body).toContain("var(--c-dot-working)");
    expect(dot.body).not.toContain("--c-accent");

    const marker = ruleContaining(turns, '.rail-marker[data-outcome="running"]');
    expect(marker.body).toContain("var(--c-dot-working)");
    expect(marker.body).not.toContain("--c-accent");
  });

  it("keeps the header dot breathing, since motion is its second channel", () => {
    // The dot renders only below 48rem, where the tab strip is off-canvas, so it
    // is the off-screen-work case the pulsing-dot ruling carves out
    // (13-messages.css). Motion is also what separates `running` from a still
    // outcome without relying on hue — dropping it would leave the two
    // yellow/red/neutral outcomes and this one separable by colour alone.
    const dot = ruleContaining(turns, '.turn-header[data-outcome="running"] .turn-dot');
    expect(dot.body).toMatch(/opacity:\s*calc\(1 - var\(--vk-beat\)/u);
  });

  it("shows no ledger glyph at all for a running turn", () => {
    // The footer says how a turn ENDED. Its accent ring was indistinguishable
    // from the resting green one at 8px and its tooltip was the only channel
    // saying otherwise, so the arm went — and it had to become `display: none`
    // rather than a deletion, because the base rule is a green ring meaning
    // CLEAN and falling through to it puts a finished turn's mark on a live one.
    const rule = ruleContaining(turns, '.turn-footer[data-outcome="running"] .turn-ledger-glyph');
    expect(rule.body).toMatch(/display:\s*none/u);
    expect(rule.body).not.toMatch(/border-color:/u);
    // Folded into `completed`'s rule, which is the honest shape: the two states
    // want the same treatment for the same reason.
    expect(rule.selector).toContain('.turn-footer[data-outcome="completed"] .turn-ledger-glyph');
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
