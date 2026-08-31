// ---------------------------------------------------------------------------
// The activity dot on a workflow run's tab.
//
// Four rules carry the whole feature and each is easy to break silently: EVERY
// run gets a dot (a chat's own dot cannot cover a run that outlives its turn); the
// dot must repaint when the DOCK's queue changes rather than only when a run event
// arrives (a background run blocked on a permission with nobody watching is the
// case it exists for); the status must come from the store AS A TRACKED READ, so
// the fetch an invalidation coalesces into repaints the dot when it resolves; and
// a tracked run whose tab is not there YET keeps its place and paints the moment
// the tab lands — the automatic tab offer is a server round trip, so the run's
// first frame routinely beats its own tab.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import { signal } from "@cplieger/reactive";
import type { RunState } from "./run-store.js";

const m = {
  painted: [] as { id: string; status: string }[],
  tabs: new Set<string>(),
  asking: new Set<string>(),
  states: new Map<string, RunState>(),
};

const tabsVersion = signal(0);
vi.mock("./tabs.js", () => ({
  // The projection's lookup: a run's TAB id is opaque, so the module asks for it
  // by `(kind, ref)`. The fake keeps the old readable id as the answer, which is
  // what the assertions below name.
  tabIdFor: vi.fn((_kind: string, ref: string) => (m.tabs.has(`run:${ref}`) ? `run:${ref}` : "")),
  setTabStatus: vi.fn((id: string, status: string) => {
    m.painted.push({ id, status });
  }),
  // A signal read, like production: the effect's re-run on a tab mutation IS the
  // dependency under test.
  tabSetVersion: vi.fn(() => tabsVersion.value),
}));

// All three dependencies have to stay SIGNAL reads, or the effect under test
// loses the dependency that makes it repaint.
const queueVersion = signal(0);
vi.mock("./decision-dock.js", () => ({
  hasPendingDecision: vi.fn((id: string) => {
    void queueVersion.value;
    return m.asking.has(id);
  }),
}));

const statesVersion = signal(0);
vi.mock("./run-store.js", () => ({
  // TRACKED, like the real runState: a cell write repaints. The old peekRunState
  // read is the mutant this mock refuses to satisfy — production importing it
  // fails the suite at link time.
  runState: vi.fn((id: string) => {
    void statesVersion.value;
    return m.states.get(id);
  }),
}));

const { trackRun, refreshRunDots, installRunDotSubscriber } = await import("./run-dots.js");

/** Bump the mocked dock queue the way a real push does, so the effect re-runs. */
function dockChanged(): void {
  queueVersion.value = queueVersion.value + 1;
}

/** Bump the mocked tab set the way a committed tab mutation does. */
function tabsChanged(): void {
  tabsVersion.value = tabsVersion.value + 1;
}

/** Bump the mocked run store the way a resolved fetch's cell write does. */
function storeChanged(): void {
  statesVersion.value = statesVersion.value + 1;
}

let installed = false;

beforeEach(() => {
  m.painted.length = 0;
  m.tabs.clear();
  m.asking.clear();
  m.states.clear();
  if (!installed) {
    installRunDotSubscriber();
    installed = true;
  }
});

describe("every run's tab gets a dot, whoever launched it", () => {
  it("paints a run that has a tab open", () => {
    m.tabs.add("run:wf_1");
    m.states.set("wf_1", { workflowId: "wf_1", status: "running" });
    trackRun("wf_1");
    expect(m.painted).toEqual([{ id: "run:wf_1", status: "working" }]);
  });

  // The REVERSAL, pinned in the direction it was reversed. An agent-launched run
  // used to be excluded because its launching chat showed `working`; that chat goes
  // idle the moment the launching turn ends, which is seconds into a run that lasts
  // minutes, so the exclusion left the run with no signal at all.
  it("paints an agent-launched run too, because its chat's dot goes idle first", () => {
    m.tabs.add("run:wf_agent");
    m.states.set("wf_agent", { workflowId: "wf_agent", status: "running" });
    trackRun("wf_agent");
    expect(m.painted).toEqual([{ id: "run:wf_agent", status: "working" }]);
  });

  it("ignores an empty workflow id", () => {
    trackRun("");
    expect(m.painted).toEqual([]);
  });

  it("paints nothing for a run with no tab open", () => {
    m.states.set("wf_notab", { workflowId: "wf_notab", status: "running" });
    trackRun("wf_notab");
    expect(m.painted).toEqual([]);
  });
});

