// The model glyph's geometry, derived rather than snapshotted.
//
// This icon renders at 12px and at 20px, and the 12px size is what shaped it
// twice. First the eyes: they were STROKED rings (r=2.5, stroke-width 2) whose
// outer edges left one user unit between them, which is 0.50 CSS px at 12px.
// Under a device pixel, so the two rings bridged into a blob, and because the
// centres sat on integers the 1-unit wall split 25/75 across two pixel columns
// and painted two greys instead of a gap.
//
// Then the head. An antenna dot and stem owned the top 6 of the 22 units a
// centred content bbox gets, so the head was 15 units tall and its INTERIOR 13
// — 6.5 CSS px at 12px to hold two eyes and a mouth, with the mouth's stroke
// ending half a unit off the inner bottom. The antenna itself quantises to a 1px
// tick at that size, so it was buying nothing with the room it took. The head
// now fills the box and the face has 18 interior units.
//
// The invariants worth pinning are therefore not "the eyes are circles" or "there
// is an antenna" but: the gap between the eyes survives quantisation at the
// SMALLEST size this icon is rendered at, the head owns the whole square, and the
// face fits inside it with the mouth's dip counted. Everything here is computed
// from the shipped string.
//
// Node environment: no DOM, the icon is text.

import { describe, it, expect } from "vitest";
import { ICON_MODEL, ICON_MODEL_UI } from "./icons.js";

/** Every render size this glyph ships at, in CSS pixels. Both are real call
 *  sites: the composer's model pill (model-switcher.ts) at the inline tier and
 *  the empty-chat picker's heading (picker.ts) at the ui tier. The second used
 *  to be 20, which was the number its own markup spelled; the tiers in
 *  01-tokens.css put it at 16 on a fine pointer and 18 on a coarse one, so 16 is
 *  the smaller of the two it can render at. A new size belongs here, because the
 *  floors below are evaluated at the smallest member. */
const RENDER_SIZES = [12, 16];

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

/** The eyes are the only pair of circles sharing a radius AND a y. Found by that
 *  property rather than by index, so reordering the markup cannot make this test
 *  read the wrong two shapes — and if a third circle is ever added, this fails
 *  loudly instead of silently measuring it. */
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

/** Declared once on the root, so every stroked edge spends half of it outward. */
/** The stroke the glyph was DRAWN against, in viewBox units.
 *
 *  It is no longer an attribute on the tag: 03-base.css owns the rendered
 *  stroke and decouples it from the size with `vector-effect:
 *  non-scaling-stroke`, so one authored number could not describe both tiers
 *  anyway. The geometry asserted below is still laid out against 2 units on the
 *  24-unit grid — that is what makes the head's painted box square and centred —
 *  so the value is declared here rather than read back off a tag that no longer
 *  carries it. */
const DRAWN_STROKE_UNITS = 2;

function strokeWidth(): number {
  return DRAWN_STROKE_UNITS;
}

/** The head: the glyph's only rect, and its whole silhouette. */
function headRect(svg: string): { x: number; y: number; width: number; height: number } {
  const tag = /<rect [^>]*\/>/.exec(svg);
  expect(tag, "expected the head rect").not.toBeNull();
  return {
    x: attr(tag![0], "x"),
    y: attr(tag![0], "y"),
    width: attr(tag![0], "width"),
    height: attr(tag![0], "height"),
  };
}

/** The mouth, as a start point plus the span and dip of one quadratic. The
 *  trailing ` 0` in the pattern is the curve ending level with its start, which
 *  is what makes the dip the only thing that can reach past the geometry. */
