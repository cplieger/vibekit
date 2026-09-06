// ---------------------------------------------------------------------------
// The tab strip's per-open-row store subscriptions + the context-bar effect
// (chat.ts installStoreSubscribers).
//
// One effect per OPEN chat tab, registry synced on the tab projection's emit;
// the context bar on its own active-session effect. These tests drive the REAL
// store (an in-process collaborator we own) and assert through the tab-write
// seam (renameTab / setTabStatus / setTabTooltip) — a command boundary to a
// separate DOM subsystem. The registry seam (openChatRefs) and the decision
// dock are signal-backed fakes, so the real reactive graph decides what
// re-runs: what these tests pin is the SUBSCRIPTION TOPOLOGY, not the writers'
// rendering.
// ---------------------------------------------------------------------------
import { describe, it, expect, vi, beforeEach } from "vitest";

const h = vi.hoisted(() => ({
  renameTab: vi.fn(),
  setTabStatus: vi.fn(),
  setTabTooltip: vi.fn(),
  tabIdFor: vi.fn((_kind: string, ref = "") => (ref === "" ? "" : `tb_${ref}`)),
  refreshContextUI: vi.fn(),
  // Placeholders, replaced with REAL signals by the async mock factories below
  // (they can await the reactive import; this hoisted block runs before it).
  openRefs: { value: [] as string[] },
  dockVersion: { value: 0 },
  pendingAsks: new Set<string>(),
}));

vi.mock("./tabs.js", async () => {
  const { signal } = await import("@cplieger/reactive");
  const { tabsMock } = await import("./__test-helpers__/tabs-mock.js");
  h.openRefs = signal<string[]>([]);
  return {
    ...tabsMock(),
    // The registry seam: a TRACKED read of which chat refs hold an open tab.
    // Signal-backed so a test's write stands in for the projection's emit and
    // re-runs the sync effect for real.
    openChatRefs: () => h.openRefs.value,
    tabIdFor: h.tabIdFor,
    renameTab: h.renameTab,
    setTabStatus: h.setTabStatus,
    setTabTooltip: h.setTabTooltip,
  };
});

// The dock, shaped like the real module: one version signal, per-chat queues,
// so a row effect's read subscribes it to decisions arriving anywhere.
vi.mock("./decision-dock.js", async () => {
  const { signal } = await import("@cplieger/reactive");
  h.dockVersion = signal(0);
  return {
    hasPendingDecision: (chatID: string): boolean => {
      void h.dockVersion.value;
      return h.pendingAsks.has(chatID);
    },
    dropDecisions: vi.fn(),
  };
});

vi.mock("./context-ui.js", () => ({ refreshContextUI: h.refreshContextUI }));

// chat.ts's remaining first-hop dependencies, inert. The store is deliberately
// NOT on this list: the subject is what its signals re-run.
// `confirmChatExists` is present-but-inert so real-ESM linking succeeds: `chat.ts`
// imports it for the deep-link arm, and Browser Mode links for real rather than
// reading properties off a namespace object. No case here reaches it.
vi.mock("./store-load.js", () => ({
  loadList: vi.fn(),
  loadMessages: vi.fn(),
  confirmChatExists: vi.fn(),
}));
vi.mock("./banner-stack.js", () => ({ ensureBound: vi.fn() }));
vi.mock("./submit.js", () => ({ submitPrompt: vi.fn() }));
vi.mock("./skeleton.js", () => ({ chatSkeleton: vi.fn(() => document.createElement("div")) }));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  skeletonTiming: vi.fn(() => ({ commit: vi.fn(), cancel: vi.fn() })),
}));
vi.mock("./messages.js", () => ({
  mountChatView: vi.fn(),
  setLoadMore: vi.fn(),
  loadTurnRail: vi.fn(),
  pointTurnRail: vi.fn(),
  fadeInTranscript: vi.fn(),
  activeTranscriptView: vi.fn(() => null),
  transcriptViewFor: vi.fn(() => null),
  disposeChatView: vi.fn(),
}));
vi.mock("./attachments.js", () => ({ addAttachment: vi.fn() }));
vi.mock("./composer-state.js", () => ({
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  retargetComposer: vi.fn(),
  seedComposerState: vi.fn(),
  flushComposerDraft: vi.fn(),
  dropComposerState: vi.fn(),
}));
vi.mock("./session-context.js", () => ({ setCurrentModel: vi.fn(), getLastModel: () => "auto" }));
vi.mock("./roles.js", () => ({
  iconForMode: vi.fn(() => ""),
  labelForMode: vi.fn((id: string) => (id === "plan" ? "Plan" : id)),
}));
vi.mock("./dom.js", () => ({
  $: { messages: document.createElement("div"), promptInput: { focus: () => undefined } },
}));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./bus.js", () => ({ onBus: vi.fn(), BUS_ACTIVATE_CHAT: "activate-chat" }));
vi.mock("./transport.js", () => ({ newOpID: () => "op-test" }));
vi.mock("./actions/chat.js", () => ({
  setMode: { dispatch: vi.fn() },
  forkChat: { dispatch: vi.fn() },
  createChat: { dispatch: vi.fn() },
}));

