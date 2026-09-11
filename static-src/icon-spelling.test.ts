// A DRAWING SHARED WITH THE REGISTRY IS SPELLED THE WAY `svg()` SPELLS IT.
//
// WHY THIS IS NOT `menu-icons.test.ts`. That file guards the same two sites and the
// opposite half: its `inner()` helper strips the `<svg>` element on purpose, and its
// own comment says the wrapper attributes "legitimately differ — index.html adds a
// layout class and aria-hidden — so only the child elements are the shared contract".
// It owns which DRAWING a concept gets. This owns how the wrapper around a shared
// drawing is SPELLED, which that contract deliberately excludes.
//
// THE DEFECT, measured 2026-09. Seventeen inline SVGs in static/index.html carried a
// drawing `icons.ts` also owns while omitting `stroke-linejoin="round"`, which `svg()`
// emits for every glyph. Sixteen were byte-only: rasterised at 240px against the
// canonical spelling, `PATH_X` (11 sites), `PATH_PLUS` (4) and `PATH_TRASH` (1) each
// differed in ZERO pixels, because a stroke join can only show where two segments meet
// and those drawings are single-segment subpaths — the trash's junctions are all
// rounded arcs whose tangents already run continuous. The seventeenth was real:
// `PATH_PENCIL` differed in 770 pixels, 1.34% of the frame, max channel delta 510,
// because it closes with `z` and its tip is a sharp corner that miter draws as a spike
// and round draws as a curve. So the same edit glyph rendered one way in the markup and
// another way everywhere the app draws it.
//
// WHY THE GUARD IS OVER BYTES WHEN ONLY ONE SITE RENDERED WRONG. A rendering guard is
// the honest subject and needs a rasteriser per glyph per run; this is the cheap proxy,
// it is strictly stronger than the visual rule, and it is what makes the class
// unreachable rather than merely absent — the next drawing hand-authored into that file
// may well have joins, and the omission was already propagating by copy.
//
// WHAT IT DELIBERATELY DOES NOT CHECK. Not the TIER: a size is the markup's own choice,
// and `ic-inline` beside a registry entry exported at `ui` is a legitimate call the
// registry has no standing to make. Not attribute ORDER, and not extra attributes: a
// layout class, an `aria-hidden`, an `id` are all fine, so the assertion is set
// membership rather than a contiguous run and stays insensitive to reordering.

import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";
import iconsSrc from "./icons.ts?raw";

/** The five attributes `svg()` emits for every glyph. `class` is excluded because the
 *  tier is the markup's call and extra classes are legitimate. */
const CANONICAL = [
  'viewBox="0 0 24 24"',
  'fill="none"',
  'stroke="currentColor"',
  'stroke-linecap="round"',
  'stroke-linejoin="round"',
] as const;

/** Collapse whitespace runs only. Whitespace inside path data is a coordinate
 *  separator, so stripping it outright would let two different drawings compare equal —
 *  the same reason `menu-icons.test.ts` normalises this way and no further. */
function norm(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

function inner(svg: string): string {
  return norm(svg.replace(/^<svg\b[^>]*>/, "").replace(/<\/svg>$/, ""));
}

/** Every drawing the registry owns, read off the SOURCE rather than the module, so a
 *  glyph assembled from a shared `PATH_` const and one written out as a literal string
 *  are both in the population. */
function registryDrawings(src: string): Map<string, string[]> {
  const paths = new Map<string, string>();
  for (const m of src.matchAll(/const (PATH_\w+) =\s*'([^']*)'/g)) {
    paths.set(m[1] ?? "", m[2] ?? "");
  }
  const out = new Map<string, string[]>();
  const add = (drawing: string, name: string): void => {
    const key = norm(drawing);
    out.set(key, [...(out.get(key) ?? []), name]);
  };
  for (const m of src.matchAll(/export const (ICON_\w+) = svg\(\s*"\w+",\s*(PATH_\w+|'[^']*')/g)) {
    const arg = m[2] ?? "";
    add(arg.startsWith("PATH_") ? (paths.get(arg) ?? "") : arg.slice(1, -1), m[1] ?? "");
  }
  // `\s*(?:\+\s*)?` rather than `\s*\+?\s*`: the second spelling lets a whitespace run
  // split arbitrarily between its two `\s*` whenever no `+` is present, and the outer
  // `+` compounds that into exponential backtracking (CodeQL js/redos). Anchoring the
  // optional whitespace behind the literal `+` makes each split point determined.
  // `\s*` alone replaces `\s*\n?\s*` for the same reason — `\n` is already whitespace,
  // so the `\n?` only added a second way to match the same run.
  for (const m of src.matchAll(/export const (ICON_\w+) =\s*((?:'[^']*'\s*(?:\+\s*)?)+);/g)) {
    const lit = [...(m[2] ?? "").matchAll(/'([^']*)'/g)].map((q) => q[1] ?? "").join("");
    if (lit.startsWith("<svg")) {
      add(inner(lit), m[1] ?? "");
    }
  }
  return out;
}

/** Each inline `<svg>` in index.html that draws something the registry also draws. */
function sharedSVGs(): { line: number; attrs: string; owners: string[] }[] {
  const reg = registryDrawings(iconsSrc);
  const out: { line: number; attrs: string; owners: string[] }[] = [];
  for (const m of indexHtml.matchAll(/<svg\b([^>]*)>[\s\S]*?<\/svg>/g)) {
    const owners = reg.get(inner(m[0]));
    if (owners === undefined) {
      continue;
    }
    out.push({
      line: indexHtml.slice(0, m.index).split("\n").length,
      attrs: norm(m[1] ?? ""),
      owners,
    });
  }
  return out;
}

describe("a registry drawing hand-authored in index.html", () => {
  const shared = sharedSVGs();

  it("has a population to check, so the scan below cannot pass vacuously", () => {
    // The regexes read two real files; a parser change that matched nothing would
    // otherwise leave every assertion trivially green.
    expect(registryDrawings(iconsSrc).size).toBeGreaterThan(20);
    expect(shared.length).toBeGreaterThan(10);
  });

  for (const attr of CANONICAL) {
    it(`carries ${attr} at every site`, () => {
      const missing = shared
        .filter((s) => !s.attrs.includes(attr))
        .map((s) => `line ${String(s.line)} (${s.owners.join(", ")})`);
      expect(
        missing,
        `these hand-authored glyphs omit ${attr}, which svg() emits for the registry ` +
          `copy of the same drawing — on a drawing with joins that renders differently ` +
          `(PATH_PENCIL measured 770 differing pixels)`,
      ).toEqual([]);
    });
  }
});
