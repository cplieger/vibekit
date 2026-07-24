// Vitest config for Stryker mutation runs ONLY (stryker.config.json points
// here; plain `npx vitest run` keeps using vitest.config.ts).
//
// Why a separate config: Stryker's instrumentation slows hot loops several-x,
// and the heavy fast-check property suites (e.g. lineDiff's Hirschberg
// invariants over large inputs) then blow the base 5s per-test cap during the
// initial dry run — before any mutant even runs. Raise the cap for mutation
// runs only; per-MUTANT runaway protection stays with Stryker's own
// timeoutMS/timeoutFactor, not vitest's cap.
//
// Deliberately NO bare imports (vitest/config): spreading the base config
// needs no defineConfig/mergeConfig helper — they are identity functions over
// plain objects for this shape.
import base from "./vitest.config.js";

export default {
  ...base,
  test: {
    ...base.test,
    // 90s: the Hirschberg large-input properties (2001x2001-line diffs,
    // 3 fast-check runs) tipped over the previous 30s cap once diff.ts +
    // the instrumentation overhead grew (weekly-stryker 2026-07-18 dry-run
    // interrupt). Normal vitest runs keep the base cap.
    testTimeout: 90_000,
  },
};
