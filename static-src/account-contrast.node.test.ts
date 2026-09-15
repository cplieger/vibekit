// THE SIDEBAR FOOTER'S MEASURED FLOORS, and the one FAIL this change ships.
//
// TWO SURFACES with different backdrops, and naming which is the point:
//
//   THE TRIGGER (`.account-btn`) sits on `.sidebar-footer`, whose backdrop really is
//   `--c-bg-secondary`. Its interaction states are a translucent wash COMPOSITED over
//   that, and over `--c-bg-tertiary` when the pointer is also hovering.
//
//   THE CREDITS ROW (`.pill-account`) sits INSIDE the status card, and the card is NOT
//   that token: `.pill-status-content` paints
//   `color-mix(in srgb, var(--status-color, …) 8%, var(--c-bg-secondary))` and
//   `status.ts` writes `--status-color` for all three connection states. So every card
//   figure is taken over a status-TINTED surface, composed here the way the card
//   composes it — with each hue substituted for the custom property — or this file
//   measures the bare token and returns numbers no comment in the tree contains.
//
// THE INSTRUMENT. Every interaction backdrop is composed with the script's own
// `over(<wash>, <surface>)` primitive — ALPHA COMPOSITING IN sRGB, which is what a
// browser paints — never a `color-mix()` of the wash into the surface, which
// interpolates in oklch and returns a colour nothing paints. `css-contrast.py` ships
// that primitive for exactly this and documents it as "what a browser actually
// paints".
//
// Shelling out rather than reimplementing the colour maths, for the reason the
// sibling floors already record: a second implementation is a second thing to be
// wrong, and every number in the CSS comments was measured with the first one.
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

import { ruleBody } from "./__test-helpers__/css-rules.js";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

/** Read with `node:fs`, not the shared helper's `?raw` glob: in vitest's NODE project
 *  a `*.css?raw` import resolves to the EMPTY STRING (Vite's CSS pipeline claims the
 *  module for the server environment), which would make every sweep here pass over
 *  nothing. */
const shell = readFileSync(join(here, "css", "10-shell-app.css"), "utf8");
const input = readFileSync(join(here, "css", "15-input.css"), "utf8");

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

const ratio = (theme: string, fg: string, bg: string): number => {
  const row = pair(fg, bg).find((r) => r.theme === theme);
  expect(row, `${theme} row for ${fg} on ${bg}`).toBeDefined();
  return row?.ratio ?? 0;
};

