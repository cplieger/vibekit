// The vertical centring of a search bar's buttons, pinned as SOURCE facts in the
// stylesheets AND as a structural fact in the builders.
//
// THE DEFECT. `align-items: center` centres a flex item's BOX. A text node inside
// a flex container becomes an anonymous flex item whose box is its LINE BOX, and a
// line box is only symmetric about the ink when the ink happens to fill it. None
// of the bars' glyphs did: `×` (U+00D7) is a math operator drawn about the math
// axis, which sits near half x-height and well below the cap-band centre; `↑`/`↓`
// (U+2191/U+2193) follow neither the cap nor the x-height band and are drawn to
// each font's own arbitrary extent; and `Aa` was a different family and size
// again. Four sets of metrics in one row, so the offset was platform-dependent by
// construction and no authored value could correct it.
//
// TWO FIXES, ONE PER GLYPH KIND, and the split is the judgement this file pins.
//
//   - A NON-LETTERFORM GLYPH becomes an SVG. A replaced element's box IS its ink
//     box, so centring the box centres the glyph in every font. `line-height: 0`
//     is the other half: a replaced element sits on the baseline, so the line
//     box's STRUT is what oversizes it, and collapsing the strut leaves the
//     glyph's own box. `.tab-close` and `.shell-header-btn` are the same pairing —
//     it is this app's convention for every correctly-centred icon button, and
//     label-centring.test.ts already records the strut reasoning for a replaced
//     element (`.pill-role-icon`).
//
//   - `Aa` STAYS TEXT and takes `text-box: trim-both cap alphabetic` instead. The
//     letters ARE the affordance, where an icon for "match case" would have to be
//     learned. The trim makes the box edges the CAP BAND, so centring the box
//     centres the letterforms with no per-font value — and `A` is a cap while `a`
//     sits on the baseline, so the band is exactly full. That is also why the trim
//     is NOT the answer for the other three: it addresses the cap-to-baseline
//     band, and only a letterform fills it. The composer packet landed the same
//     declaration on the pill labels (15-input.css) for the same reason.
//
// A numeric line-height is not a candidate for either: the offset formula is
// ((ascent - descent)/2 - band/2)em and has no line-height term in it (CSS2.1
// 10.8.1 splits leading symmetrically), so one would move the glyphs by zero.
//
// These read the shipped stylesheets as text, because the test page loads no app
// stylesheet: nothing links `css/MANIFEST`, so `getComputedStyle` has no cascade
// to report on. Source text is the only fact available here.

import { describe, it, expect } from "vitest";
import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import findInChatSrc from "./find-in-chat.ts?raw";
import filesSearchSrc from "./files-search.ts?raw";
import editorFindSrc from "./editor-find.ts?raw";
import searchShellSrc from "./search-shell.ts?raw";
import searchPopupSrc from "./search-popup.ts?raw";

/** The five builders the glyph scan below is the whole population of. */
const searchBuilderSources: Record<string, string> = {
  "find-in-chat.ts": findInChatSrc,
  "files-search.ts": filesSearchSrc,
  "editor-find.ts": editorFindSrc,
  "search-shell.ts": searchShellSrc,
  "search-popup.ts": searchPopupSrc,
};

/** The three search bars, and where each one's button rules live. All three are
 *  in the table on purpose: the SAME defect was in each, so a fix in one would
 *  have left the others wrong and the bars looking different from each other. */
const BARS: { name: string; sheet: string; button: string; caseToggle: string }[] = [
  {
    name: "find in chat",
    sheet: "24-find.css",
    button: ".chat-find-btn",
    caseToggle: ".chat-find-case",
  },
  {
    name: "find in files",
    sheet: "19-files.css",
    button: ".fb-search-btn",
    caseToggle: ".fb-search-case",
  },
  {
    name: "find in file (editor)",
    sheet: "20-editor.css",
    button: ".editor-find-btn",
    caseToggle: ".editor-find-case",
  },
];

/** Strip comments so prose ABOUT a declaration is never read as one — these
 *  rules quote the values they exist to explain. */
function body(sheet: string, selector: string): string {
  return ruleContaining(loadCSS(sheet), selector, "top").body.replace(/\/\*[\s\S]*?\*\//g, " ");
}

describe("search-bar icon buttons", () => {
  it("collapse the strut around their SVG with line-height: 0", () => {
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.button);
      expect(
        decls,
        `${bar.sheet}: ${bar.button} holds a replaced element, so the line box's strut is what ` +
          `oversizes it; line-height: 0 is what leaves the glyph's own box for align-items to centre`,
      ).toMatch(/line-height:\s*0\s*;/);
    }
  });

  it("never carry line-height: 1, the value that looked like the fix", () => {
    // All three bars shipped `line-height: 1`. It moves a glyph band by EXACTLY
    // zero relative to its box centre (the leading is symmetric either way) and
    // shrinks the box while doing it, so it was a change that measured as nothing.
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.button);
      const m = /line-height:\s*([^;]+)/.exec(decls);
      expect(
        m?.[1]?.trim(),
        `${bar.sheet}: ${bar.button} must not restate line-height: 1 as a centring fix`,
      ).toBe("0");
    }
  });

  it("center on both axes and hold a fixed square", () => {
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.button);
      expect(decls, `${bar.sheet}: ${bar.button} align-items`).toMatch(/align-items:\s*center/);
      expect(decls, `${bar.sheet}: ${bar.button} justify-content`).toMatch(
        /justify-content:\s*center/,
      );
    }
  });

  it("agree on ONE size, and it is the app's button token", () => {
    // The two original bars disagreed — 1.75rem against var(--btn-h) — so two
    // surfaces that deliberately share a vocabulary rendered at different sizes.
    // 1.75rem (28px) was also short of the input beside it, so the row read as
    // misaligned before anything was centred.
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.button);
      expect(decls, `${bar.sheet}: ${bar.button} inline-size`).toMatch(
        /inline-size:\s*var\(--btn-h\)/,
      );
      expect(decls, `${bar.sheet}: ${bar.button} block-size`).toMatch(
        /block-size:\s*var\(--btn-h\)/,
      );
    }
  });

  it("declare no font-size, because there is no text left in them to size", () => {
    // Each carried `font-size: var(--fs-lg)` for the text glyphs it no longer
    // holds. A leftover font-size on an SVG-only button is a value that looks
    // load-bearing and is not.
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.button);
      expect(decls, `${bar.sheet}: ${bar.button} should have no font-size`).not.toMatch(
        /font-size:/,
      );
    }
  });
});

