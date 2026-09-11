// ---------------------------------------------------------------------------
// A clamp's line count is ONE fact written in TWO languages.
//
// Every show-more in this app is two numbers: the count a TypeScript constant
// hands `attachClamp`, which measures the pre-layout character fallback against
// it, and the count the stylesheet declares, which does the actual clipping.
// Until this file the only thing linking them was a comment, so moving one side
// alone was silent — a stylesheet clamping tighter than the constant claims
// withdraws the opener from text that is genuinely clipped, and one clamping
// looser leaves an opener over nothing to reveal.
//
// A SOURCE test rather than a rendered one, because both counts are authored
// literals: `getComputedStyle` on a clamped element reports a resolved pixel
// height, which cannot answer whether the two authored counts agree.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";

import { loadCSS, ruleBody } from "./__test-helpers__/css-rules.js";
import execPageSrc from "./exec-view/page.ts?raw";
import steerNoteSrc from "./fundamentals/steer-note.ts?raw";
import pendingSteersSrc from "./pending-steers.ts?raw";
import runInputSrc from "./run-input.ts?raw";
import userInputSrc from "./user-input.ts?raw";

/** Every shipped stylesheet, for the exhaustiveness sweep. `css-rules.ts` exports
 *  no sheet map, so the sweep needs its own eager glob — the pattern
 *  `spin-period.test.ts` uses for the same reason. */
const sheets = import.meta.glob<string>("./css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

/** A top-level clamp rule, by the attribute `attachClamp` toggles. */
const CLAMPED_RULE = /^(\.[a-z-]+\[data-clamped\])\s*\{/gm;

interface ClampPair {
  /** The surface a reader sees, so a failure names the thing rather than the file. */
  readonly what: string;
  readonly tsFile: string;
  readonly tsSrc: string;
  readonly constant: string;
  readonly sheet: string;
  readonly selector: string;
}

// No per-pair PROPERTY name: the CSS reader is mechanism-agnostic, mirroring
// `attachClamp` itself, which decides by measurement and never reads the
// declaration.
//
// THE TURN HEADER'S REQUEST IS DELIBERATELY ABSENT. Its clamp is CSS-only and
// FOLD-conditional (`.turn[data-folded] .turn-req-text`, 29-turns.css), so there
// is no constant to pair it against and no `[data-clamped]` rule for the sweep
// below to find — the contract this file states cannot express a one-language
// clamp, and widening `ClampPair` to make one representable would give the sweep
// a row it can never check. The count is asserted where it can be, against real
// layout, in `disclosure-row-css.test.ts`.
const PAIRS: readonly ClampPair[] = [
  {
    what: "the run page's instructions",
    tsFile: "exec-view/page.ts",
    tsSrc: execPageSrc,
    constant: "CLAMP",
    sheet: "31-exec-view.css",
    selector: ".ev-in-text[data-clamped]",
  },
  {
    what: "the run page's results",
    tsFile: "exec-view/page.ts",
    tsSrc: execPageSrc,
    constant: "RESULT_CLAMP",
    sheet: "31-exec-view.css",
    selector: ".ev-r-text[data-clamped]",
  },
  {
    what: "a steer note in the transcript",
    tsFile: "fundamentals/steer-note.ts",
    tsSrc: steerNoteSrc,
    constant: "CLAMP_LINES",
    sheet: "13-messages.css",
    selector: ".steer-note-text[data-clamped]",
  },
  {
    what: "a steer row in the dock",
    tsFile: "pending-steers.ts",
    tsSrc: pendingSteersSrc,
    constant: "DOCK_CLAMP_LINES",
    sheet: "26-dock.css",
    selector: ".steer-text[data-clamped]",
  },
  {
    what: "a parked workflow step's question",
    tsFile: "run-input.ts",
    tsSrc: runInputSrc,
    constant: "CLAMP_LINES",
    sheet: "26-dock.css",
    selector: ".run-input-question[data-clamped]",
  },
  {
    what: "the agent's own question",
    tsFile: "user-input.ts",
    tsSrc: userInputSrc,
    constant: "CLAMP_LINES",
    sheet: "26-dock.css",
    selector: ".user-input-question[data-clamped]",
  },
];

function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/^\s*\/\/.*$/gm, " ");
}

