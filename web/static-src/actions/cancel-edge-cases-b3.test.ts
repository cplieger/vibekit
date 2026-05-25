// @vitest-environment happy-dom
// Cancel handling edge cases — Batch 3 (C16S1B2):
// 1. Cancel of non-scoped dispatch before first await in runOnce
// 2. Cancel + dispatchWithResult + scope-queued (combined path)
// 3. Cancel during optimistic that partially mutates (rollback correctness)
// 4. Cancel + dedupe: re-dispatch from deduped caller's onCancel
// 5. Cancel + retry: signal.aborted checked at runWithRetry loop top
// 6. Cancel of multiple actions sharing the same scope key
// 7. Cancel + onRollback not fired when optimistic is undefined
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, dispatchWithResult, _resetForTest as resetDefine, _internalsForTest } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { ActionError } from "./error.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

describe("cancel of non-scoped dispatch before first await", () => {
  it("synchronous cancel after dispatch resolves as cancelled", async () => {
    const onCancel = vi.fn();
    const onSettled = vi.fn();
    let runReached = false;

    const action = defineAction<void, string>({
      name: "test.sync_cancel_no_scope",
      run: async (_args, signal) => {
        runReached = true;
        await Promise.resolve();
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "done";
      },
    });

    const p = action.dispatch(undefined, { onCancel, onSettled });
    action.cancel();
    await p;

    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onSettled).toHaveBeenCalledTimes(1);
    // run() starts synchronously (before first await), signal aborts during await
    expect(runReached).toBe(true);
  });

  it("signal.aborted is true inside run when cancel fires before first yield", async () => {
    let signalAborted: boolean | undefined;

    const action = defineAction<void, string>({
      name: "test.signal_state_sync_cancel",
      run: async (_args, signal) => {
        await Promise.resolve();
        signalAborted = signal.aborted;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "ok";
      },
    });

    const p = action.dispatch(undefined);
    action.cancel();
    await p;

    expect(signalAborted).toBe(true);
  });
});

describe("cancel + dispatchWithResult + scope-queued", () => {
  it("dispatchWithResult returns cancelled for scope-queued cancel", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const blocker = defineAction<void, string>({
      name: "test.dwr_scope_blocker",
      scope: "dwr",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<void, string>({
      name: "test.dwr_scope_cancel",
      scope: "dwr",
      run: async () => "should-not-run",
    });

    const pBlock = blocker.dispatch();
    const resultP = dispatchWithResult(action, undefined);

    action.cancel();
    resolve1();
    await pBlock;
    const result = await resultP;

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.cancelled).toBe(true);
      expect(result.error.code).toBe("cancelled");
    }
  });

  it("dispatchWithResult user onCancel fires alongside framework capture", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });
    const userOnCancel = vi.fn();

    const blocker = defineAction<void, string>({
      name: "test.dwr_scope_user_cb_blocker",
      scope: "dwr2",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<void, string>({
      name: "test.dwr_scope_user_cb",
      scope: "dwr2",
      run: async () => "should-not-run",
    });

    const pBlock = blocker.dispatch();
    const resultP = dispatchWithResult(action, undefined, { onCancel: userOnCancel });

    action.cancel();
    resolve1();
    await pBlock;
    const result = await resultP;

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.cancelled).toBe(true);
    expect(userOnCancel).toHaveBeenCalledTimes(1);
  });
});

describe("cancel during partial optimistic mutation", () => {
  it("rollback receives full op even if cancel fires mid-run after optimistic", async () => {
    const state = { a: 0, b: 0 };
    const action = defineAction<void, string, { prevA: number; prevB: number }>({
      name: "test.partial_opt_cancel",
      optimistic: () => {
        const prev = { prevA: state.a, prevB: state.b };
        state.a = 10;
        state.b = 20;
        return prev;
      },
      rollback: (_args, op) => {
        if (op) { state.a = op.prevA; state.b = op.prevB; }
      },
      run: async (_args, signal) => {
        await Promise.resolve();
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "ok";
      },
    });

    const p = action.dispatch(undefined);
    // Optimistic already applied
    expect(state).toEqual({ a: 10, b: 20 });
    action.cancel();
    await p;
    // Rollback restored
    expect(state).toEqual({ a: 0, b: 0 });
  });
});

describe("cancel + dedupe: re-dispatch from deduped caller onCancel", () => {
  it("deduped caller can re-dispatch from onCancel and get fresh result", async () => {
    let runCount = 0;
    let redispatchResult: string | null = null;
    let redispatchP: Promise<string | null> | undefined;

    const action = defineAction<string, string>({
      name: "test.dedupe_redispatch_from_deduped_oncancel",
      dedupe: true,
      run: async (_args, signal) => {
        runCount++;
        await Promise.resolve();
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return `run-${runCount}`;
      },
    });

    const p1 = action.dispatch("x");
    const p2 = action.dispatch("x", {
      onCancel: () => {
        redispatchP = action.dispatch("x");
        void redispatchP.then((r) => { redispatchResult = r; });
      },
    });

    action.cancel();
    await Promise.all([p1, p2]);
    await redispatchP;

    expect(redispatchResult).toBe("run-2");
    expect(runCount).toBe(2);
  });
});

