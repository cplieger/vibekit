// @vitest-environment happy-dom
// Cycle 9 Stage 1 Batch 2: Cross-action chains, race conditions, retry
// interactions. Validates edge cases where scope serialization, dedupe,
// retry, and per-dispatch callbacks interact in non-trivial ways.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine, _internalsForTest } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { ActionError, retryNetwork } from "./error.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

// ===========================================================================
// 1. Dedupe + retry: deduped caller receives correct error after retries exhaust
// ===========================================================================

describe("dedupe + retry interaction", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("deduped caller's onError fires with the real error after retries exhaust", async () => {
    let attempt = 0;
    const action = defineAction<string, string>({
      name: "test.dedupe_retry",
      dedupe: true,
      retryable: (err) => err.code !== "cancelled",
      retry: { count: 2, delay: 50 },
      error: false,
      run: () => {
        attempt++;
        throw new ActionError("server down", { status: 503 });
      },
    });

    const onError1 = vi.fn();
    const onError2 = vi.fn();
    const onSettled1 = vi.fn();
    const onSettled2 = vi.fn();

    const p1 = action.dispatch("x", { onError: onError1, onSettled: onSettled1 });
    const p2 = action.dispatch("x", { onError: onError2, onSettled: onSettled2 });

    // Advance through retries: 50ms + 100ms
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(100);

    const [r1, r2] = await Promise.all([p1, p2]);

    expect(r1).toBeNull();
    expect(r2).toBeNull();
    expect(attempt).toBe(3); // 1 original + 2 retries, NOT doubled

    // Both callers get the real error
    expect(onError1).toHaveBeenCalledWith(
      expect.objectContaining({ message: "server down", status: 503 }),
      "x",
    );
    expect(onError2).toHaveBeenCalledWith(
      expect.objectContaining({ message: "server down", status: 503 }),
      "x",
    );
    expect(onSettled1).toHaveBeenCalledTimes(1);
    expect(onSettled2).toHaveBeenCalledTimes(1);
  });

  it("deduped caller's onSuccess fires when retries eventually succeed", async () => {
    let attempt = 0;
    const action = defineAction<string, string>({
      name: "test.dedupe_retry_ok",
      dedupe: true,
      retryable: retryNetwork,
      retry: { count: 2, delay: 30 },
      error: false,
      run: () => {
        attempt++;
        if (attempt < 3) {throw new ActionError("timeout", { code: "network" });}
        return Promise.resolve("recovered");
      },
    });

    const onSuccess1 = vi.fn();
    const onSuccess2 = vi.fn();

    const p1 = action.dispatch("y", { onSuccess: onSuccess1 });
    const p2 = action.dispatch("y", { onSuccess: onSuccess2 });

    await vi.advanceTimersByTimeAsync(30);
    await vi.advanceTimersByTimeAsync(60);

    const [r1, r2] = await Promise.all([p1, p2]);

    expect(r1).toBe("recovered");
    expect(r2).toBe("recovered");
    expect(attempt).toBe(3);
    expect(onSuccess1).toHaveBeenCalledWith("recovered", "y");
    expect(onSuccess2).toHaveBeenCalledWith("recovered", "y");
  });

  it("dedupe map is cleaned up after retry exhaustion", async () => {
    const action = defineAction<string, string>({
      name: "test.dedupe_cleanup",
      dedupe: true,
      retryable: (err) => err.code !== "cancelled",
      retry: { count: 1, delay: 20 },
      error: false,
      run: () => {
        throw new ActionError("fail", { status: 500 });
      },
    });

    const p = action.dispatch("z");
    await vi.advanceTimersByTimeAsync(20);
    await p;

    // Dedupe map should be empty after settlement
    const { activeDedupes } = _internalsForTest();
    expect(activeDedupes).toBe(0);
  });
});

// ===========================================================================
// 2. onError → dispatch chain in same scope serializes correctly
// ===========================================================================

