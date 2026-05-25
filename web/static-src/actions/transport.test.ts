// @vitest-environment happy-dom
// Tests for transportAction error classification (B1/B2).

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../transport.js", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../transport.js")>();
  return {
    ...orig,
    send: vi.fn(),
  };
});

import { send as transportSend } from "../transport.js";
import { transportAction } from "./transport.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";

const mockSend = vi.mocked(transportSend);

beforeEach(() => {
  resetDefine();
  resetRegistry();
});

const testAction = () =>
  transportAction<{ chatID: string }>({
    name: "test.transport",
    command: ({ chatID }) => ({ type: "cancel", chat_id: chatID }),
    error: "Test failed",
  });

describe("transportAction error classification", () => {
  it("classifies timeout via r.code (not substring)", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "Request timed out", code: "timeout" });
    const action = testAction();
    await action.dispatch({ chatID: "c1" });
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
    expect(log[0]?.error?.code).toBe("timeout");
  });

  it("classifies cancelled via r.code (not signal.aborted)", async () => {
    // When the server returns code='cancelled' but the client signal is NOT
    // aborted, the dispatcher records as "error" (only signal.aborted triggers
    // the "cancelled" status path). The error carries code='cancelled'.
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "Request cancelled", code: "cancelled" });
    const action = testAction();
    await action.dispatch({ chatID: "c1" });
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
    expect(log[0]?.error?.code).toBe("cancelled");
  });

  it("classifies network error via r.code", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "Failed to fetch", code: "network" });
    const action = testAction();
    await action.dispatch({ chatID: "c1" });
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
    expect(log[0]?.error?.code).toBe("network");
  });

  it("HTTP errors without code throw with status only", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "Internal Server Error" });
    const action = testAction();
    await action.dispatch({ chatID: "c1" });
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
    expect(log[0]?.error?.status).toBe(500);
    expect(log[0]?.error?.code).toBeUndefined();
  });

  it("signal.aborted takes precedence", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "Request cancelled", code: "network" });
    const action = testAction();
    const promise = action.dispatch({ chatID: "c1" });
    action.cancel();
    await promise;
    const log = recentLog();
    expect(log[0]?.status).toBe("cancelled");
  });
});
