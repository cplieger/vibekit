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

/**
 * Capture group 1 of a match, asserted rather than optional-chained.
 *
 * Every pattern in this file declares group 1 as mandatory — not optional, not
 * inside an alternation that can skip it — so a match missing it means the
 * PATTERN was edited wrong, which is a defect in the test rather than a shape
 * the stylesheets can produce. Asserting once here is what lets all thirteen
 * read sites use the group as the string it is.
 */
function capture(m: RegExpExecArray, re: RegExp): string {
  const group = m[1];
  if (group === undefined) {
    throw new Error(`${re.source} matched ${JSON.stringify(m[0])} with no capture group 1`);
  }
  return group;
}

/** Capture group 1 of every match, for the loops that need nothing else. */
function* captures(text: string, re: RegExp): Generator<string> {
  for (const m of text.matchAll(re)) {
    yield capture(m, re);
  }
}

/** The patterns whose capture is read through `capture`/`captures`. */
const DECLARATION = /(--[\w-]+)\s*:/g;
const VAR_READ = /var\(\s*(--[\w-]+)/g;
const INNERMOST_BLOCK = /\{([^{}]*)\}/g;
// oklch(<L> 0 <hue>) — chroma written as a bare zero, hue anything but `none`.
const ZERO_CHROMA = /oklch\(\s*[\d.]+%?\s+0(?:\.0+)?\s+([^)/\s]+)/g;

const appSheets = readSheets(cssDir);
const vendorSheets = vendorDirs.flatMap(readSheets);

/** Every `--name:` declaration, wherever it is scoped. */
function declaredIn(sheets: Sheet[]): Set<string> {
  const out = new Set<string>();
  for (const s of sheets) {
    for (const name of captures(stripComments(s.text), DECLARATION)) {
      out.add(name);
    }
    // @property registers a name and gives it an initial value, which is a
    // declaration for our purposes even though the syntax differs.
    for (const name of captures(s.text, /@property\s+(--[\w-]+)/g)) {
      out.add(name);
    }
  }
  return out;
}

/**
 * Names a TypeScript module writes at runtime (`setProperty("--x", …)` or a
 * `"--x"` literal). Those are genuinely declared, just not in CSS, and pinning
 * them by hand would make this test a list to maintain instead of a check.
 *
 * TEST files are excluded, and that exclusion is the check working rather than a
 * scoping detail: a test naming a token in an assertion is not a declaration of
 * it, so counting one let a token be READ by a stylesheet, ASSERTED by a test,
 * and declared nowhere — measured by deleting `--c-selected-orange-fg` from
 * 01-tokens.css while 70-selection.css still read it, which stayed green because
 * tab-dot.test.ts happened to name it. Every on-selected ink was in that state.
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
      } else if (entry.name.endsWith(".ts") && !entry.name.endsWith(".test.ts")) {
        for (const name of captures(readFileSync(p, "utf8"), /["'`](--[\w-]+)["'`]/g)) {
          out.add(name);
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
        for (const name of captures(line, VAR_READ)) {
          if (!declared.has(name)) {
            orphans.push(`${sheet.name}:${i + 1} reads ${name}`);
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
    // The `--c-term-*` exemption used to cover the whole family on that same
    // vocabulary ground, and it was hiding a real gap: 15-ansi.css hardcoded 32
    // ANSI literals rather than reading the seeds, so the palette was declared
    // twice, only the unread copy was theme-split, and this test could not say
    // so. That is fixed — all 32 ANSI entries (16 inks + 16 fills) are read by
    // 15-ansi.css now — so the exemption is down to the four members that are
    // genuinely unwired, named individually rather than by prefix so the next
    // dead one cannot hide behind them.
    //
    // These four are the ENGINE's cursor and selection vocabulary. shell.ts's
    // SHELL_THEME maps five variables (--bg, --text, --accent, --surface,
    // --border) and none of them is these, so nothing reads them today. They are
    // left declared rather than deleted because that is a decision about the live
    // terminal's theming surface, not about contrast; flag them if they are still
    // unwired next time this file is edited.
    const scoped = /^--(c|shadow)-/;
    const vocabulary = /^--c-term-(cursor|cursor-accent|selection|selection-inactive)$/;

    const declared = new Map<string, string>();
    for (const sheet of appSheets) {
      for (const name of captures(stripComments(sheet.text), DECLARATION)) {
        if (scoped.test(name) && !vocabulary.test(name) && !declared.has(name)) {
          declared.set(name, sheet.name);
        }
      }
    }

    // A reader is any var() in any app or vendor sheet, or a name a TS module
    // names as a string literal (shell.ts's SHELL_THEME maps --c-term-bg and
    // --c-term-fg onto the engine's own variables), or a use inside ANOTHER
    // token's value.
    const read = new Set<string>([...writtenByScript()]);
    for (const sheet of [...appSheets, ...vendorSheets]) {
      for (const name of captures(stripComments(sheet.text), VAR_READ)) {
        read.add(name);
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

  it("declares exactly one spelling of the focus ring", () => {
    // `outline: 2px solid var(--c-accent)` was written out at 37 sites across 14
    // stylesheets, so the ring's width and style were a design decision restated
    // 37 times with no owner. The OFFSET was worse: five values (2px, 1px, -2px,
    // -1px, 3px) with nothing recording why, and measured side by side 1px and
    // 2px are indistinguishable on a control standing alone. Two values now,
    // both tokens — outside the edge, or inside it for a full-bleed row whose
    // container would clip an outside ring.
    const literals: string[] = [];
    for (const sheet of appSheets) {
      if (sheet.name === "01-tokens.css") {
        continue;
      }
      const lines = stripComments(sheet.text).split("\n");
      lines.forEach((line, i) => {
        if (/outline:\s*2px\s+solid\s+var\(--c-accent\)/.test(line)) {
          literals.push(`${sheet.name}:${i + 1} spells the ring out`);
        }
        // An offset is only this vocabulary's when it sits with the ring; a flash
        // keyframe's transparent outline and the danger ring carry their own.
        if (
          /^\s*outline-offset:\s*-?[\d.]/.test(line) &&
          /outline:\s*var\(--focus-ring\)/.test(lines[i - 1] ?? "")
        ) {
          literals.push(`${sheet.name}:${i + 1} spells the offset out`);
        }
      });
    }
    expect(
      literals,
      "Use var(--focus-ring) with var(--focus-offset) or var(--focus-offset-inset).",
    ).toEqual([]);
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
          // ZERO_CHROMA matches the declaration form only; a mix's own arguments
          // are var() references and cannot carry a literal triple.
          for (const m of line.matchAll(ZERO_CHROMA)) {
            if (capture(m, ZERO_CHROMA) !== "none") {
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

describe("an ink is only paired with a fill it clears", () => {
  // Two inks are sized against the PAGE and are not general-purpose, so pairing
  // them with a raised fill is a rule violation rather than a taste call. That
  // makes it a stylesheet question, which is why it lives here instead of in
  // scripts/css-contrast.py: the script can only measure the cross product of
  // every token against every surface, and reporting a combination the app does
  // not contain puts standing failures in its output.
  //
  // The hint ink measures 4.771:1 on the second rung, 3.601:1 on the third and
  // 2.699:1 on the top one — the second rung clears AA since the 63->66 lift,
  // the top two cannot at any lift (01-tokens.css states the two-rung contract).
  // Seven rules paired it with a raised fill anyway — a picker's metadata, a
  // badge, two gutters, a group label, a footer and a link badge — and each was
  // found by grep rather than by a gate.
  const RAISED = /background(?:-color)?\s*:\s*[^;]*var\(--c-bg-(?:tertiary|elevated)\)/;
  // Anchored so `border-color` and `outline-color` are not swept up with it:
  // an EDGE is a graphic at a 3:1 floor and the hint ink can legitimately draw
  // one. Only the text ink is at issue.
  const HINT = /(?<![-\w])color:\s*var\(--c-text-tertiary\)/;

  it("keeps the hint ink off a raised fill", () => {
    const offenders: string[] = [];
    for (const sheet of appSheets) {
      const text = stripComments(sheet.text);
      // Innermost blocks only. A nested state that raises the fill while the ink
      // sits on its parent is a real pairing too, but it is not decidable from
      // one block, and this catches the shape that actually recurred.
      for (const m of text.matchAll(INNERMOST_BLOCK)) {
        const body = capture(m, INNERMOST_BLOCK);
        if (RAISED.test(body) && HINT.test(body)) {
          offenders.push(`${sheet.name}:${text.slice(0, m.index).split("\n").length}`);
        }
      }
    }
    expect(
      offenders,
      "--c-text-tertiary clears 4.5:1 on --c-bg-primary and --c-bg-secondary and " +
        "on neither rung above them. On --c-bg-tertiary or --c-bg-elevated, step " +
        "up to --c-text-secondary.",
    ).toEqual([]);
  });

  // The gate above and scripts/css-contrast.py share one blind spot, and it is the
  // one that shipped a 2.792:1 paragraph: an `opacity` below 1 multiplies whatever
  // the ink measured, and NEITHER tool can see it. The script resolves the token
  // GRAPH, where no element and no opacity exists. This file reads the STYLESHEET,
  // so it can see the declaration — which is what makes this check possible where
  // the ancestor-supplied-fill case is not. Every offender found so far declared
  // the ink and the opacity in the SAME block (`.reasoning-block`, `.rail-gap`),
  // so one block is enough to decide it.
  //
  // Chrome and an inactive control are exempt, and the reasons differ: WCAG 1.4.3
  // exempts an inactive component outright, while a decorative graphic is not text
  // at all. So the exemption is keyed on the SELECTOR, not on a promise in a
  // comment — a rule that dims text has to name a state in this list to do it.
  const INACTIVE_STATE =
    /:disabled|\[disabled\]|aria-disabled|aria-busy|-cloning\b|-rejected\b|\.btn-loading\b|-disabled\b/;
  // The declared exceptions: a container whose `color` only ever feeds an svg's
  // `currentColor`, so it carries no text and answers to WCAG 1.4.11's 3:1 rather
  // than 1.4.3's 4.5:1. An explicit list rather than a name pattern (`-icon`,
  // `-btn`) on purpose: a pattern would silently adopt the next control that
  // happens to be named that way and also happens to hold a label. Each entry
  // carries its measured ratio against the 3:1 graphic floor, so a surface change
  // that breaks one is a re-measure rather than a re-argument.
  const GRAPHIC_ONLY = new Set([
    // 48px empty-state illustration on --c-bg-primary: 3.582 dark / 3.044 light.
    ".git-multirepo-empty-icon",
    // 1.5rem steer-row glyph button on --c-bg-secondary: 5.214 dark / 3.997 light.
    ".steer-act",
  ]);
  const TEXT_INK =
    /(?<![-\w])color:\s*var\(--c-text-(?:primary|secondary|tertiary|control|aside)\)/;
  const DIMMED = /(?<![-\w])opacity:\s*0?\.\d+/;

  it("never dims a text ink with opacity", () => {
    const offenders: string[] = [];
    for (const sheet of appSheets) {
      const text = stripComments(sheet.text);
      for (const m of text.matchAll(INNERMOST_BLOCK)) {
        const body = capture(m, INNERMOST_BLOCK);
        if (!TEXT_INK.test(body) || !DIMMED.test(body)) {
          continue;
        }
        const line = text.slice(0, m.index).split("\n").length;
        // The selector is whatever precedes the block; take the last line of it.
        const before = text.slice(0, m.index).split("\n");
        const selector = (before[before.length - 1] ?? "").trim();
        if (INACTIVE_STATE.test(selector)) {
          continue;
        }
        if (GRAPHIC_ONLY.has(selector.replace(/\s*\{$/, "").trim())) {
          continue;
        }
        offenders.push(`${sheet.name}:${line} ${selector}`);
      }
    }
    expect(
      offenders,
      "An `opacity` multiplies the ink's measured contrast and is invisible to " +
        "both contrast gates, so the number the tools report is not the number on " +
        "screen. Dim active text by choosing an ink token (--c-text-aside for " +
        "subordinate prose); keep opacity for chrome and inactive controls, and " +
        "add a graphic-only container to GRAPHIC_ONLY with its measured ratio.",
    ).toEqual([]);
  });

  it("keeps a status ink off the top rung", () => {
    // red 2.797, danger 2.585 and warning 2.980 against --c-bg-elevated, under
    // the 3:1 a coloured glyph needs. Today the only element with that fill is
    // .pill:active and it carries the primary ink, so this guards the next one.
    const hue = /(?<![-\w])color:\s*var\(--c-(?:red|danger|warning)\)/;
    const top = /background(?:-color)?\s*:\s*[^;]*var\(--c-bg-elevated\)/;
    const offenders: string[] = [];
    for (const sheet of appSheets) {
      const text = stripComments(sheet.text);
      for (const m of text.matchAll(INNERMOST_BLOCK)) {
        const body = capture(m, INNERMOST_BLOCK);
        if (top.test(body) && hue.test(body)) {
          offenders.push(`${sheet.name}:${text.slice(0, m.index).split("\n").length}`);
        }
      }
    }
    expect(offenders, "Use the on-selected ink family, or a lower rung.").toEqual([]);
  });
});
