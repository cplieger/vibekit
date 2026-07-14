// Vitest config for Stryker mutation runs ONLY (stryker.config.json points
// here; plain `npx vitest run` keeps using vitest.config.ts).
//
// Why a separate config: Stryker's instrumentation slows hot loops several-x,
// and the heavy fast-check property suites (e.g. lineDiff's Hirschberg
// invariants over large inputs) then blow the base 5s per-test cap during the
// initial dry run — before any mutant even runs. Raise the cap for mutation
// runs only; per-MUTANT runaway protection stays with Stryker's own
// timeoutMS/timeoutFactor, not vitest's cap.
import { defineConfig, mergeConfig } from "vitest/config";
import base from "./vitest.config.js";

export default mergeConfig(
  base,
  defineConfig({
    test: {
      testTimeout: 30000,
    },
  }),
);
