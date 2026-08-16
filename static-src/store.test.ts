// @vitest-environment happy-dom
// Unit tests for store.ts — property-based idempotency invariants.
import { describe, it, expect } from "vitest";
import * as fc from "fast-check";
import {
  parseContextSize,
  setSessions,
  getSessions,
  get,
  setActive,
  appendMessage,
  upsertMessage,
  upsertHeader,
  removeChat,
  setThinking,
  setWorkingLabel,
  recordSteerQueued,
  markSteerInjected,
  clearSteers,
  steerCount,
  setName,
  activeSession,
  getActiveId,
} from "./store.js";
import type { Session } from "./types.js";
import { effect, flushSync } from "@cplieger/reactive";

// Arbitrary generators for domain types.
const arbMessage = () =>
  fc.record({
    id: fc.uuid(),
    role: fc.constantFrom("user", "assistant") as fc.Arbitrary<"user" | "assistant">,
    ts: fc.nat({ max: 2_000_000_000_000 }),
    content: fc.string({ maxLength: 200 }),
  });

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: 0,
    messages: [],
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

function resetStore(chatID: string): void {
  setSessions([makeSession(chatID)]);
  setActive(chatID);
}

describe("Store idempotency (property-based)", () => {
  it("appendMessage is idempotent: duplicate ID is ignored", () => {
    fc.assert(
      fc.property(arbMessage(), (msg) => {
        resetStore("chat-1");
        appendMessage("chat-1", msg);
        appendMessage("chat-1", msg);
        const session = get("chat-1")!;
        const matches = session.messages.filter((m) => m.id === msg.id);
        expect(matches).toHaveLength(1);
      }),
      { numRuns: 200 },
    );
  });

  it("appendMessage: different IDs all land", () => {
    fc.assert(
      fc.property(fc.array(arbMessage(), { minLength: 1, maxLength: 20 }), (msgs) => {
        resetStore("chat-1");
        const unique = msgs.map((m, i) => ({ ...m, id: `msg-${String(i)}` }));
        for (const m of unique) {
          appendMessage("chat-1", m);
        }
        const session = get("chat-1")!;
        expect(session.messages).toHaveLength(unique.length);
      }),
      { numRuns: 100 },
    );
  });

  it("upsertMessage is idempotent: same message applied twice yields same state", () => {
    fc.assert(
      fc.property(arbMessage(), (msg) => {
        resetStore("chat-1");
        upsertMessage("chat-1", msg);
        const after1 = JSON.stringify(get("chat-1")!.messages);
        upsertMessage("chat-1", msg);
        const after2 = JSON.stringify(get("chat-1")!.messages);
        expect(after1).toBe(after2);
      }),
      { numRuns: 200 },
    );
  });

  it("upsertMessage: non-empty content wins on merge, upsert not duplicate", () => {
    fc.assert(
      fc.property(arbMessage(), fc.string({ minLength: 1, maxLength: 100 }), (msg, newContent) => {
        resetStore("chat-1");
        upsertMessage("chat-1", msg);
        upsertMessage("chat-1", { ...msg, content: newContent });
        const session = get("chat-1")!;
        const found = session.messages.find((m) => m.id === msg.id);
        // ingestMessage merges: a non-empty incoming content replaces the
        // existing, and the message is upserted (never duplicated) by id.
        expect(found?.content).toBe(newContent);
        expect(session.messages.filter((m) => m.id === msg.id)).toHaveLength(1);
      }),
      { numRuns: 200 },
    );
  });

  it("upsertMessage: an empty incoming content does not clobber existing content", () => {
    fc.assert(
      fc.property(arbMessage(), (msg) => {
        resetStore("chat-1");
        // Seed with real content, then re-upsert the same id with empty content
        // (the message_created-after-stream case): the merge must keep it.
        upsertMessage("chat-1", { ...msg, content: "seeded-content" });
        upsertMessage("chat-1", { ...msg, content: "" });
        const found = get("chat-1")!.messages.find((m) => m.id === msg.id);
        expect(found?.content).toBe("seeded-content");
      }),
      { numRuns: 100 },
    );
  });

  it("appendMessage on unknown chat is a no-op", () => {
    fc.assert(
      fc.property(arbMessage(), (msg) => {
        resetStore("chat-1");
        appendMessage("nonexistent", msg);
        expect(get("chat-1")!.messages).toHaveLength(0);
      }),
      { numRuns: 50 },
    );
  });
});

