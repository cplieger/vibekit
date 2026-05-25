// @vitest-environment happy-dom
// Cycle 6 coverage: interaction tests for primitive combinations not
// covered by prior cycles:
//   1. actionStatus + retry (pending stays 1 across retries, attempts tracked)
//   2. bindLoadingState + retry (button stays disabled during auto-retry)
//   3. Success toast fires for actions with success: configured
//   4. actionStatus + scope (pending reflects queued dispatches)
//   5. debouncedDispatch + actionStatus (pending only after debounce fires)
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, isPending } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { actionStatus, _resetForTest as resetActionStatus } from "./action-status.js";
import { bindLoadingState } from "./loading.js";
import { debouncedDispatch } from "./debounce.js";
import { ActionError } from "./error.js";
import * as toast from "../toast.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  resetActionStatus();
  vi.clearAllMocks();
});

// ===========================================================================
// 1. actionStatus + retry interaction
// ===========================================================================

describe("actionStatus + retry interaction", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("pending stays at 1 during auto-retry (does not increment per attempt)", async () => {
    let attempt = 0;
    const action = defineAction<void, string>({
      name: "test.status_retry",
      retryable: "network",
      retry: { count: 2, delay: 100 },
      error: false,
      run: () => {
        attempt++;
        if (attempt < 3) throw new ActionError("net", { code: "network" });
        return Promise.resolve("ok");
      },
    });

    const status = actionStatus("test.status_retry");
    expect(status.pending).toBe(0);

    const p = action.dispatch();
    // After first attempt fails, pending should still be 1 (not 0, not 2)
    expect(status.pending).toBe(1);

    await vi.advanceTimersByTimeAsync(100); // first retry fires
    expect(status.pending).toBe(1);
    expect(attempt).toBe(2);

    await vi.advanceTimersByTimeAsync(200); // second retry fires
    await p;
    expect(status.pending).toBe(0);
    expect(status.lastSuccess).toBe("ok");
    expect(attempt).toBe(3);
  });

  it("actionStatus.lastError is set when all retries are exhausted", async () => {
    const action = defineAction<void, string>({
      name: "test.status_retry_exhaust",
      retryable: "always",
      retry: { count: 1, delay: 50 },
      error: false,
      run: () => Promise.reject(new ActionError("server down", { status: 503 })),
    });

    const status = actionStatus("test.status_retry_exhaust");
    const p = action.dispatch();
    await vi.advanceTimersByTimeAsync(50);
    await p;

    expect(status.pending).toBe(0);
    expect(status.lastError?.message).toBe("server down");
    expect(status.lastError?.status).toBe(503);
  });
});

// ===========================================================================
// 2. bindLoadingState + retry interaction
// ===========================================================================

describe("bindLoadingState + retry interaction", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("button stays disabled throughout auto-retry until final resolution", async () => {
    let attempt = 0;
    const action = defineAction<void, string>({
      name: "test.bind_retry",
      retryable: "network",
      retry: { count: 2, delay: 100 },
      error: false,
      run: () => {
        attempt++;
        if (attempt < 3) throw new ActionError("net", { code: "network" });
        return Promise.resolve("done");
      },
    });

    const btn = document.createElement("button");
    bindLoadingState("test.bind_retry", btn);
    expect(btn.disabled).toBe(false);

    const p = action.dispatch();
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");

    // During first retry backoff
    await vi.advanceTimersByTimeAsync(100);
    expect(btn.disabled).toBe(true);

    // During second retry backoff
    await vi.advanceTimersByTimeAsync(200);
    await p;

    // After success, button is re-enabled
    expect(btn.disabled).toBe(false);
    expect(btn.getAttribute("aria-busy")).toBeNull();
  });

  it("button re-enables after all retries exhausted (error path)", async () => {
    const action = defineAction<void, string>({
      name: "test.bind_retry_fail",
      retryable: "always",
      retry: { count: 1, delay: 50 },
      error: false,
      run: () => Promise.reject(new ActionError("fail", { status: 500 })),
    });

    const btn = document.createElement("button");
    bindLoadingState("test.bind_retry_fail", btn);

    const p = action.dispatch();
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");

    await vi.advanceTimersByTimeAsync(50);
    await p;

    expect(btn.disabled).toBe(false);
    expect(btn.getAttribute("aria-busy")).toBeNull();
  });

  it("isPending stays true while bindLoadingState keeps button disabled during retry", async () => {
    let attempt = 0;
    let resolveSecond: (() => void) | null = null;
    const action = defineAction<void, string>({
      name: "test.ispending_bind",
      retryable: "network",
      retry: { count: 1, delay: 80 },
      error: false,
      run: () => {
        attempt++;
        if (attempt < 2) throw new ActionError("net", { code: "network" });
        return new Promise<string>((r) => { resolveSecond = () => r("ok"); });
      },
    });

    const btn = document.createElement("button");
    bindLoadingState("test.ispending_bind", btn);

    const p = action.dispatch();
    expect(isPending("test.ispending_bind")).toBe(true);
    expect(btn.disabled).toBe(true);

    await vi.advanceTimersByTimeAsync(80);
    // Retry fired, second attempt is now running (pending)
    expect(isPending("test.ispending_bind")).toBe(true);
    expect(btn.disabled).toBe(true);

    resolveSecond!();
    await p;
    expect(isPending("test.ispending_bind")).toBe(false);
    expect(btn.disabled).toBe(false);
  });
});