describe("onError → dispatch chain in same scope", () => {
  it("recovery action dispatched from onError queues behind and runs after error", async () => {
    const order: string[] = [];

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const failAction = defineAction<void, string>({
      name: "test.err_chain_fail",
      scope: "err-chain",
      error: false,
      run: () => {
        order.push("fail-run");
        throw new ActionError("oops");
      },
    });

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const recoverAction = defineAction<void, string>({
      name: "test.err_chain_recover",
      scope: "err-chain",
      run: () => {
        order.push("recover-run");
        return Promise.resolve("recovered");
      },
    });

    let recoveryPromise: Promise<string | null> | null = null;
    const pFail = failAction.dispatch(undefined, {
      onError: () => {
        order.push("fail-onError");
        recoveryPromise = recoverAction.dispatch();
      },
    });

    const rFail = await pFail;
    expect(rFail).toBeNull();
    expect(order).toContain("fail-run");
    expect(order).toContain("fail-onError");

    // Recovery runs after the failed action completes
    const rRecover = await recoveryPromise!;
    expect(rRecover).toBe("recovered");
    expect(order).toEqual(["fail-run", "fail-onError", "recover-run"]);
  });

  it("multiple error-triggered dispatches serialize in order", async () => {
    const order: string[] = [];
    let callCount = 0;
    let chainP1: Promise<unknown> | null = null;
    let chainP2: Promise<unknown> | null = null;

    const flaky = defineAction<number, string>({
      name: "test.err_multi",
      scope: "err-multi",
      error: false,
      run: (n) => {
        callCount++;
        if (n === 0) {
          order.push("flaky-fail");
          throw new ActionError("fail");
        }
        order.push(`flaky-ok-${String(n)}`);
        return Promise.resolve(`ok-${String(n)}`);
      },
    });

    // First dispatch fails, triggers two recovery dispatches
    const p0 = flaky.dispatch(0, {
      onError: () => {
        chainP1 = flaky.dispatch(1);
        chainP2 = flaky.dispatch(2);
      },
    });

    await p0;
    // Await the chained dispatches directly
     
    await chainP1;
     
    await chainP2;

    expect(order).toEqual(["flaky-fail", "flaky-ok-1", "flaky-ok-2"]);
    expect(callCount).toBe(3);
  });
});

// ===========================================================================
// 3. Cancel during scope-queued wait (action hasn't started run() yet)
// ===========================================================================

describe("cancel during scope-queued wait", () => {
  it("cancelling a queued action that hasn't started lets subsequent actions proceed", async () => {
    let resolveOccupant: (() => void) | null = null;
    const order: string[] = [];

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const occupant = defineAction<void, string>({
      name: "test.queue_cancel_occ",
      scope: "q-cancel",
      run: () =>
        new Promise<string>((r) => {
          resolveOccupant = () => { r("occ"); };
        }),
    });

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const victim = defineAction<void, string>({
      name: "test.queue_cancel_victim",
      scope: "q-cancel",
      error: false,
      run: () => {
        order.push("victim-run");
        return Promise.resolve("victim");
      },
    });

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const follower = defineAction<void, string>({
      name: "test.queue_cancel_follower",
      scope: "q-cancel",
      run: () => {
        order.push("follower-run");
        return Promise.resolve("follower");
      },
    });

    const pOcc = occupant.dispatch();
    await Promise.resolve();
    const pVictim = victim.dispatch();
    const pFollower = follower.dispatch();
    await Promise.resolve();

    // Cancel victim while it's queued (occupant still running)
    victim.cancel();

    // Release occupant
    resolveOccupant!();
    await pOcc;

    // Victim should resolve as cancelled
    const rVictim = await pVictim;
    expect(rVictim).toBeNull();
    expect(order).not.toContain("victim-run");

    // Follower should still run
    const rFollower = await pFollower;
    expect(rFollower).toBe("follower");
    expect(order).toContain("follower-run");
  });

  it("cancelled queued action fires onSettled but not onError", async () => {
    let resolveOccupant: (() => void) | null = null;

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const occupant = defineAction<void, string>({
      name: "test.queue_cancel_cb_occ",
      scope: "q-cancel-cb",
      run: () =>
        new Promise<string>((r) => {
          resolveOccupant = () => { r("occ"); };
        }),
    });

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const victim = defineAction<void, string>({
      name: "test.queue_cancel_cb_victim",
      scope: "q-cancel-cb",
      error: false,
      run: () => Promise.resolve("never"),
    });

    const onError = vi.fn();
    const onSettled = vi.fn();

    const pOcc = occupant.dispatch();
    await Promise.resolve();
    const pVictim = victim.dispatch(undefined, { onError, onSettled });
    await Promise.resolve();

    victim.cancel();
    resolveOccupant!();
    await pOcc;
    await pVictim;

    expect(onError).not.toHaveBeenCalled();
    expect(onSettled).toHaveBeenCalledTimes(1);
  });

  it("registry records cancelled status for queued-then-cancelled action", async () => {
    let resolveOccupant: (() => void) | null = null;

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const occupant = defineAction<void, string>({
      name: "test.queue_cancel_reg_occ",
      scope: "q-cancel-reg",
      run: () =>
        new Promise<string>((r) => {
          resolveOccupant = () => { r("occ"); };
        }),
    });

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const victim = defineAction<void, string>({
      name: "test.queue_cancel_reg_victim",
      scope: "q-cancel-reg",
      error: false,
      run: () => Promise.resolve("never"),
    });

    const pOcc = occupant.dispatch();
    await Promise.resolve();
    const pVictim = victim.dispatch();
    await Promise.resolve();

    victim.cancel();
    resolveOccupant!();
    await pOcc;
    await pVictim;

    const log = recentLog();
    const victimEntry = log.find((e) => e.name === "test.queue_cancel_reg_victim");
    expect(victimEntry).toBeDefined();
    expect(victimEntry!.status).toBe("cancelled");
  });
});

