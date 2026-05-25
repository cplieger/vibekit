// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

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
vi.mock("./attachments.js", () => ({ takeAttachments: () => [], addAttachment: vi.fn() }));

import { sendPromptTo, switchModel } from "./chat-commands.js";
import * as store from "./store.js";
import * as transport from "./transport.js";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("sendPromptTo", () => {
  it("returns 'sent' on 2xx and calls setThinking(id, true)", async () => {
    vi.mocked(transport.send).mockResolvedValue({ ok: true, status: 200 });
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("sent");
    expect(store.setThinking).toHaveBeenCalledWith("chat1", true);
    expect(store.setThinking).toHaveBeenCalledTimes(1);
  });

  it("returns 'queued' on 409 and enqueues prompt", async () => {
    vi.mocked(transport.send).mockResolvedValue({ ok: false, status: 409 });
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("queued");
    expect(store.enqueuePrompt).toHaveBeenCalledWith("chat1", "hello");
  });

  it("returns 'failed' on 500 and clears thinking", async () => {
    vi.mocked(transport.send).mockResolvedValue({ ok: false, status: 500 });
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("failed");
    expect(store.setThinking).toHaveBeenCalledWith("chat1", false);
  });
});

describe("switchModel", () => {
  it("returns false immediately for empty chatID", async () => {
    const result = await switchModel("", "gpt-4");
    expect(result).toBe(false);
    expect(transport.send).not.toHaveBeenCalled();
  });

  it("returns true on successful switch", async () => {
    vi.mocked(transport.send).mockResolvedValue({ ok: true, status: 200 });
    const result = await switchModel("chat1", "gpt-4");
    expect(result).toBe(true);
    // B2 fix: switchModelAction no longer touches thinking state —
    // it's owned by sendPromptAction; the model switcher button uses
    // bindLoadingState for its own indicator.
    expect(store.setThinking).not.toHaveBeenCalled();
  });
});