/** The line count a clamp constant declares. Two shapes in the tree — an options
 *  object (`{ lines: 3, fallbackChars: 220 }`) and a bare count
 *  (`const CLAMP_LINES = 4`) — so both are matched here rather than in the table.
 *
 *  Comments are stripped first because two of these docs quote their own number in
 *  prose ("Twelve lines because…", "Four rather than the turn header's three"). */
function tsClampLines(pair: ClampPair): number {
  const code = stripComments(pair.tsSrc);
  // `\s+CLAMP` cannot match inside `RESULT_CLAMP` (the preceding character is a
  // word character, so there is no whitespace to consume), which is what keeps the
  // two exec-view constants apart.
  const re = new RegExp(
    String.raw`\bconst\s+${pair.constant}\s*=\s*(?:\{[^{}]*?\blines\s*:\s*(\d+)|(\d+)\s*;)`,
  );
  const m = re.exec(code);
  expect(m, `${pair.tsFile}: no line count found for \`${pair.constant}\``).not.toBeNull();
  return Number.parseInt(m?.[1] ?? m?.[2] ?? "", 10);
}

/** Every line count a clamp rule declares, whatever spelling it uses. Three in the
 *  tree: `-webkit-line-clamp`, its standard twin `line-clamp`, and `max-block-size`
 *  in `lh` (the results clamp, which cannot use line-clamp because a markdown
 *  bubble's children are block-level). All of them are returned, so a rule
 *  declaring the pair has both checked and cannot half-move. */
function cssClampCounts(pair: ClampPair): { prop: string; lines: number }[] {
  const body = ruleBody(loadCSS(pair.sheet), pair.selector).replace(/\/\*[\s\S]*?\*\//g, " ");
  return [
    // `(?:-webkit-)?` rather than an alternation, so `-webkit-line-clamp` cannot be
    // read as a bare `line-clamp`.
    ...body.matchAll(/((?:-webkit-)?line-clamp)\s*:\s*(\d+)/g),
    ...body.matchAll(/(max-block-size)\s*:\s*(\d+)lh/g),
  ].map((m) => ({ prop: m[1] ?? "", lines: Number.parseInt(m[2] ?? "", 10) }));
}

describe("a clamp's line count is one fact in two languages", () => {
  for (const pair of PAIRS) {
    it(`${pair.what}: the stylesheet clamps to the same count as ${pair.constant}`, () => {
      const tsLines = tsClampLines(pair);
      const declared = cssClampCounts(pair);
      expect(
        declared.length,
        `css/${pair.sheet} \`${pair.selector}\` declares no line count`,
      ).toBeGreaterThan(0);
      for (const found of declared) {
        expect(
          found.lines,
          `${pair.what}: ${pair.tsFile} \`${pair.constant}\` clamps to ${String(tsLines)} lines, ` +
            `but css/${pair.sheet} \`${pair.selector}\` declares ` +
            `${found.prop}: ${String(found.lines)}. One clamp is one count — move both or neither.`,
        ).toBe(tsLines);
      }
    });
  }

  // The premise, or the comparison above can pass on two absences: `toBe` reads
  // NaN as equal to NaN, so a regex that stopped matching would agree with itself.
  it("reads a real count from both sides of every pair", () => {
    for (const pair of PAIRS) {
      const tsLines = tsClampLines(pair);
      expect(
        Number.isInteger(tsLines) && tsLines > 0,
        `${pair.tsFile} \`${pair.constant}\` read as ${String(tsLines)}`,
      ).toBe(true);
      for (const found of cssClampCounts(pair)) {
        expect(
          Number.isInteger(found.lines) && found.lines > 0,
          `css/${pair.sheet} \`${pair.selector}\` ${found.prop} read as ${String(found.lines)}`,
        ).toBe(true);
      }
    }
  });

  // What makes the table above collectively exhaustive: a clamp site added with
  // no row fails here instead of shipping unpinned.
  it("covers every clamp rule in the shipped stylesheets", () => {
    const declared = new Set<string>();
    for (const css of Object.values(sheets)) {
      for (const m of css.replace(/\/\*[\s\S]*?\*\//g, " ").matchAll(CLAMPED_RULE)) {
        declared.add(m[1] ?? "");
      }
    }
    expect([...declared].sort(), "a new clamp site needs a row in this file's table").toEqual(
      [...new Set(PAIRS.map((p) => p.selector))].sort(),
    );
  });
});
