// @vitest-environment happy-dom
// Tests for sendPrompt (409 queued path).

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

vi.mock("../store.js", () => ({
  get: () => ({ id: "c1", model: "m1" }),
  setThinking: vi.fn(),
  enqueuePrompt: vi.fn(),
  setModel: vi.fn(),
  setSupervisedMode: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: () => 0,
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

import { send as transportSend } from "../transport.js";
import { setThinking, enqueuePrompt } from "../store.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { sendPrompt } from "./chat.js";

const mockSend = vi.mocked(transportSend);

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
});

describe("sendPrompt — 409 queued path", () => {
  it("returns 'queued' on 409 WITHOUT enqueuing (queueing is prompt-queue's job)", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "in-flight" });
    const result = await sendPrompt.dispatch({
      chatID: "c1",
      text: "hello",
      messageID: "m1",
      model: "model-1",
    });
    expect(result).toBe("queued");
    // The action stays a pure send so the drain path can re-send a queued
    // prompt without double-enqueuing. prompt-queue.submitPrompt owns enqueue.
    expect(enqueuePrompt).not.toHaveBeenCalled();
  });

  it("sets thinking optimistically", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await sendPrompt.dispatch({
      chatID: "c1",
      text: "hi",
      messageID: "m2",
      model: "model-1",
    });
    expect(setThinking).toHaveBeenCalledWith("c1", true);
  });
});
