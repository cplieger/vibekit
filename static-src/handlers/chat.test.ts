// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for handlers/chat.ts SSE event routing.
//
// These drive the REAL store (store.ts is an in-process collaborator we own,
// not an external dependency to mock) and assert the resulting store state —
// the observable behavior a domain expert would recognize ("the chat appears /
// disappears"), not which store function was called. tabs.ts is kept as a
// mock because closing a tab is a command to a separate DOM subsystem whose
// own state is out of this handler's contract.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import { setSessions, getSessions, get } from "../store.js";
import type { Session, ChatHeader } from "../types.js";

// tabs.ts: a separate subsystem; assert the close command, not store state.
const mockCloseTab = vi.fn();
const mockHasTab = vi.fn(() => false);
vi.mock("../tabs.js", () => ({
  closeTab: mockCloseTab,
  hasTab: mockHasTab,
}));

// Capture SSE handlers via shared helper.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
vi.mock("../bus.js", () => createBusMock());

// chat_deleted fires fire-and-forget dynamic imports; stub them so they
// resolve to no-ops instead of pulling in real DOM-touching modules.
vi.mock("../conflicts.js", () => ({ clearConflicts: vi.fn() }));
vi.mock("../banner-stack.js", () => ({ clearBannersForChat: vi.fn() }));

// Import after mocks so chat.ts registers its handlers against the bus mock.
await import("./chat.js");

function makeHeader(over: Partial<ChatHeader> = {}): ChatHeader {
  return {
    id: "c1",
    name: "Test Chat",
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
  setSessions([]);
});

describe("chat_created", () => {
  it("inserts the chat header into the store", () => {
    fireSSE("chat_created", "", makeHeader({ id: "c1", name: "Test Chat" }));
    expect(get("c1")?.name).toBe("Test Chat");
  });

  it("ignores an undefined payload (no chat created)", () => {
    fireSSE("chat_created", "", undefined);
    expect(getSessions()).toHaveLength(0);
  });
});

describe("chat_updated", () => {
  it("re-syncs the header fields of an existing chat", () => {
    setSessions([makeSession("c1", { name: "Old Name" })]);
    fireSSE("chat_updated", "", makeHeader({ id: "c1", name: "Renamed" }));
    expect(get("c1")?.name).toBe("Renamed");
  });

  it("ignores an undefined payload (existing chat unchanged)", () => {
    setSessions([makeSession("c1", { name: "Old Name" })]);
    fireSSE("chat_updated", "", undefined);
    expect(get("c1")?.name).toBe("Old Name");
  });
});

describe("chat_deleted", () => {
  it("removes the chat from the store", () => {
    setSessions([makeSession("c1")]);
    fireSSE("chat_deleted", "", { id: "c1" });
    expect(get("c1")).toBeUndefined();
  });

  it("closes the open tab for the deleted chat", () => {
    setSessions([makeSession("c2")]);
    mockHasTab.mockReturnValue(true);
    fireSSE("chat_deleted", "", { id: "c2" });
    expect(mockCloseTab).toHaveBeenCalledWith("c2", { skipOnClose: true });
    expect(get("c2")).toBeUndefined();
  });

  it("does not close a tab that is not open", () => {
    setSessions([makeSession("c3")]);
    mockHasTab.mockReturnValue(false);
    fireSSE("chat_deleted", "", { id: "c3" });
    expect(mockCloseTab).not.toHaveBeenCalled();
    expect(get("c3")).toBeUndefined();
  });

  it("ignores a payload with no id (chat left intact)", () => {
    setSessions([makeSession("c1")]);
    fireSSE("chat_deleted", "", {});
    expect(get("c1")).toBeDefined();
    expect(mockCloseTab).not.toHaveBeenCalled();
  });

  it("ignores an undefined payload (chat left intact)", () => {
    setSessions([makeSession("c1")]);
    fireSSE("chat_deleted", "", undefined);
    expect(get("c1")).toBeDefined();
  });
});
