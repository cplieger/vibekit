// @vitest-environment happy-dom
// Tests for the three UX primitives added on top of the action
// framework: auto-retry with backoff, mutation scopes (serialization),
// and per-dispatch callback overrides.
//
// Each test uses fake timers so backoff delays don't slow the suite.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { ActionError } from "./error.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.useFakeTimers();
  vi.clearAllMocks();
});

afterEach(() => {
  vi.useRealTimers();
});

// ===========================================================================
// Auto-retry with backoff
// ===========================================================================

describe("defineAction retry { count, delay, factor }", () => {
  it("retries up to count times on retry-class errors then succeeds", async () => {
    let attempts = 0;
    const action = defineAction<{ id: string }, string>({
      name: "test.retry_recovers",
      retryable: "network",
      retry: { count: 2, delay: 100 },
      run: () => {
        attempts++;
        if (attempts < 3) throw new ActionError("flaky", { code: "network" });
        return Promise.resolve("ok");
      },
    });

    const promise = action.dispatch({ id: "x" });
    // Fast-forward through the two backoff windows: 100ms + 200ms.
    await vi.advanceTimersByTimeAsync(100);
    await vi.advanceTimersByTimeAsync(200);
    const result = await promise;
    expect(result).toBe("ok");
    expect(attempts).toBe(3);
  });

  it("returns null after exhausting retries", async () => {
    let attempts = 0;
    const action = defineAction<void, void>({
      name: "test.retry_exhausts",
      retryable: "network",
      retry: { count: 2, delay: 50 },
      error: false,
      run: () => {
        attempts++;
        return Promise.reject(new ActionError("permanent network fail", { code: "network" }));
      },
    });

    const promise = action.dispatch();
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(100);
    const result = await promise;
    expect(result).toBeNull();
    expect(attempts).toBe(3); // initial + 2 retries
  });

  it("does NOT retry non-retry-class errors even with retry config", async () => {
    let attempts = 0;
    const action = defineAction<void, void>({
      name: "test.retry_skips_4xx",
      retryable: "network",
      retry: { count: 3, delay: 50 },
      error: false,
      run: () => {
        attempts++;
        return Promise.reject(new ActionError("validation", { status: 422 }));
      },
    });

    await action.dispatch();
    expect(attempts).toBe(1);
  });

  it("respects exponential backoff factor (default 2)", async () => {
    const timestamps: number[] = [];
    const start = Date.now();
    const action = defineAction<void, void>({
      name: "test.retry_backoff",
      retryable: "network",
      retry: { count: 2, delay: 100 },
      error: false,
      run: () => {
        timestamps.push(Date.now() - start);
        return Promise.reject(new ActionError("fail", { code: "network" }));
      },
    });

    const promise = action.dispatch();
    await vi.advanceTimersByTimeAsync(100); // first retry after 100ms
    await vi.advanceTimersByTimeAsync(200); // second retry after 200ms
    await promise;
    expect(timestamps).toHaveLength(3);
    expect(timestamps[1]).toBe(100);
    expect(timestamps[2]).toBe(300); // 100 + 200
  });

  it("custom factor: 1 (linear backoff)", async () => {
    const timestamps: number[] = [];
    const start = Date.now();
    const action = defineAction<void, void>({
      name: "test.retry_linear",
      retryable: "network",
      retry: { count: 2, delay: 100, factor: 1 },
      error: false,
      run: () => {
        timestamps.push(Date.now() - start);
        return Promise.reject(new ActionError("fail", { code: "network" }));
      },
    });

    const promise = action.dispatch();
    await vi.advanceTimersByTimeAsync(100);
    await vi.advanceTimersByTimeAsync(100);
    await promise;
    expect(timestamps[1]).toBe(100);
    expect(timestamps[2]).toBe(200); // linear: 100 + 100
  });

  it("aborts retry chain if action.cancel() during backoff", async () => {
    let attempts = 0;
    const action = defineAction<void, void>({
      name: "test.retry_cancel_mid_backoff",
      retryable: "network",
      retry: { count: 5, delay: 1000 },
      error: false,
      run: () => {
        attempts++;
        return Promise.reject(new ActionError("fail", { code: "network" }));
      },
    });

    const promise = action.dispatch();
    await vi.advanceTimersByTimeAsync(50);  // first attempt failed, backoff scheduled
    action.cancel();
    await vi.advanceTimersByTimeAsync(2000);
    await promise;
    expect(attempts).toBe(1); // only the initial attempt; backoff aborted
  });

  it("retry: undefined still allows manual Retry button", async () => {
    // Without retry config, no auto-retry happens, but the toast's
    // Retry button (from `retryable: 'network'`) should still work.
    let attempts = 0;
    const action = defineAction<void, string>({
      name: "test.no_auto_retry",
      retryable: "network",
      run: () => {
        attempts++;
        return Promise.reject(new ActionError("fail", { code: "network" }));
      },
    });

    await action.dispatch();
    expect(attempts).toBe(1); // no auto retry without retry config
  });
});