describe("the Aa match-case toggle", () => {
  it("trims its box to the cap band in every bar", () => {
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.caseToggle);
      expect(
        decls,
        `${bar.sheet}: ${bar.caseToggle} keeps its letters, so it needs the cap-band trim rather ` +
          `than the strut collapse — the trim makes the box edges the cap band, so centring the ` +
          `box centres the letterforms with no per-font value`,
      ).toMatch(/text-box:\s*trim-both\s+cap\s+alphabetic/);
    }
  });

  it("restores the strut its sibling rule collapsed", () => {
    // It inherits `line-height: 0` from the icon-button rule, and that answer is
    // for a REPLACED element. A text node in a zero line box is a different
    // problem, so the toggle says `normal` explicitly and lets the trim do the
    // centring.
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.caseToggle);
      expect(decls, `${bar.sheet}: ${bar.caseToggle} line-height`).toMatch(
        /line-height:\s*normal\s*;/,
      );
    }
  });

  it("declares no NUMERIC line-height, which would move the letters by zero", () => {
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.caseToggle);
      const m = /line-height:\s*([^;]+)/.exec(decls);
      expect(
        m?.[1]?.trim(),
        `${bar.sheet}: the offset formula has no line-height term, so a number here is a change ` +
          `that looks like the fix and measures as nothing`,
      ).toBe("normal");
    }
  });

  it("draws its latched fill from 70-selection.css and nowhere else", () => {
    // No local selected state, in any bar. The consolidated rule set is what makes
    // one selected treatment across the app, and it has to come last in the
    // MANIFEST to beat each feature file's equal-specificity :hover.
    const selection = loadCSS("70-selection.css");
    for (const bar of BARS) {
      const pressed = `${bar.caseToggle}[aria-pressed="true"]`;
      const rule = ruleContaining(selection, pressed, "top");
      expect(rule.body, `${pressed} must take the shared selected fill`).toMatch(
        /background:\s*var\(--c-selected-bg\)/,
      );
      const local = body(bar.sheet, bar.caseToggle);
      expect(
        local,
        `${bar.sheet}: ${bar.caseToggle} must not paint its own selected background`,
      ).not.toMatch(/--c-selected-bg/);
    }
  });
});

describe("the close × is a real element, not a character", () => {
  // The DOM half of this contract — that `searchIconButton` produces an <svg> and
  // no text node — is in search-shell.test.ts. This file's half is the source
  // scan below, read through Vite `?raw` imports rather than `node:fs`.
  it("is never a bare × character anywhere in a search bar's builder", () => {
    // A grep-shaped guard, because the failure mode is a NEW button rather than an
    // edit to an existing one: the three builders are the population, and a `×`,
    // `↑` or `↓` in any of them is the bug coming back.
    for (const [file, src] of Object.entries(searchBuilderSources)) {
      // Comments explain the glyphs, so they are stripped before the scan.
      const code = src.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/^\s*\/\/.*$/gm, " ");
      for (const glyph of ["\\u00d7", "\\u2191", "\\u2193"]) {
        expect(
          code,
          `${file} must not carry a bare ${glyph} glyph: a text node's LINE BOX is what ` +
            `align-items centres, so the ink lands off-centre by a font-dependent amount`,
        ).not.toContain(glyph);
      }
    }
  });
});

describe("the page search popup's × takes the same shape", () => {
  // A FOURTH bar, and the one that is four surfaces: History, the configuration
  // browser and the git view's two panels all share `.page-find` (search-popup.ts),
  // so one rule is the whole population. It is absent from the BARS table above
  // because it has no `Aa` to centre — none of those four endpoints can honour a
  // match-case flag — and a table entry with an empty column would read as a gap
  // rather than a decision.
  const decls = body("24-find.css", ".page-find-btn");

  it("collapses the strut around its SVG", () => {
    expect(decls).toMatch(/line-height:\s*0\s*;/);
  });

  it("centres on both axes and holds the app's button square", () => {
    expect(decls).toMatch(/align-items:\s*center/);
    expect(decls).toMatch(/justify-content:\s*center/);
    expect(decls).toMatch(/inline-size:\s*var\(--btn-h\)/);
    expect(decls).toMatch(/block-size:\s*var\(--btn-h\)/);
  });

  it("declares no font-size, because there is no text in it", () => {
    expect(decls).not.toMatch(/font-size:/);
  });

  it("has no match-case rule to carry, in any stylesheet", () => {
    // The tell that its absence is deliberate rather than forgotten: no
    // `.page-find-case` exists anywhere, so nothing is styled for a control the
    // builder does not make.
    const files = ["24-find.css", "70-selection.css"];
    for (const file of files) {
      expect(
        loadCSS(file),
        `${file} must not style a toggle this bar has no reason to hold`,
      ).not.toContain(".page-find-case");
    }
  });
});
