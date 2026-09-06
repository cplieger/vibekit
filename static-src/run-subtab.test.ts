// ---------------------------------------------------------------------------
// Opening a run's tab, and the marker that says who opened it.
//
// There is ONE opener here, the manual one, and it has no guard at all: a reader
// clicking the run in its transcript is asking for it back, and refusing them would
// be the opposite of respecting the close. It nests under the launching chat with
// `owns: false`, which is the close contract — the sub-tab's × stops watching, the
// chat's × stops the run.
//
// The tab a starting run gets BY ITSELF is opened server-side, so the client's own
// automatic offer is gone and `noteAutoOpenedRun` is what is left of it: a per-client
// record of which tabs the app produced, which is the one thing the server cannot
// answer and the only thing the completion auto-close may act on.
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
  closed: [] as string[],
  tabs: new Set<string>(),
  launchedBy: new Map<string, string>(),
  active: "",
};

vi.mock("./tabs.js", () => ({
  // Present-but-inert: the run tab renders the run CARD now, whose markdown
  // bubble reaches the linkifier and through it the editor openers, so these
  // names are imported somewhere in this graph. No case here opens a file.
  // `getActiveTabId` is NOT inert here and is declared below, where the
  // auto-close's never-the-tab-on-screen rule reads it.
  openEditorView: vi.fn(),
  setTabDirty: vi.fn(),
  toggleGitView: vi.fn(),
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
  closeTab: vi.fn((id: string) => {
    m.closed.push(id);
    m.tabs.delete(id);
    return Promise.resolve();
  }),
  getActiveTabId: vi.fn(() => m.active),
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
}));

vi.mock("./run-dots.js", () => ({ refreshRunDots: vi.fn(), trackRun: vi.fn() }));

vi.mock("./actions/runs.js", () => {
  const stub = { dispatch: vi.fn(() => Promise.resolve()) };
  return { cancelRun: stub, pauseRun: stub, resumeRun: stub, retryRun: stub };
});

const { noteAutoOpenedRun, openRunView, autoCloseRunSubTab } = await import("./run-view.js");
// The dock's own reader, mocked above: the auto-close asks it the same question the
// tab-dot painter does, so a case that queues an ask drives it through here.
const { runPendingAsks } = await import("./decision-dock.js");

beforeEach(() => {
  m.opened.length = 0;
  m.closed.length = 0;
  m.tabs.clear();
  m.launchedBy.clear();
  m.active = "";
});

describe("the automatic offer is the server's", () => {
  // The whole point of moving it: a client reaction opened the tab only where one
  // browser happened to be connected, hydrated and holding the launching chat's tab
  // when a frame arrived, and `TabSubject.Parent` is immutable, so a wrong answer
  // was permanent and differed per device. Nothing here may open a tab.
  it("opens NOTHING, whatever the launching chat's tab looks like here", () => {
    m.tabs.add("c-1");
    noteAutoOpenedRun("wf_1", "c-1");
    noteAutoOpenedRun("wf_absent", "c-absent");
    noteAutoOpenedRun("wf_none", "");
    expect(m.opened).toEqual([]);
  });

  // The claim's one job: let the completion auto-close tell a tab the app produced
  // from one the reader asked for. Proven through that rule rather than by reading
  // the set, which is module-private.
  it("claims the run's tab, so the completion auto-close may take it", () => {
    m.tabs.add("c-1");
    m.tabs.add("run:wf_claim");
    noteAutoOpenedRun("wf_claim", "c-1");
    autoCloseRunSubTab("wf_claim", "completed");
    expect(m.closed).toEqual(["run:wf_claim"]);
  });

  it("claims nothing for a run it was given no launching chat for", () => {
    // A PARENTLESS run: the server offers it no tab, so the tab on screen is the
    // one the Workflows tab's Run button opened for the reader.
    m.tabs.add("run:wf_parentless");
    noteAutoOpenedRun("wf_parentless", "");
    autoCloseRunSubTab("wf_parentless", "completed");
    expect(m.closed).toEqual([]);
  });

  it("ignores an empty workflow id", () => {
    m.tabs.add("c-1");
    m.tabs.add("run:");
    noteAutoOpenedRun("", "c-1");
    autoCloseRunSubTab("", "completed");
    expect(m.closed).toEqual([]);
  });

  // `run_start` re-fires on every resume, so without the once-per-client latch a
  // resume would re-claim a tab the reader had deliberately re-opened, and the
  // completion auto-close would then take it from them.
  it("records once per client, so a resume cannot re-claim a re-opened tab", () => {
    m.tabs.add("c-1");
    m.launchedBy.set("wf_relatch", "c-1");
    noteAutoOpenedRun("wf_relatch", "c-1");
    // The reader asks for the run themselves. From here the tab is theirs.
    openRunView("wf_relatch", "publish-pr");
    // The run resumes: `run_started` fires again.
    noteAutoOpenedRun("wf_relatch", "c-1");
    autoCloseRunSubTab("wf_relatch", "completed");
    expect(m.closed).toEqual([]);
  });
});

