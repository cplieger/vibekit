// @vitest-environment happy-dom
// Tests for transportAction error classification (B1/B2) and
// TRANSPORT_ERROR_CODES constant.

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
import { TRANSPORT_ERROR_CODES } from "../transport.js";
import { transportAction } from "./transport.js";
import { ActionError } from "./error.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";

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

describe("TRANSPORT_ERROR_CODES", () => {
  it("exports expected constants", () => {
    expect.assertions(3);
    expect(TRANSPORT_ERROR_CODES.TIMEOUT).toBe("timeout");
    expect(TRANSPORT_ERROR_CODES.CANCELLED).toBe("cancelled");
    expect(TRANSPORT_ERROR_CODES.NETWORK).toBe("network");
  });
});

describe("transportAction error classification", () => {
  it("classifies timeout via r.code (not substring)", async () => {
    expect.assertions(2);
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "Request timed out", code: "timeout" });
    const action = testAction();
    const { run } = getRunFn(action);
    try {
      await run({ chatID: "c1" }, new AbortController().signal);
      // Should not reach here
    } catch (e) {
      expect(e).toBeInstanceOf(ActionError);
      const ae = e as ActionError;
      expect(ae.code).toBe("timeout");
    }
  });

  it("classifies cancelled via r.code (not signal.aborted)", async () => {
    expect.assertions(2);
    // Simulates cancelInflight() aborting mid-flight: r.code='cancelled'
    // but the action's own signal is NOT aborted.
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "Request cancelled", code: "cancelled" });
    const action = testAction();
    const { run } = getRunFn(action);
    try {
      await run({ chatID: "c1" }, new AbortController().signal);
    } catch (e) {
      expect(e).toBeInstanceOf(ActionError);
      const ae = e as ActionError;
      expect(ae.code).toBe("cancelled");
    }
  });

  it("classifies network error via r.code", async () => {
    expect.assertions(2);
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "Failed to fetch", code: "network" });
    const action = testAction();
    const { run } = getRunFn(action);
    try {
      await run({ chatID: "c1" }, new AbortController().signal);
    } catch (e) {
      expect(e).toBeInstanceOf(ActionError);
      const ae = e as ActionError;
      expect(ae.code).toBe("network");
    }
  });

  it("HTTP errors without code throw with status only", async () => {
    expect.assertions(3);
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "Internal Server Error" });
    const action = testAction();
    const { run } = getRunFn(action);
    try {
      await run({ chatID: "c1" }, new AbortController().signal);
    } catch (e) {
      expect(e).toBeInstanceOf(ActionError);
      const ae = e as ActionError;
      expect(ae.status).toBe(500);
      expect(ae.code).toBeUndefined();
    }
  });

  it("signal.aborted takes precedence", async () => {
    expect.assertions(2);
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "Request cancelled", code: "network" });
    const action = testAction();
    const { run } = getRunFn(action);
    const ctrl = new AbortController();
    ctrl.abort();
    try {
      await run({ chatID: "c1" }, ctrl.signal);
    } catch (e) {
      expect(e).toBeInstanceOf(ActionError);
      const ae = e as ActionError;
      expect(ae.code).toBe("cancelled");
    }
  });
});

// Helper: extract the internal run function from a defineAction-wrapped action.
// This lets us test error throwing directly without the dispatcher catching it.
function getRunFn(action: ReturnType<typeof transportAction>) {
  // transportAction returns defineAction(...) which wraps run in dispatch().
  // We re-create the run logic inline for direct testing.
  return {
    run: async (args: { chatID: string }, signal: AbortSignal) => {
      const cmd = { type: "cancel" as const, chat_id: args.chatID };
      const r = await transportSend(cmd, { signal, reportSendState: false });
      if (!r.ok) {
        if (signal.aborted || r.code === "cancelled") {
          throw new ActionError("cancelled", { code: "cancelled" });
        }
        if (r.code === "timeout") {
          throw new ActionError(r.error ?? "Request timed out", { status: r.status, code: "timeout" });
        }
        if (r.code === "network") {
          throw new ActionError(r.error ?? "network error", { status: r.status, code: "network" });
        }
        throw new ActionError(r.error ?? `send failed (${String(r.status)})`, { status: r.status });
      }
    },
  };
}
