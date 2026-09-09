// THE COMPACTION BAND, measured rather than asserted about.
//
// The band conveys information and carries no text and no shape, so its
// separation from the track it sits on is the whole of its legibility. Measuring
// the TOKEN NAME is what keeps this honest: the script resolves it per theme out
// of `01-tokens.css`, so a retune moves these numbers rather than leaving them
// behind.

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

/** The floor on OKLab distance between the band and the nearest stroke the fill
 *  can take. Anchored to the ramp's own scale: light's green-to-yellow endpoints
 *  sit 0.1029 apart, so a band closer than that reads as a point ON the ramp
 *  rather than a different kind of mark. 0.09 is that figure with enough headroom
 *  that a mix-rounding change cannot trip it. Cleared at 0.1603 dark, 0.1217 light. */
const MIN_RAMP_SEPARATION = 0.09;

/** Every mix `context-ring.ts` can emit, ENUMERATED rather than sampled: it
 *  quantizes the mix to one decimal, so 1001 steps per segment is the whole
 *  stroke set. Measured in OKLab through the script's own conversion and its own
 *  premultiplied oklch mix, so no colour maths is restated here. */
const RAMP_SEPARATION_PY = `
import importlib.util, math, sys
spec = importlib.util.spec_from_file_location("cc", sys.argv[1])
cc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cc)
def de(a, b):
    la, aa, ba = cc.colour_to_oklab(a)
    lb, ab, bb = cc.colour_to_oklab(b)
    return math.sqrt((la - lb) ** 2 + (aa - ab) ** 2 + (ba - bb) ** 2)
for th in cc.parse_themes():
    band = th.colour("--c-context-wedge")
    worst = (9.9, "")
    for to, frm in (("--c-yellow", "--c-green"), ("--c-red", "--c-yellow")):
        for i in range(1001):
            expr = "color-mix(in oklch, var(%s) %.1f%%, var(%s))" % (to, i / 10, frm)
            d = de(band, th.resolve(expr))
            if d < worst[0]:
                worst = (d, "%s %.1f%% into %s" % (to, i / 10, frm))
    print("%s\\t%.6f\\t%s" % (th.name, worst[0], worst[1]))
`;

interface Separation {
  theme: string;
  dE: number;
  at: string;
}

/** The band's distance from the NEAREST stroke the fill can take, per theme.
 *
 *  OKLab rather than the WCAG ratio the pairs above report, because that ratio is
 *  luminance-only and the band separates from the fill on CHROMA (01-tokens.css
 *  states why): the light retune moved its worst pair 53% in OKLab and 13% in
 *  WCAG, so a WCAG floor is blind to most of the mechanism. */
function rampSeparation(): Separation[] {
  const out = execFileSync("python3", ["-c", RAMP_SEPARATION_PY, script], { encoding: "utf8" });
  const rows = out
    .trim()
    .split("\n")
    .map((line) => {
      const cols = line.split("\t");
      return { theme: cols[0] ?? "", dE: Number(cols[1]), at: cols[2] ?? "" };
    });
  expect(
    rows.map((r) => r.theme),
    "expected both themes for the band against the fill's ramp",
  ).toEqual(["dark", "light"]);
  return rows;
}

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
    // A comparison rather than a ceiling, because whatever either is retuned to, a
    // band as loud as the fill reads as already-filled.
    const band = new Map(pair(BAND, TRACK).map((m) => [m.theme, m.ratio]));
    for (const hue of FILL_HUES) {
      for (const m of pair(hue, TRACK)) {
        expect(band.get(m.theme) ?? 0, `${m.theme}: band vs ${hue} on the track`).toBeLessThan(
          m.ratio,
        );
      }
    }
  });

  it("keeps its distance from every stroke the fill can take, not just the three seeds", () => {
    // The assertion above reads the three SEEDS, and the fill is a continuous
    // ramp between them, so a mid-mix can sit arbitrarily close to a band that
    // passes it — which is how the light theme shipped a 0.0664 pair while every
    // seed measured clear.
    for (const m of rampSeparation()) {
      expect(m.dE, `${m.theme}: band vs the nearest fill stroke (${m.at})`).toBeGreaterThanOrEqual(
        MIN_RAMP_SEPARATION,
      );
    }
  });
});
