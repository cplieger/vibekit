// Tests for Cycle 11 improvements: isInflight primitive, toActionError
// error quality (code preservation, plain-object handling), and retry
// safety (abort guard at loop top).
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { ActionError, toActionError } from "./error.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
});

describe("Action.isInflight", () => {
  it("returns false before any dispatch", () => {
    const action = defineAction({
      name: "test.inflight_idle",
      run: async () => "ok",
    });
    expect(action.isInflight).toBe(false);
  });

  it("returns true while dispatch is in-flight", async () => {
    let resolve!: () => void;
    const gate = new Promise<void>((r) => { resolve = r; });
    const action = defineAction({
      name: "test.inflight_pending",
      run: async () => { await gate; return "done"; },
    });
    const p = action.dispatch(undefined);
    expect(action.isInflight).toBe(true);
    resolve();
    await p;
    expect(action.isInflight).toBe(false);
  });

  it("returns false after cancellation", async () => {
    let resolve!: () => void;
    const gate = new Promise<void>((r) => { resolve = r; });
    const action = defineAction({
      name: "test.inflight_cancel",
      run: async (_args, signal) => {
        await gate;
        if (signal.aborted) throw new Error("aborted");
        return "done";
      },
    });
    const p = action.dispatch(undefined);
    expect(action.isInflight).toBe(true);
    action.cancel();
    resolve();
    await p;
    expect(action.isInflight).toBe(false);
  });
});

describe("toActionError — error quality", () => {
  it("preserves code from Error subclasses with a code property", () => {
    class CustomError extends Error {
      code = "custom_code";
      status = 503;
    }
    const err = new CustomError("service unavailable");
    const result = toActionError(err);
    expect(result.message).toBe("service unavailable");
    expect(result.code).toBe("custom_code");
    expect(result.status).toBe(503);
  });

  it("handles plain objects with message/status/code", () => {
    const plain = { message: "bad request", status: 400, code: "validation" };
    const result = toActionError(plain);
    expect(result.message).toBe("bad request");
    expect(result.status).toBe(400);
    expect(result.code).toBe("validation");
    expect(result.cause).toBe(plain);
  });

  it("handles plain objects with only message", () => {
    const plain = { message: "something went wrong" };
    const result = toActionError(plain);
    expect(result.message).toBe("something went wrong");
    expect(result.status).toBeUndefined();
    expect(result.code).toBeUndefined();
  });

  it("falls back to String() for non-object non-Error values", () => {
    expect(toActionError(42).message).toBe("42");
    expect(toActionError(null).message).toBe("Unknown error (null thrown)");
    expect(toActionError(undefined).message).toBe("Unknown error (undefined thrown)");
  });

  it("preserves ActionError fields exactly", () => {
    const ae = new ActionError("conflict", { status: 409, code: "conflict" });
    const result = toActionError(ae);
    expect(result.message).toBe("conflict");
    expect(result.status).toBe(409);
    expect(result.code).toBe("conflict");
    // ActionError instances don't get wrapped in cause
    expect(result.cause).toBeUndefined();
  });
});

