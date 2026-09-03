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
import { setSessions, get, liveTurnMessage, tabStatusFor } from "../store.js";
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

// ---------------------------------------------------------------------------
// Streaming evidence latches `thinking`. Before these doors existed the flag
// was only set by this client's OWN sends (sendPromptTo, switchModel) and the
// connect replay's turn_state — so an agent-initiated turn, a wire turn, or a
// prompt sent from another device streamed into a chat whose tab dot sat idle
// the whole time.
// ---------------------------------------------------------------------------

describe("streaming evidence marks the turn live", () => {
  it("a chunk with no subtask id flips thinking on a chat at rest", () => {
    fireSSE("message_chunk", "chat-1", { message_id: "m1", delta: "hi", block_index: 0 });
    expect(get("chat-1")?.thinking).toBe(true);
  });

  it("a SUBAGENT's chunk flips it too: a delegate halts the main agent", () => {
    fireSSE("message_chunk", "chat-1", {
      message_id: "m1",
      delta: "hi",
      block_index: 0,
      agent_subtask_id: "3f2b1c00-0000-4000-8000-000000000000",
    });
    expect(get("chat-1")?.thinking).toBe(true);
  });

  // A chat-parented workflow run executes on the LAUNCHING chat's session, so its
  // steps' frames arrive on this chat's connection — while the launching turn has
  // already ended (run_workflow returns as soon as the run is created). Latching
  // here made the chat's tab dot read "working" for the whole run, and nothing
  // cleared it: a step's own turn_end is dropped server-side by the workflow
  // attribution gate. The RUN's own tab dot carries that liveness.
  it("a workflow STEP's chunk does NOT flip thinking", () => {
    fireSSE("message_chunk", "chat-1", {
      message_id: "m1",
      delta: "the step wrote this",
      block_index: 0,
      agent_subtask_id: "wf:wf-1:root/step",
    });
    expect(get("chat-1")?.thinking).toBe(false);
  });

  it("a step's chunk still lands in the transcript", () => {
    fireSSE("message_created", "chat-1", { id: "m1", role: "assistant", ts: 0, content: "" });
    fireSSE("message_chunk", "chat-1", {
      message_id: "m1",
      delta: "the step wrote this",
      block_index: 0,
      agent_subtask_id: "wf:wf-1:root/step",
    });
    const blocks = get("chat-1")?.messages.find((m) => m.id === "m1")?.blocks;
    expect(blocks?.[0]?.text).toBe("the step wrote this");
  });

  // message_created carries NO attribution, so it cannot tell a step's turn from
  // the chat's own — which is why the latch moved off it entirely. The chunk that
  // follows one frame later is the door, and it can.
  it("message_created alone does not flip thinking", () => {
    fireSSE("message_created", "chat-1", { id: "m1", role: "assistant", ts: 0, content: "" });
    expect(get("chat-1")?.thinking).toBe(false);
  });

  it("message_created still marks the message unpersisted and upserts it", () => {
    fireSSE("message_created", "chat-1", { id: "m1", role: "assistant", ts: 0, content: "" });
    expect(get("chat-1")?.messages.map((m) => m.id)).toEqual(["m1"]);
    expect(liveTurnMessage("chat-1")).toBe("m1");
  });

  // The connect replay. A step-driven turn's snapshot is the ONLY copy of that
  // step's in-flight transcript (nothing persists it), so the event is emitted and
  // marked rather than skipped: apply the message, do not claim the chat is working.
  it("a workflow_step turn_state applies its message without latching thinking", () => {
    fireSSE("turn_state", "chat-1", {
      message: { id: "m1", role: "assistant", ts: 0, content: "step output" },
      chunk_seq: 3,
      workflow_step: true,
    });
    expect(get("chat-1")?.thinking).toBe(false);
    expect(get("chat-1")?.messages.map((m) => m.id)).toEqual(["m1"]);
    expect(liveTurnMessage("chat-1")).toBe("m1");
  });

  it("an ordinary turn_state still latches thinking", () => {
    fireSSE("turn_state", "chat-1", {
      message: { id: "m1", role: "assistant", ts: 0, content: "the agent is working" },
      chunk_seq: 3,
    });
    expect(get("chat-1")?.thinking).toBe(true);
  });

  // The reported symptom, end to end: the launching chat's TAB DOT. Driven through
  // the real handlers rather than by handing `tabStatusFor` a built session, which
  // would pass with the attribution gates deleted. `tab-dot.test.ts` owns the
  // mapping itself; this owns what the handlers feed it.
  it("a chat whose only activity is a workflow step does not read as working", () => {
    fireSSE("message_created", "chat-1", { id: "m1", role: "assistant", ts: 0, content: "" });
    fireSSE("message_chunk", "chat-1", {
      message_id: "m1",
      delta: "the step wrote this",
      block_index: 0,
      agent_subtask_id: "wf:wf-1:root/step",
    });
    fireSSE("turn_state", "chat-1", {
      message: { id: "m1", role: "assistant", ts: 0, content: "the step wrote this" },
      chunk_seq: 1,
      workflow_step: true,
    });
    expect(tabStatusFor(get("chat-1"))).not.toBe("working");
  });

  it("the same chat DOES read as working once its own agent streams", () => {
    fireSSE("message_chunk", "chat-1", {
      message_id: "m2",
      delta: "and now the chat's own reply",
      block_index: 0,
    });
    expect(tabStatusFor(get("chat-1"))).toBe("working");
  });

  it("a chunk on an already-live turn leaves the agent's declared status standing", () => {
    // setThinking(true) clears the previous turn's verdicts AND the agent's
    // declared status, so the latch must fire on the TRANSITION only — an
    // unguarded per-chunk call would erase a mid-turn `waiting_on_user` the
    // moment the next delta landed.
    setSessions([makeSession("chat-1", { thinking: true, agent_status: "waiting_on_user" })]);
    fireSSE("message_chunk", "chat-1", { message_id: "m1", delta: "hi", block_index: 0 });
    expect(get("chat-1")?.agent_status).toBe("waiting_on_user");
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
  // The frame is a DELTA addressed by id, so every case here has to establish the
  // call with a `tool_call` create first: a delta has nothing to apply to, and
  // the channel for a client that missed the beginning is `turn_state`.
  function createCall(kind: string): void {
    fireSSE("tool_call", "chat-1", {
      message_id: "m1",
      tool_call: { id: "tc1", title: "write", kind, status: "pending", ts: 0 },
      block_index: 0,
    });
  }

  function heldCall() {
    return get("chat-1")
      ?.messages.find((m) => m.id === "m1")
      ?.tool_calls?.find((c) => c.id === "tc1");
  }

  it("folds the delta onto the held call and marks git dirty when a repo-mutating call completes", () => {
    mockIsRepoMutatingKind.mockReturnValue(true);
    createCall("write");
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      status: "completed",
    });
    expect(heldCall()?.status).toBe("completed");
    // The create's title survives a delta that did not carry one.
    expect(heldCall()?.title).toBe("write");
    expect(mockMarkGitDirty).toHaveBeenCalled();
  });

  it("appends output rather than replacing it", () => {
    createCall("execute");
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      output_delta: "first\n",
    });
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      output_delta: "second\n",
    });
    expect(heldCall()?.output).toBe("first\nsecond\n");
  });

  it("replaces the output when the frame says so", () => {
    // The terminal's full stream winning over the ACP fragments at completion.
    createCall("execute");
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      output_delta: "a fragment",
    });
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      output_delta: "the whole stream",
      output_replace: true,
      status: "completed",
    });
    expect(heldCall()?.output).toBe("the whole stream");
  });

  it("appends diffs rather than replacing them", () => {
    createCall("edit");
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      diffs_appended: [{ path: "a.go", old_text: "x", new_text: "y" }],
    });
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      diffs_appended: [{ path: "b.go", old_text: "x", new_text: "y" }],
    });
    expect(heldCall()?.diffs?.map((d) => d.path)).toEqual(["a.go", "b.go"]);
  });

  it("reads the completed call's kind from the store, not from the frame", () => {
    // `kind` rides a delta only when THAT frame changed it, and the frame that
    // completes a write normally carries a status alone. Reading it off the frame
    // made every completed edit look like a non-mutating tool.
    mockIsRepoMutatingKind.mockReturnValue(true);
    createCall("write");
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      status: "completed",
    });
    expect(mockIsRepoMutatingKind).toHaveBeenCalledWith("write");
  });

  it("drops a delta for a call it does not hold", () => {
    mockIsRepoMutatingKind.mockReturnValue(true);
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "unknown",
      status: "completed",
    });
    expect(get("chat-1")?.messages).toEqual([]);
    expect(mockMarkGitDirty).not.toHaveBeenCalled();
  });

  it("does not mark git dirty for non-mutating tool calls", () => {
    mockIsRepoMutatingKind.mockReturnValue(false);
    createCall("read");
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      status: "completed",
    });
    expect(mockMarkGitDirty).not.toHaveBeenCalled();
  });

  it("does not mark git dirty for a repo-mutating call that has not completed", () => {
    mockIsRepoMutatingKind.mockReturnValue(true);
    createCall("write");
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      status: "in_progress",
    });
    expect(mockMarkGitDirty).not.toHaveBeenCalled();
  });
});
