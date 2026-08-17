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
import { setSessions, setActive, get, recordSteerQueued, steerCount } from "../store.js";
import { workspaceRoot, _resetForTest as resetWorkspace, setWorkspaceRoot } from "../workspace.js";
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
  isEditorTabID: (id: string) => id.startsWith("editor:"),
}));

vi.mock("../settings.js", () => ({ syncSettings: vi.fn(() => Promise.resolve({})) }));
vi.mock("../session-context.js", () => ({ restoreLastModel: vi.fn() }));
vi.mock("../status.js", () => ({ refreshCompactionThreshold: vi.fn() }));
vi.mock("../retention.js", () => ({ refreshRetention: vi.fn() }));

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
  resetWorkspace();
});

// The handshake is the only channel that states where the workspace is, and every
// relative agent path needs it to become openable (workspace.ts). It is recorded
// HERE rather than in transport.ts, whose own handshake hook returns early on the
// first connection of a page load — the connection that matters.
describe("connected handshake", () => {
  it("records the workspace root", () => {
    expect.assertions(1);
    fireSSE("connected", "", { workspace: "/workspace", floor: 1, head: 9 });
    expect(workspaceRoot()).toBe("/workspace");
  });

  it("ignores a handshake that carries no workspace", () => {
    // An older server, or a frame that lost the field: leaving the root unknown
    // makes the file request fail as it did before rather than being rewritten.
    expect.assertions(1);
    fireSSE("connected", "", { floor: 1, head: 9 });
    expect(workspaceRoot()).toBe("");
  });

  it("ignores an empty workspace rather than recording it as the root", () => {
    expect.assertions(1);
    setWorkspaceRoot("/workspace");
    fireSSE("connected", "", { workspace: "", floor: 1, head: 9 });
    expect(workspaceRoot()).toBe("/workspace");
  });

  it("ignores a non-string workspace", () => {
    expect.assertions(1);
    fireSSE("connected", "", { workspace: 42, floor: 1, head: 9 });
    expect(workspaceRoot()).toBe("");
  });
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

  it("never closes an editor or singleton tab — neither is a chat", async () => {
    // The reconcile compares tab ids against SESSION ids, so any tab whose id is
    // not a chat id has to be excluded by kind. Editor tabs go through
    // isEditorTabID rather than a hand-written prefix test.
    expect.assertions(3);
    setSessions([makeSession("s1")]);
    mockGetOpenTabIDs.mockReturnValue(["s1", "editor:/workspace/a.ts", "__files__"]);
    mockHasTab.mockReturnValue(true);

    fireGap();
    await mockLoadList();

    expect(mockCloseTab).not.toHaveBeenCalledWith("editor:/workspace/a.ts");
    expect(mockCloseTab).not.toHaveBeenCalledWith("__files__");
    expect(mockCloseTab).not.toHaveBeenCalledWith("s1");
  });

  it("refetches messages for the active chat", () => {
    setSessions([makeSession("active-chat")]);
    setActive("active-chat");
    fireGap();
    expect(mockLoadMessages).toHaveBeenCalledWith("active-chat");
  });

  it("clears the finished-turn latch, for the same reason it clears thinking", async () => {
    // The latch normally stands until the next turn, but "the next turn" may have
    // happened inside the outage, so a green dot after a gap is a claim this client
    // can no longer support. The busy chats that ARE still running get an
    // authoritative turn_state in the connect replay; a finished one gets nothing,
    // which is the accepted cost of not guessing.
    const { setTurnDone, setTurnFailed, tabStatusFor } = await import("../store.js");
    setSessions([makeSession("a"), makeSession("b")]);
    setTurnDone("a");
    setTurnFailed("b");

    fireGap();
    expect(tabStatusFor(get("a"))).toBe("idle");
    expect(tabStatusFor(get("b"))).toBe("idle");
  });

  it("drops every unanswered ask, because the connect replay re-pushes the live ones", async () => {
    // `streamInitialState` lists the whole pending set — all three ask kinds — on
    // EVERY connect, and it writes those frames after the `connected` frame this
    // handler runs off. So clearing is safe and self-healing, where keeping an ask
    // whose answering frame was among the lost events left the chat reporting
    // `input` forever.
    const { pushDecision, hasPendingDecision, _resetForTest } = await import("../decision-dock.js");
    _resetForTest();
    setSessions([makeSession("a")]);
    pushDecision({
      kind: "permission",
      chatID: "a",
      runID: "",
      requestID: 1,
      payload: { request_id: 1, title: "run a command", options: [] } as never,
      submit: vi.fn(),
    });
    expect(hasPendingDecision("a")).toBe(true);

    fireGap();
    expect(hasPendingDecision("a")).toBe(false);
  });

  it("clears every session's steers, because they are claims it can no longer support", () => {
    // Steers are KAS's state and the gap means the frames that resolved or
    // dropped them may be among the lost ones. A chip saying "the agent hasn't
    // read this" is an assertion about the server; after an outage this client
    // cannot make it, so it stops making it. Same reasoning as clearing
    // `thinking` on every chat above.
    setSessions([makeSession("a"), makeSession("b", { thinking: true })]);
    recordSteerQueued("a", { id: "steer-a", text: "one" });
    recordSteerQueued("b", { id: "steer-b", text: "two" });

    fireGap();
    expect(steerCount("a")).toBe(0);
    expect(steerCount("b")).toBe(0);
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
