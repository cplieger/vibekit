// ---------------------------------------------------------------------------
// The 16px context indicator's two halves: the compaction band's geometry, and
// the fill's colour ramp.
//
// Every expectation is a hardcoded string. Recomputing one with the same
// arithmetic the module uses would assert nothing, and the ramp's whole point is
// a specific pair of numbers.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";

import {
  CONTEXT_GREEN_PCT,
  CONTEXT_RED_PCT,
  KAS_SUMMARIZATION_PCT,
  KAS_TRUNCATION_PCT,
  contextStroke,
  tokensUsed,
  wedgeDash,
} from "./context-ring.js";

describe("the KAS fallbacks", () => {
  // The two numbers live in ONE place. A second copy is what makes a client and a
  // server disagree about where compaction happens.
  it("mirror KAS's own thresholds", () => {
    expect(KAS_SUMMARIZATION_PCT).toBe(80);
    expect(KAS_TRUNCATION_PCT).toBe(95);
  });
});

describe("wedgeDash", () => {
  // With pathLength="100" every dash number is a percent, so the band from T to
  // 100 is a `100-T` dash preceded by a `T` gap, offset by the band's own width.
  it.each([
    [80, "20 80", "20"],
    [95, "5 95", "5"],
    [50, "50 50", "50"],
    [0, "100 0", "100"],
    [100, "0 100", "0"],
  ])(
    "draws the band from %i percent as dasharray %s offset %s",
    (threshold, dasharray, dashoffset) => {
      expect(wedgeDash(threshold)).toEqual({ dasharray, dashoffset });
    },
  );

  it.each([
    [-40, "100 0", "100"],
    [140, "0 100", "0"],
  ])("clamps an out-of-range threshold of %i", (threshold, dasharray, dashoffset) => {
    expect(wedgeDash(threshold)).toEqual({ dasharray, dashoffset });
  });
});

describe("tokensUsed", () => {
  // ONE derivation, read by the ramp and by the expanded card's readout. Two
  // owners of it with different inputs is what this export exists to prevent.
  it.each([
    [25, 200_000, 50_000],
    [50, 200_000, 100_000],
    [10, 1_000_000, 100_000],
    [0, 1_000_000, 0],
    [25, 0, 0],
  ])("puts %i percent of a %i-token window at %i tokens", (pct, size, want) => {
    expect(tokensUsed(pct, size)).toBe(want);
  });

  it("never reports more tokens than the window holds", () => {
    // The ring saturates at 100%, so the readout beside it may not claim 240K of a
    // 200K window when the wire reports a percentage over 100.
    expect(tokensUsed(120, 200_000)).toBe(200_000);
    expect(tokensUsed(-20, 200_000)).toBe(0);
  });
});

describe("the ramp's thresholds", () => {
  // The ramp exists to warm toward the compaction band, so red must land BEFORE
  // it. A threshold at or past summarization would make the fill reach the band
  // and the band's own colour at the same moment.
  it("reach red before KAS summarizes", () => {
    expect(CONTEXT_GREEN_PCT).toBe(50);
    expect(CONTEXT_RED_PCT).toBe(70);
    expect(CONTEXT_GREEN_PCT).toBeLessThan(CONTEXT_RED_PCT);
    expect(CONTEXT_RED_PCT).toBeLessThan(KAS_SUMMARIZATION_PCT);
  });
});

describe("contextStroke", () => {
  // A continuous ramp inside the warming band, so two nearby percentages resolve
  // to different mixes. At one decimal that is the module's own resolution floor.
  it("is continuous across nearby percentages", () => {
    const a = contextStroke(55);
    const b = contextStroke(55.01);
    expect(a).toBe("color-mix(in oklch, var(--c-yellow) 50.0%, var(--c-green))");
    expect(b).toBe("color-mix(in oklch, var(--c-yellow) 50.1%, var(--c-green))");
    expect(a).not.toBe(b);
  });

  // THE POINT OF THE WHOLE RAMP. One percentage is one colour: the window's size
  // is not an input, so the ring cannot disagree with the number beside it.
  it("resolves one percentage to one colour whatever the window", () => {
    expect(contextStroke(25)).toBe("var(--c-green)");
    expect(contextStroke(75)).toBe("var(--c-red)");
  });

  it.each([
    // pct, expected — every segment boundary and each segment's midpoint.
    [0, "var(--c-green)"],
    [25, "var(--c-green)"],
    [50, "var(--c-green)"],
    [55, "color-mix(in oklch, var(--c-yellow) 50.0%, var(--c-green))"],
    [60, "color-mix(in oklch, var(--c-red) 0.0%, var(--c-yellow))"],
    [65, "color-mix(in oklch, var(--c-red) 50.0%, var(--c-yellow))"],
    [70, "var(--c-red)"],
    [80, "var(--c-red)"],
    [100, "var(--c-red)"],
  ])("maps %i percent to %s", (pct, want) => {
    expect(contextStroke(pct)).toBe(want);
  });

  // Green HOLDS through the first band rather than warming across it — the whole
  // reason the thresholds moved. A ramp starting at 0 shows a warm ring at 40%.
  it("holds green across the whole first band", () => {
    for (const pct of [0, 10, 20, 30, 40, 49.9, CONTEXT_GREEN_PCT]) {
      expect(contextStroke(pct)).toBe("var(--c-green)");
    }
  });

  // Saturates rather than extrapolating: no mix percentage above 100 is ever
  // emitted, and 100% looks exactly like the red threshold.
  it.each([CONTEXT_RED_PCT, 75, 90, 100])("saturates at red for %i percent", (pct) => {
    expect(contextStroke(pct)).toBe("var(--c-red)");
  });

  it("hands the yellow midpoint to the second segment, not the first", () => {
    // Exactly at the midpoint the mix names --c-red, so the boundary belongs to
    // segment 2 — the same rule the green threshold follows the other way.
    expect(contextStroke(60)).toBe("color-mix(in oklch, var(--c-red) 0.0%, var(--c-yellow))");
  });

  it("produces no NaN or Infinity anywhere on the ramp", () => {
    for (const pct of [0, 25, 50, 55, 60, 65, 70, 100]) {
      const got = contextStroke(pct);
      expect(got).not.toContain("NaN");
      expect(got).not.toContain("Infinity");
    }
  });

  it("clamps an out-of-range percentage at both ends", () => {
    expect(contextStroke(-20)).toBe(contextStroke(0));
    expect(contextStroke(140)).toBe(contextStroke(100));
  });

  // Tokens only: a literal here would fork the palette and be wrong in one theme.
  it.each([0, 50, 55, 65, 100])("names only design tokens at %i percent", (pct) => {
    expect(contextStroke(pct)).toMatch(/^(var\(--c-[a-z]+\)|color-mix\(in oklch, .+\))$/);
    expect(contextStroke(pct)).not.toMatch(/#|rgb|oklch\(\d/);
  });
});
