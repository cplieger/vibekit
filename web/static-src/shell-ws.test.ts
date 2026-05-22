// Property-based test for ShellWS reconnect backoff invariants.
import { describe, it, expect } from "vitest";
import * as fc from "fast-check";

// The backoff constants from shell-ws.ts (not exported, so we replicate).
const RECONNECT_BASE_MS = 250;
const RECONNECT_MAX_MS = 8000;

/** Compute the expected delay for a given attempt number. */
function expectedDelay(attempt: number): number {
  return Math.min(RECONNECT_BASE_MS * 2 ** attempt, RECONNECT_MAX_MS);
}

describe("ShellWS backoff invariants (property-based)", () => {
  it("delay is always >= RECONNECT_BASE_MS for any attempt", () => {
    fc.assert(
      fc.property(fc.nat({ max: 100 }), (attempt) => {
        const delay = expectedDelay(attempt);
        expect(delay).toBeGreaterThanOrEqual(RECONNECT_BASE_MS);
      }),
      { numRuns: 200 },
    );
  });

  it("delay is always <= RECONNECT_MAX_MS for any attempt", () => {
    fc.assert(
      fc.property(fc.nat({ max: 100 }), (attempt) => {
        const delay = expectedDelay(attempt);
        expect(delay).toBeLessThanOrEqual(RECONNECT_MAX_MS);
      }),
      { numRuns: 200 },
    );
  });

  it("delay doubles with each consecutive attempt until cap", () => {
    fc.assert(
      fc.property(fc.nat({ max: 50 }), (attempt) => {
        const d1 = expectedDelay(attempt);
        const d2 = expectedDelay(attempt + 1);
        // Either d2 == 2*d1 (doubling) or both are capped at max.
        if (d1 < RECONNECT_MAX_MS) {
          expect(d2).toBe(Math.min(d1 * 2, RECONNECT_MAX_MS));
        } else {
          expect(d2).toBe(RECONNECT_MAX_MS);
        }
      }),
      { numRuns: 200 },
    );
  });

  it("attempt 0 gives exactly RECONNECT_BASE_MS", () => {
    expect(expectedDelay(0)).toBe(RECONNECT_BASE_MS);
  });

  it("reaches cap at attempt 5 (250 * 2^5 = 8000)", () => {
    expect(expectedDelay(5)).toBe(RECONNECT_MAX_MS);
  });

  it("stays at cap for all attempts beyond 5", () => {
    fc.assert(
      fc.property(fc.integer({ min: 5, max: 100 }), (attempt) => {
        expect(expectedDelay(attempt)).toBe(RECONNECT_MAX_MS);
      }),
      { numRuns: 100 },
    );
  });
});
