// @vitest-environment happy-dom
// Cycle 11 Stage 1 Batch 2: Cross-action chains and race conditions.
// Validates interactions where multiple distinct actions share scope,
// chain via callbacks, or race against cancellation/retry boundaries.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine, _internalsForTest } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { ActionError } from "./error.js";
import { debouncedDispatch } from "./debounce.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

// ===========================================================================
// 1. Cross-action scope chain: onSuccess of A dispatches B in same scope
// ===========================================================================

describe("cross-action scope chain via onSuccess", () => {
  it("action B queued from A's onSuccess serializes correctly in shared scope", async () => {
    const order: string[] = [];

    const actionB = defineAction<string, string>({
      name: "test.chain_B",
      scope: "chain",
      run: (args) => {
        order.push(`B-run-${args}`);
        return Promise.resolve(`B-${args}`);
      },
    });

    const actionA = defineAction<string, string>({
      name: "test.chain_A",
      scope: "chain",
      run: (args) => {
        order.push(`A-run-${args}`);
        return Promise.resolve(`A-${args}`);
      },
    });

    let chainedPromise: Promise<string | null> | null = null;
    const pA = actionA.dispatch("1", {
      onSuccess: (result) => {
        order.push(`A-onSuccess-${result}`);
        chainedPromise = actionB.dispatch("from-A");
      },
    });

    const rA = await pA;
    expect(rA).toBe("A-1");

    // The chained dispatch is scope-queued; await it directly
    const chainedResult = await chainedPromise!;

    expect(chainedResult).toBe("B-from-A");
    expect(order).toEqual(["A-run-1", "A-onSuccess-A-1", "B-run-from-A"]);
  });

  it("scope chain drains after cross-action chain completes", async () => {
    let chainedPromise: Promise<string | null> | null = null;

    const actionB = defineAction<void, string>({
      name: "test.chain_drain_B",
      scope: "drain-chain",
      run: () => Promise.resolve("B"),
    });

    const actionA = defineAction<void, string>({
      name: "test.chain_drain_A",
      scope: "drain-chain",
      run: () => Promise.resolve("A"),
    });

    await actionA.dispatch(undefined, {
      onSuccess: () => { chainedPromise = actionB.dispatch(); },
    });

    // Await the chained dispatch to fully settle
    await chainedPromise!;
    // Let .finally() cleanup run
    await Promise.resolve();
    await Promise.resolve();

    const { scopeChains } = _internalsForTest();
    expect(scopeChains).toBe(0);
  });
});

// ===========================================================================
// 2. Cancel mid-scope-chain: cancel action A while B is queued behind it
// ===========================================================================

