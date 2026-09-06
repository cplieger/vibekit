// Does the transcript paint ONE hue per severity, on every surface?
//
// This file used to ask a weaker question — does every OUTCOME have a treatment —
// and it passed while the defect it now guards was live. Four surfaces in
// 29-turns.css (the header dot, the footer wash, the footer glyph, the rail
// marker and cluster) each carried their own per-outcome colour table, and a fifth
// (`.turn-notice`) read `severityOf`. So `interrupted` — graded BROKEN by the
// shared table — painted the notice red and the other four yellow, on one card,
// for one outcome. Measured in the live render.
//
// The fix is a severity partition, and this file asserts it in BOTH DIRECTIONS.
// The forward direction (every severity that paints a mark has a rule) is what the
// old cases did. The MISSING one — no surface may carry a `[data-outcome=…]`
// colour rule of its own for an outcome the table grades `broken` — is what would
// have caught the defect, because it is the shape that lets a hue be set per
// outcome behind the partition's back.
//
// Source facts rather than computed ones, for the reason `css-rules.ts` records:
// the test page links no app stylesheet, so `getComputedStyle` has no cascade to
// report on. Every case below asks which rule owns a selector and what that rule
// declares, which is the association a file-wide grep cannot make.
//
// A CSS rule keyed on an attribute nothing writes paints nothing and fails
// silently, so this file is only meaningful beside `turn-outcome-attr.test.ts`,
// which drives the three real writers in a real DOM and asserts each stamps
// `data-severity` from `severityOf`.
import { describe, it, expect } from "vitest";

