// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for handlers/chat.ts SSE event routing.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

// --- Mocks ---

const mockUpsertHeader = vi.fn();
const mockRemoveChat = vi.fn();

vi.mock("../store.js", () => ({
  upsertHeader: mockUpsertHeader,
  removeChat: mockRemoveChat,
}));

const mockCloseTab = vi.fn();
const mockHasTab = vi.fn(() => false);

vi.mock("../tabs.js", () => ({
  closeTab: mockCloseTab,
  hasTab: mockHasTab,
}));

// Capture SSE handlers
type SSEHandler = (chatID: string, payload: unknown) => void;
const sseHandlers = new Map<string, SSEHandler>();
vi.mock("../bus.js", () => ({
  onSSE: vi.fn((event: string, handler: SSEHandler) => {
    sseHandlers.set(event, handler);
  }),
}));

// Mock dynamic imports used by chat_deleted
vi.mock("../conflicts.js", () => ({ clearConflicts: vi.fn() }));
vi.mock("../banner-stack.js", () => ({ clearBannersForChat: vi.fn() }));

// Import after mocks
await import("./chat.js");

function fireSSE(event: string, chatID: string, payload: unknown): void {
  const handler = sseHandlers.get(event);
  if (handler) {
    handler(chatID, payload);
  }
}

describe("chat_created", () => {
  beforeEach(() => vi.clearAllMocks());

  it("upserts header into store", () => {
    const header = { id: "c1", name: "Test Chat" };
    fireSSE("chat_created", "", header);
    expect(mockUpsertHeader).toHaveBeenCalledWith(header);
  });

  it("skips undefined payload", () => {
    fireSSE("chat_created", "", undefined);
    expect(mockUpsertHeader).not.toHaveBeenCalled();
  });
});

describe("chat_updated", () => {
  beforeEach(() => vi.clearAllMocks());

  it("upserts header into store", () => {
    const header = { id: "c1", name: "Renamed" };
    fireSSE("chat_updated", "", header);
    expect(mockUpsertHeader).toHaveBeenCalledWith(header);
  });

  it("skips undefined payload", () => {
    fireSSE("chat_updated", "", undefined);
    expect(mockUpsertHeader).not.toHaveBeenCalled();
  });
});

describe("chat_deleted", () => {
  beforeEach(() => vi.clearAllMocks());

  it("removes chat from store", () => {
    fireSSE("chat_deleted", "", { id: "c1" });
    expect(mockRemoveChat).toHaveBeenCalledWith("c1");
  });

  it("closes open tab for deleted chat", () => {
    mockHasTab.mockReturnValue(true);
    fireSSE("chat_deleted", "", { id: "c2" });
    expect(mockCloseTab).toHaveBeenCalledWith("c2", { skipOnClose: true });
  });

  it("does not close tab if not open", () => {
    mockHasTab.mockReturnValue(false);
    fireSSE("chat_deleted", "", { id: "c3" });
    expect(mockCloseTab).not.toHaveBeenCalled();
  });

  it("skips when payload has no id", () => {
    fireSSE("chat_deleted", "", {});
    expect(mockRemoveChat).not.toHaveBeenCalled();
  });

  it("skips undefined payload", () => {
    fireSSE("chat_deleted", "", undefined);
    expect(mockRemoveChat).not.toHaveBeenCalled();
  });
});
