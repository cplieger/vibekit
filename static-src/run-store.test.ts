// ---------------------------------------------------------------------------
// The run store: the fetch discipline, and the derived reads over one state.
//
// Two properties carry the whole module. The COALESCING one is why it exists at
// all — KAS emits a `run_progress` per node event, so a twenty-step run produces
// dozens of invalidations and the only state that matters is the one after the
// last of them. The DERIVED reads are functions rather than stored fields on
// purpose: a second copy of "how many steps finished" is a second thing that can
// be wrong, so they are tested as arithmetic over a tree.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import type { RunNode, RunState } from "./run-store.js";

const fetches: string[] = [];
let responses: (RunState | undefined)[] = [];
let resolvers: (() => void)[] = [];
let liveRunsReply: {
  runs: { workflow_id: string; chat_id: string; executing: boolean }[];
} | null = null;

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(async (path: string) => {
    fetches.push(path);
    // A deferred resolve, so a test can invalidate again WHILE one is in flight —
    // which is the whole case the coalescing exists for.
    await new Promise<void>((r) => resolvers.push(r));
    const state = responses.shift();
    return state === undefined ? null : { workflowId: state.workflowId, state };
  }),
  // The live-runs rebuild goes through the typed GET; the decoder is the
  // generated one and is not under test here, so the mock answers typed values
  // directly (null is the degrade arm: non-2xx / network / decode failure).
  apiGetTyped: vi.fn((path: string) => {
    fetches.push(path);
    return Promise.resolve(liveRunsReply);
  }),
}));

const store = await import("./run-store.js");

