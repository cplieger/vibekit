// ---------------------------------------------------------------------------
// The workflow adapter: KAS's `inspect` reply folded into the exec view's model.
//
// Pure, so these are plain value assertions with no DOM. What they pin is the half of
// the reply nothing used to read — the control-flow containers, `nodePlan`, and
// `state.inputs` — plus the two derivations that are this file's own judgement rather
// than a copy of the wire: the roll-up of a container's state from its children, and
// the alert's precedence.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { runToExec, indexPlan } from "./run-exec-source.js";
import { flatten, leaves, counters } from "./exec-view/model.js";
import type { RunState } from "./run-store.js";
import type { RunAsks } from "./fundamentals/run-card.js";

const NO_ASKS: RunAsks = { count: 0, nodes: new Set<string>(), label: "" };

function stateWith(root: unknown, extra: Record<string, unknown> = {}): RunState {
  return { workflowId: "wf_1", status: "running", root, ...extra } as unknown as RunState;
}

const step = (nodeId: string, status: string, extra: Record<string, unknown> = {}) => ({
  nodeId,
  type: "step",
  status,
  children: [],
  ...extra,
});

describe("runToExec structure", () => {
  // The root is a container KAS names after the workflow itself, so keeping it would
  // put one group around everything — an indent carrying no information.
  it("unwraps the synthetic root and keeps the real top level", () => {
    const run = runToExec(
      "wf_1",
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [step("a", "completed"), step("b", "running")],
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.nodes.map((n) => n.label)).toEqual(["a", "b"]);
    // The path still carries the root, because it is the address the server stamps on
    // a step frame and the two sides of that join must agree.
    expect(run.nodes[0]?.path).toBe("wf_1/a");
  });

  // The regression this file exists to prevent: every surface flattened the tree to
  // its leaves, so a loop, a parallel and a watch were all invisible.
  it("keeps control-flow containers as nodes of their own", () => {
    const run = runToExec(
      "wf_1",
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          {
            nodeId: "loop",
            type: "repeat",
            status: "running",
            children: [step("work", "running")],
          },
          { nodeId: "watch", type: "watch", status: "pending", children: [] },
        ],
      }),
      undefined,
      NO_ASKS,
    );
    expect(flatten(run.nodes).map((n) => `${n.kind}:${n.label}`)).toEqual([
      "repeat:loop",
      "step:work",
      "watch:watch",
    ]);
    // Only the leaves count as steps: a container's span is its children's, so
    // counting it would inflate the total and double-count the time.
    expect(counters(run.nodes).total).toBe(2);
    expect(leaves(run.nodes).map((n) => n.label)).toEqual(["work", "watch"]);
  });

  // A node type this build has never seen must land on a kind the CSS has a rule for,
  // or the row renders with no glyph and no treatment.
  it("maps an unknown node type onto group rather than passing it through", () => {
    const run = runToExec(
      "wf_1",
      stateWith({ nodeId: "wf_1", type: "fanout", status: "running", children: [] }),
      undefined,
      NO_ASKS,
    );
    expect(run.nodes[0]?.kind).toBe("group");
  });
});

describe("runToExec container state", () => {
  // A container reads `running` for as long as anything inside it is open, which tells
  // a reader nothing they cannot already see. The worst outcome beneath it is what a
  // collapsed group has to be able to say.
  it("rolls the worst child outcome up to its container", () => {
    for (const [child, want] of [
      ["failed", "fail"],
      ["aborted", "warn"],
      ["running", "running"],
      ["paused", "waiting"],
    ] as const) {
      const run = runToExec(
        "wf_1",
        stateWith({
          nodeId: "wf_1",
          type: "sequence",
          status: "running",
          children: [
            {
              nodeId: "g",
              type: "parallel",
              status: "running",
              children: [step("ok", "completed"), step("other", child)],
            },
          ],
        }),
        undefined,
        NO_ASKS,
      );
      expect(run.nodes[0]?.state).toBe(want);
    }
  });

  // All children done means the container is done, even while its own status still
  // says running — which it does until KAS settles it.
  it("settles a container once every child has", () => {
    const run = runToExec(
      "wf_1",
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          {
            nodeId: "g",
            type: "sequence",
            status: "running",
            children: [step("a", "completed"), step("b", "skipped")],
          },
        ],
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.nodes[0]?.state).toBe("ok");
  });
});