describe("the status comes from the store, as a tracked read", () => {
  it("repaints on a store cell write alone, through a run's whole life", () => {
    m.tabs.add("run:wf_3");
    m.states.set("wf_3", { workflowId: "wf_3", status: "running" });
    trackRun("wf_3");
    expect(m.painted.at(-1)).toEqual({ id: "run:wf_3", status: "working" });

    // A pause is not a finish: the run is stopped waiting for something. The
    // drive is the STORE's own signal — the fetch an invalidation coalesced
    // into resolving — with no run event and no dock churn. An untracked read
    // (the old peekRunState) never sees these writes and the dot stays stale.
    m.states.set("wf_3", { workflowId: "wf_3", status: "paused" });
    storeChanged();
    expect(m.painted.at(-1)).toEqual({ id: "run:wf_3", status: "waiting" });

    m.states.set("wf_3", { workflowId: "wf_3", status: "failed" });
    storeChanged();
    expect(m.painted.at(-1)).toEqual({ id: "run:wf_3", status: "failed" });
  });

  it("paints nothing at all for a run the store has never answered for", () => {
    // "" is the absent state, not `idle`: an idle dot would claim a run exists and
    // is doing nothing, which is a different thing from not knowing yet.
    m.tabs.add("run:wf_4");
    trackRun("wf_4");
    expect(m.painted).toEqual([{ id: "run:wf_4", status: "" }]);
  });

  it("refreshRunDots repaints a run the module already knows", () => {
    // run-view's door: after its own fetch it calls trackRun (a no-op for a
    // known id, so no bump) + refreshRunDots. The nudge must repaint from the
    // store's CURRENT answer even though no tracked signal fired.
    m.tabs.add("run:wf_nudge");
    m.states.set("wf_nudge", { workflowId: "wf_nudge", status: "running" });
    trackRun("wf_nudge");
    m.painted.length = 0;

    m.states.set("wf_nudge", { workflowId: "wf_nudge", status: "completed" });
    trackRun("wf_nudge");
    expect(m.painted).toEqual([]);
    refreshRunDots();
    expect(m.painted).toEqual([{ id: "run:wf_nudge", status: "done" }]);
  });
});

describe("the dot repaints on a dock change, with no run event", () => {
  it("flips to input when an ask lands under the synthetic chat id", () => {
    m.tabs.add("run:wf_5");
    m.states.set("wf_5", { workflowId: "wf_5", status: "running" });
    trackRun("wf_5");
    m.painted.length = 0;

    m.asking.add("run:wf_5");
    dockChanged();
    expect(m.painted).toEqual([{ id: "run:wf_5", status: "input" }]);
  });

  it("also joins on the bare workflow id, the dock's other key", () => {
    m.tabs.add("run:wf_6");
    m.states.set("wf_6", { workflowId: "wf_6", status: "running" });
    trackRun("wf_6");
    m.painted.length = 0;

    m.asking.add("wf_6");
    dockChanged();
    expect(m.painted).toEqual([{ id: "run:wf_6", status: "input" }]);
  });
});

describe("a tracked run without a tab keeps its place and paints on arrival", () => {
  // The automatic tab offer is a server round trip, so a run's first frame
  // routinely beats its own tab. The old sweep DELETED the id when the tab was
  // missing, and a paused run emits no re-add frame at all — its dot stayed
  // blank until unrelated dock churn happened to repaint. PAUSED is used below
  // because it is exactly the no-second-chance case.
  it("paints the moment the tab lands, with no fresh run event", () => {
    m.states.set("wf_race", { workflowId: "wf_race", status: "paused" });
    trackRun("wf_race");
    expect(m.painted).toEqual([]);

    m.tabs.add("run:wf_race");
    tabsChanged();
    expect(m.painted).toEqual([{ id: "run:wf_race", status: "waiting" }]);
  });

  it("stops painting while the tab is closed, and resumes when it reopens", () => {
    m.tabs.add("run:wf_7");
    m.states.set("wf_7", { workflowId: "wf_7", status: "running" });
    trackRun("wf_7");
    expect(m.painted).toEqual([{ id: "run:wf_7", status: "working" }]);

    // Closed: no paint — but the id is NOT dropped.
    m.tabs.delete("run:wf_7");
    m.painted.length = 0;
    tabsChanged();
    expect(m.painted).toEqual([]);

    // Reopened from History: the tab-set dependency alone brings the dot back.
    m.tabs.add("run:wf_7");
    m.painted.length = 0;
    tabsChanged();
    expect(m.painted).toEqual([{ id: "run:wf_7", status: "working" }]);
  });
});
