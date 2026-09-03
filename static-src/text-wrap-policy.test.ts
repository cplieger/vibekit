// ---------------------------------------------------------------------------
// The app asks for NO `text-wrap: pretty`, anywhere, and this is the guard.
//
// `pretty` optimizes a paragraph's closing lines, so while text is appended the
// tail keeps moving and it revises breaks in lines the reader is already looking
// at. `auto` is greedy and cannot: a break, once chosen, only ever has text added
// after it. Measured in Chromium 151 over 1,408 appends (3 paragraphs x 4 widths
// x 3 chunk sizes, appended as the reveal buffer's chunk spans): `pretty` moved
// an on-screen break on 40 appends and a settled line's break on 7, `auto` on
// zero of either. Against that it changed the rendered height of NONE of the 51
// prose elements on a live transcript, and 25 of those 51 were `<pre>` machine
// output where a re-chosen break is a downgrade rather than a nicety. Reasoning
// and the engine picture: `web.md`.
//
// A SOURCE guard rather than a rendered one, matching reasoning-live-cue.test.ts:
// the fact being pinned is that no stylesheet carries the declaration, and a
// rendered check would only cover whichever elements a test page happens to
// mount. `balance` is untouched and stays legal — a different value, on static
// headings, with none of the streaming exposure.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { allRules, loadCSS } from "./__test-helpers__/css-rules.js";
import manifest from "./css/MANIFEST?raw";

/** Every sheet the bundle concatenates, by name, excluding the vendored base
 *  (ui-primitives is a dependency, so its declarations are not ours to police). */
function ownSheets(): string[] {
  return manifest
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#") && !l.startsWith("../"));
}

describe("text-wrap policy", () => {
  it("declares `pretty` in no stylesheet, under either spelling", () => {
    const offenders: string[] = [];
    for (const name of ownSheets()) {
      for (const rule of allRules(loadCSS(name))) {
        // The shorthand and the longhand both reach `text-wrap-style`.
        if (/text-wrap(-style)?\s*:\s*pretty/.test(rule.body)) {
          offenders.push(`${name} { ${rule.selector} }`);
        }
      }
    }
    expect(offenders).toEqual([]);
  });

  it("needs no carve-out on form controls, because nothing sets the style", () => {
    // The reset used to spend `text-wrap: wrap` on `:where(input, textarea,
    // select)` purely to undo the body rule — Safari 26 re-runs whole-paragraph
    // optimization on every keystroke, so a draft visibly re-wrapped while
    // typing. With the body rule gone there is nothing to undo, and a reset that
    // resets a default is the line that rots.
    const reset = loadCSS("02-reset.css");
    const control = allRules(reset).find((r) => r.selector === ":where(input, textarea, select)");
    expect(control, "the caret-color rule is still there").toBeDefined();
    expect(control?.body).not.toMatch(/text-wrap/);
  });

  it("leaves `balance` alone", () => {
    // Not a blanket blessing: these are the declarations that existed, all on
    // short static text. A new one wants its own reasoning, not this test's.
    //
    // Was three. `.page-title` was the fourth-to-last and went with the title bar:
    // every view's heading is the bar's `<h1>` now, which is a single nowrap line
    // that ellipsizes, so it has nothing to balance.
    const found: string[] = [];
    for (const name of ownSheets()) {
      for (const rule of allRules(loadCSS(name))) {
        if (/text-wrap(-style)?\s*:\s*balance/.test(rule.body)) {
          found.push(name);
        }
      }
    }
    expect(found.length).toBe(2);
  });
});
