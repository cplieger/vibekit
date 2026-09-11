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
import {
  setSessions,
  get,
  liveTurnMessage,
  tabStatusFor,
  isTruncatedSnapshot,
  clearTruncatedSnapshots,
  setThinking,
  relatchTurnVerdict,
} from "../store.js";
import type { Session, Message } from "../types.js";

// Arguments are FORWARDED, not discarded: the paths a completed call reports are
// what scope the rescan to the owning repositories, and a scope derived from the
// wrong fields sends the server looking at the wrong repo while the right one stays
// stale — with nothing red and nothing on screen to say so.
const mockMarkGitDirty = vi.fn();
vi.mock("../git.js", () => ({
  markGitDirty: (paths?: readonly string[]) => mockMarkGitDirty(paths),
}));

// `vi.hoisted`, not a plain const: `store.ts` imports a VALUE from this module now, so the
// factory is resolved during linking rather than lazily, and a hoisted factory that closes
// over an ordinary top-level binding reads it before initialisation.
const { mockIsRepoMutatingKind } = vi.hoisted(() => ({
  mockIsRepoMutatingKind: vi.fn(() => false),
}));
vi.mock("../tool-schema.js", () => ({
  isRepoMutatingKind: mockIsRepoMutatingKind,
  // Stubbed rather than present-but-undefined: `store.ts` CALLS this on the tool_call_update
  // path these tests drive, to decide whether an arrival is structural. No fixture here is a
  // delegate, so `false` is the honest answer and keeps the real title table out of the test.
  isSubagentInvocation: () => false,
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

// ---------------------------------------------------------------------------
// The persisted PROMPT row is the one frame that means "the server accepted a
// prompt", and it is the only liveness signal a client that did NOT send it gets
// for the gap before the first chunk. `thinking` is written by the sender's own
// dispatch, and `turn_state` is connect-time synthesis, so a second tab, a phone
// or a background chat had nothing between this row landing and the reply
// starting — and derived a terminal outcome for the whole window.
// ---------------------------------------------------------------------------

describe("message_appended latches liveness from a prompt row", () => {
  it("latches thinking for a user PROMPT row", () => {
    fireSSE("message_appended", "chat-1", { id: "u1", role: "user", ts: 1, content: "go" });
    expect(get("chat-1")?.thinking).toBe(true);
  });

  it("does not latch for a STEER row", () => {
    // A steer joins the turn already running, so it asserts nothing new about
    // liveness — and on the sending device it arrives while `thinking` is already
    // set, where a latch would re-clear the previous turn's verdicts.
    fireSSE("message_appended", "chat-1", {
      id: "s1",
      role: "user",
      ts: 1,
      content: "also this",
      user_kind: "steer",
    });
    expect(get("chat-1")?.thinking).toBe(false);
  });

  it("does not latch for an assistant row", () => {
    // The persist echo of a reply, which is the END of a turn's content rather
    // than the start of one.
    fireSSE("message_appended", "chat-1", { id: "a1", role: "assistant", ts: 1, content: "hi" });
    expect(get("chat-1")?.thinking).toBe(false);
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

  // The RECOVERY BOUND on both retraction doors, and it is the reason a wrong
  // retraction is survivable rather than permanent. `retractStaleThinking` and the
  // connect replay's busy-set sweep both clear `thinking` on a chat whose turn may
  // genuinely still be streaming — the busy set deliberately excludes a workflow-step
  // turn — so the cost of being wrong is bounded by whatever brings it back. This
  // door is that bound: the next chunk of the running turn re-latches it.
  //
  // The bound is the length of a TOOL CALL, not one chunk: `markTurnLive` fires only
  // from here and from `message_appended`'s opensTurn arm, so a retraction landing
  // mid-tool-call holds until that call produces text.
  it("re-latches a retracted chat on the next chunk of the turn still running", () => {
    fireSSE("message_chunk", "chat-1", { message_id: "m1", delta: "hi", block_index: 0 });
    setThinking("chat-1", false); // the retraction
    expect(get("chat-1")?.thinking, "retracted").toBe(false);

    fireSSE("message_chunk", "chat-1", { message_id: "m1", delta: " there", block_index: 0 });

    expect(get("chat-1")?.thinking, "re-latched").toBe(true);
  });

  // The other half of that bound, and the one the reader actually sees: the retraction
  // door is `setThinking(false)` plus `relatchTurnVerdict`, so on a chat whose resident
  // transcript carries an outcome it LATCHES that outcome — and `tabStatusFor` ranks
  // `turn_failed` above `thinking`, which paints a solid failed dot over a reply that is
  // still arriving. Recovering therefore has to drop the verdict as well as re-latch, and
  // that is the reason `markTurnLive` gates on the false→true TRANSITION: `setThinking`
  // clears both latches only on the way up, so a write on every chunk would be a
  // per-chunk verdict wipe rather than a recovery.
  it("drops the verdict the retraction latched, not just the flag", () => {
    setSessions([
      makeSession("chat-1", {
        thinking: true,
        messages: [
          { id: "m1", role: "assistant", ts: 1, content: "half a repl", turn_outcome: "failed" },
        ],
        message_count: 1,
      }),
    ]);
    // The narrow retraction, spelled the way `retractStaleThinking` spells it.
    setThinking("chat-1", false);
    relatchTurnVerdict("chat-1");
    expect(tabStatusFor(get("chat-1")), "the verdict painted over the live reply").toBe("failed");

    fireSSE("message_chunk", "chat-1", { message_id: "m1", delta: " more", block_index: 0 });

    expect(get("chat-1")?.turn_failed, "the stale verdict is gone").toBeUndefined();
    expect(tabStatusFor(get("chat-1"))).toBe("working");
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

  // The scope comes off the ACCUMULATED call, not off the frame: the completion
  // frame normally carries a status and nothing else, while the paths arrived on
  // the create and on earlier deltas. Reading the frame would send an empty scope
  // and cost a whole-tree scan on every completion.
  it("scopes the rescan to the paths the accumulated call reports", () => {
    mockIsRepoMutatingKind.mockReturnValue(true);
    fireSSE("tool_call", "chat-1", {
      message_id: "m1",
      tool_call: {
        id: "tc1",
        title: "write",
        kind: "edit",
        status: "pending",
        ts: 0,
        locations: [{ path: "subflux/main.go", line: 1 }],
      },
      block_index: 0,
    });
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      diffs_appended: [{ path: "vibekit/app.ts", new_text: "x" }],
    });
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      status: "completed",
    });
    // Both sources, because neither is complete on its own: `locations` is what a
    // read or a command reports and `diffs[].path` is what a write carries.
    expect(mockMarkGitDirty).toHaveBeenLastCalledWith(["subflux/main.go", "vibekit/app.ts"]);
  });

  // A call that names nothing must ask for the WHOLE tree. An empty scope would be
  // read by the server as "no repository owns these", which it answers from its
  // snapshot without scanning — so the change would never reach the badge.
  it("names no paths when the call reports none, which asks for a full rescan", () => {
    mockIsRepoMutatingKind.mockReturnValue(true);
    createCall("execute");
    fireSSE("tool_call_update", "chat-1", {
      message_id: "m1",
      tool_call_id: "tc1",
      status: "completed",
    });
    expect(mockMarkGitDirty).toHaveBeenLastCalledWith([]);
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

// ---------------------------------------------------------------------------
// The capped-snapshot marker's two SSE moments.
//
// The connect-time cap sends only the TAIL of a big in-flight turn, so a reader
// shown that tail with nothing saying so reads a bounded payload as the whole
// reply. `truncated` is a REQUIRED wire field for exactly that reason, and this
// is its consumer.
// ---------------------------------------------------------------------------
describe("turn_state truncation marker", () => {
  beforeEach(() => {
    setSessions([makeSession("chat-1")]);
    clearTruncatedSnapshots("chat-1");
  });

  it("a truncated turn_state records the marker for its message", () => {
    fireSSE("turn_state", "chat-1", {
      message: { id: "m1", role: "assistant", ts: 0, content: "…the tail of a big turn" },
      chunk_seq: 400,
      truncated: true,
    });
    expect(isTruncatedSnapshot("chat-1", "m1")).toBe(true);
    // The snapshot is still APPLIED: a bounded transcript is the point, not none.
    expect(get("chat-1")?.messages.map((m) => m.id)).toEqual(["m1"]);
  });

  it("an untruncated turn_state records nothing", () => {
    fireSSE("turn_state", "chat-1", {
      message: { id: "m1", role: "assistant", ts: 0, content: "a short reply" },
      chunk_seq: 2,
      truncated: false,
    });
    expect(isTruncatedSnapshot("chat-1", "m1")).toBe(false);
  });

  // The HEAL. message_appended is the persist echo, so it carries the whole
  // message and the note has nothing left to claim.
  it("a later message_appended for the same id clears it", () => {
    fireSSE("turn_state", "chat-1", {
      message: { id: "m1", role: "assistant", ts: 0, content: "…the tail" },
      chunk_seq: 400,
      truncated: true,
    });
    expect(isTruncatedSnapshot("chat-1", "m1")).toBe(true);

    fireSSE("message_appended", "chat-1", {
      id: "m1",
      role: "assistant",
      ts: 1,
      content: "the whole reply, from the chat file",
    });
    expect(isTruncatedSnapshot("chat-1", "m1")).toBe(false);
  });

  // A workflow step's snapshot is capped like any other, and the two marks are
  // orthogonal: `workflow_step` says whose turn it is, `truncated` says whether
  // the payload is complete.
  it("marks a truncated workflow-step snapshot too, without latching thinking", () => {
    fireSSE("turn_state", "chat-1", {
      message: { id: "m1", role: "assistant", ts: 0, content: "…the step's tail" },
      chunk_seq: 400,
      workflow_step: true,
      truncated: true,
    });
    expect(isTruncatedSnapshot("chat-1", "m1")).toBe(true);
    expect(get("chat-1")?.thinking).toBe(false);
  });

  // A bare busy signal withholds nothing, so there is no id to mark.
  it("a turn_state with no message records nothing", () => {
    fireSSE("turn_state", "chat-1", { chunk_seq: 0, truncated: false });
    expect(get("chat-1")?.messages).toEqual([]);
  });
});