describe("the re-open", () => {
  it("has no guard: it re-opens a run whose app-opened tab was closed", () => {
    // The tab the server offered, and the reader's close of it — so nothing here
    // holds a `run:wf_5` row and the app has spent its one offer.
    m.tabs.add("c-1");
    m.launchedBy.set("wf_5", "c-1");
    noteAutoOpenedRun("wf_5", "c-1");

    // What the card's "Open run" link does.
    openRunView("wf_5", "publish-pr");
    expect(m.opened).toEqual([
      { id: "wf_5", name: "publish-pr", opts: { parent: "c-1", owns: false } },
    ]);
  });

  // One link, two jobs: bring it back when it is closed, and come to it when it is
  // already open. `openTab` activates an existing id unless told not to, and this
  // path deliberately does not tell it not to — unlike the automatic offer.
  it("focuses a tab that is already open rather than doing nothing", () => {
    // The server's offer already landed, so the row is on the strip.
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

// ---------------------------------------------------------------------------
// The completion auto-close.
//
// The offer's counterpart, and it is narrower than "close a finished run's tab" in
// four directions: only a tab this client opened by itself, only a clean ending,
// only a run whose tab DOT reads green, and never the tab on screen. Each gate
// closes a way the app could take a tab someone still wanted, which is the same
// rule the offer guard enforces from the other side.
// ---------------------------------------------------------------------------

describe("the completion auto-close", () => {
  /** The state after the server offered a run's tab and this client recorded the
   *  claim: the launching chat's row, the run's own row, and the marker. Named
   *  because every case below needs all three — a case missing the run's row would
   *  pass for the wrong reason, since the auto-close drops a claim whose tab is
   *  already gone. */
  function appOpened(workflowID: string): void {
    m.tabs.add("c-1");
    m.tabs.add(`run:${workflowID}`);
    noteAutoOpenedRun(workflowID, "c-1");
  }

  it("closes an automatic sub-tab once its run completes", () => {
    appOpened("wf_ac1");
    autoCloseRunSubTab("wf_ac1", "completed");
    expect(m.closed).toEqual(["run:wf_ac1"]);
  });

  // The house rule for automatic hiding, borrowed from tool-group.ts: a failure is
  // not noise, so nothing folds it away. A failed run is the one whose detail is
  // worth the row.
  it.each(["failed", "aborted"])("keeps the tab when the run ended badly: %s", (status) => {
    appOpened(`wf_bad_${status}`);
    autoCloseRunSubTab(`wf_bad_${status}`, status);
    expect(m.closed).toEqual([]);
  });

  // `paused` arrives on the same frame as a real ending (KAS reports an
  // onMaxIterations stop through it), so treating it as one would close the tab of
  // a run that is still this process's to resume — and the claim has to SURVIVE it,
  // or the resumed run's real completion would find nothing to close.
  it("treats paused as no ending at all, and still closes on the real one", () => {
    appOpened("wf_ac2");
    autoCloseRunSubTab("wf_ac2", "paused");
    expect(m.closed).toEqual([]);

    autoCloseRunSubTab("wf_ac2", "completed");
    expect(m.closed).toEqual(["run:wf_ac2"]);
  });

  it("keeps a tab whose status it cannot classify", () => {
    appOpened("wf_ac3");
    autoCloseRunSubTab("wf_ac3", "something-new-upstream");
    expect(m.closed).toEqual([]);
  });

  /** One queued ask, filed under `workflowID` and under no other run — the dock's
   *  own answer shape, so the gate reads it exactly as production does. */
  function asking(workflowID: string): void {
    vi.mocked(runPendingAsks).mockImplementation((id) =>
      id === workflowID
        ? { count: 1, nodes: new Set(["publish"]), label: "Allow git push?" }
        : { count: 0, nodes: new Set<string>(), label: "" },
    );
  }

  // The ask is the dot's SECOND input, and the `run_finished` path retires none of
  // them — they leave the dock queue on their own settle frames — so a run stopped
  // while parked on one arrives here as a CLEAN ending with the ask still queued.
  // The dot is amber, so the tab stays: the answer is still owed.
  it("keeps the tab of a run stopped while parked on an ask", () => {
    appOpened("wf_ask");
    asking("wf_ask");
    autoCloseRunSubTab("wf_ask", "cancelled");
    expect(m.closed).toEqual([]);
  });

  // ...and the ask has to be THIS run's. The dock holds every run's, keyed two
  // ways, so a gate that read the queue without naming the run would keep every
  // automatic tab alive for as long as anything anywhere is blocked on a person.
  it("closes the tab when the queued ask belongs to another run", () => {
    appOpened("wf_ac8");
    asking("wf_other");
    autoCloseRunSubTab("wf_ac8", "completed");
    expect(m.closed).toEqual(["run:wf_ac8"]);
  });

  // The moment a run's output becomes worth reading is the moment it finishes, so
  // this is exactly when the view must not be pulled away.
  it("never closes the tab the reader is looking at", () => {
    appOpened("wf_ac4");
    m.active = "run:wf_ac4";
    autoCloseRunSubTab("wf_ac4", "completed");
    expect(m.closed).toEqual([]);
  });

  it("leaves a tab the reader opened themselves alone, for good", () => {
    appOpened("wf_ac5");
    m.launchedBy.set("wf_ac5", "c-1");
    // The card's "Open run" link, or a /run/{id} deep link. From here the tab is
    // theirs.
    openRunView("wf_ac5", "publish-pr");
    autoCloseRunSubTab("wf_ac5", "completed");
    expect(m.closed).toEqual([]);
  });

  // The Workflows tab's Run button goes through the same door as every other
  // manual one now — `openLiveRunView` is gone, and with it the `owns: true` tab
  // whose × cancelled — so a launched run's tab is claimed by the reader exactly
  // like a re-opened one and this function cannot reach it either.
  it("leaves a launched run's own tab alone", () => {
    appOpened("wf_ac6");
    openRunView("wf_ac6", "publish-pr");
    autoCloseRunSubTab("wf_ac6", "completed");
    expect(m.closed).toEqual([]);
  });

  // Only the run door above ever makes a tab closable here, which is what keeps a
  // TANGENT out without a filter naming one. Three populations land in it, and the
  // last two are a recorded DECISION rather than an oversight (see `autoOpened`):
  // the server opens the tab for every device while the marker is per client, so a
  // run no client saw start — and a reader who joined after the start frame, whose
  // tab came from the server's own retry — keep it after a clean finish.
  it.each([
    ["a tangent, or any other tab this module never touched", "wf_elsewhere"],
    ["a run whose start frame no connected client saw", "wf_nowitness"],
    ["a reader who joined mid-run, after the start frame", "wf_joinedlate"],
  ])("closes nothing for %s", (_population, workflowID) => {
    m.tabs.add("c-1");
    m.tabs.add(`run:${workflowID}`);
    autoCloseRunSubTab(workflowID, "completed");
    expect(m.closed).toEqual([]);
  });

  it("tolerates a tab the reader already closed", () => {
    appOpened("wf_ac7");
    m.tabs.delete("run:wf_ac7");
    autoCloseRunSubTab("wf_ac7", "completed");
    expect(m.closed).toEqual([]);
  });
});
