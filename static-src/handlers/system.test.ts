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
import {
  setSessions,
  setActive,
  get,
  recordSteerQueued,
  steerCount,
  noteLiveTurnMessage,
  liveTurnMessage,
  setTurnDone,
  setTurnFailed,
  tabStatusFor,
  syncEpoch,
  transcriptStale,
} from "../store.js";
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
vi.mock("../tabs.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  activateTab: undefined,
  getActiveTabId: undefined,
  getActiveTabRoute: undefined,
  openEditorView: undefined,
  setGitTab: undefined,
  setSettingsTab: undefined,
  setTabDirty: undefined,
  tabIdFor: undefined,
  toggleGitView: undefined,
  toggleSettingsView: undefined,
  closeTab: mockCloseTab,
  hasTab: mockHasTab,
  // Reached through turn-teardown.ts, which the gap handler now shares with the
  // turn_ended door. A no-op rather than present-but-undefined: the gap handler
  // CALLS it once per session, so undefined would throw rather than link.
  setTabStatus: vi.fn(),
}));

vi.mock("../settings.js", () => ({
  syncSettings: vi.fn(() => Promise.resolve({})),
  // The settings_updated handler adopts the payload's theme, which is what
  // makes a theme chosen on another device land here live.
  adoptThemeFromSettings: vi.fn(),
}));
vi.mock("../session-context.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  setLastModel: undefined,
  restoreLastModel: vi.fn(),
  restoreLastEffort: vi.fn(),
  // Present-but-undefined for the same linking reason as setLastModel above: the
  // effort picker in this graph imports both, and neither is on a path under test.
  getLastEffort: undefined,
  setLastEffort: undefined,
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // it, so no path under test changes behavior.
  setCurrentModel: undefined,
}));
vi.mock("../status.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // it, so no path under test changes behavior.
  updateContextBar: undefined,
}));
vi.mock("../retention.js", () => ({ refreshRetention: vi.fn() }));

// The live-runs inventory rebuild: the gap handler re-reads the server's
// presence projection because the events feeding the inventory were lost.
const mockRebuildLiveRuns = vi.fn(() => Promise.resolve());
vi.mock("../run-store.js", () => ({ rebuildLiveRuns: mockRebuildLiveRuns }));

// The shared turn teardown (turn-teardown.ts) reaches these three, and each is a
// boundary this test has no business driving: the rail FETCHES the session-wide
// turn index, and turn-rail.ts also pulls in scroll.ts, whose module-level
// initialisation demands a real #messages scroller.
const mockRefreshTurnRail = vi.fn(() => Promise.resolve());
vi.mock("../turn-rail.js", () => ({
  refreshTurnRail: mockRefreshTurnRail,
  pointTurnRail: vi.fn(),
  mountTurnRail: undefined,
  resetTurnRail: undefined,
  observeTurns: undefined,
  ROW_PITCH_PX: 28,
}));
const mockDrainModelSwitchQueue = vi.fn();
vi.mock("../model-switcher.js", () => ({
  drainModelSwitchQueue: mockDrainModelSwitchQueue,
  initModelSwitcher: undefined,
  queueModelSwitch: undefined,
  switchModel: undefined,
}));
const mockOnTurnEnded = vi.fn();
vi.mock("../banner-stack.js", () => ({
  onTurnEnded: mockOnTurnEnded,
  showBanner: vi.fn(),
  dismissBanner: undefined,
  clearBanners: undefined,
  mountBannerStack: undefined,
}));

