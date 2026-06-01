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

vi.mock("./store.js", () => ({
  get: vi.fn(() => ({ model: "claude" })),
  setThinking: vi.fn(),
  setModel: vi.fn(),
  enqueuePrompt: vi.fn(),
  setLastQueuedAttachments: vi.fn(),
}));

vi.mock("./transport.js", () => ({
  send: vi.fn(),
  newMessageID: vi.fn(() => "m-test-123"),
}));

vi.mock("./session-context.js", () => ({
  getCurrentAgent: () => "default",
  getCurrentModel: () => "claude",
}));
vi.mock("./editor-types.js", () => ({
  getActiveFilePath: () => "src/main.ts",
  getOpenFilePaths: () => ["src/main.ts"],
}));
vi.mock("./attachments.js", () => ({ takeAttachments: vi.fn(() => []), addAttachment: vi.fn() }));

import { sendPromptTo, switchModel } from "./chat-commands.js";
import * as store from "./store.js";
import * as attachments from "./attachments.js";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("sendPromptTo", () => {
  it("returns 'sent' on 2xx and calls setThinking(id, true)", async () => {
    mockSendPromptDispatch.mockResolvedValue("sent");
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("sent");
    expect(mockSendPromptDispatch).toHaveBeenCalledWith(
      expect.objectContaining({
        chatID: "chat1",
        text: "hello",
      }),
    );
  });

  it("returns 'queued' on 409 and enqueues prompt", async () => {
    mockSendPromptDispatch.mockResolvedValue("queued");
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("queued");
  });

  it("returns 'queued' on 409 with attachments and calls setLastQueuedAttachments", async () => {
    mockSendPromptDispatch.mockResolvedValue("queued");
    vi.mocked(attachments.takeAttachments).mockReturnValueOnce([{ path: "foo", name: "foo" }]);
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("queued");
    expect(store.setLastQueuedAttachments).toHaveBeenCalledWith("chat1", [
      { path: "foo", name: "foo" },
    ]);
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
