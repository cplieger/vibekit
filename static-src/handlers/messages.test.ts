// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for handlers/messages.ts SSE event routing.
//
// Drives the REAL store and asserts the resulting message / chunk / tool-call
// state (the observable projection the renderer reads). git.ts and
// tool-schema.ts stay mocked: they are separate subsystems, and a call into
// them (markGitDirty) is a command at the handler's boundary, not store state.
// isRepoMutatingKind is stubbed so the "completed + mutating ⇒ markGitDirty"
// branch can be exercised independently of tool-schema's classification table.
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

// Capture SSE handlers via shared helper.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
vi.mock("../bus.js", () => createBusMock());

// Import after mocks so messages.ts registers its handlers against the bus mock.
await import("./messages.js");

function makeSession(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: "seeded",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
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
    const stored = get("chat-1")?.messages;
    expect(stored).toHaveLength(1);
    expect(stored?.[0]?.id).toBe("m1");
    expect(stored?.[0]?.content).toBe("hi");
    // ingestMessage normalizes an assistant message into the canonical block
    // model so the renderer has one path.
    expect(stored?.[0]?.blocks).toEqual([{ type: "text", text: "hi" }]);
  });

  it("skips an undefined payload (transcript unchanged)", () => {
    fireSSE("message_appended", "chat-1", undefined);
    expect(get("chat-1")?.messages).toEqual([]);
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

describe("code_references", () => {
  it("attaches the reference list to an existing assistant message", () => {
    const msg: Message = { id: "m1", role: "assistant", ts: 0, content: "code" };
    fireSSE("message_appended", "chat-1", msg);
    fireSSE("code_references", "chat-1", {
      message_id: "m1",
      references: [
        { license_name: "MIT", repository: "github.com/a/b", url: "https://example.com" },
      ],
    });
    const got = get("chat-1")?.messages.find((m) => m.id === "m1");
    expect(got?.code_references).toEqual([
      { license_name: "MIT", repository: "github.com/a/b", url: "https://example.com" },
    ]);
  });

  it("no-ops for an unknown message id (no message created)", () => {
    fireSSE("code_references", "chat-1", {
      message_id: "nope",
      references: [{ license_name: "MIT" }],
    });
    expect(get("chat-1")?.messages).toEqual([]);
  });

  it("skips an undefined payload", () => {
    const msg: Message = { id: "m1", role: "assistant", ts: 0, content: "code" };
    fireSSE("message_appended", "chat-1", msg);
    fireSSE("code_references", "chat-1", undefined);
    expect(get("chat-1")?.messages.find((m) => m.id === "m1")?.code_references).toBeUndefined();
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