// ===========================================================================
// 4. Retry + scope: callbacks fire before next scope entry starts
// ===========================================================================

describe("retry exhaustion callbacks fire before next scope entry", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("onSettled fires before the next queued action's run() begins", async () => {
    const order: string[] = [];

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const retrier = defineAction<void, string>({
      name: "test.retry_cb_order",
      scope: "retry-cb",
      retryable: (err) => err.code !== "cancelled",
      retry: { count: 1, delay: 30 },
      error: false,
      run: () => {
        throw new ActionError("fail", { status: 500 });
      },
    });

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const follower = defineAction<void, string>({
      name: "test.retry_cb_follower",
      scope: "retry-cb",
      run: () => {
        order.push("follower-run");
        return Promise.resolve("done");
      },
    });

    const pRetrier = retrier.dispatch(undefined, {
      onError: () => {
        order.push("retrier-onError");
      },
      onSettled: () => {
        order.push("retrier-onSettled");
      },
    });
    await Promise.resolve();
    const pFollower = follower.dispatch();

    // Advance past retry
    await vi.advanceTimersByTimeAsync(30);
    await pRetrier;
    await Promise.resolve();
    await pFollower;

    // Callbacks fire before follower starts
    expect(order.indexOf("retrier-onError")).toBeLessThan(order.indexOf("follower-run"));
    expect(order.indexOf("retrier-onSettled")).toBeLessThan(order.indexOf("follower-run"));
  });

  it("onSuccess fires before next scope entry when retry eventually succeeds", async () => {
    const order: string[] = [];
    let attempt = 0;

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const retrier = defineAction<void, string>({
      name: "test.retry_success_order",
      scope: "retry-success",
      retryable: retryNetwork,
      retry: { count: 1, delay: 20 },
      run: () => {
        attempt++;
        if (attempt < 2) {throw new ActionError("net", { code: "network" });}
        return Promise.resolve("ok");
      },
    });

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const follower = defineAction<void, string>({
      name: "test.retry_success_follower",
      scope: "retry-success",
      run: () => {
        order.push("follower-run");
        return Promise.resolve("f");
      },
    });

    const pRetrier = retrier.dispatch(undefined, {
      onSuccess: () => {
        order.push("retrier-onSuccess");
      },
    });
    await Promise.resolve();
    const pFollower = follower.dispatch();

    await vi.advanceTimersByTimeAsync(20);
    await pRetrier;
    await Promise.resolve();
    await pFollower;

    expect(order.indexOf("retrier-onSuccess")).toBeLessThan(order.indexOf("follower-run"));
  });
});

// ===========================================================================
// 5. Dedupe + cancel: cancelling original propagates to deduped callers
// ===========================================================================

