// Stress test: verifies scopeChains, dedupeInflight, and inFlight maps
// stay bounded after many dispatches. Catches memory leaks where cleanup
// paths fail to delete map entries.

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { defineAction, _resetForTest as resetDefine, _internalsForTest } from "./define.js";
import { _resetForTest as resetRegistry, pendingFor, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
});

afterEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
});

describe("memory leak stress — scopeChains", () => {
  it("1000 scoped dispatches leave scopeChains empty", async () => {
    const action = defineAction({
      name: "stress.scope",
      scope: "s",
      run: async () => "ok",
    });

    const promises: Promise<unknown>[] = [];
    for (let i = 0; i < 1000; i++) {
      promises.push(action.dispatch({ i }));
    }
    await Promise.all(promises);

    expect(_internalsForTest().scopeChains).toBe(0);
  });

  it("1000 dynamic-scope dispatches leave scopeChains empty", async () => {
    const action = defineAction({
      name: "stress.dynscope",
      scope: (args: { key: string }) => args.key,
      run: async () => "ok",
    });

    const promises: Promise<unknown>[] = [];
    for (let i = 0; i < 1000; i++) {
      promises.push(action.dispatch({ key: `k${i % 10}` }));
    }
    await Promise.all(promises);

    expect(_internalsForTest().scopeChains).toBe(0);
  });
});

describe("memory leak stress — dedupeInflight", () => {
  it("1000 deduped dispatches leave dedupeInflight empty", async () => {
    let callCount = 0;
    const action = defineAction({
      name: "stress.dedupe",
      dedupe: true,
      run: async (args: { v: number }) => { callCount++; return args.v; },
    });

    // All dispatches with same args should dedupe to one in-flight
    const promises: Promise<unknown>[] = [];
    for (let i = 0; i < 1000; i++) {
      promises.push(action.dispatch({ v: i % 5 }));
    }
    await Promise.all(promises);

    expect(_internalsForTest().dedupeInflight).toBe(0);
    // Verify dedupe actually worked (exactly 5 unique arg keys)
    expect(callCount).toBe(5);
  });
});

describe("memory leak stress — inFlight (via pendingFor)", () => {
  it("1000 parallel dispatches leave no pending entries", async () => {
    const action = defineAction({
      name: "stress.parallel",
      run: async () => "ok",
    });

    const promises: Promise<unknown>[] = [];
    for (let i = 0; i < 1000; i++) {
      promises.push(action.dispatch({ i }));
    }
    await Promise.all(promises);

    // No pending instances remain in the registry
    expect(pendingFor("stress.parallel")).toHaveLength(0);
  });

  it("1000 dispatches with errors leave no pending entries", async () => {
    const action = defineAction({
      name: "stress.errors",
      error: false, // suppress toast
      run: async () => { throw new Error("fail"); },
    });

    const promises: Promise<unknown>[] = [];
    for (let i = 0; i < 1000; i++) {
      promises.push(action.dispatch({ i }));
    }
    await Promise.all(promises);

    expect(pendingFor("stress.errors")).toHaveLength(0);
  });

  it("100 cancelled dispatches leave no pending entries", async () => {
    const action = defineAction({
      name: "stress.cancel",
      scope: "cancel-scope",
      run: async (_args, signal) => {
        await new Promise((r) => setTimeout(r, 1));
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "ok";
      },
    });

    const promises: Promise<unknown>[] = [];
    for (let i = 0; i < 100; i++) {
      promises.push(action.dispatch({ i }));
    }
    // Cancel mid-flight
    action.cancel();
    await Promise.all(promises);

    expect(pendingFor("stress.cancel")).toHaveLength(0);
    expect(_internalsForTest().scopeChains).toBe(0);
  });
});

describe("memory leak stress — combined scope + dedupe", () => {
  it("1000 scoped+deduped dispatches leave maps empty", async () => {
    const action = defineAction({
      name: "stress.both",
      scope: "shared",
      dedupe: true,
      run: async (args: { v: number }) => args.v,
    });

    const promises: Promise<unknown>[] = [];
    for (let i = 0; i < 1000; i++) {
      promises.push(action.dispatch({ v: i % 3 }));
    }
    await Promise.all(promises);

    const internals = _internalsForTest();
    expect(internals.scopeChains).toBe(0);
    expect(internals.dedupeInflight).toBe(0);
    expect(pendingFor("stress.both")).toHaveLength(0);
  });
});

describe("memory leak stress — registry log eviction", () => {
  it("1000 dispatches with full lifecycle: log stays bounded at MAX_LOG_SIZE", async () => {
    // Use sequential dispatches so each one completes before the next
    // starts — this exercises the eviction path (new record() sees
    // _liveCount > MAX_LOG_SIZE and evicts settled entries).
    const action = defineAction({
      name: "stress.registry",
      run: async (args: { i: number }) => args.i,
    });

    for (let i = 0; i < 1000; i++) {
      await action.dispatch({ i });
    }

    // MAX_LOG_SIZE is 200; after 1000 sequential dispatches the log
    // must have evicted old entries and stay bounded.
    const log = recentLog();
    expect(log.length).toBeLessThanOrEqual(200);
    expect(log.every((e) => e.status !== "pending")).toBe(true);
  });

  it("tombstones are compacted (log array length stays reasonable)", async () => {
    // Sequential dispatches trigger eviction + compact() when head > 256.
    const action = defineAction({
      name: "stress.compact",
      run: async (args: { i: number }) => args.i,
    });

    for (let i = 0; i < 500; i++) {
      await action.dispatch({ i });
    }

    const log = recentLog();
    // After eviction + compaction, live entries stay bounded
    expect(log.length).toBeLessThanOrEqual(200);
    expect(log.length).toBeGreaterThan(0);
  });

  it("memory growth bounded: sequential batches don't accumulate", async () => {
    const action = defineAction({
      name: "stress.growth",
      run: async (args: { i: number }) => args.i,
    });

    // Run 5 batches of 200 dispatches each (1000 total, sequential)
    for (let batch = 0; batch < 5; batch++) {
      for (let i = 0; i < 200; i++) {
        await action.dispatch({ i: batch * 200 + i });
      }
    }

    const log = recentLog();
    expect(log.length).toBeLessThanOrEqual(200);
    expect(pendingFor("stress.growth")).toHaveLength(0);
  });
});