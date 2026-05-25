// Cycle 17 Stage 1: completeness gaps — onRollback after retry exhaustion,
// dispatchWithResult opts passthrough (onSettled, onError, onCancel).
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { defineAction, dispatchWithResult, _resetForTest as resetDefine } from "./define.js";
import { ActionError } from "./error.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
});

describe("onRollback fires after retry exhaustion", () => {
  it("fires onRollback with the final error after all retries fail", async () => {
    const onRollback = vi.fn();
    const rollback = vi.fn();
    const action = defineAction<string, string, string>({
      name: "test.rollback_retry_exhaust",
      optimistic: () => "snapshot",
      rollback,
      retryable: "always",
      retry: { count: 2, delay: 0 },
      error: false,
      run: async () => { throw new ActionError("persistent", { status: 503, code: "network" }); },
    });
    await action.dispatch("arg", { onRollback });
    expect(rollback).toHaveBeenCalledTimes(1);
    expect(onRollback).toHaveBeenCalledTimes(1);
    expect(onRollback.mock.calls[0]![0].message).toBe("persistent");
  });

  it("does NOT fire onRollback when retry succeeds (no rollback needed)", async () => {
    const onRollback = vi.fn();
    let attempts = 0;
    const action = defineAction<string, string, string>({
      name: "test.rollback_retry_success",
      optimistic: () => "snap",
      rollback: () => {},
      retryable: "always",
      retry: { count: 2, delay: 0 },
      error: false,
      run: async () => {
        attempts++;
        if (attempts === 1) throw new ActionError("transient", { code: "network" });
        return "ok";
      },
    });
    const result = await action.dispatch("arg", { onRollback });
    expect(result).toBe("ok");
    expect(onRollback).not.toHaveBeenCalled();
  });

  it("onRetryExhausted fires before onRollback", async () => {
    const order: string[] = [];
    const action = defineAction<string, string, string>({
      name: "test.exhaust_before_rollback",
      optimistic: () => "snap",
      rollback: () => { order.push("rollback"); },
      retryable: "always",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => { throw new ActionError("fail", { code: "network" }); },
    });
    await action.dispatch("arg", {
      onRetryExhausted: () => { order.push("exhausted"); },
      onRollback: () => { order.push("onRollback"); },
      onError: () => { order.push("onError"); },
      onSettled: () => { order.push("onSettled"); },
    });
    expect(order).toEqual(["exhausted", "rollback", "onRollback", "onError", "onSettled"]);
  });
});

describe("dispatchWithResult opts passthrough", () => {
  it("passes onSettled through to dispatch", async () => {
    const onSettled = vi.fn();
    const action = defineAction({ name: "test.dwr_settled", run: async () => "ok" });
    await dispatchWithResult(action, undefined, { onSettled });
    expect(onSettled).toHaveBeenCalledTimes(1);
  });

  it("passes onError through and still captures result", async () => {
    const onError = vi.fn();
    const action = defineAction({
      name: "test.dwr_onerror",
      error: false,
      run: async () => { throw new ActionError("boom", { status: 500 }); },
    });
    const r = await dispatchWithResult(action, undefined, { onError });
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error.message).toBe("boom");
    expect(onError).toHaveBeenCalledTimes(1);
  });

  it("passes onCancel through and still captures cancelled", async () => {
    const onCancel = vi.fn();
    const action = defineAction({
      name: "test.dwr_oncancel",
      run: (_x, signal) => new Promise<string>((_, rej) => {
        signal.addEventListener("abort", () => rej(new Error("aborted")));
      }),
    });
    const p = dispatchWithResult(action, undefined, { onCancel });
    action.cancel();
    const r = await p;
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.cancelled).toBe(true);
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("passes onRetryAttempt through", async () => {
    const onRetryAttempt = vi.fn();
    let attempts = 0;
    const action = defineAction({
      name: "test.dwr_retry_attempt",
      retryable: "always",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        attempts++;
        if (attempts === 1) throw new ActionError("blip", { code: "network" });
        return "ok";
      },
    });
    const r = await dispatchWithResult(action, undefined, { onRetryAttempt });
    expect(r.ok).toBe(true);
    expect(onRetryAttempt).toHaveBeenCalledTimes(1);
  });

  it("passes onRollback through", async () => {
    const onRollback = vi.fn();
    const action = defineAction<undefined, string, string>({
      name: "test.dwr_rollback",
      optimistic: () => "snap",
      rollback: () => {},
      error: false,
      run: async () => { throw new ActionError("fail", { status: 500 }); },
    });
    const r = await dispatchWithResult(action, undefined, { onRollback });
    expect(r.ok).toBe(false);
    expect(onRollback).toHaveBeenCalledTimes(1);
  });
});
