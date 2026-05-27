// @vitest-environment happy-dom
// Edge-case tests for action framework primitives — fills coverage gaps
// identified in Cycle 3 audit:
//   1. dedupe key with undefined args
//   2. structuredClone fallback on retry (non-cloneable args)
//   3. cancel after dedupe entry created but before runOnce starts (scope-queued)
//   4. scope + cancel interaction (cancel while queued behind scope chain)
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { ActionError, retryNetwork } from "./error.js";
import * as toast from "../toast.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

// ===========================================================================
// 1. Dedupe key with undefined args
// ===========================================================================

describe("dedupe with undefined args", () => {
  it("dedupe: true collapses dispatches when args is undefined", async () => {
    let runCalls = 0;
    const action = defineAction<void, string>({
      name: "test.dedupe_undefined_args",
      dedupe: true,
      run: () => {
        runCalls++;
        return new Promise<string>((r) => setTimeout(() => r("ok"), 10));
      },
    });

    const p1 = action.dispatch(undefined);
    const p2 = action.dispatch(undefined);
    expect(runCalls).toBe(1);
    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1).toBe("ok");
    expect(r2).toBe("ok");
  });

  it("dedupe key distinguishes undefined from null in args object", async () => {
    let runCalls = 0;
    const action = defineAction<{ id: string | undefined | null }, string>({
      name: "test.dedupe_undefined_vs_null",
      dedupe: true,
      run: (args) => {
        runCalls++;
        return Promise.resolve(String(args.id));
      },
    });

    // undefined and null serialize differently in JSON.stringify
    const p1 = action.dispatch({ id: undefined });
    const p2 = action.dispatch({ id: null });
    await Promise.all([p1, p2]);
    // JSON.stringify({id: undefined}) => '{}', JSON.stringify({id: null}) => '{"id":null}'
    // These are different keys, so both should run
    expect(runCalls).toBe(2);
  });

  it("dedupe function key handles undefined arg fields gracefully", async () => {
    let runCalls = 0;
    const action = defineAction<{ chatID?: string | undefined }, string>({
      name: "test.dedupe_fn_undefined_field",
      dedupe: (args) => `chat:${args.chatID}`,
      run: () => {
        runCalls++;
        return new Promise<string>((r) => setTimeout(() => r("done"), 5));
      },
    });

    // Both produce key "chat:undefined"
    const p1 = action.dispatch({ chatID: undefined });
    const p2 = action.dispatch({});
    expect(runCalls).toBe(1);
    await Promise.all([p1, p2]);
  });
});

// ===========================================================================
// 2. structuredClone fallback on retry (non-cloneable args)
// ===========================================================================

describe("structuredClone fallback on retry toast", () => {
  it("retry button works when args contain non-cloneable values (functions)", async () => {
    let attempts = 0;
    const callback = vi.fn();
    const action = defineAction<{ fn: () => void; id: string }, string>({
      name: "test.retry_noncloneable",
      retryable: (err) => err.code !== "cancelled",
      run: (args) => {
        attempts++;
        if (attempts === 1) throw new ActionError("fail", { status: 0 });
        args.fn();
        return Promise.resolve("ok");
      },
    });

    await action.dispatch({ fn: callback, id: "x" });
    expect(attempts).toBe(1);

    // The retry button should have been created despite structuredClone
    // failing on the function arg — it falls back to using the original ref
    const errorCalls = vi.mocked(toast.error).mock.calls;
    expect(errorCalls.length).toBe(1);
    const retryHandler = errorCalls[0]?.[1] as { onClick: () => void } | undefined;
    expect(retryHandler).toBeDefined();
    expect(typeof retryHandler?.onClick).toBe("function");

    // Fire the retry
    retryHandler!.onClick();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(attempts).toBe(2);
    expect(callback).toHaveBeenCalledTimes(1);
  });

  it("retry button works when args contain DOM-like objects (non-cloneable)", async () => {
    let attempts = 0;
    // Simulate a non-cloneable object (has a circular reference)
    const circular: Record<string, unknown> = { name: "node" };
    circular["self"] = circular;

    const action = defineAction<{ el: Record<string, unknown> }, string>({
      name: "test.retry_circular",
      retryable: retryNetwork,
      run: (args) => {
        attempts++;
        if (attempts === 1) throw new ActionError("timeout", { code: "network" });
        return Promise.resolve(args.el["name"] as string);
      },
    });

    await action.dispatch({ el: circular });
    expect(attempts).toBe(1);

    const retryHandler = vi.mocked(toast.error).mock.calls[0]?.[1] as
      | { onClick: () => void }
      | undefined;
    expect(retryHandler).toBeDefined();

    retryHandler!.onClick();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(attempts).toBe(2);
  });
});

// ===========================================================================
// 3. Cancel after dedupe entry created but before runOnce starts (scope-queued)
// ===========================================================================

