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
let liveRunsReply: { runs: { workflow_id: string; chat_id: string }[] } | null = null;

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
    expect(store.peekRunState("r1")?.status).toBe("completed");
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
    expect(store.peekRunState("r1")?.status).toBe("running");
    expect(store.peekRunState("r2")?.status).toBe("failed");
  });

  it("ignores an empty id rather than fetching /api/runs/", () => {
    store.invalidateRun("");
    expect(fetches).toEqual([]);
  });

  it("keeps the last good value when a fetch comes back empty", async () => {
    responses = [{ workflowId: "r3", status: "running" }, undefined];
    store.invalidateRun("r3");
    await settle();
    expect(store.peekRunState("r3")?.status).toBe("running");

    // A deleted run answers with no state. Blanking the cell would make a card
    // that was showing a real run flip to its loading row.
    store.invalidateRun("r3");
    await settle();
    expect(store.peekRunState("r3")?.status).toBe("running");
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
            { nodeId: "iter-0", type: "sequence", status: "completed", children: [first] },
            { nodeId: "iter-1", type: "sequence", status: "running", children: [second] },
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
// ---------------------------------------------------------------------------

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
describe("the live-runs inventory", () => {
  it("answers by chat for runs the lifecycle events fed in", () => {
    store.noteRunLive("wf-live-1", "chat-a");
    expect(store.hasLiveRunForChat("chat-a")).toBe(true);
    expect(store.hasLiveRunForChat("chat-b")).toBe(false);

    store.noteRunSettled("wf-live-1");
    expect(store.hasLiveRunForChat("chat-a")).toBe(false);
  });

  it("exempts no chat for a parentless run, and never answers for the empty chat", () => {
    store.noteRunLive("wf-parentless", "");
    expect(store.hasLiveRunForChat("")).toBe(false);
    store.noteRunSettled("wf-parentless");
  });

  it("survives the render cache dropping the run's card (forgetRun)", () => {
    // The disposed-run-card case: forgetRun is the CACHE's bound (last card
    // unmounted), and a run does not stop being live because nothing renders
    // it — the exemption must hold for a chat nobody is looking at, which is
    // exactly the chat eviction considers.
    store.noteRunLive("wf-carded", "chat-carded");
    store.forgetRun("wf-carded");
    expect(store.hasLiveRunForChat("chat-carded")).toBe(true);
    store.noteRunSettled("wf-carded");
  });

  it("rebuilds from the endpoint, replacing the event-fed view", async () => {
    // Event-fed state is stale in both directions: wf-stale settled while this
    // client was away, wf-missed started then.
    store.noteRunLive("wf-stale", "chat-stale");
    liveRunsReply = {
      runs: [
        { workflow_id: "wf-missed", chat_id: "chat-missed" },
        { workflow_id: "wf-parentless", chat_id: "" },
      ],
    };

    await store.rebuildLiveRuns();

    expect(fetches).toContain("/api/runs/live");
    expect(store.hasLiveRunForChat("chat-missed")).toBe(true);
    expect(store.hasLiveRunForChat("chat-stale")).toBe(false);
    store.noteRunSettled("wf-missed");
    store.noteRunSettled("wf-parentless");
  });

  it("KEEPS the event-fed state when the rebuild fails, and retries later", async () => {
    store.noteRunLive("wf-kept", "chat-kept");
    liveRunsReply = null; // endpoint unreachable / non-2xx / undecodable

    await store.rebuildLiveRuns();
    expect(
      store.hasLiveRunForChat("chat-kept"),
      "a failed rebuild must never clear to empty — degrade toward keeping",
    ).toBe(true);

    // The next rebuild (gap or boot) applies the server's answer.
    liveRunsReply = { runs: [] };
    await store.rebuildLiveRuns();
    expect(store.hasLiveRunForChat("chat-kept")).toBe(false);
  });
});
