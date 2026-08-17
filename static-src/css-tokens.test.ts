// Structural guards for the design-system token layer (css/01-tokens.css and
// the stylesheets that consume it).
//
// Two defects motivated these, both of which shipped and neither of which any
// existing tool catches.
//
//  1. `--r-lg` — the app's 12px container radius — was READ three times in
//     15-input.css and DECLARED nowhere. It resolved through a per-site
//     `var(--r-lg, 0.75rem)` fallback, so it looked like a token and behaved
//     like a literal: changing the container radius meant editing three
//     fallbacks, and nothing said so. stylelint's `no-unknown-custom-properties`
//     is on in this repo and cannot see it — a fallback suppresses that rule
//     entirely (verified: `var(--nope, 4px)` passes, `var(--nope)` fails). So
//     the fallback form is exactly the blind spot, and it is the form this
//     defect took.
//
//  2. A selected state carried by ONE channel. The app had 20 selected-state
//     rules across 9 recipes — fill only, ink only, fill+ink, fill+border,
//     an underline, a font-weight — which is what the user saw as "some
//     surfaces colour the icon and others colour the background". The three
//     channels now travel together (70-selection.css), and a fill without its
//     border and ink is the shape the drift took.
//
// Both tests read the SHIPPED stylesheets rather than a fixture, because the
// thing being guarded is the real bundle.

import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cssDir = join(here, "css");
const vendorDirs = [
  join(here, "node_modules", "@cplieger", "ui-primitives", "css"),
  join(here, "node_modules", "@cplieger", "web-terminal-ui", "css"),
];

interface Sheet {
  name: string;
  text: string;
}

function readSheets(dir: string): Sheet[] {
  if (!existsSync(dir)) {
    return [];
  }
  return readdirSync(dir)
    .filter((f) => f.endsWith(".css"))
    .sort()
    .map((f) => ({ name: f, text: readFileSync(join(dir, f), "utf8") }));
}

/** Comments hold prose ABOUT tokens; blanking them keeps line numbers usable. */
function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, (m) => "\n".repeat((m.match(/\n/g) ?? []).length));
}

const appSheets = readSheets(cssDir);
const vendorSheets = vendorDirs.flatMap(readSheets);

/** Every `--name:` declaration, wherever it is scoped. */
function declaredIn(sheets: Sheet[]): Set<string> {
  const out = new Set<string>();
  for (const s of sheets) {
    for (const m of stripComments(s.text).matchAll(/(--[\w-]+)\s*:/g)) {
      out.add(m[1]);
    }
    // @property registers a name and gives it an initial value, which is a
    // declaration for our purposes even though the syntax differs.
    for (const m of s.text.matchAll(/@property\s+(--[\w-]+)/g)) {
      out.add(m[1]);
    }
  }
  return out;
}

/**
 * Names a TypeScript module writes at runtime (`setProperty("--x", …)` or a
 * `"--x"` literal). Those are genuinely declared, just not in CSS, and pinning
 * them by hand would make this test a list to maintain instead of a check.
 */
function writtenByScript(): Set<string> {
  const out = new Set<string>();
  const walk = (dir: string): void => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (entry.name === "node_modules" || entry.name === "dist") {
        continue;
      }
      const p = join(dir, entry.name);
      if (entry.isDirectory()) {
        walk(p);
      } else if (entry.name.endsWith(".ts")) {
        for (const m of readFileSync(p, "utf8").matchAll(/["'`](--[\w-]+)["'`]/g)) {
          out.add(m[1]);
        }
      }
    }
  };
  walk(here);
  return out;
}

