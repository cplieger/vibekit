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
  steerMarks,
  promoteSteer,
  appendMessage,
  noteLiveTurnMessage,
  liveTurnMessage,
  setTurnDone,
  setTurnFailed,
  tabStatusFor,
  transcriptStale,
} from "../store.js";
import { noteLoaded, syncEpoch } from "../tab-freshness.js";
import { workspaceRoot, _resetForTest as resetWorkspace, setWorkspaceRoot } from "../workspace.js";
import {
  liveRunsForChat,
  registerLiveRunObserver,
  runChatID,
  hasExecutingRunForChat,
} from "../run-store.js";
import type { Session } from "../types.js";
// The two modules the factories below spread rather than replace. Type-only, so neither
// adds a runtime edge the `vi.mock` would have to reach around.
import type * as RunStore from "../run-store.js";
import type * as ApiClient from "../api-client.js";

vi.mock("../store-load.js", () => ({
  loadList: () => mockLoadList(),
  loadMessages: mockLoadMessages,
  scheduleListRetry: () => mockScheduleListRetry(),
}));
const mockLoadList = vi.fn(() => Promise.resolve(true));
const mockLoadMessages = vi.fn(() => Promise.resolve(true));
// The gap door's answer to a failed list load. A spy, because the LADDER is
// store-load.test.ts's subject (it owns the reach gate, the delays and the bound);
// what this file owns is that the door consults it and only on a failure.
const mockScheduleListRetry = vi.fn();

const mockCloseTab = vi.fn();
const mockHasTab = vi.fn(() => true);
// The dispatcher door. The gate itself is tabs.test.ts's subject; what this file
// owns is that the gap and the resume reach it, once, and after the epoch bump.
const mockRefreshActiveView = vi.fn();
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
  toggleGitView: undefined,
  toggleSettingsView: undefined,
  closeTab: mockCloseTab,
  hasTab: mockHasTab,
  refreshActiveView: mockRefreshActiveView,
  // Reached through turn-teardown.ts, which the gap handler now shares with the
  // turn_ended door. No-ops rather than present-but-undefined: the gap handler
  // CALLS both once per session, so undefined would throw rather than link.
  // tabIdFor answers "" (the no-tab answer), the mock rule's empty value.
  tabIdFor: vi.fn(() => ""),
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

// The mode/model catalog re-read. Mocked because the real one is a network fetch
// that seeds four controls, and because a call count is the whole assertion: the
// catalog is announced on no frame, so a gap is the only signal this client gets
// that a `config_option_update` or a server restart happened during the outage.
const mockFetchCatalog = vi.fn(() => Promise.resolve());
vi.mock("../session-catalog.js", () => ({ fetchCatalog: mockFetchCatalog }));

// The live-runs inventory rebuild: the gap handler re-reads the server's
// presence projection because the events feeding the inventory were lost.
//
// A PARTIAL mock, because the two doors want opposite things. The gap door's two run
// readers stay spies: what this file owns there is the token they share, and both are
// network reads. `adoptConnectRuns` is the REAL function, because what the connect door
// owns IS the state it seeds — a spy for it could only assert that the handler called
// something, which is the shape that let this door ship with no test at all.
//
// Both spies go through `vi.hoisted` for the same reason the api-client pair below does:
// this file now imports run-store STATICALLY, so the mocker resolves this factory during
// linking rather than lazily at the first `import("./system.js")`.
const { mockInvalidateCachedRuns, mockRebuildLiveRuns } = vi.hoisted(() => ({
  mockInvalidateCachedRuns: vi.fn(),
  mockRebuildLiveRuns: vi.fn(),
}));
vi.mock("../run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  rebuildLiveRuns: mockRebuildLiveRuns,
  invalidateCachedRuns: mockInvalidateCachedRuns,
}));

