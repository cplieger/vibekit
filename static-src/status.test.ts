import { describe, it, expect } from "vitest";
import { formatTokens, formatMetering, contextReserve } from "./status-format.js";

describe("formatTokens", () => {
  const cases: [number, string][] = [
    [0, "0"],
    [1, "1"],
    [999, "999"],
    [1000, "1.0K"],
    [1500, "1.5K"],
    [999_999, "1000.0K"],
    [1_000_000, "1.0M"],
    [1_500_000, "1.5M"],
    [10_000_000, "10.0M"],
  ];

  it.each(cases)("formatTokens(%d) === %s", (input, expected) => {
    expect(formatTokens(input)).toBe(expected);
  });
});

describe("formatMetering", () => {
  const cases: [number, string][] = [
    [0, "0"],
    [1, "1"],
    [42, "42"],
    [999, "999"],
    [1000, "1.0K"],
    [1500, "1.5K"],
    [999_999, "1000.0K"],
    [1_000_000, "1.00M"],
    [1_500_000, "1.50M"],
    [0.5, "0.50"],
    [3.14, "3.14"],
    [99.9, "99.90"],
  ];

  it.each(cases)("formatMetering(%d) === %s", (input, expected) => {
    expect(formatMetering(input)).toBe(expected);
  });
});

describe("contextReserve", () => {
  // `buffer` is the percentage kiro-cli holds BACK
  // (compaction.excludeContextWindowPercent), so the threshold is 100 - buffer
  // and the reserve runs from there round to 12 o'clock. lengthPct is the dash
  // extent (the element carries pathLength="100", so a percent IS the number the
  // SVG wants) and rotateDeg places its start, offset -90 because the ring's 0%
  // is 12 o'clock while SVG's 0deg is 3 o'clock.
  const cases: [number, { thresholdPct: number; lengthPct: number; rotateDeg: number }][] = [
    // kiro-cli's own default: a 10% buffer, so compaction fires at 90% and the
    // reserve is the last tenth of the circle.
    [10, { thresholdPct: 90, lengthPct: 10, rotateDeg: 234 }],
    // The settings input's cap. Reachable through the UI, so it has to be right.
    [50, { thresholdPct: 50, lengthPct: 50, rotateDeg: 90 }],
    // LOW CLAMP END. Nothing held back, so there is no reserve: a zero-length
    // dash, which paints nothing under butt caps. A round cap would leave a dot
    // at 12 o'clock claiming a threshold that does not exist, which is why the
    // markup carries no stroke-linecap.
    [0, { thresholdPct: 100, lengthPct: 0, rotateDeg: 270 }],
    // HIGH CLAMP END. Everything held back: the reserve is the whole circle and
    // the threshold is 0%.
    [100, { thresholdPct: 0, lengthPct: 100, rotateDeg: -90 }],
    // BEYOND both ends. fetchKiroSetting admits [0, 100] while the settings input
    // caps at [0, 50], so a value over 50 cannot be typed here but can arrive
    // from kiro-cli's config, the TUI or a hand-edited file — and a negative one
    // from a corrupted one. Neither may produce a rotation outside the circle or
    // a negative dash.
    [-5, { thresholdPct: 100, lengthPct: 0, rotateDeg: 270 }],
    [105, { thresholdPct: 0, lengthPct: 100, rotateDeg: -90 }],
  ];

  it.each(cases)("contextReserve(%d)", (buffer, expected) => {
    expect(contextReserve(buffer)).toEqual(expected);
  });

  it("never derives a dash longer than the circle or a rotation off it", () => {
    for (let buffer = -20; buffer <= 120; buffer++) {
      const { thresholdPct, lengthPct, rotateDeg } = contextReserve(buffer);
      expect(lengthPct, `buffer ${String(buffer)}`).toBeGreaterThanOrEqual(0);
      expect(lengthPct, `buffer ${String(buffer)}`).toBeLessThanOrEqual(100);
      expect(thresholdPct + lengthPct, `buffer ${String(buffer)}`).toBe(100);
      expect(rotateDeg, `buffer ${String(buffer)}`).toBeGreaterThanOrEqual(-90);
      expect(rotateDeg, `buffer ${String(buffer)}`).toBeLessThanOrEqual(270);
    }
  });
});
