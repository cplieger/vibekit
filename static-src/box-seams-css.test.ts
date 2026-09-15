// EVERY BOX SEAM IN A TRANSCRIPT, under the one rule vibekit-ui.md "Seams" states:
// a seam is marked by a FILL step or by a 1px rule, never both.
//
//   - a step (a band over the body, a well below a header) → no rule
//   - regions sharing one fill → the rule is the only boundary, and it stays
//
// A TABLE rather than a set of cases: the population is what has to be right, and
// a new box added on either side of the split without a row here is the drift this
// guards. Both halves of each row are asserted, because either alone passes for the
// wrong reason — the rule's absence alone is satisfied by a box that stopped
// painting its band, and the fill step alone says nothing about whether somebody
// put the line back.
//
// SOURCE facts, read off the assembled stylesheet, not computed style. What a future
// editor changes is a DECLARATION, and `.tool-call` carries `content-visibility:
// auto`, so a card's own box is its `contain-intrinsic-size` fallback until Chromium
// decides it is near the viewport (tool-group-height.test.ts records the trap in
// full) — measuring these by layout means fighting that for no gain, since the
// question is which selector declares what.
import { describe, expect, it } from "vitest";

import { loadCSS, ruleBody } from "./__test-helpers__/css-rules.js";

/** A seam, and which side of the split it is on.
 *
 *  `owner` is the selector that draws (or would draw) the rule, spelled exactly as
 *  the source spells it — `ruleBody` matches the selector line, so a reworded
 *  selector fails loudly here rather than silently matching nothing.
 *
 *  `fillOwner` is the region below the seam. `step: true` means it declares a fill of
 *  its OWN, so the boundary is visible without a rule; `step: false` means it
 *  declares none and shows its card's, so the rule is all there is. */
interface Seam {
  readonly name: string;
  readonly sheet: string;
  readonly owner: string;
  readonly prop: string;
  readonly fillOwner: string;
  readonly step: boolean;
  readonly why: string;
}

const SEAMS: readonly Seam[] = [
  {
    name: "turn card: header → body",
    sheet: "29-turns.css",
    owner: ".turn-header",
    prop: "border-bottom",
    fillOwner: ".turn-header",
    step: true,
    why: "the band is --c-bg-tertiary over a --c-turn-body card",
  },
  {
    name: "turn card: body → foot",
    sheet: "29-turns.css",
    owner: ".turn-footer",
    prop: "border-top",
    fillOwner: ".turn-footer",
    step: true,
    why: "the same band at the other end",
  },
  {
    name: "code block: head → pre",
    sheet: "13-messages.css",
    owner: ".code-head",
    prop: "border-block-end",
    fillOwner: ".code-head",
    step: true,
    why: "the box-rung head sits over the pre's well",
  },
  {
    name: "tool card: claim row → details",
    sheet: "14-tools.css",
    owner: ".tool-details",
    prop: "border-block-start",
    fillOwner: ".tool-details",
    step: true,
    why: "the machine-text wash is a step against the card's own fill",
  },
  {
    name: "tool group: header → row, and row → row",
    sheet: "14-tools.css",
    owner: ".tool-group-body > .tool-call",
    prop: "border-top",
    fillOwner: ".tool-group-body > .tool-call",
    step: false,
    why: "a member flattens to `background: none`, so every row shares the group's fill",
  },
  {
    name: "delegate card: header → body",
    sheet: "14-tools.css",
    owner: ".subagent-body",
    prop: "border-block-start",
    fillOwner: ".subagent-body",
    step: false,
    why: "the body declares no fill and shows the card's",
  },
  {
    name: "delegate card: body → foot",
    sheet: "14-tools.css",
    owner: ".subagent-foot",
    prop: "border-block-start",
    fillOwner: ".subagent-foot",
    step: false,
    why: "the one card foot of the three with no band of its own",
  },
  {
    name: "run card: head → body",
    sheet: "27-run-card.css",
    owner: ".run-body",
    prop: "border-block-start",
    fillOwner: ".run-body",
    step: false,
    why: "a nested header takes no band, so head and body share the box fill",
  },
  {
    name: "run card: body → foot",
    sheet: "27-run-card.css",
    owner: ".run-foot",
    prop: "border-block-start",
    fillOwner: ".run-foot",
    step: false,
    why: "the nested feet — this and the delegate's — are box-toned, only the turn's is a band",
  },
  {
    name: "run card: body → outputs",
    sheet: "27-run-card.css",
    owner: ".run-outputs",
    prop: "border-block-start",
    fillOwner: ".run-outputs",
    step: false,
    why: "a list inside the body, separating two regions of one fill",
  },
];

