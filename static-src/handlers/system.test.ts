// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for handlers/system.ts: the BUS_TRANSPORT_GAP reconcile handler and
// the mode_changed SSE handler.
//
// Drives the REAL store and asserts the resulting session state (thinking
// flags cleared, current mode reflected). The loader (store-load.ts) and
// tabs.ts stay mocked — they are network / DOM-subsystem boundaries — so the
// orphan-tab and reload assertions verify the command at that boundary.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import { setSessions, setActive, get } from "../store.js";
import type { Session } from "../types.js";

vi.mock("../store-load.js", () => ({
  loadList: () => mockLoadList(),
  loadMessages: mockLoadMessages,
}));
const mockLoadList = vi.fn(() => Promise.resolve(true));
const mockLoadMessages = vi.fn(() => Promise.resolve(true));

const mockCloseTab = vi.fn();
const mockHasTab = vi.fn(() => true);
const mockGetOpenTabIDs = vi.fn(() => [] as string[]);
vi.mock("../tabs.js", () => ({
  closeTab: mockCloseTab,
  hasTab: mockHasTab,
  getOpenTabIDs: () => mockGetOpenTabIDs(),
}));

vi.mock("../settings.js", () => ({ syncSettings: vi.fn(() => Promise.resolve({})) }));
vi.mock("../session-context.js", () => ({ restoreLastModel: vi.fn() }));
vi.mock("../status.js", () => ({ refreshCompactionThreshold: vi.fn() }));
vi.mock("../retention.js", () => ({ refreshRetention: vi.fn() }));

const mockMaybeDrainIfIdle = vi.fn();
vi.mock("../prompt-queue.js", () => ({ maybeDrainIfIdle: mockMaybeDrainIfIdle }));

// Capture SSE handlers (shared helper) + bus handlers (onBus) so we can fire
// both transport:gap and mode_changed.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
const busHandlers = new Map<string, (...args: unknown[]) => void>();
vi.mock("../bus.js", () =>
  createBusMock({
    onBus: vi.fn((event: string, handler: (...args: unknown[]) => void) => {
      busHandlers.set(event, handler);
    }),
    BUS_TRANSPORT_GAP: "transport:gap",
  }),
);

// Import after mocks so system.ts registers its handlers against the bus mock.
await import("./system.js");

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

function fireGap(): void {
  busHandlers.get("transport:gap")?.({ lastSeen: 0, floor: 0, head: 0 });
}

beforeEach(() => {
  vi.clearAllMocks();
  mockLoadList.mockReturnValue(Promise.resolve(true));
  mockLoadMessages.mockReturnValue(Promise.resolve(true));
  setSessions([]);
});

describe("BUS_TRANSPORT_GAP handler", () => {
  it("clears the thinking flag on every session", () => {
    setSessions([
      makeSession("a", { thinking: true }),
      makeSession("b", { thinking: false }),
      makeSession("c", { thinking: true }),
    ]);
    fireGap();
    expect(get("a")?.thinking).toBe(false);
    expect(get("b")?.thinking).toBe(false);
    expect(get("c")?.thinking).toBe(false);
  });

  it("reloads the header list", () => {
    setSessions([makeSession("a")]);
    fireGap();
    expect(mockLoadList).toHaveBeenCalled();
  });

  it("closes tabs whose session no longer exists after the reload", async () => {
    setSessions([makeSession("s1")]);
    mockGetOpenTabIDs.mockReturnValue(["s1", "s2", "s3"]);
    mockHasTab.mockReturnValue(true);

    fireGap();
    await mockLoadList(); // flush the loadList().then(...) tab-reconcile microtask

    expect(mockCloseTab).toHaveBeenCalledWith("s2");
    expect(mockCloseTab).toHaveBeenCalledWith("s3");
    expect(mockCloseTab).not.toHaveBeenCalledWith("s1");
  });

  it("refetches messages for the active chat", () => {
    setSessions([makeSession("active-chat")]);
    setActive("active-chat");
    fireGap();
    expect(mockLoadMessages).toHaveBeenCalledWith("active-chat");
  });

  it("re-drains every session so a queued prompt isn't stranded by the outage", () => {
    // The gap clears thinking on all chats; a prompt queued before the outage
    // would otherwise wait for a turn_ended that will never fire. The handler
    // re-checks each chat for a drainable queue.
    setSessions([makeSession("a"), makeSession("b", { thinking: true })]);
    fireGap();
    expect(mockMaybeDrainIfIdle).toHaveBeenCalledWith("a");
    expect(mockMaybeDrainIfIdle).toHaveBeenCalledWith("b");
  });
});

describe("mode_changed handler", () => {
  it("reflects the new mode id on the chat", () => {
    setSessions([makeSession("chat-1", { current_mode_id: "" })]);
    fireSSE("mode_changed", "chat-1", { mode_id: "plan" });
    expect(get("chat-1")?.current_mode_id).toBe("plan");
  });

  it("ignores an empty mode id (current mode unchanged)", () => {
    setSessions([makeSession("chat-1", { current_mode_id: "build" })]);
    fireSSE("mode_changed", "chat-1", { mode_id: "" });
    expect(get("chat-1")?.current_mode_id).toBe("build");
  });

  it("ignores an event with an empty chat id", () => {
    setSessions([makeSession("chat-1", { current_mode_id: "build" })]);
    fireSSE("mode_changed", "", { mode_id: "plan" });
    expect(get("chat-1")?.current_mode_id).toBe("build");
  });
});
