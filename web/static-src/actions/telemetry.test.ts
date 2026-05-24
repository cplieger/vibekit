// @vitest-environment happy-dom
import { describe, it, expect, beforeEach, vi } from "vitest";

import { _resetForTest as resetRegistry, record } from "./registry.js";
import {
  initTelemetry,
  _resetForTest as resetTelemetry,
  type TelemetryEvent,
} from "./telemetry.js";
import type { ActionInstance } from "./types.js";

beforeEach(() => {
  resetRegistry();
  resetTelemetry();
  try { localStorage.clear(); } catch { /* */ }
});

function makeInstance(over: Partial<ActionInstance> = {}): ActionInstance {
  return {
    id: over.id ?? "id-" + Math.random().toString(36).slice(2, 8),
    name: over.name ?? "t.action",
    status: over.status ?? "success",
    args: over.args ?? {},
    startedAt: over.startedAt ?? 1000,
    completedAt: over.completedAt ?? 1042,
    ...(over.result !== undefined ? { result: over.result } : {}),
    ...(over.error !== undefined ? { error: over.error } : {}),
  } as ActionInstance;
}

describe("telemetry adapter", () => {
  it("does not subscribe when opt-out (default)", () => {
    const sink = vi.fn();
    initTelemetry({ sink });
    record(makeInstance());
    expect(sink).not.toHaveBeenCalled();
  });

  it("subscribes when localStorage flag is set", () => {
    localStorage.setItem("vk.telemetry", "1");
    const sink = vi.fn();
    initTelemetry({ sink });
    record(makeInstance({ name: "x.do", status: "success" }));
    return Promise.resolve().then(() => {
      expect(sink).toHaveBeenCalledTimes(1);
      const ev = sink.mock.calls[0]?.[0] as TelemetryEvent;
      expect(ev.name).toBe("x.do");
      expect(ev.status).toBe("success");
      expect(ev.durationMs).toBe(42);
    });
  });

  it("subscribes when force: true bypasses opt-in flag", async () => {
    const sink = vi.fn();
    initTelemetry({ sink, force: true });
    record(makeInstance({ name: "y.do" }));
    await Promise.resolve();
    expect(sink).toHaveBeenCalledTimes(1);
  });

  it("filters out pending events", async () => {
    const sink = vi.fn();
    initTelemetry({ sink, force: true });
    record(makeInstance({ id: "p", name: "p.do", status: "pending" } as Partial<ActionInstance>));
    await Promise.resolve();
    expect(sink).not.toHaveBeenCalled();
  });

  it("includes errorCode + errorStatus when present", async () => {
    const sink = vi.fn();
    initTelemetry({ sink, force: true });
    record(
      makeInstance({
        name: "err.do",
        status: "error",
        error: { message: "nope", code: "E_BAD", status: 503 },
      }),
    );
    await Promise.resolve();
    const ev = sink.mock.calls[0]?.[0] as TelemetryEvent;
    expect(ev.errorCode).toBe("E_BAD");
    expect(ev.errorStatus).toBe(503);
  });

  it("omits errorCode/errorStatus on success events", async () => {
    const sink = vi.fn();
    initTelemetry({ sink, force: true });
    record(makeInstance({ name: "ok.do", status: "success" }));
    await Promise.resolve();
    const ev = sink.mock.calls[0]?.[0] as TelemetryEvent;
    expect(ev.errorCode).toBeUndefined();
    expect(ev.errorStatus).toBeUndefined();
  });

  it("does not double-subscribe on re-init", async () => {
    const sink = vi.fn();
    initTelemetry({ sink, force: true });
    initTelemetry({ sink, force: true });
    record(makeInstance({ name: "once.do" }));
    await Promise.resolve();
    expect(sink).toHaveBeenCalledTimes(1);
  });

  it("teardown fn removes subscription", async () => {
    const sink = vi.fn();
    const stop = initTelemetry({ sink, force: true });
    stop();
    record(makeInstance({ name: "after-stop" }));
    await Promise.resolve();
    expect(sink).not.toHaveBeenCalled();
  });

  it("rejecting sink does not break other listeners", async () => {
    const errSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const sink = vi.fn(() => Promise.reject(new Error("network")));
    initTelemetry({ sink, force: true });
    record(makeInstance({ name: "z.do" }));
    await vi.waitFor(() => {
      expect(errSpy).toHaveBeenCalled();
    });
    errSpy.mockRestore();
  });
});