describe("setActive updates the active session", () => {
  // Regression: messages.ts paint() effect tracks activeSession. setActive
  // must re-derive activeSession on chat switch so the renderer re-runs and
  // #messages doesn't keep the previous chat's children (stale messages
  // bleeding through under the new chat's model picker).
  it("activeSession follows the active id", () => {
    setSessions([makeSession("a"), makeSession("b")]);
    setActive("a");
    expect(getActiveId()).toBe("a");
    expect(activeSession.peek()?.id).toBe("a");
    setActive("b");
    expect(getActiveId()).toBe("b");
    expect(activeSession.peek()?.id).toBe("b");
  });

  it("is a no-op / undefined when no active id", () => {
    setSessions([]);
    setActive("");
    expect(getActiveId()).toBe("");
    expect(activeSession.peek()).toBeUndefined();
    setActive("");
    expect(getActiveId()).toBe("");
  });
});

describe("activeSession reactivity (two-tier tracking + batch)", () => {
  it("activeSession re-fires on active-session field change, not on inactive", () => {
    setSessions([makeSession("a"), makeSession("b")]);
    setActive("a");

    let count = 0;
    const dispose = effect(() => {
      void activeSession.value;
      count++;
    });
    // effect() runs once synchronously on registration.
    const afterRegister = count;

    // A field change on the ACTIVE session re-derives activeSession.
    setWorkingLabel("a", "x");
    flushSync();
    expect(count).toBe(afterRegister + 1);
    expect(activeSession.value?.working_label).toBe("x");

    const afterActiveChange = count;

    // A field change on an INACTIVE session fires only that session's signal,
    // which activeSession does not track — so the counter must not move.
    setWorkingLabel("b", "y");
    flushSync();
    expect(count).toBe(afterActiveChange);

    dispose();
  });

  it("activeSession recovers when active id set before session exists", () => {
    setSessions([]);
    setActive("ghost");
    // No session yet for the active id.
    expect(activeSession.value).toBeUndefined();

    // The session arrives after the id was made active. Recovery relies on the
    // computed tracking sessions.ids (the `void sessions.ids.value` line), since
    // signalFor("ghost") didn't exist to be tracked at first derive.
    upsertHeader({
      id: "ghost",
      name: "ghost",
      usage: {
        context_pct: 0,
        context_size: 0,
        credits: 0,
        turn_count: 0,
        last_turn_ms: 0,
        has_real_data: false,
      },
      created_at: 0,
      updated_at: 0,
      message_count: 0,
    });

    expect(activeSession.value?.id).toBe("ghost");
  });

  it("removeChat of active does not double-render", () => {
    setSessions([makeSession("rc-a"), makeSession("rc-b")]);
    setActive("rc-a");

    let count = 0;
    const dispose = effect(() => {
      void activeSession.value;
      count++;
    });
    // Discard the registration run + setup so we count only the removal.
    count = 0;

    removeChat("rc-a");
    flushSync();

    // The batch() in removeChat coalesces sessions.remove (sessions.ids) and the
    // activeId reassignment into ONE re-derive of activeSession. Without it this
    // would be 2.
    expect(count).toBe(1);

    dispose();
  });
});

