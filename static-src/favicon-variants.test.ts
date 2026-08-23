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
// rewrites to and `.kiro/scripts/gen-attention-icons.py` writes: the `favicon` token of
// the filename gains `-<variant>`, extension preserved. Three variants for four
// cues, because `crashed` and `failed` both render as `alert`.
//
// Regenerate from the workspace root with:
//   python3 .kiro/scripts/gen-attention-icons.py --app vibekit --static vibekit/static
// which reads the base icon and appends one dot rather than redrawing anything.
//
// Skipped assets under Stryker: its sandbox copies static-src only
// (ignorePatterns excludes ../static), so the real assets may be absent there.
// A GLOB rather than static imports is what preserves that: a missing key reads
// as absent, exactly as the existsSync guard it replaced did, where a static
// `?raw` import of a missing file would fail the whole module instead.

import { describe, it, expect } from "vitest";
import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

/** The shipped page and every favicon beside it, inlined as text. */
const staticAssets = import.meta.glob<string>(["../static/*.svg", "../static/index.html"], {
  query: "?raw",
  import: "default",
  eager: true,
});

/** One entry per cue, and each field pins a different link in the chain the
 *  generator walks.
 *
 *  `token` + `oklch` are what the generator is HANDED: its APPS entry names these
 *  three tab-dot tokens by their oklch parameters
 *  (.kiro/scripts/gen-attention-icons.py). `fill` is
 *  what it WRITES — the sRGB hex those parameters resolve to, which is the only
 *  form an SVG fill can carry.
 *
 *  Pinning both ends is what makes a colour drift fail here rather than ship: an
 *  asset whose dot took another cue's colour fails the fill assertion, and a theme
 *  edit to one of the tokens fails the oklch assertion, which is the reminder to
 *  re-run the generator so the tab icon and the tab dot still agree. The
 *  conversion itself is deliberately NOT reimplemented here — porting a colour
 *  space into a test would make the test the thing most likely to be wrong.
 *
 *  `state` and `shape` are the other half of the agreement: a cue stands for one
 *  TAB DOT STATE, so the assertions below read that state's own rule out of
 *  12-tabs.css rather than restating what it should be. `alert` is a diamond
 *  because `failed` is one, and `failed` is one because it and `done` were
 *  otherwise separable by hue alone.
 *
 *  The three values are web-terminal-kiro's own --status-* overrides, adopted in
 *  2026-08 (see the --c-dot-* block in 01-tokens.css). `input` and `done` are its
 *  literal declarations; `alert` keeps its hue and moves in L, because #dc2626 is
 *  invisible on this app's hovered sidebar row. */
const CUES = {
  input: {
    token: "--c-dot-input",
    oklch: "oklch(78% 0.15 95deg)",
    fill: "#d6b529",
    state: "input",
    shape: "circle",
  },
  done: {
    token: "--c-dot-done",
    oklch: "oklch(78% 0.15 150deg)",
    fill: "#67d283",
    state: "done",
    shape: "circle",
  },
  alert: {
    token: "--c-dot-failed",
    oklch: "oklch(70% 0.125 27.3deg)",
    fill: "#e27e73",
    state: "failed",
    shape: "diamond",
  },
} as const;

const VARIANTS = ["input", "done", "alert"] as const;

/** The generator's geometry constants, in the 32-unit space it declares them in:
 *  a dot of radius DOT_R with PAD of clear space above and right of it. Every
 *  output scales from this, so the ratios below hold whatever an app's viewBox is
 *  (vibekit's is 48). */
const DOT_R = 5.5;
const PAD = 3;
const UNIT = 32;

