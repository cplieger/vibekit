// The model glyph's geometry, derived rather than snapshotted.
//
// This icon renders at 12px and at 20px, and the 12px size is what broke it: the
// previous drawing had STROKED eyes (r=2.5, stroke-width 2) whose outer edges
// left one user unit between them, which is 0.50 CSS px at 12px. Under a device
// pixel, so the two rings bridged into a blob, and because the centres sat on
// integers the 1-unit wall split 25/75 across two pixel columns and painted two
// greys instead of a gap.
//
// The invariant worth pinning is therefore not "the eyes are circles" but "the
// gap between them survives quantisation at the SMALLEST size this icon is
// rendered at". Everything here is computed from the shipped string.
//
// Node environment: no DOM, the icon is text.

import { describe, it, expect } from "vitest";
import { ICON_MODEL, ICON_MODEL_20 } from "./icons.js";

/** Every render size this glyph ships at. Both are real call sites: the composer's
 *  model pill (model-switcher.ts) and the empty-chat picker's heading (picker.ts).
 *  A new size belongs here, because the floors below are evaluated at the
 *  smallest member. */
const RENDER_SIZES = [12, 20];

interface Circle {
  cx: number;
  cy: number;
  r: number;
}

function attr(tag: string, name: string): number {
  const m = new RegExp(`${name}="([-\\d.]+)"`).exec(tag);
  expect(m, `<${tag.slice(0, 24)}…> has no ${name}`).not.toBeNull();
  return Number(m![1]);
}

function circles(svg: string): Circle[] {
  return [...svg.matchAll(/<circle [^>]*\/>/g)].map((m) => ({
    cx: attr(m[0], "cx"),
    cy: attr(m[0], "cy"),
    r: attr(m[0], "r"),
  }));
}

/** The viewBox is square and starts at 0, so one number describes it. */
function viewBoxSide(svg: string): number {
  const m = /viewBox="0 0 (\d+) (\d+)"/.exec(svg);
  expect(m, "expected a square viewBox anchored at 0 0").not.toBeNull();
  expect(m![1], "viewBox must be square").toBe(m![2]);
  return Number(m![1]);
}

/** The eyes are the only pair of circles sharing a radius AND a y — the antenna
 *  dot is alone on both counts. Found by that property rather than by index, so
 *  reordering the markup cannot make this test read the wrong two shapes. */
function eyes(svg: string): [Circle, Circle] {
  const groups = new Map<string, Circle[]>();
  for (const c of circles(svg)) {
    const key = `${String(c.cy)}:${String(c.r)}`;
    groups.set(key, [...(groups.get(key) ?? []), c]);
  }
  const pair = [...groups.values()].filter((g) => g.length === 2);
  expect(pair.length, "expected exactly one pair of circles sharing cy and r").toBe(1);
  const [a, b] = [...pair[0]!].sort((p, q) => p.cx - q.cx);
  return [a!, b!];
}

describe("the model glyph's eye gap", () => {
  it("clears a device pixel at the smallest size it is rendered at", () => {
    const svg = ICON_MODEL;
    const side = viewBoxSide(svg);
    const [left, right] = eyes(svg);

    // Filled circles, so the painted edge is the radius — no stroke to add. That
    // is the change: a stroked ring spends its width outward AND inward, which is
    // what ate the gap and the pupil at the same time.
    expect(
      svg,
      "the eyes must be filled, not stroked: a ring at 12px has to hold a wall and a hole inside 1.5 CSS px and loses both",
    ).toContain(
      `<circle cx="${String(left.cx)}" cy="${String(left.cy)}" r="${String(left.r)}" fill="currentColor" stroke="none"/>`,
    );

    const gapUnits = right.cx - right.r - (left.cx + left.r);
    const smallest = Math.min(...RENDER_SIZES);
    const gapPx = (gapUnits * smallest) / side;

    expect(
      gapPx,
      `the eyes leave ${String(gapUnits)} user units between them, which is ${gapPx.toFixed(2)} CSS px ` +
        `at ${String(smallest)}px. Under 1 there is no whole device pixel to hold the gap at DPR 1 and the ` +
        `two eyes bridge into one blob — the defect this geometry was redrawn to fix.`,
    ).toBeGreaterThanOrEqual(1);
  });

  it("puts the gap's edges on whole pixel boundaries at the smallest size", () => {
    // Sub-pixel edges are the second half of the same defect: a gap can be wide
    // enough on paper and still render as two greys if neither edge lands on a
    // pixel boundary. At 12px one unit is exactly 0.5 CSS px, so even unit values
    // ARE the boundaries.
    const svg = ICON_MODEL;
    const side = viewBoxSide(svg);
    const [left, right] = eyes(svg);
    const smallest = Math.min(...RENDER_SIZES);
    const edges = [left.cx + left.r, right.cx - right.r].map((u) => (u * smallest) / side);

    for (const px of edges) {
      expect(
        Number.isInteger(px),
        `gap edge lands at ${String(px)} CSS px at ${String(smallest)}px; a fractional edge antialiases ` +
          `into the gap, which is how the previous drawing's 1-unit wall became two greys`,
      ).toBe(true);
    }
  });
});

