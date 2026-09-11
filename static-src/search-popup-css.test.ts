// The revealed field's glyphs as BOXES. Size and stroke come only from the `ic-*`
// tier class the producer emits, so a class replacement leaves an unsized SVG that
// `flex-shrink: 0` forbids the row squeezing back: 370px in a 363px row against
// 16px in a 35px one. An element-presence assertion cannot see either number.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { createSearchPopup } from "./search-popup.js";
import type { FindKind } from "./find-registry.js";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

interface Box {
  glyph: SVGElement;
  closeGlyph: SVGElement;
  /** An `ic-ui` glyph this factory never touched, so a size claim resolves from the
   *  tier rather than from a number pinned here — `--icon-ui` is 1rem on a fine
   *  pointer and 1.25rem on a coarse one. */
  reference: SVGElement;
  input: HTMLInputElement;
  row: HTMLElement;
}

function open(kind: FindKind): Box {
  document.body.innerHTML = `
    <button type="button" id="find-btn" aria-pressed="false"></button>
    <svg id="reference" class="ic-ui" viewBox="0 0 24 24"></svg>
    <div id="host"></div>`;
  const popup = createSearchPopup<string>({
    id: "probe",
    kind,
    label: "Probe things",
    placeholder: "Probe\u2026",
    host: () => document.getElementById("host"),
    query: (q) => q,
    render: () => undefined,
  });
  expect(popup.open()).toBe(true);
  const glyph = document.querySelector<SVGElement>(".page-find-icon");
  const closeGlyph = document.querySelector<SVGElement>(".page-find-btn svg");
  const reference = document.querySelector<SVGElement>("#reference");
  const input = document.getElementById("probe-input");
  const row = document.querySelector<HTMLElement>(".page-find-row");
  if (
    glyph === null ||
    closeGlyph === null ||
    reference === null ||
    row === null ||
    !(input instanceof HTMLInputElement)
  ) {
    throw new Error("the opened popup is missing the row, a glyph, the reference or the field");
  }
  return { glyph, closeGlyph, reference, input, row };
}

describe("the search popup's glyphs keep the size their producer gave them", () => {
  it("carries the tier class as well as the skin, for both kinds", () => {
    for (const kind of ["search", "filter"] as const) {
      const { glyph } = open(kind);
      expect(glyph.classList.contains("ic-ui")).toBe(true);
      expect(glyph.classList.contains("page-find-icon")).toBe(true);
    }
  });

  it("resolves the leading glyph to the tier's size, for both kinds", () => {
    // Computed `inline-size` rather than a rect: the popup reveals under a scale
    // transform, which a rect taken mid-reveal carries (15.68 for a 16px box).
    for (const kind of ["search", "filter"] as const) {
      const { glyph, reference } = open(kind);
      const g = getComputedStyle(glyph);
      const ref = getComputedStyle(reference);
      expect(Number.parseFloat(ref.inlineSize)).toBeGreaterThan(0);
      expect(g.inlineSize).toBe(g.blockSize);
      expect(g.inlineSize).toBe(ref.inlineSize);
      expect(g.blockSize).toBe(ref.blockSize);
    }
  });

  it("agrees with the × beside it on size and on stroke weight", () => {
    // Both properties are the tier's, so one glyph sized at its use site disagrees
    // with its row on both at once.
    const { glyph, closeGlyph, reference } = open("search");
    const g = getComputedStyle(glyph);
    const x = getComputedStyle(closeGlyph);
    const ref = getComputedStyle(reference);
    expect(x.inlineSize).toBe(g.inlineSize);
    expect(x.blockSize).toBe(g.blockSize);
    expect(x.strokeWidth).toBe(ref.strokeWidth);
    expect(g.strokeWidth).toBe(ref.strokeWidth);
  });

  it("leaves the row the height of the field rather than stretching it", () => {
    const { row, input, glyph } = open("search");
    expect(row.getBoundingClientRect().height).toBe(input.getBoundingClientRect().height);
    expect(glyph.getBoundingClientRect().height).toBeLessThan(input.getBoundingClientRect().height);
  });
});
