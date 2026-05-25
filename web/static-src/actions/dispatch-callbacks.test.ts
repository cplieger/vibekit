// @vitest-environment happy-dom
// Tests for per-dispatch callbacks: onSuccess, onError, onSettled.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { ActionError } from "./error.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
});

describe("dispatch callbacks — onSuccess", () => {
  it("fires with result and args on success", async () => {
    const onSuccess = vi.fn();
    const action = defineAction({ name: "cb.ok", run: async (n: number) => n * 2 });
    await action.dispatch(5, { onSuccess });
    expect(onSuccess).toHaveBeenCalledWith(10, 5);
  });

  it("does not fire on error", async () => {
    const onSuccess = vi.fn();
    const action = defineAction({
      name: "cb.fail",
      run: async () => { throw new ActionError("nope"); },
    });
    await action.dispatch({}, { onSuccess });
    expect(onSuccess).not.toHaveBeenCalled();
  });

  it("throwing in onSuccess is caught and logged", async () => {
    const consoleErr = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const action = defineAction({ name: "cb.throw", run: async () => "ok" });
    const result = await action.dispatch({}, {
      onSuccess: () => { throw new Error("callback boom"); },
    });
    expect(result).toBe("ok"); // dispatch still resolves
    expect(consoleErr).toHaveBeenCalled();
    consoleErr.mockRestore();
  });
});

describe("dispatch callbacks — onError", () => {
  it("fires with error and args on failure", async () => {
    const onError = vi.fn();
    const action = defineAction({
      name: "cb.err",
      run: async () => { throw new ActionError("bad", { status: 500 }); },
    });
    await action.dispatch("arg", { onError });
    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({ message: "bad", status: 500 }),
      "arg",
    );
  });

  it("does not fire on cancellation", async () => {
    const onError = vi.fn();
    const action = defineAction({
      name: "cb.cancel",
      run: (_args, signal) =>
        new Promise<void>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const p = action.dispatch({}, { onError });
    action.cancel();
    await p;
    expect(onError).not.toHaveBeenCalled();
  });

  it("does not fire on success", async () => {
    const onError = vi.fn();
    const action = defineAction({ name: "cb.ok2", run: async () => "fine" });
    await action.dispatch({}, { onError });
    expect(onError).not.toHaveBeenCalled();
  });
});

describe("dispatch callbacks — onCancel", () => {
  it("fires with args on cancellation", async () => {
    const onCancel = vi.fn();
    const action = defineAction({
      name: "cb.oncancel",
      run: (_args, signal) =>
        new Promise<void>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const p = action.dispatch("arg", { onCancel });
    action.cancel();
    await p;
    expect(onCancel).toHaveBeenCalledWith("arg");
  });

  it("does not fire on success", async () => {
    const onCancel = vi.fn();
    const action = defineAction({ name: "cb.oncancel_ok", run: async () => "fine" });
    await action.dispatch({}, { onCancel });
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("does not fire on error", async () => {
    const onCancel = vi.fn();
    const action = defineAction({
      name: "cb.oncancel_err",
      run: async () => { throw new ActionError("nope"); },
    });
    await action.dispatch({}, { onCancel });
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("fires before onSettled", async () => {
    const order: string[] = [];
    const action = defineAction({
      name: "cb.oncancel_order",
      run: (_args, signal) =>
        new Promise<void>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const p = action.dispatch("x", {
      onCancel: () => order.push("onCancel"),
      onSettled: () => order.push("onSettled"),
    });
    action.cancel();
    await p;
    expect(order).toEqual(["onCancel", "onSettled"]);
  });
});

describe("dispatch callbacks — onSettled", () => {
  it("fires on success", async () => {
    const onSettled = vi.fn();
    const action = defineAction({ name: "cb.settled.ok", run: async () => "x" });
    await action.dispatch("a", { onSettled });
    expect(onSettled).toHaveBeenCalledWith("a");
  });

  it("fires on error", async () => {
    const onSettled = vi.fn();
    const action = defineAction({
      name: "cb.settled.err",
      run: async () => { throw new ActionError("fail"); },
    });
    await action.dispatch("b", { onSettled });
    expect(onSettled).toHaveBeenCalledWith("b");
  });

  it("fires on cancellation", async () => {
    const onSettled = vi.fn();
    const action = defineAction({
      name: "cb.settled.cancel",
      run: (_args, signal) =>
        new Promise<void>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const p = action.dispatch("c", { onSettled });
    action.cancel();
    await p;
    expect(onSettled).toHaveBeenCalledWith("c");
  });

  it("fires exactly once per dispatch", async () => {
    const onSettled = vi.fn();
    const action = defineAction({ name: "cb.settled.once", run: async () => "x" });
    await action.dispatch({}, { onSettled });
    expect(onSettled).toHaveBeenCalledTimes(1);
  });

  it("fires even when onSuccess callback throws", async () => {
    const onSettled = vi.fn();
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const action = defineAction({ name: "cb.settled.throw-success", run: async () => "ok" });
    await action.dispatch("d", {
      onSuccess: () => { throw new Error("onSuccess boom"); },
      onSettled,
    });
    expect(onSettled).toHaveBeenCalledWith("d");
    vi.mocked(console.error).mockRestore();
  });

  it("fires even when onError callback throws", async () => {
    const onSettled = vi.fn();
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const action = defineAction({
      name: "cb.settled.throw-error",
      run: async () => { throw new ActionError("fail"); },
    });
    await action.dispatch("e", {
      onError: () => { throw new Error("onError boom"); },
      onSettled,
    });
    expect(onSettled).toHaveBeenCalledWith("e");
    vi.mocked(console.error).mockRestore();
  });
});