describe("cancel mid-scope-chain across actions", () => {
  it("cancelling A unblocks queued B which runs with fresh signal", async () => {
    const order: string[] = [];

    const actionA = defineAction<void, string>({
      name: "test.cancel_chain_A",
      scope: "cancel-chain",
      error: false,
      run: (_args, signal) =>
        new Promise<string>((_resolve, reject) => {
          if (signal.aborted) {
            order.push("A-already-aborted");
            reject(new DOMException("aborted", "AbortError"));
            return;
          }
          signal.addEventListener("abort", () => {
            order.push("A-aborted");
            reject(new DOMException("aborted", "AbortError"));
          });
        }),
    });

    const actionB = defineAction<void, string>({
      name: "test.cancel_chain_B",
      scope: "cancel-chain",
      run: (_args, signal) => {
        order.push(`B-run-aborted=${String(signal.aborted)}`);
        return Promise.resolve("B-done");
      },
    });

    const pA = actionA.dispatch();
    // Let A's run() start (scope chain resolves the .then())
    await Promise.resolve();
    const pB = actionB.dispatch();

    // Cancel A — should unblock B
    actionA.cancel();

    const [rA, rB] = await Promise.all([pA, pB]);
    expect(rA).toBeNull();
    expect(rB).toBe("B-done");
    // A was aborted via the signal listener (not early-exit)
    expect(order).toContain("A-aborted");
    expect(order).toContain("B-run-aborted=false");
  });

  it("cancelling A does not cancel B even in same scope", async () => {
    const actionA = defineAction<void, string>({
      name: "test.cancel_iso_A",
      scope: "iso",
      error: false,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const actionB = defineAction<void, string>({
      name: "test.cancel_iso_B",
      scope: "iso",
      run: () => Promise.resolve("B-ok"),
    });

    const onSuccessB = vi.fn();
    const pA = actionA.dispatch();
    await Promise.resolve(); // let A start
    const pB = actionB.dispatch(undefined, { onSuccess: onSuccessB });

    actionA.cancel();
    await Promise.all([pA, pB]);

    expect(onSuccessB).toHaveBeenCalledWith("B-ok", undefined);
  });

  it("B queued behind cancelled-before-start A still runs", async () => {
    const order: string[] = [];

    const actionA = defineAction<void, string>({
      name: "test.cancel_prestart_A",
      scope: "prestart",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        order.push("A-run");
        return Promise.resolve("A");
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.cancel_prestart_B",
      scope: "prestart",
      run: () => {
        order.push("B-run");
        return Promise.resolve("B");
      },
    });

    const pA = actionA.dispatch();
    const pB = actionB.dispatch();
    // Cancel A immediately (before scope chain resolves)
    actionA.cancel();

    const [rA, rB] = await Promise.all([pA, pB]);
    expect(rA).toBeNull();
    expect(rB).toBe("B");
    expect(order).toEqual(["B-run"]);
  });
});

// ===========================================================================
// 3. Retry exhaustion in A triggers dispatch of B via onError
// ===========================================================================

describe("retry exhaustion triggers cross-action dispatch via onError", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("onError of retried A dispatches fallback B successfully", async () => {
    const order: string[] = [];

    const fallback = defineAction<string, string>({
      name: "test.fallback_B",
      run: (args) => {
        order.push(`fallback-${args}`);
        return Promise.resolve(`fallback-done-${args}`);
      },
    });

    const primary = defineAction<string, string>({
      name: "test.primary_A",
      retryable: "always",
      retry: { count: 1, delay: 30 },
      error: false,
      run: () => {
        order.push("primary-attempt");
        throw new ActionError("down", { status: 503 });
      },
    });

    let fallbackPromise: Promise<string | null> | null = null;
    const pA = primary.dispatch("req", {
      onError: (err) => {
        order.push(`onError-${err.message}`);
        fallbackPromise = fallback.dispatch("recovery");
      },
    });

    await vi.advanceTimersByTimeAsync(30); // retry fires
    await pA;

    const fallbackResult = await fallbackPromise!;

    expect(order).toEqual([
      "primary-attempt",
      "primary-attempt",
      "onError-down",
      "fallback-recovery",
    ]);
    expect(fallbackResult).toBe("fallback-done-recovery");
  });

  it("fallback B in same scope as A serializes after A completes", async () => {
    const order: string[] = [];

    const fallback = defineAction<void, string>({
      name: "test.scope_fallback_B",
      scope: "retry-fallback",
      run: () => {
        order.push("fallback-run");
        return Promise.resolve("fallback-ok");
      },
    });

    const primary = defineAction<void, string>({
      name: "test.scope_primary_A",
      scope: "retry-fallback",
      retryable: "always",
      retry: { count: 1, delay: 20 },
      error: false,
      run: () => {
        order.push("primary-run");
        throw new ActionError("fail", { status: 500 });
      },
    });

    let fallbackPromise: Promise<string | null> | null = null;
    const pA = primary.dispatch(undefined, {
      onError: () => {
        fallbackPromise = fallback.dispatch();
      },
    });

    await vi.advanceTimersByTimeAsync(20);
    await pA;

    const fallbackResult = await fallbackPromise!;

    expect(order).toEqual(["primary-run", "primary-run", "fallback-run"]);
    expect(fallbackResult).toBe("fallback-ok");
  });
});

// ===========================================================================
// 4. Debounce feeding into scoped action — rapid calls coalesce correctly
// ===========================================================================

describe("debounce + scope interaction", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("debounced dispatch coalesces then serializes in scope", async () => {
    const runs: string[] = [];

    const action = defineAction<string, string>({
      name: "test.debounce_scope",
      scope: "debounce-s",
      run: (args) => {
        runs.push(args);
        return Promise.resolve(`done-${args}`);
      },
    });

    const debounced = debouncedDispatch(action, { wait: 100 });

    debounced("a");
    debounced("b");
    debounced("c"); // only "c" should fire after 100ms

    await vi.advanceTimersByTimeAsync(100);
    await Promise.resolve();
    await Promise.resolve();

    expect(runs).toEqual(["c"]);
  });

  it("debounce cancel prevents dispatch even with scope", async () => {
    const runs: string[] = [];

    const action = defineAction<string, string>({
      name: "test.debounce_cancel_scope",
      scope: "debounce-cancel",
      run: (args) => {
        runs.push(args);
        return Promise.resolve(`done-${args}`);
      },
    });

    const debounced = debouncedDispatch(action, { wait: 50 });

    debounced("a");
    debounced("b");
    debounced.cancel(); // should prevent dispatch

    await vi.advanceTimersByTimeAsync(100);
    await Promise.resolve();

    expect(runs).toEqual([]);
    expect(debounced.isPending()).toBe(false);
  });

  it("flush fires immediately and result goes through scope", async () => {
    const runs: string[] = [];

    const action = defineAction<string, string>({
      name: "test.debounce_flush",
      scope: "flush-scope",
      run: (args) => {
        runs.push(args);
        return Promise.resolve(`done-${args}`);
      },
    });

    const debounced = debouncedDispatch(action, { wait: 200 });

    debounced("pending");
    debounced.flush(); // fires "pending" immediately

    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    await Promise.resolve();

    expect(runs).toEqual(["pending"]);
  });
});

