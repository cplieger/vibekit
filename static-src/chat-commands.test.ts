// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

const { mockSendPromptDispatch, mockSwitchModelDispatch } = vi.hoisted(() => ({
  mockSendPromptDispatch: vi.fn(),
  mockSwitchModelDispatch: vi.fn(),
}));

vi.mock("./actions/chat.js", () => ({
  sendPrompt: { dispatch: mockSendPromptDispatch },
  switchModel: { dispatch: mockSwitchModelDispatch },
  resolvePendingChange: { dispatch: vi.fn() },
}));

vi.mock("./transport.js", () => ({
  send: vi.fn(),
  newMessageID: vi.fn(() => "m-test-123"),
}));

vi.mock("./session-context.js", () => ({
  getCurrentModel: () => "claude",
}));
vi.mock("./editor-types.js", () => ({
  getActiveFilePath: () => "src/main.ts",
  getOpenFilePaths: () => ["src/main.ts"],
}));

import { sendPromptTo, switchModel } from "./chat-commands.js";

beforeEach(() => {
  vi.clearAllMocks();
});

// sendPromptTo is a pure "send once" primitive now: it dispatches the prompt
// command and maps the action result to sent/queued/failed. It does NOT own
// the queue (that's prompt-queue.ts) — so there is no enqueue/attachment side
// effect to assert here.
describe("sendPromptTo", () => {
  it("returns 'sent' on 2xx and forwards chat/text/model to the action", async () => {
    mockSendPromptDispatch.mockResolvedValue("sent");
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("sent");
    expect(mockSendPromptDispatch).toHaveBeenCalledWith(
      expect.objectContaining({
        chatID: "chat1",
        text: "hello",
        model: "claude",
      }),
    );
    // No attachments passed → the key is omitted (exactOptionalPropertyTypes),
    // not sent as `undefined`.
    expect(mockSendPromptDispatch.mock.calls[0]?.[0]).not.toHaveProperty("attachments");
  });

  it("returns 'queued' on 409 (no local enqueue — that's prompt-queue's job)", async () => {
    mockSendPromptDispatch.mockResolvedValue("queued");
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("queued");
  });

  it("forwards explicit opts (model + attachments) to the action", async () => {
    mockSendPromptDispatch.mockResolvedValue("sent");
    const att = [{ path: "foo.ts", name: "foo.ts" }];
    await sendPromptTo("chat1", "hello", { model: "gpt-5.5", attachments: att });
    expect(mockSendPromptDispatch).toHaveBeenCalledWith(
      expect.objectContaining({ model: "gpt-5.5", attachments: att }),
    );
  });

  it("returns 'failed' on null result (action error)", async () => {
    mockSendPromptDispatch.mockResolvedValue(null);
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("failed");
  });
});

describe("switchModel", () => {
  it("returns false immediately for empty chatID", async () => {
    const result = await switchModel("", "gpt-4");
    expect(result).toBe(false);
    expect(mockSwitchModelDispatch).not.toHaveBeenCalled();
  });

  it("returns true on successful switch", async () => {
    mockSwitchModelDispatch.mockResolvedValue(true);
    const result = await switchModel("chat1", "gpt-4");
    expect(result).toBe(true);
    expect(mockSwitchModelDispatch).toHaveBeenCalledWith({ chatID: "chat1", model: "gpt-4" });
  });
});