/** A rule's declarations, COMMENTS STRIPPED. Load-bearing rather than tidy: several
 *  of these rules carry a comment naming the very property the seam is about — the
 *  reason it is not declared — and a scanner reading the raw body finds the property
 *  name in the prose and reports the rule as drawing one. `allRules` strips them for
 *  the same reason. */
function declarations(sheet: string, selector: string): string {
  return ruleBody(loadCSS(sheet), selector).replace(/\/\*[\s\S]*?\*\//g, " ");
}

/** Whether a rule's own body declares the property with a value that paints. A
 *  nested `&` block is part of the body, which is what we want: `border-*: 0` or
 *  `none` in a state block cancels rather than draws, and neither counts. */
function draws(body: string, prop: string): boolean {
  const at = new RegExp(`(^|[;{\\s])${prop}\\s*:\\s*([^;]+)`, "g");
  for (const m of body.matchAll(at)) {
    const value = (m[2] ?? "").trim();
    if (value !== "" && value !== "0" && value !== "none") {
      return true;
    }
  }
  return false;
}

/** Whether a rule declares a fill that paints. `background: none` is the tool
 *  group's way of saying "show the box behind me", so it counts as no fill. */
function declaresFill(body: string): boolean {
  const m = /(^|[;{\s])background\s*:\s*([^;]+)/.exec(body);
  if (m === null) {
    return false;
  }
  const value = (m[2] ?? "").trim();
  return value !== "" && value !== "none" && value !== "transparent";
}

describe("a box seam draws a rule only where no fill change marks it", () => {
  const stepped = SEAMS.filter((s) => s.step);
  const flat = SEAMS.filter((s) => !s.step);

  it("has both halves of the split populated, or the table proves nothing", () => {
    // The differential for the whole file: a table that drifted to one side would let
    // every row below pass while saying nothing about the rule being tested.
    expect(stepped.length, "seams a fill change marks").toBeGreaterThan(1);
    expect(flat.length, "seams where the rule is the only boundary").toBeGreaterThan(1);
  });

  it.each(stepped)("draws no rule at $name, because $why", ({ sheet, owner, prop, fillOwner }) => {
    expect(
      draws(declarations(sheet, owner), prop),
      `${owner} in ${sheet} must not draw ${prop}`,
    ).toBe(false);
    expect(
      declaresFill(declarations(sheet, fillOwner)),
      `${fillOwner} must paint its own fill, or removing the rule lost the boundary`,
    ).toBe(true);
  });

  it.each(flat)("keeps its rule at $name, because $why", ({ sheet, owner, prop, fillOwner }) => {
    expect(draws(declarations(sheet, owner), prop), `${owner} in ${sheet} must draw ${prop}`).toBe(
      true,
    );
    expect(
      declaresFill(declarations(sheet, fillOwner)),
      `${fillOwner} must NOT paint a fill, or its rule is redundant too`,
    ).toBe(false);
  });

  it("leaves the two dashed FRAMES alone, which are not seams", () => {
    // `.turn-notice` and `.boundary` draw a dashed rule that OPENS a framed banner,
    // and each sits against a region carrying its own fill — so the rule is the only
    // boundary there is, by the same test, and it happens to be the reason the frame
    // reads as a frame. Here so a sweep over "1px rules in a transcript" does not
    // read them as part of the population above.
    expect(draws(declarations("29-turns.css", ".turn-notice"), "border-block-start")).toBe(true);
    expect(draws(declarations("13-messages.css", ".boundary"), "border-block-start")).toBe(true);
  });
});