/** One declaration's value out of a rule's body, comments stripped. */
function decl(body: string, prop: string): string {
  const m = new RegExp(`(?:^|[;{\\s])${prop}:\\s*([^;]+);`).exec(
    body.replace(/\/\*[\s\S]*?\*\//g, " "),
  );
  expect(m, `${prop} is declared`).not.toBeNull();
  return (m?.[1] ?? "").trim();
}

/** A nested state block's body out of a rule. */
function nested(body: string, selector: string): string {
  const clean = body.replace(/\/\*[\s\S]*?\*\//g, " ");
  const at = clean.indexOf(selector);
  expect(at, `${selector} is declared`).toBeGreaterThan(-1);
  const open = clean.indexOf("{", at);
  const close = clean.indexOf("}", open);
  return clean.slice(open + 1, close);
}

/** The composite a browser paints when `wash` sits on `surface`. */
const over = (wash: string, surface: string): string => `over(${wash}, ${surface})`;

/** THE CARD'S OWN BACKDROP, read out of `.pill-status-content` rather than restated,
 *  with one connection hue substituted for `--status-color`. Composed the way the card
 *  composes it, or every card figure below describes a surface nothing paints. */
function cardBackdrop(hue: string): string {
  // `ruleBody` rather than `ruleContaining`: the latter matches every rule whose
  // selector LIST contains the string, and `.pill-status-content` is a substring of
  // nothing here but `.pill-expand-content` is of `.pill-expand-content.is-open` —
  // so keying on the exact selector line is the reading that stays right.
  // Whitespace collapsed AND stripped after each opening paren: prettier wraps this
  // declaration across five lines, so the raw value carries newlines inside
  // `color-mix(` that the resolver would have to re-parse anyway.
  const bg = decl(ruleBody(input, ".pill-status-content"), "background")
    .replace(/\s+/gu, " ")
    .replace(/\(\s+/gu, "(")
    .replace(/\s+\)/gu, ")")
    .trim();
  // The declaration reads
  // `color-mix(in srgb, var(--status-color, var(--c-bg-secondary)) 8%, var(--c-bg-secondary))`,
  // and the inner `var()` carries its own fallback — so the substitution has to take
  // the WHOLE `var(--status-color, …)` call including its closing paren.
  const substituted = bg.replace(/var\(--status-color,\s*var\([^)]*\)\s*\)/u, `var(--c-${hue})`);
  expect(substituted, `the ${hue} tint substitutes cleanly`).not.toContain("--status-color");
  expect(substituted, "and it is still the card's own mix").toContain("color-mix(in srgb");
  // The parens have to balance, or the script resolves a different expression than the
  // card paints and every figure below is about a colour nothing renders.
  const opens = (substituted.match(/\(/gu) ?? []).length;
  const closes = (substituted.match(/\)/gu) ?? []).length;
  expect(opens, `the ${hue} expression's parens balance`).toBe(closes);
  return substituted;
}

/** The three connection hues, read off `.status-dot`'s own rules rather than named
 *  here — the base rule plus its two settled states. */
function markInks(): { state: string; ink: string }[] {
  const body = ruleBody(shell, ".status-dot");
  return [
    { state: "connecting", ink: decl(body, "background") },
    { state: "connected", ink: decl(nested(body, "&.connected"), "background") },
    { state: "error", ink: decl(nested(body, "&.error"), "background") },
  ];
}

const FOOTER = "var(--c-bg-secondary)";
const HOVER_TOKEN = "var(--c-hover)";
const PRESS_TOKEN = "var(--c-press)";
const HUES = ["green", "yellow", "red"] as const;

// The script lives outside static-src, and Stryker's sandbox copies static-src
// alone — so its absence is a skip, the same rule the sibling floors use.
describe.skipIf(!existsSync(script))("the trigger's floors", () => {
  it("holds all three connection marks to 3:1 at rest and hovered", () => {
    // WCAG 1.4.11: the mark is a graphical object a reader has to pick out of the row.
    // The hover fill is read out of the rule, so changing that token moves this.
    const btn = ruleBody(shell, ".account-btn");
    const hoverFill = decl(nested(btn, "&:hover"), "background");
    for (const { state, ink } of markInks()) {
      for (const [label, bg] of [
        ["rest", FOOTER],
        ["hovered", hoverFill],
      ] as const) {
        for (const m of pair(ink, bg)) {
          expect(
            m.ratio,
            `${m.theme}: the ${state} mark ${label} (${ink} on ${bg})`,
          ).toBeGreaterThanOrEqual(3);
        }
      }
    }
  });

  it("holds the address to 4.5:1 in every state, pressed included", () => {
    // WCAG 1.4.3: `--fs-sm` regular-weight text, so the large-text exception does not
    // reach it. Unlike the mark, the address clears every state — which is what makes
    // the residual below an argument about the INK rather than about the press.
    const btn = ruleBody(shell, ".account-btn");
    const hoverFill = decl(nested(btn, "&:hover"), "background");
    const ink = decl(ruleBody(shell, ".sidebar-email"), "color");
    for (const bg of [FOOTER, hoverFill, over(PRESS_TOKEN, FOOTER), over(PRESS_TOKEN, hoverFill)]) {
      for (const m of pair(ink, bg)) {
        expect(m.ratio, `${m.theme}: the address on ${bg}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});

describe.skipIf(!existsSync(script))("the pressed mark, a residual the ink ramp closed", () => {
  // This used to pin six KNOWN VALUES because the floor could not be written honestly:
  // pressed over `over(--c-press, --c-bg-secondary)`, dark red read 2.847:1, light
  // green 2.878 and light red 2.773, so the pressed mark was a recorded residual in
  // BOTH themes. Authoring every status ink against the hovered box (01-tokens.css
  // "SEEDS: ink") moved them to 3.22 / 3.65 / 3.65, so the 1.4.11 floor is the honest
  // assertion now and the residual is history rather than a table.
  //
  // `over()` is the instrument: a `color-mix()` of the wash into the surface returns
  // different numbers for a colour nothing paints.
  it.each([
    ["green", "dark"],
    ["green", "light"],
    ["yellow", "dark"],
    ["yellow", "light"],
    ["red", "dark"],
    ["red", "light"],
  ] as const)("holds the %s mark to 3:1 pressed, in %s", (hue, theme) => {
    const bg = over(PRESS_TOKEN, FOOTER);
    expect(ratio(theme, `var(--c-${hue})`, bg), `${theme} ${hue} on ${bg}`).toBeGreaterThanOrEqual(
      3,
    );
  });

  it("takes the app-wide press rather than a bespoke one, which is WHY it was a residual", () => {
    // The residual is the price of the footer's two controls answering the pointer
    // alike. Fixing it means exempting this one control from 03-base.css's universal
    // press, which is the user instruction it would break — so the absence of a
    // bespoke `:active` here IS the decision, and this is what fails if someone
    // "fixes" it locally instead of reopening that decision.
    const btn = ruleBody(shell, ".account-btn").replace(/\/\*[\s\S]*?\*\//g, " ");
    expect(btn, "no bespoke press on the trigger").not.toMatch(/&:active/u);
  });
});

describe.skipIf(!existsSync(script))("the credits row's floors, over the TINTED card", () => {
  it("holds the plan ink to 4.5:1 hovered and pressed, on every tint", () => {
    // 4.5:1 AND NOT 3:1: the plan line is a 13px `--fs-popup` regular-weight label, so
    // the large-text exception does not reach it. Mis-filing it against 3:1 is what
    // this assertion exists to prevent.
    //
    // The LIFTED ink is read out of the rule's own state blocks, so deleting the lift
    // fails here rather than silently shipping the unlifted ink.
    const row = ruleBody(input, ".pill-account");
    const hoverInk = decl(nested(row, "&:hover"), "color");
    const pressInk = decl(nested(row, "&:active"), "color");
    for (const hue of HUES) {
      const card = cardBackdrop(hue);
      for (const [label, ink, wash] of [
        ["hovered", hoverInk, HOVER_TOKEN],
        ["pressed", pressInk, PRESS_TOKEN],
      ] as const) {
        const bg = over(wash, card);
        for (const m of pair(ink, bg)) {
          expect(
            m.ratio,
            `${m.theme}: the plan ink ${label} over the ${hue} card (${ink})`,
          ).toBeGreaterThanOrEqual(4.5);
        }
      }
    }
  });

  it("would FAIL that floor unlifted, which is why the ladder moves the ink", () => {
    // The measurement the lift rests on, so the lift is not a preference. The resting
    // ink is what the row INHERITS from the card, read off `.pill-expand-content`.
    const resting = decl(ruleBody(input, ".pill-expand-content"), "color");
    const failures: string[] = [];
    for (const hue of HUES) {
      const card = cardBackdrop(hue);
      for (const [label, wash] of [
        ["hovered", HOVER_TOKEN],
        ["pressed", PRESS_TOKEN],
      ] as const) {
        for (const m of pair(resting, over(wash, card))) {
          if (m.ratio < 4.5) {
            failures.push(`${m.theme} ${hue} ${label}`);
          }
        }
      }
    }
    // Since the ink ramp was re-cut against the hovered box, the unlifted ink clears
    // every HOVERED tint in both themes and every PRESSED tint in light (4.92-4.96),
    // and fails only dark's three pressed tints (3.89-4.05). So the lift still ships,
    // and it is the press in dark that decides it — the hover half no longer does.
    expect(failures.sort(), "the unlifted ink's failures").toEqual(
      ["dark green pressed", "dark red pressed", "dark yellow pressed"].sort(),
    );
  });

  it("holds the external mark to 3:1 WHILE INHERITING, in every state", () => {
    // The mark declares no `color` at all, so it inherits the row's — which is the
    // only value that clears 1.4.11's 3:1 in all four states. Measured while
    // inheriting, which is the claim: substituting a named ink is what fails.
    const row = ruleBody(input, ".pill-account");
    const resting = decl(ruleBody(input, ".pill-expand-content"), "color");
    const hoverInk = decl(nested(row, "&:hover"), "color");
    const pressInk = decl(nested(row, "&:active"), "color");
    for (const hue of HUES) {
      const card = cardBackdrop(hue);
      for (const [label, ink, bg] of [
        ["rest", resting, card],
        ["hovered", hoverInk, over(HOVER_TOKEN, card)],
        ["pressed", pressInk, over(PRESS_TOKEN, card)],
      ] as const) {
        for (const m of pair(ink, bg)) {
          expect(
            m.ratio,
            `${m.theme}: the external mark ${label} over the ${hue} card`,
          ).toBeGreaterThanOrEqual(3);
        }
      }
    }
  });

  it("declares no colour for that mark, so the value above cannot drift", () => {
    // An ABSENT declaration is what makes the floor above permanent. A named ink is
    // what would break it: `--c-text-tertiary` reads 2.71:1 pressed over the tinted
    // card in dark, under 1.4.11's 3:1. That is one state of four rather than the
    // three it failed before the ink ramp was re-cut, and one is enough — the
    // pressed state is the one a reader is looking at.
    const tertiary = "var(--c-text-tertiary)";
    const card = cardBackdrop("yellow");
    expect(
      ratio("dark", tertiary, over(PRESS_TOKEN, card)),
      "the rejected ink, pressed in dark",
    ).toBeLessThan(3);
    // And the rule genuinely declares nothing for it.
    expect(input.replace(/\/\*[\s\S]*?\*\//g, " "), "no .pill-account-out rule").not.toMatch(
      /\.pill-account-out[^{]*\{/u,
    );
  });

  it("keeps the wash VISIBLE over the tinted card, which is why the opaque rung lost", () => {
    // The other half of the ladder decision, and the half a contrast floor cannot see:
    // the opaque `.pill`-family rung clears every ink floor and its surface STEP over
    // this card is ~1.07-1.16, an invisible hover, because the 8% tint has already
    // travelled most of the way to it. The gated wash steps ~1.59 dark / ~1.35 light.
    for (const hue of HUES) {
      const card = cardBackdrop(hue);
      const washStep = ratio("dark", over(HOVER_TOKEN, card), card);
      const opaqueStep = ratio("dark", "var(--c-bg-tertiary)", card);
      expect(washStep, `${hue}: the wash steps visibly`).toBeGreaterThan(1.4);
      expect(opaqueStep, `${hue}: the opaque rung would not`).toBeLessThan(1.2);
      expect(washStep, `${hue}: and the wash is the louder of the two`).toBeGreaterThan(opaqueStep);
    }
  });
});
