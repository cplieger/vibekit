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
  markGhostChat,
  isGhostChat,
  setModel,
} from "./store.js";
import type { ChatHeader, Session } from "./types.js";
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

/** A minimal server header for `chatID`. `model` is deliberately absent, which
 *  is the shape the wire produces for a chat whose model the server has not been
 *  told yet (`Model` is `omitempty`). */
function headerFor(chatID: string): ChatHeader {
  return {
    id: chatID,
    name: chatID,
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

  it("a header that omits the model leaves a locally-chosen one alone", () => {
    // A model picked before the first prompt is client-only — it rides that
    // prompt — and `Model` is `omitempty` on the wire, so the record that
    // `set_effort` / `set_mode` auto-creates broadcasts a header carrying no
    // model at all. Reading that as a clear is what reset the pill to "auto" and
    // unselected every row in the model list on the first effort click in a
    // fresh chat, which looked like the effort control changing the model.
    setSessions([makeSession("hm-keep")]);
    setActive("hm-keep");
    setModel("hm-keep", "claude-opus-5");

    upsertHeader({ ...headerFor("hm-keep"), effort: "high" });

    expect(get("hm-keep")?.model).toBe("claude-opus-5");
    expect(get("hm-keep")?.effort).toBe("high");
  });

  it("a header that names a model overwrites the local one", () => {
    // The other direction, so the rule above cannot be read as "the client wins":
    // once the server knows a model it is authoritative, which is what makes a
    // switch performed on another device land here.
    setSessions([makeSession("hm-take")]);
    setActive("hm-take");
    setModel("hm-take", "claude-opus-5");

    upsertHeader({ ...headerFor("hm-take"), model: "claude-sonnet-5" });

    expect(get("hm-take")?.model).toBe("claude-sonnet-5");
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

// A client-minted chat exists nowhere but this tab until the server says
// otherwise, and asking the server about it 404s. The mark is what lets a caller
// skip that question, so what matters is that it CLEARS the moment the record is
// real — a mark left standing would go on suppressing a fetch the chat has since
// earned.
describe("ghost chats (a client-minted id the server has not acknowledged)", () => {
  const header = (id: string) => ({
    id,
    name: id,
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

  it("clears the mark when a server frame names the chat", () => {
    setSessions([makeSession("g1")]);
    markGhostChat("g1");
    expect(isGhostChat("g1")).toBe(true);

    upsertHeader(header("g1"));
    expect(isGhostChat("g1")).toBe(false);
  });

  it("keeps the mark across writes that are not a server re-sync", () => {
    setSessions([makeSession("g2")]);
    markGhostChat("g2");
    setName("g2", "renamed locally");
    setThinking("g2", true);
    expect(isGhostChat("g2")).toBe(true);
  });

  it("reports no ghost for an id with no row, which is nothing to ask about", () => {
    setSessions([]);
    markGhostChat("nothing");
    expect(isGhostChat("nothing")).toBe(false);
  });

  it("does not survive the row: a reload's list is all persisted chats", () => {
    setSessions([makeSession("g3")]);
    markGhostChat("g3");
    setSessions([makeSession("g3")]); // what loadList does
    expect(isGhostChat("g3")).toBe(false);
  });

  it("does not churn the session when the mark is already standing", () => {
    // Every New chat click mints one id and marks it; a re-entrant call (a
    // restore, a second seeding pass) must not fire the chat's signal again, or
    // the sidebar row repaints for a fact that has not changed.
    setSessions([makeSession("g4")]);
    markGhostChat("g4");
    const marked = get("g4");
    markGhostChat("g4");
    expect(get("g4")).toBe(marked);
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

// mergeMessage is a field-by-field ALLOWLIST: an unlisted field is silently
// dropped on the second ingest of the same id, and a user message with
// attachments is ingested at least twice — once from the prompt's own
// message_appended and again on any chat refetch or reconnect replay. So the
// clause is what makes the turn header's pills survive a reload, not decoration.
describe("Store message attachments (merge allowlist)", () => {
  const atts = [
    { path: "out/shot.png", name: "shot.png" },
    { path: "docs/spec.pdf", name: "spec.pdf" },
  ];

  it("keeps the attachments on first ingest", () => {
    resetStore("chat-att1");
    appendMessage("chat-att1", {
      id: "m-1",
      role: "user",
      ts: 1,
      content: "look at these",
      attachments: atts,
    });
    expect(get("chat-att1")?.messages[0]?.attachments).toEqual(atts);
  });

  it("survives a re-ingest of the same id that omits them", () => {
    resetStore("chat-att2");
    appendMessage("chat-att2", {
      id: "m-1",
      role: "user",
      ts: 1,
      content: "look at these",
      attachments: atts,
    });
    // A refetch or replay of the same row without the field must not erase it —
    // the exact shape the allowlist exists to get right.
    upsertMessage("chat-att2", { id: "m-1", role: "user", ts: 1, content: "look at these" });
    expect(get("chat-att2")?.messages[0]?.attachments).toEqual(atts);
  });

  it("adopts a non-empty incoming list over an existing one", () => {
    resetStore("chat-att3");
    appendMessage("chat-att3", { id: "m-1", role: "user", ts: 1, content: "x", attachments: [] });
    upsertMessage("chat-att3", {
      id: "m-1",
      role: "user",
      ts: 1,
      content: "x",
      attachments: atts,
    });
    expect(get("chat-att3")?.messages[0]?.attachments).toEqual(atts);
  });

  it("leaves a message with none alone", () => {
    resetStore("chat-att4");
    appendMessage("chat-att4", { id: "m-1", role: "user", ts: 1, content: "just a question" });
    expect(get("chat-att4")?.messages[0]?.attachments).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// The render signal: one bump per list change, coalesced.
//
// `messagesVersion` is the renderer's only coarse "the list changed" input, and
// the streaming paths deliberately do NOT bump it per delta — the per-block and
// per-message signals carry those. So the two things worth pinning are that a
// list change bumps it exactly once, and that several changes arriving in one
// tick still bump it exactly once. A per-event bump would repaint the whole
// transcript for every chunk of every turn.
// ---------------------------------------------------------------------------

import {
  messagesVersion,
  emitMessages,
  isThinking,
  defaultUsage,
  setTurnFailed,
  clearTurnFailed,
  clearTurnDone,
  tabStatusFor,
  setAgentStatus,
  setCurrentMode,
  setSupervisedMode,
  setEffort,
  normalizeMessage,
  setTurnSummary,
  reinsertSession,
  setCodeReferences,
  rebuildMsgIndex,
} from "./store.js";
import { ensureBlockTextSig, ensureBlockThinkingSig, clearAllBlockSigs } from "./store-signals.js";

/** Let the queueMicrotask coalescer run. A macrotask, so every pending
 *  microtask has drained by the time it resolves. */
function tick(): Promise<void> {
  return new Promise((r) => setTimeout(r, 0));
}

describe("messagesVersion", () => {
  it("counts up by one per explicit emit", () => {
    const before = messagesVersion.peek();
    emitMessages();
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("coalesces two new messages arriving in one tick into one render", async () => {
    resetStore("mv-1");
    const before = messagesVersion.peek();
    appendChunk("mv-1", "m-a", "a", false, 0, "");
    appendChunk("mv-1", "m-b", "b", false, 0, "");
    // Deferred: the coalescer owns the microtask, so nothing has repainted yet.
    expect(messagesVersion.peek()).toBe(before);
    await tick();
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("schedules again once the deferred render has run", async () => {
    resetStore("mv-2");
    appendChunk("mv-2", "m-a", "a", false, 0, "");
    await tick();
    const between = messagesVersion.peek();
    appendChunk("mv-2", "m-b", "b", false, 0, "");
    await tick();
    // The guard has to reset, or the second turn of any chat never repaints.
    expect(messagesVersion.peek()).toBe(between + 1);
  });
});

// ---------------------------------------------------------------------------
// parseContextSize: the whitespace the model catalog's prose actually carries.
//
// The table above covers the shapes we have seen; these cover the tolerance the
// `\s*` in both patterns exists for. The input is upstream prose from the model
// catalog, so "128K context", "128K  context" and "128Kcontext" are all the same
// claim, and a multi-digit M size must not be read one digit at a time.
// ---------------------------------------------------------------------------

describe("parseContextSize tolerates the spacing upstream prose varies on", () => {
  const cases: { input: string; expected: number | undefined }[] = [
    { input: "128K  context", expected: 128_000 },
    { input: "128Kcontext", expected: 128_000 },
    { input: "2M  context", expected: 2_000_000 },
    { input: "10M context", expected: 10_000_000 },
    { input: "100 M context", expected: 100_000_000 },
  ];

  for (const { input, expected } of cases) {
    it(`parseContextSize(${JSON.stringify(input)}) → ${String(expected)}`, () => {
      expect(parseContextSize(input)).toBe(expected);
    });
  }
});

// ---------------------------------------------------------------------------
// The latches, and the no-churn discipline they share.
//
// Every mutator here is driven by an SSE frame that an interrupted stream can
// replay, so "already in that state" has to be a no-op rather than a rewrite:
// a rewrite fires the chat's per-entity signal, and every subscriber of that
// chat repaints for a frame that said nothing new. Object identity is the only
// way to assert that from outside, which is why these read `toBe`.
// ---------------------------------------------------------------------------

describe("Store defaults for a chat it has never heard of", () => {
  it("reports no turn in flight", () => {
    setSessions([]);
    expect(isThinking("nobody")).toBe(false);
  });

  it("seeds usage as not-yet-measured", () => {
    // The flag is what stops the context ring presenting a fresh chat's zeroes as
    // a measurement; flipping it would make "0 credits used" look observed.
    expect(defaultUsage().has_real_data).toBe(false);
  });
});

describe("Store failure latch", () => {
  it("latches a failed turn", () => {
    resetStore("tf-1");
    setTurnFailed("tf-1");
    expect(get("tf-1")?.turn_failed).toBe(true);
    expect(tabStatusFor(get("tf-1"))).toBe("failed");
  });

  it("does not churn the session on a replayed error frame", () => {
    resetStore("tf-2");
    setTurnFailed("tf-2");
    const latched = get("tf-2");
    setTurnFailed("tf-2");
    expect(get("tf-2")).toBe(latched);
  });

  it("clears on request without a turn starting", () => {
    resetStore("tf-3");
    setTurnFailed("tf-3");
    clearTurnFailed("tf-3");
    expect(get("tf-3")?.turn_failed).toBeUndefined();
    expect(tabStatusFor(get("tf-3"))).toBe("idle");
  });

  it("does not churn the session clearing a latch that was never set", () => {
    resetStore("tf-4");
    const before = get("tf-4");
    clearTurnFailed("tf-4");
    expect(get("tf-4")).toBe(before);
  });
});

describe("Store finished latch", () => {
  it("does not churn the session clearing a latch that was never set", () => {
    resetStore("td-1");
    const before = get("td-1");
    clearTurnDone("td-1");
    expect(get("td-1")).toBe(before);
  });
});

// ---------------------------------------------------------------------------
// setAgentStatus: the agent's own words about what it is doing.
//
// Two fields, one frame, and an empty string is a CLEAR rather than a value —
// the fields are deleted so a cleared session compares equal to one that never
// had a status, which is what keeps the tab strip from repainting on every
// turn boundary.
// ---------------------------------------------------------------------------

describe("Store agent status", () => {
  it("records the status and its description", () => {
    resetStore("as-1");
    setAgentStatus("as-1", "waiting_on_user", "which file did you mean?");
    expect(get("as-1")?.agent_status).toBe("waiting_on_user");
    expect(get("as-1")?.agent_status_text).toBe("which file did you mean?");
  });

  it("does not churn the session on a repeated frame", () => {
    resetStore("as-2");
    setAgentStatus("as-2", "in_progress", "reading the parser");
    const settled = get("as-2");
    setAgentStatus("as-2", "in_progress", "reading the parser");
    expect(get("as-2")).toBe(settled);
  });

  it("takes a new description under an unchanged status", () => {
    resetStore("as-3");
    setAgentStatus("as-3", "in_progress", "reading the parser");
    setAgentStatus("as-3", "in_progress", "writing the patch");
    expect(get("as-3")?.agent_status_text).toBe("writing the patch");
  });

  it("takes a new status under an unchanged description", () => {
    resetStore("as-4");
    setAgentStatus("as-4", "in_progress", "reading the parser");
    setAgentStatus("as-4", "completed", "reading the parser");
    expect(get("as-4")?.agent_status).toBe("completed");
  });

  it("reads an empty status as a clear, and deletes the field", () => {
    resetStore("as-5");
    setAgentStatus("as-5", "completed", "all done");
    setAgentStatus("as-5", "", "all done");
    expect(get("as-5")?.agent_status).toBeUndefined();
  });

  it("reads an empty description as a clear, and deletes the field", () => {
    resetStore("as-6");
    setAgentStatus("as-6", "completed", "all done");
    setAgentStatus("as-6", "completed", "");
    expect(get("as-6")?.agent_status_text).toBeUndefined();
  });

  it("does not churn a chat that never had one when both fields arrive empty", () => {
    // The `?? ""` on both reads is what makes this a no-op: an absent field and a
    // cleared one are the same state, so the clearing frame says nothing new.
    resetStore("as-7");
    const before = get("as-7");
    setAgentStatus("as-7", "", "");
    expect(get("as-7")).toBe(before);
  });
});

// ---------------------------------------------------------------------------
// upsertHeader: a SERVER-authoritative re-sync that must not lose what only the
// client knows. `chat_updated` arrives on every write to any field of the chat,
// so this runs constantly and each `??` in it is a decision about who wins.
// ---------------------------------------------------------------------------

describe("Store upsertHeader re-sync", () => {
  it("never lowers a message count the client has already seen", () => {
    // The header's count is the server's; `messages` is the paginated window. A
    // header built before the last turn was flushed would otherwise walk the
    // count backwards and make a chat with history look empty.
    setSessions([{ ...makeSession("uh-1"), message_count: 5 }]);
    upsertHeader({ ...headerFor("uh-1"), message_count: 2 });
    expect(get("uh-1")?.message_count).toBe(5);
  });

  it("does not read an empty model string as a clear either", () => {
    // Same rule as an absent model, one step further along: "" is what a header
    // built from a record the server has no model for looks like once the field
    // is present at all.
    setSessions([makeSession("uh-2")]);
    setModel("uh-2", "claude-opus-5");
    upsertHeader({ ...headerFor("uh-2"), model: "" });
    expect(get("uh-2")?.model).toBe("claude-opus-5");
  });

  it("leaves supervised mode off when the header does not mention it", () => {
    setSessions([makeSession("uh-3")]);
    upsertHeader(headerFor("uh-3"));
    expect(get("uh-3")?.supervised_mode).toBe(false);
  });

  it("adopts a compaction watermark", () => {
    setSessions([makeSession("uh-4")]);
    upsertHeader({ ...headerFor("uh-4"), compaction_watermark: "msg-42" });
    expect(get("uh-4")?.compaction_watermark).toBe("msg-42");
  });

  it("drops the watermark when the header stops carrying one", () => {
    setSessions([makeSession("uh-5")]);
    upsertHeader({ ...headerFor("uh-5"), compaction_watermark: "msg-42" });
    upsertHeader(headerFor("uh-5"));
    expect(get("uh-5")?.compaction_watermark).toBeUndefined();
  });

  it("seeds a brand-new chat as idle and unsupervised", () => {
    setSessions([]);
    upsertHeader(headerFor("uh-6"));
    expect(get("uh-6")?.thinking).toBe(false);
    expect(get("uh-6")?.supervised_mode).toBe(false);
  });

  it("seeds a brand-new chat with history as having more to fetch", () => {
    setSessions([]);
    upsertHeader({ ...headerFor("uh-7"), message_count: 3 });
    expect(get("uh-7")?.has_more).toBe(true);
  });

  it("seeds a brand-new empty chat as having nothing to fetch", () => {
    setSessions([]);
    upsertHeader(headerFor("uh-8"));
    expect(get("uh-8")?.has_more).toBe(false);
  });

  it("carries a watermark onto a brand-new chat", () => {
    setSessions([]);
    upsertHeader({ ...headerFor("uh-9"), compaction_watermark: "msg-7" });
    expect(get("uh-9")?.compaction_watermark).toBe("msg-7");
  });
});

// ---------------------------------------------------------------------------
// removeChat and reinsertSession: the optimistic-delete pair.
//
// `chat.delete` removes the row before the server answers and puts it back if
// the command fails, so which chat becomes active and where the row lands are
// both user-visible.
// ---------------------------------------------------------------------------

describe("Store removeChat", () => {
  it("activates the next remaining chat when the active one goes", () => {
    setSessions([makeSession("rm-a"), makeSession("rm-b")]);
    setActive("rm-a");
    removeChat("rm-a");
    expect(getActiveId()).toBe("rm-b");
  });

  it("leaves the active chat alone when a different one goes", () => {
    setSessions([makeSession("rm-a"), makeSession("rm-b"), makeSession("rm-c")]);
    setActive("rm-c");
    removeChat("rm-a");
    expect(getActiveId()).toBe("rm-c");
  });

  it("does not switch chats for an id that has no row", () => {
    // An id can be active before its session arrives (the ghost case above), and
    // removing something that was never there must not move the user.
    setSessions([makeSession("rm-a"), makeSession("rm-b")]);
    setActive("rm-ghost");
    removeChat("rm-ghost");
    expect(getActiveId()).toBe("rm-ghost");
  });

  it("drops the removed chat's message index with it", () => {
    setSessions([makeSession("rm-i")]);
    appendMessage("rm-i", { id: "m-1", role: "user", ts: 1, content: "one" });
    appendMessage("rm-i", { id: "m-2", role: "user", ts: 2, content: "two" });
    removeChat("rm-i");
    upsertHeader(headerFor("rm-i"));
    // A surviving index would report m-2 as already resident at slot 1 of an
    // empty list, and the message would be silently dropped.
    appendMessage("rm-i", { id: "m-2", role: "user", ts: 2, content: "two" });
    expect(get("rm-i")?.messages).toHaveLength(1);
    expect(get("rm-i")?.messages[0]?.id).toBe("m-2");
  });
});

describe("Store reinsertSession", () => {
  it("restores a removed chat at the index it held", () => {
    setSessions([makeSession("ri-a"), makeSession("ri-b"), makeSession("ri-c")]);
    const gone = get("ri-b")!;
    removeChat("ri-b");
    reinsertSession(gone, 1);
    expect(getSessions().map((s) => s.id)).toEqual(["ri-a", "ri-b", "ri-c"]);
  });

  it("puts it at the head when no index is named", () => {
    setSessions([makeSession("ri-a"), makeSession("ri-b")]);
    reinsertSession(makeSession("ri-z"));
    expect(getSessions().map((s) => s.id)).toEqual(["ri-z", "ri-a", "ri-b"]);
  });

  it("clamps an index past the end to the end", () => {
    setSessions([makeSession("ri-a"), makeSession("ri-b")]);
    reinsertSession(makeSession("ri-z"), 99);
    expect(getSessions().map((s) => s.id)).toEqual(["ri-a", "ri-b", "ri-z"]);
  });

  it("clamps a negative index to the head", () => {
    setSessions([makeSession("ri-a")]);
    reinsertSession(makeSession("ri-z"), -5);
    expect(getSessions().map((s) => s.id)).toEqual(["ri-z", "ri-a"]);
  });

  it("is idempotent: a chat already present is left where it is", () => {
    setSessions([makeSession("ri-a"), makeSession("ri-b")]);
    reinsertSession(makeSession("ri-b"), 0);
    expect(getSessions().map((s) => s.id)).toEqual(["ri-a", "ri-b"]);
  });
});

// ---------------------------------------------------------------------------
// normalizeMessage: give the renderer ONE path.
//
// v3 messages already carry `blocks`; a legacy replay carries content /
// reasoning / tool_calls and nothing else. The synthesis order is the
// chronological order the renderer walks — thinking, then text, then one
// tool_use per call — and anything that already has blocks, or has nothing to
// synthesize from, must pass through untouched rather than gain an empty array.
// ---------------------------------------------------------------------------

describe("Store normalizeMessage", () => {
  it("leaves a user message alone", () => {
    const m = { id: "n-1", role: "user" as const, ts: 1, content: "hello" };
    expect(normalizeMessage(m).blocks).toBeUndefined();
  });

  it("leaves an assistant message that already carries blocks alone", () => {
    const blocks = [{ type: "text" as const, text: "already" }];
    const m = { id: "n-2", role: "assistant" as const, ts: 1, content: "ignored", blocks };
    expect(normalizeMessage(m).blocks).toBe(blocks);
  });

  it("synthesizes into an assistant message whose blocks array is empty", () => {
    // What appendChunk mints, and what a v3 message with no content looks like.
    const m = { id: "n-3", role: "assistant" as const, ts: 1, content: "hi", blocks: [] };
    expect(normalizeMessage(m).blocks).toEqual([{ type: "text", text: "hi" }]);
  });

  it("leaves a plan-only assistant message alone", () => {
    const m = {
      id: "n-4",
      role: "assistant" as const,
      ts: 1,
      content: "",
      plan: [{ content: "step one", priority: "high", status: "pending" as const }],
    };
    expect(normalizeMessage(m).blocks).toBeUndefined();
  });

  it("synthesizes a thinking block from reasoning alone", () => {
    const m = { id: "n-5", role: "assistant" as const, ts: 1, reasoning: "let me think" };
    expect(normalizeMessage(m).blocks).toEqual([{ type: "thinking", thinking: "let me think" }]);
  });

  it("orders thinking ahead of text", () => {
    const m = { id: "n-6", role: "assistant" as const, ts: 1, content: "answer", reasoning: "why" };
    expect(normalizeMessage(m).blocks).toEqual([
      { type: "thinking", thinking: "why" },
      { type: "text", text: "answer" },
    ]);
  });

  it("synthesizes one tool_use per call, carrying each call's subtask", () => {
    const m = {
      id: "n-7",
      role: "assistant" as const,
      ts: 1,
      content: "",
      tool_calls: [
        {
          id: "t-1",
          title: "readFile",
          kind: "read" as const,
          status: "completed" as const,
          ts: 0,
        },
        {
          id: "t-2",
          title: "bash",
          kind: "execute" as const,
          status: "completed" as const,
          ts: 0,
          agent_subtask_id: "sub-1",
        },
      ],
    };
    expect(normalizeMessage(m).blocks).toEqual([
      { type: "tool_use", tool_call_id: "t-1" },
      { type: "tool_use", tool_call_id: "t-2", agent_subtask_id: "sub-1" },
    ]);
  });
});

// ---------------------------------------------------------------------------
// mergeMessage's allowlist, field by field.
//
// The rule is one sentence — adopt the incoming's non-empty fields, never
// clobber a non-empty field with an empty one — and it is a per-field clause
// rather than a loop, so every field is its own chance to get it wrong. A
// message is ingested at least twice (its own message_appended, then any
// refetch or reconnect replay), so a clause that clobbers loses real data on
// the second pass.
// ---------------------------------------------------------------------------

describe("Store mergeMessage allowlist", () => {
  const seededBlocks = () => [{ type: "text" as const, text: "seeded" }];

  function seed(chat: string, over: Record<string, unknown> = {}): void {
    setSessions([makeSession(chat)]);
    setActive(chat);
    appendMessage(chat, {
      id: "mm-1",
      role: "assistant",
      ts: 5,
      content: "seeded",
      blocks: seededBlocks(),
      ...over,
    });
  }

  function reingest(chat: string, over: Record<string, unknown> = {}): void {
    upsertMessage(chat, {
      id: "mm-1",
      role: "assistant",
      ts: 5,
      content: "seeded",
      blocks: seededBlocks(),
      ...over,
    });
  }

  function merged(chat: string) {
    return get(chat)?.messages[0];
  }

  it("adopts a non-empty incoming reasoning", () => {
    seed("mg-1", { reasoning: "first pass" });
    reingest("mg-1", { reasoning: "the sanitized version" });
    expect(merged("mg-1")?.reasoning).toBe("the sanitized version");
  });

  it("adopts a non-empty incoming blocks array", () => {
    seed("mg-2");
    reingest("mg-2", { blocks: [{ type: "text", text: "final" }] });
    expect(merged("mg-2")?.blocks).toEqual([{ type: "text", text: "final" }]);
  });

  it("does not let an empty blocks array wipe the streamed ones", () => {
    // The message_created shape: an empty assistant bubble arriving after the
    // stream that filled it. Nothing on it is non-empty, so normalizeMessage
    // passes it through with its empty array and the merge has to refuse it.
    seed("mg-3");
    reingest("mg-3", { content: "", blocks: [] });
    expect(merged("mg-3")?.blocks).toEqual([{ type: "text", text: "seeded" }]);
  });

  it("adopts a non-empty incoming tool_calls list", () => {
    seed("mg-4");
    reingest("mg-4", {
      tool_calls: [{ id: "t-1", title: "readFile", kind: "read", status: "completed", ts: 0 }],
    });
    expect(merged("mg-4")?.tool_calls).toHaveLength(1);
  });

  it("does not let an empty tool_calls list wipe the streamed ones", () => {
    seed("mg-5", {
      tool_calls: [{ id: "t-1", title: "readFile", kind: "read", status: "pending", ts: 0 }],
    });
    reingest("mg-5", { tool_calls: [] });
    expect(merged("mg-5")?.tool_calls).toHaveLength(1);
  });

  it("adopts a non-empty incoming plan", () => {
    seed("mg-6");
    reingest("mg-6", { plan: [{ content: "step one", priority: "high", status: "pending" }] });
    expect(merged("mg-6")?.plan).toHaveLength(1);
  });

  it("does not let an empty plan wipe the one already rendered", () => {
    seed("mg-7", { plan: [{ content: "step one", priority: "high", status: "pending" }] });
    reingest("mg-7", { plan: [] });
    expect(merged("mg-7")?.plan).toHaveLength(1);
  });

  it("adopts a non-empty incoming code_references list", () => {
    seed("mg-8");
    reingest("mg-8", { code_references: [{ license_name: "MIT" }] });
    expect(merged("mg-8")?.code_references).toEqual([{ license_name: "MIT" }]);
  });

  it("does not let an empty code_references list wipe the footnote", () => {
    seed("mg-9", { code_references: [{ license_name: "MIT" }] });
    reingest("mg-9", { code_references: [] });
    expect(merged("mg-9")?.code_references).toEqual([{ license_name: "MIT" }]);
  });

  it("does not let an empty attachments list wipe the turn header's pills", () => {
    seed("mg-10", { attachments: [{ path: "out/shot.png", name: "shot.png" }] });
    reingest("mg-10", { attachments: [] });
    expect(merged("mg-10")?.attachments).toEqual([{ path: "out/shot.png", name: "shot.png" }]);
  });

  it("adopts a refusal that only the later frame carries", () => {
    seed("mg-11");
    reingest("mg-11", { refusal: { category: "policy" } });
    expect(merged("mg-11")?.refusal).toEqual({ category: "policy" });
  });

  it("adopts an event_kind that only the later frame carries", () => {
    seed("mg-12");
    reingest("mg-12", { event_kind: "compacted" });
    expect(merged("mg-12")?.event_kind).toBe("compacted");
  });

  it("adopts a later timestamp", () => {
    seed("mg-13");
    reingest("mg-13", { ts: 9 });
    expect(merged("mg-13")?.ts).toBe(9);
  });

  it("does not let a zero timestamp overwrite a real one", () => {
    // A frame that never had a ts decodes as 0, and a message stamped 1970 sorts
    // to the top of the transcript.
    seed("mg-14");
    reingest("mg-14", { ts: 0 });
    expect(merged("mg-14")?.ts).toBe(5);
  });
});

// ---------------------------------------------------------------------------
// ingestMessage's bookkeeping.
// ---------------------------------------------------------------------------

describe("Store ingestMessage bookkeeping", () => {
  it("never lowers the server's count when a message lands", () => {
    setSessions([{ ...makeSession("ig-1"), message_count: 7 }]);
    appendMessage("ig-1", { id: "m-1", role: "user", ts: 1, content: "one" });
    expect(get("ig-1")?.message_count).toBe(7);
  });

  it("repaints when a message lands", () => {
    resetStore("ig-2");
    const before = messagesVersion.peek();
    appendMessage("ig-2", { id: "m-1", role: "user", ts: 1, content: "one" });
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("repaints when an existing message is merged over", () => {
    resetStore("ig-3");
    appendMessage("ig-3", { id: "m-1", role: "user", ts: 1, content: "one" });
    const before = messagesVersion.peek();
    upsertMessage("ig-3", { id: "m-1", role: "user", ts: 1, content: "one (sanitized)" });
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("indexes messages that arrived with the session, so a replay merges", () => {
    // The page-load shape: the fetched window is already on the record when the
    // first ingest happens, so the index has to be built FROM it rather than
    // accumulated by the ingests. Without that, every message in the window is
    // appended a second time by the next refetch or reconnect replay.
    setSessions([
      {
        ...makeSession("mi-1"),
        message_count: 2,
        messages: [
          { id: "m-1", role: "user", ts: 1, content: "one" },
          {
            id: "m-2",
            role: "assistant",
            ts: 2,
            content: "two",
            blocks: [{ type: "text", text: "two" }],
          },
        ],
      },
    ]);
    upsertMessage("mi-1", {
      id: "m-2",
      role: "assistant",
      ts: 2,
      content: "two (sanitized)",
      blocks: [{ type: "text", text: "two (sanitized)" }],
    });
    expect(get("mi-1")?.messages).toHaveLength(2);
    expect(get("mi-1")?.messages[1]?.content).toBe("two (sanitized)");
  });

  it("re-derives the index after a page of history is prepended", () => {
    // The load-more path in store-load.ts: it prepends the older page onto
    // `session.messages` (store-load.ts:181) and then calls rebuildMsgIndex,
    // which is the only way the index can learn that every resident message
    // shifted. Without the rebuild the next message_updated for a resident id
    // merges into whatever now sits at its stale slot — silently rewriting a
    // different turn.
    setSessions([makeSession("rb-1")]);
    appendMessage("rb-1", {
      id: "m-new",
      role: "assistant",
      ts: 9,
      content: "newest",
      blocks: [{ type: "text", text: "newest" }],
    });
    const session = get("rb-1");
    if (session === undefined) {
      throw new Error("session went missing");
    }
    session.messages = [
      { id: "m-old-1", role: "user", ts: 1, content: "oldest" },
      {
        id: "m-old-2",
        role: "assistant",
        ts: 2,
        content: "older",
        blocks: [{ type: "text", text: "older" }],
      },
      ...session.messages,
    ];
    rebuildMsgIndex("rb-1", session.messages);

    upsertMessage("rb-1", {
      id: "m-new",
      role: "assistant",
      ts: 9,
      content: "newest (sanitized)",
      blocks: [{ type: "text", text: "newest (sanitized)" }],
    });

    expect(get("rb-1")?.messages).toHaveLength(3);
    expect(get("rb-1")?.messages[2]?.content).toBe("newest (sanitized)");
    expect(get("rb-1")?.messages[0]?.content).toBe("oldest");
  });
});

// ---------------------------------------------------------------------------
// setTurnSummary: the turn footer's ledger.
//
// It stamps the chat's LAST ASSISTANT message, which is not the last message —
// the next user turn can already be on the record when a background turn's
// summary arrives. Every field is guarded on being present AND meaningful,
// because a zero credit count or an empty changed-files map would render a
// footer claiming a measurement nobody made.
// ---------------------------------------------------------------------------

describe("Store setTurnSummary", () => {
  function chatWithTurn(chat: string): void {
    setSessions([makeSession(chat)]);
    appendMessage(chat, {
      id: "t-a",
      role: "assistant",
      ts: 1,
      content: "first",
      blocks: [{ type: "text", text: "first" }],
    });
    appendMessage(chat, {
      id: "t-b",
      role: "assistant",
      ts: 2,
      content: "second",
      blocks: [{ type: "text", text: "second" }],
    });
    appendMessage(chat, { id: "t-c", role: "user", ts: 3, content: "next question" });
  }

  it("walks back past a trailing user message to the assistant's", () => {
    chatWithTurn("ts-1");
    setTurnSummary("ts-1", { credits: 4 });
    expect(get("ts-1")?.messages[1]?.turn_credits).toBe(4);
    expect(get("ts-1")?.messages[2]?.turn_credits).toBeUndefined();
  });

  it("stamps the credits and repaints", () => {
    chatWithTurn("ts-2");
    const before = messagesVersion.peek();
    setTurnSummary("ts-2", { credits: 12 });
    expect(get("ts-2")?.messages[1]?.turn_credits).toBe(12);
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("ignores a zero credit count rather than stamping one", () => {
    chatWithTurn("ts-3");
    const before = messagesVersion.peek();
    setTurnSummary("ts-3", { credits: 0 });
    expect(get("ts-3")?.messages[1]?.turn_credits).toBeUndefined();
    expect(messagesVersion.peek()).toBe(before);
  });

  it("stamps the elapsed time and repaints", () => {
    chatWithTurn("ts-4");
    const before = messagesVersion.peek();
    setTurnSummary("ts-4", { elapsedMs: 250 });
    expect(get("ts-4")?.messages[1]?.turn_elapsed_ms).toBe(250);
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("ignores a zero elapsed time", () => {
    chatWithTurn("ts-5");
    const before = messagesVersion.peek();
    setTurnSummary("ts-5", { elapsedMs: 0 });
    expect(get("ts-5")?.messages[1]?.turn_elapsed_ms).toBeUndefined();
    expect(messagesVersion.peek()).toBe(before);
  });

  it("stamps the changed files and repaints", () => {
    chatWithTurn("ts-6");
    const before = messagesVersion.peek();
    setTurnSummary("ts-6", {
      changedFiles: { "src/parser.ts": { lines_added: 3, lines_removed: 1 } },
    });
    expect(get("ts-6")?.messages[1]?.changed_files).toEqual({
      "src/parser.ts": { lines_added: 3, lines_removed: 1 },
    });
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("ignores an empty changed-files map", () => {
    chatWithTurn("ts-7");
    const before = messagesVersion.peek();
    setTurnSummary("ts-7", { changedFiles: {} });
    expect(get("ts-7")?.messages[1]?.changed_files).toBeUndefined();
    expect(messagesVersion.peek()).toBe(before);
  });

  it("stamps the model and repaints", () => {
    chatWithTurn("ts-8");
    const before = messagesVersion.peek();
    setTurnSummary("ts-8", { model: "claude-opus-5" });
    expect(get("ts-8")?.messages[1]?.turn_model).toBe("claude-opus-5");
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("does not repaint for a summary carrying nothing", () => {
    chatWithTurn("ts-9");
    const before = messagesVersion.peek();
    setTurnSummary("ts-9", {});
    expect(messagesVersion.peek()).toBe(before);
  });
});

// ---------------------------------------------------------------------------
// The per-chat switches. Each is a set-once control whose SSE echo arrives
// after the optimistic local write, so "already that value" is the common case
// and has to be a no-op.
// ---------------------------------------------------------------------------

describe("Store per-chat switches", () => {
  it("setCurrentMode applies a new mode", () => {
    resetStore("sw-1");
    setCurrentMode("sw-1", "plan");
    expect(get("sw-1")?.current_mode_id).toBe("plan");
  });

  it("setCurrentMode does not churn on the mode it already has", () => {
    resetStore("sw-2");
    setCurrentMode("sw-2", "plan");
    const settled = get("sw-2");
    setCurrentMode("sw-2", "plan");
    expect(get("sw-2")).toBe(settled);
  });

  it("setSupervisedMode applies a new value", () => {
    resetStore("sw-3");
    setSupervisedMode("sw-3", true);
    expect(get("sw-3")?.supervised_mode).toBe(true);
  });

  it("setSupervisedMode does not churn on the value it already has", () => {
    resetStore("sw-4");
    const settled = get("sw-4");
    setSupervisedMode("sw-4", false);
    expect(get("sw-4")).toBe(settled);
  });

  it("setEffort applies a level", () => {
    resetStore("sw-5");
    setEffort("sw-5", "high");
    expect(get("sw-5")?.effort).toBe("high");
  });

  it("setEffort does not churn on the level it already has", () => {
    resetStore("sw-6");
    setEffort("sw-6", "high");
    const settled = get("sw-6");
    setEffort("sw-6", "high");
    expect(get("sw-6")).toBe(settled);
  });

  it("setEffort reads an absent level as empty, so clearing an unset one is a no-op", () => {
    resetStore("sw-7");
    const before = get("sw-7");
    setEffort("sw-7", "");
    expect(get("sw-7")).toBe(before);
  });
});

// ---------------------------------------------------------------------------
// appendChunk: the delta lands in three places at once — the message's flat
// content/reasoning string, the chronological block at the server's block
// index, and whichever signal is mounted over that block. The block array is
// what the renderer walks, so a delta that reaches the string and not the block
// is invisible.
// ---------------------------------------------------------------------------

describe("Store appendChunk blocks", () => {
  it("puts the first text delta in the block the server named", () => {
    resetStore("ac-1");
    appendChunk("ac-1", "m-1", "hello", false, 0, "");
    expect(get("ac-1")?.messages[0]?.blocks).toEqual([{ type: "text", text: "hello" }]);
  });

  it("appends a second text delta to the same block", () => {
    resetStore("ac-2");
    appendChunk("ac-2", "m-1", "hel", false, 0, "");
    appendChunk("ac-2", "m-1", "lo", false, 0, "");
    expect(get("ac-2")?.messages[0]?.blocks?.[0]?.text).toBe("hello");
  });

  it("accumulates thinking on a reasoning block rather than replacing it", () => {
    resetStore("ac-3");
    appendChunk("ac-3", "m-1", "let ", true, 0, "");
    appendChunk("ac-3", "m-1", "me think", true, 0, "");
    expect(get("ac-3")?.messages[0]?.blocks?.[0]?.thinking).toBe("let me think");
  });

  it("keeps text and thinking on separate blocks", () => {
    resetStore("ac-4");
    appendChunk("ac-4", "m-1", "why", true, 0, "");
    appendChunk("ac-4", "m-1", "because", false, 1, "");
    expect(get("ac-4")?.messages[0]?.blocks).toEqual([
      { type: "thinking", thinking: "why" },
      { type: "text", text: "because" },
    ]);
  });

  it("pads the gap when the server's block index runs ahead", () => {
    resetStore("ac-5");
    appendChunk("ac-5", "m-1", "third", false, 2, "");
    const blocks = get("ac-5")?.messages[0]?.blocks;
    expect(blocks).toHaveLength(3);
    expect(blocks?.[2]?.text).toBe("third");
  });

  it("stamps a delegated block with its subtask id", () => {
    resetStore("ac-6");
    appendChunk("ac-6", "m-1", "delegated", false, 0, "sub-7");
    expect(get("ac-6")?.messages[0]?.blocks?.[0]?.agent_subtask_id).toBe("sub-7");
  });

  it("keeps the server's message count when a streamed message appears", () => {
    setSessions([{ ...makeSession("ac-7"), message_count: 9 }]);
    appendChunk("ac-7", "m-1", "x", false, 0, "");
    expect(get("ac-7")?.message_count).toBe(9);
  });

  it("appends a new streaming message rather than writing over a resident one", () => {
    setSessions([makeSession("ac-8")]);
    appendMessage("ac-8", { id: "m-old-0", role: "user", ts: 1, content: "q" });
    appendMessage("ac-8", {
      id: "m-old-1",
      role: "assistant",
      ts: 2,
      content: "a",
      blocks: [{ type: "text", text: "a" }],
    });
    appendChunk("ac-8", "m-new", "streaming", false, 0, "");
    expect(get("ac-8")?.messages).toHaveLength(3);
    expect(get("ac-8")?.messages[1]?.content).toBe("a");
    expect(get("ac-8")?.messages[2]?.content).toBe("streaming");
  });

  it("streams into the resident message its index names", () => {
    setSessions([makeSession("ac-9")]);
    appendMessage("ac-9", { id: "m-0", role: "user", ts: 1, content: "q" });
    appendMessage("ac-9", {
      id: "m-1",
      role: "assistant",
      ts: 2,
      content: "par",
      blocks: [{ type: "text", text: "par" }],
    });
    appendChunk("ac-9", "m-1", "tial", false, 0, "");
    expect(get("ac-9")?.messages).toHaveLength(2);
    expect(get("ac-9")?.messages[1]?.content).toBe("partial");
  });
});

// ---------------------------------------------------------------------------
// appendChunk's repaint discipline: a mounted signal carries the delta and the
// list must NOT repaint; with nothing mounted the list is the only channel and
// it must. Getting this backwards is either a dropped delta or a transcript
// that re-renders per character.
// ---------------------------------------------------------------------------

describe("Store appendChunk repaint discipline", () => {
  it("stays off the list when a streaming signal is carrying the text", async () => {
    resetStore("ar-1");
    appendChunk("ar-1", "m-1", "hello", false, 0, "");
    await tick();
    const sig = ensureStreamingSig("m-1", "hello");
    const before = messagesVersion.peek();
    appendChunk("ar-1", "m-1", " world", false, 0, "");
    await tick();
    expect(sig.value).toBe("hello world");
    expect(messagesVersion.peek()).toBe(before);
    clearStreamingSig("m-1");
  });

  it("repaints the list when nothing is mounted to carry the text", async () => {
    resetStore("ar-2");
    appendChunk("ar-2", "m-1", "hello", false, 0, "");
    await tick();
    const before = messagesVersion.peek();
    appendChunk("ar-2", "m-1", " world", false, 0, "");
    await tick();
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("routes the text delta to the per-block signal without repainting", async () => {
    resetStore("ar-3");
    appendChunk("ar-3", "m-1", "hello", false, 0, "");
    await tick();
    const blockSig = ensureBlockTextSig("m-1", 0, "hello");
    const before = messagesVersion.peek();
    appendChunk("ar-3", "m-1", " world", false, 0, "");
    await tick();
    expect(blockSig.value).toBe("hello world");
    expect(messagesVersion.peek()).toBe(before);
    clearAllBlockSigs();
  });

  it("routes a reasoning delta to its own per-block signal", async () => {
    resetStore("ar-4");
    appendChunk("ar-4", "m-1", "why", true, 0, "");
    await tick();
    const blockSig = ensureBlockThinkingSig("m-1", 0, "why");
    const before = messagesVersion.peek();
    appendChunk("ar-4", "m-1", " not", true, 0, "");
    await tick();
    expect(blockSig.value).toBe("why not");
    expect(messagesVersion.peek()).toBe(before);
    clearAllBlockSigs();
  });

  it("repaints for a reasoning delta with nothing mounted", async () => {
    resetStore("ar-5");
    appendChunk("ar-5", "m-1", "why", true, 0, "");
    await tick();
    const before = messagesVersion.peek();
    appendChunk("ar-5", "m-1", " not", true, 0, "");
    await tick();
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("repaints when a refusal is stamped, so the callout can mount", async () => {
    resetStore("ar-6");
    appendChunk("ar-6", "m-1", "I can't", false, 0, "");
    await tick();
    const sig = ensureStreamingSig("m-1", "I can't");
    const before = messagesVersion.peek();
    appendChunk("ar-6", "m-1", " help with that", false, 0, "", 0, { category: "policy" });
    await tick();
    expect(get("ar-6")?.messages[0]?.refusal).toEqual({ category: "policy" });
    // The delta still rides the signal, as it would without a refusal...
    expect(sig.value).toBe("I can't help with that");
    // ...but the per-block signal carries text only, so a message-level callout
    // needs the keyed reconcile as well.
    expect(messagesVersion.peek()).toBe(before + 1);
    clearStreamingSig("m-1");
  });

  it("stamps a refusal once, so a later frame cannot restate it", async () => {
    resetStore("ar-7");
    appendChunk("ar-7", "m-1", "a", false, 0, "", 0, { category: "policy" });
    await tick();
    appendChunk("ar-7", "m-1", "b", false, 0, "", 0, { category: "something else" });
    await tick();
    expect(get("ar-7")?.messages[0]?.refusal).toEqual({ category: "policy" });
  });
});

// ---------------------------------------------------------------------------
// upsertToolCall: the same three-way write as a chunk, plus the tool call's own
// identity. A tool call is updated many times (pending → in_progress →
// completed), so finding the existing entry is the hot path and appending a
// duplicate would render the same card twice.
// ---------------------------------------------------------------------------

describe("Store upsertToolCall", () => {
  const call = (id: string, status = "pending") => ({
    id,
    title: "readFile",
    kind: "read" as const,
    status: status as ToolCall["status"],
    ts: 0,
  });

  it("mints the assistant message when the tool call arrives first", () => {
    resetStore("tc-1");
    upsertToolCall("tc-1", "m-1", call("t-1"), 0);
    const msg = get("tc-1")?.messages[0];
    expect(msg?.tool_calls).toEqual([call("t-1")]);
    expect(msg?.blocks).toEqual([{ type: "tool_use", tool_call_id: "t-1" }]);
  });

  it("repaints when it mints one", () => {
    resetStore("tc-2");
    const before = messagesVersion.peek();
    upsertToolCall("tc-2", "m-1", call("t-1"), 0);
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("keeps the server's message count when it mints one", () => {
    setSessions([{ ...makeSession("tc-3"), message_count: 6 }]);
    upsertToolCall("tc-3", "m-1", call("t-1"), 0);
    expect(get("tc-3")?.message_count).toBe(6);
  });

  it("indexes the message it minted, so the next call finds it", () => {
    resetStore("tc-4");
    upsertToolCall("tc-4", "m-1", call("t-1"), 0);
    upsertToolCall("tc-4", "m-1", call("t-2"), 1);
    expect(get("tc-4")?.messages).toHaveLength(1);
    expect(get("tc-4")?.messages[0]?.tool_calls).toHaveLength(2);
  });

  it("updates a call in place rather than appending a duplicate", () => {
    resetStore("tc-5");
    upsertToolCall("tc-5", "m-1", call("t-1"), 0);
    upsertToolCall("tc-5", "m-1", call("t-1", "completed"), 0);
    expect(get("tc-5")?.messages[0]?.tool_calls).toEqual([call("t-1", "completed")]);
  });

  it("updates the named call and leaves its siblings alone", () => {
    resetStore("tc-6");
    upsertToolCall("tc-6", "m-1", call("t-1"), 0);
    upsertToolCall("tc-6", "m-1", call("t-2"), 1);
    upsertToolCall("tc-6", "m-1", call("t-2", "completed"), 1);
    const calls = get("tc-6")?.messages[0]?.tool_calls;
    expect(calls?.[0]?.status).toBe("pending");
    expect(calls?.[1]?.status).toBe("completed");
  });

  it("pins a new call to the block index the server reported", () => {
    resetStore("tc-7");
    appendChunk("tc-7", "m-1", "let me look", false, 0, "");
    upsertToolCall("tc-7", "m-1", call("t-1"), 2);
    const blocks = get("tc-7")?.messages[0]?.blocks;
    expect(blocks).toHaveLength(3);
    expect(blocks?.[2]).toEqual({ type: "tool_use", tool_call_id: "t-1" });
  });

  it("leaves a block already standing at that index alone", () => {
    resetStore("tc-8");
    appendChunk("tc-8", "m-1", "one", false, 0, "");
    appendChunk("tc-8", "m-1", "two", false, 1, "");
    upsertToolCall("tc-8", "m-1", call("t-1"), 1);
    const blocks = get("tc-8")?.messages[0]?.blocks;
    expect(blocks).toHaveLength(2);
    expect(blocks?.[1]).toEqual({ type: "text", text: "two" });
  });

  it("repaints when a call joins a message already on screen", async () => {
    resetStore("tc-9");
    appendMessage("tc-9", { id: "m-1", role: "assistant", ts: 1, content: "", blocks: [] });
    await tick();
    const before = messagesVersion.peek();
    upsertToolCall("tc-9", "m-1", call("t-1"), 0);
    await tick();
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("fans a later update through the tool's own signal without repainting", async () => {
    resetStore("tc-10");
    upsertToolCall("tc-10", "m-1", call("t-1"), 0);
    await tick();
    const sig = ensureToolCallSig("t-1", call("t-1"));
    const before = messagesVersion.peek();
    upsertToolCall("tc-10", "m-1", call("t-1", "completed"), 0);
    await tick();
    expect(sig.value.status).toBe("completed");
    expect(messagesVersion.peek()).toBe(before);
    clearToolCallSig("t-1");
  });

  it("repaints a later update when no tool signal is mounted", async () => {
    resetStore("tc-11");
    upsertToolCall("tc-11", "m-1", call("t-1"), 0);
    await tick();
    const before = messagesVersion.peek();
    upsertToolCall("tc-11", "m-1", call("t-1", "completed"), 0);
    await tick();
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("mints a new message rather than embedding into a resident one", () => {
    setSessions([makeSession("tc-12")]);
    appendMessage("tc-12", { id: "m-0", role: "user", ts: 1, content: "q" });
    appendMessage("tc-12", {
      id: "m-1",
      role: "assistant",
      ts: 2,
      content: "a",
      blocks: [{ type: "text", text: "a" }],
    });
    upsertToolCall("tc-12", "m-new", call("t-1"), 0);
    expect(get("tc-12")?.messages).toHaveLength(3);
    expect(get("tc-12")?.messages[1]?.tool_calls).toBeUndefined();
  });

  it("embeds into the resident message its index names", () => {
    setSessions([makeSession("tc-13")]);
    appendMessage("tc-13", { id: "m-0", role: "user", ts: 1, content: "q" });
    appendMessage("tc-13", {
      id: "m-1",
      role: "assistant",
      ts: 2,
      content: "a",
      blocks: [{ type: "text", text: "a" }],
    });
    upsertToolCall("tc-13", "m-1", call("t-1"), 1);
    expect(get("tc-13")?.messages).toHaveLength(2);
    expect(get("tc-13")?.messages[1]?.tool_calls).toEqual([call("t-1")]);
  });
});

// ---------------------------------------------------------------------------
// setCodeReferences: licensed-code attributions arriving mid-turn. The refs
// persist server-side and render on reload, so a message that has not landed
// yet is a no-op — but attaching them to whichever message happens to sit at a
// fallback index would put another turn's attribution under this one.
// ---------------------------------------------------------------------------

describe("Store setCodeReferences", () => {
  function chatWithTwo(chat: string): void {
    setSessions([makeSession(chat)]);
    appendMessage(chat, { id: "m-1", role: "user", ts: 1, content: "any licensed code?" });
    appendMessage(chat, {
      id: "m-2",
      role: "assistant",
      ts: 2,
      content: "here it is",
      blocks: [{ type: "text", text: "here it is" }],
    });
  }

  it("attaches the attributions to the message the id names, and repaints", () => {
    chatWithTwo("cr-1");
    const before = messagesVersion.peek();
    setCodeReferences("cr-1", "m-2", [{ license_name: "MIT" }]);
    expect(get("cr-1")?.messages[1]?.code_references).toEqual([{ license_name: "MIT" }]);
    expect(messagesVersion.peek()).toBe(before + 1);
  });

  it("is a no-op for a message that has not arrived yet", () => {
    chatWithTwo("cr-2");
    setCodeReferences("cr-2", "m-not-here", [{ license_name: "MIT" }]);
    expect(get("cr-2")?.messages[0]?.code_references).toBeUndefined();
    expect(get("cr-2")?.messages[1]?.code_references).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// Steers: the two frames that carry an acknowledgement, and the entry a
// replayed frame must not touch.
// ---------------------------------------------------------------------------

describe("Store steer projection, frame by frame", () => {
  it("refreshes only the replayed steer, leaving its siblings alone", () => {
    resetStore("st-1");
    recordSteerQueued("st-1", { id: "s-1", text: "one" });
    recordSteerQueued("st-1", { id: "s-2", text: "two" });
    markSteerInjected("st-1", "s-1", "one");
    recordSteerQueued("st-1", { id: "s-2", text: "two, corrected" });
    expect(get("st-1")?.steers?.[0]).toEqual({ id: "s-1", text: "one", injected: true });
    expect(get("st-1")?.steers?.[1]).toEqual({
      id: "s-2",
      text: "two, corrected",
      injected: false,
    });
  });

  it("records an acknowledgement that rides the steer's first frame", () => {
    resetStore("st-2");
    markSteerInjected("st-2", "s-ghost", "from another tab", "switched the file to tabs");
    expect(get("st-2")?.steers).toEqual([
      {
        id: "s-ghost",
        text: "from another tab",
        injected: true,
        ack: "switched the file to tabs",
      },
    ]);
  });

  it("adds no verdict when a first frame's acknowledgement is empty", () => {
    // An empty ack is the absence of one. A chip carrying `ack: ""` renders a
    // verdict row with nothing in it.
    resetStore("st-3");
    markSteerInjected("st-3", "s-ghost", "from another tab", "");
    expect(get("st-3")?.steers).toEqual([
      { id: "s-ghost", text: "from another tab", injected: true },
    ]);
  });

  it("adds no verdict when a read frame's acknowledgement is empty", () => {
    resetStore("st-4");
    recordSteerQueued("st-4", { id: "s-1", text: "use tabs" });
    markSteerInjected("st-4", "s-1", "use tabs", "");
    expect(get("st-4")?.steers).toEqual([{ id: "s-1", text: "use tabs", injected: true }]);
  });
});
