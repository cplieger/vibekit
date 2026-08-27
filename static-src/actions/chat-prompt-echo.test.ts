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
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  newOpID: vi.fn(() => "op-test"),
}));

// The echo the rescue path looks for. `get` is the one reader this file drives:
// the action reads the chat's message list back to see whether the server's
// `message_appended` landed, so the array a case pushes into IS the echo.
const storeMessages: { id: string }[] = [];

// The TOTAL store mock, plus that one reader. Browser Mode links real ESM, so
// every name any module in this graph imports has to be present — which is what
// the shared helper is for, and what the hand-listed factory that used to live
// here got wrong the moment store.ts gained an export.
vi.mock("../store.js", async () => ({
  ...(await import("../__test-helpers__/store-mock.js")).storeMock,
  get: () => ({ id: "c1", model: "m1", messages: storeMessages }),
  // The echo predicate is the store's now, so the fake has to answer it from the
  // same array the cases push into — that push IS the arriving message_appended.
  hasMessage: (_chatID: string, id: string) => storeMessages.some((m) => m.id === id),
  setThinking: vi.fn(),
  setModel: vi.fn(),
  setSupervisedMode: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: () => 0,
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));

vi.mock("../failure-notice.js", () => ({
  clearFailure: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  // Present-but-inert so real-ESM linking succeeds. The tab projection widened
  // this graph: `apiGetTyped` is how tabs-sync reads `GET /api/tabs`, and other
  // modules reached through it import `apiGet`. Nothing here calls either.
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { send as transportSend } from "../transport.js";
import { setThinking } from "../store.js";
import { clearFailure } from "../failure-notice.js";
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
  vi.mocked(clearFailure).mockReset();
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
    // The failure toast transport already raised must be retracted: it would
    // otherwise stand over a turn the reader can watch streaming.
    expect(clearFailure).toHaveBeenCalledWith("c1");
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
      expect(clearFailure).not.toHaveBeenCalled();
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
    expect(clearFailure).not.toHaveBeenCalled();
  });
});
