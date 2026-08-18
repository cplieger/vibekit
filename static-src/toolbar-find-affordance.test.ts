// The toolbar's find control: when it is drawn, which glyph it draws, and how it
// leaves. A stylesheet reader plus a DOM half, because the two halves fail
// separately — the collapse is CSS the app cannot observe, and the glyph swap is
// DOM the stylesheet knows nothing about.
//
// WHY IT COLLAPSES RATHER THAN HIDING. `#find-btn` was painted unconditionally
// with a fixed magnifier, so on `/settings`, on a run view, on the git view's
// Sources tab and on an editor tab showing a diff, an image or rendered markdown
// it was a control that did nothing — and on `/docs` and the git panels it
// promised a search over a box that only filters. Both halves are audited here.
import { describe, it, expect } from "vitest";
import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { findGlyph } from "./icons.js";

/** Comments quote the values they explain, so they are stripped before a scan. */
function body(sheet: string, selector: string): string {
  return ruleContaining(loadCSS(sheet), selector, "top").body.replace(/\/\*[\s\S]*?\*\//g, " ");
}

describe("the collapse is animated, not a display swap", () => {
  const collapsed = body("12-chat.css", ".chat-toolbar > .icon-btn.is-collapsed");
  const base = body("12-chat.css", ".chat-toolbar > .icon-btn");

  it("never reaches for display, which cannot animate", () => {
    // `.hidden` is `display: none !important` (40-a11y.css) and `display` is a
    // discrete property: the button would vanish in one frame and the bar's width
    // would jump. 26-dock.css made the same choice for the same reason.
    expect(collapsed).not.toMatch(/display:/);
    expect(collapsed).not.toMatch(/!important/);
  });

  it("takes the box, the ink AND the gap to zero", () => {
    // Three, not one. `min-inline-size` because the toolbar sets a floor of
    // `--btn-h` that would otherwise hold the width open, and the negative margin
    // because `gap` is drawn between flex items whatever their width — so a
    // zero-width button still costs the row 2px.
    expect(collapsed, "the box").toMatch(/inline-size:\s*0/);
    expect(collapsed, "the floor the toolbar sets").toMatch(/min-inline-size:\s*0/);
    expect(collapsed, "the ink").toMatch(/opacity:\s*0/);
    expect(collapsed, "the gap it is still entitled to").toMatch(
      /margin-inline-start:\s*calc\(-1 \* var\(--toolbar-gap\)\)/,
    );
  });

  it("derives that gap from the one the toolbar declares", () => {
    // Named once, consumed twice, so the collapse cannot drift from the row.
    const toolbar = body("12-chat.css", ".chat-toolbar");
    expect(toolbar).toMatch(/--toolbar-gap:/);
    expect(toolbar).toMatch(/gap:\s*var\(--toolbar-gap\)/);
  });

  it("leaves the accessibility tree and the tab order, at the END of the fade", () => {
    // `visibility` holds `visible` for the whole transition when either endpoint
    // is visible, flipping only at the finish — so the fade plays out and THEN the
    // control stops existing for a keyboard or a screen reader. A zero-width
    // transparent button that still took focus would be the worst of both.
    expect(collapsed).toMatch(/visibility:\s*hidden/);
    expect(collapsed).toMatch(/pointer-events:\s*none/);
    expect(base, "visibility must be in the transition or it flips at once").toMatch(
      /visibility var\(--dur-exit\)/,
    );
  });

  it("transitions every property it changes", () => {
    for (const prop of ["opacity", "inline-size", "min-inline-size", "margin-inline-start"]) {
      expect(base, prop).toContain(`${prop} var(--dur-exit)`);
    }
  });

  it("keeps the press and hover transitions it would otherwise steal", () => {
    // The trap 03-base.css documents: `:where(…):active` scores (0,1,0) and is
    // unlayered, so a more specific unlayered rule TAKES the whole `transition`
    // property from it. Dropping these three would leave the press scale and the
    // hover wash snapping on every toolbar button, not just the collapsible one.
    expect(base, "the press scale").toMatch(/transform var\(--dur-micro\)/);
    expect(base, "the hover wash").toMatch(/background var\(--dur-micro\)/);
    expect(base, "the hover ink").toMatch(/color var\(--dur-micro\)/);
  });
});

describe("the glyph says which of the two a page has", () => {
  it("draws a magnifier for a search and a funnel for a filter", () => {
    expect(findGlyph("search", 18)).toContain("<circle");
    expect(findGlyph("search", 18)).not.toContain("<polygon");
    expect(findGlyph("filter", 18)).toContain("<polygon");
    expect(findGlyph("filter", 18)).not.toContain("<circle");
  });

  it("draws them at the caller's size, so the toolbar and the box agree by construction", () => {
    // One producer for both consumers — the button (18) and the field's leading
    // glyph (14) — is what stops a page promising a search and opening a filter.
    expect(findGlyph("search", 18)).toContain('width="18"');
    expect(findGlyph("filter", 14)).toContain('width="14"');
  });
});
