// @vitest-environment happy-dom
// Tests verifying that retryable transport actions propagate idempotency keys
// across retries and that error codes propagate correctly through the chain.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../transport.js", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../transport.js")>();
  return { ...orig, send: vi.fn() };
});

vi.mock("../store.js", () => ({
  get: vi.fn(), setThinking: vi.fn(), setSupervisedMode: vi.fn(),
  setAutoApproveCrew: vi.fn(), enqueuePrompt: vi.fn(), removeChat: vi.fn(),
  reinsertSession: vi.fn(), indexOfSession: vi.fn(() => 0), setFrozen: vi.fn(),
  setModel: vi.fn(),
}));

import { send as transportSend } from "../transport.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import {
  resolveAllPending,
  resolvePendingChange,
  respondPermission,
  restoreCheckpoint,
} from "./chat.js";

const mockSend = vi.mocked(transportSend);

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.useFakeTimers();
  mockSend.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("retryable transport actions — idempotency key across retries", () => {
  it("resolveAllPending sends idempotency_key and reuses it on retry", async () => {
    let attempt = 0;
    mockSend.mockImplementation(async () => {
      attempt++;
      if (attempt < 3) return { ok: false, status: 0, error: "network", code: "network" };
      return { ok: true, status: 200 };
    });

    const p = resolveAllPending.dispatch({ chatID: "c1", action: "accept" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;

    expect(mockSend).toHaveBeenCalledTimes(3);
    const key1 = (mockSend.mock.calls[0]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    const key2 = (mockSend.mock.calls[1]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    const key3 = (mockSend.mock.calls[2]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    expect(key1).toEqual(expect.any(String));
    expect(key1).toBe(key2);
    expect(key2).toBe(key3);
  });

  it("resolvePendingChange sends idempotency_key and reuses it on retry", async () => {
    let attempt = 0;
    mockSend.mockImplementation(async () => {
      attempt++;
      if (attempt < 3) return { ok: false, status: 0, error: "timeout", code: "timeout" };
      return { ok: true, status: 200 };
    });

    const p = resolvePendingChange.dispatch({ chatID: "c1", toolCallID: "tc1", action: "accept" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;

    expect(mockSend).toHaveBeenCalledTimes(3);
    const key1 = (mockSend.mock.calls[0]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    const key2 = (mockSend.mock.calls[1]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    expect(key1).toBe(key2);
  });

  it("respondPermission sends idempotency_key and reuses it on retry", async () => {
    let attempt = 0;
    mockSend.mockImplementation(async () => {
      attempt++;
      if (attempt < 2) return { ok: false, status: 0, error: "network", code: "network" };
      return { ok: true, status: 200 };
    });

    const p = respondPermission.dispatch({ chatID: "c1", requestID: 7, optionID: "allow" });
    await vi.advanceTimersByTimeAsync(300);
    await p;

    expect(mockSend).toHaveBeenCalledTimes(2);
    const key1 = (mockSend.mock.calls[0]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    const key2 = (mockSend.mock.calls[1]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    expect(key1).toEqual(expect.any(String));
    expect(key1).toBe(key2);
  });

  it("restoreCheckpoint sends idempotency_key and reuses it on retry", async () => {
    let attempt = 0;
    mockSend.mockImplementation(async () => {
      attempt++;
      if (attempt < 2) return { ok: false, status: 0, error: "network", code: "network" };
      return { ok: true, status: 200 };
    });

    const p = restoreCheckpoint.dispatch({ chatID: "c1", tag: "v1" });
    await vi.advanceTimersByTimeAsync(300);
    await p;

    expect(mockSend).toHaveBeenCalledTimes(2);
    const key1 = (mockSend.mock.calls[0]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    const key2 = (mockSend.mock.calls[1]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    expect(key1).toBe(key2);
  });
});

describe("retryable transport actions — error code propagation", () => {
  it("server error code propagates to registry entry", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 429, error: "rate limited", code: "rate_limit" });

    const p = resolveAllPending.dispatch({ chatID: "c1", action: "reject" });
    // 429 is transient → retries up to 2 times with 300ms delay
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;

    const entry = recentLog()[0]!;
    expect(entry.status).toBe("error");
    expect(entry.error?.message).toBe("rate limited");
    expect(entry.error?.status).toBe(429);
    expect(entry.error?.code).toBe("rate_limit");
  });

  it("network code is retried, non-network code is not", async () => {
    // server_rejected is in PERMANENT_FAILURE_CODES — should NOT retry
    mockSend.mockResolvedValue({ ok: false, status: 400, error: "bad request", code: "server_rejected" });

    const p = resolvePendingChange.dispatch({ chatID: "c1", toolCallID: "tc1", action: "reject" });
    await vi.advanceTimersByTimeAsync(1000); // give time for potential retries
    await p;

    // Only 1 call — permanent failure code prevents retry
    expect(mockSend).toHaveBeenCalledTimes(1);
    expect(recentLog()[0]?.error?.code).toBe("server_rejected");
  });

  it("timeout code triggers retry", async () => {
    let attempt = 0;
    mockSend.mockImplementation(async () => {
      attempt++;
      if (attempt === 1) return { ok: false, status: 0, error: "timed out", code: "timeout" };
      return { ok: true, status: 200 };
    });

    const p = respondPermission.dispatch({ chatID: "c1", requestID: 1, optionID: "deny" });
    await vi.advanceTimersByTimeAsync(300);
    await p;

    expect(mockSend).toHaveBeenCalledTimes(2);
    expect(recentLog()[0]?.status).toBe("success");
  });
});
