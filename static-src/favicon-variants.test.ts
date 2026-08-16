// Presence guard for the attention favicon variants (static/favicon-*.svg).
//
// The attention system swaps every `link[rel~="icon"]` to a pre-rendered variant
// when a background chat holds an unacknowledged cue. A MISSING variant is not a
// missing feature: the link gets an href that 404s, so the tab icon goes blank
// with nothing logged anywhere. Nothing else in this repo asserts that a static
// asset exists, so this is the whole guard, and it has to hold whether or not the
// icon sink is wired yet.
//
// The names are the convention @cplieger/web-terminal-ui's `iconVariantHref`
// rewrites to and `scripts/gen-attention-icons.py` writes: the `favicon` token of
// the filename gains `-<variant>`, extension preserved. Three variants for four
// cues, because `crashed` and `failed` both render as `alert`.
//
// Regenerate with:
//   python3 scripts/gen-attention-icons.py --app vibekit --static ../vibekit/static
// from a web-terminal-ui checkout, which reads the base icon and appends one dot
// rather than redrawing anything.
//
// Skipped under Stryker: its sandbox copies static-src only (ignorePatterns
// excludes ../static), so the real assets are absent there.

import { describe, it, expect } from "vitest";

/** One entry per cue, and each field pins a different link in the chain the
 *  generator walks.
 *
 *  `token` + `oklch` are what the generator is HANDED: vibekit has no --status-*
 *  family, so its APPS entry names these three tab-dot tokens by their oklch
 *  parameters (gen-attention-icons.py, in the web-terminal-ui checkout). `fill` is
 *  what it WRITES — the sRGB hex those parameters resolve to, which is the only
 *  form an SVG fill can carry.
 *
 *  Pinning both ends is what makes a colour drift fail here rather than ship: an
 *  asset whose dot took another cue's colour fails the fill assertion, and a theme
 *  edit to one of the tokens fails the oklch assertion, which is the reminder to
 *  re-run the generator so the tab icon and the tab dot still agree. The
 *  conversion itself is deliberately NOT reimplemented here — porting a colour
 *  space into a test would make the test the thing most likely to be wrong. */
const CUES = {
  input: { token: "--c-yellow", oklch: "oklch(91.9% 0.07 86.5deg)", fill: "#f9e2af" },
  done: { token: "--c-green", oklch: "oklch(85.8% 0.109 142.7deg)", fill: "#a6e3a1" },
  alert: { token: "--c-red", oklch: "oklch(75.6% 0.13 2.8deg)", fill: "#f38ba8" },
} as const;

const VARIANTS = ["input", "done", "alert"] as const;

/** The generator's geometry constants, in the 32-unit space it declares them in:
 *  a dot of radius DOT_R with PAD of clear space above and right of it. Every
 *  output scales from this, so the ratios below hold whatever an app's viewBox is
 *  (vibekit's is 48). */
const DOT_R = 5.5;
const PAD = 3;
const UNIT = 32;

async function staticDir(): Promise<string> {
  const { dirname, join } = await import("node:path");
  const { fileURLToPath } = await import("node:url");
  return join(dirname(fileURLToPath(import.meta.url)), "..", "static");
}

async function readStatic(name: string): Promise<string | null> {
  const { existsSync, readFileSync } = await import("node:fs");
  const { join } = await import("node:path");
  const path = join(await staticDir(), name);
  return existsSync(path) ? readFileSync(path, "utf8") : null;
}

