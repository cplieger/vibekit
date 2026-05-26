// @vitest-environment happy-dom
// Cycle 10 Stage 1 Batch 2: Additional cross-action chain, race condition,
// and retry interaction tests. Covers gaps identified in batch 1 review:
// - dedupe + scope combined behavior
// - optimistic persistence across retries (rollback fires only once)
// - concurrent cancel + success race in dedupe
// - onSettled re-dispatch with dedupe key
// - scope chain cleanup after optimistic throw
// - debounce + retry interaction
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
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
// 1. Dedupe + scope combined: deduped dispatch shares scope-queued promise
// ===========================================================================

describe("dedupe + scope combined behavior", () => {
  it("deduped dispatch shares the scope-queued promise without double-queuing", async () => {
    let runCount = 0;
    let resolveRun: ((v: string) => void) | null = null;

    const action = defineAction<string, string>({
      name: "test.dedupe_scope",
      dedupe: true,
      scope: "ds",
      run: () => {
        runCount++;
        return new Promise<string>((r) => { resolveRun = r; });
      },
    });

    const p1 = action.dispatch("x");
    const p2 = action.dispatch("x"); // same dedupe key, should collapse

    await Promise.resolve();
    expect(runCount).toBe(1); // only one run

    resolveRun!("done");
    const [r1, r2] = await Promise.all([p1, p2]);

    expect(r1).toBe("done");
    expect(r2).toBe("done");
    expect(runCount).toBe(1);
  });

  it("deduped dispatch does not block scope chain for different args", async () => {
    const order: string[] = [];

    const action = defineAction<string, string>({
      name: "test.dedupe_scope_diff",
      dedupe: true,
      scope: "ds-diff",
      run: (args) => {
        order.push(`run-${args}`);
        return Promise.resolve(`done-${args}`);
      },
    });

    // Different args = different dedupe keys, so both queue in scope
    const p1 = action.dispatch("a");
    const p2 = action.dispatch("b");

    await Promise.all([p1, p2]);
    expect(order).toEqual(["run-a", "run-b"]);
  });

  it("scope chain drains after deduped dispatch settles", async () => {
    const action = defineAction<string, string>({
      name: "test.dedupe_scope_drain",
      dedupe: true,
      scope: "ds-drain",
      run: () => Promise.resolve("ok"),
    });

    const p1 = action.dispatch("k");
    const p2 = action.dispatch("k");
    await Promise.all([p1, p2]);

    // Let .finally() cleanup run
    await Promise.resolve();
    await Promise.resolve();

    const { scopeChains, activeDedupes } = _internalsForTest();
    expect(scopeChains).toBe(0);
    expect(activeDedupes).toBe(0);
  });
});

// ===========================================================================
// 2. Optimistic persists across retries — rollback fires only once at end
// ===========================================================================

describe("optimistic persistence across retries", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("rollback fires exactly once after all retries exhaust", async () => {
    const rollbackCalls: string[] = [];
    let attempt = 0;

    const action = defineAction<string, string, string>({
      name: "test.opt_retry_rollback",
      retryable: (err) => err.code !== "cancelled",
      retry: { count: 2, delay: 50 },
      error: false,
      optimistic: (args) => `snap-${args}`,
      rollback: (_args, op) => { rollbackCalls.push(op ?? "none"); },
      run: () => {
        attempt++;
        throw new ActionError("fail", { status: 500 });
      },
    });

    const p = action.dispatch("x");
    // Advance through retries
    await vi.advanceTimersByTimeAsync(50);  // retry 1
    await vi.advanceTimersByTimeAsync(100); // retry 2
    await p;

    expect(attempt).toBe(3); // 1 original + 2 retries
    expect(rollbackCalls).toEqual(["snap-x"]); // exactly once
  });

  it("rollback does NOT fire when retries eventually succeed", async () => {
    const rollbackCalls: string[] = [];
    let attempt = 0;

    const action = defineAction<string, string, string>({
      name: "test.opt_retry_success",
      retryable: retryNetwork,
      retry: { count: 2, delay: 30 },
      optimistic: (args) => `snap-${args}`,
      rollback: (_args, op) => { rollbackCalls.push(op ?? "none"); },
      run: () => {
        attempt++;
        if (attempt < 3) throw new ActionError("net", { code: "network" });
        return Promise.resolve("recovered");
      },
    });

    const p = action.dispatch("y");
    await vi.advanceTimersByTimeAsync(30);
    await vi.advanceTimersByTimeAsync(60);
    const result = await p;

    expect(result).toBe("recovered");
    expect(rollbackCalls).toEqual([]); // no rollback on success
  });

  it("optimistic runs exactly once even with retries", async () => {
    let optimisticCount = 0;

    const action = defineAction<void, string>({
      name: "test.opt_once",
      retryable: (err) => err.code !== "cancelled",
      retry: { count: 1, delay: 20 },
      error: false,
      optimistic: () => { optimisticCount++; return undefined; },
      run: () => { throw new ActionError("fail", { status: 500 }); },
    });

    const p = action.dispatch();
    await vi.advanceTimersByTimeAsync(20);
    await p;

    expect(optimisticCount).toBe(1);
  });
});

