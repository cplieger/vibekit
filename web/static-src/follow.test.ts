// @vitest-environment happy-dom
// Property-based tests for follow.ts pure geometry helpers.
import { describe, it, expect } from "vitest";
import * as fc from "fast-check";
import { computeVirtualWindow, computeScrollTarget } from "./follow.js";

describe("computeVirtualWindow", () => {
  it("returns valid range for positive inputs", () => {
    fc.assert(
      fc.property(
        fc.integer({ min: 1, max: 10_000 }),
        fc.integer({ min: 1, max: 100 }),
        fc.integer({ min: 0, max: 200_000 }),
        fc.integer({ min: 1, max: 2000 }),
        fc.integer({ min: 0, max: 50 }),
        (totalLines, lineHeight, scrollTop, viewportHeight, bufferLines) => {
          const w = computeVirtualWindow(
            totalLines,
            lineHeight,
            scrollTop,
            viewportHeight,
            bufferLines,
          );
          expect(w.startLine).toBeGreaterThanOrEqual(0);
          expect(w.endLine).toBeLessThanOrEqual(totalLines);
          expect(w.paddingTopPx).toBeGreaterThanOrEqual(0);
          expect(w.paddingBottomPx).toBeGreaterThanOrEqual(0);
          // When scrollTop is within document bounds, startLine <= endLine
          if (scrollTop <= totalLines * lineHeight) {
            expect(w.startLine).toBeLessThanOrEqual(w.endLine);
          }
        },
      ),
      { numRuns: 500 },
    );
  });

  it("zero lines yields empty window", () => {
    const w = computeVirtualWindow(0, 20, 0, 600, 10);
    expect(w.startLine).toBe(0);
    expect(w.endLine).toBe(0);
    expect(w.paddingTopPx).toBe(0);
    expect(w.paddingBottomPx).toBe(0);
  });

  it("zero viewport height yields minimal window (buffer only)", () => {
    const w = computeVirtualWindow(100, 20, 0, 0, 5);
    expect(w.startLine).toBe(0);
    // With viewport=0, visibleCount = ceil(0/20) + 5*2 = 10
    expect(w.endLine).toBe(10);
  });

  it("negative scrollTop is clamped to startLine=0", () => {
    const w = computeVirtualWindow(100, 20, -100, 600, 10);
    expect(w.startLine).toBe(0);
  });

  it("paddingTop + visible + paddingBottom = totalHeight when in bounds", () => {
    fc.assert(
      fc.property(
        fc.integer({ min: 1, max: 5000 }),
        fc.integer({ min: 1, max: 50 }),
        fc.integer({ min: 0, max: 100_000 }),
        fc.integer({ min: 1, max: 1000 }),
        fc.integer({ min: 0, max: 20 }),
        (totalLines, lineHeight, scrollTop, viewportHeight, bufferLines) => {
          const w = computeVirtualWindow(
            totalLines,
            lineHeight,
            scrollTop,
            viewportHeight,
            bufferLines,
          );
          // Only valid when startLine <= endLine (scrollTop within document)
          if (w.startLine <= w.endLine) {
            const visiblePx = (w.endLine - w.startLine) * lineHeight;
            const totalHeight = totalLines * lineHeight;
            expect(w.paddingTopPx + visiblePx + w.paddingBottomPx).toBe(totalHeight);
          }
        },
      ),
      { numRuns: 500 },
    );
  });
});

describe("computeScrollTarget", () => {
  it("returns 0 for line 1 regardless of viewport", () => {
    fc.assert(
      fc.property(
        fc.integer({ min: 1, max: 100 }),
        fc.integer({ min: 1, max: 2000 }),
        (lineHeight, viewportHeight) => {
          const target = computeScrollTarget(1, lineHeight, viewportHeight);
          expect(target).toBe(Math.max(0, 0 - viewportHeight / 2));
        },
      ),
      { numRuns: 100 },
    );
  });

  it("never returns negative", () => {
    fc.assert(
      fc.property(
        fc.integer({ min: 1, max: 10_000 }),
        fc.integer({ min: 1, max: 100 }),
        fc.integer({ min: 1, max: 2000 }),
        (line, lineHeight, viewportHeight) => {
          expect(computeScrollTarget(line, lineHeight, viewportHeight)).toBeGreaterThanOrEqual(0);
        },
      ),
      { numRuns: 500 },
    );
  });

  it("zero viewport returns targetTop directly", () => {
    const target = computeScrollTarget(10, 20, 0);
    // (10-1)*20 - 0/2 = 180
    expect(target).toBe(180);
  });

  it("negative scrollTop scenario: line=0 edge", () => {
    // line=0 means targetTop = -1 * lineHeight, clamped to 0
    const target = computeScrollTarget(0, 20, 600);
    expect(target).toBe(0);
  });
});