describe("runToExec reads what nothing read before", () => {
  // `state.inputs` had zero readers on any surface: what the run was ASKED to do was
  // displayed nowhere in the app.
  it("carries the run's inputs", () => {
    const run = runToExec(
      "wf_1",
      stateWith(step("a", "running"), { inputs: { repo: "vibekit" } }),
      undefined,
      NO_ASKS,
    );
    expect(run.inputs).toEqual({ repo: "vibekit" });
  });

  // `nodePlan` had zero readers: passed through verbatim by GET /api/runs/{id} and
  // decoded by nothing, so a loop's bound and its exit condition were on the wire and
  // had never been on screen.
  it("states a repeat's bound and stop condition from the plan", () => {
    const run = runToExec(
      "wf_1",
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          { nodeId: "loop", type: "repeat", status: "running", children: [step("w", "running")] },
        ],
      }),
      [
        {
          nodeId: "loop",
          type: "repeat",
          maxIterations: 5,
          onMaxIterations: "pause",
          stopCondition: "w.output contains PASS",
          steps: [{ nodeId: "w", type: "step" }],
        },
      ],
      NO_ASKS,
    );
    const loop = run.nodes[0];
    expect(loop?.subtitle).toContain("up to 5 passes");
    expect(loop?.subtitle).toContain("w.output contains PASS");
    const labels = (loop?.facts ?? []).map((f) => f.label);
    expect(labels).toContain("Max passes");
    expect(labels).toContain("At the cap");
  });

  // Four more fields with zero render sites before the exec view.
  it("surfaces the per-step facts nothing rendered", () => {
    const run = runToExec(
      "wf_1",
      stateWith(
        step("a", "completed", {
          agentName: "wf-coder",
          modelId: "claude-opus-5",
          effortLevel: "max",
          completionSignal: "success",
          sessionId: "sess_1",
          continuationAttempts: 2,
          watchTerminal: true,
        }),
      ),
      undefined,
      NO_ASKS,
    );
    const facts = new Map((run.nodes[0]?.facts ?? []).map((f) => [f.label, f.value]));
    expect(facts.get("Effort")).toBe("max");
    expect(facts.get("Signal")).toBe("success");
    expect(facts.get("Session")).toBe("sess_1");
    expect(facts.get("Retries")).toBe("2");
    expect(facts.get("Watch")).toBe("reached a terminal state");
  });

  // `auto` is the ABSENCE of a model choice, so naming it as one implies a pin that
  // never happened.
  it("does not report auto as a model", () => {
    const run = runToExec(
      "wf_1",
      stateWith(step("a", "running", { agentName: "wf-coder", modelId: "auto" })),
      undefined,
      NO_ASKS,
    );
    expect((run.nodes[0]?.facts ?? []).map((f) => f.label)).not.toContain("Model");
    expect(run.nodes[0]?.subtitle).toBe("wf-coder");
  });
});