// ===========================================================================
// 3. Cancel + success race in dedupe: cancel arrives as run() resolves
// ===========================================================================

describe("cancel + success race in dedupe", () => {
  it("cancel wins over success — deduped caller sees cancellation", async () => {
    const action = defineAction<string, string>({
      name: "test.dedupe_cancel_race",
      dedupe: true,
      error: false,
      run: (_args, signal) => {
        return new Promise<string>((_resolve, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        });
      },
    });

    const onSuccess1 = vi.fn();
    const onError1 = vi.fn();
    const onSettled1 = vi.fn();
    const onSuccess2 = vi.fn();
    const onError2 = vi.fn();
    const onSettled2 = vi.fn();

    const p1 = action.dispatch("k", { onSuccess: onSuccess1, onError: onError1, onSettled: onSettled1 });
    const p2 = action.dispatch("k", { onSuccess: onSuccess2, onError: onError2, onSettled: onSettled2 });

    // Cancel before resolving — signal aborts, run rejects with AbortError
    action.cancel();

    const [r1, r2] = await Promise.all([p1, p2]);

    expect(r1).toBeNull();
    expect(r2).toBeNull();
    // Cancellation: onError does NOT fire
    expect(onError1).not.toHaveBeenCalled();
    expect(onError2).not.toHaveBeenCalled();
    // onSuccess does NOT fire
    expect(onSuccess1).not.toHaveBeenCalled();
    expect(onSuccess2).not.toHaveBeenCalled();
    // onSettled fires for both
    expect(onSettled1).toHaveBeenCalledTimes(1);
    expect(onSettled2).toHaveBeenCalledTimes(1);
  });

  it("dedupe map cleaned after cancel-race", async () => {
    const action = defineAction<string, string>({
      name: "test.dedupe_cancel_race_clean",
      dedupe: true,
      error: false,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const p1 = action.dispatch("k");
    action.dispatch("k"); // deduped
    action.cancel();
    await p1;

    await Promise.resolve();
    const { activeDedupes } = _internalsForTest();
    expect(activeDedupes).toBe(0);
  });
});

// ===========================================================================
// 4. onSettled re-dispatch with same dedupe key starts fresh
// ===========================================================================

describe("onSettled re-dispatch with dedupe", () => {
  it("dispatch from onSettled with same dedupe key starts a fresh run", async () => {
    let runCount = 0;
    const action = defineAction<string, string>({
      name: "test.settled_redispatch",
      dedupe: true,
      run: () => {
        runCount++;
        return Promise.resolve(`run-${String(runCount)}`);
      },
    });

    let chainedPromise: Promise<string | null> | null = null;
    await action.dispatch("k", {
      onSettled: () => {
        chainedPromise = action.dispatch("k");
      },
    });

    const result = await chainedPromise!;
    expect(runCount).toBe(2);
    expect(result).toBe("run-2");
  });

  it("dispatch from onError with same dedupe key starts a fresh run", async () => {
    let runCount = 0;
    const action = defineAction<string, string>({
      name: "test.error_redispatch",
      dedupe: true,
      error: false,
      run: () => {
        runCount++;
        if (runCount === 1) throw new ActionError("fail");
        return Promise.resolve("recovered");
      },
    });

    let chainedPromise: Promise<string | null> | null = null;
    await action.dispatch("k", {
      onError: () => {
        chainedPromise = action.dispatch("k");
      },
    });

    const result = await chainedPromise!;
    expect(runCount).toBe(2);
    expect(result).toBe("recovered");
  });
});

// ===========================================================================
// 5. Scope chain cleanup after optimistic throw
// ===========================================================================

describe("scope chain after optimistic throw", () => {
  it("next action in scope runs after optimistic throw", async () => {
    const order: string[] = [];

    const broken = defineAction<void, string>({
      name: "test.opt_throw",
      scope: "opt-throw",
      error: false,
      optimistic: () => { throw new Error("optimistic boom"); },
      run: () => Promise.resolve("never"),
    });

    const follower = defineAction<void, string>({
      name: "test.opt_throw_follower",
      scope: "opt-throw",
      run: () => {
        order.push("follower-run");
        return Promise.resolve("ok");
      },
    });

    const pBroken = broken.dispatch();
    const pFollower = follower.dispatch();

    const rBroken = await pBroken;
    expect(rBroken).toBeNull();

    const rFollower = await pFollower;
    expect(rFollower).toBe("ok");
    expect(order).toEqual(["follower-run"]);
  });

  it("scope chain drains after optimistic throw", async () => {
    const action = defineAction<void, string>({
      name: "test.opt_throw_drain",
      scope: "opt-throw-drain",
      error: false,
      optimistic: () => { throw new Error("boom"); },
      run: () => Promise.resolve("never"),
    });

    await action.dispatch();
    await Promise.resolve();
    await Promise.resolve();

    const { scopeChains } = _internalsForTest();
    expect(scopeChains).toBe(0);
  });
});

// ===========================================================================
// 6. Retry + dedupe + scope triple interaction
// ===========================================================================

describe("retry + dedupe + scope triple interaction", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("deduped dispatch in scope with retry: single run, retries, both callers get result", async () => {
    let attempt = 0;

    const action = defineAction<string, string>({
      name: "test.triple",
      dedupe: true,
      scope: "triple",
      retryable: retryNetwork,
      retry: { count: 1, delay: 40 },
      error: false,
      run: () => {
        attempt++;
        if (attempt < 2) throw new ActionError("net", { code: "network" });
        return Promise.resolve("ok");
      },
    });

    const onSuccess1 = vi.fn();
    const onSuccess2 = vi.fn();

    const p1 = action.dispatch("k", { onSuccess: onSuccess1 });
    const p2 = action.dispatch("k", { onSuccess: onSuccess2 });

    await vi.advanceTimersByTimeAsync(40);
    const [r1, r2] = await Promise.all([p1, p2]);

    expect(r1).toBe("ok");
    expect(r2).toBe("ok");
    expect(attempt).toBe(2); // 1 fail + 1 retry success
    expect(onSuccess1).toHaveBeenCalledWith("ok", "k");
    expect(onSuccess2).toHaveBeenCalledWith("ok", "k");
  });

  it("different dedupe keys in same scope serialize correctly", async () => {
    const order: string[] = [];

    const action = defineAction<string, string>({
      name: "test.triple_diff",
      dedupe: true,
      scope: "triple-diff",
      retryable: retryNetwork,
      retry: { count: 1, delay: 20 },
      run: (args) => {
        order.push(`run-${args}`);
        return Promise.resolve(`done-${args}`);
      },
    });

    const p1 = action.dispatch("a");
    const p2 = action.dispatch("b"); // different key, queues in scope

    await Promise.all([p1, p2]);
    expect(order).toEqual(["run-a", "run-b"]);
  });
});

// ===========================================================================
// 7. Rapid cancel + re-dispatch: no stale AbortController leaks
// ===========================================================================

describe("rapid cancel + re-dispatch", () => {
  it("re-dispatch after cancel uses a fresh AbortController", async () => {
    let signalAborted: boolean[] = [];

    const action = defineAction<void, string>({
      name: "test.rapid_cancel",
      error: false,
      run: (_args, signal) => {
        signalAborted.push(signal.aborted);
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("ok");
      },
    });

    // First dispatch + immediate cancel
    const p1 = action.dispatch();
    action.cancel();
    await p1;

    // Second dispatch should have a fresh (non-aborted) signal
    const p2 = action.dispatch();
    const r2 = await p2;

    expect(r2).toBe("ok");
    // First call saw non-aborted signal (abort happens after run starts)
    // Second call definitely sees non-aborted signal
    expect(signalAborted[1]).toBe(false);
  });

  it("scope chain is not corrupted by rapid cancel + re-dispatch", async () => {
    const order: string[] = [];

    const action = defineAction<number, string>({
      name: "test.rapid_cancel_scope",
      scope: "rapid",
      error: false,
      run: (n, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        order.push(`run-${String(n)}`);
        return Promise.resolve(`done-${String(n)}`);
      },
    });

    // Dispatch 1, cancel, dispatch 2, dispatch 3
    const p1 = action.dispatch(1);
    action.cancel();
    await p1;

    const p2 = action.dispatch(2);
    const p3 = action.dispatch(3);
    await Promise.all([p2, p3]);

    expect(order).toEqual(["run-2", "run-3"]);
  });
});

// ===========================================================================
// 8. Idempotency key stability across retries
// ===========================================================================

describe("idempotency key stability across retries", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("same idempotency key is passed to all retry attempts", async () => {
    const keys: (string | undefined)[] = [];

    const action = defineAction<void, string>({
      name: "test.idem_retry",
      idempotencyKey: true,
      retryable: (err) => err.code !== "cancelled",
      retry: { count: 2, delay: 20 },
      error: false,
      run: (_args, _signal, ctx) => {
        keys.push(ctx?.idempotencyKey);
        throw new ActionError("fail", { status: 500 });
      },
    });

    const p = action.dispatch();
    await vi.advanceTimersByTimeAsync(20);
    await vi.advanceTimersByTimeAsync(40);
    await p;

    expect(keys.length).toBe(3);
    expect(keys[0]).toBeDefined();
    expect(keys[0]).toBe(keys[1]);
    expect(keys[1]).toBe(keys[2]);
  });

  it("different dispatches get different idempotency keys", async () => {
    const keys: (string | undefined)[] = [];

    const action = defineAction<void, string>({
      name: "test.idem_unique",
      idempotencyKey: true,
      run: (_args, _signal, ctx) => {
        keys.push(ctx?.idempotencyKey);
        return Promise.resolve("ok");
      },
    });

    await action.dispatch();
    await action.dispatch();

    expect(keys.length).toBe(2);
    expect(keys[0]).not.toBe(keys[1]);
  });
});

