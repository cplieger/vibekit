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
// the tab lands — the tab a starting run gets is opened server-side, so the run's
// first frame routinely beats its own tab.
//
// The NAME rides the same effect, and for the same reason as that last rule: a tab
// opened by the server carries no label, so the row is built from a store cell that
// is still empty and has to be corrected when the first fetch resolves.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import { signal } from "@cplieger/reactive";
import type { RunNode, RunState } from "./run-store.js";

const m = {
  painted: [] as { id: string; status: string }[],
  named: [] as { id: string; name: string }[],
  tabs: new Set<string>(),
  // One entry per unanswered ask, as the dock holds it: the chat id it is FILED
  // under plus the run stamped on its payload. Two fields rather than one set of
  // keys, because the two keyings are exactly what the join has to reconcile — a
  // parentless run's ask is filed under `run:<id>` with no chat, an
  // agent-launched one under the LAUNCHING chat with the run on the payload.
  asks: [] as { chatID: string; runID: string }[],
  states: new Map<string, RunState>(),
  names: new Map<string, string>(),
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
  // The real one is a no-op when the row already carries the label, which is what
  // bounds the self-write this effect makes; the fake records every call so a case
  // can assert the repeat is suppressed.
  renameTab: vi.fn((id: string, name: string) => {
    if (m.names.get(id) === name) {
      return;
    }
    m.names.set(id, name);
    m.named.push({ id, name });
    tabsVersion.value = tabsVersion.value + 1;
  }),
  // A signal read, like production: the effect's re-run on a tab mutation IS the
  // dependency under test.
  tabSetVersion: vi.fn(() => tabsVersion.value),
}));

// All three dependencies have to stay SIGNAL reads, or the effect under test
// loses the dependency that makes it repaint.
const queueVersion = signal(0);
vi.mock("./decision-dock.js", () => ({
  // The RUN-scoped reader, and the fake performs the real join rather than a set
  // lookup: `hasPendingDecision` is a CHAT-keyed predicate, so passing it a
  // workflow id matched nothing ever and an agent-launched run's ask — the one
  // population whose asks arrive on a chat bridge — never reached this dot.
  // Production importing that name again fails the suite at link time.
  runPendingAsks: vi.fn((workflowID: string) => {
    void queueVersion.value;
    const runKey = `run:${workflowID}`;
    const mine = m.asks.filter((a) => a.runID === workflowID || a.chatID === runKey);
    return { count: mine.length, nodes: new Set<string>(), label: "" };
  }),
}));

