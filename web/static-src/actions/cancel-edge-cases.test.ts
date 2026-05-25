// @vitest-environment happy-dom
// Cancel handling edge cases: retry-backoff cancel, onCancel throw safety,
// cancel-after-optimistic-before-run, re-dispatch from onSettled after cancel.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { ActionError } from "./error.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

describe("cancel during retry backoff sleep", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("fires onCancel (not onError) when cancelled mid-backoff", async () => {
    let attempts = 0;
    const onCancel = vi.fn();
    const onError = vi.fn();
    const onSettled = vi.fn();

    const action = defineAction<void, string>({
      name: "test.cancel_mid_backoff",
      retryable: "always",
      retry: { count: 3, delay: 500 },
      error: false,
      run: async () => {
        attempts++;
        throw new ActionError("transient", { code: "network" });
      },
    });

    const p = action.dispatch(undefined, { onCancel, onError, onSettled });
    // First attempt fails, enters 500ms backoff sleep
    await vi.advanceTimersByTimeAsync(0);
    expect(attempts).toBe(1);

    // Cancel during the backoff sleep
    action.cancel();
    await vi.advanceTimersByTimeAsync(600);
    await p;

    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onError).not.toHaveBeenCalled();
    expect(onSettled).toHaveBeenCalledTimes(1);
    expect(attempts).toBe(1); // no second attempt
    const log = recentLog();
    expect(log[0]?.status).toBe("cancelled");
  });

  it("isInflight becomes false after cancel mid-backoff", async () => {
    const action = defineAction<void, string>({
      name: "test.cancel_backoff_inflight",
      retryable: "always",
      retry: { count: 3, delay: 1000 },
      error: false,
      run: async () => { throw new ActionError("fail", { code: "network" }); },
    });

    const p = action.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(0);
    expect(action.isInflight).toBe(true);

    action.cancel();
    await vi.advanceTimersByTimeAsync(1100);
    await p;

    expect(action.isInflight).toBe(false);
  });
});

