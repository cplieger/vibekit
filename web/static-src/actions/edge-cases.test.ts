// @vitest-environment happy-dom
// Cycle-3 edge-case tests: abort-vs-retry, transport type preservation,
// mcp-state coalescing after teardown.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../transport.js", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../transport.js")>();
  return { ...orig, send: vi.fn() };
});

import { send as transportSend } from "../transport.js";
import { transportAction } from "./transport.js";
import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { ActionError } from "./error.js";

const mockSend = vi.mocked(transportSend);

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

// ===========================================================================
// Edge case 1: runWithRetry does NOT retry when signal.aborted is true,
// even if the error IS retry-class (network/timeout). This covers the
// retention.ts scenario where CancellableSlot.start() aborts the previous
// signal mid-flight and the fetch throws a network error.
// ===========================================================================

describe("runWithRetry: no retry on abort even for retry-class errors", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("signal aborted externally before run() throws — no retry", async () => {
    let attempts = 0;
    const action = defineAction<void, string>({
      name: "test.abort_no_retry",
      retryable: "network",
      retry: { count: 3, delay: 100 },
      error: false,
      run: async (_args, signal) => {
        attempts++;
        // Simulate: signal was aborted externally (e.g. CancellableSlot.start())
        // but run() throws a network error (not AbortError).
        if (attempts === 1) {
          await new Promise<void>((resolve) => {
            signal.addEventListener("abort", () => resolve(), { once: true });
          });
          throw new ActionError("network error", { code: "network" });
        }
        return "should not reach";
      },
    });

    const p = action.dispatch();
    action.cancel();
    await vi.advanceTimersByTimeAsync(1000);
    const result = await p;

    expect(result).toBeNull();
    expect(attempts).toBe(1); // No retry despite retry-class error
    expect(recentLog()[0]?.status).toBe("cancelled");
  });

  it("signal aborted during run() that throws AbortError — classified as cancelled, not retried", async () => {
    let attempts = 0;
    const action = defineAction<void, string>({
      name: "test.abort_error_no_retry",
      retryable: "network",
      retry: { count: 3, delay: 100 },
      error: false,
      run: async (_args, signal) => {
        attempts++;
        return new Promise<string>((_resolve, reject) => {
          signal.addEventListener("abort", () => {
            reject(new DOMException("aborted", "AbortError"));
          }, { once: true });
        });
      },
    });

    const p = action.dispatch();
    await vi.advanceTimersByTimeAsync(0);
    action.cancel();
    await vi.advanceTimersByTimeAsync(1000);
    const result = await p;

    expect(result).toBeNull();
    expect(attempts).toBe(1);
    expect(recentLog()[0]?.status).toBe("cancelled");
  });
});

// ===========================================================================
// Edge case 2: transportAction spread preserves the `type` literal field
// from TypedCommand. The spread `{ ...raw, payload: { ... } }` must not
// lose the discriminant.
// ===========================================================================

describe("transportAction: spread preserves type field", () => {
  it("idempotencyKey spread preserves the command type field", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const action = transportAction<{ chatID: string }>({
      name: "test.type_preserved",
      idempotencyKey: true,
      command: ({ chatID }) => ({
        type: "cancel" as const,
        chat_id: chatID,
      }),
      error: false,
    });

    await action.dispatch({ chatID: "c1" });
    const sentCmd = mockSend.mock.calls[0]![0] as { type: string };
    expect(sentCmd.type).toBe("cancel");
  });

  it("spread on TypedCommand with payload merges idempotency_key without losing type", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const action = transportAction<{ chatID: string; model: string }>({
      name: "test.type_with_payload",
      idempotencyKey: true,
      command: ({ chatID, model }) => ({
        type: "switch_model" as const,
        chat_id: chatID,
        payload: { model },
      }),
      error: false,
    });

    await action.dispatch({ chatID: "c1", model: "gpt-4" });
    const sentCmd = mockSend.mock.calls[0]![0] as {
      type: string;
      payload: { model: string; idempotency_key: string };
    };
    expect(sentCmd.type).toBe("switch_model");
    expect(sentCmd.payload.model).toBe("gpt-4");
    expect(sentCmd.payload.idempotency_key).toEqual(expect.any(String));
  });
});
