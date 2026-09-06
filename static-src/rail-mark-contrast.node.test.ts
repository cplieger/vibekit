// THE RAIL'S POSITION MARKS, measured rather than asserted about.
//
// Two marks say "the reader is here": a marker takes the accent fill, and a CLUSTER
// containing the current turn takes a subdued derived wash, because a range of nine
// turns may not claim a single turn's emphasis. The wash arrived with no contrast
// measurement at all while every sibling rule in `29-turns.css` records its own
// ratios — so a reader could not tell whether the 9px label on it was readable, and
// neither could the next person to retune the mix.
//
// Shelling out to `scripts/css-contrast.py pair` rather than reimplementing the
// colour maths, for the reason the send button's floors already record: a second
// implementation is a second thing to be wrong, and the numbers in the stylesheet's
// comments were measured with the first one. `color-mix(in oklch, …)` is exactly
// what that script resolves, which is why the derived wash is measurable at all.
//
// EVERY EXPRESSION IS READ OUT OF THE STYLESHEET. A floor asserted against colours
// this file names itself keeps passing after someone changes the CSS to a mix that
// violates it, which is the one failure a floor exists to prevent.
//
// Node environment: this runs a process.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { ruleContaining } from "./__test-helpers__/css-rules.js";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

/** Read with `node:fs`, not the shared helper's `?raw` glob: in vitest's NODE
 *  project a `*.css?raw` import resolves to the EMPTY STRING (Vite's CSS pipeline
 *  claims the module for the server environment), which would make every sweep here
 *  pass over nothing. Same reason `send-btn-contrast.node.test.ts` reads its own. */
const turns = readFileSync(join(here, "css", "29-turns.css"), "utf8");

interface Measurement {
  theme: string;
  fg: string;
  bg: string;
  ratio: number;
}

function pair(fg: string, bg: string): Measurement[] {
  const out = execFileSync("python3", [script, "pair", fg, bg], { encoding: "utf8" });
  const rows = out
    .trim()
    .split("\n")
    .map((line) => {
      const cols = line.split("\t");
      return { theme: cols[0] ?? "", fg: cols[1] ?? "", bg: cols[2] ?? "", ratio: Number(cols[3]) };
    });
  expect(
    rows.map((r) => r.theme),
    `expected both themes for ${fg} on ${bg}`,
  ).toEqual(["dark", "light"]);
  return rows;
}

/** One declaration's value out of a rule's body, comments stripped. */
function decl(body: string, prop: string): string {
  const m = new RegExp(`(?:^|[;{\\s])${prop}:\\s*([^;]+);`).exec(
    body.replace(/\/\*[\s\S]*?\*\//g, " "),
  );
  expect(m, `${prop} is declared`).not.toBeNull();
  return (m?.[1] ?? "").trim();
}

/** The three colours of the cluster's current mark, as the stylesheet spells them. */
function clusterMark(): { fill: string; ink: string; edge: string } {
  const body = ruleContaining(turns, ".rail-cluster[data-current]").body;
  return {
    fill: decl(body, "background"),
    ink: decl(body, "color"),
    edge: decl(body, "border-color"),
  };
}

/** The marker's mark. One rule serves both `data-current` and `data-selected`, so
 *  measuring it once measures both — which is the same fact `rail-mark-css.test.ts`
 *  pins from the other side. */
function markerMark(): { fill: string; ink: string } {
  const body = ruleContaining(turns, ".rail-marker[data-current]").body;
  return { fill: decl(body, "background"), ink: decl(body, "color") };
}

/** The surface the rail hangs over. It is the PAGE rather than a raised surface,
 *  which the rows' own shared rule states: the rail sits in the gutter beside the
 *  cards, over nothing else. */
const PAGE = "var(--c-bg-primary)";

// The script lives outside static-src, and Stryker's sandbox copies static-src
// alone — so its absence is a skip, the same rule the sibling floors use.
describe.skipIf(!existsSync(script))("the rail's position marks, measured", () => {
  it("holds the marker's digit to 4.5:1 on its fill, in both themes", () => {
    // The label is an 11px mono digit, so WCAG 1.4.3's 4.5:1 is the floor that
    // applies — 3:1 is for text at 18.66px bold or 24px regular, which this is not.
    const { fill, ink } = markerMark();
    for (const m of pair(ink, fill)) {
      expect(m.ratio, `${m.theme}: ${ink} on ${fill}`).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("holds the cluster's label to 4.5:1 on its derived wash, in both themes", () => {
    // THE MEASUREMENT THE REVIEW ASKED FOR. The cluster's label is 9px — the
    // smallest text in the app — on a `color-mix` nothing had measured, so this is
    // the floor that was being taken on trust.
    const { fill, ink } = clusterMark();
    for (const m of pair(ink, fill)) {
      expect(m.ratio, `${m.theme}: ${ink} on ${fill}`).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("holds the cluster's edge to 3:1 against the page it hangs over", () => {
    // WCAG 1.4.11: the boundary carries 3:1, not the fill. That division of labour
    // is deliberate here — the wash is subdued ON PURPOSE, so the border is what
    // makes the marked cluster a distinguishable object.
    const { edge } = clusterMark();
    for (const m of pair(edge, PAGE)) {
      expect(m.ratio, `${m.theme}: edge ${edge} vs page`).toBeGreaterThanOrEqual(3.0);
    }
  });

  it("keeps the wash's own faintness on the record, so a widening is visible", () => {
    // NOT a floor: a mark for a RANGE is supposed to be quieter than a mark for a
    // turn, so this number is deliberately under 3:1 and the edge above is what
    // carries the boundary. Recorded so that anyone deepening the mix sees the
    // number move rather than discovering by eye that a cluster now shouts as loudly
    // as a marker. The figures are the ones written at the rule.
    const { fill } = clusterMark();
    const byTheme = new Map(pair(fill, PAGE).map((m) => [m.theme, m.ratio]));
    expect(byTheme.get("dark")).toBeCloseTo(1.354, 2);
    expect(byTheme.get("light")).toBeCloseTo(1.397, 2);
  });

  it("keeps the cluster's mark quieter than the marker's", () => {
    // The relation the design decision rests on, stated as a comparison rather than
    // as two absolute numbers: whatever either mark is retuned to, a range must not
    // claim a single turn's emphasis. Measured against the page, so "louder" means
    // "further from the surface it sits on".
    const marker = new Map(pair(markerMark().fill, PAGE).map((m) => [m.theme, m.ratio]));
    const cluster = new Map(pair(clusterMark().fill, PAGE).map((m) => [m.theme, m.ratio]));
    for (const theme of ["dark", "light"]) {
      expect(cluster.get(theme), `${theme}: cluster wash vs marker fill`).toBeLessThan(
        marker.get(theme) ?? 0,
      );
    }
  });
});
