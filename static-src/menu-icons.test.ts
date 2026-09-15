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
  ICON_REPO,
  ICON_SUBAGENT_INTROSPECT,
  ICON_TAB_AGENT,
  ICON_EXTERNAL,
  ICON_TAB_DOCS,
  ICON_TAB_FILES,
  ICON_TAB_GIT,
  ICON_TAB_HISTORY,
  ICON_TAB_RUN,
  ICON_TAB_SETTINGS,
  ICON_TAB_SPEC,
  ICON_TOOL_TERMINAL,
  toolIcon,
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

/** The first glyph inside the element that carries `anchor`, scoped to that element
 *  so a reordering of the markup cannot silently pick up a sibling's.
 *
 *  `close` is a parameter because not every host is a `<button>` any more: the status
 *  card's account row is an `<a>`. Without it the slice would run from that link
 *  through the sidebar footer's logout button and pass by ACCIDENT on the first
 *  `<svg>` it found. The failure message is interpolated for the same reason — the
 *  hardcoded "is not inside a button" names the wrong container the moment a caller
 *  passes anything else, and that message is the only thing a failing run shows. */
function glyphOf(anchor: string, close = "</button>"): string {
  const at = indexHtml.indexOf(anchor);
  expect(at, `static/index.html has no ${anchor}`).toBeGreaterThan(-1);
  const end = indexHtml.indexOf(close, at);
  expect(end, `${anchor} has no ${close}`).toBeGreaterThan(at);
  const host = indexHtml.slice(at, end);
  const m = /<svg\b[\s\S]*?<\/svg>/.exec(host);
  expect(m, `${anchor} carries no inline svg`).not.toBeNull();
  return inner(m?.[0] ?? "");
}

/** A pair, plus the closing tag that scopes the slice to its own host. Defaults to
 *  `</button>`, which every entry but the account link takes. */
const PAIRS: readonly (readonly [
  label: string,
  anchor: string,
  registry: string,
  close?: string,
])[] = [
  // Sidebar: the button and the tab it opens are one destination.
  ["sidebar Kiro docs", 'id="docs-btn"', ICON_TAB_DOCS],
  ["sidebar History", 'id="history-btn"', ICON_TAB_HISTORY],
  ["sidebar Files", 'id="files-btn"', ICON_TAB_FILES],
  ["sidebar Git", 'id="git-btn"', ICON_TAB_GIT],
  ["toolbar Settings", 'id="settings-btn"', ICON_TAB_SETTINGS],
  // The shell toggle is deliberately NOT here — see the divergence test below.
  // Docs categories that name something the app already draws elsewhere.
  ["docs tab Agents", 'data-docs-tab="agents"', ICON_TAB_AGENT],
  ["docs tab Specs", 'data-docs-tab="specs"', ICON_TAB_SPEC],
  ["docs tab Workflows", 'data-docs-tab="workflows"', ICON_TAB_RUN],
  // The PR empty state renders inside the tab whose icon names it.
  ["git tab Pull requests", 'data-git-tab="prs"', ICON_PR_EMPTY],
  // The status card's account row: an <a> rather than a button, and the one thing in
  // this app that LEAVES it, which is the case chevron.ts reserves ICON_EXTERNAL
  // for. Passing "</a>" is what scopes the slice to the link — with the default the
  // slice would run through the footer's logout button and match its glyph.
  ["status card account link", 'id="st-account"', ICON_EXTERNAL, "</a>"],
];

describe("hand-authored glyphs in static/index.html", () => {
  for (const [label, anchor, registry, close] of PAIRS) {
    it(`${label} draws the icons.ts glyph`, () => {
      expect(glyphOf(anchor, close)).toBe(inner(registry));
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

  // The honest extension of that half to the sidebar header, which is where the two
  // new pointer drawings land. Every glyph the bar can SHOW is counted, not just the
  // one each button starts on: both toggles there swap glyphs at runtime, so a
  // collision the reader meets after a click is the same defect as one on load.
  it("gives every glyph in the sidebar header a distinct mark", () => {
    const at = indexHtml.indexOf('class="sidebar-header-actions"');
    expect(at, "static/index.html has no sidebar-header-actions").toBeGreaterThan(-1);
    const bar = indexHtml.slice(at, indexHtml.indexOf("</div>", at));
    const glyphs = [...bar.matchAll(/<svg\b[\s\S]*?<\/svg>/g)].map((m) => inner(m[0]));
    // Moon, sun, system half-disc, mouse, hand, close.
    expect(glyphs).toHaveLength(6);
    expect(new Set(glyphs).size, "sidebar-header glyphs sharing one mark").toBe(glyphs.length);
  });

  // The ONE sanctioned divergence, pinned so a one-glyph-per-concept sweep cannot
  // fold it back into the table above. The toolbar's shell toggle draws a BOXED
  // prompt where a tool card draws the bare `>_`: a bare prompt reaches 12 of the
  // grid's 15 units, so among boxed neighbours it reads short and sits optically
  // high, and the frame is what aligns it. It has been removed once already.
  it("keeps the shell toggle's prompt in a box, unlike the bare tool glyph", () => {
    const shell = glyphOf('id="shell-btn"');
    expect(shell, "the frame is the whole point of the divergence").toContain("<rect");
    expect(shell).not.toBe(inner(ICON_TOOL_TERMINAL));
    expect(inner(ICON_TOOL_TERMINAL), "the tool glyph stays bare").not.toContain("<rect");
  });

  // Every coordinate on the 3-unit grid. `icon-crisp.ts` owns the pixel PHASE at
  // runtime, so the artwork's job is only to keep its structural strokes in ONE phase
  // class, and multiples of 3 are that class for this set.
  it("keeps the shell box's strokes on the 3-unit grid", () => {
    const rect = /<rect\b[^>]*>/.exec(glyphOf('id="shell-btn"'))?.[0] ?? "";
    const num = (attr: string): number =>
      Number.parseFloat(new RegExp(`${attr}="([\\d.]+)"`).exec(rect)?.[1] ?? "NaN");

    const x = num("x");
    const y = num("y");
    for (const [name, edge] of [
      ["left", x],
      ["right", x + num("width")],
      ["top", y],
      ["bottom", y + num("height")],
    ] as const) {
      expect(edge % 3, `${name} edge of the shell box`).toBe(0);
    }
    expect(num("width"), "the shell box is square").toBe(num("height"));
  });

  // The open book's two meanings are DECLARED, which is the other half of this file's
  // job: a share nothing states is drift, and a share stated here is a decision. The
  // configuration browser and the read-family tool card draw one mark on purpose
  // (icons.ts `PATH_BOOK_OPEN`), so the pair that must NOT drift apart is pinned from
  // one side and the glyph that must not join them from the other.
  // Resolved through `toolIcon`, the way a card does, rather than off the constant: a
  // title override would defeat the share without the constant moving.
  it("draws one open book for the browser and the read family", () => {
    expect(inner(toolIcon("read", "readFile"))).toBe(inner(ICON_TAB_DOCS));
  });

  // It was Lucide's book-open and was indistinguishable from the read glyph at 16px,
  // with both rendering in the transcript. Re-unifying them would restore that.
  it("keeps the introspect subagent out of the book family", () => {
    expect(inner(ICON_SUBAGENT_INTROSPECT)).not.toBe(inner(ICON_TAB_DOCS));
    expect(inner(ICON_SUBAGENT_INTROSPECT), "nor the closed book").not.toBe(inner(ICON_REPO));
  });
});
