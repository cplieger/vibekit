// Vitest 5 configuration for vibekit TypeScript unit tests.
//
// Two projects, and the DEFAULT is the browser. A test file runs in a real
// headless Chromium unless its name opts out, because the browser is the
// environment this client actually ships into and a DOM emulator got several of
// these assertions wrong for free. There is no `environment` option on the
// browser side and no per-file `@vitest-environment` pragma: Browser Mode is not
// an environment, it is a runner.
//
// The opt-out is the `.node.test.ts` suffix, and it is load-bearing rather than
// decorative: placement has to be readable off the filename because one of the
// two reasons a file needs Node fails SILENTLY when it is misplaced.
//
//   - A test that needs Node capabilities (spawning a process, walking a
//     directory) throws on the import when it lands in the browser. Loud,
//     self-correcting.
//   - A test that needs the DOM to be ABSENT does not. It passes vacuously,
//     having exercised nothing. `attention-no-dom.node.test.ts` is that case:
//     its subject is an architectural invariant enforced by module-load failure,
//     so the reason lives in the stem (`no-dom`) and the placement in the suffix
//     (`.node`). It asserts its own premise for the same reason.
//
// Fuzz keeps its own axis: `*.fuzz.test.ts` is how ts-ci selects fuzz targets,
// and a DOM fuzz test needs no marker here at all.
//
// `channel: "chromium"` opts into Chromium's newer headless mode, the real
// browser rather than the separate headless-shell build. CI installs it with
// `npx playwright install --with-deps chromium`; locally it is a one-time
// `npx --no-install playwright install chromium`.
//
// Run: vitest --run (single pass) or vitest (watch mode)
import { type PlaywrightBrowserProvider, playwright } from "@vitest/browser-playwright";
import { configDefaults, defineConfig } from "vitest/config";
import { resolve } from "node:path";

// Hold Chromium's request interception permanently ON, so a hoisted `vi.mock`
// cannot silently fail to apply.
//
// `vi.mock` in Browser Mode is served by a `context.route()` handler, and the
// provider unroutes every one of a session's routes when a test file ends. The
// route count therefore crosses ZERO between files, and Playwright drives
// interception off the count: it emits CDP `Fetch.disable` then `Fetch.enable`
// around every file that mocks. Chromium acks `Fetch.enable` BEFORE interception
// is live, so a module request issued inside that window is served the REAL
// module. The ES registry caches it for the rest of the file, so every test in
// one random file dies in its own hook on
// `vi.mocked(x).mockReset is not a function`. Chromium-only: Firefox and WebKit
// do not use the CDP Fetch path.
//
// One never-matching anchor route per context keeps the count above zero, so the
// toggle never fires again. Upstream is vitest-dev/vitest#8339 — open, root-
// caused with CDP traces in the thread, and NOT fixed in 4.1.11 nor in 5.0.0
// (re-read off the installed provider: `createContext` still routes nothing), so
// there is no version to wait for.
//
// Measured over this 233-file browser project: 69.6s without, 71.2s with. The
// tradeoff is that interception stays active for files that mock nothing, which
// is cheap here only because `fileParallelism: false` below means one session.
function playwrightWithAlwaysOnInterception(...args: Parameters<typeof playwright>) {
  const provider = playwright(...args);
  return {
    ...provider,
    providerFactory(...factoryArgs: Parameters<typeof provider.providerFactory>) {
      const instance = provider.providerFactory(...factoryArgs) as PlaywrightBrowserProvider;
      // Per CONTEXT, not per session: `persistentContext` shares one context
      // across sessions, and routing the same anchor twice would leave a second
      // matcher behind for the run's lifetime.
      const anchored = new WeakSet<object>();
      const openPage = instance.openPage.bind(instance);
      instance.openPage = async (sessionId, url, options) => {
        await openPage(sessionId, url, options);
        const context = instance.contexts.get(sessionId);
        if (context && !anchored.has(context)) {
          anchored.add(context);
          await context.route(
            () => false,
            // Unreachable, because the predicate never matches. Deferring rather
            // than an empty body means a request would still be answered if that
            // ever stopped being true.
            (route) => route.fallback(),
          );
        }
      };
      return instance;
    },
  };
}

const actionsInternals = resolve(__dirname, "node_modules/@cplieger/actions/dist/src");

// Exclude compiled output and node_modules symlink. `**/.stryker-tmp/**`
// keeps Stryker's `inPlace: true` backup copy of the suite (and a leftover
// sandbox from an interrupted run) from being collected a SECOND time: the
// backup holds every .ts file and none of the fixtures beside them, so a
// duplicate test file resolves `../css/x.css` inside the backup and fails.
// Spreading configDefaults.exclude also widens the previous top-level-only
// `node_modules/**`. Both projects need the whole list: a project's `exclude`
// REPLACES the root one rather than adding to it.
const sharedExclude = [...configDefaults.exclude, "../static/**", "**/.stryker-tmp/**"];

