// ---------------------------------------------------------------------------
// Opening a run's tab: the automatic offer, and the re-open.
//
// The two paths have OPPOSITE guards and that is the whole design. The automatic
// offer fires once per run per client, because a run emits a `run_progress` per
// node event and without the guard a reader who closed the tab would watch it
// reappear within seconds, forever. The re-open has no guard at all, because a
// reader clicking the run in its transcript is asking for it back and refusing them
// would be the opposite of respecting the close.
//
// Both nest under the launching chat with `owns: false`, which is the close
// contract: the sub-tab's × stops watching, the chat's × stops the run.
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
}));

vi.mock("./decision-dock.js", () => ({
  mountRunDecisionDock: vi.fn(),
  rerenderDocks: vi.fn(),
  hasPendingDecision: vi.fn(() => false),
}));

vi.mock("./run-store.js", () => ({
  invalidateRun: vi.fn(),
  runState: vi.fn(() => undefined),
  runChatID: vi.fn((id: string) => m.launchedBy.get(id) ?? ""),
}));

vi.mock("./run-dots.js", () => ({ refreshRunDots: vi.fn(), trackRun: vi.fn() }));

vi.mock("./actions/runs.js", () => {
  const stub = { dispatch: vi.fn(() => Promise.resolve()) };
  return { cancelRun: stub, pauseRun: stub, resumeRun: stub, retryRun: stub };
});

const { openRunSubTab, openRunView, openLiveRunView, autoCloseRunSubTab } =
  await import("./run-view.js");

beforeEach(() => {
  m.opened.length = 0;
  m.closed.length = 0;
  m.tabs.clear();
  m.launchedBy.clear();
  m.active = "";
});

describe("the automatic offer", () => {
  it("nests under the launching chat, without stealing focus, owning nothing", () => {
    m.tabs.add("c-1");
    openRunSubTab("wf_1", "publish-pr", "c-1");
    expect(m.opened).toEqual([
      { id: "wf_1", name: "publish-pr", opts: { parent: "c-1", owns: false, activate: false } },
    ]);
  });

  // The bug this exists for: a run emits a progress frame per node event, so a
  // second offer would undo the reader's close over and over.
  it("offers ONCE per run, so a close is final for the automatic path", () => {
    m.tabs.add("c-1");
    openRunSubTab("wf_2", "publish-pr", "c-1");
    expect(m.opened).toHaveLength(1);

    // The reader closes it. Every later frame must leave it closed.
    m.tabs.delete("run:wf_2");
    for (let i = 0; i < 5; i++) {
      openRunSubTab("wf_2", "publish-pr", "c-1");
    }
    expect(m.opened).toHaveLength(1);
  });

  it("does nothing for a parentless run: there is no chat to nest under", () => {
    openRunSubTab("wf_3", "nightly", "");
    expect(m.opened).toEqual([]);
  });

  it("does nothing when the launching chat has no tab, and stays offerable", () => {
    // A background chat on another device's arrangement. Marking it offered here
    // would deny the run its tab for good once that chat does open.
    openRunSubTab("wf_4", "publish-pr", "c-absent");
    expect(m.opened).toEqual([]);

    m.tabs.add("c-absent");
    openRunSubTab("wf_4", "publish-pr", "c-absent");
    expect(m.opened).toHaveLength(1);
  });

  it("ignores an empty workflow id", () => {
    m.tabs.add("c-1");
    openRunSubTab("", "publish-pr", "c-1");
    expect(m.opened).toEqual([]);
  });
});