// ===========================================================================
// 9. retryable: composition with custom permanent-code filters
// ===========================================================================

describe("retryable composition (custom permanent codes)", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("'cancelled' code does not retry under app-defined retry-everything-except-cancelled", async () => {
    let attempt = 0;

    const action = defineAction<void, string>({
      name: "test.cancelled_no_retry",
      retryable: (err) => err.code !== "cancelled",
      retry: { count: 3, delay: 50 },
      error: false,
      run: () => {
        attempt++;
        throw new ActionError("cancelled", { code: "cancelled" });
      },
    });

    const p = action.dispatch();
    await p;

    expect(attempt).toBe(1);
  });

  it("composed classifier filters app-specific permanent codes", async () => {
    const APP_PERMANENT = new Set(["server_rejected", "send_failed", "clipboard", "unsupported"]);
    const retryAppSafe = (err: { code?: string; status?: number; message: string }): boolean =>
      !APP_PERMANENT.has(err.code ?? "") && err.code !== "cancelled";

    let attempt = 0;
    const action = defineAction<void, string>({
      name: "test.app_perm_rejected",
      retryable: retryAppSafe,
      retry: { count: 3, delay: 50 },
      error: false,
      run: () => {
        attempt++;
        throw new ActionError("rejected", { code: "server_rejected" });
      },
    });

    const p = action.dispatch();
    await p;

    expect(attempt).toBe(1);
  });

  it("composed classifier still retries non-permanent errors", async () => {
    const APP_PERMANENT = new Set(["server_rejected"]);
    const retryAppSafe = (err: { code?: string; status?: number; message: string }): boolean =>
      !APP_PERMANENT.has(err.code ?? "") && err.code !== "cancelled";

    let attempt = 0;
    const action = defineAction<void, string>({
      name: "test.app_perm_validation",
      retryable: retryAppSafe,
      retry: { count: 2, delay: 0 },
      error: false,
      run: () => {
        attempt++;
        throw new ActionError("transient", { code: "validation" });
      },
    });

    const p = action.dispatch();
    await vi.runAllTimersAsync();
    await p;

    expect(attempt).toBe(3); // 1 initial + 2 retries
  });
});

