// THE COMPACTION BAND, measured rather than asserted about.
//
// The band is a graphical object conveying information — where KAS will
// auto-summarize — and it carries no text and no shape of its own, so WCAG
// 1.4.11's 3:1 against the track it sits on is the whole of its legibility. It
// shipped at 2.07:1 in the light theme, which is what a floor here would have
// caught: `01-tokens.css` enforces the same number on the --c-dot-* family two
// declarations below and nothing enforced it on this one.
//
// Shelling out to `scripts/css-contrast.py pair` rather than reimplementing the
// colour maths, for the reason the sibling floors record: a second implementation
// is a second thing to be wrong, and the ratios in the stylesheet's comments were
// measured with the first one. Passing the TOKEN NAME rather than a value is also
// what keeps the measurement honest — the script resolves it per theme out of
// `01-tokens.css`, so a retune moves these numbers instead of leaving them behind.
//
// Node environment: this runs a process.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

interface Measurement {
  theme: string;
  ratio: number;
}

function pair(fg: string, bg: string): Measurement[] {
  const out = execFileSync("python3", [script, "pair", fg, bg], { encoding: "utf8" });
  const rows = out
    .trim()
    .split("\n")
    .map((line) => {
      const cols = line.split("\t");
      return { theme: cols[0] ?? "", ratio: Number(cols[3]) };
    });
  expect(
    rows.map((r) => r.theme),
    `expected both themes for ${fg} on ${bg}`,
  ).toEqual(["dark", "light"]);
  return rows;
}

const BAND = "--c-context-wedge";
/** The ring's own track: the third circle is drawn over it, and the fill over that. */
const TRACK = "--c-border";
const PAGE = "--c-bg-primary";
/** The three hues the fill ramps between. */
const FILL_HUES = ["--c-green", "--c-yellow", "--c-red"];

// The script lives outside static-src, and Stryker's sandbox copies static-src
// alone — so its absence is a skip, the same rule the sibling floors use.
describe.skipIf(!existsSync(script))("the compaction band, measured", () => {
  it("clears 3:1 against the track it is drawn on, in both themes", () => {
    for (const m of pair(BAND, TRACK)) {
      expect(m.ratio, `${m.theme}: band vs track`).toBeGreaterThanOrEqual(3.0);
    }
  });

  it("clears 3:1 against the page the pill sits on, in both themes", () => {
    // The track is a wash off the ink, so the page shows through it; a band that
    // separated from the track alone could still vanish into the surface.
    for (const m of pair(BAND, PAGE)) {
      expect(m.ratio, `${m.theme}: band vs page`).toBeGreaterThanOrEqual(3.0);
    }
  });

  it("stays quieter than every hue the fill can take", () => {
    // The relation the design rests on, as a comparison rather than a ceiling: the
    // band marks a zone and the fill reports the reading, so whatever either is
    // retuned to, a band as loud as the fill reads as already-filled.
    const band = new Map(pair(BAND, TRACK).map((m) => [m.theme, m.ratio]));
    for (const hue of FILL_HUES) {
      for (const m of pair(hue, TRACK)) {
        expect(band.get(m.theme) ?? 0, `${m.theme}: band vs ${hue} on the track`).toBeLessThan(
          m.ratio,
        );
      }
    }
  });
});