describe("dedupe + cancel interaction", () => {
  it("cancelling the original dispatch propagates cancellation to deduped caller", async () => {
    const action = defineAction<string, string>({
      name: "test.dedupe_cancel",
      dedupe: true,
      error: false,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => { reject(new DOMException("aborted", "AbortError")); });
        }),
    });

    const onError1 = vi.fn();
    const onError2 = vi.fn();
    const onSettled1 = vi.fn();
    const onSettled2 = vi.fn();

    const p1 = action.dispatch("a", { onError: onError1, onSettled: onSettled1 });
    const p2 = action.dispatch("a", { onError: onError2, onSettled: onSettled2 });

    action.cancel();

    const [r1, r2] = await Promise.all([p1, p2]);

    expect(r1).toBeNull();
    expect(r2).toBeNull();
    // onError should NOT fire for cancellation (per contract)
    expect(onError1).not.toHaveBeenCalled();
    expect(onError2).not.toHaveBeenCalled();
    // onSettled fires for both
    expect(onSettled1).toHaveBeenCalledTimes(1);
    expect(onSettled2).toHaveBeenCalledTimes(1);
  });

  it("dedupe map is cleaned up after cancellation", async () => {
    const action = defineAction<string, string>({
      name: "test.dedupe_cancel_cleanup",
      dedupe: true,
      error: false,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => { reject(new DOMException("aborted", "AbortError")); });
        }),
    });

    const p = action.dispatch("b");
    action.cancel();
    await p;

    const { activeDedupes } = _internalsForTest();
    expect(activeDedupes).toBe(0);
  });
});

// ===========================================================================
// 6. Cross-action scope chain: interleaved dispatches from different actions
// ===========================================================================

describe("interleaved cross-action scope chain ordering", () => {
  it("A-B-A-B dispatches serialize in dispatch order", async () => {
    const order: string[] = [];

    const actionA = defineAction<number, string>({
      name: "test.interleave_A",
      scope: "interleave",
      run: (n) => {
        order.push(`A-${String(n)}`);
        return Promise.resolve(`A-${String(n)}`);
      },
    });

    const actionB = defineAction<number, string>({
      name: "test.interleave_B",
      scope: "interleave",
      run: (n) => {
        order.push(`B-${String(n)}`);
        return Promise.resolve(`B-${String(n)}`);
      },
    });

    const p1 = actionA.dispatch(1);
    const p2 = actionB.dispatch(1);
    const p3 = actionA.dispatch(2);
    const p4 = actionB.dispatch(2);

    await Promise.all([p1, p2, p3, p4]);

    expect(order).toEqual(["A-1", "B-1", "A-2", "B-2"]);
  });

  it("scope chain drains completely — no leaked promises", async () => {
    const action = defineAction<number, number>({
      name: "test.drain",
      scope: "drain",
      run: (n) => Promise.resolve(n),
    });

    await action.dispatch(1);
    await action.dispatch(2);
    await action.dispatch(3);

    const { scopeChains } = _internalsForTest();
    expect(scopeChains).toBe(0);
  });
});

// ===========================================================================
// 7. onSuccess re-dispatch with dedupe: dispatch after full settlement
// ===========================================================================

describe("onSuccess re-dispatch with dedupe", () => {
  it("re-dispatching same action after promise settles starts a fresh run", async () => {
    let runCount = 0;
    const action = defineAction<string, string>({
      name: "test.redispatch_dedupe",
      dedupe: true,
      run: () => {
        runCount++;
        return Promise.resolve(`run-${String(runCount)}`);
      },
    });

    const r1 = await action.dispatch("key");
    expect(r1).toBe("run-1");

    // After full settlement (including .finally() cleanup), dedupe is clear
    await Promise.resolve(); // let .finally() run

    const r2 = await action.dispatch("key");
    expect(r2).toBe("run-2");
    expect(runCount).toBe(2);
  });

  it("dispatch from onSuccess with same dedupe key starts a fresh run (eager cleanup)", async () => {
    // With the eager evictDedupeSlot fix, dispatching from onSuccess with
    // the same key starts a fresh run because the dedupe entry is
    // cleared synchronously before callbacks fire.
    let runCount = 0;
    const action = defineAction<string, string>({
      name: "test.redispatch_dedupe_sync",
      dedupe: true,
      run: () => {
        runCount++;
        return Promise.resolve(`run-${String(runCount)}`);
      },
    });

    let chainedPromise: Promise<string | null> | null = null;
    await action.dispatch("key", {
      onSuccess: () => {
        // Dedupe entry cleared before onSuccess — this starts a fresh run
        chainedPromise = action.dispatch("key");
      },
    });

    // Await the chained dispatch directly
     
    const chainedResult = await chainedPromise;

    expect(runCount).toBe(2); // Two separate runs
    expect(chainedResult).toBe("run-2");
  });
});