import { allRules, loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { severityOf } from "./turn-severity.js";
import type { TurnOutcome } from "./turns.js";
import type { TurnSeverity } from "./wire/types.gen.js";

const turns = loadCSS("29-turns.css");

/** Every outcome the wire can send. Not imported as a list — the generated union
 *  is a type — so it is spelled here and pinned total by the severity coverage
 *  case at the foot of this file. */
const OUTCOMES: TurnOutcome[] = [
  "running",
  "completed",
  "cancelled",
  "interrupted",
  "refused",
  "unknown",
  "failed",
];

/** The four MARK surfaces, each as the selector prefix its severity arms take
 *  plus the descendant the mark itself is. The `.turn-notice` prose surface is
 *  asserted separately at the foot of the file: it is the one that already read
 *  the table, and it carries ink rather than a mark. */
const SURFACES = [
  { name: "header dot", sel: (attr: string) => `.turn-header[${attr}] .turn-dot` },
  { name: "footer wash", sel: (attr: string) => `.turn-footer[${attr}]` },
  { name: "footer glyph", sel: (attr: string) => `.turn-footer[${attr}] .turn-ledger-glyph` },
  { name: "rail marker", sel: (attr: string) => `.rail-marker[${attr}]` },
] as const;

/** The rail marker's RESTING declarations.
 *
 *  `ruleContaining` cannot reach this one: TWO top-level rules list `.rail-marker`
 *  — the shared box rule it holds with the cluster and the zoom-out button, and a
 *  later one-liner giving it `position: relative` so the search-hit dot has
 *  something to anchor to — and the helper demands exactly one match rather than
 *  silently picking a first hit. So the lookup says which by naming the
 *  declaration it is after. */
function restingMarkerRule(): string {
  const hits = allRules(turns).filter(
    (r) =>
      r.selector
        .split(",")
        .map((s) => s.trim())
        .includes(".rail-marker") && /color:/u.test(r.body),
  );
  expect(hits, "exactly one rule sets the resting marker ink").toHaveLength(1);
  return hits[0]?.body ?? "";
}

/** Which severities a surface paints a MARK for. `clean` and `running` paint
 *  nothing on three of the four (absence IS the clean mark, and the footer reports
 *  how a turn ENDED), and the rail leaves both to the resting ink. */
const MARKED: TurnSeverity[] = ["stopped", "broken"];

describe("hue comes off the severity table, on every surface", () => {
  it("gives each surface a rule for every severity that paints a mark", () => {
    for (const surface of SURFACES) {
      for (const severity of MARKED) {
        const rule = ruleContaining(turns, surface.sel(`data-severity="${severity}"`));
        expect(rule.body, `${surface.name} / ${severity} declares a colour`).toMatch(
          /(background|border-color|color):/u,
        );
      }
    }
  });

  it("re-colours a BROKEN turn away from the resting ink on every surface, and never hides it", () => {
    // The failure DIRECTION rather than the hue. `display: none` is `clean`'s and
    // `running`'s treatment on the two footer surfaces and would erase a failure
    // outright; the resting green ring on the glyph and the resting tertiary ink on
    // the rail both read as "this worked".
    const dot = ruleContaining(turns, '.turn-header[data-severity="broken"] .turn-dot');
    expect(dot.body).not.toMatch(/display:\s*none/u);
    expect(dot.body).not.toMatch(/var\(--c-text-tertiary\)/u);

    const glyph = ruleContaining(turns, '.turn-footer[data-severity="broken"] .turn-ledger-glyph');
    expect(glyph.body, "a broken turn still paints a mark").not.toMatch(/display:\s*none/u);
    expect(glyph.body, "a broken turn does not keep the clean green ring").not.toMatch(
      /--c-green/u,
    );
    expect(glyph.body, "a broken glyph is filled, not a ring").toMatch(/background:/u);

    const marker = ruleContaining(turns, '.rail-marker[data-severity="broken"]');
    expect(marker.body, "a broken marker re-colours").not.toMatch(
      /color:\s*var\(--c-text-tertiary\)/u,
    );

    const wash = ruleContaining(turns, '.turn-footer[data-severity="broken"]');
    expect(wash.body).toMatch(/background:\s*color-mix/u);
  });

  it("never paints broken and stopped the same, on any surface", () => {
    // The two are different verdicts, and a reader scanning a transcript separates
    // them by hue before reading a word. Equal bodies would mean the partition
    // exists in name only.
    for (const surface of SURFACES) {
      const broken = ruleContaining(turns, surface.sel('data-severity="broken"'));
      const stopped = ruleContaining(turns, surface.sel('data-severity="stopped"'));
      expect(broken.body.trim(), `${surface.name} separates broken from stopped`).not.toBe(
        stopped.body.trim(),
      );
    }
  });

  it("paints no settled mark for a clean or a running turn", () => {
    // The header dot and the footer glyph HIDE, which is `clean`'s whole treatment —
    // a mark on every row of the transcript communicates nothing. `running` joins
    // `clean` on the glyph because the footer reports how a turn ENDED.
    expect(ruleContaining(turns, '.turn-header[data-severity="clean"] .turn-dot').body).toMatch(
      /display:\s*none/u,
    );

    const glyph = ruleContaining(turns, '.turn-footer[data-severity="clean"] .turn-ledger-glyph');
    expect(glyph.body).toMatch(/display:\s*none/u);
    expect(glyph.selector).toContain('.turn-footer[data-severity="running"] .turn-ledger-glyph');
    expect(glyph.body).not.toMatch(/border-color:/u);

    // The wash and the rail have no clean arm at all: both fall through to a resting
    // state authored for exactly that case, so an arm restating it would be dead.
    expect(
      allRules(turns).filter((r) => r.selector.includes('.turn-footer[data-severity="clean"]:not')),
    ).toEqual([]);
  });

  it("keeps the in-flight marks on the TAB STRIP's ink", () => {
    // One fact, one violet. Both of these used to take `--c-accent`, a
    // near-neighbour of the tab dot's `--c-dot-working`, so "a turn is running"
    // carried two colours depending on which surface you read it from. The two
    // marks are asserted together because that agreement is the whole point —
    // repointing one and not the other would leave the transcript disagreeing
    // with itself.
    const dot = ruleContaining(turns, '.turn-header[data-severity="running"] .turn-dot');
    expect(dot.body).toContain("var(--c-dot-working)");
    expect(dot.body).not.toContain("--c-accent");

    const marker = ruleContaining(turns, '.rail-marker[data-severity="running"]');
    expect(marker.body).toContain("var(--c-dot-working)");
    expect(marker.body).not.toContain("--c-accent");
  });

  it("keeps the header dot breathing, since motion is its second channel", () => {
    // The dot renders only below 48rem, where the tab strip is off-canvas, so it
    // is the off-screen-work case the pulsing-dot ruling carves out
    // (13-messages.css). Motion is also what separates `running` from a still
    // outcome without relying on hue.
    const dot = ruleContaining(turns, '.turn-header[data-severity="running"] .turn-dot');
    expect(dot.body).toMatch(/opacity:\s*calc\(1 - var\(--vk-beat\)/u);
  });
});

// ---------------------------------------------------------------------------
// THE MISSING DIRECTION. Everything above would have passed on the defective
// stylesheet too, because a per-outcome rule sitting behind the partition still
// leaves the partition's own rules in place. What the old file could not ask is
// whether anything OVERRIDES it.
// ---------------------------------------------------------------------------

/** Every `[data-outcome="…"]` colour rule left in the sheet, as
 *  (outcome, selector, body) triples. A COLOUR rule: `data-outcome` legitimately
 *  keys other things (the `[data-outcome]`-free trigger and fold attributes are a
 *  different vocabulary), so the sweep is scoped to the three properties the
 *  partition owns. */
function outcomeColourRules(): { outcome: string; selector: string; body: string }[] {
  const out: { outcome: string; selector: string; body: string }[] = [];
  for (const rule of allRules(turns)) {
    if (!/(background|border-color|color)\s*:/u.test(rule.body)) {
      continue;
    }
    for (const member of rule.selector.split(",").map((s) => s.trim())) {
      const hit = /\[data-outcome="([a-z_]+)"\]/u.exec(member);
      if (hit?.[1] !== undefined) {
        out.push({ outcome: hit[1], selector: member, body: rule.body });
      }
    }
  }
  return out;
}

describe("no hue may be set per OUTCOME behind the severity partition", () => {
  it("carries no colour rule for any outcome the table grades broken", () => {
    // THE CASE THAT WOULD HAVE CAUGHT THE REPORTED DEFECT. Before the fix
    // `.turn-footer[data-outcome="interrupted"] .turn-ledger-glyph` set a yellow
    // fill on an outcome `severityOf` grades `broken`, and every forward-direction
    // assertion in this file stayed green.
    const broken = OUTCOMES.filter((o) => severityOf(o) === "broken");
    expect(broken, "the population this case is about").toEqual([
      "interrupted",
      "refused",
      "failed",
    ]);
    const offenders = outcomeColourRules().filter((r) => broken.includes(r.outcome as TurnOutcome));
    expect(
      offenders.map((r) => r.selector),
      "a broken outcome's hue comes from data-severity and nowhere else",
    ).toEqual([]);
  });

  it("leaves `unknown` as the ONLY per-outcome colour rule, and states its ink", () => {
    // The one stated exception, and it is an exception because the table cannot
    // express it: `unknown` and `cancelled` are both `stopped`, and a pure partition
    // would paint an unreadable end the same yellow a user's own cancel gets. That
    // overturns a ruling this stylesheet already made — an end vibekit could not read
    // has no honest hue.
    //
    // FIVE rules, one per surface, which is the cost of keeping it. Uniform for the
    // first time: `unknown` was neutral on three surfaces, ABSENT on the footer wash
    // (so its footer was byte-identical to a clean turn's) and yellow on the notice.
    const rules = outcomeColourRules();
    expect(
      [...new Set(rules.map((r) => r.outcome))].sort(),
      "unknown is the only outcome with a colour rule of its own",
    ).toEqual(["unknown"]);

    const bySelector = new Map(rules.map((r) => [r.selector, r.body]));
    expect(
      [...bySelector.keys()].sort(),
      "one unknown override per surface, all five named",
    ).toEqual([
      '.rail-cluster[data-outcome="unknown"]',
      '.rail-marker[data-outcome="unknown"]',
      '.turn-footer[data-outcome="unknown"]',
      '.turn-footer[data-outcome="unknown"] .turn-ledger-glyph',
      '.turn-header[data-outcome="unknown"] .turn-dot',
      '.turn-notice[data-outcome="unknown"]',
    ]);

    // Each carries the NEUTRAL ink, except the wash, which restates the base
    // `.turn-footer` background — `unknown` keeps today's untinted footer rather
    // than gaining a neutral tint, which would be a fresh visual decision inside a
    // hue-consistency fix.
    for (const [selector, body] of bySelector) {
      if (selector === '.turn-footer[data-outcome="unknown"]') {
        expect(body).toMatch(/background:\s*var\(--c-bg-tertiary\)/u);
        continue;
      }
      expect(body, `${selector} paints the neutral ink`).toMatch(/var\(--c-text-tertiary\)/u);
    }
  });

  it("places the rail's unknown override BEFORE the selection fill", () => {
    // Both are (0,2,0), so source order is the whole tiebreak — and the wrong order
    // is silent: the rail would simply stop marking the reader's position on an
    // unknown turn, which is the only surface that says where they are.
    const rules = allRules(turns).map((r) => r.selector);
    const override = rules.findIndex((s) => s.includes('.rail-marker[data-outcome="unknown"]'));
    const fill = rules.findIndex((s) => s.includes(".rail-marker[data-current]"));
    expect(override, "the unknown override exists").toBeGreaterThan(-1);
    expect(fill, "the selection fill exists").toBeGreaterThan(-1);
    expect(override, "unknown must not outrank the selection fill").toBeLessThan(fill);
  });

  it("places each other unknown override AFTER its surface's severity rules", () => {
    // Same tie, other direction: a (0,3,0) `unknown` override written BEFORE the
    // (0,3,0) severity arm would lose, and the exception would silently stop
    // applying.
    const rules = allRules(turns).map((r) => r.selector);
    const idx = (needle: string): number => {
      const at = rules.findIndex((s) => s.includes(needle));
      expect(at, `${needle} exists`).toBeGreaterThan(-1);
      return at;
    };
    expect(idx('.turn-header[data-outcome="unknown"] .turn-dot')).toBeGreaterThan(
      idx('.turn-header[data-severity="stopped"] .turn-dot'),
    );
    expect(idx('.turn-footer[data-outcome="unknown"]')).toBeGreaterThan(
      idx('.turn-footer[data-severity="stopped"]'),
    );
    expect(idx('.turn-footer[data-outcome="unknown"] .turn-ledger-glyph')).toBeGreaterThan(
      idx('.turn-footer[data-severity="stopped"] .turn-ledger-glyph'),
    );
    expect(idx('.turn-notice[data-outcome="unknown"]')).toBeGreaterThan(
      idx('.turn-notice[data-severity="stopped"]'),
    );
  });

  it("keeps the marker's words as the channel colour cannot carry", () => {
    // `unknown` and `completed` share an ink by decision, so the separation has to
    // live somewhere colour-blind and screen-reader users can reach. It is
    // `rail-labels.ts`, pinned in its own suite; this case is the cross-reference —
    // the stylesheet may share an ink ONLY because the label does not.
    expect(restingMarkerRule(), "the resting ink is the premise of this case").toMatch(
      /color:\s*var\(--c-text-tertiary\)/u,
    );
    const rule = ruleContaining(turns, '.rail-marker[data-outcome="unknown"]');
    expect(rule.selector).toContain('.rail-cluster[data-outcome="unknown"]');
  });

  it("grades every outcome, so the sweep above is a partition rather than a sample", () => {
    // What makes OUTCOMES total: an eighth member added to the generated union with
    // no entry here would silently drop out of every case in this file.
    const graded = new Map<TurnSeverity, TurnOutcome[]>();
    for (const o of OUTCOMES) {
      const s = severityOf(o);
      graded.set(s, [...(graded.get(s) ?? []), o]);
    }
    expect(Object.fromEntries(graded)).toEqual({
      running: ["running"],
      clean: ["completed"],
      stopped: ["cancelled", "unknown"],
      broken: ["interrupted", "refused", "failed"],
    });
  });
});

// ---------------------------------------------------------------------------
// The turn's own failure notice: the durable inline surface that did not exist.
//
// A failed turn used to render a red footer mark, a lead word and an empty body,
// with the cause reaching the reader through a 12-second toast alone. The notice is
// what closes that, and it is a CARD-level sibling rather than part of the face
// precisely so an OPEN turn shows it — a broken turn is the one turn that never
// auto-folds, so a face-only surface was unreachable exactly when it was needed.
// ---------------------------------------------------------------------------

describe("the failure notice", () => {
  it("is styled at all, and reads as prose rather than a badge", () => {
    const rule = ruleContaining(turns, ".turn-notice");
    // pre-wrap because the text is upstream prose that may carry its own newlines,
    // and no clamp for the face's own reason: the fold hides a turn's WORK, never
    // what it has to say.
    expect(rule.body).toMatch(/white-space:\s*pre-wrap/u);
    expect(rule.body).not.toMatch(/-webkit-line-clamp/u);
  });

  it("tints by severity, so it never calls a cancel a failure", () => {
    // Red is the default because `broken` is the population it exists for; the
    // `stopped` pair takes the same yellow their footer glyph takes — minus
    // `unknown`, whose own override is asserted with the other four above.
    expect(ruleContaining(turns, ".turn-notice").body).toMatch(/color:\s*var\(--c-red\)/u);
    expect(ruleContaining(turns, '.turn-notice[data-severity="stopped"]').body).toMatch(
      /color:\s*var\(--c-yellow\)/u,
    );
  });

  it("is not inside the collapsed face, which is what makes it reach an open turn", () => {
    // The whole structural point, asserted against the SELECTOR: a rule scoped
    // under `.turn-face` would be unreachable for an unfolded card, because
    // `syncTurnFace` early-returns for one.
    const rule = ruleContaining(turns, ".turn-notice");
    expect(rule.selector).not.toContain(".turn-face ");
  });
});
