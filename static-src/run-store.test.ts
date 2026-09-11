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

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type { RunNode, RunState } from "./run-store.js";
import type { RunControlsResponse } from "./wire/types.gen.js";

const fetches: string[] = [];
// Deliberately looser than `RunState`: `status` stays a bare string so a case can
// spell an off-enum word an engine ahead of this build would send, and `root` stays
// `unknown` so a case can spell a malformed tree.
let responses: (
  | {
      workflowId: string;
      status?: string;
      root?: unknown;
      runLabel?: string;
      workflowName?: string;
    }
  | undefined
)[] = [];
let resolvers: (() => void)[] = [];
// The HTTP status each FAILED read answers with, in order. 502 by default — a read that
// never reached the engine, which is what every case predating the status means by an
// absent response, and the arm that keeps the retry ladder.
let failStatuses: number[] = [];
let liveRunsReply: {
  runs: { workflow_id: string; chat_id: string; executing: boolean }[];
} | null = null;
let controlsReplies: RunControlsResponse[] = [];

vi.mock("./api-client.js", () => ({
  // The OrError variant, because the store reads a failed read's STATUS: the collapsing
  // `apiGet` answers null for a settled 404 and a dead network alike.
  apiGetOrError: vi.fn(async (path: string) => {
    fetches.push(path);
    // A deferred resolve, so a test can invalidate again WHILE one is in flight —
    // which is the whole case the coalescing exists for.
    await new Promise<void>((r) => resolvers.push(r));
    const state = responses.shift();
    if (state === undefined) {
      return { ok: false, status: failStatuses.shift() ?? 502, data: null, error: "" };
    }
    return {
      ok: true,
      status: 200,
      data: { workflowId: state.workflowId, state },
      error: "",
    };
  }),
  // The live-runs rebuild and the affordance both go through the typed GET; the
  // decoder is the generated one and is not under test here, so the mock answers
  // typed values directly (null is the degrade arm: non-2xx / network / decode
  // failure).
  apiGetTyped: vi.fn(async (path: string) => {
    fetches.push(path);
    if (!path.endsWith("/controls")) {
      return liveRunsReply;
    }
    // Deferred like the state fetch above, so a test can invalidate the
    // affordance again WHILE one read is open — the run that ends inside the
    // tab-open read's window, which is the one moment its answer changes.
    await new Promise<void>((r) => resolvers.push(r));
    return controlsReplies.shift() ?? null;
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
  failStatuses = [];
  liveRunsReply = null;
  controlsReplies = [];
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

  it("classifies unknown run and node statuses once at the fetch boundary", async () => {
    responses = [
      {
        workflowId: "r4",
        status: "quiesced",
        root: { nodeId: "future", type: "step", status: "blocked" },
      },
    ];
    store.invalidateRun("r4");
    await settle();
    expect(store.peekRunState("r4")?.status).toBe("unknown");
    expect(store.peekRunState("r4")?.root?.status).toBe("unknown");
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

// The CAUSE token. A transport gap invalidates every cached run and then, a
// network round trip later, invalidates every LIVE run again from the rebuild's
// answer — so at 7 live runs the gap cost 14 of its 24 requests. The token says
// the two are the same event, and the guard has to hold however the pair
// interleaves, because which response lands first is not something the client
// controls.
describe("one cause costs one request per run", () => {
  it("fetches once when the second invalidation lands AFTER the first answered", async () => {
    responses = [{ workflowId: "r1", status: "running" }];

    store.invalidateRun("r1", "gap:1");
    await settle();
    expect(fetches).toEqual(["/api/runs/r1"]);

    // This is the interleaving an in-flight-only guard misses: `rebuildLiveRuns`
    // awaits `/api/runs/live` first, so its invalidation can arrive after the
    // per-run read it would be duplicating has already come back.
    store.invalidateRun("r1", "gap:1");
    await settle();
    expect(fetches).toEqual(["/api/runs/r1"]);
  });

  it("fetches once when the second lands WHILE the first is still open", async () => {
    responses = [{ workflowId: "r1", status: "running" }];

    store.invalidateRun("r1", "gap:1");
    store.invalidateRun("r1", "gap:1");
    expect(fetches).toHaveLength(1);

    await settle();
    // No trailing fetch either: the read in flight was already answering for this
    // cause, so there is nothing left to be stale about.
    expect(fetches).toEqual(["/api/runs/r1"]);
  });

  it("still fetches for a DIFFERENT cause, or a second gap would be swallowed", async () => {
    responses = [
      { workflowId: "r1", status: "running" },
      { workflowId: "r1", status: "completed" },
    ];

    store.invalidateRun("r1", "gap:1");
    await settle();
    store.invalidateRun("r1", "gap:2");
    await settle();

    expect(fetches).toEqual(["/api/runs/r1", "/api/runs/r1"]);
    expect(store.runState("r1")?.status).toBe("completed");
  });

  it("still fetches for an UNCAUSED invalidation, because an SSE frame is its own cause", async () => {
    responses = [
      { workflowId: "r1", status: "running" },
      { workflowId: "r1", status: "completed" },
    ];

    store.invalidateRun("r1", "gap:1");
    await settle();
    store.invalidateRun("r1");
    await settle();

    expect(fetches).toEqual(["/api/runs/r1", "/api/runs/r1"]);
  });

  it("claims nothing when the read FAILED, so the same cause retries", async () => {
    // A cause recorded over an answer nobody got would make the gap's own recovery
    // a no-op — the one direction this guard must not fail in.
    responses = [undefined, { workflowId: "r1", status: "running" }];

    store.invalidateRun("r1", "gap:1");
    await settle();
    store.invalidateRun("r1", "gap:1");
    await settle();

    expect(fetches).toEqual(["/api/runs/r1", "/api/runs/r1"]);
    expect(store.runState("r1")?.status).toBe("running");
  });

  it("threads the gap's token through BOTH of its readers, so each run is read once", async () => {
    // The production pair: `invalidateCachedRuns` over what is cached, then
    // `rebuildLiveRuns` over what the server says is live, on one token.
    responses = [
      { workflowId: "r1", status: "running" },
      { workflowId: "r2", status: "running" },
    ];
    store.invalidateRun("r1");
    store.invalidateRun("r2");
    await settle();
    fetches.length = 0;

    responses = [
      { workflowId: "r1", status: "completed" },
      { workflowId: "r2", status: "completed" },
    ];
    liveRunsReply = {
      runs: [
        { workflow_id: "r1", chat_id: "c1", executing: true },
        { workflow_id: "r2", chat_id: "c1", executing: true },
      ],
    };

    const cause = "gap:7";
    store.invalidateCachedRuns(cause);
    const rebuilt = store.rebuildLiveRuns(cause);
    await settle();
    await rebuilt;
    await settle();

    expect(fetches.filter((p) => p === "/api/runs/r1")).toHaveLength(1);
    expect(fetches.filter((p) => p === "/api/runs/r2")).toHaveLength(1);
  });

  it("forgets the claim with the run, so a re-tracked run is read again", async () => {
    responses = [{ workflowId: "r1", status: "running" }];
    store.invalidateRun("r1", "gap:1");
    await settle();

    store.forgetRun("r1");
    responses = [{ workflowId: "r1", status: "completed" }];
    store.invalidateRun("r1", "gap:1");
    await settle();

    expect(fetches).toEqual(["/api/runs/r1", "/api/runs/r1"]);
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

  it("counts an unknown leaf as current rather than finished or not started", () => {
    const c = store.runCounters(state(step("a", { status: "unknown" })));
    expect(c).toEqual({ total: 1, done: 0, failed: 0, current: 1 });
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
    expect(store.runIsLive({ workflowId: "r1", status: "unknown" })).toBe(true);
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

  // The ROW reader C2's floor is built on. `foldRuns` iterates rows and takes the
  // inventory's `executing` where the state cell is absent, so it needs the whole
  // row AND the workflow id — and the id is the map's KEY rather than a field, so a
  // reader handed the row alone cannot name the run it describes.
  it("answers a row per matching run, carrying the id the map holds it under", () => {
    store.noteRunLive("wf-a", "chat-x", true);
    store.noteRunLive("wf-b", "chat-x", false);
    store.noteRunLive("wf-c", "chat-y", true);

    const rows = store.liveRunsForChat("chat-x");

    // Sorted, because the answer's ORDER is the map's insertion order and no
    // consumer depends on it — asserting it would pin a fact nothing reads.
    expect([...rows].sort((a, b) => a.id.localeCompare(b.id))).toEqual([
      { id: "wf-a", chat: "chat-x", executing: true },
      { id: "wf-b", chat: "chat-x", executing: false },
    ]);
    expect(store.liveRunsForChat("chat-y"), "the other chat's own row").toEqual([
      { id: "wf-c", chat: "chat-y", executing: true },
    ]);
    expect(store.liveRunsForChat("chat-none"), "a chat with no run").toEqual([]);
    expect(store.liveRunsForChat(""), "the empty chat is never a subject").toEqual([]);
  });

  it("answers the same ids through the ids-only wrapper", () => {
    // Three callers ask only how many or which (`run-bar.ts` twice,
    // `chat-settled.ts`'s `.length`), and the wrapper exists so they do not each
    // map the rows themselves. It reads THROUGH the row reader, so the two cannot
    // disagree about which runs belong to a chat.
    store.noteRunLive("wf-a", "chat-x", true);
    store.noteRunLive("wf-b", "chat-x", false);
    store.noteRunLive("wf-c", "chat-y", true);

    expect([...store.liveRunIDsForChat("chat-x")].sort()).toEqual(["wf-a", "wf-b"]);
    expect(store.liveRunIDsForChat("chat-y")).toEqual(["wf-c"]);
    expect(store.liveRunIDsForChat("chat-none")).toEqual([]);
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

  // A frame that moves nothing must cost nothing. The store's value is what every
  // reader watches and it dedups by IDENTITY, so handing back a new object for an
  // unchanged tree wakes every subscriber to repaint the same pixels.
  //
  // `watch_poll` is the frame that made this reachable — it re-states `running` on a
  // node already running, once per poll interval for the life of a watch — and a
  // duplicate frame across a KAS resume is the other. Asserted through the state's
  // identity rather than a render count, because identity is the thing the
  // subscribers key on.
  it("does not reassign the state for a frame that moves nothing", async () => {
    await seedRun("r1", {
      workflowId: "r1",
      status: "running",
      root: {
        nodeId: "seq",
        type: "sequence",
        status: "running",
        children: [{ nodeId: "w", type: "watch", status: "running", startedAt: "T0" }],
      },
    });
    const before = store.peekRunState("r1");

    // The watch_poll shape: the node's path and the status it already holds.
    const landed = store.applyRunProgress({
      workflow_id: "r1",
      node_path: "seq/w",
      status: "running",
    });

    // LANDED, so the caller must not refetch — "nothing changed" is not "I could
    // not apply this", and conflating them would put the HTTP round trip back on
    // every poll.
    expect(landed).toBe(true);
    expect(store.peekRunState("r1")).toBe(before);
  });

  // The same claim one level up: an unchanged leaf must not rebuild the spine
  // above it either, or the root identity changes and the saving is lost.
  it("leaves the spine alone when the addressed leaf did not move", async () => {
    await seedRun("r1", {
      workflowId: "r1",
      status: "running",
      root: {
        nodeId: "seq",
        type: "sequence",
        status: "running",
        children: [{ nodeId: "w", type: "watch", status: "running" }, step("b")],
      },
    });
    const root = store.peekRunState("r1")?.root;

    store.applyRunProgress({ workflow_id: "r1", node_path: "seq/w", status: "running" });

    expect(store.peekRunState("r1")?.root).toBe(root);
  });

  // And the guard must not swallow a real change. A frame carrying a field the node
  // does not hold is a change, however small.
  it("still reassigns when the frame moves one field", async () => {
    await seedRun("r1", {
      workflowId: "r1",
      status: "running",
      root: { nodeId: "w", type: "watch", status: "running" },
    });
    const before = store.peekRunState("r1");

    store.applyRunProgress({
      workflow_id: "r1",
      node_path: "w",
      status: "running",
      started_at: "T1",
    });

    expect(store.peekRunState("r1")).not.toBe(before);
    expect(store.peekRunState("r1")?.root?.startedAt).toBe("T1");
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

// The affordance is a SECOND cell on its own clock: the state is re-read on every
// gap and shape change, while what a run offers turns over only when it reaches a
// terminal status. Two triggers ask for it — a tab opening and that run's own
// `run_finished` — and they can land together, which is the whole subject here.
describe("the affordance cell coalesces like the state cell, trailing fetch included", () => {
  const live: RunControlsResponse = {
    verbs: ["pause", "cancel"],
    refused: {},
    parent_chat_id: "",
  };
  const ended: RunControlsResponse = { verbs: ["retry"], refused: {}, parent_chat_id: "" };

  /** The controls requests issued so far. `fetches` also holds state reads. */
  function controlsFetches(): string[] {
    return fetches.filter((p) => p.endsWith("/controls"));
  }

  // THE DEFECT. The in-flight guard dropped a coincident call and scheduled
  // nothing, so a run that ENDED inside the tab-open read's window kept the
  // pre-terminal row — Pause and Cancel on a run that had already aborted — with
  // no trigger left to re-ask for the tab's lifetime.
  it("re-asks for a run that ended while the tab-open read was still open", async () => {
    controlsReplies = [live, ended];
    store.invalidateRunControls("r1"); // the tab opening
    store.invalidateRunControls("r1"); // run_finished, inside that read's window
    expect(controlsFetches()).toHaveLength(1);

    await settle();
    expect(controlsFetches()).toHaveLength(2);
    await settle();
    expect(store.runControls("r1")?.verbs).toEqual(["retry"]);
  });

  it("asks once when nothing coincided, rather than answering the same question twice", async () => {
    controlsReplies = [live];
    store.invalidateRunControls("r1");
    await settle();
    await settle();

    expect(controlsFetches()).toHaveLength(1);
    expect(store.runControls("r1")?.verbs).toEqual(["pause", "cancel"]);
  });

  it("does not conflate two runs", async () => {
    controlsReplies = [live, ended];
    store.invalidateRunControls("r1");
    store.invalidateRunControls("r2");
    expect(controlsFetches()).toHaveLength(2);
    await settle();

    expect(store.runControls("r1")?.verbs).toEqual(["pause", "cancel"]);
    expect(store.runControls("r2")?.verbs).toEqual(["retry"]);
  });

  // A failed read leaves the previous answer standing: degrading to the last known
  // row beats blanking the controls under a reader about to use them.
  it("keeps the last good answer when a read comes back empty", async () => {
    controlsReplies = [live];
    store.invalidateRunControls("r1");
    await settle();
    await settle();

    store.invalidateRunControls("r1"); // nothing left in the queue, so null
    await settle();
    expect(store.runControls("r1")?.verbs).toEqual(["pause", "cancel"]);
  });

  it("ignores an empty id rather than fetching /api/runs//controls", () => {
    store.invalidateRunControls("");
    expect(controlsFetches()).toEqual([]);
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

// ---------------------------------------------------------------------------
// The retry ladder behind a read that produced nothing.
//
// The window it covers is the one nothing else revisits: a `run_progress` frame is
// answered by ONE read, so a read that came back empty leaves the card sitting on
// whatever it last showed until the next frame — and a paused run emits none at all.
// Bounded because a failed read is not always transient: the run endpoint answers 503
// for an engine with no workflow support, which no number of attempts can move.
//
// SKIPPED outright for one status. `handleRun` grades a failed inspect three ways, and a
// 404 is the narrow arm where the engine answered ABOUT this run and refused — so the
// answer is the same however often it is asked, and a run the server has forgotten costs
// one read per event instead of four.
//
// Fake timers are installed per case; the module is shared with the rest of this file,
// so the ladder is dropped by `beforeEach`'s `forgetRun`.
// ---------------------------------------------------------------------------

describe("the retry ladder behind a run read that produced nothing", () => {
  afterEach(async () => {
    // Drain anything still in flight before handing the clock back: `forgetRun` does not
    // clear the in-flight guard, so a straggling read would decide the next case rather
    // than this one.
    await settle();
    vi.useRealTimers();
  });

  it("climbs three rungs at a doubling delay, then stops and says so", async () => {
    vi.useFakeTimers();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    responses = [undefined, undefined, undefined, undefined];

    store.invalidateRun("r1");
    await settle();
    expect(fetches).toHaveLength(1);

    // Each rung is bracketed, because a flat delay produces the same COUNTS: what
    // separates the two is that nothing is due one millisecond before the doubled delay.
    await vi.advanceTimersByTimeAsync(999);
    expect(fetches).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetches).toHaveLength(2);
    await settle();

    await vi.advanceTimersByTimeAsync(1999);
    expect(fetches).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetches).toHaveLength(3);
    await settle();

    await vi.advanceTimersByTimeAsync(3999);
    expect(fetches).toHaveLength(3);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetches).toHaveLength(4);
    await settle();

    // Bounded: a run the server will never describe stops being asked about rather than
    // being polled for the life of the document.
    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetches).toHaveLength(4);
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("arms NOTHING and says nothing for a run the server has settled", async () => {
    // The whole point of reading the status: before it, a run the server had permanently
    // forgotten cost three retries and a warn per transport gap — four reads for an
    // answer that cannot change.
    vi.useFakeTimers();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    responses = [undefined];
    failStatuses = [404];

    store.invalidateRun("r1");
    await settle();
    expect(fetches).toHaveLength(1);

    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetches).toHaveLength(1);
    expect(warn).not.toHaveBeenCalled();
  });

  it("still climbs for a 503, and the line it gives up on names that status", async () => {
    // The skip is narrow to ONE status rather than to any failure carrying one: a 503 is
    // an engine with no workflow verbs, which says nothing about whether this run exists,
    // so it keeps the bounded ladder it is the reason for.
    vi.useFakeTimers();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    responses = [undefined, undefined, undefined, undefined];
    failStatuses = [503, 503, 503, 503];

    store.invalidateRun("r1");
    await settle();
    await vi.advanceTimersByTimeAsync(1000);
    await settle();
    await vi.advanceTimersByTimeAsync(2000);
    await settle();
    await vi.advanceTimersByTimeAsync(4000);
    await settle();
    expect(fetches).toHaveLength(4);

    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0]?.[0]).toContain("503");
  });

  it("drops a rung a transient left ARMED once the server settles", async () => {
    // The coalescing pair, the first half failing transiently and the trailing one
    // settling: read 1's rung is armed while read 2 runs, so the skip has to CANCEL it
    // rather than merely decline to arm — otherwise the rung fires and fetches an answer
    // this read already has.
    vi.useFakeTimers();
    responses = [undefined, undefined];
    failStatuses = [502, 404];

    store.invalidateRun("r1");
    store.invalidateRun("r1");
    await settle();
    await settle();
    expect(fetches).toHaveLength(2);

    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetches).toHaveLength(2);
  });

  it("re-enters under the read's OWN cause, so the gap's other reader is not charged again", async () => {
    // `""` would fetch too, and it is the wrong token: the cause the failed read dropped
    // is what the rung answers for, so the gap's second reader — a round trip behind the
    // first — finds the question already asked. With `""` the rung would claim nothing and
    // that reader would issue a third request for one event.
    vi.useFakeTimers();
    responses = [undefined, { workflowId: "r1", status: "running" }];

    store.invalidateRun("r1", "gap:1");
    await settle();
    await vi.advanceTimersByTimeAsync(1000);
    await settle();
    expect(fetches).toHaveLength(2);
    expect(store.runState("r1")?.status).toBe("running");

    store.invalidateRun("r1", "gap:1");
    await settle();
    expect(fetches).toHaveLength(2);
  });

  it("is answered by a read that lands anywhere, not only by its own rung", async () => {
    // The next SSE frame normally beats the ladder to it, and a read that ANSWERED leaves
    // nothing to retry.
    vi.useFakeTimers();
    responses = [undefined, { workflowId: "r1", status: "running" }];

    store.invalidateRun("r1");
    await settle();
    store.invalidateRun("r1");
    await settle();
    expect(fetches).toHaveLength(2);

    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetches).toHaveLength(2);
  });

  it("lets a trailing read REPLACE the armed rung rather than leaving one beside it", async () => {
    // The coalescing pair, both halves failing: the first read arms a rung and the
    // trailing one then fails too. The newest failure owns the rung, and it inherits the
    // count — two armed timers would fetch twice per rung and widen the ladder.
    vi.useFakeTimers();
    responses = [undefined, undefined, undefined];

    store.invalidateRun("r1");
    store.invalidateRun("r1");
    await settle();
    await settle();
    expect(fetches).toHaveLength(2);

    // The first read's own rung was due here, and went with it.
    await vi.advanceTimersByTimeAsync(1000);
    expect(fetches).toHaveLength(2);
    // The replacement's, at the second rung's delay because the count carried over.
    await vi.advanceTimersByTimeAsync(1000);
    expect(fetches).toHaveLength(3);
  });

  it("is dropped with the run, so a rung cannot fetch for a card nothing renders", async () => {
    vi.useFakeTimers();
    responses = [undefined];

    store.invalidateRun("r2");
    await settle();
    expect(fetches).toHaveLength(1);

    store.forgetRun("r2");
    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetches).toHaveLength(1);
  });
});

// ---------------------------------------------------------------------------
// The cache's bound is a REGISTERED DEMAND, and that is the shape rather than a
// detail: `forgetRun` is reached from ONE call site that cannot enumerate this
// store's readers, and the enumeration it used to carry went stale the moment a
// reader was added for the case that guard let it through.
// ---------------------------------------------------------------------------

describe("forgetRun asks the registered demands before it drops anything", () => {
  const unregisters: (() => void)[] = [];

  function demand(fn: (workflowID: string) => boolean): void {
    unregisters.push(store.registerRunStateDemand(fn));
  }

  afterEach(() => {
    for (const un of unregisters.splice(0)) {
      un();
    }
    store.forgetRun("r1");
    store.forgetRun("r2");
  });

  it("keeps the cell a demand claims, and asks it about the RUN", async () => {
    await seedRun("r1", { workflowId: "r1", status: "running" });
    const claim = vi.fn(() => true);
    demand(claim);

    store.forgetRun("r1");

    expect(store.peekRunState("r1")?.status).toBe("running");
    expect(claim).toHaveBeenCalledWith("r1");
  });

  it("still drops a run no demand claims, so the bound is per RUN", async () => {
    await seedRun("r1", { workflowId: "r1", status: "running" });
    await seedRun("r2", { workflowId: "r2", status: "running" });
    demand((id) => id === "r1");

    store.forgetRun("r1");
    store.forgetRun("r2");

    expect(store.peekRunState("r1")?.status, "claimed").toBe("running");
    expect(store.peekRunState("r2"), "unclaimed, so the cache is still bounded").toBeUndefined();
  });

  it("drops the run once its demand unregisters", async () => {
    await seedRun("r1", { workflowId: "r1", status: "running" });
    const un = store.registerRunStateDemand(() => true);

    store.forgetRun("r1");
    expect(store.peekRunState("r1")?.status).toBe("running");

    un();
    store.forgetRun("r1");

    expect(store.peekRunState("r1")).toBeUndefined();
  });
});
