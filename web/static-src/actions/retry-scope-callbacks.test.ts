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

beforeEach(() => {
  resetDefine();
  resetRegistry();
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