describe("onCancel callback throws — onSettled still fires", () => {
  it("onSettled fires even when onCancel throws", async () => {
    const consoleErr = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const onSettled = vi.fn();

    const action = defineAction<void, string>({
      name: "test.oncancel_throws",
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const p = action.dispatch(undefined, {
      onCancel: () => { throw new Error("onCancel exploded"); },
      onSettled,
    });
    action.cancel();
    await p;

    expect(onSettled).toHaveBeenCalledTimes(1);
    expect(consoleErr).toHaveBeenCalled();
    consoleErr.mockRestore();
  });

  it("onSettled fires when onCancel throws in scope-queued fast-path", async () => {
    const consoleErr = vi.spyOn(console, "error").mockImplementation(() => undefined);
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });
    const onSettled = vi.fn();

    const blocker = defineAction<void, string>({
      name: "test.blocker_oncancel_throw",
      scope: "s",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<void, string>({
      name: "test.fastpath_oncancel_throw",
      scope: "s",
      run: async () => "should-not-run",
    });

    const pBlock = blocker.dispatch();
    const p = action.dispatch(undefined, {
      onCancel: () => { throw new Error("onCancel boom"); },
      onSettled,
    });

    action.cancel();
    resolve1();
    await pBlock;
    await p;

    expect(onSettled).toHaveBeenCalledTimes(1);
    expect(consoleErr).toHaveBeenCalled();
    consoleErr.mockRestore();
  });
});

describe("cancel after optimistic before run completes (same-tick)", () => {
  it("rollback fires with code:cancelled when cancel is synchronous after dispatch", async () => {
    const rollback = vi.fn();
    let runStarted = false;

    const action = defineAction<void, string>({
      name: "test.cancel_sync_after_optimistic",
      optimistic: () => ({ token: "opt" }),
      rollback,
      run: async (_args, signal) => {
        runStarted = true;
        await new Promise((r) => setTimeout(r, 50));
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "done";
      },
    });

    const p = action.dispatch();
    // Cancel in same microtask — run() has started but signal aborts
    action.cancel();
    await p;

    expect(rollback).toHaveBeenCalledTimes(1);
    expect(rollback.mock.calls[0]![2]).toMatchObject({ code: "cancelled" });
    expect(runStarted).toBe(true);
  });

  it("optimistic is NOT rolled back on success (sanity check)", async () => {
    const rollback = vi.fn();
    const action = defineAction<void, string>({
      name: "test.no_rollback_on_success",
      optimistic: () => ({ token: "x" }),
      rollback,
      run: async () => "ok",
    });

    await action.dispatch();
    expect(rollback).not.toHaveBeenCalled();
  });
});

describe("re-dispatch from onSettled after cancel", () => {
  it("re-dispatch in onSettled callback starts a fresh run (no stale dedupe)", async () => {
    let runCount = 0;
    let secondResult: string | null = null;

    const action = defineAction<string, string>({
      name: "test.redispatch_from_onsettled",
      dedupe: true,
      run: async (_args, signal) => {
        runCount++;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return `run-${runCount}`;
      },
    });

    const p1 = action.dispatch("a", {
      onSettled: () => {
        // Re-dispatch from within onSettled after cancel
        void action.dispatch("a").then((r) => { secondResult = r; });
      },
    });
    action.cancel();
    await p1;
    // Allow the re-dispatch to complete
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));

    expect(secondResult).toBe("run-2");
    expect(runCount).toBe(2);
  });

  it("re-dispatch in onCancel callback starts a fresh run", async () => {
    let runCount = 0;
    let secondResult: string | null = null;

    const action = defineAction<string, string>({
      name: "test.redispatch_from_oncancel",
      dedupe: true,
      run: async (_args, signal) => {
        runCount++;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return `run-${runCount}`;
      },
    });

    const p1 = action.dispatch("b", {
      onCancel: () => {
        void action.dispatch("b").then((r) => { secondResult = r; });
      },
    });
    action.cancel();
    await p1;
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));

    expect(secondResult).toBe("run-2");
    expect(runCount).toBe(2);
  });
});

describe("multiple in-flight dispatches — partial vs full cancel", () => {
  it("cancel() aborts ALL in-flight dispatches for the action", async () => {
    const results: (string | null)[] = [];
    const action = defineAction<number, string>({
      name: "test.cancel_all_inflight",
      run: async (n, signal) => {
        await new Promise((r) => setTimeout(r, 10));
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return `done-${n}`;
      },
    });

    const p1 = action.dispatch(1);
    const p2 = action.dispatch(2);
    const p3 = action.dispatch(3);
    expect(action.isInflight).toBe(true);

    action.cancel();
    const [r1, r2, r3] = await Promise.all([p1, p2, p3]);
    results.push(r1, r2, r3);

    expect(results).toEqual([null, null, null]);
    expect(action.isInflight).toBe(false);
  });

  it("new dispatch after cancel-all succeeds independently", async () => {
    const action = defineAction<number, string>({
      name: "test.dispatch_after_cancel_all",
      run: async (n, signal) => {
        await new Promise((r) => setTimeout(r, 5));
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return `ok-${n}`;
      },
    });

    const p1 = action.dispatch(1);
    action.cancel();
    await p1;

    const r2 = await action.dispatch(2);
    expect(r2).toBe("ok-2");
  });
});

describe("cancel with scope + dedupe + retry combined", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("scope-queued + dedupe + retry: cancel cleans up all layers", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });
    let runCount = 0;
    const onCancel = vi.fn();
    const onSettled = vi.fn();

    const blocker = defineAction<void, string>({
      name: "test.combo_blocker",
      scope: "combo",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<string, string>({
      name: "test.combo_cancel",
      scope: "combo",
      dedupe: true,
      retryable: "always",
      retry: { count: 2, delay: 100 },
      error: false,
      run: async () => { runCount++; return "should-not-reach"; },
    });

    const pBlock = blocker.dispatch();
    const p1 = action.dispatch("x", { onCancel, onSettled });
    const p2 = action.dispatch("x"); // dedupes onto p1

    // Cancel while queued behind blocker
    action.cancel();
    resolve1();
    await pBlock;
    await vi.advanceTimersByTimeAsync(500);
    const [r1, r2] = await Promise.all([p1, p2]);

    expect(r1).toBeNull();
    expect(r2).toBeNull();
    expect(runCount).toBe(0); // fast-path: never ran
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onSettled).toHaveBeenCalledTimes(1);
    expect(action.isInflight).toBe(false);
  });
});

