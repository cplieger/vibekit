// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for handlers/messages.ts SSE event routing.
//
// Drives the REAL store and asserts the resulting message / chunk / tool-call
// state (the observable projection the renderer reads). git.ts, tool-schema.ts,
// banner-stack.ts and crew-card.ts stay mocked: they are separate subsystems,
// and a call into them (markGitDirty, clearBannerCodes, setSubagentActivity) is
// a command at the handler's boundary, not store state. isRepoMutatingKind is
// stubbed so the "completed + mutating ⇒ markGitDirty" branch can be exercised
// independently of tool-schema's classification table.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import { setSessions, get } from "../store.js";
import type { Session, Message } from "../types.js";

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

// Capture SSE handlers via shared helper.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
vi.mock("../bus.js", () => createBusMock());

// Import after mocks so messages.ts registers its handlers against the bus mock.
await import("./messages.js");

function makeSession(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: "seeded",
    agent: "",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    auto_approve_crew: false,
    available_modes: [],
    available_models: [],
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    messages: [],
    message_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
    pending_changes: [],
    ...over,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  setSessions([makeSession("chat-1")]);
});

describe("message_appended", () => {
  it("appends the message to the active chat's transcript", () => {
    const msg: Message = { id: "m1", role: "assistant", ts: 0, content: "hi" };
    fireSSE("message_appended", "chat-1", msg);
    expect(get("chat-1")?.messages).toEqual([msg]);
  });

  it("skips an undefined payload (transcript unchanged)", () => {
    fireSSE("message_appended", "chat-1", undefined);
    expect(get("chat-1")?.messages).toEqual([]);
  });

  it("clears agent init-error banners on an agent_switched event and still appends it", () => {
    const msg: Message = { id: "m2", role: "event", ts: 0, event_kind: "agent_switched" };
    fireSSE("message_appended", "chat-1", msg);
    expect(get("chat-1")?.messages).toEqual([msg]);
    expect(mockClearBannerCodes).toHaveBeenCalledWith("chat-1", [
      "agent_not_found",
      "agent_config_error",
    ]);
  });
});

describe("message_chunk", () => {
  it("accumulates a content delta onto the streaming message", () => {
    fireSSE("message_chunk", "chat-1", { message_id: "m1", delta: "hello", block_index: 0 });
    const msg = get("chat-1")?.messages.find((m) => m.id === "m1");
    expect(msg?.content).toBe("hello");
  });

  it("routes reasoning deltas into the reasoning stream, not content", () => {
    fireSSE("message_chunk", "chat-1", {
      message_id: "m1",
      delta: "thinking",
      is_reasoning: true,
      block_index: 0,
    });
    const msg = get("chat-1")?.messages.find((m) => m.id === "m1");
    expect(msg?.reasoning).toBe("thinking");
    expect(msg?.content).toBe("");
  });

  it("skips an undefined payload (no message created)", () => {
    fireSSE("message_chunk", "chat-1", undefined);
    expect(get("chat-1")?.messages).toEqual([]);
  });
});

describe("tool_call_update", () => {
  it("records the tool call in the store and marks git dirty when a repo-mutating call completes", () => {
    mockIsRepoMutatingKind.mockReturnValue(true);
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call: { id: "tc1", title: "write", kind: "write", status: "completed", ts: 0 },
      block_index: 0,
    });
    const tc = get("chat-1")
      ?.messages.find((m) => m.id === "m1")
      ?.tool_calls?.find((c) => c.id === "tc1");
    expect(tc?.status).toBe("completed");
    expect(mockMarkGitDirty).toHaveBeenCalled();
  });

  it("does not mark git dirty for non-mutating tool calls", () => {
    mockIsRepoMutatingKind.mockReturnValue(false);
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call: { id: "tc1", title: "read", kind: "read", status: "completed", ts: 0 },
    });
    expect(mockMarkGitDirty).not.toHaveBeenCalled();
  });

  it("does not mark git dirty for a repo-mutating call that has not completed", () => {
    mockIsRepoMutatingKind.mockReturnValue(true);
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call: { id: "tc1", title: "write", kind: "write", status: "in_progress", ts: 0 },
    });
    expect(mockMarkGitDirty).not.toHaveBeenCalled();
  });
});

describe("subagent_activity", () => {
  // The label-extraction logic + guards live in the handler; setSubagentActivity
  // is the command at the crew-card boundary, so asserting it is correct here.
  it.each<{ desc: string; payload: unknown; expected: [string, string] | null }>([
    {
      desc: "sets activity label from event.label",
      payload: { sub_session_id: "sub-1", event: { label: "Reading file.go" } },
      expected: ["sub-1", "Reading file.go"],
    },
    {
      desc: "falls back to event.title",
      payload: { sub_session_id: "sub-1", event: { title: "Running tests" } },
      expected: ["sub-1", "Running tests"],
    },
    {
      desc: "falls back to event.tool_name",
      payload: { sub_session_id: "sub-1", event: { tool_name: "bash" } },
      expected: ["sub-1", "bash"],
    },
    {
      desc: "skips when sub_session_id is empty",
      payload: { sub_session_id: "", event: { label: "test" } },
      expected: null,
    },
    {
      desc: "skips when sub_session_id is not a string",
      payload: { sub_session_id: 123, event: { label: "test" } },
      expected: null,
    },
    {
      desc: "skips when event is null",
      payload: { sub_session_id: "sub-1", event: null },
      expected: null,
    },
    {
      desc: "skips when event is a non-object primitive",
      payload: { sub_session_id: "sub-1", event: "not-an-object" },
      expected: null,
    },
    {
      desc: "skips when all label fields are empty",
      payload: { sub_session_id: "sub-1", event: { unrelated: "value" } },
      expected: null,
    },
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
