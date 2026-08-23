// Reading the SHIPPED stylesheets as source, for the guards that cannot be
// written any other way.
//
// These suites assert SOURCE facts — which of two rules applies, whether a
// declaration belongs to a given selector — rather than computed ones, because
// the test page loads no app stylesheet: nothing links `css/MANIFEST`, so
// `getComputedStyle` has no cascade to report on. These three helpers are that
// reader. (Before Browser Mode the reason was happy-dom, which implemented no
// cascade at all and returned the last declaration parsed rather than the
// winner. Asserting the computed fact in Chromium instead is a real
// opportunity, and a separate piece of work from this migration.)
//
// The stylesheets arrive as Vite `?raw` imports rather than `node:fs` reads, so
// this helper — and the eleven suites that import it — run in the browser
// project. `import.meta.glob` is eager, so every sheet is inlined at transform
// time and `loadCSS` is a map lookup.
//
// Extracted from pill-press.test.ts when a second suite (tab-dot.test.ts) needed
// `ruleContaining`. The alternative was a second copy of a brace-matching
// parser, which is the one kind of duplication a test helper directory exists to
// prevent.

import { expect } from "vitest";

const sheets = import.meta.glob<string>("../css/*.css", {
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