describe("indexPlan tolerance", () => {
  // A foreign shape whose members grow between kiro-cli releases. The page's other
  // half renders fine whether or not this walk understood all of it, so nothing here
  // may throw.
  it("survives a plan that is not the shape it expects", () => {
    for (const input of [undefined, null, 42, "nope", {}, [], [null, 7, "x"]]) {
      expect(() => indexPlan(input)).not.toThrow();
      expect(indexPlan(input).size).toBe(0);
    }
  });

  it("descends every container spelling, including one it does not know", () => {
    const idx = indexPlan([
      {
        nodeId: "top",
        steps: [{ nodeId: "mid", branches: [{ nodeId: "deep", maxIterations: 3 }] }],
      },
      { nodeId: "other", nodes: [{ nodeId: "nested", join: "any" }] },
    ]);
    expect(idx.get("deep")?.maxIterations).toBe(3);
    expect(idx.get("nested")?.join).toBe("any");
  });

  // `stopWhen` is the other spelling; the engine rejects a node declaring both, so
  // folding them into one field cannot lose one.
  it("takes stopWhen as a stop condition", () => {
    expect(indexPlan([{ nodeId: "n", stopWhen: "watch.terminal" }]).get("n")?.stopCondition).toBe(
      "watch.terminal",
    );
  });

  // A node the plan mentions with nothing interesting on it earns no entry, so a
  // caller can treat "present" as "has a fact".
  it("indexes only nodes that carry a fact", () => {
    expect(indexPlan([{ nodeId: "bare", type: "step" }]).size).toBe(0);
  });
});

describe("runToExec alert precedence", () => {
  // An unanswered ask outranks the run's own status, because the run genuinely still
  // reads `running` while a step's ask blocks it — so the status reports nothing wrong
  // and would leave the one actionable state unsaid.
  it("puts an unanswered ask ahead of everything", () => {
    const run = runToExec(
      "wf_1",
      stateWith(step("a", "running"), { status: "failed" }),
      undefined,
      {
        count: 2,
        nodes: new Set(["a"]),
        label: "Run tests?",
      },
    );
    expect(run.alert?.kind).toBe("input");
    expect(run.alert?.text).toContain("Run tests?");
    expect(run.alert?.text).toContain("2 asks waiting");
    // And it reclassifies the run itself, so the header says "needs input".
    expect(run.state).toBe("input");
    expect(run.nodes[0]?.state).toBe("input");
  });

  // A deliberate stop is not a failure, and telling them apart is the whole reason
  // `stopInitiator` is on the wire.
  it("separates a user stop from a failure", () => {
    const stopped = runToExec(
      "wf_1",
      stateWith(step("a", "aborted"), {
        status: "aborted",
        stopInitiator: "user",
        stopReason: "changed my mind",
      }),
      undefined,
      NO_ASKS,
    );
    expect(stopped.alert?.kind).toBe("stopped");
    expect(stopped.alert?.text).toContain("changed my mind");

    const failed = runToExec(
      "wf_1",
      stateWith(step("a", "failed", { failureReason: "exit 1" }), { status: "failed" }),
      undefined,
      NO_ASKS,
    );
    expect(failed.alert?.kind).toBe("failed");
    // It names the step, because "the run failed" sends a reader looking for which.
    expect(failed.alert?.text).toContain("a failed: exit 1");
  });

  it("names the transient-error code behind a pause", () => {
    const run = runToExec(
      "wf_1",
      stateWith(step("a", "paused"), {
        status: "paused",
        pauseReason: "retrying",
        pauseDetail: { code: "ThrottlingException" },
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.alert?.kind).toBe("paused");
    expect(run.alert?.text).toContain("ThrottlingException");
  });

  it("raises no alert on a run that wants nothing", () => {
    const run = runToExec(
      "wf_1",
      stateWith(step("a", "completed"), { status: "completed" }),
      undefined,
      NO_ASKS,
    );
    expect(run.alert).toBeUndefined();
    expect(run.live).toBe(false);
  });
});

describe("runToExec outputs", () => {
  // KAS keeps artifacts and captured outputs apart because they are produced
  // differently; to a reader they are one question. An artifact wins a key collision,
  // being the value a step CHOSE to publish.
  it("merges artifacts over captured outputs", () => {
    const run = runToExec(
      "wf_1",
      stateWith(step("a", "completed"), {
        capturedOutputs: { a: "from capture", b: "only capture" },
        artifacts: { a: "from artifact" },
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.outputs).toEqual({ a: "from artifact", b: "only capture" });
  });

  it("reports no outputs rather than an empty map", () => {
    const run = runToExec("wf_1", stateWith(step("a", "completed")), undefined, NO_ASKS);
    expect(run.outputs).toBeUndefined();
  });
});
