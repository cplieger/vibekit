// @vitest-environment happy-dom
// Cancel handling edge cases — Batch 2 (C14S1B2):
// 1. Cancel race between optimistic() success and run() start
// 2. Cancel + dispatchWithResult discriminated union
// 3. Cancel of scope-queued instance: optimistic never fires
// 4. Multiple scope-queued instances cancelled: scope chain cleanup
// 5. Cancel + re-dispatch in same scope after cancel
// 6. Cancel with dedupe: deduped caller receives onCancel
// 7. Cancel during optimistic that mutates external state
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, dispatchWithResult, _resetForTest as resetDefine, _internalsForTest } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

describe("cancel race: signal aborts between optimistic and run", () => {
  it("rollback fires when cancel occurs after optimistic but before run resolves", async () => {
    const rollback = vi.fn();
    const optimisticOp = { snapshot: [1, 2, 3] };
    let runCalled = false;

    const action = defineAction<void, string>({
      name: "test.cancel_between_opt_run",
      optimistic: () => optimisticOp,
      rollback,
      run: async (_args, signal) => {
        runCalled = true;
        // Signal may not be aborted yet at entry (abort is sync but
        // run() starts in the same microtask as dispatch). The abort
        // fires during the await.
        await Promise.resolve();
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "done";
      },
    });

    const p = action.dispatch();
    // Cancel synchronously — optimistic already ran, run() started
    action.cancel();
    await p;

    expect(runCalled).toBe(true);
    expect(rollback).toHaveBeenCalledTimes(1);
    expect(rollback).toHaveBeenCalledWith(
      undefined,
      optimisticOp,
      expect.objectContaining({ code: "cancelled" }),
    );
  });

  it("optimistic mutation persists until rollback (not eagerly undone)", async () => {
    const state = { items: ["a", "b"] };
    const action = defineAction<string, string, number>({
      name: "test.cancel_opt_persists",
      optimistic: (item) => {
        state.items.push(item);
        return state.items.length - 1; // index to remove
      },
      rollback: (_args, idx) => {
        if (idx !== undefined) state.items.splice(idx, 1);
      },
      run: async (_args, signal) => {
        await Promise.resolve();
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "ok";
      },
    });

    const p = action.dispatch("c");
    // Optimistic already applied
    expect(state.items).toEqual(["a", "b", "c"]);
    action.cancel();
    await p;
    // Rollback undid the optimistic
    expect(state.items).toEqual(["a", "b"]);
  });
});

describe("cancel + dispatchWithResult", () => {
  it("returns { ok: false, cancelled: true } on cancellation", async () => {
    const action = defineAction<void, string>({
      name: "test.dispatch_result_cancel",
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const resultP = dispatchWithResult(action, undefined);
    action.cancel();
    const result = await resultP;

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.cancelled).toBe(true);
      expect(result.error.code).toBe("cancelled");
    }
  });

  it("returns { ok: true } on success (sanity)", async () => {
    const action = defineAction<void, string>({
      name: "test.dispatch_result_success",
      run: async () => "hello",
    });

    const result = await dispatchWithResult(action, undefined);
    expect(result.ok).toBe(true);
    if (result.ok) expect(result.value).toBe("hello");
  });

  it("fires user-provided onCancel alongside dispatchWithResult capture", async () => {
    const userOnCancel = vi.fn();
    const action = defineAction<void, string>({
      name: "test.dispatch_result_user_oncancel",
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const resultP = dispatchWithResult(action, undefined, { onCancel: userOnCancel });
    action.cancel();
    const result = await resultP;

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.cancelled).toBe(true);
    expect(userOnCancel).toHaveBeenCalledTimes(1);
  });
});

describe("cancel of scope-queued instance: optimistic never fires", () => {
  it("optimistic does NOT run for scope-queued instance cancelled before start", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });
    const optimistic = vi.fn(() => ({ tok: 1 }));
    const rollback = vi.fn();

    const blocker = defineAction<void, string>({
      name: "test.scope_opt_blocker",
      scope: "opt-scope",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<void, string>({
      name: "test.scope_opt_queued",
      scope: "opt-scope",
      optimistic,
      rollback,
      run: async () => "should-not-run",
    });

    const pBlock = blocker.dispatch();
    const p = action.dispatch();

    // Cancel while queued — optimistic should NOT have fired
    action.cancel();
    resolve1();
    await pBlock;
    await p;

    expect(optimistic).not.toHaveBeenCalled();
    expect(rollback).not.toHaveBeenCalled();
    const log = recentLog();
    const cancelled = log.find((e) => e.name === "test.scope_opt_queued");
    expect(cancelled?.status).toBe("cancelled");
  });
});