describe("dedupe path — onSettled guarantee (try/finally)", () => {
  it("fires onSettled for deduped caller on success", async () => {
    let resolve!: () => void;
    const gate = new Promise<void>((r) => { resolve = r; });
    const action = defineAction({
      name: "test.dedupe_settled_ok",
      dedupe: true,
      run: async () => { await gate; return "val"; },
    });
    const settled1 = vi.fn();
    const settled2 = vi.fn();
    const p1 = action.dispatch("a", { onSettled: settled1 });
    const p2 = action.dispatch("a", { onSettled: settled2 });
    resolve();
    await Promise.all([p1, p2]);
    expect(settled1).toHaveBeenCalledWith("a");
    expect(settled2).toHaveBeenCalledWith("a");
  });

  it("fires onSettled for deduped caller on error", async () => {
    const action = defineAction({
      name: "test.dedupe_settled_err",
      dedupe: true,
      run: async () => { throw new ActionError("boom"); },
    });
    const settled1 = vi.fn();
    const settled2 = vi.fn();
    const p1 = action.dispatch("x", { onSettled: settled1 });
    const p2 = action.dispatch("x", { onSettled: settled2 });
    await Promise.all([p1, p2]);
    expect(settled1).toHaveBeenCalledWith("x");
    expect(settled2).toHaveBeenCalledWith("x");
  });

  it("fires onSettled for deduped caller on cancellation", async () => {
    const action = defineAction({
      name: "test.dedupe_settled_cancel",
      dedupe: true,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const settled1 = vi.fn();
    const settled2 = vi.fn();
    const p1 = action.dispatch("c", { onSettled: settled1 });
    const p2 = action.dispatch("c", { onSettled: settled2 });
    action.cancel();
    await Promise.all([p1, p2]);
    expect(settled1).toHaveBeenCalledWith("c");
    expect(settled2).toHaveBeenCalledWith("c");
  });
});

describe("retry safety — abort guard at loop top", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("does not call run() when signal is already aborted before retry", async () => {
    let callCount = 0;
    const action = defineAction({
      name: "test.retry_abort_guard",
      retryable: "always",
      retry: { count: 3, delay: 0 },
      run: async (_args, _signal) => {
        callCount++;
        if (callCount === 1) {
          // First call fails with a retryable error, then we abort
          throw new ActionError("network blip", { code: "network" });
        }
        // Should never reach here if abort guard works
        return "unexpected";
      },
    });

    const p = action.dispatch(undefined);
    // Cancel immediately after the first run() throws — the retry loop
    // should check signal.aborted before the second attempt.
    // Since delay is 0, sleep resolves immediately, but the abort guard
    // at the top of the while loop catches it.
    action.cancel();
    const result = await p;
    expect(result).toBeNull();
    // With delay: 0, the abort guard fires before the second run() call.
    // callCount should be exactly 1 (the initial attempt).
    expect(callCount).toBe(1);
    const log = recentLog();
    expect(log[0]?.status).toBe("cancelled");
  });

  it("records correct attempt count when aborted mid-retry", async () => {
    let callCount = 0;
    let cancelFn!: () => void;
    const action = defineAction({
      name: "test.retry_abort_mid",
      retryable: "always",
      retry: { count: 5, delay: 50 },
      run: async () => {
        callCount++;
        if (callCount === 2) {
          // Cancel during the second attempt — the third should not start
          cancelFn();
        }
        throw new ActionError("transient", { code: "network" });
      },
    });
    cancelFn = () => action.cancel();
    const p = action.dispatch(undefined);
    // Advance past the first retry delay (50ms) to trigger attempt 2
    await vi.advanceTimersByTimeAsync(50);
    const result = await p;
    expect(result).toBeNull();
    // Should have run at most 2 times (first attempt + one retry before cancel takes effect)
    expect(callCount).toBeLessThanOrEqual(2);
  });

  it("handles frozen error objects without crashing", async () => {
    const action = defineAction({
      name: "test.retry_frozen_error",
      retryable: "always",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        const err = new ActionError("frozen", { code: "network" });
        Object.freeze(err);
        throw err;
      },
    });
    // Should not throw — attachAttempts gracefully skips frozen objects
    const result = await action.dispatch(undefined);
    expect(result).toBeNull();
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
  });
});

describe("retryArgs — fresh args at retry-click time", () => {
  it("uses retryArgs function when provided for retry button", async () => {
    const dispatches: string[] = [];
    const action = defineAction<{ id: string }, string>({
      name: "test.retry_args_fresh",
      retryable: "always",
      retryArgs: (original) => ({ id: `fresh-${original.id}` }),
      run: async (args) => {
        dispatches.push(args.id);
        if (dispatches.length === 1) {
          throw new ActionError("fail", { code: "network" });
        }
        return "ok";
      },
    });

    // First dispatch fails
    await action.dispatch({ id: "orig" });
    expect(dispatches).toEqual(["orig"]);

    // The toast.error mock captures the retry config as the 2nd arg.
    const { error: toastError } = await import("../toast.js");
    const lastCall = vi.mocked(toastError).mock.calls[0];
    expect(lastCall).toBeDefined();
    const retryConfig = lastCall![1] as { onClick: () => void } | undefined;
    expect(retryConfig).toBeDefined();

    // Click retry — should use retryArgs to compute fresh args
    retryConfig!.onClick();
    await Promise.resolve();
    await Promise.resolve();

    expect(dispatches).toEqual(["orig", "fresh-orig"]);
  });

  it("suppresses retry when retryArgs returns null", async () => {
    const dispatches: number[] = [];
    const action = defineAction<number, string>({
      name: "test.retry_args_null",
      retryable: "always",
      retryArgs: () => null,
      run: async (args) => {
        dispatches.push(args);
        throw new ActionError("fail", { code: "network" });
      },
    });

    await action.dispatch(42);
    expect(dispatches).toEqual([42]);

    const { error: toastError } = await import("../toast.js");
    const lastCall = vi.mocked(toastError).mock.calls[0];
    expect(lastCall).toBeDefined();
    const retryConfig = lastCall![1] as { onClick: () => void } | undefined;
    expect(retryConfig).toBeDefined();

    // Click retry — retryArgs returns null, so no dispatch
    retryConfig!.onClick();
    await Promise.resolve();
    await Promise.resolve();

    expect(dispatches).toEqual([42]); // no second dispatch
  });

  it("does not throw when retryArgs function throws", async () => {
    const dispatches: string[] = [];
    const action = defineAction<string, string>({
      name: "test.retry_args_throws",
      retryable: "always",
      retryArgs: () => { throw new Error("retryArgs exploded"); },
      run: async (args) => {
        dispatches.push(args);
        throw new ActionError("fail", { code: "network" });
      },
    });

    await action.dispatch("orig");
    expect(dispatches).toEqual(["orig"]);

    const { error: toastError } = await import("../toast.js");
    const calls = vi.mocked(toastError).mock.calls;
    const lastCall = calls[calls.length - 1];
    const retryConfig = lastCall![1] as { onClick: () => void } | undefined;
    expect(retryConfig).toBeDefined();

    // Click retry — retryArgs throws, should not crash or dispatch
    retryConfig!.onClick();
    await Promise.resolve();
    await Promise.resolve();

    expect(dispatches).toEqual(["orig"]); // no second dispatch
  });
});

