// THE RAIL'S POSITION MARKS, measured rather than asserted about.
//
// ONE mark says "the reader is here": the marker takes the accent fill, addressed
// through either `data-current` or `data-selected`, and its 11px digit sits on that
// fill. Two floors apply and they answer different questions — WCAG 1.4.3 for the
// digit, 1.4.11 for the filled box as an object against the page it hangs over.
//
// Shelling out to `scripts/css-contrast.py pair` rather than reimplementing the
// colour maths, for the reason the send button's floors already record: a second
// implementation is a second thing to be wrong, and the numbers in the stylesheet's
// comments were measured with the first one.
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

  it("holds the marker's fill to 3:1 against the page it hangs over", () => {
    // WCAG 1.4.11: the filled marker is a graphical object a reader has to be able to
    // pick out of the column, and the page is what it sits on — the rail hangs in the
    // gutter beside the cards, over nothing else.
    const { fill } = markerMark();
    for (const m of pair(fill, PAGE)) {
      expect(m.ratio, `${m.theme}: fill ${fill} vs page`).toBeGreaterThanOrEqual(3.0);
    }
  });
});