describe("parseContextSize (table-driven)", () => {
  const cases: { input: string; expected: number | undefined }[] = [
    { input: "128K context", expected: 128_000 },
    { input: "200k context", expected: 200_000 },
    { input: "32K context window", expected: 32_000 },
    { input: "1M context", expected: 1_000_000 },
    { input: "2M context", expected: 2_000_000 },
    { input: "Has 1M token limit", expected: 1_000_000 },
    { input: "no match here", expected: undefined },
    { input: "", expected: undefined },
    { input: "4k context", expected: 4_000 },
    { input: "100 K context", expected: 100_000 },
  ];

  for (const { input, expected } of cases) {
    it(`parseContextSize(${JSON.stringify(input)}) → ${String(expected)}`, () => {
      expect(parseContextSize(input)).toBe(expected);
    });
  }
});

describe("Store removeChat index consistency (property-based)", () => {
  const chatPool = ["c-0", "c-1", "c-2", "c-3", "c-4"];

  const arbOp = () =>
    fc.oneof(
      fc.record({ type: fc.constant("add" as const), id: fc.constantFrom(...chatPool) }),
      fc.record({ type: fc.constant("remove" as const), id: fc.constantFrom(...chatPool) }),
    );

  function makeHeader(id: string) {
    return {
      id,
      name: id,
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      available_modes: [],
      available_models: [],
      supervised_mode: false,
      usage: {
        context_pct: 0,
        context_size: 0,
        credits: 0,
        turn_count: 0,
        last_turn_ms: 0,
        has_real_data: false,
      },
      message_count: 0,
      created_at: 0,
      updated_at: 0,
    };
  }

  it("sessionIndex stays in sync after arbitrary upsertHeader/removeChat sequences", () => {
    fc.assert(
      fc.property(fc.array(arbOp(), { minLength: 10, maxLength: 50 }), (ops) => {
        setSessions([]);
        setActive("");

        for (const op of ops) {
          if (op.type === "add") {
            upsertHeader(makeHeader(op.id));
          } else {
            removeChat(op.id);
          }

          const sessions = getSessions();
          const indexedCount = sessions.filter((s) => get(s.id) === s).length;
          expect(indexedCount).toBe(sessions.length);

          for (const s of sessions) {
            expect(get(s.id)).toBe(s);
          }

          for (const cid of chatPool) {
            if (!sessions.some((s) => s.id === cid)) {
              expect(get(cid)).toBeUndefined();
            }
          }
        }
      }),
      { numRuns: 200 },
    );
  });
});

describe("Store setWorkingLabel/setThinking interaction", () => {
  it("setThinking(false) resets working_label to 'Thinking'", () => {
    resetStore("chat-1");
    setThinking("chat-1", true);
    setWorkingLabel("chat-1", "Editing");
    expect(get("chat-1")!.working_label).toBe("Editing");
    setThinking("chat-1", false);
    expect(get("chat-1")!.working_label).toBe("Thinking");
  });

  it("setWorkingLabel on a non-thinking session is accepted", () => {
    resetStore("chat-1");
    expect(get("chat-1")!.thinking).toBe(false);
    setWorkingLabel("chat-1", "Custom");
    expect(get("chat-1")!.working_label).toBe("Custom");
  });
});

// --- Mid-turn steers: a projection of KAS's buffer, not a queue ---
//
// Every case here is about surviving the wire rather than about data structure
// mechanics. The old queue was client-owned, so its tests could assume each
// mutation happened once and in order; this projection is driven by SSE, where a
// frame can arrive twice, out of order, or without the frame that should have
// preceded it. Those are the cases that matter.

