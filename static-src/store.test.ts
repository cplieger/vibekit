// Unit tests for store.ts — property-based idempotency invariants.
import { describe, it, expect, vi } from "vitest";
import * as fc from "fast-check";
import {
  parseContextSize,
  setSessions,
  getSessions,
  get,
  setActive,
  getActive,
  appendMessage,
  upsertMessage,
  upsertHeader,
  removeChat,
  setThinking,
  setWorkingLabel,
  steerIDFor,
  recordSteerSent,
  forgetSteer,
  recordSteerQueued,
  promoteSteer,
  dropSteers,
  dropConfirmedSteers,
  restoreSteers,
  forgetSteers,
  steerCount,
  steerMarks,
  setName,
  activeSession,
  getActiveId,
  setModel,
  syncEpoch,
  bumpSyncEpoch,
  transcriptStale,
} from "./store.js";
import type { Block, ChatHeader, Session } from "./types.js";
import { effect } from "@cplieger/reactive";

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

  // The empty string is the store's sentinel for "nothing is active":
  // `getActiveId` returns it before the first `setActive`, and `setActive("")`
  // is how a selection is cleared. `setSessions` is fed straight from the
  // server's chat list, so a row whose id decoded to "" is a shape it can
  // receive — and it must not become the active chat by matching the sentinel,
  // which would show a phantom conversation to a user who has none selected.
  it("never resolves the no-active sentinel to a row whose id is empty", () => {
    setSessions([makeSession("real"), makeSession("")]);
    setActive("real");
    setActive("");

    expect(getActiveId()).toBe("");
    expect(getActive()).toBeUndefined();
    expect(activeSession.peek()).toBeUndefined();
    // The malformed row is still in the list; it is only barred from being the
    // active one, since dropping rows is not this function's job.
    expect(get("")?.id).toBe("");
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
    expect(count).toBe(afterRegister + 1);
    expect(activeSession.value?.working_label).toBe("x");

    const afterActiveChange = count;

    // A field change on an INACTIVE session fires only that session's signal,
    // which activeSession does not track — so the counter must not move.
    setWorkingLabel("b", "y");
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

// --- Mid-turn steers: two fields, two lifetimes ---
//
// `session.steers` is what the agent has NOT read (the dock's rows, whose
// lifetime is the turn) and `session.steer_marks` is what has LEFT it — read, or
// dropped unread at a boundary — anchored in the turn transcript, whose lifetime
// is the loaded transcript. An entry moves from the first to the second and never
// back, so every case below asserts BOTH sides of a move rather than a flag.
//
// Every case here is about surviving the wire rather than about data structure
// mechanics. The client writes intent (`recordSteerSent` on submit, `forgetSteer`
// on a refusal) and the server writes fact, and the server's frames arrive twice,
// out of order, or without the frame that should have preceded them. Those are
// the cases that matter.

/** A chat mid-turn: one user message and one assistant message carrying `blocks`
 *  blocks, which is what an anchor is measured against. `content` stays empty on
 *  purpose — `normalizeMessage` synthesizes a block from a non-empty content, so
 *  a message that is meant to have none has to have neither. */
function chatWithTurn(chatID: string, blocks: number): void {
  resetStore(chatID);
  appendMessage(chatID, { id: "u-1", role: "user", ts: 1, content: "do the thing" });
  appendMessage(chatID, { id: "a-1", role: "assistant", ts: 2, blocks: turnBlocks(blocks) });
}

function turnBlocks(n: number): Block[] {
  return Array.from({ length: n }, (_, i) => ({
    type: "text" as const,
    text: `block ${String(i)}`,
  }));
}

describe("Store steer projection", () => {
  // --- The client's half: intent, drawn on submit and un-drawn on a refusal ---

  // The row the user is owed for pressing Send, under the id KAS will return
  // (`internal/vibekit/commands.go:329-330`), which is what makes the reconcile a
  // plain by-id merge rather than a guess.
  it("records a submitted steer as pending, keyed by the derived id", () => {
    resetStore("chat-1");
    recordSteerSent("chat-1", "m-42", "actually use tabs");
    expect(steerIDFor("m-42")).toBe("steer-m-42");
    expect(get("chat-1")?.steers).toEqual([
      { id: "steer-m-42", text: "actually use tabs", pending: true },
    ]);
  });

  // submit.ts reuses one message id when it retries a failed attempt, so the
  // retry has to refresh the row rather than add a second one for one message.
  it("is idempotent by message id, so a retried submit adds no second row", () => {
    fc.assert(
      fc.property(fc.integer({ min: 1, max: 6 }), (repeats) => {
        resetStore("chat-1");
        for (let i = 0; i < repeats; i++) {
          recordSteerSent("chat-1", "m-1", "same message");
        }
        expect(steerCount("chat-1")).toBe(1);
      }),
    );
  });

  it("ignores a submit with no message id rather than creating an unaddressable row", () => {
    resetStore("chat-1");
    recordSteerSent("chat-1", "", "nowhere");
    expect(get("chat-1")?.steers).toBeUndefined();
  });

  // The rollback half. A 409 for the message just sent must un-draw its own row
  // and nothing else — a sibling still waiting is a different message.
  it("forgets one pending row and leaves a confirmed sibling alone", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "earlier" });
    recordSteerSent("chat-1", "m-2", "just sent");
    forgetSteer("chat-1", steerIDFor("m-2"));
    expect(get("chat-1")?.steers).toEqual([{ id: "steer-1", text: "earlier" }]);
  });

  it("deletes the field when the forgotten row was the last one", () => {
    resetStore("chat-1");
    recordSteerSent("chat-1", "m-1", "one");
    forgetSteer("chat-1", steerIDFor("m-1"));
    expect(get("chat-1")?.steers).toBeUndefined();
  });

  it("is a no-op when the id to forget is not held", () => {
    resetStore("chat-1");
    recordSteerSent("chat-1", "m-1", "one");
    const before = get("chat-1")?.steers;
    forgetSteer("chat-1", "steer-nope");
    // Same array identity: a no-op must not churn the session, or the dock would
    // repaint on every failed dispatch elsewhere.
    expect(get("chat-1")?.steers).toBe(before);
  });

  // --- The server's half: confirm, and the reconcile that keeps it one row ---

  it("records a queued steer as waiting, with no pending flag", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually use tabs" });
    expect(steerCount("chat-1")).toBe(1);
    expect(get("chat-1")?.steers?.[0]).toEqual({
      id: "steer-1",
      text: "actually use tabs",
    });
  });

  // THE case both halves exist for: the optimistic row and its confirmation are
  // one row, not two. The id matches because the client derived the one KAS
  // returns.
  it("confirms the pending row in place when the ids agree", () => {
    resetStore("chat-1");
    recordSteerSent("chat-1", "m-1", "use tabs instead");
    recordSteerQueued("chat-1", { id: steerIDFor("m-1"), text: "use tabs instead" });
    expect(get("chat-1")?.steers).toEqual([{ id: "steer-m-1", text: "use tabs instead" }]);
  });

  // The safety net for a KAS whose id convention has drifted: the oldest pending
  // row with the same TEXT adopts the server's id, so the reconcile does not
  // depend on the prefix rule holding.
  it("adopts a server id onto the pending row when only the text matches", () => {
    resetStore("chat-1");
    recordSteerSent("chat-1", "m-1", "use tabs instead");
    recordSteerQueued("chat-1", { id: "kas-generated-7", text: "use tabs instead" });
    expect(get("chat-1")?.steers).toEqual([{ id: "kas-generated-7", text: "use tabs instead" }]);
  });

  // With two in flight, the server's id lands on the OLDEST match, so the second
  // still has a row of its own waiting for its own frame.
  it("adopts onto the oldest pending row of equal text, leaving the newer pending", () => {
    resetStore("chat-1");
    recordSteerSent("chat-1", "m-1", "same words");
    recordSteerSent("chat-1", "m-2", "same words");
    recordSteerQueued("chat-1", { id: "kas-1", text: "same words" });
    expect(get("chat-1")?.steers).toEqual([
      { id: "kas-1", text: "same words" },
      { id: "steer-m-2", text: "same words", pending: true },
    ]);
  });

  // An SSE reconnect replays unacknowledged frames, so the same steer_queued can
  // legitimately arrive twice. Two rows for one message would misreport how much
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

  it("ignores a queued frame with an empty id", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "", text: "nowhere" });
    expect(steerCount("chat-1")).toBe(0);
  });

  // --- Promotion: out of the dock, into the transcript ---

  it("moves a read steer out of the dock and anchors it in the turn", () => {
    chatWithTurn("chat-1", 2);
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead" });
    promoteSteer("chat-1", "steer-1", "use tabs instead");
    expect(get("chat-1")?.steers).toBeUndefined();
    expect(steerMarks("chat-1")).toEqual([
      {
        id: "steer-1",
        text: "use tabs instead",
        anchor: { msgID: "a-1", blockIndex: 2 },
      },
    ]);
  });

  // The promotion also has to find an optimistic row whose id KAS never
  // confirmed: the injected frame can beat the queued one.
  it("promotes a still-pending row through the text fallback", () => {
    chatWithTurn("chat-1", 1);
    recordSteerSent("chat-1", "m-1", "use tabs instead");
    promoteSteer("chat-1", "kas-generated-7", "use tabs instead");
    expect(get("chat-1")?.steers).toBeUndefined();
    expect(steerMarks("chat-1").map((m) => m.id)).toEqual(["kas-generated-7"]);
  });

  // Two steer_injected frames, one id: KAS's read frame carries the text, the
  // agent's acknowledgement marker carries what it did. Each field is adopted
  // only from the frame that has it, or the second would blank the first's text
  // — and the second must not re-anchor a note already placed, or the reader
  // would watch it jump down the turn.
  it("merges a later acknowledgement onto the mark without blanking it", () => {
    chatWithTurn("chat-1", 1);
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead" });
    promoteSteer("chat-1", "steer-1", "use tabs instead");
    // A second block arrives between the two frames, so a re-anchor would be
    // visible as the note jumping down the turn.
    upsertMessage("chat-1", { id: "a-1", role: "assistant", ts: 2, blocks: turnBlocks(2) });
    promoteSteer("chat-1", "steer-1", "", "switched the file to tabs");
    expect(steerMarks("chat-1")).toEqual([
      {
        id: "steer-1",
        text: "use tabs instead",
        ack: "switched the file to tabs",
        anchor: { msgID: "a-1", blockIndex: 1 },
      },
    ]);
  });

  // A read frame after an ack frame must not erase the ack: SSE reconnect
  // replays every unanswered frame, so the two can arrive in either order.
  it("keeps an acknowledgement when a read frame is replayed after it", () => {
    chatWithTurn("chat-1", 1);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    promoteSteer("chat-1", "steer-1", "", "did the thing");
    promoteSteer("chat-1", "steer-1", "one");
    expect(steerMarks("chat-1")[0]?.ack).toBe("did the thing");
    expect(steerMarks("chat-1")).toHaveLength(1);
  });

  // steer_injected can arrive with no steer_queued behind it: another device sent
  // the steer, or this one connected mid-turn. Dropping it would leave the
  // transcript showing the agent change course with nothing explaining why.
  it("marks an injected steer it never saw queued", () => {
    chatWithTurn("chat-1", 0);
    promoteSteer("chat-1", "steer-ghost", "from another tab");
    expect(get("chat-1")?.steers).toBeUndefined();
    expect(steerMarks("chat-1")).toEqual([
      {
        id: "steer-ghost",
        text: "from another tab",
        anchor: { msgID: "a-1", blockIndex: 0 },
      },
    ]);
  });

  // An ack frame carries no text, so an id this client never saw has nothing to
  // label a note with. A note reading only "the agent did X" names no message
  // and cannot be matched to anything the user wrote.
  it("ignores an acknowledgement for a steer it never saw", () => {
    chatWithTurn("chat-1", 1);
    promoteSteer("chat-1", "steer-unknown", "", "did something");
    expect(get("chat-1")?.steer_marks).toBeUndefined();
  });

  it("ignores a promotion with an empty id", () => {
    chatWithTurn("chat-1", 1);
    promoteSteer("chat-1", "", "nowhere");
    expect(get("chat-1")?.steer_marks).toBeUndefined();
  });

  // A reconnect replays the queued frame for a steer the agent has since read.
  // Appending it as confirmed would put a delivered message back in the dock.
  it("does not resurrect a dock row for an already-promoted id", () => {
    chatWithTurn("chat-1", 1);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    promoteSteer("chat-1", "steer-1", "one");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    expect(get("chat-1")?.steers).toBeUndefined();
    expect(steerMarks("chat-1")).toHaveLength(1);
  });

  // A steer read before the turn produced anything belongs ABOVE the turn's
  // first block, and until that message exists there is no id to say so with.
  it("rebinds an anchor-less mark to the first assistant message that arrives", () => {
    resetStore("chat-1");
    appendMessage("chat-1", { id: "u-1", role: "user", ts: 1, content: "go" });
    promoteSteer("chat-1", "steer-1", "read before anything landed");
    expect(steerMarks("chat-1")[0]?.anchor).toEqual({ msgID: "", blockIndex: 0 });

    appendMessage("chat-1", { id: "a-9", role: "assistant", ts: 2, content: "here" });
    expect(steerMarks("chat-1")[0]?.anchor).toEqual({ msgID: "a-9", blockIndex: 0 });
  });

  // --- Turn-boundary drops: the same move, labelled undelivered ---

  it("promotes a dropped steer as undelivered, keeping its text", () => {
    chatWithTurn("chat-1", 3);
    recordSteerQueued("chat-1", { id: "steer-1", text: "never read" });
    dropSteers("chat-1", ["steer-1"]);
    expect(get("chat-1")?.steers).toBeUndefined();
    expect(steerMarks("chat-1")).toEqual([
      {
        id: "steer-1",
        text: "never read",
        dropped: true,
        anchor: { msgID: "a-1", blockIndex: 3 },
      },
    ]);
  });

  // KAS clears its buffer at EVERY boundary, so `steer_cleared` routinely names
  // ids the model already read. Those keep their existing note; a second one
  // claiming they were missed would be false.
  it("leaves an already-promoted id alone rather than marking it undelivered", () => {
    chatWithTurn("chat-1", 1);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    promoteSteer("chat-1", "steer-1", "one");
    dropSteers("chat-1", ["steer-1"]);
    expect(steerMarks("chat-1")).toEqual([
      { id: "steer-1", text: "one", anchor: { msgID: "a-1", blockIndex: 1 } },
    ]);
  });

  it("drops only the named ids", () => {
    chatWithTurn("chat-1", 1);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "three" });
    dropSteers("chat-1", ["steer-1", "steer-3"]);
    expect(get("chat-1")?.steers?.map((e) => e.id)).toEqual(["steer-2"]);
    expect(steerMarks("chat-1").map((m) => m.id)).toEqual(["steer-1", "steer-3"]);
  });

  // The field is DELETED rather than emptied so a cleared session compares equal
  // to one that never had steers — the dock dedups by value, and an empty array
  // would repaint on every turn boundary.
  it("deletes the field when the last steer goes", () => {
    chatWithTurn("chat-1", 1);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    dropSteers("chat-1", ["steer-1"]);
    expect(get("chat-1")?.steers).toBeUndefined();
    expect(steerMarks("chat-1")).toHaveLength(1);
  });

  it("treats an empty id list as drop-everything, which is what a turn boundary means", () => {
    chatWithTurn("chat-1", 1);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two" });
    dropSteers("chat-1", []);
    expect(get("chat-1")?.steers).toBeUndefined();
    expect(steerMarks("chat-1").map((m) => m.dropped)).toEqual([true, true]);
  });

  it("is a no-op for ids it does not hold, and for an unknown chat", () => {
    chatWithTurn("chat-1", 1);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    const before = get("chat-1")?.steers;
    dropSteers("chat-1", ["steer-nope"]);
    // Same array identity: a no-op must not churn the session, or every
    // boundary would repaint the row.
    expect(get("chat-1")?.steers).toBe(before);
    expect(get("chat-1")?.steer_marks).toBeUndefined();
    dropSteers("nonexistent");
    expect(steerCount("nonexistent")).toBe(0);
  });

  // --- The two paths that leave NO mark ---

  // An explicit discard is not the agent missing something, so the entries go
  // before the server's frame can promote them. Only the confirmed ones: a
  // pending steer is not in KAS's buffer yet, so the clear cannot address it and
  // removing it locally would hide a message still on its way.
  it("takes the confirmed rows out on an explicit discard and keeps the pending one", () => {
    chatWithTurn("chat-1", 1);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    recordSteerSent("chat-1", "m-2", "still sending");
    const removed = dropConfirmedSteers("chat-1");
    expect(removed.map((e) => e.id)).toEqual(["steer-1", "steer-m-2"]);
    expect(get("chat-1")?.steers).toEqual([
      { id: "steer-m-2", text: "still sending", pending: true },
    ]);
    // No note, which is the point: the user took the message back.
    expect(get("chat-1")?.steer_marks).toBeUndefined();
  });

  it("reports nothing removed when every row is still pending", () => {
    resetStore("chat-1");
    recordSteerSent("chat-1", "m-1", "still sending");
    expect(dropConfirmedSteers("chat-1")).toEqual([]);
    expect(steerCount("chat-1")).toBe(1);
  });

  // The rollback of a failed discard restores the array as it was, in order,
  // rather than reconstructing it from the entries taken.
  it("restores a discard snapshot in its original order", () => {
    resetStore("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two" });
    const removed = dropConfirmedSteers("chat-1");
    expect(get("chat-1")?.steers).toBeUndefined();
    restoreSteers("chat-1", removed);
    expect(get("chat-1")?.steers?.map((e) => e.id)).toEqual(["steer-1", "steer-2"]);
  });

  // A transport gap means the frames that resolved these steers may be among the
  // ones we lost, so promoting them would assert "the agent never read this" on
  // no evidence. Marks already established are facts and stay.
  it("forgets the dock on a gap without marking anything undelivered", () => {
    chatWithTurn("chat-1", 1);
    recordSteerQueued("chat-1", { id: "steer-1", text: "read one" });
    promoteSteer("chat-1", "steer-1", "read one");
    recordSteerQueued("chat-1", { id: "steer-2", text: "unresolved" });
    forgetSteers("chat-1");
    expect(get("chat-1")?.steers).toBeUndefined();
    expect(steerMarks("chat-1").map((m) => m.id)).toEqual(["steer-1"]);
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
    const sig = ensureToolCallSig("chat-1", "t1", baseTC("t1", "pending"));
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

    clearToolCallSig("chat-1", "t1");
  });

  it("ensureToolCallSig is idempotent on the chat and id together", () => {
    const a = ensureToolCallSig("chat-1", "t-id", baseTC("t-id", "pending"));
    const b = ensureToolCallSig("chat-1", "t-id", baseTC("t-id", "completed"));
    // Same signal — initial value preserved (b's `initial` is ignored).
    expect(a).toBe(b);
    expect(a.value.status).toBe("pending");
    clearToolCallSig("chat-1", "t-id");
  });

  // THE KEYING: a tool call id is backend-authored and the wire guarantees no
  // uniqueness for it, while `upsertToolCall` runs for whatever chat a frame
  // arrived on. Keyed on the id alone, a collision wrote a background chat's card
  // state into the visible chat's card.
  it("keeps two chats' signals for one tool call id apart", () => {
    const a = ensureToolCallSig("chat-A", "dup", baseTC("dup", "pending"));
    const b = ensureToolCallSig("chat-B", "dup", baseTC("dup", "pending"));
    expect(a).not.toBe(b);
  });

  it("does not move a signal ensured under chat A when chat B is written", () => {
    resetStore("chat-A");
    resetStore("chat-B");
    upsertToolCall("chat-A", "m1", baseTC("dup", "pending"), 0);
    upsertToolCall("chat-B", "m1", baseTC("dup", "pending"), 0);
    const a = ensureToolCallSig("chat-A", "dup", baseTC("dup", "pending"));

    upsertToolCall("chat-B", "m1", baseTC("dup", "completed"), 0);

    expect(a.value.status).toBe("pending");
    clearToolCallSig("chat-A", "dup");
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
// The render signal: one bump per list change, coalesced, PER CHAT.
//
// Each chat's `messagesVersionOf` signal is the renderer's only coarse "the
// list changed" input, and the streaming paths deliberately do NOT bump it per
// delta — the per-block and per-message signals carry those. Worth pinning:
// a list change bumps exactly once; several changes in one tick still bump
// exactly once; a BACKGROUND chat's changes bump its own signal and never the
// active chat's; and a chat removed before its deferred flush does not get its
// version signal re-minted by the flush.
// ---------------------------------------------------------------------------

import {
  messagesVersionOf,
  bumpMessages,
  renderCauseOf,
  watchActiveId,
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

describe("per-chat messages version", () => {
  it("counts up by one per explicit bump, on the named chat only", () => {
    const mine = messagesVersionOf("mv-a").peek();
    const other = messagesVersionOf("mv-b").peek();
    bumpMessages("mv-a");
    expect(messagesVersionOf("mv-a").peek()).toBe(mine + 1);
    expect(messagesVersionOf("mv-b").peek()).toBe(other);
  });

  it("coalesces two new messages arriving in one tick into one render", async () => {
    resetStore("mv-1");
    const before = messagesVersionOf("mv-1").peek();
    appendChunk("mv-1", "m-a", "a", false, 0, "");
    appendChunk("mv-1", "m-b", "b", false, 0, "");
    // Deferred: the coalescer owns the microtask, so nothing has repainted yet.
    expect(messagesVersionOf("mv-1").peek()).toBe(before);
    await tick();
    expect(messagesVersionOf("mv-1").peek()).toBe(before + 1);
  });

  it("schedules again once the deferred render has run", async () => {
    resetStore("mv-2");
    appendChunk("mv-2", "m-a", "a", false, 0, "");
    await tick();
    const between = messagesVersionOf("mv-2").peek();
    appendChunk("mv-2", "m-b", "b", false, 0, "");
    await tick();
    // The guard has to reset, or the second turn of any chat never repaints.
    expect(messagesVersionOf("mv-2").peek()).toBe(between + 1);
  });

  it("a background chat's stream bumps its own version, never the active chat's", async () => {
    setSessions([makeSession("mv-fg"), makeSession("mv-bg")]);
    setActive("mv-fg");
    const fg = messagesVersionOf("mv-fg").peek();
    const bg = messagesVersionOf("mv-bg").peek();
    appendChunk("mv-bg", "m-a", "a", false, 0, "");
    await tick();
    expect(messagesVersionOf("mv-bg").peek()).toBe(bg + 1);
    expect(messagesVersionOf("mv-fg").peek()).toBe(fg);
  });

  it("a chat removed before its deferred flush is skipped, not re-minted", async () => {
    resetStore("mv-gone");
    appendChunk("mv-gone", "m-a", "a", false, 0, "");
    removeChat("mv-gone");
    const fresh = messagesVersionOf("mv-gone").peek(); // a NEW signal, minted by this read
    await tick();
    // The parked flush must not have bumped the re-minted signal.
    expect(messagesVersionOf("mv-gone").peek()).toBe(fresh);
  });

  it("transcript facts bump the chat's version (coalesced)", async () => {
    resetStore("mv-fact");
    const before = messagesVersionOf("mv-fact").peek();
    setThinking("mv-fact", true);
    await tick();
    expect(messagesVersionOf("mv-fact").peek()).toBe(before + 1);
    const mid = messagesVersionOf("mv-fact").peek();
    setTurnFailed("mv-fact");
    setWorkingLabel("mv-fact", "Reading files");
    await tick();
    // Two facts in one tick coalesce into one repaint.
    expect(messagesVersionOf("mv-fact").peek()).toBe(mid + 1);
  });

  it("a replayed fact frame that changes nothing bumps nothing", async () => {
    resetStore("mv-noop");
    setTurnFailed("mv-noop");
    await tick();
    const before = messagesVersionOf("mv-noop").peek();
    setTurnFailed("mv-noop"); // replay: latch already set
    await tick();
    expect(messagesVersionOf("mv-noop").peek()).toBe(before);
  });

  it("watchActiveId reads the active id", () => {
    setSessions([makeSession("mv-w")]);
    setActive("mv-w");
    expect(watchActiveId()).toBe("mv-w");
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
    const before = messagesVersionOf("ig-2").peek();
    appendMessage("ig-2", { id: "m-1", role: "user", ts: 1, content: "one" });
    expect(messagesVersionOf("ig-2").peek()).toBe(before + 1);
  });

  it("repaints when an existing message is merged over", () => {
    resetStore("ig-3");
    appendMessage("ig-3", { id: "m-1", role: "user", ts: 1, content: "one" });
    const before = messagesVersionOf("ig-3").peek();
    upsertMessage("ig-3", { id: "m-1", role: "user", ts: 1, content: "one (sanitized)" });
    expect(messagesVersionOf("ig-3").peek()).toBe(before + 1);
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
    // Activated because four of these cases assert a repaint, and a repaint is
    // gated on the chat being the one on screen ("repaints are gated on the
    // active chat" below). The stamping itself is unconditional either way, which
    // is what the non-repaint cases here check.
    setActive(chat);
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
    const before = messagesVersionOf("ts-2").peek();
    setTurnSummary("ts-2", { credits: 12 });
    expect(get("ts-2")?.messages[1]?.turn_credits).toBe(12);
    expect(messagesVersionOf("ts-2").peek()).toBe(before + 1);
  });

  it("ignores a zero credit count rather than stamping one", () => {
    chatWithTurn("ts-3");
    const before = messagesVersionOf("ts-3").peek();
    setTurnSummary("ts-3", { credits: 0 });
    expect(get("ts-3")?.messages[1]?.turn_credits).toBeUndefined();
    expect(messagesVersionOf("ts-3").peek()).toBe(before);
  });

  it("stamps the elapsed time and repaints", () => {
    chatWithTurn("ts-4");
    const before = messagesVersionOf("ts-4").peek();
    setTurnSummary("ts-4", { elapsedMs: 250 });
    expect(get("ts-4")?.messages[1]?.turn_elapsed_ms).toBe(250);
    expect(messagesVersionOf("ts-4").peek()).toBe(before + 1);
  });

  it("ignores a zero elapsed time", () => {
    chatWithTurn("ts-5");
    const before = messagesVersionOf("ts-5").peek();
    setTurnSummary("ts-5", { elapsedMs: 0 });
    expect(get("ts-5")?.messages[1]?.turn_elapsed_ms).toBeUndefined();
    expect(messagesVersionOf("ts-5").peek()).toBe(before);
  });

  it("stamps the changed files and repaints", () => {
    chatWithTurn("ts-6");
    const before = messagesVersionOf("ts-6").peek();
    setTurnSummary("ts-6", {
      changedFiles: { "src/parser.ts": { lines_added: 3, lines_removed: 1 } },
    });
    expect(get("ts-6")?.messages[1]?.changed_files).toEqual({
      "src/parser.ts": { lines_added: 3, lines_removed: 1 },
    });
    expect(messagesVersionOf("ts-6").peek()).toBe(before + 1);
  });

  it("ignores an empty changed-files map", () => {
    chatWithTurn("ts-7");
    const before = messagesVersionOf("ts-7").peek();
    setTurnSummary("ts-7", { changedFiles: {} });
    expect(get("ts-7")?.messages[1]?.changed_files).toBeUndefined();
    expect(messagesVersionOf("ts-7").peek()).toBe(before);
  });

  it("stamps the model and repaints", () => {
    chatWithTurn("ts-8");
    const before = messagesVersionOf("ts-8").peek();
    setTurnSummary("ts-8", { model: "claude-opus-5" });
    expect(get("ts-8")?.messages[1]?.turn_model).toBe("claude-opus-5");
    expect(messagesVersionOf("ts-8").peek()).toBe(before + 1);
  });

  it("does not repaint for a summary carrying nothing", () => {
    chatWithTurn("ts-9");
    const before = messagesVersionOf("ts-9").peek();
    setTurnSummary("ts-9", {});
    expect(messagesVersionOf("ts-9").peek()).toBe(before);
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

  it("accepts a block index that goes BACKWARDS mid-turn", () => {
    // The interleaved case, and the index is deliberately non-monotonic: the
    // server extends the newest block of the DELTA'S OWN subtask, so the parent's
    // second delta addresses block 0 with a delegate's block 1 already open
    // (internal/buffer's lastBlockOfSubtask). Nothing here may pad a gap or open a
    // third block — the two halves of the parent's sentence belong in one bubble.
    resetStore("ac-10");
    appendChunk("ac-10", "m-1", "The", false, 0, "");
    appendChunk("ac-10", "m-1", "I", false, 1, "wf:wf_1:wf_1/plan");
    appendChunk("ac-10", "m-1", " workflow is running.", false, 0, "");
    const msg = get("ac-10")?.messages[0];
    expect(msg?.blocks).toEqual([
      { type: "text", text: "The workflow is running." },
      { type: "text", text: "I", agent_subtask_id: "wf:wf_1:wf_1/plan" },
    ]);
    // The flat string is the wire's arrival order, not the blocks' — it feeds
    // search and the persisted Content, which are chronological by delta.
    expect(msg?.content).toBe("TheI workflow is running.");
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
// appendChunk's repaint discipline: a mounted signal carries the TEXT, so the
// version bump it schedules declares cause `chunk` — the renderer's tail-
// bookkeeping-only branch; with nothing mounted the list is the only channel,
// so the bump declares `shape` (the full pass is what puts the text on
// screen). Getting this backwards is either a dropped delta or a transcript
// that re-projects per character.
// ---------------------------------------------------------------------------

describe("Store appendChunk repaint discipline", () => {
  it("declares a chunk-cause bump when a streaming signal is carrying the text", async () => {
    resetStore("ar-1");
    appendChunk("ar-1", "m-1", "hello", false, 0, "");
    await tick();
    const sig = ensureStreamingSig("m-1", "hello");
    const before = messagesVersionOf("ar-1").peek();
    appendChunk("ar-1", "m-1", " world", false, 0, "");
    await tick();
    expect(sig.value).toBe("hello world");
    expect(messagesVersionOf("ar-1").peek()).toBe(before + 1);
    expect(renderCauseOf("ar-1")).toEqual({ cause: "chunk" });
    clearStreamingSig("m-1");
  });

  it("repaints the list when nothing is mounted to carry the text", async () => {
    resetStore("ar-2");
    appendChunk("ar-2", "m-1", "hello", false, 0, "");
    await tick();
    const before = messagesVersionOf("ar-2").peek();
    appendChunk("ar-2", "m-1", " world", false, 0, "");
    await tick();
    expect(messagesVersionOf("ar-2").peek()).toBe(before + 1);
    expect(renderCauseOf("ar-2").cause).toBe("shape");
  });

  it("routes the text delta to the per-block signal, bumping as a chunk", async () => {
    resetStore("ar-3");
    appendChunk("ar-3", "m-1", "hello", false, 0, "");
    await tick();
    const blockSig = ensureBlockTextSig("m-1", 0, "hello");
    const before = messagesVersionOf("ar-3").peek();
    appendChunk("ar-3", "m-1", " world", false, 0, "");
    await tick();
    expect(blockSig.value).toEqual({ full: "hello world", delta: " world" });
    expect(messagesVersionOf("ar-3").peek()).toBe(before + 1);
    expect(renderCauseOf("ar-3")).toEqual({ cause: "chunk" });
    clearAllBlockSigs();
  });

  it("routes a delta to the signal of the earlier block it names", async () => {
    // A backwards block index (an interleaved delegate opened a block behind
    // which the parent's stream continues) has to reach the signal mounted over
    // THAT block, or the parent's prose stops growing on screen while it keeps
    // accumulating in the store.
    resetStore("ar-7");
    appendChunk("ar-7", "m-1", "The", false, 0, "");
    appendChunk("ar-7", "m-1", "I", false, 1, "sub-7");
    await tick();
    const parentSig = ensureBlockTextSig("m-1", 0, "The");
    const before = messagesVersionOf("ar-7").peek();
    appendChunk("ar-7", "m-1", " workflow is running.", false, 0, "");
    await tick();
    expect(parentSig.value).toEqual({
      full: "The workflow is running.",
      delta: " workflow is running.",
    });
    expect(messagesVersionOf("ar-7").peek()).toBe(before + 1);
    expect(renderCauseOf("ar-7")).toEqual({ cause: "chunk" });
    clearAllBlockSigs();
  });

  it("routes a reasoning delta to its own per-block signal", async () => {
    resetStore("ar-4");
    appendChunk("ar-4", "m-1", "why", true, 0, "");
    await tick();
    const blockSig = ensureBlockThinkingSig("m-1", 0, "why");
    const before = messagesVersionOf("ar-4").peek();
    appendChunk("ar-4", "m-1", " not", true, 0, "");
    await tick();
    expect(blockSig.value).toEqual({ full: "why not", delta: " not" });
    expect(messagesVersionOf("ar-4").peek()).toBe(before + 1);
    expect(renderCauseOf("ar-4")).toEqual({ cause: "chunk" });
    clearAllBlockSigs();
  });

  it("repaints for a reasoning delta with nothing mounted", async () => {
    resetStore("ar-5");
    appendChunk("ar-5", "m-1", "why", true, 0, "");
    await tick();
    const before = messagesVersionOf("ar-5").peek();
    appendChunk("ar-5", "m-1", " not", true, 0, "");
    await tick();
    expect(messagesVersionOf("ar-5").peek()).toBe(before + 1);
  });

  it("repaints when a refusal is stamped, so the callout can mount", async () => {
    resetStore("ar-6");
    appendChunk("ar-6", "m-1", "I can't", false, 0, "");
    await tick();
    const sig = ensureStreamingSig("m-1", "I can't");
    const before = messagesVersionOf("ar-6").peek();
    appendChunk("ar-6", "m-1", " help with that", false, 0, "", 0, { category: "policy" });
    await tick();
    expect(get("ar-6")?.messages[0]?.refusal).toEqual({ category: "policy" });
    // The delta still rides the signal, as it would without a refusal...
    expect(sig.value).toBe("I can't help with that");
    // ...but the per-block signal carries text only, so a message-level callout
    // needs the keyed reconcile as well.
    expect(messagesVersionOf("ar-6").peek()).toBe(before + 1);
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
    const before = messagesVersionOf("tc-2").peek();
    upsertToolCall("tc-2", "m-1", call("t-1"), 0);
    expect(messagesVersionOf("tc-2").peek()).toBe(before + 1);
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
    const before = messagesVersionOf("tc-9").peek();
    upsertToolCall("tc-9", "m-1", call("t-1"), 0);
    await tick();
    expect(messagesVersionOf("tc-9").peek()).toBe(before + 1);
  });

  it("fans a later update through the tool's own signal, bumping as a tool cause", async () => {
    resetStore("tc-10");
    upsertToolCall("tc-10", "m-1", call("t-1"), 0);
    await tick();
    const sig = ensureToolCallSig("tc-10", "t-1", call("t-1"));
    const before = messagesVersionOf("tc-10").peek();
    upsertToolCall("tc-10", "m-1", call("t-1", "completed"), 0);
    await tick();
    expect(sig.value.status).toBe("completed");
    // The card's own effect paints the update; the bump carries the keyed-
    // update address so the renderer refreshes ONE message, never the list.
    expect(messagesVersionOf("tc-10").peek()).toBe(before + 1);
    expect(renderCauseOf("tc-10")).toEqual({ cause: "tool", msgID: "m-1" });
    clearToolCallSig("tc-10", "t-1");
  });

  it("repaints a later update when no tool signal is mounted", async () => {
    resetStore("tc-11");
    upsertToolCall("tc-11", "m-1", call("t-1"), 0);
    await tick();
    const before = messagesVersionOf("tc-11").peek();
    upsertToolCall("tc-11", "m-1", call("t-1", "completed"), 0);
    await tick();
    expect(messagesVersionOf("tc-11").peek()).toBe(before + 1);
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
    // See `chatWithTurn` above: the repaint is gated on the chat being active.
    setActive(chat);
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
    const before = messagesVersionOf("cr-1").peek();
    setCodeReferences("cr-1", "m-2", [{ license_name: "MIT" }]);
    expect(get("cr-1")?.messages[1]?.code_references).toEqual([{ license_name: "MIT" }]);
    expect(messagesVersionOf("cr-1").peek()).toBe(before + 1);
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
    promoteSteer("st-1", "s-1", "one");
    recordSteerQueued("st-1", { id: "s-2", text: "two, corrected" });
    // The promoted one is out of the dock; the replay corrected its sibling in
    // place rather than appending a third row.
    expect(get("st-1")?.steers).toEqual([{ id: "s-2", text: "two, corrected" }]);
    expect(steerMarks("st-1").map((m) => m.id)).toEqual(["s-1"]);
  });

  it("records an acknowledgement that rides the steer's first frame", () => {
    resetStore("st-2");
    promoteSteer("st-2", "s-ghost", "from another tab", "switched the file to tabs");
    expect(steerMarks("st-2")).toEqual([
      {
        id: "s-ghost",
        text: "from another tab",
        ack: "switched the file to tabs",
        anchor: { msgID: "", blockIndex: 0 },
      },
    ]);
  });

  it("adds no verdict when a first frame's acknowledgement is empty", () => {
    // An empty ack is the absence of one. A note carrying `ack: ""` renders a
    // verdict line with nothing in it.
    resetStore("st-3");
    promoteSteer("st-3", "s-ghost", "from another tab", "");
    expect(steerMarks("st-3")).toEqual([
      { id: "s-ghost", text: "from another tab", anchor: { msgID: "", blockIndex: 0 } },
    ]);
  });

  it("adds no verdict when a read frame's acknowledgement is empty", () => {
    resetStore("st-4");
    recordSteerQueued("st-4", { id: "s-1", text: "use tabs" });
    promoteSteer("st-4", "s-1", "use tabs", "");
    expect(steerMarks("st-4")).toEqual([
      { id: "s-1", text: "use tabs", anchor: { msgID: "", blockIndex: 0 } },
    ]);
  });
});

// ---------------------------------------------------------------------------
// A message the store has not seen before has to reach the LIST, whatever is
// already mounted.
//
// The signal maps are module-global and keyed by message id, so a signal can
// outlive the row that created it — a paginated window drops resident messages,
// and a re-created message keeps its id. When the row itself is new, feeding
// that leftover signal is not enough: nothing has mounted the row, so the only
// channel that can put it on screen is the keyed reconcile behind the chat's
// version signal. This is the one case where the "a mounted signal carries
// the delta, so stay off the list" discipline above does NOT apply.
// ---------------------------------------------------------------------------

describe("Store appendChunk on a first sighting", () => {
  it("repaints the list even when a signal for that id is already mounted", async () => {
    resetStore("fs-1");
    const sig = ensureStreamingSig("m-unseen", "");
    const before = messagesVersionOf("fs-1").peek();

    appendChunk("fs-1", "m-unseen", "hello", false, 0, "");
    await tick();

    expect(get("fs-1")?.messages[0]?.content).toBe("hello");
    expect(messagesVersionOf("fs-1").peek()).toBe(before + 1);
    // The leftover signal is not the channel for a row that has yet to mount.
    expect(sig.value).toBe("");
    clearStreamingSig("m-unseen");
  });

  it("repaints for a first reasoning delta with a mounted reasoning signal", async () => {
    resetStore("fs-2");
    const sig = ensureReasoningSig("m-unseen-r", "");
    const before = messagesVersionOf("fs-2").peek();

    appendChunk("fs-2", "m-unseen-r", "why", true, 0, "");
    await tick();

    expect(get("fs-2")?.messages[0]?.reasoning).toBe("why");
    expect(messagesVersionOf("fs-2").peek()).toBe(before + 1);
    expect(sig.value).toBe("");
    clearReasoningSig("m-unseen-r");
  });
});

// ---------------------------------------------------------------------------
// The coalescer's schedule set, read at the module's first list change.
//
// `scheduleMessages` guards itself with a module-scope Set so several changes
// in one tick cost one repaint. That initial state is computed once when the
// module is evaluated, which a static import freezes at collection time — so
// this test loads its own copy of the store, the way `platform.pwa.test.ts` does
// for the constants it stubs a platform for. Without a fresh module the very
// first repaint of the page is not observable at all.
// ---------------------------------------------------------------------------

describe("the deferred-repaint guard", () => {
  it("is clear on a freshly loaded module, so the first list change repaints", async () => {
    vi.resetModules();
    const store = await import("./store.js");
    store.setSessions([makeSession("boot-1")]);
    store.setActive("boot-1");
    const before = store.messagesVersionOf("boot-1").peek();

    store.appendChunk("boot-1", "m-1", "a", false, 0, "");
    // Deferred by one microtask, so nothing has repainted yet.
    expect(store.messagesVersionOf("boot-1").peek()).toBe(before);

    await tick();
    expect(store.messagesVersionOf("boot-1").peek()).toBe(before + 1);
  });
});

// ---------------------------------------------------------------------------
// Each chat repaints ITSELF; the chat on screen is untouched by the rest.
//
// Versions are per chat: a background chat's stream bumps its OWN signal, which
// nothing on screen subscribes to — the transcript effect and the task-list
// pill track the ACTIVE chat's signal only. That is what closes the multi-tab
// freeze (N streaming chats used to repaint the visible transcript N times per
// frame) without the old active-chat gate's blind spot, where a background
// consumer (the subagent page) had no repaint channel at all.
//
// The tests are in pairs on purpose: the foreground signal stays put, AND the
// background data lands with its own bump. A gate that dropped the delta would
// be a data-loss bug wearing a performance improvement's clothes.
// ---------------------------------------------------------------------------

describe("per-chat repaint isolation", () => {
  /** Two chats in the store, the first active. */
  function twoChats(): void {
    setSessions([makeSession("fg"), makeSession("bg")]);
    setActive("fg");
  }

  it("a background text delta bumps its own chat, not the active one", async () => {
    twoChats();
    const fg = messagesVersionOf("fg").peek();
    const bg = messagesVersionOf("bg").peek();

    appendChunk("bg", "m-bg", "hello", false, 0, "");
    await tick();

    expect(messagesVersionOf("fg").peek()).toBe(fg);
    expect(messagesVersionOf("bg").peek()).toBe(bg + 1);
    // The mutation lands too. Asserted through the blocks array rather than
    // `content`, because the blocks are what the renderer reads.
    expect(get("bg")?.messages[0]?.blocks?.[0]?.text).toBe("hello");
    expect(get("bg")?.messages[0]?.content).toBe("hello");
  });

  it("a background reasoning delta does the same", async () => {
    twoChats();
    const fg = messagesVersionOf("fg").peek();
    const bg = messagesVersionOf("bg").peek();

    appendChunk("bg", "m-bg-r", "weighing", true, 0, "");
    await tick();

    expect(messagesVersionOf("fg").peek()).toBe(fg);
    expect(messagesVersionOf("bg").peek()).toBe(bg + 1);
    expect(get("bg")?.messages[0]?.blocks?.[0]?.thinking).toBe("weighing");
  });

  it("a background tool call does the same", async () => {
    twoChats();
    const fg = messagesVersionOf("fg").peek();
    const bg = messagesVersionOf("bg").peek();

    // A NEW tool call on a message the store has not seen: the path that creates
    // the assistant message, which is the most structural change there is.
    upsertToolCall(
      "bg",
      "m-bg-t",
      { id: "tc-1", title: "ls", kind: "execute", status: "pending", ts: 0 },
      0,
    );
    await tick();

    expect(messagesVersionOf("fg").peek()).toBe(fg);
    expect(messagesVersionOf("bg").peek()).toBe(bg + 1);
    expect(get("bg")?.messages[0]?.tool_calls?.[0]?.id).toBe("tc-1");
  });

  it("a background message arriving bumps its own chat synchronously", () => {
    twoChats();
    const fg = messagesVersionOf("fg").peek();
    const bg = messagesVersionOf("bg").peek();

    // `ingestMessage`, reached through both of its doors. The bump here is
    // SYNCHRONOUS: the renderer's keyed reconcile must see an arrival before
    // the frame it was announced in.
    upsertMessage("bg", { id: "m-bg", role: "assistant", ts: 1, content: "hi" });
    appendMessage("bg", { id: "m-bg", role: "assistant", ts: 1, content: "hi there" });

    expect(messagesVersionOf("fg").peek()).toBe(fg);
    expect(messagesVersionOf("bg").peek()).toBe(bg + 2);
    expect(get("bg")?.messages[0]?.content).toBe("hi there");
  });

  it("a background turn summary bumps its own chat", () => {
    twoChats();
    appendMessage("bg", { id: "m-bg", role: "assistant", ts: 1, content: "done" });
    const fg = messagesVersionOf("fg").peek();
    const bg = messagesVersionOf("bg").peek();

    setTurnSummary("bg", { credits: 4, elapsedMs: 90 });

    expect(messagesVersionOf("fg").peek()).toBe(fg);
    expect(messagesVersionOf("bg").peek()).toBe(bg + 1);
    expect(get("bg")?.messages[0]?.turn_credits).toBe(4);
    expect(get("bg")?.messages[0]?.turn_elapsed_ms).toBe(90);
  });

  it("setActive bumps no version — the switch travels on the active id", async () => {
    twoChats();
    appendChunk("bg", "m-bg", "hello", false, 0, "");
    await tick();
    const fg = messagesVersionOf("fg").peek();
    const bg = messagesVersionOf("bg").peek();

    setActive("bg");
    await tick();

    // `setActive` writes `activeId`, which the transcript effect tracks via
    // `watchActiveId` — so the switch repaints by re-running the effect against
    // the new chat's signal, not by bumping anything.
    expect(messagesVersionOf("fg").peek()).toBe(fg);
    expect(messagesVersionOf("bg").peek()).toBe(bg);
    expect(activeSession.value?.id).toBe("bg");
    expect(activeSession.value?.messages[0]?.content).toBe("hello");
  });

  it("a chat streams normally with no chat active at all", async () => {
    setSessions([makeSession("orphan")]);
    setActive("");
    const before = messagesVersionOf("orphan").peek();

    appendChunk("orphan", "m-1", "hello", false, 0, "");
    await tick();

    // Reachable on the boot path before a tab is activated. The chat's own
    // version moves; there is simply no subscriber for it yet.
    expect(messagesVersionOf("orphan").peek()).toBe(before + 1);
    expect(get("orphan")?.messages[0]?.content).toBe("hello");
  });
});

// ---------------------------------------------------------------------------
// The in-flight turn marker: which message id the chat file cannot carry yet.
//
// `loadMessages` replaces the array with the server's page, so it needs to know
// which local message the page is entitled to omit. Position cannot answer it:
// the agent persists messages DURING a turn (a plan update, a compaction or
// safety event, the cancel badge), each landing after the streaming reply
// locally while sitting inside the page — so a rule of "keep everything after
// the newest id the page carries" dropped the reply and the reader saw their own
// prompt above an empty turn body until a reload.
// ---------------------------------------------------------------------------

import { noteLiveTurnMessage, clearLiveTurnMessage, liveTurnMessage } from "./store.js";

describe("the in-flight turn marker", () => {
  it("is unset for a chat with no turn running", () => {
    resetStore("chat-live0");
    expect(liveTurnMessage("chat-live0")).toBeUndefined();
  });

  it("is set by a chunk that arrives before its own message_created", () => {
    resetStore("chat-live1");
    appendChunk("chat-live1", "m-early", "hello", false, 0, "");
    expect(liveTurnMessage("chat-live1")).toBe("m-early");
  });

  it("is cleared by the persist echo of the same id, which is what message_appended is", () => {
    resetStore("chat-live2");
    noteLiveTurnMessage("chat-live2", "m-turn");
    appendMessage("chat-live2", { id: "m-turn", role: "assistant", ts: 2, content: "done" });
    expect(liveTurnMessage("chat-live2")).toBeUndefined();
  });

  it("survives a persisted message with a DIFFERENT id — the plan update that caused the bug", () => {
    resetStore("chat-live3");
    appendChunk("chat-live3", "m-turn", "streaming", false, 0, "");
    appendMessage("chat-live3", { id: "m-plan", role: "assistant", ts: 3, content: "" });
    expect(liveTurnMessage("chat-live3")).toBe("m-turn");
  });

  it("only tracks the chat it was recorded against", () => {
    resetStore("chat-live4");
    noteLiveTurnMessage("chat-live4", "m-a");
    noteLiveTurnMessage("chat-live5", "m-b");
    clearLiveTurnMessage("chat-live5");
    expect(liveTurnMessage("chat-live4")).toBe("m-a");
    expect(liveTurnMessage("chat-live5")).toBeUndefined();
  });

  it("goes with the chat", () => {
    resetStore("chat-live6");
    noteLiveTurnMessage("chat-live6", "m-turn");
    removeChat("chat-live6");
    expect(liveTurnMessage("chat-live6")).toBeUndefined();
  });
});

describe("transcriptStale (the activation refetch gate)", () => {
  function loadedNow(chatID: string): Session {
    return { ...makeSession(chatID), residency: "loaded", loadedEpoch: syncEpoch() };
  }

  it("a loaded window from the current epoch is fresh", () => {
    expect(transcriptStale(loadedNow("c-fresh"))).toBe(false);
  });

  it("a never-loaded chat is stale by construction", () => {
    // No residency and no stamp: the boot-listed shape. Both halves absent must
    // read stale, or the first activation would skip the fetch it exists to do.
    expect(transcriptStale(makeSession("c-cold"))).toBe(true);
  });

  it("an evicted window is stale whatever its stamp says", () => {
    const s: Session = {
      ...makeSession("c-evicted"),
      residency: "evicted",
      loadedEpoch: syncEpoch(),
    };
    expect(transcriptStale(s)).toBe(true);
  });

  it("a partial window is stale whatever its stamp says", () => {
    // Background ingest into an evicted chat: some rows resident, the window
    // around them not — only a newest-page load may claim otherwise.
    const s: Session = {
      ...makeSession("c-partial"),
      residency: "partial",
      loadedEpoch: syncEpoch(),
    };
    expect(transcriptStale(s)).toBe(true);
  });

  it("a gap flips a fresh window stale", () => {
    const s = loadedNow("c-gapped");
    expect(transcriptStale(s)).toBe(false);
    bumpSyncEpoch();
    expect(transcriptStale(s)).toBe(true);
  });

  it("a loaded window with no stamp is stale", () => {
    // The pre-upgrade shape (residency landed one task before the stamp): a row
    // claiming loaded with no epoch record must refetch, not trust the hole.
    const s: Session = { ...makeSession("c-unstamped"), residency: "loaded" };
    expect(transcriptStale(s)).toBe(true);
  });
});