async function readStatic(name: string): Promise<string | null> {
  return Promise.resolve(staticAssets[`../static/${name}`] ?? null);
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
      // A variant is the app's own icon pixel-for-pixel plus a status badge. Any
      // other difference means it was hand-edited and has drifted from the base.
      expect(svg.startsWith(head), `favicon-${variant}.svg diverges from favicon.svg`).toBe(true);
      expect(svg.endsWith(`</svg>${tail}`)).toBe(true);
      const inserted = svg.slice(head.length, svg.length - `</svg>${tail}`.length);
      // Two shapes, one element each: a circle, or the diamond's four-vertex
      // path. Both are matched here so a cue that silently lost its shape fails
      // in the shape case below rather than passing a loose structural regex.
      expect(inserted).toMatch(
        CUES[variant].shape === "diamond"
          ? /^<path d="M[-\d. LZ]+" fill="#[0-9a-f]{6}"\/>$/
          : /^<circle cx="[\d.]+" cy="[\d.]+" r="[\d.]+" fill="#[0-9a-f]{6}"\/>$/,
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
      // Top-right quadrant with PAD of clear space beyond it, proportional to the
      // frame — the exact placement DOT_CX / DOT_CY / DOT_R describe at 32 units.
      // EVERY shape holds that one footprint, which is why a cue can change
      // silhouette without moving in the artwork: the diamond's DIAGONAL is what
      // matches the circle's diameter, the same fitting the tab dot uses for its
      // own diamond (an 8px square in a 9px slot).
      const wantCx = ((UNIT - PAD - DOT_R) / UNIT) * side;
      const wantCy = ((PAD + DOT_R) / UNIT) * side;
      const wantR = (DOT_R / UNIT) * side;
      if (CUES[variant].shape === "diamond") {
        const path = /<path d="M([-\d. LZ]+)" fill="#[0-9a-f]{6}"\/>(?=<\/svg>)/.exec(svg);
        expect(path, `favicon-${variant}.svg has no trailing status badge`).not.toBeNull();
        const pts = (path?.[1] ?? "")
          .replace(/Z$/, "")
          .split("L")
          .map((p) => p.trim().split(/\s+/).map(Number));
        expect(pts, `favicon-${variant}.svg diamond must have four vertices`).toHaveLength(4);
        expect(pts).toEqual([
          [wantCx, wantCy - wantR],
          [wantCx + wantR, wantCy],
          [wantCx, wantCy + wantR],
          [wantCx - wantR, wantCy],
        ]);
      } else {
        const dot =
          /<circle cx="([\d.]+)" cy="([\d.]+)" r="([\d.]+)" fill="#[0-9a-f]{6}"\/>(?=<\/svg>)/.exec(
            svg,
          );
        expect(dot, `favicon-${variant}.svg has no trailing status badge`).not.toBeNull();
        expect(Number(dot?.[1]), `favicon-${variant}.svg dot cx`).toBeCloseTo(wantCx, 5);
        expect(Number(dot?.[2]), `favicon-${variant}.svg dot cy`).toBeCloseTo(wantCy, 5);
        expect(Number(dot?.[3]), `favicon-${variant}.svg dot radius`).toBeCloseTo(wantR, 5);
      }
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
      const fill = /<(?:circle|path)[^>]*fill="(#[0-9a-f]{6})"\/>(?=<\/svg>)/.exec(svg);
      expect(fill?.[1], `favicon-${variant}.svg carries the wrong cue colour`).toBe(
        CUES[variant].fill,
      );
    }
    // Belt: three cues, three distinct colours. A future theme that collapsed two
    // of them into one swatch would satisfy every per-variant assertion above and
    // still leave two states indistinguishable in the tab strip.
    expect(new Set(VARIANTS.map((v) => CUES[v].fill)).size).toBe(VARIANTS.length);
  });

  it("gives each cue the silhouette its own tab dot has", async () => {
    // The icon and the dot are two renderings of ONE signal, so a cue's shape is
    // read out of the state's CSS rather than restated here. `failed` spends a
    // shape because it and `done` are both settled solid marks with only hue
    // between them, which is a WCAG 1.4.1 failure; the icon carried a circle for it
    // anyway until 2026-08, so the one pair where confusing the two matters most
    // was re-merged on the surface a user glances at without looking.
    const tabs = loadCSS("12-tabs.css");
    for (const variant of VARIANTS) {
      const { state, shape } = CUES[variant];
      const rule = ruleContaining(tabs, `.tab-status-dot[data-status="${state}"]`, "top");
      const isDiamond = /transform: rotate\(45deg\)/.test(rule.body);
      expect(
        isDiamond ? "diamond" : "circle",
        `favicon-${variant} mirrors the ${state} dot, whose rule says otherwise`,
      ).toBe(shape);
    }
  });

  it("does not claim the ring that its dot has and its icon cannot carry", async () => {
    // `input`'s tab dot is a disc inside a 2px ring at 30% alpha. At the 16px
    // rendering the whole badge is 5.5px across, so a proportional ring is 0.85px
    // at 30% over a saturated violet — invisible. It is DROPPED rather than drawn
    // thicker to be seen, because a ring that is not the dot's ring would make the
    // icon claim a fidelity it does not have. The cost is stated: on the icon
    // `input` and `done` separate by hue alone.
    //
    // This asserts the drop is deliberate. If a ring is ever added, it belongs in
    // the generator with the geometry re-derived, and this case is where the
    // decision gets revisited rather than quietly contradicted.
    const svg = await readStatic("favicon-input.svg");
    if (svg === null) {
      return;
    }
    const badges = [
      ...svg.matchAll(new RegExp(`<(?:circle|path)[^>]*fill="${CUES.input.fill}"`, "g")),
    ];
    expect(badges, "favicon-input.svg must carry exactly one input-coloured mark").toHaveLength(1);
    expect(svg).not.toContain("stroke");
  });

  it("keeps each cue's colour token where the generator reads it from", () => {
    const css = loadCSS("01-tokens.css");
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