describe("attention favicon variants", () => {
  it("ships one variant per cue icon, named the way the swap rewrites hrefs", async () => {
    const base = await readStatic("favicon.svg");
    if (base === null) {
      return; // Stryker sandbox: ../static is not copied in.
    }
    for (const variant of VARIANTS) {
      const svg = await readStatic(`favicon-${variant}.svg`);
      expect(
        svg,
        `static/favicon-${variant}.svg is missing: the tab icon would 404`,
      ).not.toBeNull();
    }
  });

  it("derives each variant from the base icon plus exactly one dot", async () => {
    const base = await readStatic("favicon.svg");
    if (base === null) {
      return;
    }
    const [head, , tail] = [
      base.slice(0, base.lastIndexOf("</svg>")),
      "</svg>",
      base.slice(base.lastIndexOf("</svg>") + "</svg>".length),
    ];
    for (const variant of VARIANTS) {
      const svg = await readStatic(`favicon-${variant}.svg`);
      if (svg === null) {
        continue; // reported by the presence case above
      }
      // A variant is the app's own icon pixel-for-pixel plus a status dot. Any
      // other difference means it was hand-edited and has drifted from the base.
      expect(svg.startsWith(head), `favicon-${variant}.svg diverges from favicon.svg`).toBe(true);
      expect(svg.endsWith(`</svg>${tail}`)).toBe(true);
      const inserted = svg.slice(head.length, svg.length - `</svg>${tail}`.length);
      expect(inserted).toMatch(
        /^<circle cx="[\d.]+" cy="[\d.]+" r="[\d.]+" fill="#[0-9a-f]{6}"\/>$/,
      );
    }
  });

  it("places every variant's dot at the generator's own badge position", async () => {
    const base = await readStatic("favicon.svg");
    if (base === null) {
      return;
    }
    // The generator declares its geometry in a 32-unit space and scales it onto
    // the base's viewBox. vibekit's icon is 48, so an unscaled dot would sit
    // mid-artwork at two thirds the intended size — visible only by looking. Every
    // variant is checked, not just one: they come from one code path, so a variant
    // whose dot moved was hand-edited, and that is exactly what nothing else here
    // would notice.
    const box = /viewBox="0 0 ([\d.]+) ([\d.]+)"/.exec(base);
    expect(box).not.toBeNull();
    const side = Number(box?.[1]);
    for (const variant of VARIANTS) {
      const svg = await readStatic(`favicon-${variant}.svg`);
      if (svg === null) {
        continue; // reported by the presence case above
      }
      const dot =
        /<circle cx="([\d.]+)" cy="([\d.]+)" r="([\d.]+)" fill="#[0-9a-f]{6}"\/>(?=<\/svg>)/.exec(
          svg,
        );
      expect(dot, `favicon-${variant}.svg has no trailing status dot`).not.toBeNull();
      const cx = Number(dot?.[1]);
      const cy = Number(dot?.[2]);
      const r = Number(dot?.[3]);
      // Top-right quadrant with PAD of clear space beyond it, proportional to the
      // frame — the exact placement DOT_CX / DOT_CY / DOT_R describe at 32 units.
      expect(cx / side, `favicon-${variant}.svg dot cx`).toBeCloseTo(
        (UNIT - PAD - DOT_R) / UNIT,
        5,
      );
      expect(cy / side, `favicon-${variant}.svg dot cy`).toBeCloseTo((PAD + DOT_R) / UNIT, 5);
      expect(r / side, `favicon-${variant}.svg dot radius`).toBeCloseTo(DOT_R / UNIT, 5);
    }
  });

  it("gives each cue its own colour, so done and alert cannot be swapped", async () => {
    const base = await readStatic("favicon.svg");
    if (base === null) {
      return;
    }
    // Without this the icons carried any six-digit hex the structural cases would
    // accept, so a red "done" dot and a green "alert" dot passed — the two states a
    // glance at the tab is most expected to tell apart.
    for (const variant of VARIANTS) {
      const svg = await readStatic(`favicon-${variant}.svg`);
      if (svg === null) {
        continue;
      }
      const fill = /<circle[^>]*fill="(#[0-9a-f]{6})"\/>(?=<\/svg>)/.exec(svg);
      expect(fill?.[1], `favicon-${variant}.svg carries the wrong cue colour`).toBe(
        CUES[variant].fill,
      );
    }
    // Belt: three cues, three distinct colours. A future theme that collapsed two
    // of them into one swatch would satisfy every per-variant assertion above and
    // still leave two states indistinguishable in the tab strip.
    expect(new Set(VARIANTS.map((v) => CUES[v].fill)).size).toBe(VARIANTS.length);
  });

  it("keeps each cue's colour token where the generator reads it from", async () => {
    const { readFileSync } = await import("node:fs");
    const { dirname, join } = await import("node:path");
    const { fileURLToPath } = await import("node:url");
    const css = readFileSync(
      join(dirname(fileURLToPath(import.meta.url)), "css", "01-tokens.css"),
      "utf8",
    );
    for (const variant of VARIANTS) {
      const { token, oklch } = CUES[variant];
      // The FIRST declaration, which is the default theme's :root — that is the
      // block the generator's vibekit entry transcribed. The light-theme overrides
      // further down are deliberately out of scope: one icon serves both themes.
      const declared = new RegExp(`${token}:\\s*([^;]+);`).exec(css);
      expect(declared?.[1]?.trim(), `${token} moved: re-run gen-attention-icons.py`).toBe(oklch);
    }
  });

  it("has one icon link for the swap to rewrite, pointing at the base", async () => {
    const html = await readStatic("index.html");
    if (html === null) {
      return;
    }
    // The variant set is derived from this markup: one `link[rel~="icon"]` means
    // one base. apple-touch-icon is deliberately not matched by `rel~="icon"` —
    // the OS caches that one at install time, so a swap cannot reach it.
    const icons = [...html.matchAll(/<link\s+rel="icon"[^>]*href="([^"]+)"/g)].map((m) => m[1]);
    expect(icons).toEqual(["/favicon.svg"]);
  });
});