describe("the model glyph", () => {
  it("draws both sizes from one path, so they cannot drift", () => {
    const body = (svg: string): string => svg.replace(/^<svg[^>]*>/, "").replace("</svg>", "");
    expect(
      body(ICON_MODEL),
      "the 12px and 20px glyphs were two byte-identical literals in index.html before this; " +
        "if their bodies differ now the shared `d` has been forked again",
    ).toBe(body(ICON_MODEL_20));

    for (const size of RENDER_SIZES) {
      const svg = size === 12 ? ICON_MODEL : ICON_MODEL_20;
      expect(svg, `the ${String(size)}px call site must render at ${String(size)}px`).toContain(
        `width="${String(size)}" height="${String(size)}"`,
      );
    }
  });

  it("has a square content bbox, centred in its viewBox", () => {
    const svg = ICON_MODEL;
    const side = viewBoxSide(svg);
    const strokeWidth = attr(/^<svg[^>]*>/.exec(svg)![0], "stroke-width");

    // Two elements define the extremes: the antenna dot is the topmost painted
    // thing and the head's stroked rect is the other three edges. Everything else
    // sits inside the head.
    const dot = circles(svg).find((c) => c.r !== eyes(svg)[0].r);
    expect(dot, "expected an antenna dot distinct from the eyes").toBeDefined();
    const rectTag = /<rect [^>]*\/>/.exec(svg);
    expect(rectTag, "expected the head rect").not.toBeNull();
    const rx = attr(rectTag![0], "x");
    const ry = attr(rectTag![0], "y");
    const rw = attr(rectTag![0], "width");
    const rh = attr(rectTag![0], "height");

    const half = strokeWidth / 2;
    const box = {
      top: dot!.cy - dot!.r,
      bottom: ry + rh + half,
      left: rx - half,
      right: rx + rw + half,
    };

    expect(
      box.right - box.left,
      "the content bbox must be square; it was 22 x 21.5 before the head moved down",
    ).toBe(box.bottom - box.top);
    expect(box.left + box.right, "the bbox must be centred horizontally in the viewBox").toBe(side);
    expect(
      box.top + box.bottom,
      "the bbox must be centred vertically too. Squaring it by extending only upward reaches " +
        "22 x 22 flush against the top edge with 2 units below, which is square and still off-centre",
    ).toBe(side);
  });

  it("keeps the mouth inside the head, dip included", () => {
    // The mouth is the one element whose painted extent is not its own
    // coordinates: it is a quadratic, and a quadratic's dip is HALF its control
    // offset. The previous drawing's dip was 1 unit (0.5 px at 12px, a dash);
    // deepening it is only safe while the curve plus its stroke stays off the
    // head's inner edge.
    const svg = ICON_MODEL;
    const strokeWidth = attr(/^<svg[^>]*>/.exec(svg)![0], "stroke-width");
    const m = /<path d="M([\d.]+) ([\d.]+)q([\d.]+) ([\d.]+) ([\d.]+) 0"\/>/.exec(svg);
    expect(m, "expected the mouth as one quadratic ending level with its start").not.toBeNull();
    const [y0, cdy] = [Number(m![2]), Number(m![4])];
    const dip = cdy / 2;

    const rectTag = /<rect [^>]*\/>/.exec(svg)!;
    const headInnerBottom = attr(rectTag[0], "y") + attr(rectTag[0], "height") - strokeWidth / 2;

    expect(dip, "a dip under 2 units renders as a straight dash at 12px").toBeGreaterThanOrEqual(2);
    expect(
      y0 + dip + strokeWidth / 2,
      `the mouth's lowest painted edge must stay above the head's inner bottom (${String(headInnerBottom)})`,
    ).toBeLessThanOrEqual(headInnerBottom);
  });
});
