// ---------------------------------------------------------------------------
// The run page's LAYOUT contract, as CSS rules rather than as rendered pixels.
//
// Why a stylesheet test and not a DOM one: the defect this pins was invisible to
// every DOM test and to every screenshot taken at the component level. `#run-view`
// was simply absent from the list of views that claim the height `#chat-area`
// gives them (16-login.css), so a chain of individually-correct rules resolved to
// a `.page-content` 32px tall holding 565px of content. Nothing threw, nothing
// logged, and the card rendered perfectly inside a collapsed wrapper — measured on
// a 1440x900 desktop before the fix.
//
// A membership omission in a shared selector list cannot be caught by testing the
// member; it can only be caught by asserting the membership. So that is what these
// cases do, plus the three rules that make the page a page rather than a tall box.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { loadCSS, ruleBody } from "./__test-helpers__/css-rules.js";

describe("the run page claims its height", () => {
  // The omission itself. Every full-page view is in this list; a new one that is
  // not renders inside a wrapper with no height and looks like a styling bug.
  //
  // Keyed on the WHOLE selector list rather than through `ruleContaining`, because
  // every member appears twice at top level in this file — once here and once in
  // its own `view-transition-name` rule — and that helper requires a unique match.
  // Spelling the list out is also what makes the MEMBERSHIP the assertion:
  // reordering it is fine, dropping a member is not.
  const heightSet = [
    '[id="chat-view"]',
    '[id="settings-view"]',
    '[id="git-view"]',
    '[id="editor-view"]',
    '[id="files-view"]',
    '[id="history-view"]',
    '[id="run-view"]',
    '[id="subagent-view"]',
    '[id="docs-view"]',
  ];

  it("puts run-view in the view-height set with every other full-page view", () => {
    const body = ruleBody(loadCSS("16-login.css"), heightSet.join(",\n"));
    expect(/flex:\s*1/.test(body)).toBe(true);
    // Without `min-height: 0` a flex child refuses to shrink below its content, so
    // the growing region would push the pinned bottom bar off-screen instead of
    // scrolling.
    expect(/min-height:\s*0/.test(body)).toBe(true);
  });

  // The page wrapper is NOT the scroller: each pane scrolls itself, so the header,
  // the timeline and the results row hold their positions while the tree and the
  // detail move independently. Two nested scrollports would fight.
  it("scrolls the panes, not the page wrapper", () => {
    const pages = ruleBody(loadCSS("18-pages.css"), '[id="run-view"] .page-content');
    expect(/overflow:\s*hidden/.test(pages)).toBe(true);
    expect(pages).not.toMatch(/overflow-y:\s*auto/);

    const pane = ruleBody(loadCSS("31-exec-view.css"), ".ev-pane");
    expect(/overflow-y:\s*auto/.test(pane)).toBe(true);
  });

  // The page has to be a column that FILLS, or the panes below have nothing to grow
  // inside and the whole chain is inert.
  it("makes the exec page a filling column", () => {
    const page = ruleBody(loadCSS("31-exec-view.css"), ".ev-page");
    expect(/display:\s*flex/.test(page)).toBe(true);
    expect(/flex-direction:\s*column/.test(page)).toBe(true);
    expect(/flex:\s*1 1 0/.test(page)).toBe(true);
    expect(/min-height:\s*0/.test(page)).toBe(true);
  });

  // The panes are the primary content and the results disclosure below them is a flex
  // sibling, so without a FLOOR opening a run with three reports crushed the tree and
  // the detail pane to ~90px — measured, and it made the page unusable at the moment a
  // reader was trying to read it.
  it("floors the panes so the results region cannot crush them", () => {
    const panes = ruleBody(loadCSS("31-exec-view.css"), ".ev-panes");
    expect(/min-block-size:\s*\d/.test(panes)).toBe(true);
    expect(/flex:\s*1 1 0/.test(panes)).toBe(true);
  });

  // A container's state is a ROLL-UP of its children and must never repaint them.
  // `.ev-kids` nests inside `.ev-row`, so a descendant selector here reached every
  // glyph in the subtree: measured on a running repeat, both finished children rendered
  // a spinning ring beside their own check character.
  it("keeps every tree state rule on the child combinator", () => {
    const css = loadCSS("31-exec-view.css");
    const selectors = [...css.matchAll(/^\.ev-row\[data-state=[^\n{]*$/gm)].map((m) => m[0]);
    expect(selectors.length).toBeGreaterThan(0);
    for (const sel of selectors) {
      expect(sel, `${sel} must not be a descendant selector`).toContain("> .ev-row-main");
    }
  });

  // `#run-body` sits between the wrapper and the page, so it has to pass the height
  // through rather than sizing to content.
  it("passes the height through the run body host", () => {
    const host = ruleBody(loadCSS("18-pages.css"), '[id="run-body"]');
    expect(/flex:\s*1 1 0/.test(host)).toBe(true);
    expect(/min-height:\s*0/.test(host)).toBe(true);
  });

  // The page is not prose, so it gets its own measure — and a BOUNDED one, because
  // a step row puts its duration at `margin-inline-start: auto`.
  it("gives the page its own bounded measure rather than the reading one", () => {
    expect(
      /max-width:\s*var\(--run-page-max-w\)/.test(
        ruleBody(loadCSS("18-pages.css"), '[id="run-view"] .page-content'),
      ),
    ).toBe(true);
    expect(loadCSS("01-tokens.css")).toMatch(/--run-page-max-w:\s*\d/);
  });
});
