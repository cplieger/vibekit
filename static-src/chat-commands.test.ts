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
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  newOpID: vi.fn(() => "op-test"),
}));

vi.mock("./session-context.js", () => ({
  getCurrentModel: () => "claude",
}));
import { sendPromptTo, switchModel } from "./chat-commands.js";

beforeEach(() => {
  vi.clearAllMocks();
});

// sendPromptTo is a pure "send once" primitive: it dispatches the prompt
// command and maps the action result to sent/queued/starting/failed. What each
// value MEANS (steer vs busy face vs restore) is submit.ts's job — so there is
// no enqueue/attachment side effect to assert here.
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

  it("returns 'queued' on a plain 409 (converting it to a steer is submit's job)", async () => {
    mockSendPromptDispatch.mockResolvedValue("queued");
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("queued");
  });

  it("passes 'starting' through as a value — the caller branches on it, never on prose", async () => {
    mockSendPromptDispatch.mockResolvedValue("starting");
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("starting");
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
