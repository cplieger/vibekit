// @vitest-environment happy-dom
// Unit tests for store.ts — property-based idempotency invariants.
import { describe, it, expect } from "vitest";
import * as fc from "fast-check";
import {
  parseContextSize, setSessions, getSessions, get, setActive,
  appendMessage, upsertMessage, upsertHeader, removeChat,
  addPendingChange, setThinking, setWorkingLabel,
  enqueuePrompt, dequeuePrompt, setQueuedPrompt, queuedPrompt,
  setName,
} from "./store.js";
import type { Session } from "./types.js";

// Arbitrary generators for domain types.
const arbMessage = () =>
  fc.record({
    id: fc.uuid(),
    role: fc.constantFrom("user", "assistant") as fc.Arbitrary<"user" | "assistant">,
    ts: fc.nat({ max: 2_000_000_000_000 }),
    content: fc.string({ maxLength: 200 }),
  });

const arbPendingChange = () =>
  fc.record({
    tool_call_id: fc.uuid(),
    chat_id: fc.string({ minLength: 1, maxLength: 30 }),
    path: fc.string({ minLength: 1, maxLength: 80 }),
    kind: fc.constantFrom("create", "edit", "delete") as fc.Arbitrary<"create" | "edit" | "delete">,
    created_at: fc.nat({ max: 2_000_000_000_000 }),
  });

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
    agent: "",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    available_commands: [],
    available_prompts: [],
    auto_approve_crew: false,
    supervised_mode: false,
    pending_changes: [],
    usage: { context_pct: 0, context_size: 0, credits: 0, turn_count: 0, last_turn_ms: 0, has_real_data: false },
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
        for (const m of unique) appendMessage("chat-1", m);
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

  it("upsertMessage: updates existing message by ID", () => {
    fc.assert(
      fc.property(arbMessage(), fc.string({ maxLength: 100 }), (msg, newContent) => {
        resetStore("chat-1");
        upsertMessage("chat-1", msg);
        const updated = { ...msg, content: newContent };
        upsertMessage("chat-1", updated);
        const session = get("chat-1")!;
        const found = session.messages.find((m) => m.id === msg.id);
        expect(found?.content).toBe(newContent);
        expect(session.messages.filter((m) => m.id === msg.id)).toHaveLength(1);
      }),
      { numRuns: 200 },
    );
  });

  it("addPendingChange is idempotent: duplicate tool_call_id is ignored", () => {
    fc.assert(
      fc.property(arbPendingChange(), (change) => {
        resetStore("chat-1");
        addPendingChange("chat-1", change);
        addPendingChange("chat-1", change);
        const session = get("chat-1")!;
        const matches = session.pending_changes.filter(
          (p) => p.tool_call_id === change.tool_call_id,
        );
        expect(matches).toHaveLength(1);
      }),
      { numRuns: 200 },
    );
  });

  it("addPendingChange: different tool_call_ids all land", () => {
    fc.assert(
      fc.property(
        fc.array(arbPendingChange(), { minLength: 1, maxLength: 10 }),
        (changes) => {
          resetStore("chat-1");
          const unique = changes.map((c, i) => ({ ...c, tool_call_id: `tc-${String(i)}` }));
          for (const c of unique) addPendingChange("chat-1", c);
          const session = get("chat-1")!;
          expect(session.pending_changes).toHaveLength(unique.length);
        },
      ),
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

  it("addPendingChange on unknown chat is a no-op", () => {
    fc.assert(
      fc.property(arbPendingChange(), (change) => {
        resetStore("chat-1");
        addPendingChange("nonexistent", change);
        expect(get("chat-1")!.pending_changes).toHaveLength(0);
      }),
      { numRuns: 50 },
    );
  });
});

describe("parseContextSize (table-driven)", () => {
  const cases: Array<{ input: string; expected: number | undefined }> = [
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
      agent: "",
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      available_modes: [],
      available_models: [],
      auto_approve_crew: false,
      supervised_mode: false,
      usage: { context_pct: 0, context_size: 0, credits: 0, turn_count: 0, last_turn_ms: 0, has_real_data: false },
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

describe("Store queue operations (property-based)", () => {
  it("enqueue N then dequeue N yields same items in FIFO order", () => {
    fc.assert(
      fc.property(
        fc.array(fc.string({ minLength: 1, maxLength: 50 }), { minLength: 1, maxLength: 20 }),
        (items) => {
          resetStore("chat-1");
          for (const text of items) enqueuePrompt("chat-1", text);
          const dequeued: string[] = [];
          let next = dequeuePrompt("chat-1");
          while (next !== undefined) {
            dequeued.push(next);
            next = dequeuePrompt("chat-1");
          }
          expect(dequeued).toEqual(items);
        },
      ),
      { numRuns: 200 },
    );
  });

  it("dequeue on empty returns undefined", () => {
    resetStore("chat-1");
    expect(dequeuePrompt("chat-1")).toBeUndefined();
  });

  it("dequeue on unknown chat returns undefined", () => {
    resetStore("chat-1");
    expect(dequeuePrompt("nonexistent")).toBeUndefined();
  });

  it("setQueuedPrompt(undefined) clears the queue", () => {
    fc.assert(
      fc.property(
        fc.array(fc.string({ minLength: 1, maxLength: 50 }), { minLength: 1, maxLength: 10 }),
        (items) => {
          resetStore("chat-1");
          for (const text of items) enqueuePrompt("chat-1", text);
          setQueuedPrompt("chat-1", undefined);
          expect(queuedPrompt("chat-1")).toBeUndefined();
          expect(dequeuePrompt("chat-1")).toBeUndefined();
        },
      ),
      { numRuns: 100 },
    );
  });

  it("setQueuedPrompt(text) replaces entire queue with single item", () => {
    fc.assert(
      fc.property(
        fc.array(fc.string({ minLength: 1, maxLength: 50 }), { minLength: 1, maxLength: 10 }),
        fc.string({ minLength: 1, maxLength: 50 }),
        (items, replacement) => {
          resetStore("chat-1");
          for (const text of items) enqueuePrompt("chat-1", text);
          setQueuedPrompt("chat-1", replacement);
          expect(queuedPrompt("chat-1")).toBe(replacement);
          expect(dequeuePrompt("chat-1")).toBe(replacement);
          expect(dequeuePrompt("chat-1")).toBeUndefined();
        },
      ),
      { numRuns: 100 },
    );
  });

  it("queuedPrompt returns the front of the queue without consuming it", () => {
    fc.assert(
      fc.property(
        fc.array(fc.string({ minLength: 1, maxLength: 50 }), { minLength: 1, maxLength: 10 }),
        (items) => {
          resetStore("chat-1");
          for (const text of items) enqueuePrompt("chat-1", text);
          if (items.length > 0) {
            expect(queuedPrompt("chat-1")).toBe(items[0]);
            expect(queuedPrompt("chat-1")).toBe(items[0]);
          }
        },
      ),
      { numRuns: 100 },
    );
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