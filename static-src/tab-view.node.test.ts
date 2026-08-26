// The two per-kind tables and the view contract. No DOM, no store, no mocks —
// which is the point of the module being separate: it holds the facts a factory
// needs and nothing that needs a document.
//
// `.node.test.ts` rather than the default browser project deliberately: the
// subject is DOM-FREE by design, and a test that never touches a document proves
// that where a browser test could pass while quietly depending on one.

import { describe, it, expect } from "vitest";
import { TAB_VIEWS, TAB_ICONS } from "./tab-view.js";
import type { TabKind } from "./types.js";

// The eight kinds, written out. This list is NOT the gate — the gate is the
// `Readonly<Record<TabKind, string>>` annotation on both tables, which is a
// compile error the moment a ninth kind is added to the Go const block and
// regenerated. What the list buys is the other direction: a table that grows an
// entry the wire does not know, and a table that disagrees with its sibling.
const KINDS: readonly TabKind[] = [
  "chat",
  "editor",
  "run",
  "settings",
  "git",
  "files",
  "history",
  "docs",
];

describe("TAB_VIEWS", () => {
  it("names a view element for every wire kind and no others", () => {
    expect(Object.keys(TAB_VIEWS).sort()).toEqual([...KINDS].sort());
  });

  it.each(KINDS)("%s maps to an id selector", (kind) => {
    expect(TAB_VIEWS[kind]).toMatch(/^#[a-z-]+$/);
  });

  // A kind pointing at another kind's view is the one mistake in this table that
  // a type cannot catch: every value is a string, so a copy-paste shows up only
  // as two kinds sharing a selector and one view never being reachable.
  it("gives each kind its own view", () => {
    const selectors = Object.values(TAB_VIEWS);
    expect(new Set(selectors).size).toBe(selectors.length);
  });
});

describe("TAB_ICONS", () => {
  it("covers exactly the kinds TAB_VIEWS covers", () => {
    expect(Object.keys(TAB_ICONS).sort()).toEqual(Object.keys(TAB_VIEWS).sort());
  });

  it.each(KINDS)("%s carries an svg glyph", (kind) => {
    expect(TAB_ICONS[kind]).toMatch(/^<svg/);
  });

  // Same trap as the view table, and the same reason a type cannot see it: two
  // kinds sharing a glyph reads as a tab wearing the wrong badge.
  it("gives each kind its own glyph", () => {
    const glyphs = Object.values(TAB_ICONS);
    expect(new Set(glyphs).size).toBe(glyphs.length);
  });
});
