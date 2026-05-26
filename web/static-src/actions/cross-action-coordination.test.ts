// @vitest-environment happy-dom
// Cross-action coordination tests (C7F16): validates that retry-configured
// actions sharing a scope don't conflict, onSuccess→dispatch chains
// terminate, and cancellation during retry unblocks queued actions.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog, isPending } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { ActionError, retryNetwork, retryAlways } from "./error.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

// ===========================================================================
// 1. Two different retry-configured actions in the same scope
// ===========================================================================

describe("two retry-configured actions in the same scope", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("second action waits for first action's retries to complete before starting", async () => {
    let attemptA = 0;
    let attemptB = 0;

    const actionA = defineAction<void, string>({
      name: "test.scope_retry_A",
      scope: "shared",
      retryable: retryNetwork,
      retry: { count: 2, delay: 100 },
      error: false,
      run: () => {
        attemptA++;
        if (attemptA < 3) throw new ActionError("net", { code: "network" });
        return Promise.resolve("A-done");
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.scope_retry_B",
      scope: "shared",
      retryable: retryNetwork,
      retry: { count: 1, delay: 50 },
      error: false,
      run: () => {
        attemptB++;
        return Promise.resolve("B-done");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    await Promise.resolve();

    // A is running (first attempt failed), B hasn't started
    expect(attemptA).toBe(1);
    expect(attemptB).toBe(0);

    // Advance past A's first retry (100ms)
    await vi.advanceTimersByTimeAsync(100);
    expect(attemptA).toBe(2);
    expect(attemptB).toBe(0); // B still waiting

    // Advance past A's second retry (200ms)
    await vi.advanceTimersByTimeAsync(200);
    const rA = await pA;
    expect(rA).toBe("A-done");
    expect(attemptA).toBe(3);

    // Now B starts (scope chain unblocked)
    await Promise.resolve();
    const rB = await pB;
    expect(rB).toBe("B-done");
    expect(attemptB).toBe(1);
  });

  it("second action starts after first action exhausts retries and errors", async () => {
    let attemptA = 0;
    let attemptB = 0;

    const actionA = defineAction<void, string>({
      name: "test.scope_retry_exhaust_A",
      scope: "shared",
      retryable: retryAlways,
      retry: { count: 1, delay: 50 },
      error: false,
      run: () => {
        attemptA++;
        throw new ActionError("fail", { status: 503 });
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.scope_retry_exhaust_B",
      scope: "shared",
      error: false,
      run: () => {
        attemptB++;
        return Promise.resolve("B-ok");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    await Promise.resolve();

    expect(attemptA).toBe(1);
    expect(attemptB).toBe(0);

    // Advance past A's retry
    await vi.advanceTimersByTimeAsync(50);
    const rA = await pA;
    expect(rA).toBeNull(); // A failed
    expect(attemptA).toBe(2);

    // B now runs
    await Promise.resolve();
    const rB = await pB;
    expect(rB).toBe("B-ok");
    expect(attemptB).toBe(1);
  });
});

// ===========================================================================
// 2. onSuccess → dispatch chain with same scope terminates
// ===========================================================================

describe("onSuccess → dispatch chain with same scope", () => {
  it("chained dispatch via onSuccess runs after the triggering action completes", async () => {
    const order: string[] = [];

    const actionA = defineAction<void, string>({
      name: "test.chain_A",
      scope: "chain",
      run: () => {
        order.push("A-run");
        return Promise.resolve("A-result");
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.chain_B",
      scope: "chain",
      run: () => {
        order.push("B-run");
        return Promise.resolve("B-result");
      },
    });

    let chainedPromise: Promise<string | null> | null = null;
    const pA = actionA.dispatch(undefined, {
      onSuccess: () => {
        order.push("A-onSuccess");
        chainedPromise = actionB.dispatch();
      },
    });

    const rA = await pA;
    expect(rA).toBe("A-result");
    expect(order).toContain("A-run");
    expect(order).toContain("A-onSuccess");

    // B was dispatched in onSuccess; it queues behind A in the scope chain.
    // Since A already completed, B should start immediately.
    const rB = await chainedPromise!;
    expect(rB).toBe("B-result");
    expect(order).toEqual(["A-run", "A-onSuccess", "B-run"]);
  });

  it("chain terminates — no infinite loop when onSuccess dispatches same scope", async () => {
    let dispatchCount = 0;

    const action = defineAction<{ depth: number }, string>({
      name: "test.chain_finite",
      scope: "finite",
      run: (args) => Promise.resolve(`done-${String(args.depth)}`),
    });

    // Dispatch with onSuccess that chains ONE more dispatch (depth 1 → 0)
    let chainedPromise: Promise<unknown> | null = null;
    const p = action.dispatch({ depth: 1 }, {
      onSuccess: () => {
        dispatchCount++;
        // Only chain once (depth 0 has no onSuccess)
        chainedPromise = action.dispatch({ depth: 0 });
      },
    });

    await p;
    // Await the chained dispatch directly
    await chainedPromise;

    expect(dispatchCount).toBe(1); // onSuccess fired exactly once
    const log = recentLog();
    const successes = log.filter((e) => e.status === "success");
    expect(successes.length).toBe(2); // Both dispatches succeeded
  });
});

// ===========================================================================
// 3. Cancellation during retry unblocks queued action
// ===========================================================================

describe("cancellation during retry unblocks queued action", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("cancelling a retrying action lets the queued action proceed", async () => {
    let attemptA = 0;
    let attemptB = 0;

    const actionA = defineAction<void, string>({
      name: "test.cancel_retry_A",
      scope: "cancel-scope",
      retryable: retryAlways,
      retry: { count: 5, delay: 100 },
      error: false,
      run: () => {
        attemptA++;
        throw new ActionError("fail", { status: 500 });
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.cancel_retry_B",
      scope: "cancel-scope",
      error: false,
      run: () => {
        attemptB++;
        return Promise.resolve("B-done");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    await Promise.resolve();

    // A's first attempt failed, now in backoff
    expect(attemptA).toBe(1);
    expect(attemptB).toBe(0);

    // Cancel A while it's in retry backoff
    actionA.cancel();

    // A resolves as cancelled
    const rA = await pA;
    expect(rA).toBeNull();

    // B should now start (scope chain unblocked by A's cancellation)
    await Promise.resolve();
    const rB = await pB;
    expect(rB).toBe("B-done");
    expect(attemptB).toBe(1);
  });

  it("cancelling all in-flight for one action does not cancel a different action in same scope", async () => {
    let resolveB: (() => void) | null = null;

    const actionA = defineAction<void, string>({
      name: "test.cancel_isolation_A",
      scope: "iso-scope",
      retryable: retryAlways,
      retry: { count: 2, delay: 100 },
      error: false,
      run: () => { throw new ActionError("fail", { status: 500 }); },
    });

    const actionB = defineAction<void, string>({
      name: "test.cancel_isolation_B",
      scope: "iso-scope",
      error: false,
      run: () => new Promise<string>((r) => { resolveB = () => r("B-ok"); }),
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    await Promise.resolve();

    // Cancel A
    actionA.cancel();
    await pA;

    // B starts — it has its own AbortController, not affected by A's cancel
    await Promise.resolve();
    expect(isPending("test.cancel_isolation_B")).toBe(true);

    // B completes normally
    resolveB!();
    const rB = await pB;
    expect(rB).toBe("B-ok");
  });
});

// ===========================================================================
// 4. Retry button re-dispatch respects scope serialization
// ===========================================================================

describe("retry button re-dispatch respects scope", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("manual retry via toast button queues behind active scope occupant", async () => {
    let attemptA = 0;
    let resolveOccupant: (() => void) | null = null;

    const occupant = defineAction<void, string>({
      name: "test.retry_scope_occupant",
      scope: "retry-scope",
      run: () => new Promise<string>((r) => { resolveOccupant = () => r("occ-done"); }),
    });

    const actionA = defineAction<void, string>({
      name: "test.retry_scope_A",
      scope: "retry-scope",
      retryable: retryAlways,
      error: false,
      run: () => {
        attemptA++;
        throw new ActionError("fail", { status: 500 });
      },
    });

    // A fails (no auto-retry configured beyond retryable flag)
    const pA = actionA.dispatch();
    await pA;
    expect(attemptA).toBe(1);

    // Now occupant takes the scope
    const pOcc = occupant.dispatch();
    await Promise.resolve();

    // Manual retry of A (simulates toast button click) — should queue behind occupant
    attemptA = 0;
    const pRetry = actionA.dispatch();
    await Promise.resolve();
    expect(attemptA).toBe(0); // queued, not running

    // Release occupant
    resolveOccupant!();
    await pOcc;
    await Promise.resolve();

    // Now retry runs
    await pRetry;
    expect(attemptA).toBe(1);
  });
});

// ===========================================================================
// 5. Throwing onSuccess/onError callbacks don't corrupt status or break chain
// ===========================================================================

describe("throwing callbacks don't break scope chain", () => {
  it("onSuccess throwing does not re-record action as error", async () => {
    const action = defineAction<void, string>({
      name: "test.cb_throw_success",
      scope: "cb-scope",
      run: () => Promise.resolve("ok"),
    });

    const consoleSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const result = await action.dispatch(undefined, {
      onSuccess: () => { throw new Error("callback boom"); },
    });
    consoleSpy.mockRestore();

    // Action still reports success (not corrupted to error)
    expect(result).toBe("ok");
    const log = recentLog();
    const entry = log.find((e) => e.name === "test.cb_throw_success");
    expect(entry?.status).toBe("success");
  });

  it("onSuccess throwing does not block next action in scope chain", async () => {
    let bRan = false;

    const actionA = defineAction<void, string>({
      name: "test.cb_throw_chain_A",
      scope: "cb-chain",
      run: () => Promise.resolve("A"),
    });

    const actionB = defineAction<void, string>({
      name: "test.cb_throw_chain_B",
      scope: "cb-chain",
      run: () => { bRan = true; return Promise.resolve("B"); },
    });

    const consoleSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const pA = actionA.dispatch(undefined, {
      onSuccess: () => { throw new Error("callback boom"); },
    });
    const pB = actionB.dispatch();

    await pA;
    await pB;
    consoleSpy.mockRestore();

    expect(bRan).toBe(true);
  });

  it("onError throwing does not reject dispatch promise", async () => {
    const action = defineAction<void, string>({
      name: "test.cb_throw_error",
      scope: "cb-err-scope",
      error: false,
      run: () => Promise.reject(new Error("fail")),
    });

    const consoleSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const result = await action.dispatch(undefined, {
      onError: () => { throw new Error("callback boom"); },
    });
    consoleSpy.mockRestore();

    // dispatch resolves null (not rejects)
    expect(result).toBeNull();
  });
});
