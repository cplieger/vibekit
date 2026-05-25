// @vitest-environment happy-dom
// Cycle 16 Stage 1 Batch 2: Cross-action race conditions.
// Validates race scenarios between scope serialization, retry, dedupe,
// and early-cancel when multiple actions interact in the same scope.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine, _internalsForTest } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { ActionError } from "./error.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

// ===========================================================================
// 1. Cancel during retry backoff in scope chain unblocks follower
// ===========================================================================

describe("cancel during retry backoff in scope chain", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("follower starts after retrying action is cancelled mid-backoff", async () => {
    const order: string[] = [];
    let attempt = 0;

    const retrier = defineAction<void, string>({
      name: "test.retry_scope_cancel_A",
      scope: "retry-scope-cancel",
      retryable: "always",
      retry: { count: 3, delay: 100 },
      error: false,
      run: () => {
        attempt++;
        order.push(`A-attempt-${attempt}`);
        throw new ActionError("fail", { status: 500 });
      },
    });

    const follower = defineAction<void, string>({
      name: "test.retry_scope_cancel_B",
      scope: "retry-scope-cancel",
      run: () => {
        order.push("B-run");
        return Promise.resolve("B");
      },
    });

    const pA = retrier.dispatch();
    await Promise.resolve(); // first attempt runs
    expect(attempt).toBe(1);

    const pB = follower.dispatch();

    // Advance partway into backoff
    await vi.advanceTimersByTimeAsync(50);
    expect(attempt).toBe(1); // still in backoff

    // Cancel A mid-backoff
    retrier.cancel();
    const rA = await pA;
    expect(rA).toBeNull();

    // B should now run (scope unblocked)
    await Promise.resolve();
    await Promise.resolve();
    const rB = await pB;
    expect(rB).toBe("B");
    expect(order).toEqual(["A-attempt-1", "B-run"]);
  });

  it("follower starts after retrying action exhausts retries", async () => {
    const order: string[] = [];

    const retrier = defineAction<void, string>({
      name: "test.retry_exhaust_scope_A",
      scope: "retry-exhaust",
      retryable: "always",
      retry: { count: 1, delay: 50 },
      error: false,
      run: () => {
        order.push("A-attempt");
        throw new ActionError("fail", { status: 500 });
      },
    });

    const follower = defineAction<void, string>({
      name: "test.retry_exhaust_scope_B",
      scope: "retry-exhaust",
      run: () => {
        order.push("B-run");
        return Promise.resolve("B");
      },
    });

    const pA = retrier.dispatch();
    await Promise.resolve();
    const pB = follower.dispatch();

    // Advance through retry
    await vi.advanceTimersByTimeAsync(50);
    await pA;
    await Promise.resolve();
    const rB = await pB;

    expect(rB).toBe("B");
    expect(order).toEqual(["A-attempt", "A-attempt", "B-run"]);
  });
});

// ===========================================================================
// 2. Cross-action dedupe + scope + early-cancel + re-dispatch
// ===========================================================================

