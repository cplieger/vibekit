// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for the BUS_TRANSPORT_GAP handler in system.ts.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

const mockSetThinking = vi.fn();
const mockGetActiveId = vi.fn(() => "chat-1");
const mockGetSessions = vi.fn(() => [] as Array<{ id: string; thinking: boolean }>);
const mockLoadList = vi.fn(() => Promise.resolve(true));
const mockLoadMessages = vi.fn(() => Promise.resolve(true));

vi.mock("../store.js", () => ({
  getSessions: () => mockGetSessions(),
  getActiveId: () => mockGetActiveId(),
  get: vi.fn(),
  setThinking: mockSetThinking,
  loadList: () => mockLoadList(),
  loadMessages: mockLoadMessages,
  setCurrentMode: vi.fn(),
  clearMsgIndex: vi.fn(),
  invalidateSession: vi.fn(),
  version: { value: 0, peek: () => 0 },
}));

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
vi.mock("../auto-approve.js", () => ({ clearCrewCache: vi.fn() }));

// Capture bus handlers
const busHandlers = new Map<string, Function>();
vi.mock("../bus.js", () => ({
  onSSE: vi.fn(),
  onBus: vi.fn((event: string, handler: Function) => { busHandlers.set(event, handler); }),
  BUS_TRANSPORT_GAP: "transport:gap",
}));

// Import after mocks
await import("./system.js");

function fireGap(): void {
  const handler = busHandlers.get("transport:gap");
  if (handler) handler({});
}

describe("BUS_TRANSPORT_GAP handler", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockLoadList.mockReturnValue(Promise.resolve(true));
  });

  it("clears thinking flags on all sessions", () => {
    mockGetSessions.mockReturnValue([
      { id: "a", thinking: true },
      { id: "b", thinking: false },
      { id: "c", thinking: true },
    ]);
    fireGap();
    expect(mockSetThinking).toHaveBeenCalledWith("a", false);
    expect(mockSetThinking).not.toHaveBeenCalledWith("b", false);
    expect(mockSetThinking).toHaveBeenCalledWith("c", false);
  });

  it("calls loadList()", () => {
    mockGetSessions.mockReturnValue([]);
    fireGap();
    expect(mockLoadList).toHaveBeenCalled();
  });

  it("closes orphaned tabs not in the new session list", async () => {
    mockGetSessions.mockReturnValue([{ id: "s1", thinking: false }]);
    mockGetOpenTabIDs.mockReturnValue(["s1", "s2", "s3"]);
    mockHasTab.mockReturnValue(true);
    mockLoadList.mockReturnValue(Promise.resolve(true));

    fireGap();
    await mockLoadList();

    expect(mockCloseTab).toHaveBeenCalledWith("s2");
    expect(mockCloseTab).toHaveBeenCalledWith("s3");
    expect(mockCloseTab).not.toHaveBeenCalledWith("s1");
  });

  it("reloads active session messages", () => {
    mockGetSessions.mockReturnValue([]);
    mockGetActiveId.mockReturnValue("active-chat");
    fireGap();
    expect(mockLoadMessages).toHaveBeenCalledWith("active-chat");
  });
});