const statesVersion = signal(0);
let observer: ((workflowID: string) => void) | null = null;
vi.mock("./run-store.js", () => ({
  // TRACKED, like the real runState: a cell write repaints. The old peekRunState
  // read is the mutant this mock refuses to satisfy — production importing it
  // fails the suite at link time.
  runState: vi.fn((id: string) => {
    void statesVersion.value;
    return m.states.get(id);
  }),
  // The label, resolved by the store over the same cell. Untracked in production,
  // which costs nothing here: the status read above is already the subscription.
  runLabelOf: vi.fn((id: string) => {
    const st = m.states.get(id);
    return st?.runLabel ?? st?.workflowName ?? "";
  }),
  // The store's seam for "this client now knows about this run", which the
  // live-runs rebuild reports each row to. Recorded rather than inert: whether a
  // consumer registers at all is the half a store-side test cannot see.
  registerLiveRunObserver: vi.fn((fn: (workflowID: string) => void) => {
    observer = fn;
  }),
  // The REAL rule, not a stub. The dot's yellow arm is decided by it, so a stub
  // answering `false` would leave the park case green while production could not
  // tell a park on a person from a network blip. BOTH arms, because a park inside a
  // parallel branch reaches only the second: the run keeps no matching reason for
  // it, and the node's own signal is what survives.
  isNeedInputPark: (state: RunState | undefined): boolean => {
    if (state?.status !== "paused") {
      return false;
    }
    const reason = state.pauseReason;
    const byReason =
      reason === "Step requested user input via send_message." ||
      (reason !== undefined &&
        reason.startsWith("Step '") &&
        (reason.endsWith("' is waiting for user input.") ||
          reason.endsWith("' is waiting for the next user message.")));
    const parked = (function find(n: RunNode | undefined): boolean {
      if (n === undefined) {
        return false;
      }
      if (n.status === "paused" && n.completionSignal === "need_input") {
        return true;
      }
      return (n.children ?? []).some(find);
    })(state.root);
    return byReason || parked;
  },
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
  m.named.length = 0;
  m.tabs.clear();
  m.asks.length = 0;
  m.states.clear();
  m.names.clear();
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

// ---------------------------------------------------------------------------
// The name.
//
// A run's tab is opened by the SERVER, and a `tabs_changed` frame carries no
// label, so the row is built from a store cell that is still empty at that
// instant. The correction is this effect, on the same read as the dot.
// ---------------------------------------------------------------------------

describe("the tab's name is corrected when the run's state arrives", () => {
  it("renames the row once the first fetch resolves", () => {
    m.tabs.add("run:wf_name");
    trackRun("wf_name");
    // Nothing fetched yet: the factory's placeholder stands, and renaming to
    // nothing would be worse than leaving it.
    expect(m.named).toEqual([]);

    m.states.set("wf_name", { workflowId: "wf_name", runLabel: "nightly sweep" });
    storeChanged();
    expect(m.named).toEqual([{ id: "run:wf_name", name: "nightly sweep" }]);
  });

  it("renames once, not on every repaint", () => {
    // The self-write this effect makes is bounded by renameTab returning early on
    // an unchanged label. Without that it would re-enter on its own tab-set bump.
    m.tabs.add("run:wf_once");
    m.states.set("wf_once", { workflowId: "wf_once", runLabel: "sweep" });
    trackRun("wf_once");
    expect(m.named).toEqual([{ id: "run:wf_once", name: "sweep" }]);

    storeChanged();
    dockChanged();
    expect(m.named).toHaveLength(1);
  });

  it("follows a label the server later replaces", () => {
    m.tabs.add("run:wf_relabel");
    m.states.set("wf_relabel", { workflowId: "wf_relabel", workflowName: "sweep.yaml" });
    trackRun("wf_relabel");
    m.states.set("wf_relabel", {
      workflowId: "wf_relabel",
      runLabel: "nightly sweep",
      workflowName: "sweep.yaml",
    });
    storeChanged();
    expect(m.named.map((n) => n.name)).toEqual(["sweep.yaml", "nightly sweep"]);
  });

  it("renames nothing for a run whose tab is not open", () => {
    m.states.set("wf_notab_name", { workflowId: "wf_notab_name", runLabel: "sweep" });
    trackRun("wf_notab_name");
    expect(m.named).toEqual([]);
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
  it("flips to input when a PARENTLESS run's ask lands under the synthetic chat id", () => {
    m.tabs.add("run:wf_5");
    m.states.set("wf_5", { workflowId: "wf_5", status: "running" });
    trackRun("wf_5");
    m.painted.length = 0;

    m.asks.push({ chatID: "run:wf_5", runID: "wf_5" });
    dockChanged();
    expect(m.painted).toEqual([{ id: "run:wf_5", status: "input" }]);
  });

  // The half the chat-keyed predicate could never see: an agent-launched run's ask
  // is filed under the LAUNCHING chat's id, so only a scan over the payload's run
  // finds it. Before the run-scoped reader this population had no amber dot at all,
  // which is the same masking one level up from the transcript's run card.
  it("flips to input for a CHAT-PARENTED run's ask, filed under the launching chat", () => {
    m.tabs.add("run:wf_6");
    m.states.set("wf_6", { workflowId: "wf_6", status: "running" });
    trackRun("wf_6");
    m.painted.length = 0;

    m.asks.push({ chatID: "c1", runID: "wf_6" });
    dockChanged();
    expect(m.painted).toEqual([{ id: "run:wf_6", status: "input" }]);
  });

  it("goes back to the run's own status once the ask is answered", () => {
    m.tabs.add("run:wf_answered");
    m.states.set("wf_answered", { workflowId: "wf_answered", status: "running" });
    trackRun("wf_answered");
    m.asks.push({ chatID: "c1", runID: "wf_answered" });
    dockChanged();
    expect(m.painted.at(-1)).toEqual({ id: "run:wf_answered", status: "input" });

    m.asks.length = 0;
    dockChanged();
    expect(m.painted.at(-1)).toEqual({ id: "run:wf_answered", status: "working" });
  });

  it("ignores another run's ask", () => {
    m.tabs.add("run:wf_7b");
    m.states.set("wf_7b", { workflowId: "wf_7b", status: "running" });
    trackRun("wf_7b");
    m.painted.length = 0;

    m.asks.push({ chatID: "c1", runID: "wf_OTHER" });
    m.asks.push({ chatID: "run:wf_OTHER", runID: "" });
    dockChanged();
    expect(m.painted).toEqual([{ id: "run:wf_7b", status: "working" }]);
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

// ---------------------------------------------------------------------------
// The live-runs rebuild is the THIRD door into "this client knows about this
// run", and it is the only one a PAUSED run restored on boot reaches: such a run
// emits no frame at all and nothing paints its view, so without a consumer on
// the store's seam its row keeps the factory's placeholder and its dot stays
// blank for the life of the pause. Resolving the run's state is only half of
// that — a run the painter never learned about is not in the repaint at all.
// ---------------------------------------------------------------------------

describe("the live-runs rebuild reaches the dot, which a boot-restored run needs", () => {
  it("registers with the store, so the rebuild has something to report to", () => {
    expect(typeof observer).toBe("function");
  });

  it("paints and names a reported run, with no run frame and no view paint", () => {
    m.tabs.add("run:wf_boot");
    m.states.set("wf_boot", {
      workflowId: "wf_boot",
      status: "paused",
      runLabel: "nightly sweep",
    });

    observer?.("wf_boot");

    // The dot is asserted by CONTENT rather than by call count: the rename below
    // bumps the tab set, so the effect re-enters once and repaints the same status.
    // That extra pass is the documented bound, not a behaviour to pin.
    expect(m.painted).toContainEqual({ id: "run:wf_boot", status: "waiting" });
    expect(m.named).toEqual([{ id: "run:wf_boot", name: "nightly sweep" }]);
  });
});

// ---------------------------------------------------------------------------
// The COUPLING between the dot's vocabulary and the auto-close's.
//
// This file is the right home for it because it already imports the REAL
// `store.js` (it mocks `tabs.js`, `decision-dock.js` and `run-store.js`, not the
// store), so both vocabularies can be read at once against production code rather
// than against a fake.
//
// The auto-close's rule reads "green dot only" (`run-view.ts`
// `autoCloseRunSubTab`), and that only stays true while the two agree. It is
// asserted ONE-DIRECTIONALLY on purpose: an unrecognised status is green AND not a
// clean ending, which is exactly the third condition of the rule.
// ---------------------------------------------------------------------------

const { runEndedCleanly, RUN_STATUSES } = await import("./run-controls.js");
const { runStatusFor } = await import("./store.js");

describe("a clean ending is always a green dot", () => {
  // Exhaustive over the WIRE's own words rather than over whatever subset a table
  // names, plus a status this build has never seen.
  it.each([...RUN_STATUSES, "cancelled", "something-new-upstream"])(
    "agrees about %s",
    (status: string) => {
      // The implication stated as its one FORBIDDEN combination, so every case
      // asserts and a failure names both readings. A green dot that is not a clean
      // ending is legal and expected — that is `something-new-upstream`, and it is
      // the third condition of the auto-close rule doing its job.
      expect({
        status,
        clean: runEndedCleanly(status),
        green: runStatusFor(status) === "done",
      }).not.toEqual({ status, clean: true, green: false });
    },
  );

  // The vocabulary's SECOND input, and the reason the auto-close gate has to pass
  // it rather than read the status alone: an unanswered ask outranks every word on
  // this list, so no ask-bearing run is green and its tab survives whatever the
  // wire called the ending. The gate's own half of this is behavioural, in
  // `run-subtab.test.ts` — a vocabulary case cannot see the call.
  it.each([...RUN_STATUSES, "cancelled", "something-new-upstream"])(
    "reads an unanswered ask on %s as amber rather than green",
    (status: string) => {
      expect(runStatusFor(status, true)).toBe("input");
    },
  );
});

// ---------------------------------------------------------------------------
// The PARK, which is the row the liveness split added.
//
// A parked run holds no process now, so its dot is the only thing on screen that
// separates "waiting on a person" from "waiting on the network" — and the two want
// opposite things from the reader. The dock's card answers the same question when
// this client has one; the pause reason is what answers it when it does not.
// ---------------------------------------------------------------------------

describe("the dot separates a park on a PERSON from every other pause", () => {
  it("reads a needInput park as amber with no card in the dock", () => {
    expect(runStatusFor("paused", false, "need_input")).toBe("input");
  });

  it("reads every other pause as the still blue dot", () => {
    expect(runStatusFor("paused", false, "")).toBe("waiting");
  });

  // The gate, and the reason the class is consulted only in the `paused` arm: a
  // reason can outlive the pause it described, so a finished run must not be
  // painted as one waiting on somebody.
  it.each([...RUN_STATUSES.filter((s: string) => s !== "paused"), "cancelled"])(
    "ignores the pause class on %s",
    (status: string) => {
      expect(runStatusFor(status, false, "need_input")).toBe(runStatusFor(status, false, ""));
    },
  );
});

describe("a run parked on a person paints amber through the effect", () => {
  it("paints amber off the pause reason alone", () => {
    m.tabs.add("run:wf_park");
    m.states.set("wf_park", {
      workflowId: "wf_park",
      status: "paused",
      pauseReason: "Step 'review' is waiting for user input.",
    });
    trackRun("wf_park");
    // No ask was pushed into the mocked dock, which is the whole case: a client
    // that connected after the question was raised holds nothing to join.
    expect(m.asks).toEqual([]);
    expect(m.painted).toEqual([{ id: "run:wf_park", status: "input" }]);
  });

  // The arm no pause reason can reach. KAS runs a parallel branch against a shallow
  // COPY of the run state, so the branch's own sentence is written to a throwaway
  // object and the run keeps only `Parallel '<id>' is waiting on branch '<branch>'.`
  // — which fails the reason rule, so before the node-signal arm this run painted
  // the ordinary blue waiting dot and the one pause a reader has to act on was
  // indistinguishable from a network blip.
  it("paints amber for a park inside a parallel branch", () => {
    m.tabs.add("run:wf_branch");
    m.states.set("wf_branch", {
      workflowId: "wf_branch",
      status: "paused",
      pauseReason: "Parallel 'phase1' is waiting on branch 'verify'.",
      root: {
        nodeId: "root",
        type: "sequence",
        status: "paused",
        children: [
          {
            nodeId: "phase1",
            type: "parallel",
            status: "paused",
            children: [
              {
                nodeId: "verify",
                type: "step",
                status: "paused",
                completionSignal: "need_input",
              },
            ],
          },
        ],
      },
    });
    trackRun("wf_branch");
    expect(m.asks).toEqual([]);
    expect(m.painted).toEqual([{ id: "run:wf_branch", status: "input" }]);
  });

  // The negative, and it is the same wrapper sentence: KAS emits it whenever the
  // paused branch carries no detail, which covers an interruption and a permanent
  // failure as well. Amber here would tell a reader to answer a question nobody asked.
  it("paints a branch parked for another cause blue", () => {
    m.tabs.add("run:wf_branch_blip");
    m.states.set("wf_branch_blip", {
      workflowId: "wf_branch_blip",
      status: "paused",
      pauseReason: "Parallel 'phase1' is waiting on branch 'verify'.",
      root: {
        nodeId: "root",
        type: "sequence",
        status: "paused",
        children: [
          {
            nodeId: "phase1",
            type: "parallel",
            status: "paused",
            children: [{ nodeId: "verify", type: "step", status: "paused" }],
          },
        ],
      },
    });
    trackRun("wf_branch_blip");
    expect(m.painted).toEqual([{ id: "run:wf_branch_blip", status: "waiting" }]);
  });

  it("paints a transient pause blue, not amber", () => {
    m.tabs.add("run:wf_blip");
    m.states.set("wf_blip", {
      workflowId: "wf_blip",
      status: "paused",
      pauseReason: "Transient connection error (EAI_AGAIN); the run is paused and can be resumed.",
    });
    trackRun("wf_blip");
    expect(m.painted).toEqual([{ id: "run:wf_blip", status: "waiting" }]);
  });
});
