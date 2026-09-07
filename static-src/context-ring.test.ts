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
  CONTEXT_DEGRADED_TOKENS,
  CONTEXT_FADING_TOKENS,
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

describe("contextStroke", () => {
  // A continuous ramp, so two token counts 100 apart resolve to different mixes.
  // At one decimal that is the module's own resolution floor.
  it("is continuous across nearby token counts", () => {
    const a = contextStroke(5, 1_000_000); // 50_000 tokens
    const b = contextStroke(5.01, 1_000_000); // 50_100 tokens
    expect(a).toBe("color-mix(in oklch, var(--c-yellow) 50.0%, var(--c-green))");
    expect(b).toBe("color-mix(in oklch, var(--c-yellow) 50.1%, var(--c-green))");
    expect(a).not.toBe(b);
  });

  // THE POINT OF THE WHOLE RAMP. 25% of a 200K window is 50K tokens — half way
  // through the first segment — while 25% of a 1M window is 250K, past degraded.
  // A percentage-keyed implementation cannot tell these apart.
  it("resolves one percentage differently on a 200K and a 1M window", () => {
    expect(contextStroke(25, 200_000)).toBe(
      "color-mix(in oklch, var(--c-yellow) 50.0%, var(--c-green))",
    );
    expect(contextStroke(25, 1_000_000)).toBe("var(--c-red)");
  });

  it.each([
    // pct, window, expected — the report's own table, both windows.
    [25, 200_000, "color-mix(in oklch, var(--c-yellow) 50.0%, var(--c-green))"], // 50K
    [50, 200_000, "color-mix(in oklch, var(--c-red) 0.0%, var(--c-yellow))"], // 100K
    [75, 200_000, "color-mix(in oklch, var(--c-red) 50.0%, var(--c-yellow))"], // 150K
    [100, 200_000, "var(--c-red)"], // 200K
    [5, 1_000_000, "color-mix(in oklch, var(--c-yellow) 50.0%, var(--c-green))"], // 50K
    [10, 1_000_000, "color-mix(in oklch, var(--c-red) 0.0%, var(--c-yellow))"], // 100K
    [15, 1_000_000, "color-mix(in oklch, var(--c-red) 50.0%, var(--c-yellow))"], // 150K
    [20, 1_000_000, "var(--c-red)"], // 200K
    [40, 1_000_000, "var(--c-red)"], // 400K
  ])("maps %i percent of a %i-token window to %s", (pct, size, want) => {
    expect(contextStroke(pct, size)).toBe(want);
  });

  // Saturates rather than extrapolating: no mix percentage above 100 is ever
  // emitted, and 4M tokens looks exactly like 200K.
  it.each([
    [100, CONTEXT_DEGRADED_TOKENS],
    [100, 400_000],
    [100, 4_000_000],
    [50, 800_000],
  ])("saturates at red for %i percent of %i tokens", (pct, size) => {
    expect(contextStroke(pct, size)).toBe("var(--c-red)");
  });

  it("emits green at the very start of the ramp", () => {
    expect(contextStroke(0, 1_000_000)).toBe(
      "color-mix(in oklch, var(--c-yellow) 0.0%, var(--c-green))",
    );
  });

  it("hands the fading threshold to the second segment, not the first", () => {
    // Exactly at 100K the mix names --c-red, so the boundary belongs to segment 2.
    expect(contextStroke(100, CONTEXT_FADING_TOKENS)).toBe(
      "color-mix(in oklch, var(--c-red) 0.0%, var(--c-yellow))",
    );
  });

  describe("with an unknown window", () => {
    // contextSize 0 means the window is not known yet, so the ramp runs over the
    // percentage: the wrong unit, and the only one available. Never a division by
    // zero, and never a bare token count.
    it.each([
      [0, "color-mix(in oklch, var(--c-yellow) 0.0%, var(--c-green))"],
      [25, "color-mix(in oklch, var(--c-yellow) 50.0%, var(--c-green))"],
      [50, "color-mix(in oklch, var(--c-red) 0.0%, var(--c-yellow))"],
      [75, "color-mix(in oklch, var(--c-red) 50.0%, var(--c-yellow))"],
      [100, "color-mix(in oklch, var(--c-red) 100.0%, var(--c-yellow))"],
    ])("maps %i percent to %s", (pct, want) => {
      expect(contextStroke(pct, 0)).toBe(want);
    });

    it.each([0, 25, 50, 75, 100])("produces no NaN or Infinity at %i percent", (pct) => {
      const got = contextStroke(pct, 0);
      expect(got).not.toContain("NaN");
      expect(got).not.toContain("Infinity");
    });

    it("treats a negative window as unknown too", () => {
      expect(contextStroke(25, -1)).toBe(contextStroke(25, 0));
    });
  });

  it("clamps an out-of-range percentage at both ends", () => {
    expect(contextStroke(-20, 1_000_000)).toBe(contextStroke(0, 1_000_000));
    expect(contextStroke(140, 200_000)).toBe(contextStroke(100, 200_000));
  });

  // Tokens only: a literal here would fork the palette and be wrong in one theme.
  it.each([
    [10, 1_000_000],
    [50, 200_000],
    [25, 0],
    [100, 400_000],
  ])("names only design tokens for %i percent of %i", (pct, size) => {
    expect(contextStroke(pct, size)).toMatch(/^(var\(--c-[a-z]+\)|color-mix\(in oklch, .+\))$/);
    expect(contextStroke(pct, size)).not.toMatch(/#|rgb|oklch\(\d/);
  });
});
