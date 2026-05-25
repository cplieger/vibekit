// Cycle 15 Stage 1: missing primitives (isPending export, onceSettled,
// toActionError export, RETRY_AGGRESSIVE), naming, error quality
// (AggregateError handling in toActionError).
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { ActionError, toActionError } from "./error.js";
import { _resetForTest as resetRegistry, isPending, onceSettled } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { RETRY_AGGRESSIVE } from "./types.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
});

describe("isPending — exported primitive", () => {
  it("returns false when no action dispatched", () => {
    expect(isPending("nonexistent.action")).toBe(false);
  });

  it("returns true while action is in-flight", async () => {
    let resolve!: () => void;
    const gate = new Promise<void>((r) => { resolve = r; });
    const action = defineAction({
      name: "test.ispending_export",
      run: async () => { await gate; return "done"; },
    });
    const p = action.dispatch(undefined);
    expect(isPending("test.ispending_export")).toBe(true);
    resolve();
    await p;
    expect(isPending("test.ispending_export")).toBe(false);
  });
});

describe("onceSettled — one-shot settlement observer", () => {
  it("resolves on success", async () => {
    const action = defineAction({
      name: "test.once_settled_ok",
      run: async () => "result",
    });
    const settled = onceSettled("test.once_settled_ok");
    await action.dispatch(undefined);
    const inst = await settled;
    expect(inst.status).toBe("success");
    expect(inst.result).toBe("result");
  });

  it("resolves on error", async () => {
    const action = defineAction({
      name: "test.once_settled_err",
      error: false,
      run: async () => { throw new ActionError("boom", { status: 500 }); },
    });
    const settled = onceSettled("test.once_settled_err");
    await action.dispatch(undefined);
    const inst = await settled;
    expect(inst.status).toBe("error");
    expect(inst.error?.message).toBe("boom");
  });

  it("resolves on cancellation", async () => {
    const action = defineAction({
      name: "test.once_settled_cancel",
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const settled = onceSettled("test.once_settled_cancel");
    const p = action.dispatch(undefined);
    action.cancel();
    await p;
    const inst = await settled;
    expect(inst.status).toBe("cancelled");
  });

  it("does not resolve on pending (only terminal)", async () => {
    let resolve!: () => void;
    const gate = new Promise<void>((r) => { resolve = r; });
    const action = defineAction({
      name: "test.once_settled_pending",
      run: async () => { await gate; return "ok"; },
    });
    let resolved = false;
    const settled = onceSettled("test.once_settled_pending");
    void settled.then(() => { resolved = true; });
    action.dispatch(undefined);
    await Promise.resolve(); // flush microtasks
    await Promise.resolve();
    expect(resolved).toBe(false);
    resolve();
    await settled;
    expect(resolved).toBe(true);
  });

  it("auto-unsubscribes after first settlement", async () => {
    const action = defineAction({
      name: "test.once_settled_unsub",
      run: async () => "v",
    });
    const settled = onceSettled("test.once_settled_unsub");
    await action.dispatch(undefined);
    const inst = await settled;
    expect(inst.status).toBe("success");
    // Second dispatch should not affect the already-resolved promise
    await action.dispatch(undefined);
    // No assertion needed — just verifying no errors/hangs
  });
});

describe("toActionError — AggregateError handling", () => {
  it("extracts first child error message from AggregateError", () => {
    const inner1 = new Error("first failure");
    const inner2 = new Error("second failure");
    const agg = new AggregateError([inner1, inner2], "all failed");
    const result = toActionError(agg);
    expect(result.message).toBe("first failure");
    expect(result.code).toBe("aggregate");
    expect(result.cause).toBe(agg);
  });

  it("falls back to aggregate message when no child errors", () => {
    const agg = new AggregateError([], "empty aggregate");
    const result = toActionError(agg);
    expect(result.message).toBe("empty aggregate");
    expect(result.code).toBe("aggregate");
  });

  it("handles non-Error children in AggregateError", () => {
    const agg = new AggregateError(["string error", 42], "mixed");
    const result = toActionError(agg);
    // First child is not an Error, so falls back to aggregate message
    expect(result.message).toBe("mixed");
    expect(result.code).toBe("aggregate");
  });
});

describe("RETRY_AGGRESSIVE constant", () => {
  it("has expected shape", () => {
    expect(RETRY_AGGRESSIVE.count).toBe(3);
    expect(RETRY_AGGRESSIVE.delay).toBe(100);
    expect(RETRY_AGGRESSIVE.factor).toBeUndefined();
  });

  it("works with defineAction retry config", async () => {
    let attempts = 0;
    const action = defineAction({
      name: "test.retry_aggressive",
      retryable: "network",
      retry: RETRY_AGGRESSIVE,
      error: false,
      run: async () => {
        attempts++;
        if (attempts <= 3) throw new ActionError("transient", { code: "network" });
        return "recovered";
      },
    });
    const result = await action.dispatch(undefined);
    expect(result).toBe("recovered");
    expect(attempts).toBe(4); // 1 initial + 3 retries
  });
});
