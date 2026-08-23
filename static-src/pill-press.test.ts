// Cascade guard for the pill press (03-base.css + 15-input.css + 70-selection.css).
//
// The press used to be a 4% scale transform supplied by a universal
// `:where(… .pill …):active` rule. It is a COLOUR step now — one rung deeper
// than the pill's own hover, on the same axis — because that is one mechanism
// instead of two and because it survives prefers-reduced-motion, which a
// transform does not. `.pill` has left the universal scale list, so this file
// pins the new contract; what carried over unchanged is the CASCADE reasoning,
// which is the part that is easy to get wrong and invisible when it breaks.
//
// The trap, restated for the property that now matters: the universal press
// selector scores (0,1,0) — `:where()` contributes 0 and `:active` is one class
// level — exactly what a bare `.pill` scores. Both stylesheets are UNLAYERED, so
// the tie is broken by concatenation order, and the MANIFEST puts 15-input.css
// after 03-base.css. `.pill`'s own `transition` list is therefore the one that
// wins in the :active state, and it must name whatever property the press
// animates or the press lands in a single frame.
//
// A computed-style test cannot hold this. The test page does not load the stylesheet
// bundle or implement the cascade over it, and the property under test is which
// of two equal-specificity rules wins — the thing a real browser decides. So
// this asserts the SOURCE fact instead. Every assertion here fails when its
// declaration is removed.

import { describe, it, expect } from "vitest";
// The stylesheet reader is shared with tab-dot.test.ts (see the note there):
// no app stylesheet is loaded, so both suites assert SOURCE facts, and a
// second copy of a brace-matching parser is the duplication to avoid.
import { loadCSS, ruleBody, ruleContaining } from "./__test-helpers__/css-rules.js";

/** The `transition:` declaration of a rule body, excluding nested blocks — a
 *  nested `&:hover`/`& svg` can carry its own and must not be mistaken for the
 *  rule's. */
function ownTransition(body: string): string {
  const flat = body.replace(/&[^{]*\{[^{}]*\}/g, "").replace(/&[^{]*\{[\s\S]*?\n {2}\}/g, "");
  const m = /transition:([^;]*);/.exec(flat);
  expect(m, "rule declares no transition").not.toBeNull();
  return m?.[1] ?? "";
}

describe("the pill press is a colour step, and it animates", () => {
  it("names background in the transition that actually wins", async () => {
    const css = loadCSS("15-input.css");
    const transition = ownTransition(ruleBody(css, ".pill"));
    expect(
      /\bbackground\b/.test(transition),
      `.pill's own transition must name background, or the press colour lands in
one frame. Its list is the one that wins the (0,1,0) tie against 03-base.css's
:where(…):active. Got: ${transition.trim()}`,
    ).toBe(true);
  });

  it("presses one rung deeper than it hovers", async () => {
    const css = loadCSS("15-input.css");
    // Rest is --c-bg-primary, hover --c-bg-tertiary, press --c-bg-elevated: three
    // rungs of the one surface ramp, so the press is visibly past the hover
    // instead of being a differently-named token that happens to be nearby.
    expect(/background: var\(--c-bg-tertiary\)/.test(ruleBody(css, ".pill"))).toBe(true);
    const press = ruleBody(css, ".pill:active");
    expect(/background: var\(--c-bg-elevated\)/.test(press)).toBe(true);
    // No transition of its own: `.pill`'s list already covers background and
    // border-color, and a second list here would re-create exactly the shadowing
    // that broke the old scale.
    expect(/transition:/.test(press)).toBe(false);
  });

  it("keeps .pill OUT of the universal scale list", async () => {
    const css = loadCSS("03-base.css");
    const at = css.indexOf(":where(");
    const close = css.indexOf("):active", at);
    const list = css.slice(at, close);
    expect(
      list,
      "A press signalled by both a scale and a colour step is two mechanisms for " +
        "one state, which is what the interaction ladder replaced.",
    ).not.toContain(".pill,");
    // The rule still exists for the controls that have not been converted.
    expect(css.slice(close, close + 200)).toContain("scale(0.96)");
  });

  it("lets the toggled-on press win on specificity, not on file order", async () => {
    // `.pill[aria-pressed="true"]` moved to 70-selection.css with every other
    // selected surface, so the old same-file ordering dependency is gone: its
    // press scores (0,3,0) against `.pill:active`'s (0,2,0) and wins wherever
    // either file sits. Without the press declaration a toggled-on pill would
    // show the resting selected fill under the finger.
    //
    // This asserts the selector and the declaration are in the SAME RULE. Two
    // `toContain`s over the whole file would have passed with the selector
    // listed on the resting rule and the press token used by something else
    // entirely — which is the shape a refactor of a 20-selector list produces,
    // and it is invisible in a diff.
    const sel = loadCSS("70-selection.css");
    const press = ruleContaining(sel, '.pill[aria-pressed="true"]:active');
    expect(
      /background:\s*var\(--c-selected-bg-press\)/.test(press.body),
      `The rule listing .pill[aria-pressed="true"]:active must be the one that
sets the press fill. Got body: ${press.body.trim().slice(0, 200)}`,
    ).toBe(true);
  });

  it("puts each selected state's selector in the rule that sets that state", async () => {
    // The same association for the other two rungs, so a selector cannot drift
    // between the resting, hover and press lists unnoticed. `.pill-role-item`
    // is the one member that appears in all three plus a :focus-visible.
    const sel = loadCSS("70-selection.css");
    for (const [selector, decl] of [
      [".pill-role-item.active", "background: var(--c-selected-bg)"],
      [".pill-role-item.active:hover", "background: var(--c-selected-bg-hover)"],
      [".pill-role-item.active:active", "background: var(--c-selected-bg-press)"],
    ] as const) {
      const rule = ruleContaining(sel, selector);
      expect(
        rule.body.includes(decl),
        `${selector} is listed on a rule that does not declare ${decl}`,
      ).toBe(true);
    }
  });

  it("colours the metadata a selected row's ink cannot reach", async () => {
    // `color` on the parent reaches only what INHERITS it, and every one of
    // these declares its own. Measured on the selected fill they ran 1.8-3.7:1,
    // so the row the consolidation exists to make legible had illegible
    // metadata. Assert the descendant rules exist and take an on-selected ink,
    // because the failure mode is silent: the row still looks selected.
    const sel = loadCSS("70-selection.css");
    for (const selector of [
      ".pill-model-item.active .pill-model-meta",
      ".pill-role-item.active .pill-role-scope",
      ".popup-item.active .popup-meta",
      ".picker-btn.active .picker-meta",
      ".fb-row.fb-row-selected .fb-meta",
    ]) {
      const rule = ruleContaining(sel, selector);
      expect(
        /color:\s*var\(--c-selected-[\w-]+-fg\)/.test(rule.body),
        `${selector} must take an on-selected ink, not the row's inherited colour`,
      ).toBe(true);
    }
  });

  it("keeps both files unlayered, which is what makes source order decide", async () => {
    for (const name of ["03-base.css", "15-input.css"]) {
      const css = loadCSS(name);
      expect(/^@layer\s+[\w-]+\s*\{/m.test(css), `${name} opened a layer`).toBe(false);
    }
  });
});