describe("readAttempts — Proxy edge case", () => {
  it("handles error objects with throwing _attempts getter", async () => {
    const action = defineAction({
      name: "test.proxy_attempts",
      retryable: "always",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        const err = new ActionError("proxy", { code: "network" });
        Object.defineProperty(err, "_attempts", {
          get() { throw new Error("getter exploded"); },
          configurable: true,
        });
        throw err;
      },
    });
    // Should not throw — readAttempts gracefully handles getter errors
    const result = await action.dispatch(undefined);
    expect(result).toBeNull();
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
  });
});

describe("isRetryClass — transient HTTP status auto-retry", () => {
  it("auto-retries on 429 under retryable: 'network'", async () => {
    let attempts = 0;
    const action = defineAction({
      name: "test.auto_retry_429",
      retryable: "network",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        attempts++;
        if (attempts === 1) throw new ActionError("rate limited", { status: 429 });
        return "ok";
      },
    });
    const result = await action.dispatch(undefined);
    expect(result).toBe("ok");
    expect(attempts).toBe(2);
  });

  it("auto-retries on 503 under retryable: 'network'", async () => {
    let attempts = 0;
    const action = defineAction({
      name: "test.auto_retry_503",
      retryable: "network",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        attempts++;
        if (attempts === 1) throw new ActionError("unavailable", { status: 503 });
        return "ok";
      },
    });
    const result = await action.dispatch(undefined);
    expect(result).toBe("ok");
    expect(attempts).toBe(2);
  });

  it("does not auto-retry 404 under retryable: 'network'", async () => {
    let attempts = 0;
    const action = defineAction({
      name: "test.no_retry_404",
      retryable: "network",
      retry: { count: 2, delay: 0 },
      error: false,
      run: async () => {
        attempts++;
        throw new ActionError("not found", { status: 404 });
      },
    });
    await action.dispatch(undefined);
    expect(attempts).toBe(1);
  });

  it("does not auto-retry 'unsupported' code under retryable: 'always'", async () => {
    let attempts = 0;
    const action = defineAction({
      name: "test.no_retry_unsupported",
      retryable: "always",
      retry: { count: 2, delay: 0 },
      error: false,
      run: async () => {
        attempts++;
        throw new ActionError("not supported", { code: "unsupported" });
      },
    });
    await action.dispatch(undefined);
    expect(attempts).toBe(1);
  });

  it("scope + transient retry: scoped action retries 429 then succeeds", async () => {
    let attempts = 0;
    const action = defineAction({
      name: "test.scope_retry_transient",
      scope: "s",
      retryable: "network",
      retry: { count: 2, delay: 0 },
      run: async () => {
        attempts++;
        if (attempts <= 2) throw new ActionError("overloaded", { status: 503 });
        return "recovered";
      },
    });
    const result = await action.dispatch(undefined);
    expect(result).toBe("recovered");
    expect(attempts).toBe(3);
    const log = recentLog();
    expect(log[0]?.status).toBe("success");
    expect(log[0]?.attempts).toBe(3);
  });

  it("auto-retries on 502 under retryable: 'network'", async () => {
    let attempts = 0;
    const action = defineAction({
      name: "test.auto_retry_502",
      retryable: "network",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        attempts++;
        if (attempts === 1) throw new ActionError("bad gateway", { status: 502 });
        return "ok";
      },
    });
    const result = await action.dispatch(undefined);
    expect(result).toBe("ok");
    expect(attempts).toBe(2);
  });

  it("auto-retries on 408 under retryable: 'network'", async () => {
    let attempts = 0;
    const action = defineAction({
      name: "test.auto_retry_408",
      retryable: "network",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        attempts++;
        if (attempts === 1) throw new ActionError("request timeout", { status: 408 });
        return "ok";
      },
    });
    const result = await action.dispatch(undefined);
    expect(result).toBe("ok");
    expect(attempts).toBe(2);
  });

  it("auto-retries on 504 under retryable: 'network'", async () => {
    let attempts = 0;
    const action = defineAction({
      name: "test.auto_retry_504",
      retryable: "network",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        attempts++;
        if (attempts === 1) throw new ActionError("gateway timeout", { status: 504 });
        return "ok";
      },
    });
    const result = await action.dispatch(undefined);
    expect(result).toBe("ok");
    expect(attempts).toBe(2);
  });
});