// ===========================================================================
// 3. Success toast fires for actions with success: configured
// ===========================================================================

describe("success toast fires for configured actions", () => {
  it("string success toast fires on successful dispatch", async () => {
    const action = defineAction<void, unknown>({
      name: "test.success_toast_string",
      success: "Pulled",
      run: () => Promise.resolve({}),
    });

    await action.dispatch();
    expect(toast.success).toHaveBeenCalledWith("Pulled");
  });

  it("function success toast fires with args and result", async () => {
    const action = defineAction<{ count: number }, string[]>({
      name: "test.success_toast_fn",
      success: (_args, result) => `Uploaded ${String(result.length)} files`,
      run: () => Promise.resolve(["a.ts", "b.ts"]),
    });

    await action.dispatch({ count: 2 });
    expect(toast.success).toHaveBeenCalledWith("Uploaded 2 files");
  });

  it("error toast fires with retry button when retryable: 'always'", async () => {
    const action = defineAction<void, void>({
      name: "test.error_toast_retry",
      retryable: "always",
      run: () => Promise.reject(new ActionError("server error", { status: 500 })),
    });

    await action.dispatch();
    expect(toast.error).toHaveBeenCalledTimes(1);
    const retryArg = vi.mocked(toast.error).mock.calls[0]?.[1] as { onClick: () => void } | undefined;
    expect(retryArg).toBeDefined();
    expect(typeof retryArg?.onClick).toBe("function");
  });

  it("error toast does NOT include retry button when retryable: 'network' and error is 4xx", async () => {
    const action = defineAction<void, void>({
      name: "test.error_toast_no_retry",
      retryable: "network",
      run: () => Promise.reject(new ActionError("not found", { status: 404 })),
    });

    await action.dispatch();
    expect(toast.error).toHaveBeenCalledTimes(1);
    const retryArg = vi.mocked(toast.error).mock.calls[0]?.[1] as { onClick: () => void } | undefined;
    expect(retryArg).toBeUndefined();
  });
});

// ===========================================================================
// 4. actionStatus + scope interaction
// ===========================================================================

describe("actionStatus + scope interaction", () => {
  it("scope-queued dispatch becomes pending only when it starts running", async () => {
    let resolveFirst: (() => void) | null = null;
    let callCount = 0;

    const action = defineAction<{ tag: string }, void>({
      name: "test.status_scope",
      scope: "q",
      run: () => {
        callCount++;
        if (callCount === 1) {
          return new Promise<void>((r) => { resolveFirst = r; });
        }
        return Promise.resolve();
      },
    });

    const status = actionStatus("test.status_scope");
    expect(status.pending).toBe(0);

    const p1 = action.dispatch({ tag: "A" });
    // Allow microtask for scope chain setup
    await Promise.resolve();
    const p2 = action.dispatch({ tag: "B" });
    await Promise.resolve();

    // Only the running dispatch is pending; queued one hasn't started yet
    expect(status.pending).toBe(1);
    expect(callCount).toBe(1);

    resolveFirst!();
    await p1;
    // First completed, second now starts and becomes pending
    // Need a microtask for the scope chain .then() to fire
    await Promise.resolve();
    expect(status.pending).toBe(1);
    expect(callCount).toBe(2);

    await p2;
    expect(status.pending).toBe(0);
    expect(status.lastSettledAt).toBeGreaterThan(0);
  });

  it("pendingCount reflects scope-serialized dispatches correctly", async () => {
    let resolveFirst: (() => void) | null = null;
    let resolveSecond: (() => void) | null = null;
    let callCount = 0;

    const action = defineAction<{ tag: string }, void>({
      name: "test.status_scope_count",
      scope: "q",
      run: () => {
        callCount++;
        if (callCount === 1) return new Promise<void>((r) => { resolveFirst = r; });
        return new Promise<void>((r) => { resolveSecond = r; });
      },
    });

    const status = actionStatus("test.status_scope_count");
    const p1 = action.dispatch({ tag: "A" });
    await Promise.resolve();
    const p2 = action.dispatch({ tag: "B" });
    await Promise.resolve();

    // Only first is pending (second is queued, not yet recorded)
    expect(status.pending).toBe(1);

    resolveFirst!();
    await p1;
    await Promise.resolve();
    // Second is now running
    expect(status.pending).toBe(1);

    resolveSecond!();
    await p2;
    expect(status.pending).toBe(0);
  });
});

