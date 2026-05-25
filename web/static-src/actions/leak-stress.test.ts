// Stress test: verifies scopeChains, dedupeInflight, and inFlight maps
// stay bounded after many dispatches. Catches memory leaks where cleanup
// paths fail to delete map entries.

import { describe, it, expect, beforeEach } from "vitest";
import { defineAction, _resetForTest as resetDefine, _internalsForTest } from "./define.js";
import { _resetForTest as resetRegistry, pendingFor } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

beforeEach(() => {
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

    const pre = _internalsForTest().scopeChains;
    const promises: Promise<unknown>[] = [];
    for (let i = 0; i < 1000; i++) {
      promises.push(action.dispatch({ i }));
    }
    await Promise.all(promises);

    const post = _internalsForTest().scopeChains;
    expect(post).toBe(pre);
    expect(post).toBe(0);
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
    // Verify dedupe actually worked (far fewer than 1000 calls)
    expect(callCount).toBeLessThan(1000);
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

  it("1000 cancelled dispatches leave no pending entries", async () => {
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