describe("retry abort during backoff — stale state prevention", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("cancel during backoff sleep records cancelled (not stale run error)", async () => {
    let runCount = 0;
    const action = defineAction({
      name: "test.abort_backoff_stale",
      retryable: "always",
      retry: { count: 3, delay: 200 },
      error: false,
      run: async () => {
        runCount++;
        throw new ActionError("transient failure", { status: 503, code: "network" });
      },
    });
    const p = action.dispatch(undefined);
    // First run() throws immediately, then retry loop enters sleep(200ms).
    // Cancel during the backoff sleep.
    await vi.advanceTimersByTimeAsync(100);
    action.cancel();
    await vi.advanceTimersByTimeAsync(200);
    const result = await p;
    expect(result).toBeNull();
    expect(runCount).toBe(1);
    const log = recentLog();
    // Must be cancelled, not error — the stale run error must not leak through.
    expect(log[0]?.status).toBe("cancelled");
    // The error field should NOT be present on a cancelled record.
    expect(log[0]?.error).toBeUndefined();
  });

  it("cancel during backoff does not fire onError (fires onCancel)", async () => {
    const onError = vi.fn();
    const onCancel = vi.fn();
    const action = defineAction({
      name: "test.abort_backoff_callbacks",
      retryable: "always",
      retry: { count: 2, delay: 100 },
      error: false,
      run: async () => {
        throw new ActionError("server error", { status: 500, code: "network" });
      },
    });
    const p = action.dispatch(undefined, { onError, onCancel });
    await vi.advanceTimersByTimeAsync(50);
    action.cancel();
    await vi.advanceTimersByTimeAsync(200);
    await p;
    expect(onError).not.toHaveBeenCalled();
    expect(onCancel).toHaveBeenCalledTimes(1);
  });
});

describe("onRetryAttempt callback", () => {
  it("fires before each retry attempt with correct info including delay", async () => {
    vi.useFakeTimers();
    const retryInfo: Array<{ attempt: number; maxAttempts: number; error: string; delay: number }> = [];
    let runCount = 0;
    const action = defineAction({
      name: "test.on_retry_attempt",
      retryable: "always",
      retry: { count: 2, delay: 100, factor: 3 },
      error: false,
      run: async () => {
        runCount++;
        if (runCount < 3) throw new ActionError("transient", { code: "network" });
        return "ok";
      },
    });
    const p = action.dispatch(undefined, {
      onRetryAttempt: (info) => {
        retryInfo.push({ attempt: info.attempt, maxAttempts: info.maxAttempts, error: info.error.message, delay: info.delay });
      },
    });
    // Advance past first retry delay (100ms) and second (300ms)
    await vi.advanceTimersByTimeAsync(500);
    const result = await p;
    expect(result).toBe("ok");
    // delay for attempt 2: baseDelay * factor^0 = 100
    // delay for attempt 3: baseDelay * factor^1 = 300
    expect(retryInfo).toEqual([
      { attempt: 2, maxAttempts: 3, error: "transient", delay: 100 },
      { attempt: 3, maxAttempts: 3, error: "transient", delay: 300 },
    ]);
    vi.useRealTimers();
  });

  it("does not fire on the initial attempt", async () => {
    const retryInfo: number[] = [];
    const action = defineAction({
      name: "test.on_retry_no_initial",
      retryable: "always",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => "ok",
    });
    await action.dispatch(undefined, {
      onRetryAttempt: (info) => { retryInfo.push(info.attempt); },
    });
    expect(retryInfo).toEqual([]);
  });

  it("throwing in onRetryAttempt does not disrupt retry", async () => {
    let attempts = 0;
    const consoleErr = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const action = defineAction({
      name: "test.on_retry_throws",
      retryable: "always",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        attempts++;
        if (attempts === 1) throw new ActionError("fail", { code: "network" });
        return "recovered";
      },
    });
    const result = await action.dispatch(undefined, {
      onRetryAttempt: () => { throw new Error("callback exploded"); },
    });
    expect(result).toBe("recovered");
    expect(attempts).toBe(2);
    expect(consoleErr).toHaveBeenCalled();
    consoleErr.mockRestore();
  });
});

