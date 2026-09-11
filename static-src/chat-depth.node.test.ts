// The transcript's depth ladder and its one hover recipe, in BOTH themes, because
// the claim being guarded is that the two are one ladder run in opposite
// directions. The ladder, the numbers and what a collapse looked like:
// vibekit-ui.md "Color system".
//
// Shelling out to `scripts/css-contrast.py` rather than reimplementing the colour
// maths, for the reason `rail-mark-contrast.node.test.ts` records: a second
// implementation is a second thing to be wrong.
//
// The hover POPULATION is DERIVED from the stylesheets. A hand-kept list of six
// selectors passes forever once somebody adds a seventh box header, which is the
// failure this guard exists to prevent.
//
// Node environment: this runs a process.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

/** Read with `node:fs`, not a `?raw` glob: in vitest's NODE project a `*.css?raw`
 *  import resolves to the EMPTY STRING, which would make every sweep below pass
 *  over nothing. Same reason the two sibling contrast tests read their own. */
/** The transcript's stylesheets plus the run page's, which follows the same recipe. */
const SHEETS = [
  "12-chat.css",
  "13-messages.css",
  "14-tools.css",
  "27-run-card.css",
  "29-turns.css",
  "31-exec-view.css",
] as const;

function sheet(name: string): string {
  return readFileSync(join(here, "css", name), "utf8");
}

/** Comments hold prose ABOUT rules; blanking them keeps line numbers usable. */
function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, (m) => "\n".repeat((m.match(/\n/g) ?? []).length));
}

interface Measurement {
  theme: string;
  ratio: number;
  hex: string;
  bgHex: string;
}

/** `css-contrast.py pair` for one expression against another, both themes. */
function pair(fg: string, bg: string): Record<string, Measurement> {
  const out = execFileSync("python3", [script, "pair", fg, bg], { encoding: "utf8" });
  const rows = out
    .trim()
    .split("\n")
    .map((line) => {
      const c = line.split("\t");
      return { theme: c[0] ?? "", hex: c[1] ?? "", bgHex: c[2] ?? "", ratio: Number(c[3]) };
    });
  expect(
    rows.map((r) => r.theme),
    `expected both themes for ${fg} on ${bg}`,
  ).toEqual(["dark", "light"]);
  const byTheme: Record<string, Measurement> = {};
  for (const r of rows) {
    byTheme[r.theme] = r;
  }
  return byTheme;
}

const PAGE = "var(--c-bg-primary)";
const CARD = "var(--c-turn-body)";
const BOX = "var(--c-bg-secondary)";

describe("the transcript's depth ladder is the same ladder in both themes", () => {
  it("puts the page, the turn card and a box inside it on three distinct rungs", () => {
    const cardVsPage = pair(CARD, PAGE);
    const boxVsCard = pair(BOX, CARD);
    for (const theme of ["dark", "light"]) {
      const a = cardVsPage[theme];
      const b = boxVsCard[theme];
      expect(a, theme).toBeDefined();
      expect(b, theme).toBeDefined();
      // An alias of another rung makes two of the three one colour.
      expect(
        new Set([a?.bgHex, a?.hex, b?.hex]).size,
        `${theme}: page/card/box are three colours`,
      ).toBe(3);
    }
  });

  it("places the card rung BETWEEN the page and the box, in both themes", () => {
    // Contrast ratios are exactly multiplicative along a monotonic luminance
    // chain, so composition IS the betweenness test and needs no luminance
    // implementation: a rung outside the interval exceeds the direct ratio.
    const cardVsPage = pair(CARD, PAGE);
    const boxVsCard = pair(BOX, CARD);
    const boxVsPage = pair(BOX, PAGE);
    for (const theme of ["dark", "light"]) {
      const product = (cardVsPage[theme]?.ratio ?? 0) * (boxVsCard[theme]?.ratio ?? 0);
      expect(product, `${theme}: page->card->box composes to page->box`).toBeCloseTo(
        boxVsPage[theme]?.ratio ?? 0,
        2,
      );
    }
  });

  it("makes the page-to-card step the same size in both themes", () => {
    // Equal fractions land the first step on one ratio, so this is what fails
    // when one theme's rung is retuned and the other is not.
    const m = pair(CARD, PAGE);
    const dark = m["dark"]?.ratio ?? 0;
    const light = m["light"]?.ratio ?? 0;
    expect(dark, "dark page/card step").toBeGreaterThan(1.02);
    expect(Math.abs(dark - light), `page/card step: dark ${dark} vs light ${light}`).toBeLessThan(
      0.02,
    );
  });

  it("keeps every ink the transcript puts on the card rung above AA", () => {
    for (const ink of [
      "var(--c-text-primary)",
      "var(--c-text-secondary)",
      "var(--c-text-tertiary)",
      "var(--c-accent)",
      "var(--c-link)",
      "var(--c-red)",
      "var(--c-yellow)",
      "var(--c-green)",
    ]) {
      const m = pair(ink, CARD);
      for (const theme of ["dark", "light"]) {
        expect(m[theme]?.ratio ?? 0, `${ink} on the card rung, ${theme}`).toBeGreaterThanOrEqual(
          4.5,
        );
      }
    }
  });
});