describe("cancel after dedupe entry created but before runOnce starts", () => {
  it("scope-queued dispatch with dedupe: cancel before runOnce starts resolves null and clears dedupe", async () => {
    let runCalls = 0;

    const action = defineAction<{ id: string }, string>({
      name: "test.scope_dedupe_cancel_queued",
      scope: "s",
      dedupe: true,
      run: (args, signal) => {
        runCalls++;
        return new Promise<string>((resolve, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
          setTimeout(() => resolve("result-" + args.id), 50);
        });
      },
    });

    // First dispatch starts running (holds the scope)
    const p1 = action.dispatch({ id: "a" });
    // Second dispatch is queued behind scope (different dedupe key since id differs)
    const p2 = action.dispatch({ id: "b" });

    // Allow microtasks so scope chain starts first runOnce
    await Promise.resolve();
    await Promise.resolve();
    expect(runCalls).toBe(1);

    // Cancel all — p1 is in-flight (aborts), p2 is queued (signal pre-aborted)
    action.cancel();

    // Drain microtasks for the cancellation to propagate
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    const [r1, r2] = await Promise.all([p1, p2]);

    // Both should resolve null (cancelled)
    expect(r1).toBeNull();
    expect(r2).toBeNull();
    // Second dispatch's run() never executed (was pre-aborted)
    expect(runCalls).toBe(1);

    // After cancellation, a fresh dispatch should work (dedupe map cleared)
    runCalls = 0;
    vi.useFakeTimers();
    const p3 = action.dispatch({ id: "b" });
    await vi.advanceTimersByTimeAsync(50);
    const r3 = await p3;
    vi.useRealTimers();
    expect(runCalls).toBe(1);
    expect(r3).toBe("result-b");
  });
});

// ===========================================================================
// 4. Scope + cancel: queued dispatch cancelled before it starts
// ===========================================================================

describe("scope + cancel interaction", () => {
  it("cancel aborts a scope-queued dispatch that hasn't started yet", async () => {
    let runCalls = 0;
    let resolveFirst: (() => void) | null = null;

    const action = defineAction<{ tag: string }, string>({
      name: "test.scope_cancel_queued",
      scope: "q",
      run: (args) => {
        runCalls++;
        if (runCalls === 1) {
          return new Promise<string>((r) => {
            resolveFirst = () => r("A");
          });
        }
        return Promise.resolve(args.tag);
      },
    });

    const onSettled1 = vi.fn();
    const onSettled2 = vi.fn();

    const p1 = action.dispatch({ tag: "A" }, { onSettled: onSettled1 });
    const p2 = action.dispatch({ tag: "B" }, { onSettled: onSettled2 });

    // Allow microtasks so scope chain starts first runOnce
    await Promise.resolve();
    await Promise.resolve();
    expect(runCalls).toBe(1); // only first started

    // Cancel all — first is in-flight, second is queued
    action.cancel();
    resolveFirst!();

    // Drain microtasks
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    const [r1, r2] = await Promise.all([p1, p2]);

    expect(r1).toBeNull();
    expect(r2).toBeNull();
    // Both onSettled callbacks should fire
    expect(onSettled1).toHaveBeenCalledTimes(1);
    expect(onSettled2).toHaveBeenCalledTimes(1);
    // Second dispatch's run() never executed
    expect(runCalls).toBe(1);
  });

  it("scope chain continues after a cancelled dispatch", async () => {
    let resolveFirst: (() => void) | null = null;
    const log: string[] = [];

    const action = defineAction<{ tag: string }, void>({
      name: "test.scope_cancel_continues",
      scope: "q",
      run: (args) => {
        log.push(args.tag);
        if (args.tag === "A") {
          return new Promise<void>((r) => {
            resolveFirst = () => r();
          });
        }
        return Promise.resolve();
      },
    });

    const p1 = action.dispatch({ tag: "A" });
    // Allow microtasks so first runOnce starts
    await Promise.resolve();
    await Promise.resolve();

    action.cancel(); // cancel A
    resolveFirst!();
    await Promise.resolve();
    await Promise.resolve();
    await p1;

    // Scope chain should be clear — new dispatch starts immediately
    const p2 = action.dispatch({ tag: "B" });
    await p2;

    expect(log).toContain("B");
  });
});

// ===========================================================================
// 5. Abort during retry backoff — signal fires mid-sleep
// ===========================================================================

describe("abort during retry backoff (signal fires mid-sleep)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("cancel during backoff sleep aborts the retry chain and resolves null", async () => {
    let attempts = 0;
    const action = defineAction<void, string>({
      name: "test.retry_abort_backoff",
      retryable: retryNetwork,
      retry: { count: 3, delay: 500 },
      error: false,
      run: () => {
        attempts++;
        return Promise.reject(new ActionError("net", { code: "network" }));
      },
    });

    const onSettled = vi.fn();
    const p = action.dispatch(undefined, { onSettled });

    // First attempt fails immediately, backoff of 500ms starts
    await vi.advanceTimersByTimeAsync(0);
    expect(attempts).toBe(1);

    // Cancel mid-backoff (at 250ms into the 500ms wait)
    await vi.advanceTimersByTimeAsync(250);
    action.cancel();

    await vi.advanceTimersByTimeAsync(1000); // advance well past any remaining timers
    const result = await p;

    expect(result).toBeNull();
    expect(attempts).toBe(1); // no retry happened
    expect(onSettled).toHaveBeenCalledTimes(1);
  });

  it("cancel between retry attempts (after first retry succeeds in failing, during second backoff)", async () => {
    let attempts = 0;
    const action = defineAction<void, string>({
      name: "test.retry_abort_between",
      retryable: retryNetwork,
      retry: { count: 5, delay: 100 },
      error: false,
      run: () => {
        attempts++;
        return Promise.reject(new ActionError("net", { code: "network" }));
      },
    });

    const p = action.dispatch();

    // First attempt fails, 100ms backoff
    await vi.advanceTimersByTimeAsync(100);
    // Second attempt fails, 200ms backoff
    expect(attempts).toBe(2);

    // Cancel during the 200ms backoff
    await vi.advanceTimersByTimeAsync(50);
    action.cancel();

    await vi.advanceTimersByTimeAsync(5000);
    const result = await p;

    expect(result).toBeNull();
    expect(attempts).toBe(2); // stopped after second attempt
  });
});