// The two fetchers run-store reaches, replaced so a per-run read is OBSERVABLE: the
// connect adoption's promise is that it issues none, and the withheld-inventory fallback's
// is that it issues exactly one. Spread the real surface, so every other consumer keeps
// the module it had.
//
// Through `vi.hoisted` because `../run-store.js` is statically imported below and imports
// api-client, so the mocker resolves this factory during linking — above this file's own
// top-level initializers, where a plain `const` is still in its temporal dead zone.
const { mockApiGet, mockApiGetTyped } = vi.hoisted(() => ({
  mockApiGet: vi.fn(),
  mockApiGetTyped: vi.fn(),
}));
vi.mock("../api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: mockApiGet,
  apiGetTyped: mockApiGetTyped,
}));

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
}));
const mockDrainModelSwitchQueue = vi.fn();
vi.mock("../model-switcher.js", () => ({
  drainModelSwitchQueue: mockDrainModelSwitchQueue,
  initModelSwitcher: undefined,
  queueModelSwitch: undefined,
  switchModel: undefined,
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
    BUS_PAGE_RESUMED: "page:resumed",
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

function fireResume(): void {
  busHandlers.get("page:resumed")?.(undefined);
}

beforeEach(() => {
  vi.clearAllMocks();
  mockLoadList.mockReturnValue(Promise.resolve(true));
  mockLoadMessages.mockReturnValue(Promise.resolve(true));
  // `mockReset` is on, so an implementation set at construction is gone by now. A null
  // answer is what a 404 gives, and what the real decoders' callers already handle.
  mockApiGet.mockResolvedValue(null);
  mockApiGetTyped.mockResolvedValue(null);
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

// ---------------------------------------------------------------------------
// The handshake's two NEGATIVE statements, neither of which any other frame carries.
//
// `busy_chats` is the only thing that ever says a chat this client believes is working is
// not: a turn that died with the previous process gets no turn_state, so before this the
// stale `thinking` stood until the reader prompted that chat again. `live_runs` is the
// inventory the client used to fetch three serialized round trips behind whoami.
//
// Both are guarded by their own STATED flag rather than by the list being non-empty,
// because a scoped, capped or withheld list is indistinguishable from a complete one —
// and read as complete, the first clears a live turn and the second drops live runs.
// ---------------------------------------------------------------------------

/** A handshake stating both halves. The two flags default TRUE here, which is the
 *  opposite of the wire default, so each case names the withholding it is about. */
function fireConnected(over: Record<string, unknown> = {}): void {
  fireSSE("connected", "", {
    floor: 1,
    head: 9,
    busy_stated: true,
    live_runs_stated: true,
    live_runs: [],
    ...over,
  });
}

describe("the connect handshake retracts a thinking the server does not confirm", () => {
  it("retracts a thinking chat the busy set does not name", () => {
    setSessions([makeSession("a", { thinking: true }), makeSession("b", { thinking: true })]);
    fireConnected({ busy_chats: ["b"] });
    expect(get("a")?.thinking, "unconfirmed").toBe(false);
    expect(get("b")?.thinking, "the server says this one is busy").toBe(true);
  });

  it("retracts nothing when the set is not STATED, and still adopts the runs", () => {
    // A topic-filtered or over-cap connect states nothing about the chats it omits, so the
    // flag bounds the blast radius rather than making a clear over a live turn merely
    // unlikely. The runs are OUTSIDE that gate on purpose: the inventory is
    // workspace-global, carries its own flag, and an early return must not swallow it.
    setSessions([makeSession("a", { thinking: true })]);
    fireConnected({
      busy_stated: false,
      busy_chats: [],
      live_runs: [{ workflow_id: "wf-1", chat_id: "a", executing: true }],
    });
    expect(get("a")?.thinking, "no statement, no retraction").toBe(true);
    expect(hasExecutingRunForChat("a"), "the inventory landed anyway").toBe(true);
  });

  it("keeps the retracted chat's live-turn marker", () => {
    // The NARROW retraction, and this is what makes it narrow: `busy_chats` deliberately
    // omits a chat whose only open turn is a workflow STEP, so a chat reached here may
    // still be streaming — and the live-turn marker is the only thing stopping the next
    // window load from deleting that reply. A full teardown here would drop it.
    setSessions([makeSession("a", { thinking: true })]);
    noteLiveTurnMessage("a", "m-live");
    fireConnected({ busy_chats: [] });
    expect(get("a")?.thinking).toBe(false);
    expect(liveTurnMessage("a"), "still the only copy of the reply").toBe("m-live");
  });
});

describe("the connect handshake adopts the live-run inventory off the frame", () => {
  it("seeds the inventory, the chat pairing and the observer", () => {
    // Three seeds per row, and a reload loses each one differently: without the inventory
    // a chat with a live run is evicted mid-run, without the pairing that run's tab opens
    // at the end of the strip instead of under its conversation, and without the observer
    // the row is seeded and nothing repaints — so the tab keeps the factory's placeholder
    // label for as long as the run takes.
    const seen: string[] = [];
    registerLiveRunObserver((id) => seen.push(id));
    setSessions([makeSession("a")]);

    fireConnected({ live_runs: [{ workflow_id: "wf-1", chat_id: "a", executing: true }] });

    expect(liveRunsForChat("a")).toEqual([{ id: "wf-1", chat: "a", executing: true }]);
    expect(runChatID("wf-1")).toBe("a");
    expect(seen).toEqual(["wf-1"]);
  });

  it("issues no per-run read of its own", () => {
    // The one thing the connect adoption deliberately does NOT do, and the reason it can
    // be free: C2's floor paints a live run's square from THIS frame, so the per-run
    // `inspect` is a refinement rather than a precondition. Passing a cause here would put
    // one GET per live run on every reconnect.
    setSessions([makeSession("a")]);
    fireConnected({ live_runs: [{ workflow_id: "wf-1", chat_id: "a", executing: true }] });
    expect(mockApiGet).not.toHaveBeenCalled();
    expect(mockApiGetTyped).not.toHaveBeenCalled();
  });

  it("falls back to the endpoint when the inventory was WITHHELD", () => {
    // `live_runs_stated: false` means the lease store held more rows than the frame will
    // carry, so the list is withheld rather than truncated — and the adoption CLEARS before
    // it repopulates, so reading a withheld list as empty would drop every live run.
    setSessions([makeSession("a")]);
    fireConnected({ live_runs_stated: false });
    expect(mockApiGetTyped).toHaveBeenCalledWith("/api/runs/live", expect.any(Function));
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

  it("arms the bounded retry when that reload failed", async () => {
    // The gap has already dropped every claim this client held, so a failed reload
    // leaves the sidebar on rows it was licensed to drop — and on a stream that stayed
    // up there is no later `connected` to re-read it.
    setSessions([makeSession("a")]);
    mockLoadList.mockReturnValue(Promise.resolve(false));
    fireGap();
    await mockLoadList();
    expect(mockScheduleListRetry).toHaveBeenCalledTimes(1);
  });

  it("arms nothing when the reload landed", async () => {
    setSessions([makeSession("a")]);
    fireGap();
    await mockLoadList();
    expect(mockScheduleListRetry).not.toHaveBeenCalled();
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

  // A run's node state is APPLIED from `run_progress` rather than refetched, so
  // frames lost in the outage leave a stale tree with nothing to notice it — a
  // node that completed during the gap keeps reading `running` and its clock keeps
  // ticking. A gap is the one moment the client knows it missed frames.
  it("re-reads every cached run, because the progress frames it missed were applied ones", () => {
    setSessions([makeSession("a")]);
    fireGap();
    expect(mockInvalidateCachedRuns).toHaveBeenCalledTimes(1);
  });

  // ONE token for both run readers, which is what takes a gap's run cost from two
  // requests per live run to one. They act on the same event a network round trip
  // apart — `rebuildLiveRuns` invalidates each live run only once
  // `/api/runs/live` has answered — so the token is the only thing that can say
  // they are the same event. What the token MEANS is run-store.test.ts's subject.
  it("threads ONE cause through both of its run readers", () => {
    setSessions([makeSession("a")]);
    fireGap();

    const cause = mockInvalidateCachedRuns.mock.calls[0]?.[0];
    expect(typeof cause).toBe("string");
    expect(cause).not.toBe("");
    expect(mockRebuildLiveRuns).toHaveBeenCalledWith(cause);
  });

  it("mints a DIFFERENT cause per gap, or the second gap would refetch nothing", () => {
    setSessions([makeSession("a")]);
    fireGap();
    fireGap();

    const first = mockInvalidateCachedRuns.mock.calls[0]?.[0];
    const second = mockInvalidateCachedRuns.mock.calls[1]?.[0];
    expect(second).not.toBe(first);
  });

  // The catalog left ChatHeader for one workspace-global holder, and nothing
  // broadcasts a change to it: `fetchCatalog` used to run at boot and after login
  // only, so a model list that moved during the outage — or a server restart, which
  // empties the in-memory holder until a bridge respawns — left the picker on
  // whatever boot answered, until a reload.
  it("re-reads the mode/model catalog, which no frame announces", () => {
    setSessions([makeSession("a")]);
    fireGap();
    expect(mockFetchCatalog).toHaveBeenCalledTimes(1);
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

  // ONE refresh, whatever kind of tab is on screen: the handler asks the projection
  // for the active VIEW instead of the store for the active CHAT, so the git, docs,
  // files, run and editor kinds are healed by the same line the chat kind is.
  it("refreshes the active view exactly once", () => {
    setSessions([makeSession("active-chat")]);
    setActive("active-chat");
    fireGap();
    expect(mockRefreshActiveView).toHaveBeenCalledTimes(1);
  });

  // THE RULED BEHAVIOUR CHANGE (design rev 5). `getActiveId()` is the active CHAT and
  // the projection's active row is the active TAB, and the two diverge because no
  // production `setActive` runs on a non-chat activation — so with a git tab on screen
  // the store still names the last-viewed chat. That chat is a BACKGROUND view now and
  // heals at its next activation, like every other background view.
  it("refetches no chat window of its own, even one the store still calls active", () => {
    setSessions([makeSession("bg-1"), makeSession("last-viewed"), makeSession("bg-2")]);
    setActive("last-viewed");
    fireGap();
    expect(mockLoadMessages).not.toHaveBeenCalled();
    expect(mockRefreshTurnRail).not.toHaveBeenCalledWith("last-viewed");
    expect(mockRefreshActiveView).toHaveBeenCalledTimes(1);
  });

  // Nothing else in this handler reaches the dispatcher, so a resume's whole effect on it
  // is this one call. It bumps NO epoch: a resume dropped no frames, so an answer that
  // spanned the suspension still describes the server's state — what it drops is the
  // freshness RECORDS, in the case below.
  it("refreshes the active view on a page resume, and bumps no epoch", () => {
    setSessions([makeSession("a")]);
    const before = syncEpoch();
    fireResume();
    expect(mockRefreshActiveView).toHaveBeenCalledTimes(1);
    expect(syncEpoch()).toBe(before);
  });

  // The in-app switch gap: the resume nudge reaches the ACTIVE view only, so a background
  // chat marked `loaded` at the current epoch read fresh for the life of the document and
  // switching back to it cost zero fetches — showing the window as it was before the page
  // was suspended.
  it("makes a background chat refetch at its next activation after a resume", () => {
    const fresh = makeSession("bg", { residency: "loaded" });
    setSessions([fresh]);
    noteLoaded("chat", "bg", syncEpoch());
    setActive("");
    expect(transcriptStale(get("bg")!)).toBe(false);

    fireResume();

    expect(transcriptStale(get("bg")!)).toBe(true);
  });

  it("drops the records before the refresh goes out", () => {
    // Order is the contract, for the gap handler's reason read the other way round: the
    // active view's own gate is `viewStale`, so a refresh dispatched while its record
    // still stood would be gated out and the resume would nudge nothing at all.
    const fresh = makeSession("active-chat", { residency: "loaded" });
    setSessions([fresh]);
    noteLoaded("chat", "active-chat", syncEpoch());
    setActive("active-chat");
    let staleAtRefresh = false;
    mockRefreshActiveView.mockImplementationOnce(() => {
      staleAtRefresh = transcriptStale(get("active-chat")!);
    });

    fireResume();

    expect(staleAtRefresh).toBe(true);
  });

  it("marks every loaded window stale by bumping the sync epoch", () => {
    // The lazy half of the reconcile: nothing refetches a background chat here,
    // so the bump is what guarantees its next activation does.
    const fresh = makeSession("bg", { residency: "loaded" });
    setSessions([fresh]);
    noteLoaded("chat", "bg", syncEpoch());
    setActive("");
    expect(transcriptStale(get("bg")!)).toBe(false);

    fireGap();
    expect(transcriptStale(get("bg")!)).toBe(true);
  });

  it("bumps the epoch before the refresh goes out", () => {
    // Order is the contract: a heal that started before the bump would stamp
    // the OLD epoch and read stale forever; one started after stamps the new
    // one and counts as fresh. The loader's own capture discipline is
    // store-load.test.ts's subject — this pins the door's sequencing.
    setSessions([makeSession("active-chat")]);
    setActive("active-chat");
    const before = syncEpoch();
    let epochAtRefresh = -1;
    mockRefreshActiveView.mockImplementationOnce(() => {
      epochAtRefresh = syncEpoch();
    });

    fireGap();
    expect(epochAtRefresh).toBe(before + 1);
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

  it("drops a RUN-keyed ask too, which the per-session sweep cannot reach", async () => {
    // The sweep above walks `getSessions()`, and `run:<workflowId>` is no chat, so
    // a parentless run's ask — and any ask the server reconstructed for a run
    // nothing hosts — escaped it. The replay re-offers what is still open and does
    // NOT replay the settle, so an ask answered during the outage kept its card:
    // the click then answered 409 and the dock spliced a closed question.
    const { pushDecision, runPendingAsks, _resetForTest } = await import("../decision-dock.js");
    _resetForTest();
    setSessions([makeSession("a")]);
    pushDecision({
      kind: "run_input",
      chatID: "run:wf_1",
      runID: "wf_1",
      askID: "reconciled:root/review",
      payload: {
        workflow_id: "wf_1",
        ask_id: "reconciled:root/review",
        node_id: "review",
        step_session_id: "sess_step",
        agent_name: "reviewer",
        question: "",
        asked_at: "2026-09-03T10:00:00Z",
      },
      submit: vi.fn(),
    });
    expect(runPendingAsks("wf_1").count).toBe(1);

    fireGap();
    expect(runPendingAsks("wf_1").count).toBe(0);
  });

  it("clears every session's steers, because they are claims it can no longer support", () => {
    // Steers are KAS's state and the gap means the frames that resolved or
    // dropped them may be among the lost ones. A chip saying "the agent hasn't
    // read this" is an assertion about the server; after an outage this client
    // cannot make it, so it stops making it. Same reasoning as clearing
    // `thinking` on every chat above.
    setSessions([makeSession("a"), makeSession("b", { thinking: true })]);
    recordSteerQueued("a", { id: "steer-a", text: "one", origin: "user" });
    recordSteerQueued("b", { id: "steer-b", text: "two", origin: "user" });

    fireGap();
    expect(steerCount("a")).toBe(0);
    expect(steerCount("b")).toBe(0);
  });

  // The OTHER half of the same door, characterized because it is what makes the
  // clear above admissible: a mark is a fact already established (the agent read
  // this, or a boundary dropped it unread), and its lifetime is the loaded
  // transcript rather than the turn. Clearing the dock without touching the
  // record is the whole difference between "may still be queued" and "gone".
  it("leaves the transcript marks alone, because those are facts and not claims", () => {
    setSessions([makeSession("a")]);
    setActive("a");
    appendMessage("a", { id: "u-1", role: "user", ts: 1, content: "go" });
    appendMessage("a", {
      id: "a-1",
      role: "assistant",
      ts: 2,
      content: "",
      blocks: [{ type: "text", text: "hello" }],
    });
    recordSteerQueued("a", { id: "steer-read", text: "read one", origin: "user" });
    promoteSteer("a", "steer-read", "read one", "user");
    recordSteerQueued("a", { id: "steer-waiting", text: "unresolved", origin: "user" });

    fireGap();

    expect(steerCount("a"), "the dock is forgotten").toBe(0);
    expect(
      steerMarks("a").map((m) => m.id),
      "the record survives",
    ).toEqual(["steer-read"]);
    expect(steerMarks("a")[0]?.dropped, "and claims nothing about delivery").toBeUndefined();
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
// short by three effects — both in-flight markers and the rail — so a reconnect
// left a chunk watermark that dropped the next turn's early deltas, and a
// live-message marker that made a later refetch keep a message the chat file
// already held.
// ---------------------------------------------------------------------------

describe("the gap reconcile runs the shared turn teardown", () => {
  beforeEach(() => {
    mockRefreshTurnRail.mockClear();
    mockDrainModelSwitchQueue.mockClear();
  });

  it("clears the in-flight marker for every chat, the rail for none", () => {
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("");
    noteLiveTurnMessage("chat-1", "m-live");

    fireGap();

    expect(liveTurnMessage("chat-1")).toBeUndefined();
    expect(mockDrainModelSwitchQueue).toHaveBeenCalledWith("chat-1");
    // And every chat, not only the active one: a gap describes the connection.
    expect(mockDrainModelSwitchQueue).toHaveBeenCalledWith("chat-2");
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