function mouth(svg: string): { x0: number; y0: number; dx: number; dip: number } {
  const m = /<path d="M([\d.]+) ([\d.]+)q([\d.]+) ([\d.]+) ([\d.]+) 0"\/>/.exec(svg);
  expect(m, "expected the mouth as one quadratic ending level with its start").not.toBeNull();
  return {
    x0: Number(m![1]),
    y0: Number(m![2]),
    dx: Number(m![5]),
    dip: Number(m![4]) / 2,
  };
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
    ).toBe(body(ICON_MODEL_UI));

    // The two differ only by SIZE TIER now. Neither carries a width, because
    // 03-base.css owns the pixels: the old pair spelled 12 and 20 in their own
    // markup, which is what let them drift in the first place.
    expect(ICON_MODEL).toContain('class="ic-inline"');
    expect(ICON_MODEL_UI).toContain('class="ic-ui"');
    // Scoped to the OPENING TAG: the head rect carries a legitimate width and
    // height of its own, in viewBox units.
    for (const svg of [ICON_MODEL, ICON_MODEL_UI]) {
      const openTag = /^<svg[^>]*>/.exec(svg)![0];
      expect(openTag, "the size belongs to the tier, not to the tag").not.toMatch(
        /\s(?:width|height)=/,
      );
    }
  });

  it("gives the whole box to the head", () => {
    // The head's stroked rect IS the content bbox — there is nothing else
    // outside it. That is the fix this drawing exists for: an antenna dot and
    // stem used to own the top 6 units, so the head was 15 tall of the 22 a
    // centred bbox gets and its interior 13, which is 6.5 CSS px at 12px for two
    // eyes and a mouth. Any element added above the head takes that room back,
    // and it will show up here as a margin over one unit or a non-square box.
    const svg = ICON_MODEL;
    const side = viewBoxSide(svg);
    const half = strokeWidth() / 2;
    const h = headRect(svg);
    const box = {
      top: h.y - half,
      bottom: h.y + h.height + half,
      left: h.x - half,
      right: h.x + h.width + half,
    };

    expect(box.right - box.left, "the head's painted box must be square").toBe(
      box.bottom - box.top,
    );
    expect(box.left + box.right, "the box must be centred horizontally in the viewBox").toBe(side);
    expect(box.top + box.bottom, "the box must be centred vertically too").toBe(side);
    expect(
      box.left,
      "the head must reach the 1-unit margin on every side; a bigger margin means something " +
        "else is claiming space in the viewBox, which is what the antenna did",
    ).toBeLessThanOrEqual(1);
  });

  it("keeps the face inside the head, mouth dip included", () => {
    // The mouth is the one element whose painted extent is not its own
    // coordinates: it is a quadratic, and a quadratic's dip is HALF its control
    // offset. Deepening the dip (1 unit read as a straight dash at 12px) is only
    // safe while the curve plus its stroke stays off the head's inner edge.
    const svg = ICON_MODEL;
    const half = strokeWidth() / 2;
    const h = headRect(svg);
    const m = mouth(svg);
    const [left, right] = eyes(svg);

    const inner = {
      top: h.y + half,
      bottom: h.y + h.height - half,
      left: h.x + half,
      right: h.x + h.width - half,
    };
    const face = {
      top: Math.min(left.cy - left.r, right.cy - right.r),
      bottom: m.y0 + m.dip + half,
      left: Math.min(left.cx - left.r, m.x0 - half),
      right: Math.max(right.cx + right.r, m.x0 + m.dx + half),
    };

    expect(m.dip, "a dip under 2 units renders as a straight dash at 12px").toBeGreaterThanOrEqual(
      2,
    );
    expect(
      face.bottom,
      `the mouth's lowest painted edge must stay above the head's inner bottom (${String(inner.bottom)})`,
    ).toBeLessThanOrEqual(inner.bottom);
    expect(face.top, "the eyes must stay below the head's inner top").toBeGreaterThanOrEqual(
      inner.top,
    );
    expect(face.left, "the face must stay inside the head's left wall").toBeGreaterThanOrEqual(
      inner.left,
    );
    expect(face.right, "the face must stay inside the head's right wall").toBeLessThanOrEqual(
      inner.right,
    );

    // Centred within a unit. The head is symmetric, so a face drifting toward
    // one wall is a composition bug rather than a rendering one — worth a floor
    // because the eye and mouth rows are tuned by hand and half a unit is
    // 0.25 CSS px at 12px, invisible in review and cumulative across edits.
    expect(
      Math.abs((face.top + face.bottom) / 2 - (inner.top + inner.bottom) / 2),
      "the face must sit within a unit of the head interior's vertical centre",
    ).toBeLessThanOrEqual(1);
  });
});