describe("cross-action dedupe + scope + early-cancel + re-dispatch", () => {
  it("re-dispatch with same dedupe key after early-cancel queues correctly", async () => {
    let resolveA!: (v: string) => void;
    let runCount = 0;

    const actionA = defineAction<void, string>({
      name: "test.dedupe_early_redispatch_A",
      scope: "der",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<string, string>({
      name: "test.dedupe_early_redispatch_B",
      scope: "der",
      dedupe: true,
      error: false,
      run: (_args, signal) => {
        runCount++;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve(`B-${runCount}`);
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();

    // Dispatch B, then early-cancel it
    const pB1 = actionB.dispatch("k");
    actionB.cancel();
    const rB1 = await pB1;
    expect(rB1).toBeNull();

    // Re-dispatch B with same dedupe key — should queue behind A
    const pB2 = actionB.dispatch("k");

    resolveA!("A");
    const [rA, rB2] = await Promise.all([pA, pB2]);

    expect(rA).toBe("A");
    expect(rB2).toBe("B-1");
    expect(runCount).toBe(1);

    // Dedupe map should be clean
    await Promise.resolve();
    await Promise.resolve();
    const { activeDedupes } = _internalsForTest();
    expect(activeDedupes).toBe(0);
  });

  it("deduped dispatch collapses onto scope-queued original", async () => {
    let resolveA!: (v: string) => void;
    let runCount = 0;

    const actionA = defineAction<void, string>({
      name: "test.dedupe_collapse_scope_A",
      scope: "dcs",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<string, string>({
      name: "test.dedupe_collapse_scope_B",
      scope: "dcs",
      dedupe: true,
      run: () => {
        runCount++;
        return Promise.resolve(`B-${runCount}`);
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();

    // First B dispatch queues in scope
    const pB1 = actionB.dispatch("k");
    // Second B dispatch with same key should collapse onto first
    const pB2 = actionB.dispatch("k");

    resolveA!("A");
    const [rA, rB1, rB2] = await Promise.all([pA, pB1, pB2]);

    expect(rA).toBe("A");
    expect(rB1).toBe("B-1");
    expect(rB2).toBe("B-1");
    expect(runCount).toBe(1);
  });
});

// ===========================================================================
// 3. Mixed cancel timing: some before start, some during run
// ===========================================================================

describe("mixed cancel timing in scope chain", () => {
  it("cancel running + cancel queued: both resolve, follower runs", async () => {
    const order: string[] = [];

    const actionA = defineAction<void, string>({
      name: "test.mixed_cancel_A",
      scope: "mixed-cancel",
      error: false,
      run: (_args, signal) => {
        order.push("A-start");
        return new Promise<string>((_resolve, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        });
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.mixed_cancel_B",
      scope: "mixed-cancel",
      error: false,
      run: (_args, signal) => {
        order.push("B-start");
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.mixed_cancel_C",
      scope: "mixed-cancel",
      run: () => {
        order.push("C-start");
        return Promise.resolve("C");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    const pC = actionC.dispatch();

    // Cancel B (queued) — resolves immediately
    actionB.cancel();
    const rB = await pB;
    expect(rB).toBeNull();

    // Cancel A (running) — resolves after abort
    actionA.cancel();
    const rA = await pA;
    expect(rA).toBeNull();

    // C should run after A's slot clears
    const rC = await pC;
    expect(rC).toBe("C");
    expect(order).toEqual(["A-start", "C-start"]);
  });

  it("cancel running action unblocks all queued followers", async () => {
    const order: string[] = [];

    const actionA = defineAction<void, string>({
      name: "test.cancel_run_unblock_A",
      scope: "cancel-run-unblock",
      error: false,
      run: (_args, signal) => {
        order.push("A-start");
        return new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        });
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.cancel_run_unblock_B",
      scope: "cancel-run-unblock",
      run: () => { order.push("B"); return Promise.resolve("B"); },
    });

    const actionC = defineAction<void, string>({
      name: "test.cancel_run_unblock_C",
      scope: "cancel-run-unblock",
      run: () => { order.push("C"); return Promise.resolve("C"); },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    const pC = actionC.dispatch();

    actionA.cancel();
    const [rA, rB, rC] = await Promise.all([pA, pB, pC]);

    expect(rA).toBeNull();
    expect(rB).toBe("B");
    expect(rC).toBe("C");
    expect(order).toEqual(["A-start", "B", "C"]);
  });
});

// ===========================================================================
// 4. onRetryExhausted fires before next scope entry starts
// ===========================================================================

describe("onRetryExhausted + scope ordering", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("onRetryExhausted fires before follower's run()", async () => {
    const order: string[] = [];

    const retrier = defineAction<void, string>({
      name: "test.exhaust_order_A",
      scope: "exhaust-order",
      retryable: "always",
      retry: { count: 1, delay: 30 },
      error: false,
      run: () => { throw new ActionError("fail", { status: 500 }); },
    });

    const follower = defineAction<void, string>({
      name: "test.exhaust_order_B",
      scope: "exhaust-order",
      run: () => {
        order.push("B-run");
        return Promise.resolve("B");
      },
    });

    const pA = retrier.dispatch(undefined, {
      onRetryExhausted: () => { order.push("A-exhausted"); },
      onSettled: () => { order.push("A-settled"); },
    });
    await Promise.resolve();
    const pB = follower.dispatch();

    await vi.advanceTimersByTimeAsync(30);
    await pA;
    await Promise.resolve();
    await pB;

    expect(order.indexOf("A-exhausted")).toBeLessThan(order.indexOf("B-run"));
    expect(order.indexOf("A-settled")).toBeLessThan(order.indexOf("B-run"));
  });

  it("onRetryAttempt fires for each retry in scope without blocking follower", async () => {
    const attempts: number[] = [];

    const retrier = defineAction<void, string>({
      name: "test.attempt_scope_A",
      scope: "attempt-scope",
      retryable: "always",
      retry: { count: 2, delay: 20 },
      error: false,
      run: () => { throw new ActionError("fail", { status: 500 }); },
    });

    const follower = defineAction<void, string>({
      name: "test.attempt_scope_B",
      scope: "attempt-scope",
      run: () => Promise.resolve("B"),
    });

    const pA = retrier.dispatch(undefined, {
      onRetryAttempt: (info) => { attempts.push(info.attempt); },
    });
    await Promise.resolve();
    const pB = follower.dispatch();

    await vi.advanceTimersByTimeAsync(20);
    await vi.advanceTimersByTimeAsync(40);
    await pA;
    await Promise.resolve();
    const rB = await pB;

    expect(rB).toBe("B");
    expect(attempts).toEqual([2, 3]);
  });
});

// ===========================================================================
// 5. Cross-action interleave with retry: ordering preserved
// ===========================================================================

describe("cross-action interleave with retry", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("retrying action A does not starve action B in same scope", async () => {
    let attemptA = 0;
    const order: string[] = [];

    const actionA = defineAction<void, string>({
      name: "test.interleave_retry_A",
      scope: "interleave-retry",
      retryable: "network",
      retry: { count: 1, delay: 30 },
      error: false,
      run: () => {
        attemptA++;
        order.push(`A-${attemptA}`);
        if (attemptA < 2) throw new ActionError("net", { code: "network" });
        return Promise.resolve("A-ok");
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.interleave_retry_B",
      scope: "interleave-retry",
      run: () => {
        order.push("B");
        return Promise.resolve("B-ok");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();

    // A retries after 30ms
    await vi.advanceTimersByTimeAsync(30);
    const rA = await pA;
    await Promise.resolve();
    const rB = await pB;

    expect(rA).toBe("A-ok");
    expect(rB).toBe("B-ok");
    // A runs twice (fail + retry success), then B runs
    expect(order).toEqual(["A-1", "A-2", "B"]);
  });

  it("cancel retrying A mid-backoff lets B proceed", async () => {
    const order: string[] = [];

    const actionA = defineAction<void, string>({
      name: "test.cancel_retry_interleave_A",
      scope: "cancel-retry-il",
      retryable: "always",
      retry: { count: 3, delay: 100 },
      error: false,
      run: () => {
        order.push("A-attempt");
        throw new ActionError("fail", { status: 500 });
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.cancel_retry_interleave_B",
      scope: "cancel-retry-il",
      run: () => {
        order.push("B-run");
        return Promise.resolve("B");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();

    // Cancel A during first backoff
    await vi.advanceTimersByTimeAsync(50);
    actionA.cancel();
    const rA = await pA;
    expect(rA).toBeNull();

    await Promise.resolve();
    await Promise.resolve();
    const rB = await pB;
    expect(rB).toBe("B");
    expect(order).toEqual(["A-attempt", "B-run"]);
  });
});

// ===========================================================================
// 6. Scope chain integrity after rapid dispatch-cancel-dispatch cycles
// ===========================================================================

describe("rapid dispatch-cancel-dispatch cycles in scope", () => {
  it("5 rapid cancel-redispatch cycles leave scope chain clean", async () => {
    const action = defineAction<number, string>({
      name: "test.rapid_cycle",
      scope: "rapid-cycle",
      error: false,
      run: (n, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve(`done-${n}`);
      },
    });

    for (let i = 0; i < 5; i++) {
      const p = action.dispatch(i);
      action.cancel();
      await p;
    }

    // Final dispatch should work normally
    const result = await action.dispatch(99);
    expect(result).toBe("done-99");

    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    const { scopeChains } = _internalsForTest();
    expect(scopeChains).toBe(0);
  });

  it("interleaved cancel-dispatch across two actions in same scope", async () => {
    const order: string[] = [];

    const actionA = defineAction<number, string>({
      name: "test.interleave_cycle_A",
      scope: "il-cycle",
      error: false,
      run: (n, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        order.push(`A-${n}`);
        return Promise.resolve(`A-${n}`);
      },
    });

    const actionB = defineAction<number, string>({
      name: "test.interleave_cycle_B",
      scope: "il-cycle",
      error: false,
      run: (n, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        order.push(`B-${n}`);
        return Promise.resolve(`B-${n}`);
      },
    });

    // Dispatch A1, cancel, dispatch B1, dispatch A2
    const pA1 = actionA.dispatch(1);
    actionA.cancel();
    await pA1;

    const pB1 = actionB.dispatch(1);
    const pA2 = actionA.dispatch(2);

    const [rB1, rA2] = await Promise.all([pB1, pA2]);
    expect(rB1).toBe("B-1");
    expect(rA2).toBe("A-2");
    expect(order).toEqual(["B-1", "A-2"]);
  });
});

// ===========================================================================
// 7. isInflight reflects correct state during cross-action races
// ===========================================================================

describe("isInflight during cross-action races", () => {
  it("isInflight is false immediately after early-cancel", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.inflight_A",
      scope: "inflight",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.inflight_B",
      scope: "inflight",
      error: false,
      run: () => Promise.resolve("B"),
    });

    actionA.dispatch();
    await Promise.resolve();
    actionB.dispatch();

    expect(actionB.isInflight).toBe(true);
    actionB.cancel();
    expect(actionB.isInflight).toBe(false);

    resolveA!("A");
  });

  it("isInflight is true while retrying in scope", async () => {
    vi.useFakeTimers();

    const action = defineAction<void, string>({
      name: "test.inflight_retry",
      scope: "inflight-retry",
      retryable: "always",
      retry: { count: 2, delay: 50 },
      error: false,
      run: () => { throw new ActionError("fail", { status: 500 }); },
    });

    const p = action.dispatch();
    await Promise.resolve();
    expect(action.isInflight).toBe(true);

    await vi.advanceTimersByTimeAsync(50);
    expect(action.isInflight).toBe(true);

    await vi.advanceTimersByTimeAsync(100);
    await p;
    expect(action.isInflight).toBe(false);

    vi.useRealTimers();
  });
});
