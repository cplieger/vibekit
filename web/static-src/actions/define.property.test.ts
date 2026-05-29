// @vitest-environment happy-dom
// Property-based tests for defineAction scope serialization and dedupe invariants.
import { describe, it, expect, vi, beforeEach } from "vitest";
import fc from "fast-check";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

describe("scope serialization property", () => {
  it("for any N dispatches with same scope key, runs execute sequentially and all resolve", async () => {
    await fc.assert(
      fc.asyncProperty(fc.integer({ min: 2, max: 8 }), async (n) => {
        resetDefine();
        resetRegistry();
        resetCleanup();

        const timeline: { idx: number; event: "start" | "end" }[] = [];
        let counter = 0;

        const action = defineAction<number, number>({
          name: "prop.scope_serial",
          scope: "serial",
          error: false,
          run: async (args) => {
            const idx = args;
            timeline.push({ idx, event: "start" });
            // Simulate async work with variable delay
            await new Promise((r) => setTimeout(r, 1));
            timeline.push({ idx, event: "end" });
            return counter++;
          },
        });

        const promises = Array.from({ length: n }, (_, i) => action.dispatch(i));
        const results = await Promise.all(promises);

        // All N dispatches resolved (none null = not cancelled)
        expect(results).toHaveLength(n);
        for (const r of results) {
          expect(r).not.toBeNull();
        }

        // Sequential invariant: no start[i+1] before end[i]
        for (let i = 0; i < timeline.length - 1; i++) {
          if (timeline[i].event === "start" && timeline[i + 1]?.event === "start") {
            // Two consecutive starts means overlap — violation
            expect(true).toBe(false);
          }
        }

        // Verify strict ordering: start/end pairs alternate
        for (let i = 0; i < timeline.length; i += 2) {
          expect(timeline[i].event).toBe("start");
          expect(timeline[i + 1]?.event).toBe("end");
        }
      }),
      { numRuns: 20 },
    );
  });
});

describe("dedupe property", () => {
  it("for any N dispatches with dedupe:true and same args, at most one run is in-flight", async () => {
    await fc.assert(
      fc.asyncProperty(fc.integer({ min: 2, max: 6 }), async (n) => {
        resetDefine();
        resetRegistry();
        resetCleanup();

        let inFlight = 0;
        let maxInFlight = 0;
        let runCount = 0;

        const action = defineAction<string, string>({
          name: "prop.dedupe",
          dedupe: true,
          error: false,
          run: async () => {
            inFlight++;
            runCount++;
            maxInFlight = Math.max(maxInFlight, inFlight);
            await new Promise((r) => setTimeout(r, 5));
            inFlight--;
            return "ok";
          },
        });

        // All dispatches use same args to trigger dedupe
        const promises = Array.from({ length: n }, () => action.dispatch("same"));
        const results = await Promise.all(promises);

        // At most one run in-flight at any time
        expect(maxInFlight).toBe(1);
        // All callers receive the same result
        for (const r of results) {
          expect(r).toBe("ok");
        }
        // Only one actual run executed (dedupe collapsed the rest)
        expect(runCount).toBe(1);
      }),
      { numRuns: 20 },
    );
  });
});

describe("cancellation property", () => {
  it("cancel during any lifecycle phase transitions to cancelled and returns null", async () => {
    await fc.assert(
      fc.asyncProperty(
        fc.constantFrom("immediate", "during-run", "scope-queued"),
        async (phase) => {
          resetDefine();
          resetRegistry();
          resetCleanup();

          let runStarted = false;
          let _resolveRun!: (v: string) => void;

          const action = defineAction<string, string>({
            name: "prop.cancel",
            scope: phase === "scope-queued" ? "cancel-scope" : undefined,
            error: false,
            run: async (_args, signal) => {
              runStarted = true;
              return new Promise<string>((resolve, reject) => {
                _resolveRun = resolve;
                signal.addEventListener(
                  "abort",
                  () => {
                    reject(new DOMException("aborted", "AbortError"));
                  },
                  { once: true },
                );
              });
            },
          });

          if (phase === "immediate") {
            const p = action.dispatch("x");
            // Cancel before run starts executing (next microtask)
            action.cancel();
            const result = await p;
            expect(result).toBeNull();
          } else if (phase === "during-run") {
            const p = action.dispatch("x");
            // Wait for run to start
            await new Promise((r) => setTimeout(r, 1));
            expect(runStarted).toBe(true);
            action.cancel();
            const result = await p;
            expect(result).toBeNull();
          } else {
            // scope-queued: first dispatch holds the scope, second is queued
            const p1 = action.dispatch("first");
            await new Promise((r) => setTimeout(r, 1));
            const p2 = action.dispatch("second");
            // Cancel all — both the running and queued should resolve null
            action.cancel();
            const [r1, r2] = await Promise.all([p1, p2]);
            expect(r1).toBeNull();
            expect(r2).toBeNull();
          }
        },
      ),
      { numRuns: 15 },
    );
  });
});