describe("multiple scope-queued instances cancelled: chain cleanup", () => {
  it("cancelling multiple queued instances does not leak scope chains", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const blocker = defineAction<void, string>({
      name: "test.multi_cancel_blocker",
      scope: "multi-cancel",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<number, string>({
      name: "test.multi_cancel_queued",
      scope: "multi-cancel",
      run: async (n) => `done-${n}`,
    });

    const pBlock = blocker.dispatch();
    const p1 = action.dispatch(1);
    const p2 = action.dispatch(2);
    const p3 = action.dispatch(3);

    // Cancel all queued instances
    action.cancel();
    resolve1();
    await pBlock;
    await Promise.all([p1, p2, p3]);
    // Extra ticks for deferred tail resolution (cross-action race fix)
    for (let i = 0; i < 5; i++) await Promise.resolve();

    // Verify no leaks
    const internals = _internalsForTest();
    expect(internals.scopeChains).toBe(0);
    expect(internals.activeDedupes).toBe(0);
    expect(action.isInflight).toBe(false);
  });

  it("each cancelled instance records to registry", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const blocker = defineAction<void, string>({
      name: "test.multi_cancel_reg_blocker",
      scope: "multi-reg",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<number, string>({
      name: "test.multi_cancel_reg",
      scope: "multi-reg",
      run: async (n) => `done-${n}`,
    });

    const pBlock = blocker.dispatch();
    const p1 = action.dispatch(1);
    const p2 = action.dispatch(2);

    action.cancel();
    resolve1();
    await pBlock;
    await Promise.all([p1, p2]);

    const log = recentLog();
    const cancelled = log.filter((e) => e.name === "test.multi_cancel_reg" && e.status === "cancelled");
    expect(cancelled.length).toBe(2);
  });
});

describe("cancel + re-dispatch in same scope after cancel", () => {
  it("new dispatch in same scope succeeds after cancel clears the chain", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const action = defineAction<string, string>({
      name: "test.redispatch_same_scope",
      scope: "redispatch",
      run: async (arg, signal) => {
        if (arg === "first") {
          await gate;
          if (signal.aborted) throw new DOMException("aborted", "AbortError");
        }
        return `result-${arg}`;
      },
    });

    const p1 = action.dispatch("first");
    action.cancel();
    resolve1();
    await p1;

    // New dispatch should not be blocked
    const r2 = await action.dispatch("second");
    expect(r2).toBe("result-second");
  });

  it("scope chain is clean after cancel (no stale tail blocking)", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const blocker = defineAction<void, string>({
      name: "test.scope_clean_blocker",
      scope: "clean",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<number, string>({
      name: "test.scope_clean_action",
      scope: "clean",
      run: async (n) => `ok-${n}`,
    });

    const pBlock = blocker.dispatch();
    const p1 = action.dispatch(1);
    action.cancel();
    resolve1();
    await pBlock;
    await p1;

    // Dispatch again — should not serialize behind the cancelled entry
    const r2 = await action.dispatch(2);
    expect(r2).toBe("ok-2");
  });
});

describe("cancel + dedupe: deduped caller receives onCancel", () => {
  it("deduped caller's onCancel fires when original is cancelled", async () => {
    const onCancel1 = vi.fn();
    const onCancel2 = vi.fn();
    const onError2 = vi.fn();

    const action = defineAction<string, string>({
      name: "test.dedupe_cancel_callback",
      dedupe: true,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const p1 = action.dispatch("x", { onCancel: onCancel1 });
    const p2 = action.dispatch("x", { onCancel: onCancel2, onError: onError2 });

    action.cancel();
    await Promise.all([p1, p2]);

    expect(onCancel1).toHaveBeenCalledTimes(1);
    expect(onCancel2).toHaveBeenCalledTimes(1);
    expect(onError2).not.toHaveBeenCalled();
  });

  it("deduped caller's onSettled fires on cancel", async () => {
    const onSettled1 = vi.fn();
    const onSettled2 = vi.fn();

    const action = defineAction<string, string>({
      name: "test.dedupe_cancel_settled",
      dedupe: true,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const p1 = action.dispatch("y", { onSettled: onSettled1 });
    const p2 = action.dispatch("y", { onSettled: onSettled2 });

    action.cancel();
    await Promise.all([p1, p2]);

    expect(onSettled1).toHaveBeenCalledTimes(1);
    expect(onSettled2).toHaveBeenCalledTimes(1);
  });
});

describe("cancel during optimistic that mutates external state", () => {
  it("rollback receives the optimistic op even when cancel is immediate", async () => {
    const mutations: string[] = [];
    const action = defineAction<string, string, number>({
      name: "test.cancel_opt_external",
      optimistic: (item) => {
        mutations.push(`add:${item}`);
        return mutations.length - 1;
      },
      rollback: (_args, idx) => {
        if (idx !== undefined) {
          mutations.push(`remove:${idx}`);
        }
      },
      run: async (_args, signal) => {
        await Promise.resolve();
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return "ok";
      },
    });

    const p = action.dispatch("item1");
    action.cancel();
    await p;

    expect(mutations).toEqual(["add:item1", "remove:0"]);
  });
});

describe("cancel + retry + scope combined: cancel during retry of scoped action", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("cancel during retry backoff of scoped action unblocks next in scope", async () => {
    let attempts = 0;
    const order: string[] = [];

    const action = defineAction<string, string>({
      name: "test.scope_retry_cancel",
      scope: "retry-scope",
      retryable: "always",
      retry: { count: 3, delay: 500 },
      error: false,
      run: async (arg) => {
        order.push(`run:${arg}`);
        attempts++;
        if (arg === "first") throw new Error("transient");
        return `ok-${arg}`;
      },
    });

    const p1 = action.dispatch("first");
    const p2 = action.dispatch("second");

    // First attempt of "first" fails, enters backoff
    await vi.advanceTimersByTimeAsync(0);
    expect(attempts).toBe(1);

    // Cancel during backoff
    action.cancel();
    await vi.advanceTimersByTimeAsync(600);
    await Promise.all([p1, p2]);

    // Both should be cancelled (second was scope-queued)
    const log = recentLog();
    const cancelled = log.filter((e) => e.status === "cancelled");
    expect(cancelled.length).toBe(2);
    expect(action.isInflight).toBe(false);
  });
});

describe("cancel idempotency: cancel on already-settled action", () => {
  it("cancel() after action already succeeded is a no-op", async () => {
    const action = defineAction<void, string>({
      name: "test.cancel_after_success",
      run: async () => "done",
    });

    const result = await action.dispatch();
    expect(result).toBe("done");
    expect(action.isInflight).toBe(false);

    // Should not throw
    action.cancel();
    expect(action.isInflight).toBe(false);
  });

  it("cancel() after action already errored is a no-op", async () => {
    const action = defineAction<void, string>({
      name: "test.cancel_after_error",
      error: false,
      run: async () => { throw new Error("fail"); },
    });

    await action.dispatch();
    expect(action.isInflight).toBe(false);

    action.cancel();
    expect(action.isInflight).toBe(false);
  });
});

describe("earlyCancel resolver robustness: dispatch promise resolves even if callback throws", () => {
  it("dispatch promise resolves with null even when onCancel throws in scope-queued cancel", async () => {
    const consoleErr = vi.spyOn(console, "error").mockImplementation(() => undefined);
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const blocker = defineAction<void, string>({
      name: "test.early_cancel_robust_blocker",
      scope: "robust",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<void, string>({
      name: "test.early_cancel_robust",
      scope: "robust",
      run: async () => "should-not-run",
    });

    const pBlock = blocker.dispatch();
    const p = action.dispatch(undefined, {
      onCancel: () => { throw new Error("onCancel exploded in earlyCancel path"); },
    });

    action.cancel();
    // Dispatch promise should resolve immediately (not hang)
    const result = await Promise.race([p, new Promise<"timeout">((r) => setTimeout(() => r("timeout"), 2000))]);
    expect(result).toBeNull();

    resolve1();
    await pBlock;
    expect(consoleErr).toHaveBeenCalled();
    consoleErr.mockRestore();
  });
});

describe("cancel from within onCancel: re-entrant cancel is safe", () => {
  it("re-entrant cancel() from onCancel does not throw or double-fire", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });
    const onCancel2 = vi.fn();

    const action = defineAction<number, string>({
      name: "test.reentrant_cancel",
      scope: "reentrant",
      run: async (n, signal) => {
        if (n === 1) { await gate; if (signal.aborted) throw new DOMException("aborted", "AbortError"); }
        return `ok-${n}`;
      },
    });

    const p1 = action.dispatch(1, {
      onCancel: () => {
        // Re-entrant cancel — should be a no-op since all instances
        // are already being cancelled
        action.cancel();
      },
    });
    const p2 = action.dispatch(2, { onCancel: onCancel2 });

    action.cancel();
    resolve1();
    await Promise.all([p1, p2]);

    // onCancel2 should fire exactly once (not doubled by re-entrant cancel)
    expect(onCancel2).toHaveBeenCalledTimes(1);
    expect(action.isInflight).toBe(false);
  });
});
