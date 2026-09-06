// The hourglass glyph's geometry, derived from the shipped string rather than
// snapshotted.
//
// The drawing this pins replaced one that was not an hourglass. Its single closed
// outline ran `a6 6 0 01-3 5.2` twice, and those arcs bulge OUTWARD, so the shape
// rendered as two stacked lozenges: a 12-unit-wide body pinching only to 6 at the
// waist, with a flat SQUARE top (`h12v3`) against a 3-unit ROUNDED bottom
// (`a3 3 0 01-3 3`). At 13.6px, where this renders in the steer dock, that is a
// blob with a bar near the top.
//
// Neither half of that is visible in review at 13.6px, which is what the
// assertions below are for: an asymmetry of half a unit is 0.28 CSS px, and it is
// cumulative across edits. So the invariants are the two mirror axes, the cap
// overhang that separates an hourglass from an X, and the margin — never "there
// are two triangles".
//
// Absolute commands only is a REQUIREMENT of this file, not a style preference: a
// relative form hides a drift inside its deltas, and reading symmetry off it means
// re-implementing a path parser that tracks arcs.

import { describe, it, expect } from "vitest";
import { ICON_HOURGLASS } from "./icons.js";

/** The size tier's grid, and the size the steer dock renders it at. `--icon-ui`
 *  is 16 on a fine pointer, but 26-dock.css overrides it to 0.85rem, which is the
 *  smallest size this glyph ships at and therefore the one the floors below are
 *  evaluated against. */
const VIEWBOX = 24;
const SMALLEST_PX = 13.6;

/** 4.9 + 19.1 is 24.000000000000004 in float64, so an exact mirror check on
 *  authored decimals needs a tolerance. One part in a billion is far below the
 *  0.28 CSS px that half a unit is worth at 13.6px, so nothing real hides under
 *  it. */
const EPS = 1e-9;

interface Point {
  readonly x: number;
  readonly y: number;
}

/** Every point the path visits, from a parser that accepts ONLY the absolute
 *  commands this glyph is authored with. A relative or arc command is a failure
 *  rather than something to interpret: the whole reason the coordinates are
 *  absolute is so this file does not need to compute them. */
function points(svg: string): Point[] {
  const d = /<path d="([^"]+)"/g;
  const out: Point[] = [];
  let bodies = 0;
  for (let m = d.exec(svg); m !== null; m = d.exec(svg)) {
    bodies += 1;
    let x = Number.NaN;
    let y = Number.NaN;
    const tokens = m[1]!.match(/[A-Za-z][-\d.\s]*/g) ?? [];
    for (const token of tokens) {
      const cmd = token[0]!;
      const nums = (token.slice(1).match(/-?[\d.]+/g) ?? []).map(Number);
      expect(
        cmd,
        `${cmd} is not an absolute M/H/V/L command; this glyph is authored in absolute ` +
          `coordinates so the mirror axes are readable without a path parser`,
      ).toMatch(/^[MHVL]$/);
      if (cmd === "M" || cmd === "L") {
        expect(nums.length, `${token} must carry an x and a y`).toBe(2);
        [x, y] = [nums[0]!, nums[1]!];
      } else if (cmd === "H") {
        x = nums[0]!;
      } else {
        y = nums[0]!;
      }
      out.push({ x, y });
    }
  }
  expect(bodies, "expected the two cap bars and the two chambers").toBeGreaterThanOrEqual(3);
  return out;
}

/** Whether a multiset of coordinates is its own mirror image about `axis`. Sorted
 *  and walked from both ends, so a value repeated an odd number of times off the
 *  axis fails rather than pairing with itself. */
function mirrored(values: readonly number[], axis: number): boolean {
  const sorted = [...values].sort((a, b) => a - b);
  for (let i = 0, j = sorted.length - 1; i <= j; i += 1, j -= 1) {
    if (Math.abs(sorted[i]! + sorted[j]! - 2 * axis) > EPS) {
      return false;
    }
  }
  return true;
}

describe("the hourglass glyph", () => {
  it("mirrors about both axes of the grid", () => {
    // The defect this replaces was asymmetric on the vertical axis only — square
    // top, rounded bottom — so the y check is the one that would have caught it.
    // The x check comes free from the same coordinates and guards the other
    // direction.
    const p = points(ICON_HOURGLASS);
    const centre = VIEWBOX / 2;

    expect(
      mirrored(
        p.map((q) => q.y),
        centre,
      ),
      `the glyph is not mirrored about y=${String(centre)}: ` +
        `${[...new Set(p.map((q) => q.y))].sort((a, b) => a - b).join(", ")}. ` +
        `A top and bottom that differ is what made the previous drawing read as a figure-8.`,
    ).toBe(true);

    expect(
      mirrored(
        p.map((q) => q.x),
        centre,
      ),
      `the glyph is not mirrored about x=${String(centre)}: ` +
        `${[...new Set(p.map((q) => q.x))].sort((a, b) => a - b).join(", ")}`,
    ).toBe(true);
  });

  it("pinches to a single point on the centre line", () => {
    // Both chambers must terminate at one shared point, and it has to be the
    // grid's centre: that point IS the waist, and an hourglass whose halves meet
    // off-centre or across a segment is a bowtie.
    const p = points(ICON_HOURGLASS);
    const centre = VIEWBOX / 2;
    const waist = p.filter((q) => Math.abs(q.y - centre) < EPS);

    expect(waist.length, "expected both chambers to reach the waist").toBe(2);
    for (const q of waist) {
      expect(q.x, "the waist must sit on the vertical centre line").toBeCloseTo(centre, 9);
    }
  });

  it("gives the caps an overhang wide enough to survive 13.6px", () => {
    // The caps are what make this read as an hourglass rather than an X: they are
    // the widest thing in the glyph, and the chambers hang inside them. An
    // overhang under half a CSS pixel quantises away at DPR 1, which would leave
    // a bar the same width as the chamber it caps.
    const p = points(ICON_HOURGLASS);
    const centre = VIEWBOX / 2;
    const halfWidthAt = (y: number): number =>
      Math.max(...p.filter((q) => Math.abs(q.y - y) < EPS).map((q) => Math.abs(q.x - centre)));

    const ys = [...new Set(p.map((q) => q.y))].sort((a, b) => a - b);
    const capY = ys[0]!;
    const shoulderY = ys[1]!;
    const overhangUnits = halfWidthAt(capY) - halfWidthAt(shoulderY);
    const overhangPx = (overhangUnits * SMALLEST_PX) / VIEWBOX;

    expect(
      overhangPx,
      `the cap overhangs its chamber by ${String(overhangUnits)} units, which is ` +
        `${overhangPx.toFixed(2)} CSS px at ${String(SMALLEST_PX)}px. Under half a device pixel at ` +
        `DPR 1 the bar and the chamber paint the same width and the cap stops reading as a cap.`,
    ).toBeGreaterThanOrEqual(0.5);
  });

  it("keeps the artwork inside the grid's 2-unit margin", () => {
    // The house margin for this icon set, so the glyph's optical size matches its
    // neighbours in the same row. A coordinate outside it also risks the stroke's
    // outward half being clipped by the viewBox.
    const p = points(ICON_HOURGLASS);
    for (const q of p) {
      for (const [axis, v] of [
        ["x", q.x],
        ["y", q.y],
      ] as const) {
        expect(v, `${axis}=${String(v)} is outside the 2..22 margin`).toBeGreaterThanOrEqual(2);
        expect(v, `${axis}=${String(v)} is outside the 2..22 margin`).toBeLessThanOrEqual(
          VIEWBOX - 2,
        );
      }
    }
  });
});