/** Every `:hover` rule in a transcript stylesheet that names a box-header class
 *  AND writes a background. Derived, so a seventh header joins the population by
 *  existing rather than by being added here. */
interface HoverRule {
  file: string;
  line: number;
  selector: string;
  value: string;
  gated: boolean;
}

function boxHeaderHovers(): HoverRule[] {
  const found: HoverRule[] = [];
  for (const file of SHEETS) {
    const css = stripComments(sheet(file));
    const ruleRe = /([^{}\n][^{}]*?:hover[^{}]*?)\{([^{}]*)\}/g;
    for (const m of css.matchAll(ruleRe)) {
      const written = (m[1] ?? "").trim();
      const body = m[2] ?? "";
      const before = css.slice(0, m.index);
      // A nested `&:hover` names nothing itself, so the class to test is the block
      // it sits in. The run page writes all of its row hovers that way.
      const selector = written.startsWith("&") ? `${owningSelector(before)} ${written}` : written;
      if (!/[.#][\w-]*-(head|header|summary|row-main|lane|d-link)\b/.test(selector)) {
        continue;
      }
      const bg = /background(?:-color|-image)?:\s*([^;]+);/.exec(body);
      if (bg === null) {
        continue;
      }
      found.push({
        file,
        line: before.split("\n").length,
        selector,
        value: (bg[1] ?? "").trim(),
        gated: enclosingAtRules(before).some((a) => a.includes("any-hover")),
      });
    }
  }
  return found;
}

/** The selector of the innermost plain block open at this offset. */
function owningSelector(before: string): string {
  let depth = 0;
  for (const line of before.split("\n").reverse()) {
    depth += (line.match(/\}/g) ?? []).length - (line.match(/\{/g) ?? []).length;
    if (depth < 0) {
      const s = line.trim();
      if (!s.startsWith("@") && !s.startsWith("&") && s.endsWith("{")) {
        return s.slice(0, -1).trim();
      }
      depth = 0;
    }
  }
  return "";
}

/** The at-rules a rule at this offset sits inside, innermost first. */
function enclosingAtRules(before: string): string[] {
  const out: string[] = [];
  let depth = 0;
  for (const line of before.split("\n").reverse()) {
    depth += (line.match(/\}/g) ?? []).length - (line.match(/\{/g) ?? []).length;
    if (depth < 0) {
      const at = /^\s*(@[a-z-]+[^{]*)\{/.exec(line);
      if (at !== null) {
        out.push((at[1] ?? "").trim());
      }
      depth = 0;
    }
  }
  return out;
}

describe("one hover recipe for every box header in a transcript", () => {
  const population = boxHeaderHovers();

  it("finds the headers it is meant to be guarding", () => {
    // A sweep that matches nothing passes every assertion below it. Ten box and
    // row surfaces write a hover background today, across the transcript and the
    // run page; the floor is what catches a regex that stopped matching.
    expect(population.length, JSON.stringify(population, null, 1)).toBeGreaterThanOrEqual(10);
  });

  it("writes the interaction wash and never a ramp rung or a hue", () => {
    // `--c-hover` for a header with no fill of its own, `--layer-hover` for one
    // with a fill to preserve. Anything else is a second recipe: an accent or
    // status mix makes hover carry identity, which the border and glyph already do.
    const allowed = new Set(["var(--c-hover)", "var(--layer-hover)"]);
    for (const r of population) {
      expect(allowed.has(r.value), `${r.file}:${r.line} ${r.selector} hovers with ${r.value}`).toBe(
        true,
      );
    }
  });

  it("gates each of them on any-hover so a tap cannot latch the wash on", () => {
    // Each of these is a disclosure trigger or a link, so the finger is still on
    // it when the gesture ends and `:hover` sticks. `any-hover` rather than
    // `hover`, per web.md: `hover` reports the primary input only.
    for (const r of population) {
      expect(
        r.gated,
        `${r.file}:${r.line} ${r.selector} is not inside @media (any-hover: hover)`,
      ).toBe(true);
    }
  });
});

describe("hint ink stays off the tinted band, on every surface that has one", () => {
  it("measures why: the band is the rung where the two-rung contract runs out", () => {
    // `--c-text-tertiary` is valid on the page and box rungs only (01-tokens.css).
    // The chat's band states this at `.turn-badge`; the run page's group head is
    // the same rung, and its duration was reading hint ink there.
    const hint = pair("var(--c-text-tertiary)", "var(--c-bg-tertiary)");
    const secondary = pair("var(--c-text-secondary)", "var(--c-bg-tertiary)");
    for (const theme of ["dark", "light"]) {
      expect(hint[theme]?.ratio ?? 0, `hint ink on the band, ${theme}`).toBeLessThan(4.5);
      expect(
        secondary[theme]?.ratio ?? 0,
        `secondary on the band, ${theme}`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("steps the run page's group-head duration up, below the selected rule", () => {
    const css = stripComments(sheet("31-exec-view.css"));
    const rule = /\.ev-group > \.ev-row-main :where\(\.ev-dur\)\s*\{([^{}]*)\}/.exec(css);
    expect(rule, "the group head's duration steps up").not.toBeNull();
    expect(/color:\s*var\(--c-text-secondary\);/.test(rule?.[1] ?? "")).toBe(true);
    // `:where()` keeps it under 70-selection.css's (0,3,0) muted-ink rule, so a
    // SELECTED head still reads `--c-selected-muted-fg`. Written as a class count
    // rather than a computed specificity: three classes here would tie and win on
    // MANIFEST order, which is the failure this guards.
    const selector = ".ev-group > .ev-row-main :where(.ev-dur)";
    const scoring = selector.replace(/:where\([^)]*\)/g, "").match(/\./g) ?? [];
    expect(scoring.length, "scores below the selected rule's three classes").toBeLessThan(3);
  });
});

describe("machine text inside a tool card gets its own surface", () => {
  it("reads the code wash rather than a ramp rung", () => {
    // A `--c-bg-*` rung here would have to be re-picked for every rung a tool
    // card can sit on; a wash off the ink holds at all of them.
    const css = stripComments(sheet("14-tools.css"));
    const rule = /\.tool-details\s*\{([^{}]*)\}/.exec(css);
    expect(rule, ".tool-details is declared").not.toBeNull();
    expect(/background:\s*var\(--c-code-bg\);/.test(rule?.[1] ?? "")).toBe(true);
  });
});
