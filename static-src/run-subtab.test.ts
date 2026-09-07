// ---------------------------------------------------------------------------
// Opening a run's tab.
//
// Every door into a run view is `openRunView`, and it nests the tab under the
// launching chat with `owns: false` — the close contract, where the sub-tab's ×
// stops watching and the chat's × stops the run. The launching chat comes from the
// caller when it holds one and from the run store when it does not, which is what
// makes a `/run/{id}` deep link nest correctly on a browser holding no state.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";

interface Opened {
  id: string;
  name: string;
  /** `parent` rather than `parentId`, and it names a TAB: `TabSubject.Parent` is a
   *  tab id, so a door resolves the launching CHAT to its open tab first. */
  opts: { parent?: string; owns?: boolean; activate?: boolean } | undefined;
}

const m = {
  opened: [] as Opened[],
  tabs: new Set<string>(),
  launchedBy: new Map<string, string>(),
};

vi.mock("./tabs.js", () => ({
  // Present-but-inert: the run tab renders the run CARD now, whose markdown
  // bubble reaches the linkifier and through it the editor openers, so these
  // names are imported somewhere in this graph. No case here opens a file, closes
  // a tab, or reads which tab is on screen.
  openEditorView: vi.fn(),
  setTabDirty: vi.fn(),
  toggleGitView: vi.fn(),
  closeTab: vi.fn(),
  getActiveTabId: vi.fn(() => ""),
  // `(kind, ref)` in, opaque id out — and "" for "no tab". The fake answers with
  // the readable id the assertions name.
  tabIdFor: vi.fn((kind: string, ref: string) =>
    m.tabs.has(kind === "run" ? `run:${ref}` : ref) ? (kind === "run" ? `run:${ref}` : ref) : "",
  ),
  openRunTab: vi.fn(
    (
      workflowID: string,
      name: string,
      opts?: { parent?: string; owns?: boolean; activate?: boolean },
    ) => {
      m.opened.push({ id: workflowID, name, opts });
      m.tabs.add(`run:${workflowID}`);
      return Promise.resolve();
    },
  ),
  // Item 6's two additions to this graph, both inert here: the run tab's own
  // persisted parent (the post-reload answer for a chat-parented run's launching
  // chat) and the door the detail pane's "Open the conversation" link dispatches
  // through. No case here paints a run — `runState` answers undefined — so neither
  // is reached; they exist because a Browser-Mode mock is linked as real ESM.
  parentChatRef: vi.fn(() => ""),
  hasTab: vi.fn(() => false),
  openTab: vi.fn(() => Promise.resolve("opened")),
}));

vi.mock("./decision-dock.js", () => ({
  // The card's second input beside `inspect`: which step is blocked on a person,
  // which no node status can say. None is here.
  runPendingAsks: vi.fn(() => ({ count: 0, nodes: new Set<string>(), label: "" })),
  mountRunDecisionDock: vi.fn(),
  rerenderDocks: vi.fn(),
  hasPendingDecision: vi.fn(() => false),
}));

