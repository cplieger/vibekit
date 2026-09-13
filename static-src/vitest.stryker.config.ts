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

// A suite whose SUBJECT is production source TEXT rather than its behaviour.
// `inPlace: true` rewrites every mutate target on disk, wrapping each mutant in
// a `stryMutAct_*` conditional, so such a suite parses the INSTRUMENTED copy of
// the file it means to guard: measured 2026-09-13, this one reported an
// offending rebuild site at `store.ts:2608` against a file 2244 lines long.
// Excluding costs no efficacy, because it imports no production module and can
// therefore kill no mutant; it still runs in `ci / web / validate` and locally,
// which is where a source guard is meant to bite.
const SOURCE_TEXT_SUITES = ["**/turn-base-writers.node.test.ts"];

// Added per PROJECT, never at the root: a project's `exclude` REPLACES the root
// one rather than adding to it (vitest.config.ts states this at `sharedExclude`),
// so a root-level entry here would be silently inert.
const projects = base.test?.projects?.map((project) =>
  typeof project === "object" && "test" in project
    ? {
        ...project,
        test: {
          ...project.test,
          exclude: [...(project.test?.exclude ?? []), ...SOURCE_TEXT_SUITES],
        },
      }
    : project,
);

export default {
  ...base,
  test: {
    ...base.test,
    ...(projects ? { projects } : {}),
    // 90s: the Hirschberg large-input properties (2001x2001-line diffs,
    // 3 fast-check runs) tipped over the previous 30s cap once diff.ts +
    // the instrumentation overhead grew (weekly-stryker 2026-07-18 dry-run
    // interrupt). Normal vitest runs keep the base cap.
    testTimeout: 90_000,
  },
};
