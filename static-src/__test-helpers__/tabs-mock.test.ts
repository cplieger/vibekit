// The helper's own drift guard. `tabsMock()` is only useful while it is TOTAL:
// the moment `tabs.ts` exports a name the helper omits, every file that spreads
// it goes back to the failure mode the helper exists to remove, and that failure
// does not name the missing export.
//
// A browser test, not a node one: `tabs.ts` reaches `router.ts`, which registers a
// popstate listener at module scope, so importing the real surface needs a window.
import { describe, it, expect } from "vitest";

import { tabsMock } from "./tabs-mock.js";
import * as tabs from "../tabs.js";

describe("the tabs.js mock helper stays total", () => {
  it("names every value tabs.ts exports", () => {
    // Types are erased at runtime, so this compares the VALUE surface, which is
    // exactly what an ESM link needs to resolve.
    const real = Object.keys(tabs as Record<string, unknown>).sort();
    const mocked = Object.keys(tabsMock()).sort();
    const missing = real.filter((k) => !mocked.includes(k));
    expect(missing, `tabs-mock.ts is missing: ${missing.join(", ")}`).toEqual([]);
  });

  it("names nothing tabs.ts does not export, so a rename cannot hide behind it", () => {
    const real = Object.keys(tabs as Record<string, unknown>);
    const extra = Object.keys(tabsMock()).filter((k) => !real.includes(k));
    expect(extra, `tabs-mock.ts has stale entries: ${extra.join(", ")}`).toEqual([]);
  });
});
