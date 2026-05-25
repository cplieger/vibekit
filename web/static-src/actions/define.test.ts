// @vitest-environment happy-dom
// Foundation tests for the action framework. Pins the lifecycle
// contract: optimistic, run, success/error/cancelled, rollback,
// toast wiring, registry recording, multiple in-flight instances.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock toast.ts so we can assert on the toast calls without rendering
// real DOM toasts. Must be at the top before defineAction imports.
vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { ActionError } from "./error.js";
import { recentLog, _resetForTest as resetRegistry, subscribe, pendingFor } from "./registry.js";
import * as toast from "../toast.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  vi.clearAllMocks();
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("defineAction — happy path", () => {
  it("dispatch resolves with the run() result", async () => {
    const action = defineAction({
      name: "test.echo",
      run: async (args: { msg: string }) => args.msg,
    });
    const r = await action.dispatch({ msg: "hello" });
    expect(r).toBe("hello");
  });

  it("records pending then success in the registry", async () => {
    const action = defineAction({
      name: "test.ok",
      run: async () => "done",
    });
    const events: string[] = [];
    const unsub = subscribe((i) => {
      events.push(i.status);
    });
    await action.dispatch({});
    unsub();
    expect(events).toEqual(["pending", "success"]);
    const log = recentLog();
    expect(log.length).toBe(1);
    expect(log[0]?.status).toBe("success");
    expect(log[0]?.result).toBe("done");
  });

  it("does not toast on success by default", async () => {
    const action = defineAction({
      name: "test.silent_success",
      run: async () => "x",
    });
    await action.dispatch({});
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("toasts on success when `success` is a string", async () => {
    const action = defineAction({
      name: "test.success_string",
      run: async () => "x",
      success: "Saved",
    });
    await action.dispatch({});
    expect(toast.success).toHaveBeenCalledWith("Saved");
  });

  it("toasts on success when `success` is a function", async () => {
    const action = defineAction({
      name: "test.success_fn",
      run: async (args: string) => `${args}!`,
      success: (args, result) => `Got ${result} for ${args}`,
    });
    await action.dispatch("hi");
    expect(toast.success).toHaveBeenCalledWith("Got hi! for hi");
  });

  it("dispatch({ silent: true }) suppresses success toast", async () => {
    const action = defineAction({
      name: "test.silenced",
      run: async () => "x",
      success: "Saved",
    });
    await action.dispatch({}, { silent: true });
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("dispatch({ successMessage }) overrides the success toast", async () => {
    const action = defineAction({
      name: "test.custom_success",
      run: async () => "x",
      success: "Default",
    });
    await action.dispatch({}, { successMessage: "Custom" });
    expect(toast.success).toHaveBeenCalledWith("Custom");
  });
});

describe("defineAction — error path", () => {
  it("dispatch resolves to null on run() throw", async () => {
    const action = defineAction({
      name: "test.fail",
      run: async () => {
        throw new ActionError("boom");
      },
    });
    const r = await action.dispatch({});
    expect(r).toBeNull();
  });

  it("records error status with the error message", async () => {
    const action = defineAction({
      name: "test.error",
      run: async () => {
        throw new ActionError("bad", { status: 500 });
      },
    });
    await action.dispatch({});
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
    expect(log[0]?.error?.message).toBe("bad");
    expect(log[0]?.error?.status).toBe(500);
  });

  it("toasts on error by default with the action-name prefix", async () => {
    const action = defineAction({
      name: "chat.delete",
      run: async () => {
        throw new ActionError("not found");
      },
    });
    await action.dispatch({});
    expect(toast.error).toHaveBeenCalledWith("Delete failed: not found", undefined);
  });

  it("`error: 'Custom prefix'` becomes the toast prefix", async () => {
    const action = defineAction({
      name: "test.fail",
      run: async () => {
        throw new ActionError("nope");
      },
      error: "Couldn't do the thing",
    });
    await action.dispatch({});
    expect(toast.error).toHaveBeenCalledWith("Couldn't do the thing: nope", undefined);
  });

  it("`error: false` suppresses the error toast", async () => {
    const action = defineAction({
      name: "test.no_toast",
      run: async () => {
        throw new ActionError("silent");
      },
      error: false,
    });
    await action.dispatch({});
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("dispatch({ errorPrefix }) overrides the error prefix", async () => {
    const action = defineAction({
      name: "test.fail",
      run: async () => {
        throw new ActionError("nope");
      },
      error: "Default",
    });
    await action.dispatch({}, { errorPrefix: "Per-call" });
    expect(toast.error).toHaveBeenCalledWith("Per-call: nope", undefined);
  });

  it("normalises non-ActionError throws", async () => {
    const action = defineAction({
      name: "test.weird",
      run: async () => {
        throw "string";
      },
    });
    await action.dispatch({});
    const log = recentLog();
    expect(log[0]?.error?.message).toBe("string");
  });
});

describe("defineAction — optimistic + rollback", () => {
  it("calls optimistic before run() with args", async () => {
    const order: string[] = [];
    const action = defineAction({
      name: "test.opt",
      optimistic: () => {
        order.push("opt");
        return undefined;
      },
      run: async () => {
        order.push("run");
        return undefined;
      },
    });
    await action.dispatch({});
    expect(order).toEqual(["opt", "run"]);
  });

  it("rollback receives the TOp on error", async () => {
    const rollback = vi.fn();
    const action = defineAction({
      name: "test.rollback",
      optimistic: () => ({ undoToken: 42 }),
      rollback,
      run: async () => {
        throw new ActionError("fail");
      },
    });
    await action.dispatch({ id: "x" });
    expect(rollback).toHaveBeenCalledWith(
      { id: "x" },
      { undoToken: 42 },
      expect.objectContaining({ message: "fail" }),
    );
  });

  it("rollback NOT called on success", async () => {
    const rollback = vi.fn();
    const action = defineAction({
      name: "test.no_rollback",
      optimistic: () => ({ x: 1 }),
      rollback,
      run: async () => "ok",
    });
    await action.dispatch({});
    expect(rollback).not.toHaveBeenCalled();
  });

  it("optimistic throwing skips run() and toasts the error", async () => {
    const run = vi.fn();
    const action = defineAction({
      name: "test.opt_throw",
      optimistic: () => {
        throw new Error("optimistic broke");
      },
      run: async () => {
        run();
        return "x";
      },
    });
    await action.dispatch({});
    expect(run).not.toHaveBeenCalled();
    expect(toast.error).toHaveBeenCalled();
  });

  it("rollback exception is logged but doesn't crash", async () => {
    const consoleErr = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const action = defineAction({
      name: "test.rollback_throw",
      optimistic: () => ({}),
      rollback: () => {
        throw new Error("rollback broke");
      },
      run: async () => {
        throw new ActionError("run failed");
      },
    });
    await action.dispatch({});
    expect(consoleErr).toHaveBeenCalled();
    expect(toast.error).toHaveBeenCalled();  // run-failure toast still fires
    consoleErr.mockRestore();
  });
});

describe("defineAction — cancellation", () => {
  it("action.cancel() aborts the run()'s signal", async () => {
    let aborted = false;
    const action = defineAction({
      name: "test.cancel",
      run: async (_args, signal) => {
        return new Promise<string>((_resolve, reject) => {
          signal.addEventListener("abort", () => {
            aborted = true;
            reject(new DOMException("aborted", "AbortError"));
          });
        });
      },
    });
    const p = action.dispatch({});
    action.cancel();
    const r = await p;
    expect(r).toBeNull();
    expect(aborted).toBe(true);
  });

  it("cancel records 'cancelled' status, not 'error'", async () => {
    const action = defineAction({
      name: "test.cancel_status",
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const p = action.dispatch({});
    action.cancel();
    await p;
    const log = recentLog();
    const final = log[log.length - 1];
    expect(final?.status).toBe("cancelled");
  });

  it("cancel still calls rollback to undo optimistic", async () => {
    const rollback = vi.fn();
    const action = defineAction({
      name: "test.cancel_rollback",
      optimistic: () => ({ token: "abc" }),
      rollback,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const p = action.dispatch({});
    action.cancel();
    await p;
    expect(rollback).toHaveBeenCalled();
  });

  it("cancel does NOT toast (cancellation is user-initiated)", async () => {
    const action = defineAction({
      name: "test.cancel_no_toast",
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
      error: "Should not appear",
    });
    const p = action.dispatch({});
    action.cancel();
    await p;
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("defineAction — concurrent instances", () => {
  it("each dispatch gets a unique id", async () => {
    const action = defineAction({
      name: "test.multi",
      run: async () => "x",
    });
    await Promise.all([action.dispatch({}), action.dispatch({}), action.dispatch({})]);
    const log = recentLog();
    const ids = log.map((i) => i.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("cancel() aborts all in-flight instances", async () => {
    const aborts: number[] = [];
    let i = 0;
    const action = defineAction({
      name: "test.multi_cancel",
      run: (_args, signal) => {
        const me = ++i;
        return new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => {
            aborts.push(me);
            reject(new Error("aborted"));
          });
        });
      },
    });
    const ps = [action.dispatch({}), action.dispatch({}), action.dispatch({})];
    action.cancel();
    await Promise.all(ps);
    expect(aborts.sort()).toEqual([1, 2, 3]);
  });
});

describe("registry", () => {
  it("recentLog is bounded", async () => {
    const action = defineAction({
      name: "test.bounded",
      run: async () => "x",
    });
    // Fire 250 dispatches; registry caps at 200.
    for (let i = 0; i < 250; i++) {
      await action.dispatch({});
    }
    expect(recentLog().length).toBe(200);
  });

  it("subscriber unsubscribes cleanly", async () => {
    const action = defineAction({
      name: "test.unsub",
      run: async () => "x",
    });
    let calls = 0;
    const unsub = subscribe(() => {
      calls += 1;
    });
    await action.dispatch({});
    const after1 = calls;
    unsub();
    await action.dispatch({});
    expect(calls).toBe(after1);
  });
});

describe("pendingFor — public API", () => {
  it("returns the action mid-flight, then empty after completion", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "test.slow",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const p = action.dispatch({});
    expect(pendingFor("test.slow").length).toBe(1);
    expect(pendingFor("test.slow")[0]?.name).toBe("test.slow");
    resolve();
    await p;
    expect(pendingFor("test.slow").length).toBe(0);
  });
});


describe("defineAction — retryable error toast", () => {
  it("retryable: 'always' passes a retry handler to error toast", async () => {
    const action = defineAction({
      name: "test.retry_always",
      run: async () => { throw new ActionError("network glitch"); },
      retryable: "always",
    });
    await action.dispatch({ id: 1 });
    expect(toast.error).toHaveBeenCalledTimes(1);
    const args = vi.mocked(toast.error).mock.calls[0]!;
    expect(args[1]).toBeDefined();
    expect(typeof args[1]?.onClick).toBe("function");
  });

  it("retryable: 'network' includes retry for status 0 / timeout", async () => {
    const a1 = defineAction({
      name: "test.retry_net_status0",
      run: async () => { throw new ActionError("fetch failed", { status: 0 }); },
      retryable: "network",
    });
    await a1.dispatch({});
    expect(vi.mocked(toast.error).mock.calls[0]?.[1]).toBeDefined();

    vi.clearAllMocks();
    const a2 = defineAction({
      name: "test.retry_net_timeout",
      run: async () => { throw new ActionError("Request timed out", { code: "timeout" }); },
      retryable: "network",
    });
    await a2.dispatch({});
    expect(vi.mocked(toast.error).mock.calls[0]?.[1]).toBeDefined();
  });

  it("retryable: 'network' suppresses retry for HTTP 4xx/5xx", async () => {
    const action = defineAction({
      name: "test.retry_net_4xx",
      run: async () => { throw new ActionError("not found", { status: 404 }); },
      retryable: "network",
    });
    await action.dispatch({});
    expect(vi.mocked(toast.error).mock.calls[0]?.[1]).toBeUndefined();
  });

  it("retryable: false (default) never includes retry", async () => {
    const action = defineAction({
      name: "test.retry_default",
      run: async () => { throw new ActionError("oops"); },
    });
    await action.dispatch({});
    expect(vi.mocked(toast.error).mock.calls[0]?.[1]).toBeUndefined();
  });

  it("retry handler re-dispatches the same action with the same args", async () => {
    let attempts = 0;
    const action = defineAction({
      name: "test.retry_redispatch",
      run: async () => {
        attempts += 1;
        if (attempts === 1) throw new ActionError("first", { status: 0 });
        return "ok";
      },
      retryable: "network",
    });
    const result = await action.dispatch({ msg: "hello" });
    expect(result).toBeNull();  // first attempt failed
    const retryFn = vi.mocked(toast.error).mock.calls[0]?.[1]?.onClick as () => void;
    expect(retryFn).toBeDefined();
    retryFn();
    // Wait for the re-dispatch to complete.
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    expect(attempts).toBe(2);
  });

  it("retry suppressed when error: false (no toast at all)", async () => {
    const action = defineAction({
      name: "test.retry_no_toast",
      run: async () => { throw new ActionError("silent"); },
      error: false,
      retryable: "always",
    });
    await action.dispatch({});
    expect(toast.error).not.toHaveBeenCalled();
  });
});
