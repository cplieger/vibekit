// THE ANSI PALETTE'S FLOORS, measured rather than asserted about.
//
// css/15-ansi.css carried 32 hardcoded hexes "tuned for dark backgrounds", so the
// 4-bit palette was declared TWICE — once as those literals, once as the
// theme-split --c-term-* seeds — and only the copy nothing consumed tracked the
// theme. In light mode 15 of the 16 foreground entries missed 4.5:1, worst
// `.ansi-bright-white-fg` at 1.044:1: white text on a white card. The literals
// are gone; these tests are what keeps them gone.
//
// The colour maths lives in scripts/css-contrast.py — sRGB decode, OKLCH
// conversion, the WCAG ratio, alpha compositing — and this file shells out to it
// rather than reimplementing any of it in TypeScript, for the reason
// send-btn-contrast.test.ts gives: a second implementation is a second thing to
// be wrong, and the numbers in the stylesheet comments were measured with the
// first one. It uses `ansi-check --tsv`, which emits every measurement in one
// process; 576 gated checks as individual `pair` calls would be 576 subprocesses.
//
// The script READS 15-ansi.css, so these floors are asserted against what ships.
// That direction matters: the old report kept its own copy of the palette and had
// already drifted from the stylesheet (`black` still read `#000` there after
// `.ansi-black-bg` moved to a token), which is the same duplication defect one
// level up.
//
// The 32 --c-term-* values those classes read are now GENERATED, by
// scripts/css-ansi-palette.py, from kitty's default palette plus
// web-terminal-engine's own contrast lift. Four tests below cover that: the
// generator's output matches 01-tokens.css, layer 1 is kitty's table unaltered,
// layer 2 lifts only the entries that fail, and the one set of values the
// generator AUTHORS — the four light achromatic inks, which the lift collapses
// onto a single grey — stays spread and ordered. The floors are still measured
// independently, by css-contrast.py, through the stylesheet — so the generator and
// the report can disagree, and one of them fails when they do.
//
// Node environment: this runs a process and reads TSV.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");
const generator = join(here, "..", "scripts", "css-ansi-palette.py");
const sheet = join(here, "css", "15-ansi.css");

interface Row {
  kind: string;
  theme: string;
  name: string;
  context: string;
  fg: string;
  bg: string;
  ratio: number;
  floor: number | null;
  verdict: string;
}

/** Every ANSI measurement the script makes, in one process. */
function rows(): Row[] {
  const out = execFileSync("python3", [script, "ansi-check", "--tsv"], { encoding: "utf8" });
  const parsed = out
    .trim()
    .split("\n")
    .map((line) => {
      const c = line.split("\t");
      return {
        kind: c[0] ?? "",
        theme: c[1] ?? "",
        name: c[2] ?? "",
        context: c[3] ?? "",
        fg: c[4] ?? "",
        bg: c[5] ?? "",
        ratio: Number(c[6]),
        floor: c[7] === "-" ? null : Number(c[7]),
        verdict: c[8] ?? "",
      };
    });
  expect(parsed.length, "the script produced no measurements").toBeGreaterThan(0);
  expect(
    [...new Set(parsed.map((r) => r.theme))].sort(),
    "both themes must be measured — a palette correct in one is the defect",
  ).toEqual(["dark", "light"]);
  return parsed;
}

/** The generator's kitty -> shipped audit, one row per token. */
function tableRows(): string[][] {
  return execFileSync("python3", [generator, "table"], { encoding: "utf8" })
    .trim()
    .split("\n")
    .slice(1)
    .map((line) => line.split("\t"));
}

/** `#rgb` or `#rrggbb` to its three channel values. */
function expandHex(hex: string): [number, number, number] {
  const h = hex.replace("#", "");
  const full = h.length === 3 ? [...h].map((c) => c + c).join("") : h;
  expect(full, `unexpected hex shape: ${hex}`).toMatch(/^[0-9a-f]{6}$/);
  return [0, 2, 4].map((i) => parseInt(full.slice(i, i + 2), 16)) as [number, number, number];
}

