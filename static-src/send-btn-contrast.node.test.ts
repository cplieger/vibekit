// THE SEND BUTTON'S CONTRAST FLOORS, measured rather than asserted about.
//
// The colour maths lives in scripts/css-contrast.py — sRGB decode, OKLCH
// conversion, color-mix, the WCAG ratio — and this test shells out to its `pair`
// subcommand rather than reimplementing any of it in TypeScript. A second
// implementation would be a second thing to be wrong, and the numbers in
// 15-input.css's comments were measured with the first one; a floor asserted
// against a different engine can drift from the comment it is meant to protect.
//
// WHAT IS BEING PINNED, and why it is not the obvious pair. The button's fill is
// the requested colour (the light theme's accent character in dark mode, a light
// one in light mode) with --c-text-primary on it, and that request puts the ink
// and the bar on the SAME side of the fill in both themes — so the fill cannot
// carry 3:1 against the bar AND 4.5:1 for the ink. Measured, the windows for the
// three state hues do not even overlap within one theme. The EDGE carries the
// separation instead, which is what WCAG 1.4.11 asks for: a 3:1 boundary, not a
// 3:1 fill. So the floors here are ink-on-fill >= 4.5 at every depth of the
// ladder, and edge-vs-bar >= 3.
//
// Node environment: this runs a process and reads TSV.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { ruleBody } from "./__test-helpers__/css-rules.js";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

/** The stylesheet, read with `node:fs` rather than through the shared helper's
 *  `?raw` glob. Measured: in vitest's NODE project a `*.css?raw` import resolves
 *  to the EMPTY STRING — Vite's CSS pipeline claims the module for the
 *  server-side environment, where a stylesheet is a side effect with no value —
 *  while `*.ts?raw` and every other extension resolve normally, and `*.css?raw`
 *  in the browser project resolves normally too. This is the only node-project
 *  test that needs a stylesheet, so it reads its own; `ruleBody` above is the
 *  shared parser and needs no import of the file. */
function loadCSS(name: string): string {
  return readFileSync(join(here, "css", name), "utf8");
}

/** The faces and the depth ladder are READ OUT OF THE STYLESHEET, not restated
 *  here. A floor asserted against numbers the test carries itself would keep
 *  passing after someone changed the CSS to a depth that violates it, which is
 *  the one failure a floor exists to prevent. */
function sendBtn(): { hues: string[]; depths: number[] } {
  const body = ruleBody(loadCSS("15-input.css"), ".send-btn").replace(/\/\*[\s\S]*?\*\//g, " ");
  const hues = [...body.matchAll(/--send-hue:\s*var\((--c-[\w-]+)\)/g)].map((m) => m[1]!);
  const depths = [...body.matchAll(/--send-depth:\s*([\d.]+)%/g)].map((m) => Number(m[1]!));
  expect(
    hues.length,
    ".send-btn must declare a hue for its resting face and one per state",
  ).toBeGreaterThanOrEqual(3);
  expect(depths.length, ".send-btn must declare a resting depth plus hover and press").toBe(3);
  return { hues: [...new Set(hues)], depths };
}

/** The bar the button sits in: .prompt-box's own background. */
const BAR = "--c-bg-secondary";

const fill = (hue: string, depth: number): string =>
  `color-mix(in oklch, var(${hue}) ${String(depth)}%, var(--c-bg-primary))`;

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

// The script lives outside static-src, and Stryker's sandbox copies static-src
// alone — so its absence is a skip, the same rule the index.html readers use.
describe.skipIf(!existsSync(script))("the send button's contrast", () => {
  it("holds the icon to 4.5:1 on its fill, at every depth and in both themes", () => {
    const { hues, depths } = sendBtn();
    const misses: string[] = [];
    for (const hue of hues) {
      for (const depth of depths) {
        for (const m of pair("--c-text-primary", fill(hue, depth))) {
          if (m.ratio < 4.5) {
            misses.push(
              `${m.theme} ${hue} @${String(depth)}%: ink ${m.fg} on fill ${m.bg} is ${String(m.ratio)}:1`,
            );
          }
        }
      }
    }
    expect(
      misses,
      "the glyph is --c-text-primary on every face, so WCAG 1.4.3's 4.5:1 has to hold on the resting " +
        "fill AND on both deeper steps — the press is where it binds (dark failed, the lightest hue " +
        "washed least far toward the page).",
    ).toEqual([]);
  });

  it("holds the edge to 3:1 against the bar in every state and both themes", () => {
    const { hues } = sendBtn();
    const misses: string[] = [];
    for (const hue of hues) {
      for (const m of pair(hue, BAR)) {
        if (m.ratio < 3.0) {
          misses.push(`${m.theme} ${hue}: edge ${m.fg} vs bar ${m.bg} is ${String(m.ratio)}:1`);
        }
      }
    }
    expect(
      misses,
      "the edge is the ONLY thing separating this button from the bar it sits in — the requested fill " +
        "measures 1.46:1 dark / 1.50:1 light against it — so if the edge drops under WCAG 1.4.11's 3:1 " +
        "the control has no boundary at all.",
    ).toEqual([]);
  });

  it("draws its edge from the same hue as its fill", () => {
    // The floor above measures the HUE against the bar, so it only means anything
    // while the border is that hue. If the border were a swatch of its own, the
    // measurement and the paint would be two different things.
    const body = ruleBody(loadCSS("15-input.css"), ".send-btn").replace(/\/\*[\s\S]*?\*\//g, " ");
    expect(body, "the border must be the state hue at full strength").toMatch(
      /border:\s*1px\s+solid\s+var\(--send-hue\)/,
    );
    expect(body, "the fill must be that same hue washed over the page").toMatch(
      /background:\s*color-mix\(in oklch, var\(--send-hue\) var\(--send-depth\), var\(--c-bg-primary\)\)/,
    );
  });

  it("keeps the fill's own separation on the record, so a regression is visible", () => {
    // NOT a floor: this is the measured cost of building the colours that were
    // asked for, recorded so that anyone who widens the ramp sees the number move
    // instead of discovering it by eye. If it ever reaches 3:1 the edge stops
    // being load-bearing and the comment in 15-input.css is out of date.
    const resting = pair(fill("--c-accent", sendBtn().depths[0]!), BAR);
    const byTheme = new Map(resting.map((m) => [m.theme, m.ratio]));
    expect(byTheme.get("dark")).toBeCloseTo(1.462, 2);
    expect(byTheme.get("light")).toBeCloseTo(1.501, 2);
  });

  it("reports a miss through its exit status, so the floor cannot pass vacuously", () => {
    // The floors above read a ratio and compare it here. This one checks the tool
    // itself still fails a pair it should fail — otherwise a broken `pair`
    // (a bad expression silently resolving to the backdrop, say) would make every
    // assertion above trivially true.
    expect(() =>
      execFileSync("python3", [script, "pair", "--c-text-tertiary", "--c-bg-elevated", "4.5"], {
        encoding: "utf8",
      }),
    ).toThrow();
  });
});
