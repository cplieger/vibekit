// ---------------------------------------------------------------------------
// Tests for handlers/pending.ts SSE event routing.
//
// Drives the REAL store and asserts the resulting pending_changes /
// trusted_this_turn state (the observable outcome), plus the pending:* bus
// emissions, which are this handler's documented cross-module contract (the
// tool-card / pill UIs listen for them). Only the bus is stubbed — to capture
// emissions — and its constants mirror the real bus.ts values verbatim.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import { setSessions, get } from "../store.js";
import type { Session, PendingChange } from "../types.js";

// Capture SSE handlers and bus emissions via shared helper. The constant
// values below mirror bus.ts exactly (BUS_PENDING_TRUST_* use hyphens).
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
const busEmissions: { event: string; payload: unknown }[] = [];

vi.mock("../bus.js", () =>
  createBusMock({
    emitBus: vi.fn((event: string, payload: unknown) => {
      busEmissions.push({ event, payload });
    }),
    BUS_PENDING_ADDED: "pending:added",
    BUS_PENDING_RESOLVED: "pending:resolved",
    BUS_PENDING_CLEARED: "pending:cleared",
    BUS_PENDING_TRUST_ENABLED: "pending:trust-enabled",
    BUS_PENDING_TRUST_CLEARED: "pending:trust-cleared",
  }),
);

// Import after mocks so pending.ts registers its handlers against the bus mock.
await import("./pending.js");

function makeChange(over: Partial<PendingChange> = {}): PendingChange {
  return {
    tool_call_id: "tc1",
    chat_id: "chat-1",
    path: "/a.ts",
    kind: "edit",
    created_at: 0,
    ...over,
  };
}

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
    pending_changes: [],
    ...over,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  busEmissions.length = 0;
  setSessions([makeSession("chat-1")]);
});

describe("pending_change_added", () => {
  it("adds the change to the store and emits pending:added", () => {
    const change = makeChange({ tool_call_id: "tc1", path: "/a.ts" });
    fireSSE("pending_change_added", "chat-1", { change });
    expect(get("chat-1")?.pending_changes).toEqual([change]);
    expect(busEmissions).toEqual([
      { event: "pending:added", payload: { chatID: "chat-1", change } },
    ]);
  });

  it("skips when the change is undefined (no store mutation, no emission)", () => {
    fireSSE("pending_change_added", "chat-1", { change: undefined });
    expect(get("chat-1")?.pending_changes).toEqual([]);
    expect(busEmissions).toHaveLength(0);
  });
});

describe("pending_change_resolved", () => {
  it("removes the change from the store and emits pending:resolved", () => {
    setSessions([
      makeSession("chat-1", { pending_changes: [makeChange({ tool_call_id: "tc1" })] }),
    ]);
    fireSSE("pending_change_resolved", "chat-1", { tool_call_id: "tc1", action: "accept" });
    expect(get("chat-1")?.pending_changes).toEqual([]);
    expect(busEmissions).toEqual([
      {
        event: "pending:resolved",
        payload: { chatID: "chat-1", toolCallID: "tc1", action: "accept" },
      },
    ]);
  });

  it("skips when tool_call_id is undefined (change retained, no emission)", () => {
    const change = makeChange({ tool_call_id: "tc1" });
    setSessions([makeSession("chat-1", { pending_changes: [change] })]);
    fireSSE("pending_change_resolved", "chat-1", { tool_call_id: undefined, action: "accept" });
    expect(get("chat-1")?.pending_changes).toEqual([change]);
    expect(busEmissions).toHaveLength(0);
  });
});

describe("pending_changes_cleared", () => {
  it("clears every pending change and emits pending:cleared with the reason", () => {
    setSessions([
      makeSession("chat-1", {
        pending_changes: [makeChange({ tool_call_id: "tc1" }), makeChange({ tool_call_id: "tc2" })],
      }),
    ]);
    fireSSE("pending_changes_cleared", "chat-1", { reason: "turn_ended" });
    expect(get("chat-1")?.pending_changes).toEqual([]);
    expect(busEmissions).toEqual([
      { event: "pending:cleared", payload: { chatID: "chat-1", reason: "turn_ended" } },
    ]);
  });

  it("defaults the emitted reason to an empty string when missing", () => {
    setSessions([
      makeSession("chat-1", { pending_changes: [makeChange({ tool_call_id: "tc1" })] }),
    ]);
    fireSSE("pending_changes_cleared", "chat-1", {});
    expect(get("chat-1")?.pending_changes).toEqual([]);
    expect(busEmissions).toEqual([
      { event: "pending:cleared", payload: { chatID: "chat-1", reason: "" } },
    ]);
  });
});

describe("pending_trust_enabled", () => {
  it("sets trusted_this_turn and emits pending:trust-enabled", () => {
    fireSSE("pending_trust_enabled", "chat-1", {});
    expect(get("chat-1")?.trusted_this_turn).toBe(true);
    expect(busEmissions).toEqual([
      { event: "pending:trust-enabled", payload: { chatID: "chat-1" } },
    ]);
  });
});

describe("pending_trust_cleared", () => {
  it("clears trusted_this_turn and emits pending:trust-cleared with the reason", () => {
    setSessions([makeSession("chat-1", { trusted_this_turn: true })]);
    fireSSE("pending_trust_cleared", "chat-1", { reason: "explicit" });
    expect(get("chat-1")?.trusted_this_turn).toBe(false);
    expect(busEmissions).toEqual([
      { event: "pending:trust-cleared", payload: { chatID: "chat-1", reason: "explicit" } },
    ]);
  });

  it("defaults the reason to turn_ended when the payload is undefined", () => {
    setSessions([makeSession("chat-1", { trusted_this_turn: true })]);
    fireSSE("pending_trust_cleared", "chat-1", undefined);
    expect(get("chat-1")?.trusted_this_turn).toBe(false);
    expect(busEmissions).toEqual([
      { event: "pending:trust-cleared", payload: { chatID: "chat-1", reason: "turn_ended" } },
    ]);
  });
});