describe("cancel + retry: signal checked at loop top", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("cancel between retry attempts (after sleep resolves) prevents next run", async () => {
    let attempts = 0;
    const onCancel = vi.fn();
    let actionRef: ReturnType<typeof defineAction<void, string>>;

    const action = defineAction<void, string>({
      name: "test.cancel_between_retries",
      retryable: "always",
      retry: { count: 5, delay: 100 },
      error: false,
      run: async (_args, signal) => {
        attempts++;
        if (attempts === 1) throw new ActionError("transient", { code: "network" });
        // Second attempt: check signal
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "ok";
      },
    });
    actionRef = action;

    const p = action.dispatch(undefined, { onCancel });

    // First attempt fails
    await vi.advanceTimersByTimeAsync(0);
    expect(attempts).toBe(1);

    // Advance past backoff — second attempt starts, but we cancel before it
    // Actually cancel during the sleep
    await vi.advanceTimersByTimeAsync(50);
    actionRef.cancel();
    await vi.advanceTimersByTimeAsync(100);
    await p;

    expect(onCancel).toHaveBeenCalledTimes(1);
    // At most 1 attempt (cancel during sleep prevents second)
    expect(attempts).toBe(1);
  });
});

describe("cancel of multiple actions sharing same scope key", () => {
  it("cancelling one action does not affect another action in same scope", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const actionA = defineAction<void, string>({
      name: "test.shared_scope_a",
      scope: "shared",
      run: async (_args, signal) => {
        await gate;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "a-done";
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.shared_scope_b",
      scope: "shared",
      run: async () => "b-done",
    });

    const pA = actionA.dispatch();
    const pB = actionB.dispatch();

    // Cancel only actionA
    actionA.cancel();
    resolve1();
    const rA = await pA;
    const rB = await pB;

    expect(rA).toBeNull(); // cancelled
    expect(rB).toBe("b-done"); // succeeds after A's slot clears
  });

  it("cancelling both actions in same scope cleans up scope chain", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const actionA = defineAction<void, string>({
      name: "test.shared_scope_both_a",
      scope: "shared2",
      run: async (_args, signal) => {
        await gate;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "a";
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.shared_scope_both_b",
      scope: "shared2",
      run: async () => "b",
    });

    const pA = actionA.dispatch();
    const pB = actionB.dispatch();

    actionA.cancel();
    actionB.cancel();
    resolve1();
    await Promise.all([pA, pB]);
    // Extra ticks for scope chain cleanup
    for (let i = 0; i < 5; i++) await Promise.resolve();

    const internals = _internalsForTest();
    expect(internals.scopeChains).toBe(0);
  });
});

describe("cancel + onRollback not fired when optimistic undefined", () => {
  it("onRollback does NOT fire on cancel when no optimistic defined", async () => {
    const onRollback = vi.fn();
    const rollback = vi.fn();

    const action = defineAction<void, string>({
      name: "test.no_onrollback_no_opt_cancel",
      rollback,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const p = action.dispatch(undefined, { onRollback });
    action.cancel();
    await p;

    // rollback() itself fires (it's defined)
    expect(rollback).toHaveBeenCalledTimes(1);
    // But onRollback callback does NOT fire (no optimistic to undo)
    expect(onRollback).not.toHaveBeenCalled();
  });

  it("onRollback DOES fire on cancel when optimistic IS defined", async () => {
    const onRollback = vi.fn();

    const action = defineAction<void, string, string>({
      name: "test.onrollback_with_opt_cancel",
      optimistic: () => "snapshot",
      rollback: () => {},
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const p = action.dispatch(undefined, { onRollback });
    action.cancel();
    await p;

    expect(onRollback).toHaveBeenCalledTimes(1);
    expect(onRollback.mock.calls[0]![0]).toMatchObject({ code: "cancelled" });
  });
});

describe("cancel + scope chain: no stale references after full cancel cycle", () => {
  it("scopeChains and activeDedupes are clean after cancel + re-dispatch cycle", async () => {
    const action = defineAction<string, string>({
      name: "test.full_cancel_cycle_clean",
      scope: "cycle",
      dedupe: true,
      run: async (arg, signal) => {
        await Promise.resolve();
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return `ok-${arg}`;
      },
    });

    // First dispatch + cancel
    const p1 = action.dispatch("a");
    action.cancel();
    await p1;

    // Second dispatch succeeds
    const r2 = await action.dispatch("a");
    expect(r2).toBe("ok-a");

    // Third dispatch + cancel
    const p3 = action.dispatch("a");
    action.cancel();
    await p3;

    // Allow cleanup
    await Promise.resolve();
    await Promise.resolve();

    const internals = _internalsForTest();
    expect(internals.scopeChains).toBe(0);
    expect(internals.activeDedupes).toBe(0);
  });
});

describe("cancel timing: registry records correct timestamps", () => {
  it("cancelled scope-queued instance has startedAt === completedAt", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const blocker = defineAction<void, string>({
      name: "test.cancel_ts_blocker",
      scope: "ts",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<void, string>({
      name: "test.cancel_ts_queued",
      scope: "ts",
      run: async () => "should-not-run",
    });

    const pBlock = blocker.dispatch();
    const p = action.dispatch();

    action.cancel();
    resolve1();
    await pBlock;
    await p;

    const log = recentLog();
    const entry = log.find((e) => e.name === "test.cancel_ts_queued");
    expect(entry).toBeDefined();
    expect(entry!.status).toBe("cancelled");
    expect(entry!.startedAt).toBe(entry!.completedAt);
    expect(entry!.dispatchedAt).toBeLessThanOrEqual(entry!.startedAt);
  });
});