// Trace view records a DOM snapshot per browser interaction, and the recording is
// only readable through a reporter that serves it. VITEST_TRACE=1 turns on both
// halves together, so one variable produces something openable:
//
//   VITEST_TRACE=1 npx vitest run --project browser turn-residency.test.ts
//   then open .vitest/index.html
//
// Off by default because the snapshots cost time on every one of the ~233 browser
// test files and CI has nowhere to publish them. `singleFile` inlines the UI
// assets so the report is one file to open or attach to an issue, rather than a
// directory that needs `vite preview` to serve it. This adds no devDependency:
// the html reporter's @vitest/ui is a hard dependency of @vitest/browser, which is
// already declared.
const traceView = process.env["VITEST_TRACE"] === "1";

export default defineConfig({
  resolve: {
    alias: [
      // Allow deep imports into @cplieger/actions internals for test reset
      // utilities (_resetForTest). The package "exports" field restricts
      // access to "." only; this alias bypasses that for tests.
      {
        find: /^@cplieger\/actions\/dist\/src\/(.+)$/,
        replacement: `${actionsInternals}/$1`,
      },
    ],
  },
  server: {
    fs: {
      // The `?raw` reads that cross the package boundary: the shipped page and
      // its assets (../static), the Go sources three cross-language contract
      // tests pin (../internal), and the two scripts a CSS guard reads
      // (../scripts). Vite's dev-server file guard refuses these otherwise.
      // Narrowest set that serves them; NOT the repo root.
      allow: ["../static", "../internal", "../scripts"],
    },
  },
  test: {
    ...(traceView ? { reporters: ["default", ["html", { singleFile: true }]] as const } : {}),
    projects: [
      {
        // `extends` is a key of the PROJECT, not of its `test` block. Written
        // inside `test` it is silently ignored and every root option is lost
        // while the suite stays green, because losing a strictness option never
        // fails a test. Verified with a zero-assertion probe: inside `test` the
        // probe PASSED (requireAssertions gone); here it FAILS.
        extends: true,
        test: {
          name: "node",
          environment: "node",
          // threads pool: faster than forks for pure Node.js tests with no
          // native modules (no Prisma, bcrypt, canvas).
          pool: "threads",
          // Test isolation: each test file runs with its own module graph.
          // We previously had isolate:false for speed, but action test files
          // use vi.mock() which pollutes other files in the same worker
          // (e.g. chat-delete.test.ts mocks ../transport.js, leaking into
          // transport.test.ts). The performance delta is ~0.5s; correctness
          // wins. Browser Mode isolates per test FILE, so the browser project
          // needs neither option.
          isolate: true,
          // Package-root-relative, because the tests sit at the package root.
          include: ["**/*.node.test.ts"],
          exclude: sharedExclude,
        },
      },
      {
        extends: true,
        test: {
          name: "browser",
          include: ["**/*.test.ts"],
          exclude: [...sharedExclude, "**/*.node.test.ts"],
          // One test file at a time. Not a preference: the browser mocker
          // registers its module-interception routes on the playwright browser
          // CONTEXT, which is shared by the pages running in parallel, so two
          // pages mocking the same module race and one request gets fulfilled
          // twice — `route.fulfill: Route is already handled!`, thrown as an
          // unhandled rejection that takes the whole run down. Intermittent
          // under `vitest --run`; reproducible every time under Stryker, whose
          // four concurrent runners multiply the contention. The suite costs
          // ~57s serialized instead of ~17s.
          //
          // The anchor route in the provider above does NOT make this
          // removable: that closes the zero-crossing that loses a mock, this
          // one is duplicate matchers on the same URL (vitest-dev/vitest#10819).
          fileParallelism: false,
          browser: {
            enabled: true,
            headless: true,
            traceView,
            provider: playwrightWithAlwaysOnInterception({
              launchOptions: {
                channel: "chromium",
              },
            }),
            instances: [{ browser: "chromium" }],
            // Fixed viewport so layout-dependent assertions are reproducible; a
            // real browser computes real boxes, unlike the emulator this
            // replaced.
            viewport: { width: 1280, height: 720 },
            // A failure screenshot per failing test is noise in CI and cannot
            // be read from a job log; the assertion diff is the artifact.
            screenshotFailures: false,
          },
        },
      },
    ],

    // Fail loudly if the include pattern matches nothing.
    passWithNoTests: false,

    // Forbid .only tests unconditionally — not just in CI.
    allowOnly: false,

    // Require explicit imports of describe/it/expect from "vitest".
    globals: false,

    // Force every test to call at least one expect(). Catches tests that
    // accidentally pass because they never assert anything.
    expect: {
      requireAssertions: true,
    },

    // Auto-clean/reset/restore all mocks and stubs before each test.
    // clearMocks: clears call history only.
    // mockReset: clears history + resets implementations.
    // restoreMocks: restores vi.spyOn originals.
    // unstubEnvs/unstubGlobals: restores vi.stubEnv/vi.stubGlobal.
    clearMocks: true,
    mockReset: true,
    restoreMocks: true,
    unstubEnvs: true,
    unstubGlobals: true,

    // Fail fast on first suite error in CI; run all in watch mode.
    bail: process.env["CI"] ? 1 : 0,

    // ONE retry in CI. It does NOT cover the interception race the anchor route
    // above closes: that one links the real module for a whole FILE, so both
    // attempts fail identically in the same hook and a retry never recovered it.
    // What it still covers is the OTHER shared-context defect,
    // `route.fulfill: Route is already handled!` (vitest-dev/vitest#10819, open),
    // which `fileParallelism: false` reduces but has not eliminated — it was
    // still seen in CI with parallelism already off.
    //
    // A retry does NOT hide a product flake: vitest reports a test that only
    // passed on the retry as `flaky` in its own summary line, so the signal
    // survives while the gate stops blocking on someone else's race.
    //
    // Dropping the provider's `mocker` to fall back to vitest's server-side
    // interceptor was tried and rejected: it works, but it re-transforms the
    // module graph per registration and the 195-file suite did not finish 4
    // files in 28 minutes.
    retry: process.env["CI"] ? 1 : 0,

    // Pure functions should complete in milliseconds. Property-based
    // tests (fast-check 1000-iteration) need more headroom under
    // container load — bumped from 2s to 5s. Aligns with the 10s
    // interruptAfterTimeLimit in fc-strict-setup.ts.
    testTimeout: 5000,
    hookTimeout: 5000,

    // Flag tests slower than 100ms. Root-only: `slowTestThreshold` is a
    // NonProjectOption, so vitest rejects it inside a project.
    slowTestThreshold: 100,

    // Reproducible ordering. hooks: "stack" = afterEach/afterAll run in
    // reverse definition order (correct teardown semantics).
    sequence: {
      shuffle: { files: false, tests: false },
      concurrent: false,
      hooks: "stack",
    },

    // Loaded once per worker before any test file. Configures fast-check
    // global defaults (numRuns, verbosity, time limits). See file for
    // tuning rationale.
    setupFiles: ["./fc-strict-setup.ts", "./waitfor-budget-setup.ts"],

    // Print stack traces with every console.* call in tests.
    printConsoleTrace: true,

    // Show full diff when a snapshot fails, not just a patch.
    expandSnapshotDiff: true,

    // TypeScript type-checking is handled by tsc --noEmit (via validate-local.sh
    // and CI). Vitest's built-in typecheck is experimental and redundant here.
    // typecheck: { enabled: false } is the default; omitted for clarity.

    // V8 coverage with AST-accurate remapping, as good as Istanbul.
    coverage: {
      provider: "v8",

      // Report all TS source files, not just those imported by tests. The `**/`
      // is load-bearing on vitest 5: it matches include/exclude against the
      // root-relative path without picomatch's `contains`, so a bare `*.ts`
      // reaches only the top level and silently stops measuring every nested
      // directory (actions/, exec-view/, fundamentals/, lib/, wire/).
      //
      // `node_modules/**` is equally load-bearing and must not be dropped: this
      // list feeds tinyglobby's `ignore` for untested-file discovery, and `**/*.ts`
      // without it walks the dependency tree (measured: 4439 extra files).
      include: ["**/*.ts"],
      exclude: [
        "node_modules/**",
        "**/.stryker-tmp/**",
        "**/*.test.ts",
        "**/*.d.ts",
        // Test-only helpers, imported by tests and shipped to nobody. Same
        // classification @cplieger/actions makes with "src/test-helpers/**".
        "**/__test-helpers__/**",
        // sw.ts: service worker — runs in ServiceWorkerGlobalScope, not
        // Window, so a page-context runner cannot host PushEvent /
        // NotificationEvent / ServiceWorkerRegistration either.
        "sw.ts",
        // upload.ts: uses XMLHttpRequest.upload progress events, which need a
        // real server to upload to. Chromium can back these — a genuine
        // follow-up, not a permanent exclusion.
        "upload.ts",
        // shell.ts: the terminal itself is @cplieger/web-terminal-ui's
        // createTerminal (canvas 2d text measurement + a live WebSocket).
        // shell.test.ts covers the panel wiring with createTerminal mocked;
        // the meaningful paths live in the UI package + engine, not here.
        "shell.ts",
      ],

      // Generate coverage even when tests fail.
      reportOnFailure: true,

      // text: human-readable table. text-summary: always-visible totals.
      // lcov: machine-readable for future CI integration.
      reporter: ["text", "text-summary", "lcov"],

      // perFile: true fails on any single file below threshold, not just
      // the aggregate. Start conservative; raise as tests are written.
      thresholds: {
        lines: 60,
        functions: 60,
        branches: 50,
        statements: 60,
        perFile: true,
      },
    },

    // Show full diffs in assertion errors — default truncates at 40 chars.
    chaiConfig: {
      truncateThreshold: 0,
      showDiff: true,
      includeStack: true,
    },
  },
});
