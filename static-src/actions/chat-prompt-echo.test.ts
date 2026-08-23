// Tests for sendPrompt's dead-POST/live-SSE rescue (P9 residue): a
// prompt POST that dies while the turn runs on must not surface a
// false failure when the server's message_appended echo proves the
// send was accepted.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../transport.js", () => ({
  send: vi.fn(),
}));

const storeMessages: { id: string }[] = [];
vi.mock("../store.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  setCurrentMode: undefined,
  setTurnDone: undefined,
  setTurnFailed: undefined,
  get: () => ({ id: "c1", model: "m1", messages: storeMessages }),
  setThinking: vi.fn(),
  setModel: vi.fn(),
  setSupervisedMode: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: () => 0,
}));

vi.mock("../send-state.js", () => ({
  clearLastError: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

import { send as transportSend } from "../transport.js";
import { setThinking } from "../store.js";
import { clearLastError } from "../send-state.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { sendPrompt } from "./chat.js";

const mockSend = vi.mocked(transportSend);

const args = {
  chatID: "c1",
  text: "hello",
  messageID: "m-echo-1",
  model: "model-1",
};

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  vi.mocked(clearLastError).mockReset();
  vi.mocked(setThinking).mockReset();
  storeMessages.length = 0;
});

describe("sendPrompt — dead POST with live SSE is non-authoritative", () => {
  it("returns 'sent' when the echo already arrived (network death mid-turn)", async () => {
    // The user message echo landed via SSE long before the POST died.
    storeMessages.push({ id: "m-echo-1" });
    mockSend.mockResolvedValue({
      ok: false,
      status: 0,
      code: "network",
      error: "connection reset",
    });

    const result = await sendPrompt.dispatch(args);

    expect(result).toBe("sent");
    // The blocked send-state painted by transport must be cleaned up.
    expect(clearLastError).toHaveBeenCalled();
    // thinking stays true (the optimistic set), NOT rolled back: the
    // turn is genuinely running; SSE turn_ended will clear it.
    const calls = vi.mocked(setThinking).mock.calls;
    expect(calls).toContainEqual(["c1", true]);
    expect(calls).not.toContainEqual(["c1", false]);
  });

  it("returns 'sent' when the echo arrives within the grace window", async () => {
    vi.useFakeTimers();
    try {
      mockSend.mockResolvedValue({ ok: false, status: 0, code: "timeout", error: "timed out" });
      const p = sendPrompt.dispatch(args);
      // Echo lands 500ms after the POST dies (accept/death race).
      setTimeout(() => storeMessages.push({ id: "m-echo-1" }), 500);
      await vi.runAllTimersAsync();
      expect(await p).toBe("sent");
    } finally {
      vi.useRealTimers();
    }
  });

  it("fails (null) when no echo arrives — genuine send failure", async () => {
    vi.useFakeTimers();
    try {
      mockSend.mockResolvedValue({ ok: false, status: 0, code: "network", error: "unreachable" });
      const p = sendPrompt.dispatch(args);
      await vi.runAllTimersAsync();
      expect(await p).toBeNull();
      // Rollback cleared thinking — the send truly failed.
      expect(vi.mocked(setThinking).mock.calls).toContainEqual(["c1", false]);
      expect(clearLastError).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("an HTTP error status (500) is authoritative — no echo rescue", async () => {
    // Even with the echo present, a real HTTP response IS the server
    // speaking: only connection-death failures (status 0) are rescued.
    storeMessages.push({ id: "m-echo-1" });
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" });
    expect(await sendPrompt.dispatch(args)).toBeNull();
    expect(clearLastError).not.toHaveBeenCalled();
  });
});
