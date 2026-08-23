// Property-based tests for transport.ts newRequestID/newMessageID format
// invariants. Verifies prefix, charset, uniqueness, and structural properties
// that consumers depend on (dedup keys, URL-safe path segments, JSON keys).

import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { newRequestID, newMessageID, computeBackoff, BACKOFF_CAP_MS } from "./transport.js";

const VALID_CHARS = /^[a-z0-9-]+$/;

describe("newRequestID property invariants", () => {
  it("always starts with r- prefix", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        return newRequestID().startsWith("r-");
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("contains only [a-z0-9-] characters", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        return VALID_CHARS.test(newRequestID());
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("produces unique IDs over 1000 calls", () => {
    expect.assertions(1);
    const ids = new Set<string>();
    for (let i = 0; i < 1000; i++) {
      ids.add(newRequestID());
    }
    expect(ids.size).toBe(1000);
  });

  it("has structure: r- prefix, base-36 timestamp, separator, entropy", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        const id = newRequestID();
        // Structure: "r-" + timestamp(base36) + "-" + entropy
        const withoutPrefix = id.slice(2);
        const dashIdx = withoutPrefix.indexOf("-");
        // Must have a dash separating timestamp from entropy
        if (dashIdx < 1) {
          return false;
        }
        const timestamp = withoutPrefix.slice(0, dashIdx);
        const entropy = withoutPrefix.slice(dashIdx + 1);
        // Timestamp segment must be non-empty base-36
        if (timestamp.length === 0 || !/^[a-z0-9]+$/.test(timestamp)) {
          return false;
        }
        // Entropy segment must be non-empty base-36
        if (entropy.length === 0 || !/^[a-z0-9]+$/.test(entropy)) {
          return false;
        }
        return true;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });
});

describe("newMessageID property invariants", () => {
  it("always starts with m- prefix", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        return newMessageID().startsWith("m-");
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("contains only [a-z0-9-] characters", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        return VALID_CHARS.test(newMessageID());
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("produces unique IDs over 1000 calls", () => {
    expect.assertions(1);
    const ids = new Set<string>();
    for (let i = 0; i < 1000; i++) {
      ids.add(newMessageID());
    }
    expect(ids.size).toBe(1000);
  });

  it("has same structure as newRequestID but with m- prefix", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        const id = newMessageID();
        const withoutPrefix = id.slice(2);
        const dashIdx = withoutPrefix.indexOf("-");
        if (dashIdx < 1) {
          return false;
        }
        const timestamp = withoutPrefix.slice(0, dashIdx);
        const entropy = withoutPrefix.slice(dashIdx + 1);
        if (timestamp.length === 0 || !/^[a-z0-9]+$/.test(timestamp)) {
          return false;
        }
        if (entropy.length === 0 || !/^[a-z0-9]+$/.test(entropy)) {
          return false;
        }
        return true;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });
});

describe("computeBackoff property invariants", () => {
  it("delay is always in [0, backoffMs)", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.nat(60_000), (prev) => {
        const { delay, backoffMs } = computeBackoff(prev);
        return delay >= 0 && delay < backoffMs;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("backoffMs doubles from previous (capped at BACKOFF_CAP_MS)", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.nat(60_000), (prev) => {
        const { backoffMs } = computeBackoff(prev);
        if (prev === 0) {
          return backoffMs === 500;
        }
        const expected = Math.min(prev * 2, BACKOFF_CAP_MS);
        return backoffMs === expected;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("backoffMs never exceeds BACKOFF_CAP_MS", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.nat(100_000), (prev) => {
        const { backoffMs } = computeBackoff(prev);
        return backoffMs <= BACKOFF_CAP_MS;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("sequence from 0 is monotonically non-decreasing in backoffMs", () => {
    let prev = 0;
    for (let i = 0; i < 20; i++) {
      const { backoffMs } = computeBackoff(prev);
      expect(backoffMs).toBeGreaterThanOrEqual(prev === 0 ? 0 : prev);
      prev = backoffMs;
    }
  });

  it("first call from 0 yields backoffMs=500", () => {
    const { backoffMs } = computeBackoff(0);
    expect(backoffMs).toBe(500);
  });
});

describe("computeBackoff sequence-level invariants", () => {
  it("arbitrary prev-value sequences maintain monotonicity and bounds", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.array(fc.nat(60_000), { minLength: 2, maxLength: 50 }), (prevValues) => {
        for (const prev of prevValues) {
          const { delay, backoffMs } = computeBackoff(prev);
          // Invariant 1: backoffMs never exceeds BACKOFF_CAP_MS
          if (backoffMs > BACKOFF_CAP_MS) {
            return false;
          }
          // Invariant 2: delay is always in [0, backoffMs)
          if (delay < 0 || delay >= backoffMs) {
            return false;
          }
          // Invariant 3: after a successful reconnect (prev=0), resets to 500ms
          if (prev === 0 && backoffMs !== 500) {
            return false;
          }
        }
        return true;
      }),
      { numRuns: 200 },
    );
    expect(result.failed).toBe(false);
  });

  it("simulated reconnect sequences with resets maintain invariants", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.array(fc.boolean(), { minLength: 5, maxLength: 30 }), (resetPattern) => {
        let prev = 0;
        for (const shouldReset of resetPattern) {
          if (shouldReset) {
            prev = 0;
          }
          const { delay, backoffMs } = computeBackoff(prev);
          if (backoffMs > BACKOFF_CAP_MS) {
            return false;
          }
          if (delay < 0 || delay >= backoffMs) {
            return false;
          }
          if (prev === 0 && backoffMs !== 500) {
            return false;
          }
          prev = backoffMs;
        }
        return true;
      }),
      { numRuns: 200 },
    );
    expect(result.failed).toBe(false);
  });
});

describe("newMessageID/newRequestID structural equivalence", () => {
  it("newMessageID suffix has same format as newRequestID suffix", () => {
    expect.assertions(1);
    const suffixPattern = /^[a-z0-9]+-[a-z0-9]+$/;
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        const mid = newMessageID();
        const rid = newRequestID();
        if (!mid.startsWith("m-")) {
          return false;
        }
        if (!rid.startsWith("r-")) {
          return false;
        }
        // Both suffixes must match timestamp-entropy structure
        if (!suffixPattern.test(mid.slice(2))) {
          return false;
        }
        if (!suffixPattern.test(rid.slice(2))) {
          return false;
        }
        return true;
      }),
      { numRuns: 50 },
    );
    expect(result.failed).toBe(false);
  });

  it("newMessageID and newRequestID share the same character class after prefix", () => {
    expect.assertions(1);
    const charClass = /^[a-z0-9-]+$/;
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        const mid = newMessageID();
        const rid = newRequestID();
        return charClass.test(mid.slice(2)) && charClass.test(rid.slice(2));
      }),
      { numRuns: 50 },
    );
    expect(result.failed).toBe(false);
  });
});
