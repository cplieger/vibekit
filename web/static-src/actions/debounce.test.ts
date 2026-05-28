// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { debouncedDispatch } from "./debounce.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.useFakeTimers();
});

function makeAction() {
  const run = vi.fn(async (args: string) => args);
  const action = defineAction({ name: "test.debounce", run });
  return { action, run };
}

describe("debouncedDispatch — trailing (default)", () => {
  it("dispatches after the wait period", () => {
    const { action, run } = makeAction();
    const debounced = debouncedDispatch(action, { wait: 100 });
    debounced("a");
    expect(run).not.toHaveBeenCalled();
    vi.advanceTimersByTime(100);
    expect(run).toHaveBeenCalledWith("a", expect.anything(), expect.anything());
  });

  it("coalesces rapid calls — only last args dispatched", () => {
    const { action, run } = makeAction();
    const debounced = debouncedDispatch(action, { wait: 50 });
    debounced("a");
    debounced("b");
    debounced("c");
    vi.advanceTimersByTime(50);
    expect(run).toHaveBeenCalledTimes(1);
    expect(run).toHaveBeenCalledWith("c", expect.anything(), expect.anything());
  });

  it("cancel() prevents the pending dispatch", () => {
    const { action, run } = makeAction();
    const debounced = debouncedDispatch(action, { wait: 100 });
    debounced("a");
    debounced.cancel();
    vi.advanceTimersByTime(200);
    expect(run).not.toHaveBeenCalled();
  });

  it("flush() fires immediately with pending args", () => {
    const { action, run } = makeAction();
    const debounced = debouncedDispatch(action, { wait: 100 });
    debounced("x");

    debounced.flush();
    expect(run).toHaveBeenCalledWith("x", expect.anything(), expect.anything());
  });

  it("flush(args) overrides pending args", () => {
    const { action, run } = makeAction();
    const debounced = debouncedDispatch(action, { wait: 100 });
    debounced("old");

    debounced.flush("override");
    expect(run).toHaveBeenCalledWith("override", expect.anything(), expect.anything());
  });

  it("isPending() reflects scheduled state", () => {
    const { action } = makeAction();
    const debounced = debouncedDispatch(action, { wait: 100 });
    expect(debounced.isPending()).toBe(false);
    debounced("a");
    expect(debounced.isPending()).toBe(true);
    vi.advanceTimersByTime(100);
    expect(debounced.isPending()).toBe(false);
  });

  it("flush() is no-op when nothing pending and no args", () => {
    const { action, run } = makeAction();
    const debounced = debouncedDispatch(action, { wait: 100 });

    debounced.flush();
    expect(run).not.toHaveBeenCalled();
  });
});

describe("debouncedDispatch — leading", () => {
  it("fires immediately on first call", () => {
    const { action, run } = makeAction();
    const debounced = debouncedDispatch(action, { wait: 100, leading: true });
    debounced("first");
    expect(run).toHaveBeenCalledWith("first", expect.anything(), expect.anything());
  });

  it("suppresses calls within the cooldown window", () => {
    const { action, run } = makeAction();
    const debounced = debouncedDispatch(action, { wait: 100, leading: true });
    debounced("a");
    debounced("b");
    debounced("c");
    expect(run).toHaveBeenCalledTimes(1);
    expect(run).toHaveBeenCalledWith("a", expect.anything(), expect.anything());
  });

  it("fires trailing with last suppressed args after cooldown", () => {
    const { action, run } = makeAction();
    const debounced = debouncedDispatch(action, { wait: 100, leading: true });
    debounced("a");
    debounced("b");
    vi.advanceTimersByTime(100);
    expect(run).toHaveBeenCalledTimes(2);
    expect(run).toHaveBeenLastCalledWith("b", expect.anything(), expect.anything());
  });

  it("cancel after leading fire prevents trailing fire", () => {
    const { action, run } = makeAction();
    const debounced = debouncedDispatch(action, { wait: 100, leading: true });
    debounced("a");
    debounced("b");
    debounced.cancel();
    vi.advanceTimersByTime(200);
    expect(run).toHaveBeenCalledTimes(1); // only the leading fire
  });
});
