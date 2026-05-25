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

  it("fires onCancel and onSettled but NOT onSuccess or onError", async () => {
    const onSuccess = vi.fn();
    const onError = vi.fn();
    const onCancel = vi.fn();
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
    const p = action.dispatch(undefined, { onSuccess, onError, onCancel, onSettled });
    action.cancel();
    await p;
    expect(onSuccess).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
    expect(onCancel).toHaveBeenCalledTimes(1);
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

describe("deduped onCancel — scoped fast-path abort", () => {
  it("deduped caller fires onCancel when original is cancelled via scope-queue fast-path", async () => {
    let resolve1!: () => void;
    const gate1 = new Promise<void>((r) => { resolve1 = r; });

    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe_scope_fastpath_cancel",
      dedupe: true,
      scope: "s",
      run: async (_args, signal) => {
        await gate1;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "done";
      },
    });

    const onCancel1 = vi.fn();
    const onCancel2 = vi.fn();
    const onError1 = vi.fn();
    const onError2 = vi.fn();
    const onSettled1 = vi.fn();
    const onSettled2 = vi.fn();

    // First dispatch starts running (holds the scope)
    const p1 = action.dispatch({ id: "a" }, { onCancel: onCancel1, onError: onError1, onSettled: onSettled1 });
    // Second dispatch dedupes onto first
    const p2 = action.dispatch({ id: "a" }, { onCancel: onCancel2, onError: onError2, onSettled: onSettled2 });

    // Cancel while first is in-flight
    action.cancel();
    resolve1();
    await Promise.all([p1, p2]);

    expect(onCancel1).toHaveBeenCalledTimes(1);
    expect(onCancel2).toHaveBeenCalledTimes(1);
    expect(onError1).not.toHaveBeenCalled();
    expect(onError2).not.toHaveBeenCalled();
    expect(onSettled1).toHaveBeenCalledTimes(1);
    expect(onSettled2).toHaveBeenCalledTimes(1);
  });

  it("deduped caller does NOT fire onCancel when original errors", async () => {
    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe_error_no_onCancel",
      dedupe: true,
      run: async () => { throw new Error("server error"); },
    });

    const onCancel = vi.fn();
    const onError = vi.fn();
    const onSettled = vi.fn();

    const p1 = action.dispatch({ id: "a" });
    const p2 = action.dispatch({ id: "a" }, { onCancel, onError, onSettled });
    await Promise.all([p1, p2]);

    expect(onCancel).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onSettled).toHaveBeenCalledTimes(1);
  });

  it("deduped caller fires onCancel on success-race cancel (run resolves but signal aborted)", async () => {
    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe_success_race_cancel",
      dedupe: true,
      run: (_args, signal) =>
        new Promise<string>((resolve) => {
          signal.addEventListener("abort", () => {
            // Resolve AFTER abort — simulates success-race
            Promise.resolve().then(() => resolve("late-result"));
          });
        }),
    });

    const onCancel1 = vi.fn();
    const onCancel2 = vi.fn();
    const onSuccess2 = vi.fn();
    const onError2 = vi.fn();
    const onSettled2 = vi.fn();

    const p1 = action.dispatch({ id: "a" }, { onCancel: onCancel1 });
    const p2 = action.dispatch({ id: "a" }, { onCancel: onCancel2, onSuccess: onSuccess2, onError: onError2, onSettled: onSettled2 });

    action.cancel();
    const [r1, r2] = await Promise.all([p1, p2]);

    expect(r1).toBeNull();
    expect(r2).toBeNull();
    expect(onCancel1).toHaveBeenCalledTimes(1);
    expect(onCancel2).toHaveBeenCalledTimes(1);
    expect(onSuccess2).not.toHaveBeenCalled();
    expect(onError2).not.toHaveBeenCalled();
    expect(onSettled2).toHaveBeenCalledTimes(1);
  });
});

describe("fast-path post-optimistic abort", () => {
  it("optimistic does NOT run when signal is already aborted (scope-queued cancel)", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });
    const rollback = vi.fn();
    let optimisticCalled = false;
    let runCalled = false;

    const blocker = defineAction<void, string>({
      name: "test.fastpath_blocker",
      scope: "s",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<void, string>({
      name: "test.fastpath_post_optimistic",
      scope: "s",
      optimistic: () => { optimisticCalled = true; return { token: "opt" }; },
      rollback,
      run: async (_args, signal) => {
        runCalled = true;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "ok";
      },
    });

    // First dispatch holds the scope chain
    const pBlock = blocker.dispatch();

    // Second dispatch queued behind blocker in scope "s"
    const p = action.dispatch();

    // Cancel the action while it's still queued
    action.cancel();

    // Release the blocker so the scope chain advances
    resolve1();
    await pBlock;
    const result = await p;

    expect(result).toBeNull();
    // Fast-path abort: neither optimistic nor run should execute
    expect(optimisticCalled).toBe(false);
    expect(runCalled).toBe(false);
    expect(rollback).not.toHaveBeenCalled();

    const log = recentLog();
    const entry = log.find((e) => e.name === "test.fastpath_post_optimistic");
    expect(entry?.status).toBe("cancelled");
  });

  it("deduped caller gets onCancel when original hits fast-path abort", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const blocker = defineAction<void, string>({
      name: "test.dedupe_fastpath_blocker",
      scope: "s",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe_fastpath_cancel",
      dedupe: true,
      scope: "s",
      run: async () => "should-not-run",
    });

    const pBlock = blocker.dispatch();

    const onCancel1 = vi.fn();
    const onCancel2 = vi.fn();
    const onSettled1 = vi.fn();
    const onSettled2 = vi.fn();

    const p1 = action.dispatch({ id: "x" }, { onCancel: onCancel1, onSettled: onSettled1 });
    const p2 = action.dispatch({ id: "x" }, { onCancel: onCancel2, onSettled: onSettled2 });

    // Cancel while queued
    action.cancel();
    resolve1();
    await pBlock;
    await Promise.all([p1, p2]);

    expect(onCancel1).toHaveBeenCalledTimes(1);
    expect(onCancel2).toHaveBeenCalledTimes(1);
    expect(onSettled1).toHaveBeenCalledTimes(1);
    expect(onSettled2).toHaveBeenCalledTimes(1);
  });
});