/** The 16 ANSI codes, each in both roles. */
const CODES = [
  "black",
  "red",
  "green",
  "yellow",
  "blue",
  "magenta",
  "cyan",
  "white",
  "bright-black",
  "bright-red",
  "bright-green",
  "bright-yellow",
  "bright-blue",
  "bright-magenta",
  "bright-cyan",
  "bright-white",
];

const stripComments = (css: string): string => css.replace(/\/\*[\s\S]*?\*\//g, " ");

describe.skipIf(!existsSync(script))("the ANSI palette", () => {
  it("carries no colour literal — every entry reads a token", () => {
    // The regression this exists to prevent is the original state of the file:
    // a literal cannot track the theme, so one theme is always wrong and no
    // amount of tuning the other fixes it. Scoped to COLOUR literals; `opacity:
    // 0.7` and `font-weight: 700` are not colours.
    const text = stripComments(readFileSync(sheet, "utf8"));
    const offenders: string[] = [];
    text.split("\n").forEach((line, i) => {
      // A colour-valued declaration whose value is anything but a var() read.
      // `transparent` is admitted, and only that keyword: it is the ABSENCE of
      // ink (`.ansi-hidden`'s conceal), so there is no theme for it to be wrong
      // in and it declares no part of the palette. Any other non-var() value is
      // a second declaration of a colour and fails.
      const m = /(?:^|[\s;{])(color|background-color|background)\s*:\s*([^;}]+)/.exec(line);
      if (m && !/^var\(/.test(m[2]!.trim()) && m[2]!.trim() !== "transparent") {
        offenders.push(`15-ansi.css:${i + 1} ${m[1]}: ${m[2]!.trim()}`);
      }
      // A raw colour anywhere else in the file (a shadow, a gradient stop).
      for (const lit of line.matchAll(/#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|oklab)\(/g)) {
        offenders.push(`15-ansi.css:${i + 1} ${lit[0]}`);
      }
    });
    expect(
      offenders,
      "15-ansi.css must read --c-term-* for every colour. A literal here is a " +
        "second declaration of the palette, and only the token copy is theme-split.",
    ).toEqual([]);
  });

  it("declares all 16 codes in both roles, so no code falls back to the default ink", () => {
    // A missing class is invisible rather than wrong: output-render.ts emits it,
    // nothing styles it, and the span silently inherits. That reads as "this
    // program used no colour" instead of as a gap.
    const text = stripComments(readFileSync(sheet, "utf8"));
    const missing: string[] = [];
    for (const code of CODES) {
      for (const role of ["fg", "bg"] as const) {
        const prop = role === "fg" ? "color" : "background-color";
        const re = new RegExp(`\\.ansi-${code}-${role}\\s*\\{[^}]*${prop}\\s*:\\s*var\\(--c-term-`);
        if (!re.test(text)) {
          missing.push(`.ansi-${code}-${role}`);
        }
      }
    }
    expect(missing, "every ANSI code needs both an ink and a fill").toEqual([]);
  });

  it("declares every attribute class output-render.ts can emit", () => {
    // The same invisible-failure shape as the codes above, one axis over: an
    // unstyled attribute class means a program's strikethrough or conceal simply
    // does not happen, and the transcript looks like the program never asked.
    // ATTR_* in output-render.ts is the authority for the list.
    const text = stripComments(readFileSync(sheet, "utf8"));
    const missing = [
      "ansi-bold",
      "ansi-dim",
      "ansi-italic",
      "ansi-underline",
      "ansi-double-underline",
      "ansi-overline",
      "ansi-strike",
      "ansi-hidden",
      "ansi-blink",
      "ansi-inverse-ink",
      "ansi-inverse-fill",
    ].filter((cls) => !new RegExp(`\\.${cls}\\s*\\{[^}]*[a-z]`).test(text));
    expect(missing, "an attribute output-render.ts emits must have a rule").toEqual([]);
  });

  it("keeps the inverse fallbacks out of the palette namespace, one property each", () => {
    // TWO rules in one, and both were measured rather than reasoned about.
    //
    // NAMESPACE: `.ansi-<x>-fg` / `.ansi-<x>-bg` is the 16-code palette, and
    // css-contrast.py parses it out of this file. Naming the inverse fallbacks
    // `.ansi-inverse-fg` / `-bg` made the report treat them as a 17th code,
    // including the tautology `inverse-fg on --c-bg-secondary: 1.000`, because a
    // fallback for the DEFAULT colour is not a colour a program can select on
    // its own. The script now matches an alternation over the 16 real names
    // rather than the `-fg`/`-bg` SHAPE, so a future non-palette class cannot
    // repeat that; this rule stays because the namespace is what makes the
    // alternation legible, and belt-and-braces is cheap on a defect that landed
    // once already.
    //
    // ONE PROPERTY EACH: a rule setting both would also match a HALF-explicit
    // inverse and override the swapped palette class on the side that did have a
    // colour — inverse red-on-blue rendering as the default inverse pair.
    const src = stripComments(readFileSync(sheet, "utf8"));
    expect(
      /\.ansi-inverse-(fg|bg)\s*\{/.test(src),
      "inverse must not use the palette suffixes",
    ).toBe(false);
    for (const [cls, prop, banned] of [
      ["ansi-inverse-ink", "color", "background-color"],
      ["ansi-inverse-fill", "background-color", "color"],
    ] as const) {
      const body = new RegExp(`\\.${cls}\\s*\\{([^}]*)\\}`).exec(src)?.[1] ?? "";
      expect(new RegExp(`(?<![-\\w])${prop}\\s*:`).test(body), `${cls} must set ${prop}`).toBe(
        true,
      );
      expect(
        new RegExp(`(?<![-\\w])${banned}\\s*:`).test(body),
        `${cls} must NOT also set ${banned} — that overrides the explicit side`,
      ).toBe(false);
    }
  });

  it("clears 4.5:1 for every ink on the surface it renders on, in both themes", () => {
    // ONE surface, traced rather than assumed: a tool card's `.tool-output pre`
    // paints nothing and resolves to --c-bg-secondary via `.tool-call`. Agent
    // command output moved into the card that spawned it, so `.agent-term-pane`
    // and its --c-term-bg are gone, and the live shell panel is not a second
    // surface either — web-terminal-engine paints each run from server-resolved
    // RGB inline and reads none of these tokens. ANSI_SURFACES is one entry now,
    // which is why this count is 32 rather than 64.
    const misses = rows()
      .filter((r) => r.kind === "fg-surface" && r.floor !== null && r.ratio < r.floor)
      .map(
        (r) => `${r.theme} ${r.name} on ${r.context}: ${String(r.ratio)}:1 (${r.fg} on ${r.bg})`,
      );
    expect(
      misses,
      "an ANSI foreground is body text, so WCAG 1.4.3's 4.5:1 applies on the " +
        "surface it lands on — which is also the surface the generator lifts against.",
    ).toEqual([]);
    // Coverage, so a filter that silently matched nothing cannot pass this.
    const checked = rows().filter((r) => r.kind === "fg-surface");
    expect(checked.length, "16 codes x 1 surface x 2 themes").toBe(32);
  });

  it("clears 4.5:1 for every ink-on-fill pair a program can select", () => {
    // A background entry is a surface a glyph sits on, so the pair is what
    // matters rather than the fill alone: `ESC[34;40m` is blue on black and a
    // real one — GNU ls ships `ESC[34;42m` for an other-writable directory.
    // All 16x16 are gated because a program may select any of them.
    const all = rows();
    const misses = all
      .filter((r) => r.kind === "fg-fill" && r.floor !== null && r.ratio < r.floor)
      .map(
        (r) => `${r.theme} ${r.name} on ${r.context}: ${String(r.ratio)}:1 (${r.fg} on ${r.bg})`,
      );
    expect(
      misses,
      "a fill is sized to CARRY a glyph. Every ink must clear 4.5:1 on every " +
        "fill, or a program that sets both renders text nobody can read.",
    ).toEqual([]);
    expect(all.filter((r) => r.kind === "fg-fill").length, "16 x 16 x 2 themes").toBe(512);
  });

  it("clears 4.5:1 for the container's own ink on every fill", () => {
    // The other half of the pairing, and the one easy to forget: `ESC[41m` with
    // no colour set arrives as a fill with the CONTAINER's ink on it, which is
    // --c-text-secondary in a tool card. --c-term-fg left this set with
    // --c-term-bg, for the same reason: it is the live terminal's ink and the
    // live terminal paints no ANSI fill. It was never binding either — lighter
    // than every legal dark fill, darker than every legal light one — so the
    // derived fills are byte-identical with or without it.
    const misses = rows()
      .filter((r) => r.kind === "ink-fill" && r.floor !== null && r.ratio < r.floor)
      .map((r) => `${r.theme} ${r.name} on ${r.context}: ${String(r.ratio)}:1`);
    expect(
      misses,
      "a bare ANSI background inherits the container's ink; that pair is as real " +
        "as an explicit one.",
    ).toEqual([]);
  });

  it("measures no exit-status footer, because there is no longer one to paint", () => {
    // `.agent-term-status` was the agent-terminal pane's "exited (code N)" line
    // and the pane is gone: the card's own status glyph carries the outcome now,
    // so a second rendering of it would be a second thing to keep true. The rule
    // never lived in 15-ansi.css, so the report's parser could not have found it
    // whatever happened; the parser, the section and ANSI_STATUS_OPACITY are all
    // deleted, and this asserts they stay deleted.
    expect(rows().filter((r) => r.kind === "status")).toEqual([]);
  });

  it("keeps the fill-vs-surface figures on the record without gating them", () => {
    // NOT a floor, and the comment in 01-tokens.css says why: a fill that carries
    // the darkest ink at 4.5:1 is pinned at or below the surface's own luminance,
    // so 3:1 as a region marker is unreachable unless the inks flatten toward
    // near-white — the one thing this palette may not do. Recorded so that
    // anyone who moves the surface ramp sees these numbers move rather than
    // discovering the trade by eye.
    const recorded = rows().filter((r) => r.kind === "fill-surface");
    expect(recorded.length, "16 fills x 1 surface x 2 themes").toBe(32);
    expect(
      recorded.every((r) => r.floor === null),
      "these are recorded, not gated — gating them would be 32 standing failures",
    ).toBe(true);
    // If one ever reaches 3:1 the trade has changed and the comments are stale.
    const surprising = recorded
      .filter((r) => r.ratio >= 3.0)
      .map((r) => `${r.theme} ${r.name} on ${r.context}: ${String(r.ratio)}:1`);
    expect(
      surprising,
      "a fill reaching 3:1 against its surface would mean the arithmetic in " +
        "01-tokens.css is wrong — good news, but the comment needs rewriting.",
    ).toEqual([]);
  });

  it("ships exactly what the generator derives — a hand-edited token fails here", () => {
    // The 32 --c-term-* values are GENERATED (scripts/css-ansi-palette.py) rather
    // than typed, because 32 values across two themes typed by hand is where an
    // arithmetic error hides and a surface change has to be re-derived rather
    // than re-guessed. `check` re-derives and diffs against 01-tokens.css, so an
    // edit made without re-running the generator is this failure. Same shape as
    // theme-init-snippet.test.ts's drift guard and cmd/wire-codegen's determinism
    // check: the generator is the source, the file is its output.
    const out = execFileSync("python3", [generator, "check"], { encoding: "utf8" });
    expect(out.trim(), "01-tokens.css drifted from scripts/css-ansi-palette.py").toBe("0 drifted");
  });

  it("anchors layer 1 on kitty's published palette, unaltered", () => {
    // The DEFERRAL itself, stated independently of the generator so that editing
    // its KITTY table is a test failure rather than a silent redefinition of what
    // vibekit defers to. Source: kitty/options/definition.py color0-color15, the
    // same table web-terminal-engine's vt/wire.go basic16RGB resolves indices
    // 0-15 to. This is the one place a literal palette copy is right — it is a
    // claim about an external reference, not a second declaration of what ships.
    const KITTY = [
      "#000",
      "#cc0403",
      "#19cb00",
      "#cecb00",
      "#0d73cc",
      "#cb1ed1",
      "#0dcdcd",
      "#ddd",
      "#767676",
      "#f2201f",
      "#23fd00",
      "#fffd00",
      "#1a8fff",
      "#fd28ff",
      "#14ffff",
      "#fff",
    ];
    const table = tableRows();
    expect(table.length, "16 codes x 2 roles x 2 themes").toBe(64);
    for (const theme of ["dark", "light"]) {
      for (const role of ["ink", "fill"]) {
        const seen = table.filter((r) => r[0] === theme && r[1] === role).map((r) => r[3]);
        expect(seen, `${theme} ${role} must be anchored on kitty's 16`).toEqual(KITTY);
      }
    }
  });

  it("lifts only what fails, and every ink clears the floor after the lift", () => {
    // Layer 2's two properties in one place. The floor is the engine's: every
    // lifted ink clears 4.5:1 on the surface it renders on, which is also what
    // WithMinimumContrast enforces in web-terminal-server. And the lift is
    // MINIMAL, so the common case is kitty's actual colour — 10 of 16 in dark.
    //
    // The counts are per ORIGIN rather than a lifted yes/no, because the four
    // light achromatic inks are neither kitty's value nor the engine's function
    // applied to it: they are authored in the generator (next test). A boolean
    // would file them under the lift, which is the one claim about them that is
    // false. `kitty` here means byte-exact — light keeps two, `black` and `red`.
    const rowsOf = tableRows();
    const under = rowsOf.filter((r) => Number(r[6]) < 4.5).map((r) => `${r[0]} ${r[2]}: ${r[6]}:1`);
    expect(under, "every derived value must clear the floor it was derived for").toEqual([]);
    const count = (theme: string, src: string): number =>
      rowsOf.filter((r) => r[0] === theme && r[1] === "ink" && r[5] === src).length;
    expect(count("dark", "kitty"), "dark: kitty's own colour on 10 of 16").toBe(10);
    expect(count("light", "kitty"), "light: only black and red are byte-exact kitty").toBe(2);
    expect(count("light", "authored"), "light: white, bright-black, bright-white").toBe(3);
    expect(
      count("dark", "authored"),
      "dark keeps three distinct achromatic levels under the lift, so it is not " +
        "authored — the asymmetry is the decision, not an oversight",
    ).toBe(0);
  });

  it("keeps the four light achromatic codes distinct and in contrast order", () => {
    // THE REGRESSION THIS PIN EXISTS FOR, and no floor can catch it. The lift stops
    // at the smallest blend clearing 4.5:1, which is a FIXED target luminance, so
    // on a light card every entry starting lighter than that converges on it:
    // `white`, `bright-black` and `bright-white` all arrived at #666. All three
    // cleared the floor, so nothing failed — ESC[90m (de-emphasis) and ESC[97m
    // (emphasis) simply rendered as one colour. Only the ORDER catches that.
    //
    // The order is the lightness each code ASKS for, inverted into contrast against
    // a light card: ESC[30m asks for black, the loudest thing available here, and
    // ESC[97m asks for white, which is invisible and must therefore be darkened
    // LEAST of the four — so the quietest legible grey is the honest rendering of
    // "bright white". Achromatic is asserted too: these four carry no hue, and
    // inventing one would be a worse answer than the collapse.
    const ORDER = ["black", "bright-black", "white", "bright-white"];
    const byToken = new Map(
      tableRows()
        .filter((r) => r[0] === "light" && r[1] === "ink")
        .map((r) => [r[2], r]),
    );
    const ramp = ORDER.map((name) => {
      const row = byToken.get(`--c-term-${name}`);
      expect(row, `light --c-term-${name} must be in the generator's table`).toBeDefined();
      return { name, hex: row![4]!, ratio: Number(row![6]) };
    });

    for (const step of ramp) {
      const [r, g, b] = expandHex(step.hex);
      expect(
        [r === g, g === b],
        `${step.name} must stay achromatic — ${step.hex} has unequal channels`,
      ).toEqual([true, true]);
      expect(step.ratio, `${step.name} must clear the floor`).toBeGreaterThanOrEqual(4.5);
    }

    expect(
      new Set(ramp.map((s) => s.hex)).size,
      `the four light achromatic codes collapsed: ${ramp.map((s) => `${s.name}=${s.hex}`).join(", ")}`,
    ).toBe(4);

    const gaps = ramp.slice(1).map((s, i) => ramp[i]!.ratio - s.ratio);
    expect(
      gaps.every((g) => g > 0),
      `contrast must fall monotonically across ${ORDER.join(" > ")}: ` +
        ramp.map((s) => `${s.name} ${String(s.ratio)}`).join(", "),
    ).toBe(true);
    // A gap this small would be a compression back toward the collapse rather than
    // a ramp. Measured today: 4.015, 4.867, 3.153 against a floor of 1.5.
    expect(Math.min(...gaps), `adjacent steps too close: ${gaps.join(", ")}`).toBeGreaterThan(1.5);
  });

  it("cannot pass vacuously — the tool still reports a miss, and its verdicts are computed", () => {
    // Every floor above reads a ratio out of the tool and compares it here, so if
    // the tool stopped measuring they would all be trivially true. Three guards,
    // each covering a different way that could happen.
    //
    // 1. The engine still fails a pair it should fail. (Verified load-bearing:
    //    forcing `show_pair` to return 0 fails this test.)
    expect(() =>
      execFileSync("python3", [script, "pair", "--c-text-tertiary", "--c-bg-elevated", "4.5"], {
        encoding: "utf8",
      }),
    ).toThrow();

    // 2. The verdict column is DERIVED from the ratio and the floor, not
    //    hardcoded. Recomputing it here would be a second implementation of the
    //    colour maths if it touched colour — it does not; it is one comparison,
    //    and it is what catches a verdict column stuck on "ok".
    const all = rows();
    const inconsistent = all
      .filter((r) => {
        const want = r.floor === null ? "recorded" : r.ratio < r.floor ? "FAIL" : "ok";
        return r.verdict !== want;
      })
      .map((r) => `${r.theme} ${r.name} on ${r.context}: ${r.verdict} at ${String(r.ratio)}:1`);
    expect(inconsistent, "a verdict must follow from the ratio it is printed beside").toEqual([]);

    // 3. Both output paths agree on how much was gated. `ansi-check` without
    //    --tsv prints a summary from the same rows; if the two disagree, one of
    //    them is not looking at the palette.
    const summary = execFileSync("python3", [script, "ansi-check"], { encoding: "utf8" });
    const gated = all.filter((r) => r.floor !== null).length;
    expect(gated, "the gated set must not be empty").toBeGreaterThan(500);
    expect(summary.trim()).toBe(`${String(all.length)} measurements, 0 FAIL`);
  });
});