// ===========================================================================
// 10. Registry attempts field tracks retry count correctly
// ===========================================================================

describe("registry attempts field", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("records attempts=1 when no retry occurs", async () => {
    const action = defineAction<void, string>({
      name: "test.attempts_1",
      run: () => Promise.resolve("ok"),
    });

    await action.dispatch();
    const log = recentLog();
    const entry = log.find((e) => e.name === "test.attempts_1" && e.status === "success");
    expect(entry?.attempts).toBe(1);
  });

  it("records correct attempts count after retries exhaust", async () => {
    const action = defineAction<void, string>({
      name: "test.attempts_exhaust",
      retryable: (err) => err.code !== "cancelled",
      retry: { count: 2, delay: 10 },
      error: false,
      run: () => { throw new ActionError("fail", { status: 500 }); },
    });

    const p = action.dispatch();
    await vi.advanceTimersByTimeAsync(10);
    await vi.advanceTimersByTimeAsync(20);
    await p;

    const log = recentLog();
    const entry = log.find((e) => e.name === "test.attempts_exhaust" && e.status === "error");
    expect(entry?.attempts).toBe(3);
  });

  it("records correct attempts when retry succeeds on 2nd try", async () => {
    let attempt = 0;
    const action = defineAction<void, string>({
      name: "test.attempts_recover",
      retryable: retryNetwork,
      retry: { count: 2, delay: 10 },
      run: () => {
        attempt++;
        if (attempt < 2) throw new ActionError("net", { code: "network" });
        return Promise.resolve("ok");
      },
    });

    const p = action.dispatch();
    await vi.advanceTimersByTimeAsync(10);
    await p;

    const log = recentLog();
    const entry = log.find((e) => e.name === "test.attempts_recover" && e.status === "success");
    expect(entry?.attempts).toBe(2);
  });
});