import { installStoreSubscribers } from "./chat.js";
import { setSessions, setActive, setThinking, setName, setTurnDone } from "./store.js";
import type { Session } from "./types.js";

function session(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: `Chat ${id}`,
    model: "auto",
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

/** Every strip writer's calls, one list, so "nothing was written" is one
 *  assertion instead of three that can drift apart. */
function stripWrites(): unknown[][] {
  return [...h.renameTab.mock.calls, ...h.setTabStatus.mock.calls, ...h.setTabTooltip.mock.calls];
}

function clearStripSpies(): void {
  h.renameTab.mockClear();
  h.setTabStatus.mockClear();
  h.setTabTooltip.mockClear();
  h.refreshContextUI.mockClear();
}

beforeEach(() => {
  // The store is module state shared across this file's tests; the previous
  // test's install is torn down by the next installStoreSubscribers call.
  h.pendingAsks.clear();
  h.openRefs.value = [];
  setSessions([]);
  setActive("");
  expect(stripWrites()).toEqual([]);
});

describe("per-row effects write only their own row", () => {
  it("repaints exactly one row when one background chat's status flips", () => {
    setSessions([session("a"), session("b")]);
    setActive("b");
    h.openRefs.value = ["a", "b"];
    installStoreSubscribers();
    clearStripSpies();

    setThinking("a", true);

    expect(h.setTabStatus.mock.calls).toEqual([["tb_a", "working"]]);
    expect(h.renameTab.mock.calls).toEqual([["tb_a", "Chat a"]]);
    expect(h.setTabTooltip.mock.calls).toEqual([["tb_a", ""]]);
  });

  it("triggers nothing for a closed-history session (no open tab)", () => {
    setSessions([session("a"), session("closed")]);
    setActive("a");
    h.openRefs.value = ["a"];
    installStoreSubscribers();
    clearStripSpies();

    setThinking("closed", true);
    setName("closed", "renamed while closed");

    expect(stripWrites()).toEqual([]);
    expect(h.refreshContextUI).not.toHaveBeenCalled();
  });

  it("repaints a background row when its decision arrives, and only that row", () => {
    setSessions([session("a"), session("b")]);
    setActive("b");
    h.openRefs.value = ["a", "b"];
    installStoreSubscribers();
    clearStripSpies();

    h.pendingAsks.add("a");
    h.dockVersion.value = 1;

    // The dock's version signal is global, so BOTH row effects re-run — but the
    // pending ask lands on a's dot only, and b repaints its unchanged state.
    expect(h.setTabStatus).toHaveBeenCalledWith("tb_a", "input");
    expect(h.setTabStatus).not.toHaveBeenCalledWith("tb_b", "input");
  });

  // The other direction, and the one a mapping test structurally cannot see: the
  // ask LEAVING has to re-run the row and write the recovered state. A workflow
  // run's ask is filed under the LAUNCHING chat's key, so this row is the one that
  // was reported stuck on `input` after the run's own sub-tab had recovered.
  it("writes the recovered state when a background chat's ask is cleared", () => {
    setSessions([session("a"), session("b")]);
    setActive("b");
    h.openRefs.value = ["a", "b"];
    h.pendingAsks.add("a");
    installStoreSubscribers();
    // The launching turn ended when the run was created, so `done` is what a is
    // waiting to go back to.
    setTurnDone("a");
    clearStripSpies();

    h.pendingAsks.delete("a");
    h.dockVersion.value = h.dockVersion.value + 1;

    expect(h.setTabStatus.mock.calls).toEqual([
      ["tb_a", "done"],
      ["tb_b", "idle"],
    ]);
  });
});

describe("row-effect lifecycle follows the tab projection", () => {
  it("paints a newly opened tab's row from the current store state", () => {
    setSessions([session("a", { thinking: true })]);
    h.openRefs.value = [];
    installStoreSubscribers();
    clearStripSpies();

    h.openRefs.value = ["a"];

    expect(h.renameTab.mock.calls).toEqual([["tb_a", "Chat a"]]);
    expect(h.setTabStatus.mock.calls).toEqual([["tb_a", "working"]]);
    expect(h.setTabTooltip.mock.calls).toEqual([["tb_a", ""]]);
  });

  it("disposes a closed tab's effect: no writes on later store churn", () => {
    setSessions([session("a")]);
    h.openRefs.value = ["a"];
    installStoreSubscribers();
    clearStripSpies();

    h.openRefs.value = [];
    setThinking("a", true);
    setName("a", "still churning");

    expect(stripWrites()).toEqual([]);
  });

  it("paints a row that opened before its store record arrived", () => {
    // A tab can land ahead of the session list (a remote open before loadList
    // resolves). The row effect tracks the set's structure, so the record
    // arriving is what paints the row.
    h.openRefs.value = ["a"];
    installStoreSubscribers();
    clearStripSpies();

    setSessions([session("a")]);

    expect(h.setTabStatus.mock.calls).toEqual([["tb_a", "idle"]]);
  });

  it("a second install replaces the first: one flip still writes once", () => {
    setSessions([session("a")]);
    h.openRefs.value = ["a"];
    installStoreSubscribers();
    installStoreSubscribers();
    clearStripSpies();

    setThinking("a", true);

    expect(h.setTabStatus.mock.calls).toEqual([["tb_a", "working"]]);
  });
});

describe("the context bar tracks the active session only", () => {
  it("refreshes on activation and on the active chat's own change", () => {
    setSessions([session("a"), session("b")]);
    installStoreSubscribers();
    h.refreshContextUI.mockClear();

    setActive("a");
    expect(h.refreshContextUI).toHaveBeenCalledTimes(1);
    expect((h.refreshContextUI.mock.calls[0]?.[0] as Session).id).toBe("a");

    h.refreshContextUI.mockClear();
    setName("a", "renamed");
    expect(h.refreshContextUI).toHaveBeenCalledTimes(1);
    expect((h.refreshContextUI.mock.calls[0]?.[0] as Session).name).toBe("renamed");
  });

  it("ignores background session churn", () => {
    setSessions([session("a"), session("b")]);
    setActive("a");
    installStoreSubscribers();
    h.refreshContextUI.mockClear();

    setThinking("b", true);
    setName("b", "background rename");

    expect(h.refreshContextUI).not.toHaveBeenCalled();
  });

  it("refreshes with the new session on a switch", () => {
    setSessions([session("a"), session("b")]);
    setActive("a");
    installStoreSubscribers();
    h.refreshContextUI.mockClear();

    setActive("b");

    expect(h.refreshContextUI).toHaveBeenCalledTimes(1);
    expect((h.refreshContextUI.mock.calls[0]?.[0] as Session).id).toBe("b");
  });
});
