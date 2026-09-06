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
// cases do, plus the rules that make the page a page rather than a tall box.
//
// The SECOND defect pinned here is of the same family and needed a different
// mechanism: one selector carried two contradictory blocks in one file at equal
// specificity, so the later declaration silently won and `ruleBody` — which returns
// the first match — could not see it. Counting occurrences is the only thing that
// can, so `allRules` is used for that pair rather than `ruleBody`.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { loadCSS, ruleBody, allRules } from "./__test-helpers__/css-rules.js";

/** A rule's DECLARATIONS, with its comments stripped. Required by every negative
 *  assertion below: `ruleBody` returns the authored text, and each of these rules
 *  explains in prose which declaration it no longer carries — so a bare
 *  `not.toMatch(/overflow: hidden/)` reads the comment and fails on the fix. */
function decls(css: string, selector: string): string {
  return ruleBody(css, selector).replace(/\/\*[\s\S]*?\*\//g, " ");
}

/** Every `overflow*` declaration a rule carries, in source order.
 *
 *  The EXACT set rather than a substring match, because both defects this pins are
 *  invisible to one: `overflow: hidden` is a prefix of `overflow: hidden auto`, so a
 *  negative substring assertion fails on the correct value, and two competing
 *  declarations in one rule is precisely the shape that shipped. */
function overflowDecls(css: string, selector: string): string[] {
  return [...decls(css, selector).matchAll(/^\s*(overflow[a-z-]*\s*:[^;]+);/gm)].map((m) =>
    (m[1] ?? "").replace(/\s+/g, " ").trim(),
  );
}

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

  // THE PAGE SCROLLS, and it is `.page-content` that scrolls rather than the view,
  // because `.run-bottom-bar` is pinned outside it. Inverted from what this case used
  // to assert: the panes each carried their own scrollport, which is what crushed a
  // run's content into fixed cages and trapped it there.
  it("scrolls the page wrapper, not the panes", () => {
    // `overflow: hidden auto` is the two-value shorthand: `hidden` across, `auto`
    // down. Exactly one declaration, so the retired `overflow: hidden` cannot be
    // sitting beside it.
    expect(overflowDecls(loadCSS("18-pages.css"), '[id="run-view"] .page-content')).toEqual([
      "overflow: hidden auto",
    ]);

    // A pane with an `overflow` on either axis is a scroll container again, and a
    // scroll container's automatic minimum size is 0 — the defect the retired
    // `.ev-r-val` recorded.
    expect(overflowDecls(loadCSS("31-exec-view.css"), ".ev-pane")).toEqual([]);
  });

  // The second consumer of the same exec page. It has no bottom bar, so it could
  // have scrolled the view instead — but leaving it `overflow: hidden` while
  // `.ev-page` stops clamping itself would clip a delegate's transcript outright.
  it("scrolls the subagent page too", () => {
    expect(overflowDecls(loadCSS("18-pages.css"), '[id="subagent-view"] .page-content')).toEqual([
      "overflow: hidden auto",
    ]);
  });

  // THE GUARD for the defect this batch fixed: `[id="run-view"] .page-content` carried
  // TWO contradictory blocks 63 lines apart — `overflow: hidden` with a comment reading
  // "NOT the scroller", then `overflow-y: auto` calling itself "this scroller" — at
  // equal specificity in one file, so the later declaration silently won.
  //
  // `ruleBody` cannot catch this: it returns the FIRST rule with that selector line,
  // which is why the contradiction never failed a test. Counting the occurrences is
  // the only mechanism that would have, and it will catch the next one.
  it.each(['[id="run-view"] .page-content', '[id="subagent-view"] .page-content'])(
    "declares %s exactly once",
    (selector) => {
      const hits = allRules(loadCSS("18-pages.css")).filter((r) => r.selector === selector);
      expect(hits).toHaveLength(1);
    },
  );

  // The page must SIZE TO CONTENT, or the scroller above has nothing taller than
  // itself to scroll: `flex: 1 1 0` plus `min-height: 0` made this column exactly the
  // free space of its parent, so `scrollHeight === clientHeight` and no scrollbar
  // could ever appear.
  it("makes the exec page a content-sized column", () => {
    const page = ruleBody(loadCSS("31-exec-view.css"), ".ev-page");
    expect(/display:\s*flex/.test(page)).toBe(true);
    expect(/flex-direction:\s*column/.test(page)).toBe(true);
    expect(/flex:\s*1 0 auto/.test(page)).toBe(true);
  });

  // The floor this replaced (`min-block-size: 16rem`) existed because the results
  // disclosure competed with the panes for a fixed page height and crushed them to
  // ~90px when opened. With nothing clamped to the scrollport there is no
  // competition, so the panes size to content and each shrink-wraps its own.
  it("sizes the panes to their content rather than flooring them", () => {
    const panes = decls(loadCSS("31-exec-view.css"), ".ev-panes");
    expect(/flex:\s*0 0 auto/.test(panes)).toBe(true);
    expect(panes).not.toMatch(/min-block-size:/);
    expect(/align-items:\s*start/.test(panes)).toBe(true);
  });

  // The roll-up is the run's PRODUCT, so it renders whole and the page scrolls. A
  // `max-block-size` here was a peek sized for scanning, and it only existed because
  // the region was competing for a fixed height.
  it("uncages the results region", () => {
    expect(decls(loadCSS("31-exec-view.css"), ".ev-r-body")).not.toMatch(/max-block-size:/);
    expect(overflowDecls(loadCSS("31-exec-view.css"), ".ev-r-body")).toEqual([]);
  });

  // A per-result box may carry NO `overflow` on either axis: an `auto` on one side
  // makes it a scroll container, whose automatic minimum size is 0, which is exactly
  // how a 331px report ended up rendered in a 16px box.
  it("leaves a per-result box's overflow at its initial value", () => {
    expect(overflowDecls(loadCSS("31-exec-view.css"), ".ev-r-item-body")).toEqual([]);
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

  // `#run-body` sits between the scroller and the page, so it has to SIZE TO CONTENT
  // like everything else below the scroller. `flex: 1 0 auto` is this file's
  // established page-scrolling shape (see the `[id="git-view"] > .page-content`
  // group); `flex-basis: 0` plus `min-height: 0` is what pinned it to the scrollport.
  it.each(['[id="run-body"]', '[id="subagent-body"]'])("sizes %s to its content", (selector) => {
    const host = decls(loadCSS("18-pages.css"), selector);
    expect(/flex:\s*1 0 auto/.test(host)).toBe(true);
    expect(host).not.toMatch(/min-height:\s*0/);
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