describe("Store steer projection", () => {
  it("records a queued steer as not-yet-read", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually use tabs" });
    expect(steerCount("chat-1")).toBe(1);
    expect(get("chat-1")?.steers?.[0]).toEqual({
      id: "steer-1",
      text: "actually use tabs",
      injected: false,
    });
  });

  // An SSE reconnect replays unacknowledged frames, so the same steer_queued can
  // legitimately arrive twice. Two chips for one message would misreport how much
  // the agent has been told.
  it("is idempotent by id across a replayed frame", () => {
    fc.assert(
      fc.property(fc.integer({ min: 1, max: 6 }), (repeats) => {
        resetStore("chat-1");
        for (let i = 0; i < repeats; i++) {
          recordSteerQueued("chat-1", { id: "steer-1", text: "same message" });
        }
        expect(steerCount("chat-1")).toBe(1);
      }),
    );
  });

  // A replay must not un-read a steer the model has already consumed: injected is
  // the client's own knowledge and the queued frame carries no opinion about it.
  it("keeps the read state when a queued frame is replayed", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    markSteerInjected("chat-1", "steer-1", "one");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    expect(get("chat-1")?.steers?.[0]?.injected).toBe(true);
  });

  it("marks a steer read", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    markSteerInjected("chat-1", "steer-1", "one");
    expect(get("chat-1")?.steers?.[0]?.injected).toBe(true);
  });

  // steer_injected can arrive with no steer_queued behind it: another device sent
  // the steer, or this one connected mid-turn. Dropping it would leave the row
  // with no sign the agent was redirected at all.
  it("creates an entry for an injected steer it never saw queued", () => {
    resetStore("chat-1");
    markSteerInjected("chat-1", "steer-ghost", "from another tab");
    expect(get("chat-1")?.steers).toEqual([
      { id: "steer-ghost", text: "from another tab", injected: true },
    ]);
  });

  it("carries a notification severity when KAS classified one", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "notify-1", text: "step failed", severity: "error" });
    expect(get("chat-1")?.steers?.[0]?.severity).toBe("error");
  });

  // Two steer_injected frames, one id: KAS's read frame carries the text, the
  // agent's acknowledgement marker carries what it did. Each field is adopted
  // only from the frame that has it, or the second would blank the first's text.
  it("records the agent's acknowledgement without losing the steer's text", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead" });
    markSteerInjected("chat-1", "steer-1", "use tabs instead");
    markSteerInjected("chat-1", "steer-1", "", "switched the file to tabs");
    expect(get("chat-1")?.steers?.[0]).toEqual({
      id: "steer-1",
      text: "use tabs instead",
      injected: true,
      ack: "switched the file to tabs",
    });
  });

  // A read frame after an ack frame must not erase the ack: SSE reconnect
  // replays every unanswered frame, so the two can arrive in either order.
  it("keeps an acknowledgement when a read frame is replayed after it", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    markSteerInjected("chat-1", "steer-1", "", "did the thing");
    markSteerInjected("chat-1", "steer-1", "one");
    expect(get("chat-1")?.steers?.[0]?.ack).toBe("did the thing");
  });

  // An ack frame carries no text, so an id this client never saw has nothing to
  // label a chip with. Creating a blank chip that reads only "read: did X" names
  // no message and cannot be matched to anything the user wrote.
  it("ignores an acknowledgement for a steer it never saw", () => {
    resetStore("chat-1");
    markSteerInjected("chat-1", "steer-unknown", "", "did something");
    expect(get("chat-1")?.steers).toBeUndefined();
  });

  it("ignores an empty id rather than creating an unaddressable entry", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "", text: "nowhere" });
    markSteerInjected("chat-1", "", "nowhere");
    expect(steerCount("chat-1")).toBe(0);
  });

  // The field is DELETED rather than emptied so a cleared session compares equal
  // to one that never had steers — the chip row dedups by value, and an empty
  // array would repaint on every turn boundary.
  it("deletes the field when the last steer goes", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    clearSteers("chat-1");
    expect(get("chat-1")?.steers).toBeUndefined();
  });

  it("clears only the named ids", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "three" });
    clearSteers("chat-1", ["steer-1", "steer-3"]);
    expect(get("chat-1")?.steers?.map((e) => e.id)).toEqual(["steer-2"]);
  });

  it("treats an empty id list as clear-everything, which is what a turn boundary means", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    clearSteers("chat-1", []);
    expect(get("chat-1")?.steers).toBeUndefined();
  });

  it("is a no-op for ids it does not hold, and for an unknown chat", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    const before = get("chat-1")?.steers;
    clearSteers("chat-1", ["steer-nope"]);
    // Same array identity: a no-op must not churn the session, or every
    // boundary would repaint the row.
    expect(get("chat-1")?.steers).toBe(before);
    clearSteers("nonexistent");
    expect(steerCount("nonexistent")).toBe(0);
  });
});

