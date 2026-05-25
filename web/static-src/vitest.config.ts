// Vitest 4.1 configuration for vibekit TypeScript unit tests.
// Default environment: node (pure functions, no DOM overhead).
// DOM modules: add `// @vitest-environment happy-dom` at the top of the
// test file to get window/document/localStorage/etc. No browser binary
// needed — happy-dom is a pure JS DOM implementation running in Node.
// Run: vitest --run (single pass) or vitest (watch mode)
import { defineConfig } from "vitest/config";

export default defineConfig({
  resolve: {
    alias: {
      // ansi_up is loaded via importmap in the browser; for vitest we
      // point at a stub that exports a no-op AnsiUp (tests don't need
      // real ANSI rendering, just DOM structure).
      "ansi_up": "./test-stubs/ansi_up.ts",
    },
  },
  test: {
    // Default: node. Override per test file with:
    //   // @vitest-environment happy-dom
    environment: "node",

    // threads pool: faster than forks for pure Node.js tests with no native
    // modules (no Prisma, bcrypt, canvas).
    pool: "threads",

    // Test isolation: each test file runs with its own module graph.
    // We previously had isolate:false for speed, but action test files
    // use vi.mock() which pollutes other files in the same worker
    // (e.g. chat-delete.test.ts mocks ../transport.js, leaking into
    // transport.test.ts). The performance delta is ~0.5s; correctness wins.
    isolate: true,

    // Test files co-located with source, named *.test.ts
    include: ["**/*.test.ts"],

    // Exclude compiled output and node_modules symlink
    exclude: ["../static/**", "node_modules/**"],

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

    // Pure functions should complete in milliseconds.
    testTimeout: 2000,
    hookTimeout: 5000,

    // Flag tests slower than 100ms — pure functions have no I/O.
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
    setupFiles: ["./fc-strict-setup.ts"],

    // Print stack traces with every console.* call in tests.
    printConsoleTrace: true,

    // Show full diff when a snapshot fails, not just a patch.
    expandSnapshotDiff: true,

    // TypeScript type-checking is handled by tsgo --noEmit (via validate-local.sh
    // and CI). Vitest's built-in typecheck is experimental and redundant here.
    // typecheck: { enabled: false } is the default; omitted for clarity.

    // V8 coverage with AST-accurate remapping (Vitest 4, as good as Istanbul).
    coverage: {
      provider: "v8",

      // Report all TS source files, not just those imported by tests.
      include: ["*.ts", "handlers/*.ts"],
      exclude: [
        "*.test.ts",
        "*.d.ts",
        // xterm-vendor: type declarations only, no runtime code
        "xterm-vendor.d.ts",
        // sw.ts: service worker — runs in ServiceWorkerGlobalScope,
        // not Window. Neither happy-dom nor jsdom implements
        // PushEvent/NotificationEvent/ServiceWorkerRegistration.
        "sw.ts",
        // upload.ts: uses XMLHttpRequest.upload progress events.
        // happy-dom implements XHR but does not fire upload progress
        // events (no simulated network I/O). The XHR lifecycle is
        // untestable; the pure path-construction logic is minimal.
        "upload.ts",
        // shell.ts: dynamically imports xterm.js from /vendor/*.mjs
        // at runtime. Those files don't exist in the test environment,
        // and xterm.js requires a real canvas/WebGL context.
        "shell.ts",
        // agent-terminal.ts: fully testable with happy-dom (no xterm,
        // no canvas — uses plain <pre> + DOM APIs). NOT excluded.
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

    // Persistent file system module cache between reruns.
    // Cache path redirected out of node_modules (which is a read-only symlink
    // to the shared WSL install at /usr/local/lib/vitest-deps/).
    experimental: {
      fsModuleCache: true,
      fsModuleCachePath: ".vitest-cache",
    },
  },
});