describe("onRetryExhausted callback", () => {
  it("fires when all retries are exhausted", async () => {
    const exhaustedInfo: Array<{ error: string; attempts: number }> = [];
    const action = defineAction({
      name: "test.retry_exhausted",
      retryable: "always",
      retry: { count: 2, delay: 0 },
      error: false,
      run: async () => {
        throw new ActionError("persistent failure", { code: "network" });
      },
    });
    await action.dispatch(undefined, {
      onRetryExhausted: (info) => {
        exhaustedInfo.push({ error: info.error.message, attempts: info.attempts });
      },
    });
    expect(exhaustedInfo).toEqual([
      { error: "persistent failure", attempts: 3 },
    ]);
  });

  it("does not fire when retry succeeds", async () => {
    let runCount = 0;
    const exhaustedCalls: number[] = [];
    const action = defineAction({
      name: "test.retry_exhausted_success",
      retryable: "always",
      retry: { count: 2, delay: 0 },
      error: false,
      run: async () => {
        runCount++;
        if (runCount === 1) throw new ActionError("transient", { code: "network" });
        return "ok";
      },
    });
    const result = await action.dispatch(undefined, {
      onRetryExhausted: (info) => { exhaustedCalls.push(info.attempts); },
    });
    expect(result).toBe("ok");
    expect(exhaustedCalls).toEqual([]);
  });

  it("does not fire when no retries configured", async () => {
    const exhaustedCalls: number[] = [];
    const action = defineAction({
      name: "test.retry_exhausted_no_retry",
      retryable: "always",
      error: false,
      run: async () => {
        throw new ActionError("fail", { code: "network" });
      },
    });
    await action.dispatch(undefined, {
      onRetryExhausted: (info) => { exhaustedCalls.push(info.attempts); },
    });
    expect(exhaustedCalls).toEqual([]);
  });

  it("throwing in onRetryExhausted does not disrupt error handling", async () => {
    const consoleErr = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const onError = vi.fn();
    const action = defineAction({
      name: "test.retry_exhausted_throws",
      retryable: "always",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        throw new ActionError("fail", { code: "network" });
      },
    });
    await action.dispatch(undefined, {
      onRetryExhausted: () => { throw new Error("exhausted callback exploded"); },
      onError,
    });
    expect(onError).toHaveBeenCalledTimes(1);
    expect(consoleErr).toHaveBeenCalled();
    consoleErr.mockRestore();
  });
});