vi.mock("./run-store.js", () => ({
  invalidateRun: vi.fn(),
  // The affordance fetch, which showRun triggers alongside the state one. Inert
  // here: this suite pins the sub-tab's open/close rules, and `runControls`
  // answering undefined renders no control row at all.
  invalidateRunControls: vi.fn(),
  runControls: vi.fn(() => undefined),
  runState: vi.fn(() => undefined),
  runChatID: vi.fn((id: string) => m.launchedBy.get(id) ?? ""),
  // The write-back half of the pairing: `openRunView` teaches the store the chat it
  // already knows, so a History-opened finished run has a launching chat for the rest
  // of the session. Recorded into the same fake map `runChatID` reads, which is what
  // makes the two consistent here the way they are in production.
  noteRunChat: vi.fn((id: string, chatID: string) => {
    if (id !== "" && chatID !== "") {
      m.launchedBy.set(id, chatID);
    }
  }),
  // The rest are the run CARD's, which the tab renders now instead of hand-rolling
  // a node tree. Every derived question about a run is a FUNCTION over the cached
  // state rather than a field stored beside it (see run-store.ts), so a card that
  // renders nothing still links against all of them. This suite paints no state —
  // `runState` answers undefined — so each one is inert.
  elapsedMs: vi.fn(() => 0),
  // The node PLAN, which the exec view is the first reader of: a repeat's bound and
  // stop condition come from there rather than from the state tree.
  runPlan: vi.fn(() => undefined),
  leafNodes: vi.fn(() => []),
  nodePathOf: vi.fn(() => []),
  // The exec-view adapter's own path key: KAS names a repeat's iteration container
  // one way in the state tree and another in a step frame's path, so the tree is
  // translated. Inert here for the same reason as the rest — no state is painted.
  nodePathSegment: vi.fn((node: { nodeId: string }) => node.nodeId),
  runCounters: vi.fn(() => ({ total: 0, done: 0, failed: 0, current: 0 })),
  runElapsedMs: vi.fn(() => 0),
  runIsLive: vi.fn(() => false),
  // The pause predicate, imported by the run card AND the exec source so both
  // alerts recognise a step waiting on a person. Inert here for the same reason as
  // the rest — `runState` answers undefined, so no alert is built — but it has to
  // EXIST, because a browser-mode mock is linked as real ESM: a name any module in
  // the graph reaches must be on the factory or collection fails outright.
  isNeedInputPark: vi.fn(() => false),
  // The pause-detail phrase, imported by the same two consumers — here for the
  // ESM-linking reason above, not because this suite paints a pause.
  pauseDetailPhrase: vi.fn(() => undefined),
}));

vi.mock("./run-dots.js", () => ({ refreshRunDots: vi.fn(), trackRun: vi.fn() }));

vi.mock("./actions/runs.js", () => {
  const stub = { dispatch: vi.fn(() => Promise.resolve()) };
  return { cancelRun: stub, pauseRun: stub, resumeRun: stub, retryRun: stub };
});

const { openRunView } = await import("./run-view.js");

beforeEach(() => {
  m.opened.length = 0;
  m.tabs.clear();
  m.launchedBy.clear();
});

describe("the door into a run view", () => {
  // One link, two jobs: open the run when its tab is gone, and come to it when the
  // tab is already there. `openTab` activates an existing id unless told not to,
  // and this path deliberately does not tell it not to.
  it("focuses a tab that is already open rather than doing nothing", () => {
    m.tabs.add("c-1");
    m.tabs.add("run:wf_open");
    m.launchedBy.set("wf_open", "c-1");

    openRunView("wf_open", "publish-pr");
    // It reaches openRunTab again with no `activate: false`, which is what makes
    // openTab activate the existing tab instead of returning silently.
    expect(m.opened).toHaveLength(1);
    expect(m.opened[0]?.opts?.activate).toBeUndefined();
  });

  it("finds the launching chat in the store when the caller does not know it", () => {
    // The deep-link case: `/run/{id}` carries no parent, so the store answers.
    m.tabs.add("c-7");
    m.launchedBy.set("wf_6", "c-7");
    openRunView("wf_6", "wf_6");
    expect(m.opened[0]?.opts).toEqual({ parent: "c-7", owns: false });
  });

  it("prefers an explicit parent over the store's record", () => {
    // Third and last argument: the parent CHAT. Whether the RUN is parentless is
    // not passed — that is the run's own fact and the composition root resolves it
    // from the run store, because a chat-parented run reviewed while its chat's tab
    // is closed has an empty subject Parent without being parentless.
    m.tabs.add("c-explicit");
    m.launchedBy.set("wf_7", "c-stored");
    openRunView("wf_7", "n", "c-explicit");
    expect(m.opened[0]?.opts?.parent).toBe("c-explicit");
  });

  it("stays top-level for a parentless run", () => {
    openRunView("wf_8", "nightly");
    // A review OWNS nothing whatever it nests under, so the flag travels even when
    // the parent does not: that is the field the two run forms differ in.
    expect(m.opened).toEqual([{ id: "wf_8", name: "nightly", opts: { owns: false } }]);
  });

  it("stays top-level when the launching chat is not open here", () => {
    m.launchedBy.set("wf_9", "c-elsewhere");
    openRunView("wf_9", "publish-pr");
    // No parent, because the chat has no tab to nest under — and the review's own
    // `owns: false` still travels, which is what keeps its × from stopping the run.
    expect(m.opened[0]?.opts).toEqual({ owns: false });
  });
});
