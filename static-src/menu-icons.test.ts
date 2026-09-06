//
// Drift guard for the NAVIGATION glyphs hand-authored in static/index.html.
//
// The sidebar buttons and the three tab bars carry their SVGs as markup, while
// icons.ts owns the same glyphs for the DOM the app builds at runtime. Nothing
// tied the two together, so four of them had drifted apart by 2026-09: the docs
// page drew a plain hexagon for Agents where every agent surface draws the
// hexagon-with-a-core, a check-square for Specs where the spec mode draws a
// checklist (and the composer's task pill already drew that check-square), a
// circle-play for Workflows where a run tab draws the node graph, and the sidebar
// book that OPENS the docs page was a different book from the one on its tab.
//
// This is the table that keeps a concept to one drawing. Every pair here is one
// concept with two render sites; a glyph with no registry owner (Steering's
// compass, Skills' bolt, Hooks' anchor, and the four Settings categories) is
// absent because there is nothing for it to disagree with.
import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";
import {
  ICON_PR_EMPTY,
  ICON_TAB_AGENT,
  ICON_TAB_DOCS,
  ICON_TAB_FILES,
  ICON_TAB_GIT,
  ICON_TAB_HISTORY,
  ICON_TAB_RUN,
  ICON_TAB_SETTINGS,
  ICON_TAB_SPEC,
} from "./icons.js";

/** Collapse whitespace runs and trim, and nothing else. Whitespace INSIDE path
 *  data is a coordinate separator, so stripping it outright would let two
 *  genuinely different paths compare equal. */
function norm(markup: string): string {
  return markup.replace(/\s+/g, " ").trim();
}

/** The drawing, without the wrapper. The two sites legitimately differ on the
 *  <svg> element's own attributes — index.html adds a layout class and
 *  aria-hidden — so only the child elements are the shared contract. */
function inner(svg: string): string {
  return norm(svg.replace(/^<svg\b[^>]*>/, "").replace(/<\/svg>$/, ""));
}

/** The first glyph inside the button that carries `anchor`, scoped to that
 *  button so a reordering of the markup cannot silently pick up a sibling's. */
function glyphOf(anchor: string): string {
  const at = indexHtml.indexOf(anchor);
  expect(at, `static/index.html has no ${anchor}`).toBeGreaterThan(-1);
  const end = indexHtml.indexOf("</button>", at);
  expect(end, `${anchor} is not inside a button`).toBeGreaterThan(at);
  const button = indexHtml.slice(at, end);
  const m = /<svg\b[\s\S]*?<\/svg>/.exec(button);
  expect(m, `${anchor} carries no inline svg`).not.toBeNull();
  return inner(m?.[0] ?? "");
}

const PAIRS: readonly (readonly [label: string, anchor: string, registry: string])[] = [
  // Sidebar: the button and the tab it opens are one destination.
  ["sidebar Kiro docs", 'id="docs-btn"', ICON_TAB_DOCS],
  ["sidebar History", 'id="history-btn"', ICON_TAB_HISTORY],
  ["sidebar Files", 'id="files-btn"', ICON_TAB_FILES],
  ["sidebar Git", 'id="git-btn"', ICON_TAB_GIT],
  ["sidebar Settings", 'id="settings-btn"', ICON_TAB_SETTINGS],
  // Docs categories that name something the app already draws elsewhere.
  ["docs tab Agents", 'data-docs-tab="agents"', ICON_TAB_AGENT],
  ["docs tab Specs", 'data-docs-tab="specs"', ICON_TAB_SPEC],
  ["docs tab Workflows", 'data-docs-tab="workflows"', ICON_TAB_RUN],
  // The PR empty state renders inside the tab whose icon names it.
  ["git tab Pull requests", 'data-git-tab="prs"', ICON_PR_EMPTY],
];

describe("hand-authored glyphs in static/index.html", () => {
  for (const [label, anchor, registry] of PAIRS) {
    it(`${label} draws the icons.ts glyph`, () => {
      expect(glyphOf(anchor)).toBe(inner(registry));
    });
  }

  // A menu whose two entries draw one mark cannot be read, and this is the half
  // the table above cannot see: a glyph with no registry owner is still not
  // allowed to collide with its neighbour's.
  for (const [bar, attr] of [
    ["Settings", "data-settings-tab"],
    ["docs", "data-docs-tab"],
    ["git", "data-git-tab"],
  ] as const) {
    it(`gives every ${bar} tab a distinct glyph`, () => {
      const tabs = [...indexHtml.matchAll(new RegExp(`${attr}="([^"]+)"`, "g"))].map((m) => m[1]);
      expect(tabs.length).toBeGreaterThan(1);
      const byGlyph = new Map<string, string[]>();
      for (const tab of tabs) {
        const g = glyphOf(`${attr}="${tab}"`);
        byGlyph.set(g, [...(byGlyph.get(g) ?? []), tab ?? ""]);
      }
      const shared = [...byGlyph.values()].filter((names) => names.length > 1);
      expect(shared, `${bar} tabs sharing one glyph`).toEqual([]);
    });
  }
});
