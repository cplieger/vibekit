// The helper's own drift guard, the same mechanism scroll-mock.test.ts and
// tabs-mock.test.ts carry: `tabFreshnessMock` is only useful while it is TOTAL,
// and the failure a partial one produces does not name the missing export.
import { describe, it, expect } from "vitest";

import * as freshness from "../tab-freshness.js";
import { tabFreshnessMock } from "./tab-freshness-mock.js";

// Types are erased at runtime, so both sides compare the VALUE surface — exactly
// what an ESM link needs to resolve.
const real = Object.keys(freshness as Record<string, unknown>).sort();
const mocked = Object.keys(tabFreshnessMock).sort();

describe("the tab-freshness.js mock helper stays total", () => {
  it("names every value tab-freshness.ts exports", () => {
    expect(real.length, "the real module exported nothing; the import is wrong").toBeGreaterThan(0);
    const missing = real.filter((k) => !mocked.includes(k));
    expect(missing, `tab-freshness-mock.ts is missing: ${missing.join(", ")}`).toEqual([]);
  });

  it("names nothing tab-freshness.ts does not export, so a rename cannot hide behind it", () => {
    const extra = mocked.filter((k) => !real.includes(k));
    expect(extra, `tab-freshness-mock.ts has stale entries: ${extra.join(", ")}`).toEqual([]);
  });
});
