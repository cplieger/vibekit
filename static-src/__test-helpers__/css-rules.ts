// Reading the SHIPPED stylesheets as source, for the guards that cannot be
// written any other way.
//
// Two jobs. `appCSS`/`mountAppCSS` assemble the bundle so a suite can measure
// real LAYOUT against it. The three readers below assert SOURCE facts — which of
// two rules applies, whether a declaration belongs to a given selector — which
// computed style cannot answer: a synthetic hover drives no style recalc, and
// `CSS.forcePseudoState` is a devtools protocol call rather than something a test
// page can make.
//
// The stylesheets arrive as Vite `?raw` imports rather than `node:fs` reads, so
// this helper — and the suites that import it — run in the browser project.
// `import.meta.glob` is eager, so every sheet is inlined at transform time and
// `loadCSS` is a map lookup.
//
// Extracted from pill-press.test.ts when a second suite (tab-dot.test.ts) needed
// `ruleContaining`. The alternative was a second copy of a brace-matching
// parser, which is the one kind of duplication a test helper directory exists to
// prevent — the same reason the MANIFEST assembly moved here from
// disclosure-row-css.test.ts when turn-header.test.ts needed it.

import { expect } from "vitest";
import manifest from "../css/MANIFEST?raw";

const sheets = import.meta.glob<string>("../css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

// The manifest also pulls the ui-primitives base in from node_modules, and
// `import.meta.glob` needs a static pattern per directory.
const vendor = import.meta.glob<string>("../node_modules/@cplieger/ui-primitives/css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

/** One stylesheet from css/, by filename. */
export function loadCSS(name: string): string {
  const hit = sheets[`../css/${name}`];
  if (hit === undefined) {
    throw new Error(`no stylesheet ../css/${name}`);
  }
  return hit;
}

/** The shipped stylesheet, assembled from `css/MANIFEST` in declared order the
 *  way `cmd/bundle` concatenates it — which is the cascade, since equal-
 *  specificity ties in this app are decided by that order rather than by the
 *  selectors. Reading the built `static/style.css` instead would test a
 *  gitignored artifact that need not exist. */
function appCSS(): string {
  const names = manifest
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#"));
  // An entry is relative to `css/`, so one that climbs out of it (the
  // ui-primitives base) resolves against the package.
  const text = (entry: string): string | undefined =>
    entry.startsWith("../") ? vendor[`../${entry.slice(3)}`] : sheets[`../css/${entry}`];

  const missing = names.filter((n) => text(n) === undefined);
  expect(missing, "every MANIFEST entry resolves to a stylesheet").toEqual([]);
  return names.map((n) => text(n)).join("\n");
}

/** Install the shipped stylesheet into the test page, for a suite that measures
 *  real LAYOUT. Remove the returned element in `afterAll`. A suite asserting a
 *  SOURCE fact (which of two rules applies, whether a declaration belongs to a
 *  selector) wants `loadCSS` plus the readers below instead. */
export function mountAppCSS(): HTMLStyleElement {
  const style = document.createElement("style");
  style.textContent = appCSS();
  document.head.appendChild(style);
  return style;
}

/** Every style rule in the sheet, at-rule bodies included, as
 *  (selector, body) pairs. For sweeps over a whole vocabulary ("no reasoning
 *  selector carries an animation") where keying on one exact selector would
 *  miss the rule that regressed. A rule's body includes its nested blocks. */
export function allRules(css: string): { selector: string; body: string }[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, " ");
  const out: { selector: string; body: string }[] = [];
  const scan = (src: string): void => {
    let depth = 0;
    let selStart = 0;
    let bodyStart = 0;
    let sel = "";
    for (let i = 0; i < src.length; i++) {
      if (src[i] === "{") {
        if (depth === 0) {
          sel = src.slice(selStart, i).trim();
          bodyStart = i + 1;
        }
        depth++;
      } else if (src[i] === "}") {
        depth--;
        if (depth === 0) {
          const body = src.slice(bodyStart, i);
          if (sel.startsWith("@")) {
            scan(body);
          } else {
            out.push({ selector: sel, body });
          }
          selStart = i + 1;
        }
      }
    }
  };
  scan(text);
  return out;
}

/** The body of a top-level rule, by its exact selector line. Nested `&` blocks
 *  are included, which is what we want: a rule's declarations and its nested
 *  states are one authored unit. */
export function ruleBody(css: string, selector: string): string {
  const at = css.indexOf(`\n${selector} {`);
  expect(at, `rule not found: ${selector}`).toBeGreaterThan(-1);
  const open = css.indexOf("{", at);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") {
      depth++;
    }
    if (css[i] === "}") {
      depth--;
      if (depth === 0) {
        return css.slice(open + 1, i);
      }
    }
  }
  throw new Error(`unbalanced braces after ${selector}`);
}

/**
 * The rule whose SELECTOR LIST contains `selector`, with its body.
 *
 * This is the association `ruleBody` cannot make: `ruleBody` keys on an exact
 * selector line, and 70-selection.css's rules are 20-plus selectors long, so
 * pinning one member of such a list means finding the rule it belongs to and
 * reading THAT rule's declarations. Asserting a selector and a declaration
 * appear in the same FILE proves nothing about whether the declaration applies
 * to the selector.
 *
 * `@media` wrappers are descended into, since the selected hover and the
 * reduced-motion dot both live in one. `scope` says WHERE to look, and it has to
 * be sayable because a selector can legitimately appear twice — once at top
 * level and once inside `@media (prefers-reduced-motion)` — and the point of the
 * pair is that the two bodies differ:
 *
 *   "*"    anywhere (the default; a rule appearing once, wherever it sits)
 *   "top"  outside every at-rule
 *   other  inside an at-rule whose prelude contains this substring
 *
 * Exactly one match is required either way, so a selector that gained a second
 * home is a failure rather than a silently-picked first hit.
 */
export function ruleContaining(
  css: string,
  selector: string,
  scope: string = "*",
): { selector: string; body: string } {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, " ");
  const found: { selector: string; body: string }[] = [];

  const scan = (src: string, inScope: boolean): void => {
    let depth = 0;
    let selStart = 0;
    let bodyStart = 0;
    let sel = "";
    for (let i = 0; i < src.length; i++) {
      if (src[i] === "{") {
        if (depth === 0) {
          sel = src.slice(selStart, i).trim();
          bodyStart = i + 1;
        }
        depth++;
      } else if (src[i] === "}") {
        depth--;
        if (depth === 0) {
          const body = src.slice(bodyStart, i);
          if (sel.startsWith("@")) {
            // "*" keeps whatever scope we were in; "top" leaves it on the way in;
            // a named scope is entered when this at-rule's prelude matches.
            const inner =
              scope === "*" ? inScope : scope === "top" ? false : inScope || sel.includes(scope);
            scan(body, inner);
          } else if (inScope) {
            const members = sel.split(",").map((s) => s.trim());
            if (members.includes(selector)) {
              found.push({ selector: sel, body });
            }
          }
          selStart = i + 1;
        }
      }
    }
  };
  scan(text, scope === "*" || scope === "top");

  expect(
    found.length,
    `expected exactly one rule listing ${selector} (scope: ${scope}), found ${found.length}`,
  ).toBe(1);
  const [only] = found;
  if (only === undefined) {
    // The expect above has already failed the test by the time this can run.
    // It is here because `expect` is not a type guard, and stating the invariant
    // as a throw is what lets the return type be the rule rather than a maybe.
    throw new Error(`no rule listing ${selector} (scope: ${scope})`);
  }
  return only;
}
