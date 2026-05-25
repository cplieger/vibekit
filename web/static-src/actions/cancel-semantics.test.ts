// @vitest-environment happy-dom
// Cancel-semantics coverage: tests for paths not covered elsewhere.
// 1. Success-race cancel: run() resolves but signal already aborted.
// 2. Deduped caller: onError/onSuccess do NOT fire when original is cancelled.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import * as toast from "../toast.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

describe("success-race cancel: run() resolves but signal already aborted", () => {
  it("records 'cancelled' status (not 'success')", async () => {
    const action = defineAction<void, string>({
      name: "test.race_cancel_status",
      run: (_args, signal) =>
        new Promise<string>((resolve) => {
          signal.addEventListener("abort", () => {
            Promise.resolve().then(() => resolve("late-result"));
          });
        }),
    });
    const p = action.dispatch();
    action.cancel();
    const result = await p;
    expect(result).toBeNull();
    const log = recentLog();
    expect(log[log.length - 1]?.status).toBe("cancelled");
  });

  it("calls rollback with code:'cancelled' (not success path)", async () => {
    const rollback = vi.fn();
    const action = defineAction<void, string>({
      name: "test.race_cancel_rollback",
      optimistic: () => ({ token: "x" }),
      rollback,
      run: (_args, signal) =>
        new Promise<string>((resolve) => {
          signal.addEventListener("abort", () => {
            Promise.resolve().then(() => resolve("late"));
          });
        }),
    });
    const p = action.dispatch();
    action.cancel();
    await p;
    expect(rollback).toHaveBeenCalledTimes(1);
    expect(rollback.mock.calls[0]![2]).toMatchObject({ code: "cancelled" });
  });

  it("fires onSettled but NOT onSuccess or onError", async () => {
    const onSuccess = vi.fn();
    const onError = vi.fn();
    const onSettled = vi.fn();
    const action = defineAction<void, string>({
      name: "test.race_cancel_callbacks",
      run: (_args, signal) =>
        new Promise<string>((resolve) => {
          signal.addEventListener("abort", () => {
            Promise.resolve().then(() => resolve("late"));
          });
        }),
    });
    const p = action.dispatch(undefined, { onSuccess, onError, onSettled });
    action.cancel();
    await p;
    expect(onSuccess).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
    expect(onSettled).toHaveBeenCalledTimes(1);
  });

  it("does NOT emit error toast", async () => {
    const action = defineAction<void, string>({
      name: "test.race_cancel_no_toast",
      error: "Should not appear",
      run: (_args, signal) =>
        new Promise<string>((resolve) => {
          signal.addEventListener("abort", () => {
            Promise.resolve().then(() => resolve("late"));
          });
        }),
    });
    const p = action.dispatch();
    action.cancel();
    await p;
    expect(toast.error).not.toHaveBeenCalled();
    expect(toast.success).not.toHaveBeenCalled();
  });
});

describe("deduped caller: cancel semantics", () => {
  it("deduped caller's onError/onSuccess do NOT fire when original is cancelled", async () => {
    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe_cancel_no_onError",
      dedupe: true,
      run: (_args, signal) =>
        new Promise<string>((_resolve, reject) => {
          signal.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    });

    const onSuccess1 = vi.fn();
    const onSuccess2 = vi.fn();
    const onError1 = vi.fn();
    const onError2 = vi.fn();
    const onSettled1 = vi.fn();
    const onSettled2 = vi.fn();

    const p1 = action.dispatch({ id: "a" }, { onSuccess: onSuccess1, onError: onError1, onSettled: onSettled1 });
    const p2 = action.dispatch({ id: "a" }, { onSuccess: onSuccess2, onError: onError2, onSettled: onSettled2 });

    action.cancel();
    await Promise.all([p1, p2]);

    // Neither caller's onError or onSuccess should fire — cancellation is neither.
    expect(onSuccess1).not.toHaveBeenCalled();
    expect(onSuccess2).not.toHaveBeenCalled();
    expect(onError1).not.toHaveBeenCalled();
    expect(onError2).not.toHaveBeenCalled();
    // Both onSettled should fire.
    expect(onSettled1).toHaveBeenCalledTimes(1);
    expect(onSettled2).toHaveBeenCalledTimes(1);
  });

  it("deduped caller resolves null when original is cancelled", async () => {
    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe_cancel_null",
      dedupe: true,
      run: (_args, signal) =>
        new Promise<string>((_resolve, reject) => {
          signal.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    });

    const p1 = action.dispatch({ id: "a" });
    const p2 = action.dispatch({ id: "a" });
    action.cancel();
    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1).toBeNull();
    expect(r2).toBeNull();
  });
});

describe("cancel on idle action (no in-flight instances)", () => {
  it("does not throw when called with nothing in-flight", () => {
    const action = defineAction<void, string>({
      name: "test.cancel_idle",
      run: async () => "ok",
    });
    // Should be a no-op, not throw.
    expect(() => action.cancel()).not.toThrow();
  });

  it("subsequent dispatch still works after cancel on idle", async () => {
    const action = defineAction<void, string>({
      name: "test.cancel_then_dispatch",
      run: async () => "result",
    });
    action.cancel();
    const result = await action.dispatch();
    expect(result).toBe("result");
  });
});
