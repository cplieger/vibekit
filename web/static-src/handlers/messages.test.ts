// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for handlers/messages.ts SSE event routing.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

// --- Mocks ---

const mockAppendMessage = vi.fn();
const mockUpsertMessage = vi.fn();
const mockAppendChunk = vi.fn();
const mockUpsertToolCall = vi.fn();

vi.mock("../store.js", () => ({
  appendMessage: mockAppendMessage,
  upsertMessage: mockUpsertMessage,
  appendChunk: mockAppendChunk,
  upsertToolCall: mockUpsertToolCall,
}));

const mockMarkGitDirty = vi.fn();
vi.mock("../git.js", () => ({
  markGitDirty: () => mockMarkGitDirty(),
}));

const mockIsRepoMutatingKind = vi.fn(() => false);
vi.mock("../tool-schema.js", () => ({
  isRepoMutatingKind: mockIsRepoMutatingKind,
}));

const mockClearBannerCodes = vi.fn();
vi.mock("../banner-stack.js", () => ({
  clearBannerCodes: mockClearBannerCodes,
}));

const mockSetSubagentActivity = vi.fn();
vi.mock("../crew-card.js", () => ({
  setSubagentActivity: mockSetSubagentActivity,
}));

// Capture SSE handlers via shared helper
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
vi.mock("../bus.js", () => createBusMock());

// Import after mocks
await import("./messages.js");

describe("message_appended", () => {
  beforeEach(() => vi.clearAllMocks());

  it("appends message to store", () => {
    const msg = { id: "m1", role: "assistant", content: "hi" };
    fireSSE("message_appended", "chat-1", msg);
    expect(mockAppendMessage).toHaveBeenCalledWith("chat-1", msg);
  });

  it("skips undefined payload", () => {
    fireSSE("message_appended", "chat-1", undefined);
    expect(mockAppendMessage).not.toHaveBeenCalled();
  });

  it("clears agent banners on agent_switched event", () => {
    const msg = { id: "m2", role: "assistant", event_kind: "agent_switched" };
    fireSSE("message_appended", "chat-1", msg);
    expect(mockClearBannerCodes).toHaveBeenCalledWith("chat-1", [
      "agent_not_found",
      "agent_config_error",
    ]);
  });
});

describe("message_chunk", () => {
  beforeEach(() => vi.clearAllMocks());

  it("appends chunk delta to store", () => {
    fireSSE("message_chunk", "chat-1", { message_id: "m1", delta: "hello" });
    expect(mockAppendChunk).toHaveBeenCalledWith("chat-1", "m1", "hello", false);
  });

  it("forwards is_reasoning flag for reasoning chunks", () => {
    fireSSE("message_chunk", "chat-1", { message_id: "m1", delta: "thinking", is_reasoning: true });
    expect(mockAppendChunk).toHaveBeenCalledWith("chat-1", "m1", "thinking", true);
  });

  it("skips undefined payload", () => {
    fireSSE("message_chunk", "chat-1", undefined);
    expect(mockAppendChunk).not.toHaveBeenCalled();
  });
});

describe("tool_call_update", () => {
  beforeEach(() => vi.clearAllMocks());

  it("marks git dirty on completed repo-mutating tool call", () => {
    mockIsRepoMutatingKind.mockReturnValue(true);
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call: { id: "tc1", kind: "write_file", status: "completed" },
    });
    expect(mockUpsertToolCall).toHaveBeenCalledWith("chat-1", "m1", {
      id: "tc1",
      kind: "write_file",
      status: "completed",
    });
    expect(mockMarkGitDirty).toHaveBeenCalled();
  });

  it("does not mark git dirty for non-mutating tool calls", () => {
    mockIsRepoMutatingKind.mockReturnValue(false);
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call: { id: "tc1", kind: "read_file", status: "completed" },
    });
    expect(mockMarkGitDirty).not.toHaveBeenCalled();
  });

  it("does not mark git dirty for non-completed tool calls", () => {
    mockIsRepoMutatingKind.mockReturnValue(true);
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call: { id: "tc1", kind: "write_file", status: "running" },
    });
    expect(mockMarkGitDirty).not.toHaveBeenCalled();
  });
});

describe("subagent_activity", () => {
  beforeEach(() => vi.clearAllMocks());

  it.each<{ desc: string; payload: unknown; expected: [string, string] | null }>([
    { desc: "sets activity label from event.label", payload: { sub_session_id: "sub-1", event: { label: "Reading file.go" } }, expected: ["sub-1", "Reading file.go"] },
    { desc: "falls back to event.title", payload: { sub_session_id: "sub-1", event: { title: "Running tests" } }, expected: ["sub-1", "Running tests"] },
    { desc: "falls back to event.tool_name", payload: { sub_session_id: "sub-1", event: { tool_name: "bash" } }, expected: ["sub-1", "bash"] },
    { desc: "skips when sub_session_id is empty", payload: { sub_session_id: "", event: { label: "test" } }, expected: null },
    { desc: "skips when sub_session_id is not a string", payload: { sub_session_id: 123, event: { label: "test" } }, expected: null },
    { desc: "skips when event is null", payload: { sub_session_id: "sub-1", event: null }, expected: null },
    { desc: "skips when event is a non-object primitive", payload: { sub_session_id: "sub-1", event: "not-an-object" }, expected: null },
    { desc: "skips when all label fields are empty", payload: { sub_session_id: "sub-1", event: { unrelated: "value" } }, expected: null },
    { desc: "skips undefined payload", payload: undefined, expected: null },
  ])("$desc", ({ payload, expected }) => {
    fireSSE("subagent_activity", "chat-1", payload);
    if (expected) {
      expect(mockSetSubagentActivity).toHaveBeenCalledWith(...expected);
    } else {
      expect(mockSetSubagentActivity).not.toHaveBeenCalled();
    }
  });
});