/** Let every pending fetch resolve, then drain the microtask queue. */
async function settle(): Promise<void> {
  for (const r of resolvers.splice(0)) {
    r();
  }
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

function step(nodeId: string, over: Partial<RunNode> = {}): RunNode {
  return { nodeId, type: "step", status: "pending", ...over };
}

beforeEach(() => {
  fetches.length = 0;
  responses = [];
  resolvers = [];
  liveRunsReply = null;
  for (const id of ["r1", "r2", "r3", "r4"]) {
    store.forgetRun(id);
  }
});

describe("the fetch is coalesced, because a busy run invalidates dozens of times", () => {
  it("issues ONE request for a burst, then exactly one more for what arrived during it", async () => {
    responses = [
      { workflowId: "r1", status: "running" },
      { workflowId: "r1", status: "completed" },
    ];
    store.invalidateRun("r1");
    // Twelve more frames while the first request is still open.
    for (let i = 0; i < 12; i++) {
      store.invalidateRun("r1");
    }
    expect(fetches).toHaveLength(1);

    await settle();
    // The trailing fetch: the burst happened, so the answer in hand is stale.
    expect(fetches).toHaveLength(2);
    await settle();
    // And it stops there — nothing invalidated during the second one.
    expect(fetches).toHaveLength(2);
    expect(store.runState("r1")?.status).toBe("completed");
  });

  it("does not conflate two runs", async () => {
    responses = [
      { workflowId: "r1", status: "running" },
      { workflowId: "r2", status: "failed" },
    ];
    store.invalidateRun("r1");
    store.invalidateRun("r2");
    expect(fetches).toEqual(["/api/runs/r1", "/api/runs/r2"]);
    await settle();
    expect(store.runState("r1")?.status).toBe("running");
    expect(store.runState("r2")?.status).toBe("failed");
  });

  it("ignores an empty id rather than fetching /api/runs/", () => {
    store.invalidateRun("");
    expect(fetches).toEqual([]);
  });

  it("keeps the last good value when a fetch comes back empty", async () => {
    responses = [{ workflowId: "r3", status: "running" }, undefined];
    store.invalidateRun("r3");
    await settle();
    expect(store.runState("r3")?.status).toBe("running");

    // A deleted run answers with no state. Blanking the cell would make a card
    // that was showing a real run flip to its loading row.
    store.invalidateRun("r3");
    await settle();
    expect(store.runState("r3")?.status).toBe("running");
  });
});

describe("leafNodes walks to the work and skips the scaffolding", () => {
  it("returns the steps of a nested plan in plan order", () => {
    const root: RunNode = {
      nodeId: "root",
      type: "sequence",
      status: "running",
      children: [
        step("lint", { status: "completed" }),
        {
          nodeId: "loop",
          type: "repeat",
          status: "running",
          children: [
            { nodeId: "iter", type: "sequence", status: "completed", children: [step("work")] },
            { nodeId: "iter", type: "sequence", status: "running", children: [step("work")] },
          ],
        },
        step("publish"),
      ],
    };
    expect(store.leafNodes(root).map((n) => n.nodeId)).toEqual(["lint", "work", "work", "publish"]);
  });

  it("treats a childless container as a leaf, so nothing vanishes", () => {
    // A `parallel` whose branches KAS has not expanded yet has no children. It is
    // still a row a reader must see, or the plan silently shrinks.
    const root: RunNode = { nodeId: "fan", type: "parallel", status: "pending" };
    expect(store.leafNodes(root).map((n) => n.nodeId)).toEqual(["fan"]);
  });

  it("is empty for a run with no tree", () => {
    expect(store.leafNodes(undefined)).toEqual([]);
  });
});

// KAS describes one node two ways: a repeat's iteration container is
// `<repeatId>#<n>` in the state tree these fixtures reproduce and `iter-<n>` in
// the `nodePath` it stamps on a step FRAME. The frame's spelling is the key both
// row producers have to land on, so the tree is translated into it.
describe("nodePathOf separates two iterations that share a node id", () => {
  it("builds the same path the server joins into a step's subtask id", () => {
    const first = step("work");
    const second = step("work");
    const root: RunNode = {
      nodeId: "wf",
      type: "sequence",
      status: "running",
      children: [
        {
          nodeId: "loop",
          type: "repeat",
          status: "running",
          children: [
            {
              nodeId: "loop#0",
              type: "sequence",
              status: "completed",
              iteration: 0,
              children: [first],
            },
            {
              nodeId: "loop#1",
              type: "sequence",
              status: "running",
              iteration: 1,
              children: [second],
            },
          ],
        },
      ],
    };
    expect(store.nodePathOf(root, first)).toEqual(["wf", "loop", "iter-0", "work"]);
    expect(store.nodePathOf(root, second)).toEqual(["wf", "loop", "iter-1", "work"]);
  });

  it("falls back to the node id for a node that is not in the tree", () => {
    expect(store.nodePathOf(undefined, step("orphan"))).toEqual(["orphan"]);
  });

  it("falls back to a repeat child's own id when it carries no iteration", () => {
    // Every one of the 27 iteration containers on this machine's real runs carries
    // an `iteration`, so this is the unobserved branch: it must degrade to a row in
    // the wrong place rather than to `iter-undefined`, which is the same call the
    // server's own runNodePath makes for a frame with no path.
    const target = step("work");
    const root: RunNode = {
      nodeId: "wf",
      type: "repeat",
      status: "running",
      children: [{ nodeId: "loop#0", type: "sequence", status: "running", children: [target] }],
    };
    expect(store.nodePathOf(root, target)).toEqual(["wf", "loop#0", "work"]);
  });

  it("leaves a parallel BRANCH container spelled as its own id", () => {
    // Real data: a parallel's branches are named `plan-a`…`plan-d` on both sides and
    // match byte-for-byte today, so rewriting one would break a working case. The
    // rule is a repeat's, not every container's.
    const target = step("plan-a", { branchId: "plan-a" });
    const root: RunNode = {
      nodeId: "wf",
      type: "sequence",
      status: "running",
      children: [
        { nodeId: "investigate", type: "parallel", status: "running", children: [target] },
      ],
    };
    expect(store.nodePathOf(root, target)).toEqual(["wf", "investigate", "plan-a"]);
  });

  it("rewrites a step sitting DIRECTLY under a repeat", () => {
    // The rule keys on the PARENT's type, not on the node being a container, so a
    // repeat whose body is one bare step is addressed the same way KAS addresses it.
    const target = step("work", { iteration: 2 });
    const root: RunNode = {
      nodeId: "wf",
      type: "repeat",
      status: "running",
      children: [target],
    };
    expect(store.nodePathOf(root, target)).toEqual(["wf", "iter-2"]);
  });
});

// The FALLBACK above is a well-formed value and not an address: its first segment is
// a LEAF id where the endpoint asserts the run id, so a read of it is refused. What
// separates the two is `placed`, and a consumer that puts the value on the wire or
// into a focus request is required to read it — the path alone cannot say which it
// got, which is what let the value be spent as an address.
describe("nodeAddressOf reports whether the walk PLACED the target", () => {
  it("reports placed for a node the tree holds", () => {
    const target = step("work");
    const root: RunNode = {
      nodeId: "wf",
      type: "sequence",
      status: "running",
      children: [target],
    };
    expect(store.nodeAddressOf(root, target)).toEqual({ path: ["wf", "work"], placed: true });
  });

  it("reports NOT placed for a node the tree does not hold", () => {
    const root: RunNode = { nodeId: "wf", type: "sequence", status: "running", children: [] };
    expect(store.nodeAddressOf(root, step("orphan"))).toEqual({
      path: ["orphan"],
      placed: false,
    });
  });

  it("reports NOT placed when there is no tree at all", () => {
    expect(store.nodeAddressOf(undefined, step("orphan"))).toEqual({
      path: ["orphan"],
      placed: false,
    });
  });

  // The wrapper's contract did not move: a row still gets a key for an unplaceable
  // node, because "a row in the wrong place beats content that vanishes".
  it("keeps nodePathOf answering the same path either way", () => {
    const target = step("work");
    const root: RunNode = {
      nodeId: "wf",
      type: "sequence",
      status: "running",
      children: [target],
    };
    expect(store.nodePathOf(root, target)).toEqual(["wf", "work"]);
    expect(store.nodePathOf(root, step("orphan"))).toEqual(["orphan"]);
  });
});

describe("runCounters answers the header's counter", () => {
  const state = (...kids: RunNode[]): RunState => ({
    workflowId: "r1",
    root: { nodeId: "wf", type: "sequence", status: "running", children: kids },
  });

  it("names the RUNNING position, not done + 1", () => {
    // A skipped leaf would shift a `done + 1` counter, and a parallel node has
    // several in flight — so "step 3 of 5" has to mean the running one.
    const c = store.runCounters(
      state(
        step("a", { status: "completed" }),
        step("b", { status: "skipped" }),
        step("c", { status: "running" }),
        step("d"),
        step("e"),
      ),
    );
    expect(c).toEqual({ total: 5, done: 2, failed: 0, current: 3 });
  });

  it("counts a paused leaf as the current one: it is where the run is", () => {
    const c = store.runCounters(
      state(step("a", { status: "completed" }), step("b", { status: "paused" })),
    );
    expect(c.current).toBe(2);
  });

  it("reports no current step for a finished run", () => {
    const c = store.runCounters(
      state(step("a", { status: "completed" }), step("b", { status: "failed" })),
    );
    expect(c).toEqual({ total: 2, done: 1, failed: 1, current: 0 });
  });

  it("is all zeros with no tree, rather than throwing", () => {
    expect(store.runCounters(undefined)).toEqual({ total: 0, done: 0, failed: 0, current: 0 });
  });
});

describe("the clocks", () => {
  it("measures a finished span between its own stamps", () => {
    expect(store.elapsedMs("2026-01-01T00:00:00Z", "2026-01-01T00:00:12Z")).toBe(12_000);
  });

  it("reads a pending step as nothing, not as the epoch", () => {
    // Date.parse(undefined) is NaN and Date.parse("") is NaN; either arriving as a
    // number would render a step that never ran as having taken 56 years.
    expect(store.elapsedMs(undefined, undefined)).toBe(0);
    expect(store.elapsedMs("", "")).toBe(0);
    expect(store.elapsedMs("not a date", undefined)).toBe(0);
  });

  it("runs a live span to now", () => {
    const started = new Date(Date.now() - 5_000).toISOString();
    expect(store.elapsedMs(started, undefined)).toBeGreaterThanOrEqual(4_900);
  });

  it("spans the RUN from its first start to its last end", () => {
    const state: RunState = {
      workflowId: "r1",
      status: "completed",
      root: {
        nodeId: "wf",
        type: "sequence",
        status: "completed",
        children: [
          step("a", {
            status: "completed",
            startedAt: "2026-01-01T00:00:00Z",
            endedAt: "2026-01-01T00:00:30Z",
          }),
          step("b", {
            status: "completed",
            startedAt: "2026-01-01T00:00:30Z",
            endedAt: "2026-01-01T00:02:00Z",
          }),
        ],
      },
    };
    expect(store.runElapsedMs(state)).toBe(120_000);
  });

  it("runs to NOW while any leaf is still going, whatever the others ended at", () => {
    const state: RunState = {
      workflowId: "r1",
      status: "running",
      root: {
        nodeId: "wf",
        type: "sequence",
        status: "running",
        children: [
          step("a", {
            status: "completed",
            startedAt: new Date(Date.now() - 60_000).toISOString(),
            endedAt: new Date(Date.now() - 50_000).toISOString(),
          }),
          step("b", { status: "running", startedAt: new Date(Date.now() - 50_000).toISOString() }),
        ],
      },
    };
    expect(store.runElapsedMs(state)).toBeGreaterThanOrEqual(59_000);
  });
});

describe("runIsLive counts a pause as live", () => {
  it("is true while running or paused, false once terminal", () => {
    // Built one at a time rather than spread from a list, because
    // `exactOptionalPropertyTypes` refuses `status: undefined` as a property
    // value: an absent status and a status whose value is undefined are different
    // things to this compiler, and the absent one is what a run mid-launch has.
    expect(store.runIsLive({ workflowId: "r1", status: "running" })).toBe(true);
    expect(store.runIsLive({ workflowId: "r1", status: "paused" })).toBe(true);
    expect(store.runIsLive({ workflowId: "r1", status: "completed" })).toBe(false);
    expect(store.runIsLive({ workflowId: "r1", status: "failed" })).toBe(false);
    expect(store.runIsLive({ workflowId: "r1", status: "aborted" })).toBe(false);
    expect(store.runIsLive({ workflowId: "r1" })).toBe(false);
    expect(store.runIsLive(undefined)).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// isNeedInputPause is tested in `run-store-pause.node.test.ts`, NOT here.
//
// The rule exists in Go as well (`needInputPause`, internal/agent/run_ask.go) and
// neither copy can go, so the cases live in ONE shared fixture that both languages
// read — `internal/agent/testdata/need_input_pauses.json`, on turn_outcomes.json's
// pattern. A table here would be a third copy of the same list, which is the
// duplication the fixture exists to remove, and it could not read the fixture
// anyway: this file runs in the browser project and the fixture is a disk read.
//
// isNeedInputPark IS here, because it is a different question and takes no fixture:
// it composes that reason rule with the node tree, and the tree half has no Go twin
// to share a table with (the server's arm decides which node to ADDRESS, this one
// only whether a person is owed an answer).
// ---------------------------------------------------------------------------
describe("isNeedInputPark answers over the reason AND the node tree", () => {
  // The plain-step park: KAS writes the matching sentence on the run itself.
  const byReason: RunState = {
    workflowId: "r1",
    status: "paused",
    pauseReason: "Step requested user input via send_message.",
    root: { nodeId: "root", type: "sequence", status: "paused" },
  };

  // The parallel-branch park, verbatim from KAS's executeParallel: the branch runs
  // against a shallow COPY of the run state, so its own sentence is written to a
  // throwaway object and the run keeps only this wrapper.
  const branch = (signal?: RunNode["completionSignal"]): RunState => ({
    workflowId: "r2",
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
              ...(signal === undefined ? {} : { completionSignal: signal }),
            },
          ],
        },
      ],
    },
  });

  it("recognises a plain step's park from the reason", () => {
    expect(store.isNeedInputPark(byReason)).toBe(true);
  });

  // The arm the dot exists for and the reason could never reach: without it a branch
  // parked on a person paints the ordinary blue waiting dot, so the one pause a
  // reader has to act on is indistinguishable from a network blip.
  it("recognises a park inside a parallel branch from the node's own signal", () => {
    expect(store.isNeedInputPark(branch("need_input"))).toBe(true);
  });

  // The negative that keeps the arm honest. KAS emits that SAME wrapper sentence for
  // an interruption and a permanent failure — pauseDetail is withheld for exactly
  // those kinds — so a predicate widened to the sentence would claim a person is
  // owed an answer for a run that only needs a resume.
  it("does not fire on a branch parked for any other cause", () => {
    expect(store.isNeedInputPark(branch(undefined))).toBe(false);
    expect(store.isNeedInputPark(branch("error"))).toBe(false);
  });

  // Gated on `paused`, like the dot vocabulary's own arm: a signal outliving its
  // pause must never paint a finished run as awaiting input.
  it("withholds it for a run that is no longer paused", () => {
    expect(store.isNeedInputPark({ ...branch("need_input"), status: "completed" })).toBe(false);
    expect(store.isNeedInputPark({ ...byReason, status: "running" })).toBe(false);
  });

  it("answers false for a run this client has not fetched", () => {
    expect(store.isNeedInputPark(undefined)).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// noteRunChat: which chat's agent launched a run.
// ---------------------------------------------------------------------------
describe("noteRunChat refuses the two spellings of 'no launching chat'", () => {
  it("records a real chat id", () => {
    store.noteRunChat("wf-parented", "chat-7");
    expect(store.runChatID("wf-parented")).toBe("chat-7");
  });

  it("refuses the synthetic run key, which is a surface rather than a chat", () => {
    // A parentless run's LIFECYCLE frames carry an empty envelope chat id, but its
    // ASKS are keyed to `run:<workflowId>` because the dock queues per chat. Recorded
    // as a launching chat, it nests the run's tab under a conversation that does not
    // exist — and `runChatID`'s callers cannot tell a real id from a synthetic one.
    store.noteRunChat("wf-parentless", "run:wf-parentless");
    expect(store.runChatID("wf-parentless")).toBe("");
  });

  it("refuses an empty chat id, the other spelling of parentless", () => {
    store.noteRunChat("wf-empty", "");
    expect(store.runChatID("wf-empty")).toBe("");
  });
});

// ---------------------------------------------------------------------------
// The live-runs inventory: the eviction sweep's exemption source. Event-fed,
// rebuilt from GET /api/runs/live, and degrading toward KEEPING — a stale
// exemption costs memory, a wrongly-evicted live chat costs correctness.
// Every case uses its own ids: the inventory is module state, like the runs it
// describes.
// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// runLabelOf: what a run is CALLED.
//
// A precedence over cached state, and it lives here because two readers render a
// tab row from it — the tab factory, which turns "" into its own placeholder, and
// the per-run effect that corrects a row built before the run's first fetch
// resolved. A second copy of the order is a second thing that can disagree about
// what a tab is called.
// ---------------------------------------------------------------------------

describe("runLabelOf", () => {
  it("prefers the launcher's label for THIS execution over the recipe's name", async () => {
    responses = [{ workflowId: "r1", runLabel: "nightly sweep", workflowName: "sweep.yaml" }];
    store.invalidateRun("r1");
    await settle();
    expect(store.runLabelOf("r1")).toBe("nightly sweep");
  });

  it("falls back to the recipe's name when the launcher gave none", async () => {
    responses = [{ workflowId: "r2", workflowName: "sweep.yaml" }];
    store.invalidateRun("r2");
    await settle();
    expect(store.runLabelOf("r2")).toBe("sweep.yaml");
  });

  // The state at the instant the server's own tab offer arrives: the tab exists and
  // nothing has been fetched for the run yet. "" rather than a placeholder, because
  // which placeholder to show is the tab layer's decision, not the store's.
  it("answers empty for a run nothing has been fetched for", () => {
    expect(store.runLabelOf("r3")).toBe("");
  });

  it("answers empty for a run whose state carries neither name", async () => {
    responses = [{ workflowId: "r4", status: "running" }];
    store.invalidateRun("r4");
    await settle();
    expect(store.runLabelOf("r4")).toBe("");
  });
});

describe("the live-runs inventory", () => {
  it("answers by chat for runs the lifecycle events fed in", () => {
    store.noteRunLive("wf-live-1", "chat-a", true);
    expect(store.hasExecutingRunForChat("chat-a")).toBe(true);
    expect(store.hasExecutingRunForChat("chat-b")).toBe(false);

    store.noteRunSettled("wf-live-1");
    expect(store.hasExecutingRunForChat("chat-a")).toBe(false);
  });

  // The narrowing Stage 2 exists for, and it is the whole reason the row carries
  // two facts. A needInput park can sit for hours writing nothing into the
  // transcript, so the eviction exemption must lapse — while the run stays in the
  // inventory, because the dot painter and the tab-parent resolver still need it.
  it("stops exempting a chat whose run parked, and keeps the run in the inventory", () => {
    store.noteRunLive("wf-parked", "chat-parked", true);
    expect(store.hasExecutingRunForChat("chat-parked")).toBe(true);

    store.noteRunLive("wf-parked", "chat-parked", false);

    expect(
      store.hasExecutingRunForChat("chat-parked"),
      "a parked run writes nothing into its chat, so it must not pin that window",
    ).toBe(false);
    expect(
      store.hasLiveRunForChat("chat-parked"),
      "the row must survive: the dot painter and the ask sweep both still need it",
    ).toBe(true);
    store.noteRunSettled("wf-parked");
    expect(store.hasLiveRunForChat("chat-parked")).toBe(false);
  });

  it("exempts no chat for a parentless run, and never answers for the empty chat", () => {
    store.noteRunLive("wf-parentless", "", true);
    expect(store.hasExecutingRunForChat("")).toBe(false);
    expect(store.hasLiveRunForChat("")).toBe(false);
    store.noteRunSettled("wf-parentless");
  });

  it("survives the render cache dropping the run's card (forgetRun)", () => {
    // The disposed-run-card case: forgetRun is the CACHE's bound (last card
    // unmounted), and a run does not stop being live because nothing renders
    // it — the exemption must hold for a chat nobody is looking at, which is
    // exactly the chat eviction considers.
    store.noteRunLive("wf-carded", "chat-carded", true);
    store.forgetRun("wf-carded");
    expect(store.hasExecutingRunForChat("chat-carded")).toBe(true);
    store.noteRunSettled("wf-carded");
  });

  it("rebuilds from the endpoint, replacing the event-fed view", async () => {
    // Event-fed state is stale in both directions: wf-stale settled while this
    // client was away, wf-missed started then.
    store.noteRunLive("wf-stale", "chat-stale", true);
    liveRunsReply = {
      runs: [
        { workflow_id: "wf-missed", chat_id: "chat-missed", executing: true },
        { workflow_id: "wf-parentless", chat_id: "", executing: true },
      ],
    };

    await store.rebuildLiveRuns();

    expect(fetches).toContain("/api/runs/live");
    expect(store.hasExecutingRunForChat("chat-missed")).toBe(true);
    expect(store.hasExecutingRunForChat("chat-stale")).toBe(false);
    store.noteRunSettled("wf-missed");
    store.noteRunSettled("wf-parentless");
  });

  // The endpoint's own answer for a parked run, which is the case a boot lands in:
  // a run paused across a reload emits no frames at all, so the rebuild is the only
  // thing that can say whether its chat is still being written to.
  it("adopts the endpoint's executing verdict, exempting no chat for a parked run", async () => {
    liveRunsReply = {
      runs: [{ workflow_id: "wf-boot-parked", chat_id: "chat-boot", executing: false }],
    };

    await store.rebuildLiveRuns();

    expect(store.hasExecutingRunForChat("chat-boot")).toBe(false);
    expect(
      store.hasLiveRunForChat("chat-boot"),
      "the run is still live, so the ask sweep must still see it",
    ).toBe(true);
    expect(
      store.runChatID("wf-boot-parked"),
      "a parked run's tab still nests under the chat that launched it",
    ).toBe("chat-boot");
    store.noteRunSettled("wf-boot-parked");
  });

  // The half that was dropped on the floor. This endpoint is the only place the
  // (run, launching chat) pairing arrives outside an SSE frame, so without the seed
  // `runChatID` answered "" for every live run after a reload — and on a run whose
  // step takes twenty minutes there is no frame to correct it, so the transcript
  // card's link and a `/run/{id}` deep link both opened the tab at the end of the
  // strip rather than beside the conversation.
  it("seeds which chat launched each live run, not just that it is live", async () => {
    liveRunsReply = {
      runs: [
        { workflow_id: "wf-reloaded", chat_id: "chat-reloaded", executing: true },
        { workflow_id: "wf-scheduled", chat_id: "", executing: true },
      ],
    };

    await store.rebuildLiveRuns();

    expect(store.runChatID("wf-reloaded")).toBe("chat-reloaded");
    // A parentless run has no launching chat, so there is nothing to seed and
    // nothing for a tab to nest under.
    expect(store.runChatID("wf-scheduled")).toBe("");
    store.noteRunSettled("wf-reloaded");
    store.noteRunSettled("wf-scheduled");
  });

  // The other half a seeded pairing does not cover: a tab row's NAME comes from
  // the run's own state, which nothing else fetches for a run this client saw no
  // frames for — a PAUSED run emits none at all, so its row kept the factory's
  // placeholder until a reader opened the run view.
  it("resolves each live run's cell, and reports each to the painter", async () => {
    const reported: string[] = [];
    store.registerLiveRunObserver((id) => reported.push(id));
    liveRunsReply = {
      runs: [
        { workflow_id: "r1", chat_id: "chat-a", executing: true },
        { workflow_id: "r2", chat_id: "", executing: true },
      ],
    };
    responses = [
      { workflowId: "r1", runLabel: "nightly sweep" },
      { workflowId: "r2", workflowName: "sweep.yaml" },
    ];

    await store.rebuildLiveRuns();
    expect(fetches).toEqual(["/api/runs/live", "/api/runs/r1", "/api/runs/r2"]);
    expect(reported).toEqual(["r1", "r2"]);

    await settle();
    expect(store.runLabelOf("r1")).toBe("nightly sweep");
    expect(store.runLabelOf("r2")).toBe("sweep.yaml");
    store.noteRunSettled("r1");
    store.noteRunSettled("r2");
  });

  it("KEEPS the event-fed state when the rebuild fails, and retries later", async () => {
    store.noteRunLive("wf-kept", "chat-kept", true);
    liveRunsReply = null; // endpoint unreachable / non-2xx / undecodable

    await store.rebuildLiveRuns();
    expect(
      store.hasExecutingRunForChat("chat-kept"),
      "a failed rebuild must never clear to empty — degrade toward keeping",
    ).toBe(true);

    // The next rebuild (gap or boot) applies the server's answer.
    liveRunsReply = { runs: [] };
    await store.rebuildLiveRuns();
    expect(store.hasExecutingRunForChat("chat-kept")).toBe(false);
  });
});
