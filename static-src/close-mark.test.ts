// A CLOSE OR REMOVE MARK IS `ICON_CLOSE`, NEVER A TEXT CHARACTER.
//
// THE RULE ALREADY EXISTED AND ITS GUARD WAS SCOPED TOO NARROWLY, which is why this
// file is separate from the one the scan came out of. `search-shell.ts`'s
// `searchIconButton` states the reason in its own doc, `search-centring.test.ts`
// records the measurement at length, and `search-shell.test.ts` pins the DOM half —
// and all three covered SEARCH BARS only, so five builders were held to a rule the
// other seven were not. Measured across the app before this: four sites drew the mark
// as text, in TWO different characters — `\u00d7` in banner-stack.ts, permissions-ui.ts
// and attachment-pill.ts, `\u2715` in git-status-banner.ts — while `icons.ts` had owned
// the drawing the whole time.
//
// THE REASON, quoted from the rule's original home rather than re-derived: a text node
// inside a flex container is an anonymous flex item whose box is its LINE BOX, and a
// line box is symmetric about the ink only when the ink happens to fill it. The
// multiplication sign is drawn about the math axis, near half x-height and well below
// the cap-band centre, so `align-items: center` centres the wrong box by a
// font-dependent amount no authored offset corrects. An SVG is a replaced element whose
// box IS its ink box.
//
// WHAT THE DUPLICATE WAS ACTUALLY CHARGING, which is what makes this mechanical rather
// than a taste rule: `icon-btn` declares `line-height: 0` because its child is an SVG,
// so every character site had to hand back a `line-height: 1` AND pick a `font-size` —
// and all four picked differently. Converting them deleted those declarations outright:
// `.banner-dismiss` and `.git-status-banner-dismiss` now have no rule at all,
// `.native-rule-remove` went from 14 declarations to 3, `.attachment-close` 15 to 4.
//
// A SOURCE SCAN, deliberately, and for the reason the search-scoped original gave: the
// failure mode is a NEW button rather than an edit to an existing one, so the population
// is the builder FILES and a character appearing in any of them is the duplicate back.

import { describe, it, expect } from "vitest";
import attachmentPillSrc from "./attachment-pill.ts?raw";
import bannerStackSrc from "./banner-stack.ts?raw";
import chipSrc from "./chip.ts?raw";
import editorFindSrc from "./editor-find.ts?raw";
import filesSearchSrc from "./files-search.ts?raw";
import findInChatSrc from "./find-in-chat.ts?raw";
import gitStatusBannerSrc from "./git-status-banner.ts?raw";
import iconsSrc from "./icons.ts?raw";
import mcpPairsSrc from "./mcp-pairs.ts?raw";
import permissionsUISrc from "./permissions-ui.ts?raw";
import searchPopupSrc from "./search-popup.ts?raw";
import searchShellSrc from "./search-shell.ts?raw";
import tabsSrc from "./tabs.ts?raw";

/** Comments explain the glyphs, so they are stripped before every scan below —
 *  including this file's own prose, which names the characters it forbids. */
function code(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/^\s*\/\/.*$/gm, " ");
}

/** Every module that builds a close or remove affordance, paired with the escape of the
 *  character it must not carry. The five search builders are here rather than in
 *  `search-centring.test.ts`, which keeps the ARROWS: those are a search bar's own
 *  glyphs and no other surface has them, where the close mark is app-wide. */
const BUILDERS = [
  ["attachment-pill.ts", attachmentPillSrc],
  ["banner-stack.ts", bannerStackSrc],
  ["chip.ts", chipSrc],
  ["editor-find.ts", editorFindSrc],
  ["files-search.ts", filesSearchSrc],
  ["find-in-chat.ts", findInChatSrc],
  ["git-status-banner.ts", gitStatusBannerSrc],
  ["mcp-pairs.ts", mcpPairsSrc],
  ["permissions-ui.ts", permissionsUISrc],
  ["search-popup.ts", searchPopupSrc],
  ["search-shell.ts", searchShellSrc],
  ["tabs.ts", tabsSrc],
] as const satisfies readonly (readonly [string, string])[];

/** The two that were in use, plus the two a future author is likeliest to reach for.
 *  Written as escapes so the scan cannot match this list's own source. */
const FORBIDDEN = ["\\u00d7", "\\u2715", "\\u2716", "\\u274c"] as const;

/** The four sites the conversion touched. Named, so the scan above cannot be satisfied
 *  by deleting an affordance instead of converting it. */
const CONVERTED = [
  "attachment-pill.ts",
  "banner-stack.ts",
  "git-status-banner.ts",
  "permissions-ui.ts",
] as const;

describe("a close mark is the registry's drawing", () => {
  for (const [file, src] of BUILDERS) {
    it(`${file} carries no bare close character`, () => {
      const scanned = code(src);
      for (const glyph of FORBIDDEN) {
        expect(
          scanned,
          `${file} must not carry a bare ${glyph}: a text node's LINE BOX is what ` +
            `align-items centres, so the ink lands off-centre by a font-dependent ` +
            `amount \u2014 build the mark with iconEl(ICON_CLOSE) instead`,
        ).not.toContain(glyph);
      }
    });
  }

  for (const file of CONVERTED) {
    it(`${file} builds its close mark from the registry`, () => {
      const entry = BUILDERS.find(([name]) => name === file);
      expect(entry, `${file} must be in the scanned population`).toBeDefined();
      expect(
        code(entry?.[1] ?? ""),
        `${file} lost its close affordance rather than converting it`,
      ).toContain("iconEl(ICON_CLOSE)");
    });
  }
});

describe("the icon registry names a tier pair by its TIER", () => {
  // `icons.ts` had `ICON_EDIT_14`, `ICON_TRASH_14` and `ICON_PLUS_16`. Both suffixes
  // named the `ui` tier, which is 1rem on a fine pointer and 1.25rem on a coarse one,
  // so neither number was ever what rendered — and `svg()`'s own doc says its argument
  // is a size TIER, never a pixel count. A rename is caught by the type checker; a NEW
  // export carrying a pixel suffix is not, which is the only thing this scans for.
  it("exports no icon name ending in a pixel count", () => {
    const names = [...code(iconsSrc).matchAll(/export const (ICON_\w+)/g)].map((m) => m[1] ?? "");
    expect(
      names.filter((n) => /_\d+$/.test(n)),
      "an icon export names a SIZE TIER, never a pixel count (svg() in icons.ts)",
    ).toEqual([]);
  });
});
