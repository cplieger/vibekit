// ---------------------------------------------------------------------------
// Tests for handlers/pending.ts SSE event routing.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

// --- Mocks ---

const mockAddPendingChange = vi.fn();
const mockRemovePendingChange = vi.fn();
const mockClearPendingChanges = vi.fn();
const mockSetTrustedThisTurn = vi.fn();

vi.mock("../store.js", () => ({
  addPendingChange: mockAddPendingChange,
  removePendingChange: mockRemovePendingChange,
  clearPendingChanges: mockClearPendingChanges,
  setTrustedThisTurn: mockSetTrustedThisTurn,
}));

// Capture SSE handlers and bus emissions via shared helper
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
const busEmissions: { event: string; payload: unknown }[] = [];

vi.mock("../bus.js", () => createBusMock({
  emitBus: vi.fn((event: string, payload: unknown) => {
    busEmissions.push({ event, payload });
  }),
  BUS_PENDING_ADDED: "pending:added",
  BUS_PENDING_RESOLVED: "pending:resolved",
  BUS_PENDING_CLEARED: "pending:cleared",
  BUS_PENDING_TRUST_ENABLED: "pending:trust_enabled",
  BUS_PENDING_TRUST_CLEARED: "pending:trust_cleared",
}));

// Import after mocks
await import("./pending.js");

describe("pending_change_added", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    busEmissions.length = 0;
  });

  it("adds change to store and emits bus event", () => {
    const change = { tool_call_id: "tc1", kind: "write_file", path: "/a.ts" };
    fireSSE("pending_change_added", "chat-1", { change });
    expect(mockAddPendingChange).toHaveBeenCalledWith("chat-1", change);
    expect(busEmissions).toHaveLength(1);
    expect(busEmissions[0]).toEqual({
      event: "pending:added",
      payload: { chatID: "chat-1", change },
    });
  });

  it("skips when change is undefined", () => {
    fireSSE("pending_change_added", "chat-1", { change: undefined });
    expect(mockAddPendingChange).not.toHaveBeenCalled();
    expect(busEmissions).toHaveLength(0);
  });
});

describe("pending_change_resolved", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    busEmissions.length = 0;
  });

  it("removes change from store and emits bus event", () => {
    fireSSE("pending_change_resolved", "chat-1", { tool_call_id: "tc1", action: "accept" });
    expect(mockRemovePendingChange).toHaveBeenCalledWith("chat-1", "tc1");
    expect(busEmissions[0]).toEqual({
      event: "pending:resolved",
      payload: { chatID: "chat-1", toolCallID: "tc1", action: "accept" },
    });
  });

  it("skips when tool_call_id is undefined", () => {
    fireSSE("pending_change_resolved", "chat-1", { tool_call_id: undefined, action: "accept" });
    expect(mockRemovePendingChange).not.toHaveBeenCalled();
    expect(busEmissions).toHaveLength(0);
  });
});

describe("pending_changes_cleared", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    busEmissions.length = 0;
  });

  it("clears all pending changes and emits bus event", () => {
    fireSSE("pending_changes_cleared", "chat-1", { reason: "turn_ended" });
    expect(mockClearPendingChanges).toHaveBeenCalledWith("chat-1");
    expect(busEmissions[0]).toEqual({
      event: "pending:cleared",
      payload: { chatID: "chat-1", reason: "turn_ended" },
    });
  });

  it("defaults reason to empty string when missing", () => {
    fireSSE("pending_changes_cleared", "chat-1", {});
    expect(mockClearPendingChanges).toHaveBeenCalledWith("chat-1");
    expect(busEmissions[0]).toEqual({
      event: "pending:cleared",
      payload: { chatID: "chat-1", reason: "" },
    });
  });
});

describe("pending_trust_enabled", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    busEmissions.length = 0;
  });

  it("sets trusted flag and emits bus event", () => {
    fireSSE("pending_trust_enabled", "chat-1", {});
    expect(mockSetTrustedThisTurn).toHaveBeenCalledWith("chat-1", true);
    expect(busEmissions[0]).toEqual({
      event: "pending:trust_enabled",
      payload: { chatID: "chat-1" },
    });
  });
});

describe("pending_trust_cleared", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    busEmissions.length = 0;
  });

  it("clears trusted flag and emits bus event with reason", () => {
    fireSSE("pending_trust_cleared", "chat-1", { reason: "explicit" });
    expect(mockSetTrustedThisTurn).toHaveBeenCalledWith("chat-1", false);
    expect(busEmissions[0]).toEqual({
      event: "pending:trust_cleared",
      payload: { chatID: "chat-1", reason: "explicit" },
    });
  });

  it("defaults reason to turn_ended when payload is undefined", () => {
    fireSSE("pending_trust_cleared", "chat-1", undefined);
    expect(mockSetTrustedThisTurn).toHaveBeenCalledWith("chat-1", false);
    expect(busEmissions[0]).toEqual({
      event: "pending:trust_cleared",
      payload: { chatID: "chat-1", reason: "turn_ended" },
    });
  });
});