// ===========================================================================
// Mutation scopes (serialization)
// ===========================================================================

describe("defineAction scope (serial dispatch)", () => {
  it("serializes dispatches sharing a static scope string", async () => {
    const log: string[] = [];
    const action = defineAction<{ tag: string }, string>({
      name: "test.scope_static",
      scope: "shared",
      run: async (args) => {
        log.push(`start:${args.tag}`);
        await new Promise<void>((r) => setTimeout(r, 50));
        log.push(`end:${args.tag}`);
        return args.tag;
      },
    });

    const p1 = action.dispatch({ tag: "A" });
    const p2 = action.dispatch({ tag: "B" });
    const p3 = action.dispatch({ tag: "C" });

    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2, p3]);

    expect(log).toEqual([
      "start:A", "end:A",
      "start:B", "end:B",
      "start:C", "end:C",
    ]);
  });

  it("scope as function: per-resource queues run in parallel", async () => {
    const log: string[] = [];
    const action = defineAction<{ repo: string; tag: string }, void>({
      name: "test.scope_function",
      scope: (args) => `repo:${args.repo}`,
      run: async (args) => {
        log.push(`start:${args.repo}-${args.tag}`);
        await new Promise<void>((r) => setTimeout(r, 50));
        log.push(`end:${args.repo}-${args.tag}`);
      },
    });

    // Two repo:A dispatches serialize. repo:B runs in parallel with them.
    const pA1 = action.dispatch({ repo: "A", tag: "1" });
    const pA2 = action.dispatch({ repo: "A", tag: "2" });
    const pB1 = action.dispatch({ repo: "B", tag: "1" });

    await vi.advanceTimersByTimeAsync(50); // A1 + B1 finish at ~50ms
    await vi.advanceTimersByTimeAsync(50); // A2 finishes at ~100ms
    await Promise.all([pA1, pA2, pB1]);

    // Verify ordering within scopes: A1 → A2 (serial), B1 alone.
    const aOnly = log.filter((s) => s.includes("A-"));
    expect(aOnly).toEqual(["start:A-1", "end:A-1", "start:A-2", "end:A-2"]);
    // B1 starts before A2 (since B has its own scope) but after A1 begins.
    const startA1Idx = log.indexOf("start:A-1");
    const startB1Idx = log.indexOf("start:B-1");
    const startA2Idx = log.indexOf("start:A-2");
    expect(startB1Idx).toBeLessThan(startA2Idx);
    expect(startA1Idx).toBeLessThan(startB1Idx);
  });

  it("scope queue continues after a failed dispatch", async () => {
    const log: string[] = [];
    const action = defineAction<{ tag: string; fail: boolean }, void>({
      name: "test.scope_after_fail",
      scope: "q",
      error: false,
      run: async (args) => {
        log.push(`run:${args.tag}`);
        if (args.fail) throw new ActionError("nope");
      },
    });

    const p1 = action.dispatch({ tag: "A", fail: true });
    const p2 = action.dispatch({ tag: "B", fail: false });
    await Promise.all([p1, p2]);
    expect(log).toEqual(["run:A", "run:B"]);
  });

  it("scope shared across multiple actions", async () => {
    const log: string[] = [];
    const a1 = defineAction<{ tag: string }, void>({
      name: "test.scope_share_1",
      scope: "common",
      run: async (args) => {
        log.push(`a1-start:${args.tag}`);
        await new Promise<void>((r) => setTimeout(r, 50));
        log.push(`a1-end:${args.tag}`);
      },
    });
    const a2 = defineAction<{ tag: string }, void>({
      name: "test.scope_share_2",
      scope: "common",
      run: async (args) => {
        log.push(`a2-start:${args.tag}`);
        await new Promise<void>((r) => setTimeout(r, 50));
        log.push(`a2-end:${args.tag}`);
      },
    });

    const p1 = a1.dispatch({ tag: "X" });
    const p2 = a2.dispatch({ tag: "Y" });
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);

    // a2.Y must wait for a1.X even though they're different actions,
    // because they share scope "common".
    expect(log).toEqual([
      "a1-start:X", "a1-end:X",
      "a2-start:Y", "a2-end:Y",
    ]);
  });
});