describe("double cancel() is idempotent", () => {
  it("calling cancel() twice does not throw or double-fire onCancel", async () => {
    const onCancel = vi.fn();
    const onSettled = vi.fn();

    const action = defineAction<void, string>({
      name: "test.double_cancel",
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const p = action.dispatch(undefined, { onCancel, onSettled });
    action.cancel();
    action.cancel(); // second cancel — should be no-op
    await p;

    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onSettled).toHaveBeenCalledTimes(1);
    expect(action.isInflight).toBe(false);
  });
});

describe("cancel + idempotencyKey", () => {
  it("idempotencyKey does not prevent cancellation", async () => {
    const onCancel = vi.fn();
    let receivedCtx: unknown = null;

    const action = defineAction<void, string>({
      name: "test.cancel_idem",
      idempotencyKey: true,
      run: (_args, signal, ctx) => {
        receivedCtx = ctx;
        return new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        });
      },
    });

    const p = action.dispatch(undefined, { onCancel });
    // Let run() start so ctx is captured
    await Promise.resolve();
    action.cancel();
    await p;

    expect(onCancel).toHaveBeenCalledTimes(1);
    // Verify idempotencyKey was generated
    expect(receivedCtx).toHaveProperty("idempotencyKey");
    expect((receivedCtx as { idempotencyKey: string }).idempotencyKey).toBeTruthy();
  });
});

describe("cancel during onRetryAttempt callback", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("cancel inside onRetryAttempt prevents subsequent retry attempts", async () => {
    let attempts = 0;
    const onCancel = vi.fn();
    let actionRef: ReturnType<typeof defineAction<void, string>>;

    const action = defineAction<void, string>({
      name: "test.cancel_in_retry_attempt",
      retryable: "always",
      retry: { count: 5, delay: 100 },
      error: false,
      run: async () => {
        attempts++;
        throw new ActionError("transient", { code: "network" });
      },
    });
    actionRef = action;

    const p = action.dispatch(undefined, {
      onCancel,
      onRetryAttempt: () => {
        // Cancel from within the retry attempt callback
        actionRef.cancel();
      },
    });

    // First attempt fails, enters backoff
    await vi.advanceTimersByTimeAsync(0);
    expect(attempts).toBe(1);

    // Advance past first backoff — onRetryAttempt fires and cancels
    await vi.advanceTimersByTimeAsync(200);
    await p;

    expect(onCancel).toHaveBeenCalledTimes(1);
    // Should not have run more than 2 attempts (1st fails, 2nd attempt
    // is where onRetryAttempt fires and cancels)
    expect(attempts).toBeLessThanOrEqual(2);
  });
});

describe("cancel + scope: next-in-scope starts after cancel", () => {
  it("cancelled dispatch unblocks the next dispatch in the same scope", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });
    const order: string[] = [];

    const first = defineAction<void, string>({
      name: "test.scope_cancel_first",
      scope: "unblock",
      run: async (_args, signal) => {
        order.push("first-start");
        await gate;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "first";
      },
    });

    const second = defineAction<void, string>({
      name: "test.scope_cancel_second",
      scope: "unblock",
      run: async () => { order.push("second-start"); return "second"; },
    });

    const p1 = first.dispatch();
    const p2 = second.dispatch();

    // Cancel first while it's running
    first.cancel();
    resolve1();
    await p1;
    const r2 = await p2;

    expect(r2).toBe("second");
    expect(order).toContain("second-start");
  });
});
