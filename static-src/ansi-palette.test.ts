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
// process; 578 gated checks as individual `pair` calls would be 578 subprocesses.
//
// The script READS 15-ansi.css, so these floors are asserted against what ships.
// That direction matters: the old report kept its own copy of the palette and had
// already drifted from the stylesheet (`black` still read `#000` there after
// `.ansi-black-bg` moved to a token), which is the same duplication defect one
// level up.
//
// Node environment: this runs a process and reads TSV.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");
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
      const m = /(?:^|[\s;{])(color|background-color|background)\s*:\s*([^;}]+)/.exec(line);
      if (m && !/^var\(/.test(m[2]!.trim())) {
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
    // A missing class is invisible rather than wrong: ansi_up emits it, nothing
    // styles it, and the span silently inherits. That reads as "this program
    // used no colour" instead of as a gap.
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

  it("clears 4.5:1 for every ink on both surfaces it renders on, in both themes", () => {
    // The two surfaces are traced, not assumed: a tool card's `.tool-output pre`
    // resolves to --c-bg-secondary via `.tool-call`, and an agent terminal's
    // `pre.agent-term-output` to --c-term-bg via `.agent-term-pane`. The old
    // report measured --c-bg-primary and --c-code-bg, neither of which is either.
    const misses = rows()
      .filter((r) => r.kind === "fg-surface" && r.floor !== null && r.ratio < r.floor)
      .map(
        (r) => `${r.theme} ${r.name} on ${r.context}: ${String(r.ratio)}:1 (${r.fg} on ${r.bg})`,
      );
    expect(
      misses,
      "an ANSI foreground is body text, so WCAG 1.4.3's 4.5:1 applies on every " +
        "surface it can land on — and the same class set lands on both.",
    ).toEqual([]);
    // Coverage, so a filter that silently matched nothing cannot pass this.
    const checked = rows().filter((r) => r.kind === "fg-surface");
    expect(checked.length, "16 codes x 2 surfaces x 2 themes").toBe(64);
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
    // no colour set arrives as a fill with the CONTAINER's ink on it —
    // --c-text-secondary in a tool card, --c-term-fg in an agent terminal.
    const misses = rows()
      .filter((r) => r.kind === "ink-fill" && r.floor !== null && r.ratio < r.floor)
      .map((r) => `${r.theme} ${r.name} on ${r.context}: ${String(r.ratio)}:1`);
    expect(
      misses,
      "a bare ANSI background inherits the container's ink; that pair is as real " +
        "as an explicit one.",
    ).toEqual([]);
  });

  it("clears 4.5:1 for the exit-status footer through its own opacity", () => {
    // .agent-term-status carries opacity 0.85, so the token is not what a reader
    // sees — the composite over --c-term-bg is. Measuring the raw token would
    // report a colour the app never paints.
    const status = rows().filter((r) => r.kind === "status");
    expect(status.length, "is-ok and is-err, both themes").toBe(4);
    const misses = status
      .filter((r) => r.floor !== null && r.ratio < r.floor)
      .map((r) => `${r.theme} ${r.name}: ${String(r.ratio)}:1`);
    expect(misses, "the footer is text, composited or not").toEqual([]);
  });

  it("keeps the fill-vs-surface figures on the record without gating them", () => {
    // NOT a floor, and the comment in 01-tokens.css says why: a fill that carries
    // the darkest ink at 4.5:1 is pinned at or below the surface's own luminance,
    // so 3:1 as a region marker is unreachable unless the inks flatten toward
    // near-white — the one thing this palette may not do. Recorded so that
    // anyone who moves the surface ramp sees these numbers move rather than
    // discovering the trade by eye.
    const recorded = rows().filter((r) => r.kind === "fill-surface");
    expect(recorded.length, "16 fills x 2 surfaces x 2 themes").toBe(64);
    expect(
      recorded.every((r) => r.floor === null),
      "these are recorded, not gated — gating them would be 64 standing failures",
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