describe("the re-open", () => {
  it("has no guard: it re-opens a run whose automatic tab was closed", () => {
    m.tabs.add("c-1");
    m.launchedBy.set("wf_5", "c-1");
    openRunSubTab("wf_5", "publish-pr", "c-1");
    m.tabs.delete("run:wf_5");
    m.opened.length = 0;

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
    m.tabs.add("c-1");
    m.launchedBy.set("wf_open", "c-1");
    openRunSubTab("wf_open", "publish-pr", "c-1");
    m.opened.length = 0;

    openRunView("wf_open", "publish-pr");
    // It reaches openRunTab again with no `activate: false`, which is what makes
    // openTab activate the existing tab instead of returning silently.
    expect(m.opened).toHaveLength(1);
    expect(m.opened[0]?.opts?.activate).toBeUndefined();
  });

  it("the automatic offer, by contrast, never steals focus from an open tab", () => {
    m.tabs.add("c-1");
    openRunSubTab("wf_quiet", "publish-pr", "c-1");
    expect(m.opened[0]?.opts?.activate).toBe(false);
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
// three directions: only a tab this client opened by itself, only a clean ending,
// and never the tab on screen. Each gate closes a way the app could take a tab
// someone still wanted, which is the same rule the offer guard enforces from the
// other side.
// ---------------------------------------------------------------------------

describe("the completion auto-close", () => {
  it("closes an automatic sub-tab once its run completes", () => {
    m.tabs.add("c-1");
    openRunSubTab("wf_ac1", "publish-pr", "c-1");
    autoCloseRunSubTab("wf_ac1", "completed");
    expect(m.closed).toEqual(["run:wf_ac1"]);
  });

  // The house rule for automatic hiding, borrowed from tool-group.ts: a failure is
  // not noise, so nothing folds it away. A failed run is the one whose detail is
  // worth the row.
  it.each(["failed", "aborted"])("keeps the tab when the run ended badly: %s", (status) => {
    m.tabs.add("c-1");
    openRunSubTab(`wf_bad_${status}`, "publish-pr", "c-1");
    autoCloseRunSubTab(`wf_bad_${status}`, status);
    expect(m.closed).toEqual([]);
  });

  // `paused` arrives on the same frame as a real ending (KAS reports an
  // onMaxIterations stop through it), so treating it as one would close the tab of
  // a run that is still this process's to resume — and the claim has to SURVIVE it,
  // or the resumed run's real completion would find nothing to close.
  it("treats paused as no ending at all, and still closes on the real one", () => {
    m.tabs.add("c-1");
    openRunSubTab("wf_ac2", "publish-pr", "c-1");
    autoCloseRunSubTab("wf_ac2", "paused");
    expect(m.closed).toEqual([]);

    autoCloseRunSubTab("wf_ac2", "completed");
    expect(m.closed).toEqual(["run:wf_ac2"]);
  });

  it("keeps a tab whose status it cannot classify", () => {
    m.tabs.add("c-1");
    openRunSubTab("wf_ac3", "publish-pr", "c-1");
    autoCloseRunSubTab("wf_ac3", "something-new-upstream");
    expect(m.closed).toEqual([]);
  });

  // The moment a run's output becomes worth reading is the moment it finishes, so
  // this is exactly when the view must not be pulled away.
  it("never closes the tab the reader is looking at", () => {
    m.tabs.add("c-1");
    openRunSubTab("wf_ac4", "publish-pr", "c-1");
    m.active = "run:wf_ac4";
    autoCloseRunSubTab("wf_ac4", "completed");
    expect(m.closed).toEqual([]);
  });

  it("leaves a tab the reader opened themselves alone, for good", () => {
    m.tabs.add("c-1");
    m.launchedBy.set("wf_ac5", "c-1");
    openRunSubTab("wf_ac5", "publish-pr", "c-1");
    // The card's "Open run" link, or a /run/{id} deep link. From here the tab is
    // theirs.
    openRunView("wf_ac5", "publish-pr");
    autoCloseRunSubTab("wf_ac5", "completed");
    expect(m.closed).toEqual([]);
  });

  // A launcher-owned tab's × CANCELS, so it must be unreachable from here even
  // though a finished run's cancel would be a no-op.
  it("leaves a launcher-owned tab alone", () => {
    m.tabs.add("c-1");
    openRunSubTab("wf_ac6", "publish-pr", "c-1");
    openLiveRunView("wf_ac6", "publish-pr");
    autoCloseRunSubTab("wf_ac6", "completed");
    expect(m.closed).toEqual([]);
  });

  // This is what keeps a TANGENT out without a filter naming one: only the run
  // door above ever makes a tab closable here, so a forked chat's sub-tab — and
  // any other tab in the strip — is unreachable from this function.
  it("closes nothing for an id it never opened itself", () => {
    m.tabs.add("c-1");
    m.tabs.add("run:wf_elsewhere");
    autoCloseRunSubTab("wf_elsewhere", "completed");
    expect(m.closed).toEqual([]);
  });

  it("tolerates a tab the reader already closed", () => {
    m.tabs.add("c-1");
    openRunSubTab("wf_ac7", "publish-pr", "c-1");
    m.tabs.delete("run:wf_ac7");
    autoCloseRunSubTab("wf_ac7", "completed");
    expect(m.closed).toEqual([]);
  });
});