// ===========================================================================
// Per-dispatch callback overrides
// ===========================================================================

describe("DispatchOptions onSuccess / onError / onSettled", () => {
  it("onSuccess fires with result + args", async () => {
    const action = defineAction<{ x: number }, number>({
      name: "test.cb_success",
      run: (args) => Promise.resolve(args.x * 2),
    });
    const onSuccess = vi.fn();
    const onError = vi.fn();
    const onSettled = vi.fn();
    await action.dispatch({ x: 21 }, { onSuccess, onError, onSettled });
    expect(onSuccess).toHaveBeenCalledWith(42, { x: 21 });
    expect(onError).not.toHaveBeenCalled();
    expect(onSettled).toHaveBeenCalledWith({ x: 21 });
  });

  it("onError fires with error + args", async () => {
    const action = defineAction<{ tag: string }, void>({
      name: "test.cb_error",
      error: false,
      run: () => Promise.reject(new ActionError("nope", { status: 422 })),
    });
    const onSuccess = vi.fn();
    const onError = vi.fn();
    const onSettled = vi.fn();
    await action.dispatch({ tag: "z" }, { onSuccess, onError, onSettled });
    expect(onSuccess).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({ message: "nope", status: 422 }),
      { tag: "z" },
    );
    expect(onSettled).toHaveBeenCalledWith({ tag: "z" });
  });

  it("onSettled fires on cancellation; onSuccess/onError do NOT", async () => {
    const action = defineAction<void, void>({
      name: "test.cb_cancel",
      run: (_args, signal) =>
        new Promise<void>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("cancelled")));
        }),
    });
    const onSuccess = vi.fn();
    const onError = vi.fn();
    const onSettled = vi.fn();
    const promise = action.dispatch(undefined, { onSuccess, onError, onSettled });
    action.cancel();
    await promise;
    expect(onSuccess).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
    expect(onSettled).toHaveBeenCalledTimes(1);
  });

  it("callbacks fire in addition to action-level toast wiring", async () => {
    const { success: toastSuccess } = await import("../toast.js");
    const action = defineAction<void, string>({
      name: "test.cb_with_toast",
      success: "Saved",
      run: () => Promise.resolve("ok"),
    });
    const onSuccess = vi.fn();
    await action.dispatch(undefined, { onSuccess });
    expect(toastSuccess).toHaveBeenCalledWith("Saved");
    expect(onSuccess).toHaveBeenCalledWith("ok", undefined);
  });

  it("callbacks fire even when toast is silenced via opts.silent", async () => {
    const { success: toastSuccess } = await import("../toast.js");
    const action = defineAction<void, string>({
      name: "test.cb_silent",
      success: "Saved",
      run: () => Promise.resolve("ok"),
    });
    const onSuccess = vi.fn();
    await action.dispatch(undefined, { silent: true, onSuccess });
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(onSuccess).toHaveBeenCalledWith("ok", undefined);
  });
});
