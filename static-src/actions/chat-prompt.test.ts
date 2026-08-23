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

const { mockGet } = vi.hoisted(() => ({
  mockGet: vi.fn(() => ({ id: "c1", model: "m1" }) as Record<string, unknown> | undefined),
}));
vi.mock("../store.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  activeSession: undefined,
  getActive: undefined,
  getActiveId: undefined,
  setCurrentMode: undefined,
  get: mockGet,
  setThinking: vi.fn(),
  setTurnFailed: vi.fn(),
  setTurnDone: vi.fn(),
  recordSteerQueued: vi.fn(),
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
import { setThinking, setTurnFailed, setTurnDone, recordSteerQueued } from "../store.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { sendPrompt } from "./chat.js";

const mockSend = vi.mocked(transportSend);

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  mockGet.mockReturnValue({ id: "c1", model: "m1" });
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
    // The action stays a PURE send: on 409 it reports and does nothing else.
    // submit.ts owns what a busy chat means (it steers into the running turn),
    // and the steer chip is written only by the server's own frame — so an
    // action that recorded one here would put a chip on screen for a message
    // KAS may yet refuse.
    expect(recordSteerQueued).not.toHaveBeenCalled();
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

// The optimistic write is `setThinking(chatID, true)`, and that call CLEARS the
// two outcome latches — starting a turn is what invalidates the previous turn's
// verdict. So a rollback that restored `thinking: false` alone erased a failure
// or a finished-while-away mark the reader had not seen yet, and the erasing
// event was an ordinary rejected prompt: a 400, a 413, a dead POST.
describe("sendPrompt — rollback restores what the optimistic write cleared", () => {
  it("re-latches a failure the rejected send wiped", async () => {
    mockGet.mockReturnValue({ id: "c1", model: "m1", turn_failed: true });
    mockSend.mockResolvedValue({ ok: false, status: 400, error: "bad request" });

    await sendPrompt.dispatch({ chatID: "c1", text: "hi", messageID: "m3", model: "model-1" });

    expect(setThinking).toHaveBeenCalledWith("c1", true);
    expect(setThinking).toHaveBeenLastCalledWith("c1", false);
    expect(setTurnFailed).toHaveBeenCalledWith("c1");
  });

  it("re-latches a finished-while-away mark the same way", async () => {
    mockGet.mockReturnValue({ id: "c1", model: "m1", turn_done: true });
    mockSend.mockResolvedValue({ ok: false, status: 413, error: "too large" });

    await sendPrompt.dispatch({ chatID: "c1", text: "hi", messageID: "m4", model: "model-1" });

    expect(setTurnDone).toHaveBeenCalledWith("c1");
  });

  it("re-latches nothing when there was nothing latched", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 400, error: "bad request" });

    await sendPrompt.dispatch({ chatID: "c1", text: "hi", messageID: "m5", model: "model-1" });

    expect(setTurnFailed).not.toHaveBeenCalled();
    expect(setTurnDone).not.toHaveBeenCalled();
  });

  it("re-latches nothing on a send that succeeded", async () => {
    mockGet.mockReturnValue({ id: "c1", model: "m1", turn_failed: true });
    mockSend.mockResolvedValue({ ok: true, status: 200 });

    await sendPrompt.dispatch({ chatID: "c1", text: "hi", messageID: "m6", model: "model-1" });

    // The turn started, so the cleared verdict is correctly gone: restoring it
    // here would paint a failure over live work.
    expect(setTurnFailed).not.toHaveBeenCalled();
  });
});