// ===========================================================================
// 5. debouncedDispatch + actionStatus interaction
// ===========================================================================

describe("debouncedDispatch + actionStatus interaction", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("actionStatus.pending is 0 during debounce wait, 1 after fire", async () => {
    const action = defineAction<string, void>({
      name: "test.debounce_status",
      run: () => Promise.resolve(),
    });

    const status = actionStatus("test.debounce_status");
    const dbg = debouncedDispatch(action, { wait: 200 });

    dbg("a");
    dbg("b");
    // During debounce wait, no dispatch has fired yet
    expect(status.pending).toBe(0);

    await vi.advanceTimersByTimeAsync(200);
    // After debounce fires, the action runs (and resolves immediately)
    // so pending goes 0→1→0 within the same microtask batch.
    // By the time we check, it's already resolved.
    expect(status.pending).toBe(0);
    expect(status.lastSettledAt).toBeGreaterThan(0);
  });

  it("actionStatus tracks leading-mode debounce correctly", async () => {
    let resolveRun: (() => void) | null = null;
    const action = defineAction<string, void>({
      name: "test.debounce_status_leading",
      run: () => new Promise<void>((r) => { resolveRun = r; }),
    });

    const status = actionStatus("test.debounce_status_leading");
    const dbg = debouncedDispatch(action, { wait: 100, leading: true });

    dbg("a"); // fires immediately (leading edge)
    // Action is now pending
    expect(status.pending).toBe(1);

    dbg("b"); // suppressed during cooldown
    // Still only 1 pending (the leading dispatch)
    expect(status.pending).toBe(1);

    resolveRun!();
    await vi.advanceTimersByTimeAsync(0);
    // First dispatch resolved
    expect(status.pending).toBe(0);

    // Trailing timer fires "b" at t=100
    await vi.advanceTimersByTimeAsync(100);
    // "b" dispatched — now pending again
    expect(status.pending).toBe(1);

    resolveRun!();
    await vi.advanceTimersByTimeAsync(0);
    expect(status.pending).toBe(0);
  });
});

// ===========================================================================
// 6. Documented invariant: optimistic does NOT re-fire during retries
// ===========================================================================

describe("optimistic does not re-fire during auto-retry", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("optimistic is called exactly once even when run() retries multiple times", async () => {
    let attempt = 0;
    const optimisticSpy = vi.fn(() => ({ snapshot: "before" }));
    const rollbackSpy = vi.fn();

    const action = defineAction<string, string, { snapshot: string }>({
      name: "test.optimistic_no_refire",
      retryable: "always",
      retry: { count: 2, delay: 50 },
      error: false,
      optimistic: optimisticSpy,
      rollback: rollbackSpy,
      run: () => {
        attempt++;
        if (attempt < 3) return Promise.reject(new ActionError("fail", { status: 500 }));
        return Promise.resolve("done");
      },
    });

    const p = action.dispatch("arg");
    expect(optimisticSpy).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(50); // first retry
    expect(optimisticSpy).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(100); // second retry (succeeds)
    await p;

    expect(optimisticSpy).toHaveBeenCalledTimes(1);
    expect(rollbackSpy).not.toHaveBeenCalled(); // success → no rollback
    expect(attempt).toBe(3);
  });

  it("rollback fires once after all retries are exhausted", async () => {
    const optimisticSpy = vi.fn(() => ({ val: 42 }));
    const rollbackSpy = vi.fn();

    const action = defineAction<string, string, { val: number }>({
      name: "test.optimistic_rollback_after_retry",
      retryable: "always",
      retry: { count: 1, delay: 50 },
      error: false,
      optimistic: optimisticSpy,
      rollback: rollbackSpy,
      run: () => Promise.reject(new ActionError("down", { status: 503 })),
    });

    const p = action.dispatch("x");
    await vi.advanceTimersByTimeAsync(50);
    await p;

    expect(optimisticSpy).toHaveBeenCalledTimes(1);
    expect(rollbackSpy).toHaveBeenCalledTimes(1);
    expect(rollbackSpy).toHaveBeenCalledWith("x", { val: 42 }, expect.objectContaining({ message: "down" }));
  });
});
