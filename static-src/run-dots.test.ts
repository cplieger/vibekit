// ---------------------------------------------------------------------------
// The activity dot on a workflow run's tab.
//
// Three rules carry the whole feature and each is easy to break silently: EVERY
// run gets a dot (a chat's own dot cannot cover a run that outlives its turn), the
// dot must repaint when the DOCK's queue changes rather than only when a run event
// arrives (a background run blocked on a permission with nobody watching is the
// case it exists for), and the status must come from the store rather than a copy
// kept here.
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

vi.mock("./tabs.js", () => ({
  // The projection's lookup: a run's TAB id is opaque, so the module asks for it
  // by `(kind, ref)`. The fake keeps the old readable id as the answer, which is
  // what the assertions below name.
  tabIdFor: vi.fn((_kind: string, ref: string) => (m.tabs.has(`run:${ref}`) ? `run:${ref}` : "")),
  setTabStatus: vi.fn((id: string, status: string) => {
    m.painted.push({ id, status });
  }),
}));

// Both dependencies have to stay SIGNAL reads, or the effect under test loses the
// dependency that makes it repaint.
const queueVersion = signal(0);
vi.mock("./decision-dock.js", () => ({
  hasPendingDecision: vi.fn((id: string) => {
    void queueVersion.value;
    return m.asking.has(id);
  }),
}));

vi.mock("./run-store.js", () => ({
  peekRunState: vi.fn((id: string) => m.states.get(id)),
}));

const { trackRun, refreshRunDots, installRunDotSubscriber } = await import("./run-dots.js");

/** Bump the mocked dock queue the way a real push does, so the effect re-runs. */
function dockChanged(): void {
  queueVersion.value = queueVersion.value + 1;
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

describe("the status comes from the store, not from a copy", () => {
  it("follows the store through a run's whole life with no further events", () => {
    m.tabs.add("run:wf_3");
    m.states.set("wf_3", { workflowId: "wf_3", status: "running" });
    trackRun("wf_3");
    expect(m.painted.at(-1)).toEqual({ id: "run:wf_3", status: "working" });

    // A pause is not a finish: the run is stopped waiting for something.
    m.states.set("wf_3", { workflowId: "wf_3", status: "paused" });
    refreshRunDots();
    expect(m.painted.at(-1)).toEqual({ id: "run:wf_3", status: "waiting" });

    m.states.set("wf_3", { workflowId: "wf_3", status: "failed" });
    refreshRunDots();
    expect(m.painted.at(-1)).toEqual({ id: "run:wf_3", status: "failed" });
  });

  it("paints nothing at all for a run the store has never answered for", () => {
    // "" is the absent state, not `idle`: an idle dot would claim a run exists and
    // is doing nothing, which is a different thing from not knowing yet.
    m.tabs.add("run:wf_4");
    trackRun("wf_4");
    expect(m.painted).toEqual([{ id: "run:wf_4", status: "" }]);
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

describe("the sweep is what bounds the tracked set", () => {
  it("drops a run whose tab has closed, and stops painting it", () => {
    m.tabs.add("run:wf_7");
    m.states.set("wf_7", { workflowId: "wf_7", status: "running" });
    trackRun("wf_7");
    expect(m.painted).toHaveLength(1);

    m.tabs.delete("run:wf_7");
    m.painted.length = 0;
    dockChanged();
    expect(m.painted).toEqual([]);

    // Gone from the set, not merely unpainted: a later dock change finds nothing.
    m.tabs.add("run:wf_7");
    m.painted.length = 0;
    dockChanged();
    expect(m.painted).toEqual([]);
  });
});
