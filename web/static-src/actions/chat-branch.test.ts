// @vitest-environment happy-dom
// Tests for sendPromptAction (409 queued path) and checkoutBranch (optimistic anchor + empty body).

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
  setAutoApproveCrew: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: () => 0,
  setFrozen: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

import { send as transportSend } from "../transport.js";
import { setThinking, enqueuePrompt } from "../store.js";
import { sendPromptAction } from "./chat.js";
import { checkoutBranch } from "./git-branch.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";

const mockSend = vi.mocked(transportSend);
const mockFetch = vi.fn();

beforeEach(() => {
  resetDefine();
  resetRegistry();
  mockSend.mockReset();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("sendPromptAction — 409 queued path", () => {
  it("returns 'queued' and calls enqueuePrompt on 409", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "in-flight" });
    const result = await sendPromptAction.dispatch({
      chatID: "c1",
      text: "hello",
      messageID: "m1",
      agent: "default",
      model: "model-1",
      activeFile: "",
      openFiles: [],
    });
    expect(result).toBe("queued");
    expect(enqueuePrompt).toHaveBeenCalled();
  });

  it("sets thinking optimistically", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await sendPromptAction.dispatch({
      chatID: "c1",
      text: "hi",
      messageID: "m2",
      agent: "default",
      model: "model-1",
      activeFile: "",
      openFiles: [],
    });
    expect(setThinking).toHaveBeenCalledWith("c1", true);
  });
});

describe("checkoutBranch", () => {
  it("optimistically updates anchor element text", async () => {
    mockFetch.mockResolvedValue(new Response("", { status: 200 }));
    const anchor = document.createElement("span");
    anchor.textContent = "main";
    await checkoutBranch.dispatch({ repo: "", branch: "feature", create: false, anchorEl: anchor });
    expect(anchor.textContent).toBe("feature");
  });

  it("returns null on HTTP error", async () => {
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"));
    const result = await checkoutBranch.dispatch({ repo: "", branch: "feature", create: false });
    expect(result).toBeNull();
  });

  it("handles empty 200 body gracefully", async () => {
    mockFetch.mockResolvedValue(new Response("", { status: 200 }));
    const result = await checkoutBranch.dispatch({ repo: "", branch: "dev", create: true });
    expect(result).toBeUndefined();
  });
});