// ===========================================================================
// 8. Retry + cancel race: cancel arrives exactly when retry succeeds
// ===========================================================================

describe("retry + cancel race at success boundary", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("cancel during retry backoff prevents subsequent retry attempt", async () => {
    let attempt = 0;
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = defineAction<void, string>({
      name: "test.cancel_mid_backoff",
      retryable: (err) => err.code !== "cancelled",
      retry: { count: 3, delay: 100 },
      error: false,
      run: () => {
        attempt++;
        throw new ActionError("fail", { status: 500 });
      },
    });

    const p = action.dispatch();
    await Promise.resolve(); // first attempt runs
    expect(attempt).toBe(1);

    // Advance partway into first backoff (50ms of 100ms)
    await vi.advanceTimersByTimeAsync(50);
    expect(attempt).toBe(1); // still in backoff

    // Cancel during backoff
    action.cancel();
    const result = await p;

    expect(result).toBeNull();
    expect(attempt).toBe(1); // no further attempts after cancel
  });

  it("registry records cancelled (not error) when cancel arrives during retry", async () => {
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = defineAction<void, string>({
      name: "test.cancel_retry_registry",
      retryable: (err) => err.code !== "cancelled",
      retry: { count: 2, delay: 50 },
      error: false,
      run: () => {
        throw new ActionError("fail", { status: 500 });
      },
    });

    const p = action.dispatch();
    await Promise.resolve();
    await vi.advanceTimersByTimeAsync(25); // mid-backoff
    action.cancel();
    await p;

    const log = recentLog();
    const entry = log.find((e) => e.name === "test.cancel_retry_registry");
    expect(entry).toBeDefined();
    expect(entry!.status).toBe("cancelled");
  });
});

// ===========================================================================
// 9. Scope + optimistic + cancel: rollback fires for queued-then-started action
// ===========================================================================

describe("scope + optimistic + cancel interaction", () => {
  it("rollback fires when action is cancelled after optimistic ran", async () => {
    const rollbackCalls: string[] = [];

    const action = defineAction<string, string, string>({
      name: "test.opt_cancel_rollback",
      scope: "opt-cancel",
      optimistic: (args) => `snapshot-${args}`,
      rollback: (_args, op) => {
        rollbackCalls.push(op ?? "none");
      },
      error: false,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => { reject(new DOMException("aborted", "AbortError")); });
        }),
    });

    const p = action.dispatch("x");
    await Promise.resolve(); // optimistic + run start

    // Cancel while run() is in-flight (after optimistic applied)
    action.cancel();
    const result = await p;

    expect(result).toBeNull();
    expect(rollbackCalls).toEqual(["snapshot-x"]);
  });

  it("rollback does NOT fire when action is cancelled before optimistic runs (queued)", async () => {
    const rollbackCalls: string[] = [];
    let resolveOccupant: (() => void) | null = null;

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const occupant = defineAction<void, string>({
      name: "test.opt_cancel_occ",
      scope: "opt-cancel-q",
      run: () =>
        new Promise<string>((r) => {
          resolveOccupant = () => { r("occ"); };
        }),
    });

    const victim = defineAction<string, string, string>({
      name: "test.opt_cancel_victim",
      scope: "opt-cancel-q",
      optimistic: (args) => `snap-${args}`,
      rollback: (_args, op) => {
        rollbackCalls.push(op ?? "none");
      },
      error: false,
      run: () => Promise.resolve("never"),
    });

    const pOcc = occupant.dispatch();
    await Promise.resolve();
    const pVictim = victim.dispatch("y");
    await Promise.resolve();

    // Cancel victim while it's queued (optimistic hasn't run yet)
    victim.cancel();
    resolveOccupant!();
    await pOcc;
    await pVictim;

    // No rollback because optimistic never ran
    expect(rollbackCalls).toEqual([]);
  });
});
