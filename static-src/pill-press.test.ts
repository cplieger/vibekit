// Cascade guard for the pill press (03-base.css + 15-input.css).
//
// The universal press rule declares `transform: scale(0.96)` AND a
// `transition: transform …`, which reads as self-contained and is not. Its
// selector is `:where(… .pill …):active`: `:where()` contributes 0, so the whole
// thing scores (0,1,0) — exactly what a bare `.pill` scores. Both stylesheets are
// UNLAYERED (only 02-reset, 30-utilities and 50-mobile open a layer, whatever the
// MANIFEST comment says), so the tie is broken by concatenation order, and the
// MANIFEST puts 15-input.css after 03-base.css. `.pill`'s own `transition`
// therefore wins in the :active state, and until this fix it did not name
// `transform`: the scale applied with no transition either way, which is a
// one-frame snap on press and on release rather than an animation.
//
// A computed-style test cannot hold this. happy-dom does not load the stylesheet
// bundle or implement the cascade over it, and the property under test is which
// of two equal-specificity rules wins — the thing a real browser decides. So this
// asserts the SOURCE fact instead: the winning transition names transform, and a
// press carries a second channel. Both are grep-checkable and both fail when the
// declaration is removed.
//
// Skipped under Stryker: its sandbox copies static-src, and these files are in it,
// but the mutator has nothing to mutate in CSS — the guard is a source read.

import { describe, it, expect } from "vitest";

async function loadCSS(name: string): Promise<string> {
  const { readFileSync } = await import("node:fs");
  const { dirname, join } = await import("node:path");
  const { fileURLToPath } = await import("node:url");
  const here = dirname(fileURLToPath(import.meta.url));
  return readFileSync(join(here, "css", name), "utf8");
}

/** The body of a top-level rule, by its exact selector line. Nested `&` blocks
 *  are included, which is what we want: `.pill`'s declarations and its nested
 *  states are one authored unit. */
function ruleBody(css: string, selector: string): string {
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

/** The `transition:` declaration of a rule body, excluding nested blocks — a
 *  nested `&:hover`/`& svg` can carry its own and must not be mistaken for the
 *  rule's. */
function ownTransition(body: string): string {
  const flat = body.replace(/&[^{]*\{[^{}]*\}/g, "").replace(/&[^{]*\{[\s\S]*?\n {2}\}/g, "");
  const m = /transition:([^;]*);/.exec(flat);
  expect(m, "rule declares no transition").not.toBeNull();
  return m?.[1] ?? "";
}

describe("the pill press animates", () => {
  it("names transform in the transition that actually wins", async () => {
    const css = await loadCSS("15-input.css");
    const transition = ownTransition(ruleBody(css, ".pill"));
    expect(
      /\btransform\b/.test(transition),
      `.pill's own transition must name transform, or the universal press scale
applies with no transition in either direction. Its list is the one that wins the
(0,1,0) tie against 03-base.css's :where(…):active. Got: ${transition.trim()}`,
    ).toBe(true);
  });

  // The scale alone is not enough on the narrowest control in the row. The
  // chat-actions pill is icon-only at roughly 30px, so 4% is ~1.2px of travel —
  // real once it animates, but not readable as a press. The second channel is
  // shared by every pill rather than a bigger scale factor on one of them, which
  // would make that pill press differently from its five neighbours.
  it("carries a colour channel on :active, shared by every pill", async () => {
    const css = await loadCSS("15-input.css");
    const body = ruleBody(css, ".pill:active");
    expect(/background:/.test(body) || /border-color:/.test(body)).toBe(true);
    // No transition of its own: `.pill`'s list already covers background,
    // border-color and transform, and a second list here would re-create exactly
    // the shadowing that broke the scale.
    expect(/transition:/.test(body)).toBe(false);
  });

  // Source order is the whole tiebreak between `.pill:active` and
  // `.pill[aria-pressed="true"]` — both score (0,2,0) — so a toggled-on pill only
  // presses because the press rule comes after it.
  it("puts the press channel after the aria-pressed rule", async () => {
    const css = await loadCSS("15-input.css");
    expect(css.indexOf('.pill[aria-pressed="true"] {')).toBeLessThan(css.indexOf(".pill:active {"));
  });

  // The universal rule must keep listing `.pill`: it is what supplies the scale,
  // and the transition fix above is meaningless without a transform to animate.
  it("keeps .pill in the universal press selector", async () => {
    const css = await loadCSS("03-base.css");
    const at = css.indexOf(":where(");
    const close = css.indexOf("):active", at);
    expect(css.slice(at, close)).toContain(".pill,");
    expect(css.slice(close, close + 200)).toContain("scale(0.96)");
  });

  // The tie only exists because neither file is layered. If 15-input.css ever
  // gains an `@layer components` wrapper, the winning rule changes and the
  // reasoning above stops holding — so the assumption is asserted rather than
  // left in a comment.
  it("keeps both files unlayered, which is what makes source order decide", async () => {
    for (const name of ["03-base.css", "15-input.css"]) {
      const css = await loadCSS(name);
      expect(/^@layer\s+[\w-]+\s*\{/m.test(css), `${name} opened a layer`).toBe(false);
    }
  });
});