describe("design tokens are declared before they are read", () => {
  it("has no var() read — fallback or not — without a declaration", () => {
    const declared = new Set([
      ...declaredIn(appSheets),
      ...declaredIn(vendorSheets),
      ...writtenByScript(),
    ]);

    const orphans: string[] = [];
    for (const sheet of appSheets) {
      const body = stripComments(sheet.text);
      const lines = body.split("\n");
      lines.forEach((line, i) => {
        // The name is the first argument of var(); a fallback follows a comma
        // and is deliberately NOT what makes the read legitimate.
        for (const m of line.matchAll(/var\(\s*(--[\w-]+)/g)) {
          if (!declared.has(m[1])) {
            orphans.push(`${sheet.name}:${i + 1} reads ${m[1]}`);
          }
        }
      });
    }

    expect(
      orphans,
      "A custom property read but never declared is a literal wearing a token's " +
        "name: the value lives in each call site's fallback, so changing it means " +
        "finding every site. Declare it in css/01-tokens.css.",
    ).toEqual([]);
  });

  it("has no colour or elevation token declared with nothing reading it", () => {
    // The INVERSE of the check above, and the one the battery was missing. When
    // the on-selected inks were first added, five of them sat in 01-tokens.css
    // with zero readers anywhere and every gate stayed green: stylelint has no
    // opinion on an unused custom property, and the test above only walks from
    // a read to its declaration. So the half-finished state — the values chosen
    // and named, the rules that consume them never written — was indistinguish-
    // able from the finished one, which is exactly the state a reviewer had to
    // catch by hand.
    //
    // Scoped to `--c-*` and `--shadow-*`: a colour or an elevation is chosen
    // FOR a surface, so one with no surface is dead. Geometry and motion tokens
    // are deliberately NOT in scope — a spacing or duration token is a
    // vocabulary the next rule picks from, and demanding a current consumer for
    // each would argue against having a scale at all.
    //
    // `--c-term-*` is excluded on that same vocabulary ground, and it is the one
    // exclusion worth defending: the terminal seeds are the 16-colour ANSI
    // standard, which is a COMPLETE SET by definition. Three members have
    // consumers (`--c-term-bg` / `--c-term-fg` through shell.ts's SHELL_THEME,
    // `--c-term-black` in 15-ansi.css) and declaring only those would leave the
    // palette unable to answer for the other thirteen codes. Note the gap this
    // leaves open, since it is real: 15-ansi.css still hardcodes the other 30
    // ANSI literals rather than reading these seeds, so the palette is declared
    // twice and only one copy is theme-aware.
    const scoped = /^--(c|shadow)-/;
    const vocabulary = /^--c-term-/;

    const declared = new Map<string, string>();
    for (const sheet of appSheets) {
      for (const m of stripComments(sheet.text).matchAll(/(--[\w-]+)\s*:/g)) {
        if (scoped.test(m[1]) && !vocabulary.test(m[1]) && !declared.has(m[1])) {
          declared.set(m[1], sheet.name);
        }
      }
    }

    // A reader is any var() in any app or vendor sheet, or a name a TS module
    // reads at runtime (shell.ts pulls the ANSI palette via getComputedStyle),
    // or a use inside ANOTHER token's value.
    const read = new Set<string>([...writtenByScript()]);
    for (const sheet of [...appSheets, ...vendorSheets]) {
      for (const m of stripComments(sheet.text).matchAll(/var\(\s*(--[\w-]+)/g)) {
        read.add(m[1]);
      }
    }

    const orphans = [...declared]
      .filter(([name]) => !read.has(name))
      .map(([name, where]) => `${where} declares ${name}, nothing reads it`);

    expect(
      orphans,
      "A token nobody reads is either a value that was chosen and never wired " +
        "up, or one whose last consumer went away. Wire it or delete it — a " +
        "green battery over an unconsumed token is how a half-finished change " +
        "looks finished.",
    ).toEqual([]);
  });

  it("declares exactly one spelling of the pill radius", () => {
    // 999px at 7 sites and 999rem at 5 was one shape with two names, and a
    // stadium is now reserved for non-interactive status badges — so a literal
    // is also a shape decision made without the ladder.
    const literals: string[] = [];
    for (const sheet of appSheets) {
      if (sheet.name === "01-tokens.css") {
        continue;
      }
      stripComments(sheet.text)
        .split("\n")
        .forEach((line, i) => {
          if (/border(-[a-z]+)*-radius:[^;]*\b999(px|rem)\b/.test(line)) {
            literals.push(`${sheet.name}:${i + 1}`);
          }
        });
    }
    expect(literals, "Use var(--r-pill); a stadium is a badge, not a button.").toEqual([]);
  });
});

/** Split a stylesheet into `{ selector, body }` for its top-level rules. */
function rules(css: string): { selector: string; body: string; line: number }[] {
  const out: { selector: string; body: string; line: number }[] = [];
  const text = stripComments(css);
  let depth = 0;
  let selStart = 0;
  let bodyStart = 0;
  let selector = "";
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (ch === "{") {
      if (depth === 0) {
        selector = text.slice(selStart, i).trim();
        bodyStart = i + 1;
      }
      depth++;
    } else if (ch === "}") {
      depth--;
      if (depth === 0) {
        out.push({
          selector,
          body: text.slice(bodyStart, i),
          line: text.slice(0, selStart).split("\n").length,
        });
        selStart = i + 1;
      }
    }
  }
  return out;
}

/** Declarations of `body`, ignoring anything inside a nested block. */
function ownDeclarations(body: string): string[] {
  let depth = 0;
  let buf = "";
  for (const ch of body) {
    if (ch === "{") {
      depth++;
    } else if (ch === "}") {
      depth--;
    } else if (depth === 0) {
      buf += ch;
    }
  }
  return buf.split(";").map((d) => d.trim());
}

describe("the selected state carries fill, edge and ink together", () => {
  const FILL = /^(background|background-color)\s*:\s*var\(--c-selected-bg\)/;

  it("never sets the selected fill without its border and ink", () => {
    const broken: string[] = [];
    for (const sheet of appSheets) {
      for (const rule of rules(sheet.text)) {
        // A @keyframes step is not a state rule: it animates the properties it
        // animates, and demanding a resting treatment of `0%` is meaningless.
        if (/^@keyframes\b/.test(rule.selector)) {
          continue;
        }
        // @media / @supports wrap rules; recurse one level so a gated selected
        // rule is checked too.
        const blocks = rule.selector.startsWith("@") ? rules(rule.body) : [rule];
        for (const block of blocks) {
          const decls = ownDeclarations(block.body);
          if (!decls.some((d) => FILL.test(d))) {
            continue;
          }
          const hasEdge = decls.some((d) => /^border(-color)?\s*:/.test(d));
          const hasInk = decls.some((d) => /^color\s*:/.test(d));
          if (!hasEdge || !hasInk) {
            const missing = [!hasEdge && "border-color", !hasInk && "color"]
              .filter(Boolean)
              .join(" + ");
            broken.push(`${sheet.name}:${rule.line} ${block.selector} is missing ${missing}`);
          }
        }
      }
    }
    expect(
      broken,
      "A selected surface that fills without colouring its edge and its ink is " +
        "how nine different selected-state recipes happened. Set all three, or " +
        "add the selector to the shared rule in css/70-selection.css.",
    ).toEqual([]);
  });

  it("keeps the treatment in one place rather than per surface", () => {
    const owner = appSheets.find((s) => s.name === "70-selection.css");
    expect(owner, "css/70-selection.css is the selected-state owner").toBeDefined();

    const elsewhere: string[] = [];
    for (const sheet of appSheets) {
      if (sheet.name === "70-selection.css" || sheet.name === "01-tokens.css") {
        continue;
      }
      for (const rule of rules(sheet.text)) {
        // A keyframe may borrow the fill as a transient attention flash; that is
        // not a second selected-state treatment.
        if (/^@keyframes\b/.test(rule.selector)) {
          continue;
        }
        const offset = rule.line;
        rule.body.split("\n").forEach((line, i) => {
          if (/var\(--c-selected-(bg|border|fg)\)/.test(line)) {
            elsewhere.push(`${sheet.name}:${offset + i}`);
          }
        });
      }
    }
    // 10-shell-app.css keeps the tab strip's own selected hover/press and the
    // on-selected ink for its close button: those score (0,3,0) and (0,2,1), so
    // they still beat 70-selection.css and belong beside the rest of the strip.
    expect(
      elsewhere.filter((l) => !l.startsWith("10-shell-app.css")),
      "Add the selector to css/70-selection.css instead of re-deriving the " +
        "treatment on one more surface.",
    ).toEqual([]);
  });
});

describe("an achromatic colour leaves its hue powerless", () => {
  // The defect this exists to prevent, measured in the browser rather than
  // reasoned about: `--c-text-primary` was authored `oklch(100% 0 0deg)`, and a
  // hue is powerless only when it is MISSING. An explicit `0deg` is a real hue,
  // so every `color-mix(in oklch, <that ink>, <a status colour>)` interpolated
  // toward red — the on-selected green rendered at 63deg (orange) and the accent
  // at 334deg (pink). `scripts/css-contrast.py` implements the powerless rule
  // and therefore reported ZERO drift, so the measuring tool and the paint
  // disagreed silently. `none` is what makes the two agree.
  //
  // Scoped to chroma 0, because that is exactly when the hue channel carries no
  // information and can only do harm. A chromatic colour's hue is load-bearing.
  it("authors no zero-chroma oklch() with an explicit hue", () => {
    const offenders: string[] = [];
    for (const sheet of appSheets) {
      stripComments(sheet.text)
        .split("\n")
        .forEach((line, i) => {
          // oklch(<L> 0 <hue>) — chroma written as a bare zero, hue anything but
          // `none`. Matches the declaration form only; a mix's own arguments are
          // var() references and cannot carry a literal triple.
          for (const m of line.matchAll(/oklch\(\s*[\d.]+%?\s+0(?:\.0+)?\s+([^)/\s]+)/g)) {
            if (m[1] !== "none") {
              offenders.push(`${sheet.name}:${i + 1} - ${m[0]})`);
            }
          }
        });
    }
    expect(
      offenders,
      "A zero-chroma oklch() must write its hue as `none`, or color-mix() will " +
        "interpolate that hue and rotate whatever it is mixed with.",
    ).toEqual([]);
  });
});