describe("Store setName", () => {
  it("updates session name", () => {
    resetStore("chat-1");
    expect(get("chat-1")!.name).toBe("test");
    setName("chat-1", "Renamed");
    expect(get("chat-1")!.name).toBe("Renamed");
  });

  it("no-ops on unknown chat", () => {
    resetStore("chat-1");
    setName("nonexistent", "X");
    expect(get("chat-1")!.name).toBe("test");
  });
});

// ---------------------------------------------------------------------------
// Per-message + per-tool signal architecture
// ---------------------------------------------------------------------------

import { appendChunk, upsertToolCall } from "./store.js";
import {
  ensureStreamingSig,
  streamingTextSigs,
  clearStreamingSig,
  ensureReasoningSig,
  streamingReasoningSigs,
  clearReasoningSig,
  ensureToolCallSig,
  clearToolCallSig,
} from "./store-signals.js";
import type { ToolCall } from "./types.js";

describe("streaming signals", () => {
  it("appendChunk routes content vs reasoning to separate signals", () => {
    resetStore("chat-1");
    // First chunk creates the message + bumps global; signals are
    // created lazily by callers (mountContentBubble / mountReasoningBlock).
    appendChunk("chat-1", "m1", "hello", false, 0, "");
    const session = get("chat-1")!;
    expect(session.messages[0]?.content).toBe("hello");
    expect(session.messages[0]?.reasoning ?? "").toBe("");

    // Subscribe a content signal so subsequent content chunks route here.
    const contentSig = ensureStreamingSig("m1", "hello");
    appendChunk("chat-1", "m1", " world", false, 0, "");
    expect(contentSig.value).toBe("hello world");
    expect(session.messages[0]?.content).toBe("hello world");
    expect(session.messages[0]?.reasoning ?? "").toBe("");

    // Subscribe a reasoning signal; reasoning chunks route there.
    const reasoningSig = ensureReasoningSig("m1", "");
    appendChunk("chat-1", "m1", "let me think", true, 1, "");
    expect(reasoningSig.value).toBe("let me think");
    expect(session.messages[0]?.reasoning).toBe("let me think");
    expect(session.messages[0]?.content).toBe("hello world");

    clearStreamingSig("m1");
    clearReasoningSig("m1");
  });

  it("ensure*Sig returns the same signal on repeated calls", () => {
    const a = ensureStreamingSig("m-id", "init");
    const b = ensureStreamingSig("m-id", "ignored");
    expect(a).toBe(b);
    clearStreamingSig("m-id");
  });

  it("clearStreamingSig + getStreamingSig: signal is gone after clear", () => {
    ensureStreamingSig("m-x", "x");
    expect(streamingTextSigs.get("m-x")).toBeDefined();
    clearStreamingSig("m-x");
    expect(streamingTextSigs.get("m-x")).toBeUndefined();
  });

  it("clearReasoningSig + getReasoningSig: signal is gone after clear", () => {
    ensureReasoningSig("m-y", "y");
    expect(streamingReasoningSigs.get("m-y")).toBeDefined();
    clearReasoningSig("m-y");
    expect(streamingReasoningSigs.get("m-y")).toBeUndefined();
  });
});

