// @vitest-environment happy-dom
import { describe, it, expect, beforeEach, vi } from "vitest";

import { _resetForTest as resetRegistry, record } from "./registry.js";
import {
  initActionConsoleLog,
  _resetForTest as resetConsole,
} from "./console-log.js";
import type { ActionInstance } from "./types.js";

let errSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  resetRegistry();
  resetConsole();
  errSpy = vi.spyOn(console, "error").mockImplementation(() => {});
});

function makeInstance(over: Partial<ActionInstance> = {}): ActionInstance {
  return {
    id: over.id ?? "id-" + Math.random().toString(36).slice(2, 8),
    name: over.name ?? "t.action",
    status: over.status ?? "error",
    args: over.args ?? {},
    dispatchedAt: over.dispatchedAt ?? 1000,
    startedAt: over.startedAt ?? 1000,
    completedAt: over.completedAt ?? 1042,
    ...(over.result !== undefined ? { result: over.result } : {}),
    ...(over.error !== undefined ? { error: over.error } : {}),
    ...(over.attempts !== undefined ? { attempts: over.attempts } : {}),
  } as ActionInstance;
}

describe("action console logger", () => {
  it("logs error transitions to console.error", () => {
    initActionConsoleLog();
    record(
      makeInstance({
        name: "save.do",
        error: { message: "disk full", code: "ENOSPC", status: 500 },
      }),
    );
    expect(errSpy).toHaveBeenCalledTimes(1);
    const call = errSpy.mock.calls[0]?.[0] as string;
    expect(call).toContain("save.do");
    expect(call).toContain("disk full");
    expect(call).toContain("ENOSPC");
    expect(call).toContain("HTTP 500");
    expect(call).toContain("42ms");
  });

  it("ignores non-error transitions", () => {
    initActionConsoleLog();
    record(makeInstance({ status: "success" }));
    record(makeInstance({ status: "pending" }));
    record(makeInstance({ status: "cancelled" }));
    expect(errSpy).not.toHaveBeenCalled();
  });

  it("survives errors with only a message", () => {
    initActionConsoleLog();
    record(makeInstance({ name: "min", error: { message: "minimal" } }));
    expect(errSpy).toHaveBeenCalledTimes(1);
    const msg = errSpy.mock.calls[0]?.[0] as string;
    expect(msg).toContain("min");
    expect(msg).toContain("minimal");
    // No HTTP / code segment when fields are absent.
    expect(msg).not.toContain("HTTP");
  });

  it("includes the error object as second arg for expansion", () => {
    initActionConsoleLog();
    const error = { message: "boom", code: "X", cause: new Error("inner") };
    record(makeInstance({ name: "fail", error }));
    expect(errSpy.mock.calls[0]?.[1]).toBe(error);
  });

  it("does not double-subscribe on re-init", () => {
    initActionConsoleLog();
    initActionConsoleLog();
    record(makeInstance({ name: "once", error: { message: "x" } }));
    expect(errSpy).toHaveBeenCalledTimes(1);
  });

  it("teardown fn stops logging", () => {
    const stop = initActionConsoleLog();
    stop();
    record(makeInstance({ name: "after-stop", error: { message: "x" } }));
    expect(errSpy).not.toHaveBeenCalled();
  });
});