// ===========================================================================
// 5. Concurrent dispatches: two actions race to same scope, first cancelled
// ===========================================================================

describe("concurrent scope race with cancellation", () => {
  it("three actions in same scope: cancel middle, first and third complete", async () => {
    const order: string[] = [];

    const actionA = defineAction<void, string>({
      name: "test.race_A",
      scope: "race",
      run: () => {
        order.push("A");
        return Promise.resolve("A-done");
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.race_B",
      scope: "race",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        order.push("B");
        return Promise.resolve("B-done");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.race_C",
      scope: "race",
      run: () => {
        order.push("C");
        return Promise.resolve("C-done");
      },
    });

    const pA = actionA.dispatch();
    const pB = actionB.dispatch();
    const pC = actionC.dispatch();

    // Cancel B before it gets its turn
    actionB.cancel();

    const [rA, rB, rC] = await Promise.all([pA, pB, pC]);
    expect(rA).toBe("A-done");
    expect(rB).toBeNull(); // cancelled
    expect(rC).toBe("C-done");
    expect(order).toEqual(["A", "C"]); // B skipped
  });

  it("registry records correct statuses for all three", async () => {
    const actionA = defineAction<void, string>({
      name: "test.reg_race_A",
      scope: "reg-race",
      run: () => Promise.resolve("A"),
    });

    const actionB = defineAction<void, string>({
      name: "test.reg_race_B",
      scope: "reg-race",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.reg_race_C",
      scope: "reg-race",
      run: () => Promise.resolve("C"),
    });

    const pA = actionA.dispatch();
    const pB = actionB.dispatch();
    const pC = actionC.dispatch();
    actionB.cancel();

    await Promise.all([pA, pB, pC]);

    const log = recentLog();
    const a = log.find((e) => e.name === "test.reg_race_A");
    const b = log.find((e) => e.name === "test.reg_race_B");
    const c = log.find((e) => e.name === "test.reg_race_C");

    expect(a?.status).toBe("success");
    expect(b?.status).toBe("cancelled");
    expect(c?.status).toBe("success");
  });
});

// ===========================================================================
// 6. Dedupe + cross-action scope: deduped A doesn't block B in same scope
// ===========================================================================

describe("dedupe does not block unrelated action in same scope", () => {
  it("deduped second dispatch of A does not add extra scope entry for B to wait on", async () => {
    const order: string[] = [];
    let resolveA!: (v: string) => void;

    const actionA = defineAction<string, string>({
      name: "test.dedupe_cross_A",
      dedupe: true,
      scope: "dedupe-cross",
      run: (args) => {
        order.push(`A-run-${args}`);
        return new Promise<string>((r) => { resolveA = r; });
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.dedupe_cross_B",
      scope: "dedupe-cross",
      run: () => {
        order.push("B-run");
        return Promise.resolve("B-done");
      },
    });

    const pA1 = actionA.dispatch("x");
    const pA2 = actionA.dispatch("x"); // deduped — shares pA1
    const pB = actionB.dispatch(); // queued behind A in scope

    await Promise.resolve();
    expect(order).toEqual(["A-run-x"]); // only one A run

    resolveA!("A-done");
    const [rA1, rA2, rB] = await Promise.all([pA1, pA2, pB]);

    expect(rA1).toBe("A-done");
    expect(rA2).toBe("A-done");
    expect(rB).toBe("B-done");
    // B runs after A, not after A twice
    expect(order).toEqual(["A-run-x", "B-run"]);
  });
});

// ===========================================================================
// 7. isInflight across cross-action chains
// ===========================================================================

describe("isInflight reflects cross-action state correctly", () => {
  it("action B dispatched from A's onSuccess shows inflight during its run", async () => {
    let resolveB!: (v: string) => void;

    const actionB = defineAction<void, string>({
      name: "test.inflight_chain_B",
      run: () => new Promise<string>((r) => { resolveB = r; }),
    });

    const actionA = defineAction<void, string>({
      name: "test.inflight_chain_A",
      run: () => Promise.resolve("A"),
    });

    let bInflightDuringCallback = false;
    await actionA.dispatch(undefined, {
      onSuccess: () => {
        void actionB.dispatch();
        bInflightDuringCallback = actionB.isInflight;
      },
    });

    expect(bInflightDuringCallback).toBe(true);

    resolveB!("B");
    // Need enough ticks for the promise chain to settle
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(actionB.isInflight).toBe(false);
  });

  it("cancelled action shows isInflight=false immediately after cancel settles", async () => {
    const action = defineAction<void, string>({
      name: "test.inflight_cancel_check",
      error: false,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const p = action.dispatch();
    expect(action.isInflight).toBe(true);
    action.cancel();
    await p;
    expect(action.isInflight).toBe(false);
  });
});

// ===========================================================================
// 8. Cross-action error propagation: A's error doesn't poison B's scope slot
// ===========================================================================

describe("error in A does not poison B in same scope", () => {
  it("B succeeds after A errors in shared scope", async () => {
    const actionA = defineAction<void, string>({
      name: "test.poison_A",
      scope: "poison",
      error: false,
      run: () => { throw new ActionError("A-fail", { status: 500 }); },
    });

    const actionB = defineAction<void, string>({
      name: "test.poison_B",
      scope: "poison",
      run: () => Promise.resolve("B-ok"),
    });

    const pA = actionA.dispatch();
    const pB = actionB.dispatch();

    const [rA, rB] = await Promise.all([pA, pB]);
    expect(rA).toBeNull();
    expect(rB).toBe("B-ok");
  });

  it("B's onSuccess fires even after A errored in same scope", async () => {
    const actionA = defineAction<void, string>({
      name: "test.poison_cb_A",
      scope: "poison-cb",
      error: false,
      run: () => { throw new ActionError("fail"); },
    });

    const actionB = defineAction<void, string>({
      name: "test.poison_cb_B",
      scope: "poison-cb",
      run: () => Promise.resolve("B-result"),
    });

    const onSuccessB = vi.fn();
    const pA = actionA.dispatch();
    const pB = actionB.dispatch(undefined, { onSuccess: onSuccessB });

    await Promise.all([pA, pB]);
    expect(onSuccessB).toHaveBeenCalledWith("B-result", undefined);
  });
});