describe("per-tool signal", () => {
  const baseTC = (id: string, status: ToolCall["status"]): ToolCall => ({
    id,
    title: "readFile",
    kind: "read",
    status,
    ts: 0,
  });

  it("upsertToolCall: first add bumps global, subsequent updates fan via signal", () => {
    resetStore("chat-1");
    appendMessage("chat-1", { id: "m1", role: "assistant", ts: 0, content: "" });

    // First tool — this creates the tool array. No signal yet, so it
    // bumps global to trigger reconcile mount.
    upsertToolCall("chat-1", "m1", baseTC("t1", "pending"), 0);

    // Now mount-time signal subscription happens.
    const sig = ensureToolCallSig("t1", baseTC("t1", "pending"));
    const firedCount = 0;
    const lastValue: ToolCall | null = null;
    // Manually subscribe via reading sig.value in an effect-like wrapper.
    // (Tests don't import `effect`; we use peek/value to simulate the flow.)
    const observed: ToolCall[] = [];
    // Update via upsertToolCall — should fire the signal directly,
    // not via global reconcile.
    upsertToolCall("chat-1", "m1", baseTC("t1", "completed"), 0);
    expect(sig.value.status).toBe("completed");

    // Idle reads to satisfy the linter.
    void firedCount;
    void lastValue;
    void observed;

    clearToolCallSig("t1");
  });

  it("ensureToolCallSig is idempotent on the id", () => {
    const a = ensureToolCallSig("t-id", baseTC("t-id", "pending"));
    const b = ensureToolCallSig("t-id", baseTC("t-id", "completed"));
    // Same signal — initial value preserved (b's `initial` is ignored).
    expect(a).toBe(b);
    expect(a.value.status).toBe("pending");
    clearToolCallSig("t-id");
  });
});

// ---------------------------------------------------------------------------
// Connect-time turn_state snapshot: chunk-seq watermark gating
// ---------------------------------------------------------------------------

import { setSnapshotSeq, clearSnapshotSeq } from "./store.js";

describe("appendChunk snapshot-seq gating", () => {
  it("drops chunks at or below the watermark, applies those above", () => {
    resetStore("chat-ws");
    // Snapshot said: message m-snap already contains deltas 1..3.
    setSnapshotSeq("chat-ws", "m-snap", 3);

    appendChunk("chat-ws", "m-snap", "dup-1 ", false, 0, "", 2); // folded in → drop
    appendChunk("chat-ws", "m-snap", "dup-2 ", false, 0, "", 3); // boundary → drop
    appendChunk("chat-ws", "m-snap", "fresh", false, 0, "", 4); // new → apply

    const msg = get("chat-ws")!.messages.find((m) => m.id === "m-snap");
    expect(msg?.content).toBe("fresh");
    clearSnapshotSeq("chat-ws");
  });

  it("seq 0 (pre-seq server or unrelated turn) always applies", () => {
    resetStore("chat-ws0");
    setSnapshotSeq("chat-ws0", "m-snap", 5);
    appendChunk("chat-ws0", "m-snap", "legacy", false, 0, "", 0);
    const msg = get("chat-ws0")!.messages.find((m) => m.id === "m-snap");
    expect(msg?.content).toBe("legacy");
    clearSnapshotSeq("chat-ws0");
  });

  it("a different message id ignores the watermark (fresh turn)", () => {
    resetStore("chat-wsx");
    setSnapshotSeq("chat-wsx", "m-old", 99);
    appendChunk("chat-wsx", "m-new", "next turn", false, 0, "", 1);
    const msg = get("chat-wsx")!.messages.find((m) => m.id === "m-new");
    expect(msg?.content).toBe("next turn");
    clearSnapshotSeq("chat-wsx");
  });

  it("clearSnapshotSeq lifts the gate", () => {
    resetStore("chat-wsc");
    setSnapshotSeq("chat-wsc", "m-snap", 10);
    clearSnapshotSeq("chat-wsc");
    appendChunk("chat-wsc", "m-snap", "after clear", false, 0, "", 1);
    const msg = get("chat-wsc")!.messages.find((m) => m.id === "m-snap");
    expect(msg?.content).toBe("after clear");
  });
});
