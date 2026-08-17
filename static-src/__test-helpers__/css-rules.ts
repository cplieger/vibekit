// Reading the SHIPPED stylesheets as source, for the guards that cannot be
// written any other way.
//
// happy-dom does not implement the cascade — `getComputedStyle` returns the last
// declaration parsed, not the winner — so a test that needs to know which of two
// rules applies, or whether a declaration belongs to a given selector, has to
// assert the SOURCE fact. These three helpers are that reader.
//
// Extracted from pill-press.test.ts when a second suite (tab-dot.test.ts) needed
// `ruleContaining`. The alternative was a second copy of a brace-matching
// parser, which is the one kind of duplication a test helper directory exists to
// prevent.

import { expect } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

/** One stylesheet from css/, by filename. */
export function loadCSS(name: string): string {
  const here = dirname(fileURLToPath(import.meta.url));
  return readFileSync(join(here, "..", "css", name), "utf8");
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
  return found[0];
}