describe("scope + retry + callbacks interaction", () => {
  it("scoped action fires onRetryAttempt then onSuccess in correct order", async () => {
    const events: string[] = [];
    let runCount = 0;
    const action = defineAction({
      name: "test.scope_retry_cb_order",
      scope: "sr",
      retryable: "always",
      retry: { count: 2, delay: 0 },
      error: false,
      run: async () => {
        runCount++;
        if (runCount === 1) throw new ActionError("blip", { code: "network" });
        return "done";
      },
    });
    const result = await action.dispatch(undefined, {
      onRetryAttempt: () => { events.push("retry"); },
      onSuccess: () => { events.push("success"); },
      onSettled: () => { events.push("settled"); },
    });
    expect(result).toBe("done");
    expect(events).toEqual(["retry", "success", "settled"]);
  });

  it("scoped action fires onRetryExhausted then onError then onSettled", async () => {
    const events: string[] = [];
    const action = defineAction({
      name: "test.scope_retry_exhaust_cb",
      scope: "sr2",
      retryable: "always",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        throw new ActionError("persistent", { code: "network" });
      },
    });
    await action.dispatch(undefined, {
      onRetryAttempt: () => { events.push("retry"); },
      onRetryExhausted: () => { events.push("exhausted"); },
      onError: () => { events.push("error"); },
      onSettled: () => { events.push("settled"); },
    });
    expect(events).toEqual(["retry", "exhausted", "error", "settled"]);
  });

  it("second scoped dispatch callbacks fire only after first completes retries", async () => {
    const order: string[] = [];
    let firstRuns = 0;
    const action = defineAction({
      name: "test.scope_serial_retry_cb",
      scope: "serial",
      retryable: "always",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async () => {
        firstRuns++;
        if (firstRuns <= 2) throw new ActionError("fail", { code: "network" });
        return "ok";
      },
    });
    const p1 = action.dispatch(undefined, {
      onRetryExhausted: () => { order.push("p1:exhausted"); },
      onError: () => { order.push("p1:error"); },
      onSettled: () => { order.push("p1:settled"); },
    });
    const p2 = action.dispatch(undefined, {
      onSuccess: () => { order.push("p2:success"); },
      onSettled: () => { order.push("p2:settled"); },
    });
    await Promise.all([p1, p2]);
    // p1 exhausts retries (2 attempts), then p2 runs (attempt 3 succeeds)
    expect(order).toEqual([
      "p1:exhausted", "p1:error", "p1:settled",
      "p2:success", "p2:settled",
    ]);
  });

  it("onSettled fires for scoped action even when retry + cancel interact", async () => {
    vi.useFakeTimers();
    const settled = vi.fn();
    const onCancel = vi.fn();
    const action = defineAction({
      name: "test.scope_retry_cancel_settled",
      scope: "sc",
      retryable: "always",
      retry: { count: 3, delay: 50 },
      error: false,
      run: async () => {
        throw new ActionError("transient", { code: "network" });
      },
    });
    const p = action.dispatch(undefined, { onSettled: settled, onCancel });
    // Let first attempt fail, then cancel during backoff
    await vi.advanceTimersByTimeAsync(25);
    action.cancel();
    await vi.advanceTimersByTimeAsync(100);
    await p;
    expect(settled).toHaveBeenCalledTimes(1);
    expect(onCancel).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});

describe("classifyFetchError ↔ isRetryableError consistency", () => {
  it("classifyFetchError network errors are retryable under 'network' mode", async () => {
    const { classifyFetchError, isRetryableError } = await import("./error.js");
    const ac = new AbortController();
    const err = classifyFetchError(new TypeError("Failed to fetch"), ac.signal);
    expect(isRetryableError(err, "network")).toBe(true);
    expect(err.status).toBe(0);
    expect(err.code).toBe("network");
  });

  it("classifyFetchError timeout errors are retryable under 'network' mode", async () => {
    const { classifyFetchError, isRetryableError } = await import("./error.js");
    const ac = new AbortController();
    const err = classifyFetchError(new DOMException("timed out", "TimeoutError"), ac.signal);
    expect(isRetryableError(err, "network")).toBe(true);
    expect(err.status).toBe(0);
    expect(err.code).toBe("timeout");
  });

  it("classifyFetchError cancelled errors are NOT retryable", async () => {
    const { classifyFetchError, isRetryableError } = await import("./error.js");
    const ac = new AbortController();
    ac.abort();
    const err = classifyFetchError(new Error("aborted"), ac.signal);
    expect(isRetryableError(err, "network")).toBe(false);
    expect(isRetryableError(err, "always")).toBe(false);
    expect(err.code).toBe("cancelled");
  });

  it("auto-retries classifyFetchError network error in scoped action", async () => {
    const { classifyFetchError } = await import("./error.js");
    let attempts = 0;
    const action = defineAction({
      name: "test.classify_retry_scope",
      scope: "net",
      retryable: "network",
      retry: { count: 1, delay: 0 },
      error: false,
      run: async (_args: undefined, signal: AbortSignal) => {
        attempts++;
        if (attempts === 1) {
          throw classifyFetchError(new TypeError("Failed to fetch"), signal);
        }
        return "recovered";
      },
    });
    const result = await action.dispatch(undefined);
    expect(result).toBe("recovered");
    expect(attempts).toBe(2);
  });
});