// Capture SSE handlers (shared helper) + bus handlers (onBus) so we can fire
// both transport:gap and mode_changed.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
const busHandlers = new Map<string, (...args: unknown[]) => void>();
vi.mock("../bus.js", () =>
  createBusMock({
    // Present-but-undefined so real-ESM linking succeeds: another module in this
    // graph imports the name, and Browser Mode links for real rather than reading
    // properties off a namespace object. `undefined` is what the node runner gave
    // these, so no path under test changes behavior.
    emitBus: undefined,
    lookupSSEDecoder: undefined,
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

  it("rebuilds the live-runs inventory from the endpoint", () => {
    // The inventory is event-fed, and the gap means events were lost in both
    // directions: a run that started or settled inside the outage leaves the
    // eviction exemption blind. Re-reading the presence projection is the heal;
    // the degrade rule (a failed rebuild keeps event-fed state) is
    // run-store.test.ts's subject.
    setSessions([makeSession("a")]);
    fireGap();
    expect(mockRebuildLiveRuns).toHaveBeenCalledTimes(1);
  });

  // THE TAB RECONCILE IS GONE, and its absence is what this pins.
  //
  // This handler used to close any chat tab whose session had left
  // `GET /api/chats` — membership by SET DIFFERENCE, over two collections fetched
  // separately, which is the shape that closed tabs nobody closed on the live
  // instance. Restoring it in any form would restore that: the two answers race,
  // and a chat absent from one of them is not evidence its tab was closed.
  //
  // A gap is answered by re-reading the TAB collection instead (app.ts wires
  // `transport:gap` to `listTabs`), and a chat the server deleted has already had
  // its tabs closed by the membership coordinator, under the same lock that removed
  // the record.
  it("closes NO tab, whatever the chat list came back holding", async () => {
    expect.assertions(2);
    setSessions([makeSession("s1")]);
    mockHasTab.mockReturnValue(true);

    fireGap();
    // Flush the loadList continuation, which is where the reconcile used to run.
    await mockLoadList();

    expect(mockLoadList).toHaveBeenCalled();
    expect(mockCloseTab).not.toHaveBeenCalled();
  });

  it("does not ask the tab store what is open either", async () => {
    // The other half: with no set to difference against, the handler has no reason
    // to enumerate the strip at all. `getOpenTabIDs` went with the reconcile, so
    // there is nothing left here to reach it with.
    expect.assertions(2);
    setSessions([makeSession("s1")]);
    fireGap();
    await mockLoadList();
    expect(mockHasTab).not.toHaveBeenCalled();
    expect(mockCloseTab).not.toHaveBeenCalled();
  });

  it("refetches messages for the active chat", () => {
    setSessions([makeSession("active-chat")]);
    setActive("active-chat");
    fireGap();
    expect(mockLoadMessages).toHaveBeenCalledWith("active-chat");
  });

  it("refetches the rail for the active chat, and for no other", () => {
    // The rail's half of the same heal. Background chats are deliberately NOT
    // fetched: their records are stale by epoch now, so each heals on its own
    // next activation instead of fanning N GETs out on every reconnect.
    setSessions([makeSession("bg-1"), makeSession("active-chat"), makeSession("bg-2")]);
    setActive("active-chat");
    fireGap();
    expect(mockRefreshTurnRail).toHaveBeenCalledWith("active-chat");
    expect(mockRefreshTurnRail).not.toHaveBeenCalledWith("bg-1");
    expect(mockRefreshTurnRail).not.toHaveBeenCalledWith("bg-2");
    expect(mockLoadMessages).not.toHaveBeenCalledWith("bg-1");
    expect(mockLoadMessages).not.toHaveBeenCalledWith("bg-2");
  });

  it("marks every loaded window stale by bumping the sync epoch", () => {
    // The lazy half of the reconcile: nothing refetches a background chat here,
    // so the bump is what guarantees its next activation does.
    const fresh = makeSession("bg", { residency: "loaded", loadedEpoch: syncEpoch() });
    setSessions([fresh]);
    setActive("");
    expect(transcriptStale(get("bg")!)).toBe(false);

    fireGap();
    expect(transcriptStale(get("bg")!)).toBe(true);
  });

  it("bumps the epoch before the active chat's heals go out", () => {
    // Order is the contract: a heal that started before the bump would stamp
    // the OLD epoch and read stale forever; one started after stamps the new
    // one and counts as fresh. The loader's own capture discipline is
    // store-load.test.ts's subject — this pins the door's sequencing.
    setSessions([makeSession("active-chat")]);
    setActive("active-chat");
    const before = syncEpoch();
    let epochAtMessagesFetch = -1;
    let epochAtRailFetch = -1;
    mockLoadMessages.mockImplementationOnce(() => {
      epochAtMessagesFetch = syncEpoch();
      return Promise.resolve(true);
    });
    mockRefreshTurnRail.mockImplementationOnce(() => {
      epochAtRailFetch = syncEpoch();
      return Promise.resolve();
    });

    fireGap();
    expect(epochAtMessagesFetch).toBe(before + 1);
    expect(epochAtRailFetch).toBe(before + 1);
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

// ---------------------------------------------------------------------------
// P10: the gap door and the turn_ended door share ONE outcome-independent core.
//
// The gap reconciler was a second independent spelling of that teardown and was
// short by four effects — the transient banners, both in-flight markers and the
// rail — so a reconnect left a rate-limit banner over a finished turn, a chunk
// watermark that dropped the next turn's early deltas, and a live-message marker
// that made a later refetch keep a message the chat file already held.
// ---------------------------------------------------------------------------

describe("the gap reconcile runs the shared turn teardown", () => {
  beforeEach(() => {
    mockRefreshTurnRail.mockClear();
    mockOnTurnEnded.mockClear();
    mockDrainModelSwitchQueue.mockClear();
  });

  it("clears the in-flight marker and the banners for every chat, the rail for none", () => {
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("");
    noteLiveTurnMessage("chat-1", "m-live");

    fireGap();

    expect(liveTurnMessage("chat-1")).toBeUndefined();
    expect(mockOnTurnEnded).toHaveBeenCalledWith("chat-1");
    expect(mockDrainModelSwitchQueue).toHaveBeenCalledWith("chat-1");
    // And every chat, not only the active one: a gap describes the connection.
    expect(mockOnTurnEnded).toHaveBeenCalledWith("chat-2");
    // The rail left the shared teardown: a gap makes every chat's index equally
    // unsupportable, which the epoch bump records, so no per-chat GET goes out
    // here (with no active chat, none at all) — each rail heals on activation.
    expect(mockRefreshTurnRail).not.toHaveBeenCalled();
  });

  it("latches NEITHER outcome, because a gap is not an outcome", () => {
    // The documented asymmetry, and the reason the core is outcome-INDEPENDENT: a
    // gap says the replay ring no longer covers what this client missed, so it can
    // assert nothing about how anything finished. It UNLATCHES instead.
    setSessions([makeSession("chat-1"), makeSession("chat-2")]);
    setTurnDone("chat-1");
    setTurnFailed("chat-2");

    fireGap();

    expect(tabStatusFor(get("chat-1"))).toBe("idle");
    expect(tabStatusFor(get("chat-2"))).toBe("idle");
  });
});
