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

// ---------------------------------------------------------------------------
// `run_progress` is APPLIED, not answered with a fetch. That is what removes up
// to five concurrent `GET /api/runs/{id}` round trips per burst of node events,
// each one a JSON-RPC call to KAS returning the whole state tree.
//
// The property that makes it safe is addressability: a frame names ONE execution
// by node PATH, and a repeat's iterations have distinct paths where they share a
// node id. Every write is an assignment, so KAS's duplicate frames across a
// resume cost nothing.
// ---------------------------------------------------------------------------

/** Seed a run's cached state without a fetch, by resolving one. */
async function seedRun(id: string, state: RunState): Promise<void> {
  responses = [state];
  store.invalidateRun(id);
  await settle();
  fetches.length = 0;
}

describe("applyRunProgress writes the addressed node and issues no request", () => {
  it("applies a node_start to the leaf its path names", async () => {
    await seedRun("r1", {
      workflowId: "r1",
      status: "running",
      root: { nodeId: "seq", type: "sequence", status: "running", children: [step("coder")] },
    });

    const landed = store.applyRunProgress({
      workflow_id: "r1",
      node_path: "seq/coder",
      status: "running",
      started_at: "2026-03-04T05:06:07Z",
    });

    expect(landed).toBe(true);
    expect(fetches).toHaveLength(0);
    const leaf = store.peekRunState("r1")?.root?.children?.[0];
    expect(leaf?.status).toBe("running");
    expect(leaf?.startedAt).toBe("2026-03-04T05:06:07Z");
  });

  it("finds an iteration container by its FRAME spelling, not the tree's", async () => {
    // KAS spells a repeat's per-iteration container `<repeatId>#<n>` in the state
    // tree and `iter-<n>` in a node path, so the client translates. Without that
    // a step inside a loop is unaddressable and every frame for it refetches.
    await seedRun("r1", {
      workflowId: "r1",
      status: "running",
      root: {
        nodeId: "loop",
        type: "repeat",
        status: "running",
        children: [
          {
            nodeId: "loop#0",
            type: "sequence",
            status: "completed",
            iteration: 0,
            children: [step("body", { status: "completed" })],
          },
          {
            nodeId: "loop#1",
            type: "sequence",
            status: "running",
            iteration: 1,
            children: [step("body")],
          },
        ],
      },
    });

    expect(
      store.applyRunProgress({
        workflow_id: "r1",
        node_path: "loop/iter-1/body",
        status: "running",
      }),
    ).toBe(true);
    const iters = store.peekRunState("r1")?.root?.children ?? [];
    expect(iters[1]?.children?.[0]?.status).toBe("running");
    // The first pass of the loop must be untouched — the whole point of the path.
    expect(iters[0]?.children?.[0]?.status).toBe("completed");
  });

  it("is idempotent, because KAS duplicates progress frames across a resume", async () => {
    await seedRun("r1", {
      workflowId: "r1",
      status: "running",
      root: { nodeId: "coder", type: "step", status: "pending" },
    });
    const frame = { workflow_id: "r1", node_path: "coder", status: "completed", ended_at: "T1" };
    store.applyRunProgress(frame);
    const once = store.peekRunState("r1")?.root;
    store.applyRunProgress(frame);
    expect(store.peekRunState("r1")?.root).toEqual(once);
  });

  it("keeps the fields the frame does NOT carry", async () => {
    // A `watch_poll` carries a path and no status, and a `node_complete` carries
    // no `started_at`. A frame states what changed, so an absent field must not
    // blank what node_start already left.
    await seedRun("r1", {
      workflowId: "r1",
      status: "running",
      root: { nodeId: "w", type: "watch", status: "running", startedAt: "T0" },
    });

    store.applyRunProgress({ workflow_id: "r1", node_path: "w" });
    expect(store.peekRunState("r1")?.root?.status).toBe("running");
    expect(store.peekRunState("r1")?.root?.startedAt).toBe("T0");

    store.applyRunProgress({
      workflow_id: "r1",
      node_path: "w",
      status: "completed",
      ended_at: "T9",
    });
    expect(store.peekRunState("r1")?.root?.startedAt).toBe("T0");
    expect(store.peekRunState("r1")?.root?.endedAt).toBe("T9");
  });

  it("drops a status word it does not know rather than writing it into the union", async () => {
    // The frame forwards KAS's own word as a plain string. Every renderer switches
    // on the node's status, so a new upstream word landing in the field would
    // reach those switches with no case; the next refetch carries the truth.
    await seedRun("r1", {
      workflowId: "r1",
      status: "running",
      root: { nodeId: "coder", type: "step", status: "running" },
    });
    store.applyRunProgress({ workflow_id: "r1", node_path: "coder", status: "quantum" });
    expect(store.peekRunState("r1")?.root?.status).toBe("running");
  });

  it("copies the spine rather than mutating it, so a reader's held value is stable", async () => {
    await seedRun("r1", {
      workflowId: "r1",
      status: "running",
      root: {
        nodeId: "seq",
        type: "sequence",
        status: "running",
        children: [step("a"), step("b")],
      },
    });
    const before = store.peekRunState("r1");
    const untouchedSibling = before?.root?.children?.[1];

    store.applyRunProgress({ workflow_id: "r1", node_path: "seq/a", status: "running" });

    const after = store.peekRunState("r1");
    expect(after).not.toBe(before);
    expect(before?.root?.children?.[0]?.status).toBe("pending");
    // Siblings are shared by reference: only the matched spine is rebuilt.
    expect(after?.root?.children?.[1]).toBe(untouchedSibling);
  });
});

describe("applyRunProgress refuses what it cannot express, so the caller refetches", () => {
  it("refuses a frame with no node path (loop_iteration, steps_queued, paused)", async () => {
    await seedRun("r1", {
      workflowId: "r1",
      status: "running",
      root: { nodeId: "coder", type: "step", status: "running" },
    });
    expect(store.applyRunProgress({ workflow_id: "r1" })).toBe(false);
    expect(store.applyRunProgress({ workflow_id: "r1", node_path: "" })).toBe(false);
  });

  it("refuses a run it holds no state for", () => {
    expect(store.applyRunProgress({ workflow_id: "r4", node_path: "coder" })).toBe(false);
  });

  it("refuses a path this tree does not hold, which is a freshly-created container", async () => {
    await seedRun("r1", {
      workflowId: "r1",
      status: "running",
      root: { nodeId: "seq", type: "sequence", status: "running", children: [step("a")] },
    });
    expect(store.applyRunProgress({ workflow_id: "r1", node_path: "seq/b" })).toBe(false);
    expect(store.applyRunProgress({ workflow_id: "r1", node_path: "other/a" })).toBe(false);
  });
});

describe("invalidateCachedRuns is the gap-recovery half of the push contract", () => {
  it("re-reads every run it holds, and nothing it does not", async () => {
    await seedRun("r1", { workflowId: "r1", status: "running" });
    await seedRun("r2", { workflowId: "r2", status: "running" });

    responses = [
      { workflowId: "r1", status: "completed" },
      { workflowId: "r2", status: "completed" },
    ];
    store.invalidateCachedRuns();
    await settle();

    expect(fetches).toHaveLength(2);
    expect(fetches.some((p) => p.includes("r1"))).toBe(true);
    expect(fetches.some((p) => p.includes("r2"))).toBe(true);
  });
});
