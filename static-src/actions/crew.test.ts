// @vitest-environment happy-dom
// Tests for crew.ts action configuration: scope, retry, idempotencyKey.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../transport.js", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const orig = await importOriginal<typeof import("../transport.js")>();
  return { ...orig, send: vi.fn() };
});

import { send as transportSend } from "../transport.js";
import { sendMessage } from "./crew.js";
import { getActionLog as recentLog } from "./index.js";

const mockSend = vi.mocked(transportSend);

beforeEach(() => {
  resetActionFramework();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("crew.sendMessage", () => {
  it("has correct action name", () => {
    expect(sendMessage.name).toBe("crew.send_message");
  });

  it("sends message_subagent command on success", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await sendMessage.dispatch({ chatID: "c1", subSessionID: "s1", text: "hello" });
    expect(mockSend).toHaveBeenCalledTimes(1);
    const cmd = mockSend.mock.calls[0]![0] as Record<string, unknown>;
    expect(cmd["type"]).toBe("message_subagent");
    expect(cmd["chat_id"]).toBe("c1");
    const payload = cmd["payload"] as Record<string, unknown>;
    expect(payload["sub_session_id"]).toBe("s1");
    expect(payload["text"]).toBe("hello");
  });

  it("includes idempotency_key in payload", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await sendMessage.dispatch({ chatID: "c1", subSessionID: "s1", text: "hi" });
    const cmd = mockSend.mock.calls[0]![0] as Record<string, unknown>;
    expect(cmd["idempotency_key"]).toEqual(expect.any(String));
  });

  it("retries on network error up to 2 times", async () => {
    mockSend
      .mockResolvedValueOnce({ ok: false, status: 0, error: "net", code: "network" })
      .mockResolvedValueOnce({ ok: false, status: 0, error: "net", code: "network" })
      .mockResolvedValueOnce({ ok: true, status: 200 });

    const p = sendMessage.dispatch({ chatID: "c1", subSessionID: "s1", text: "retry" });
    await vi.advanceTimersByTimeAsync(300); // first retry delay
    await vi.advanceTimersByTimeAsync(600); // second retry delay (300*2)
    await p;
    expect(mockSend).toHaveBeenCalledTimes(3);
    expect(recentLog()[0]?.status).toBe("success");
  });

  it("serializes dispatches with same scope key", async () => {
    const log: string[] = [];
    mockSend.mockImplementation(async (cmd) => {
      const payload = (cmd as Record<string, unknown>)["payload"] as Record<string, string>;

      log.push(`start:${payload["text"]}`);
      await new Promise<void>((r) => setTimeout(r, 50));

      log.push(`end:${payload["text"]}`);
      return { ok: true, status: 200 };
    });

    const p1 = sendMessage.dispatch({ chatID: "c1", subSessionID: "s1", text: "A" });
    const p2 = sendMessage.dispatch({ chatID: "c1", subSessionID: "s1", text: "B" });
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(log).toEqual(["start:A", "end:A", "start:B", "end:B"]);
  });

  it("different scope keys run in parallel", async () => {
    const log: string[] = [];
    mockSend.mockImplementation(async (cmd) => {
      const payload = (cmd as Record<string, unknown>)["payload"] as Record<string, string>;

      log.push(`start:${payload["text"]}`);
      await new Promise<void>((r) => setTimeout(r, 50));

      log.push(`end:${payload["text"]}`);
      return { ok: true, status: 200 };
    });

    const p1 = sendMessage.dispatch({ chatID: "c1", subSessionID: "s1", text: "A" });
    const p2 = sendMessage.dispatch({ chatID: "c2", subSessionID: "s2", text: "B" });
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    // Both start before either ends (parallel)
    expect(log.indexOf("start:B")).toBeLessThan(log.indexOf("end:A"));
  });
});
