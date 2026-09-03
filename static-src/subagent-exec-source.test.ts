// ---------------------------------------------------------------------------
// The subagent adapter: one delegate's slice of a chat, folded into the exec view's
// model.
//
// Pure, so these are plain value assertions with no DOM. What they pin is the half a
// reader cannot check by looking: which of the two SHAPES a delegate produces (a lone
// leaf against a pipeline's driver-and-stages), and the two facts the page's own
// layout rule then keys on — `nodes.length` and whether any node has children — since
// getting those wrong is what puts a tree pane of one row on screen or hides a
// pipeline's structure entirely.
//
// The stage/driver join is pinned here too. It is read off the tool-call ID's shape
// (`invoke_subagent_<driver>_stage_<name>`) rather than off any wire field, so it is
// exactly the kind of parse that breaks silently: a pipeline whose join fails renders
// as an unrelated single delegate, which looks like a working page.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { subagentToExec, subagentPath } from "./subagent-exec-source.js";
import { sliceSubagent, pipelineOf, stageName, groupOf } from "./subagent-slice.js";
import type { Message, ToolCall } from "./types.js";

/** An invocation tool call. `id` carries the pipeline join when stage-shaped. */
function invocation(id: string, subtask: string, extra: Partial<ToolCall> = {}): ToolCall {
  return {
    id,
    title: "Sub-agent: context-gatherer",
    status: "completed",
    kind: "other",
    ts: 0,
    agent_subtask_id: subtask,
    input: { name: "context-gatherer" },
    ...extra,
  } as unknown as ToolCall;
}

function driver(id: string, extra: Partial<ToolCall> = {}): ToolCall {
  return {
    id,
    title: "Orchestrate Sub-agent",
    status: "in_progress",
    kind: "other",
    ts: 0,
    ...extra,
  } as unknown as ToolCall;
}

/** One assistant message holding the given calls, plus a text block per subtask so
 *  the slice has something to project. */
function msg(calls: ToolCall[], texts: [string, string][] = []): Message {
  return {
    id: "m1",
    role: "assistant",
    ts: 0,
    content: "",
    tool_calls: calls,
    blocks: [
      ...calls.map((c) => ({
        type: "tool_use",
        tool_call_id: c.id,
        ...(c.agent_subtask_id === undefined ? {} : { agent_subtask_id: c.agent_subtask_id }),
      })),
      ...texts.map(([subtask, text]) => ({ type: "text", text, agent_subtask_id: subtask })),
    ],
  } as unknown as Message;
}

function exec(messages: Message[], subtask: string, live = false) {
  return subagentToExec(messages, subtask, sliceSubagent(messages, subtask, live));
}

describe("the stage/driver join", () => {
  it("reads a driver id out of a stage-shaped tool-call id", () => {
    expect(pipelineOf("invoke_subagent_orc_42_stage_review")).toBe("orc_42");
    expect(stageName("invoke_subagent_orc_42_stage_review")).toBe("review");
  });

  // A driver id is machine-minted and a stage NAME is author-supplied, so the
  // separator can only appear on the RIGHT and the first occurrence is the seam.
  // Splitting on the last one hands back a driver that does not exist, and the stage
  // then renders as a flat sibling of its own pipeline. Measured on the live volume:
  // every driver half is a `toolu_bdrk_*` id, and stage names carry underscores freely.
  it("keeps the driver id whole when a stage name contains the separator", () => {
    expect(pipelineOf("invoke_subagent_orc_1_stage_run_stage_two")).toBe("orc_1");
    expect(stageName("invoke_subagent_orc_1_stage_run_stage_two")).toBe("run_stage_two");
  });

  it("reports no pipeline for a plain invocation id", () => {
    expect(pipelineOf("tooluse_abc")).toBe("");
    expect(stageName("tooluse_abc")).toBe("");
  });

  // Both halves empty rather than a partial answer: an id that is prefixed but carries
  // no name on one side names no pipeline, and treating it as one would put a delegate
  // under a driver that does not exist.
  it("refuses a truncated stage id", () => {
    expect(pipelineOf("invoke_subagent__stage_x")).toBe("");
    expect(pipelineOf("invoke_subagent_orc_stage_")).toBe("");
  });

  it("finds every sibling stage of the pipeline a delegate belongs to", () => {
    const messages = [
      msg([
        driver("orc_1"),
        invocation("invoke_subagent_orc_1_stage_a", "sub_a"),
        invocation("invoke_subagent_orc_1_stage_b", "sub_b"),
        // A different pipeline's stage, and a plain delegate. Neither is a sibling.
        invocation("invoke_subagent_orc_2_stage_a", "sub_c"),
        invocation("tooluse_plain", "sub_d"),
      ]),
    ];
    const group = groupOf(messages, "sub_b");
    expect(group.pipeline).toBe("orc_1");
    expect(group.driver?.id).toBe("orc_1");
    expect(group.members.map((m) => m.subtaskID)).toEqual(["sub_a", "sub_b"]);
    expect(group.members.map((m) => m.stage)).toEqual(["a", "b"]);
  });

  it("reports no group for a delegate that is not a stage", () => {
    const messages = [msg([invocation("tooluse_plain", "sub_d")])];
    expect(groupOf(messages, "sub_d")).toEqual({
      pipeline: "",
      driver: undefined,
      members: [],
    });
  });
});

describe("the single-delegate shape", () => {
  // The page's layout rule is `nodes.length > 1 || any node has children`, so these two
  // numbers ARE whether a tree pane and a timeline appear. One leaf means the whole
  // width goes to the transcript, which is the point of the shape.
  it("produces exactly one childless leaf", () => {
    const run = exec([msg([invocation("tooluse_1", "sub_1")], [["sub_1", "done"]])], "sub_1");
    expect(run.nodes).toHaveLength(1);
    expect(run.nodes[0]?.children).toHaveLength(0);
    expect(run.nodes[0]?.path).toBe(subagentPath("sub_1"));
    expect(run.nodes[0]?.transcript).toBe(true);
  });

  it("names the delegate from its invocation and focuses it", () => {
    const run = exec([msg([invocation("tooluse_1", "sub_1")])], "sub_1");
    expect(run.label).toBe("context-gatherer");
    expect(run.focus).toBe(subagentPath("sub_1"));
  });

  // NO `output`, and it is the one field this adapter leaves empty on purpose. A
  // workflow step's `capturedOutput` is durable beside a transcript that is not, so
  // that pane's Output region is the only place its result exists. A delegate's blocks
  // ARE its transcript and its last text block IS its report, so filling this renders
  // the report twice on one screen — measured in the sidecar before it was removed.
  it("sets no output, because the transcript below already is it", () => {
    const run = exec(
      [
        msg(
          [invocation("tooluse_1", "sub_1")],
          [
            ["sub_1", "first"],
            ["sub_1", "the report"],
          ],
        ),
      ],
      "sub_1",
    );
    expect(run.nodes[0]?.output).toBeUndefined();
  });

  it("carries the delegate's handles as facts", () => {
    const run = exec([msg([invocation("tooluse_1", "sub_1")])], "sub_1");
    const facts = run.nodes[0]?.facts ?? [];
    expect(facts.find((f) => f.label === "Agent")?.value).toBe("context-gatherer");
    expect(facts.find((f) => f.label === "Subtask")?.value).toBe("sub_1");
    expect(facts.find((f) => f.label === "Call")?.value).toBe("tooluse_1");
  });

  // The timeline's only input. A tool call carries `ts` in millis plus a duration, so
  // the window is derivable — without it a pipeline's timeline draws nothing, and
  // overlap is the one fact a pipeline has that a column cannot show.
  it("derives the node's window from the call's ts and duration", () => {
    const run = exec(
      [msg([invocation("tooluse_1", "sub_1", { ts: 1_700_000_000_000, duration_ms: 5000 })])],
      "sub_1",
    );
    expect(run.nodes[0]?.start).toBe(new Date(1_700_000_000_000).toISOString());
    expect(run.nodes[0]?.end).toBe(new Date(1_700_000_005_000).toISOString());
  });

  // A running node has a start and no end, which is what `ExecNode` means by "still
  // going" and what makes its bar draw to the live edge rather than to a stale point.
  it("leaves the end open while the delegate is running", () => {
    const run = exec(
      [
        msg([
          invocation("tooluse_1", "sub_1", {
            status: "in_progress",
            ts: 1_700_000_000_000,
            duration_ms: 5000,
          }),
        ]),
      ],
      "sub_1",
      true,
    );
    expect(run.nodes[0]?.start).toBe(new Date(1_700_000_000_000).toISOString());
    expect(run.nodes[0]?.end).toBeUndefined();
  });

  // `in_progress` is a ToolStatus and not a member of `WireStatus`, so handing it to
  // `stateOf` folds it onto `pending` and every running delegate renders as a hollow
  // not-started ring. Each adapter maps its own vocabulary.
  it("maps the tool vocabulary's in_progress onto running", () => {
    const run = exec(
      [msg([invocation("tooluse_1", "sub_1", { status: "in_progress" })])],
      "sub_1",
      true,
    );
    expect(run.nodes[0]?.state).toBe("running");
    expect(run.state).toBe("running");
  });

  // The duration is on screen twice already — the detail pane's header and the node's
  // tree row — so a third copy in the facts list would be one number three ways, and
  // the version this list produced disagreed with both (`180s` against `3m 0s`).
  it("states no duration fact, since two surfaces already show it", () => {
    const run = exec(
      [msg([invocation("tooluse_1", "sub_1", { status: "completed", duration_ms: 180_000 })])],
      "sub_1",
    );
    expect(run.nodes[0]?.facts?.some((f) => f.label === "Took")).toBe(false);
  });

  // A delegate whose turn has been paged out of the store's window: the page renders
  // its own not-resident note rather than reaching here, but the adapter must still
  // produce a legal run rather than throwing.
  it("survives a delegate with no resident invocation", () => {
    const run = exec([msg([])], "sub_gone");
    expect(run.nodes).toHaveLength(1);
    expect(run.label).toBe("Subagent");
    expect(run.nodes[0]?.state).toBe("pending");
  });
});

describe("the pipeline shape", () => {
  const messages = [
    msg(
      [
        driver("orc_1", {
          input: { task: "review the diff", stages: [{ name: "a" }, { name: "b" }] },
        }),
        invocation("invoke_subagent_orc_1_stage_a", "sub_a"),
        invocation("invoke_subagent_orc_1_stage_b", "sub_b", { status: "in_progress" }),
      ],
      [["sub_b", "working"]],
    ),
  ];

  // One root WITH children, which is what makes the page show its tree and its
  // timeline. A pipeline rendered as a flat leaf would hide the one thing it has that
  // a single delegate does not.
  it("nests every stage under the driver", () => {
    const run = exec(messages, "sub_b");
    expect(run.nodes).toHaveLength(1);
    const root = run.nodes[0];
    expect(root?.children.map((c) => c.path)).toEqual([
      subagentPath("sub_a"),
      subagentPath("sub_b"),
    ]);
    // The row names what it HOLDS, not the object: the header one row up already
    // reads `Subagent pipeline`, so repeating those words here stutters.
    expect(root?.label).toBe("Stages");
    // The EXECUTION's name, not the stage the tab names: the header states the whole
    // pipeline's progress and elapsed time beside it.
    expect(run.label).toBe("Subagent pipeline");
  });

  // Opening a stage's link means "open THIS stage", so the focus names it even though a
  // sibling might be the one wanting attention. That is the whole reason `ExecRun.focus`
  // exists.
  it("focuses the stage the tab names, not the interesting one", () => {
    expect(exec(messages, "sub_a").focus).toBe(subagentPath("sub_a"));
    expect(exec(messages, "sub_b").focus).toBe(subagentPath("sub_b"));
  });

  // The driver's own status says only that something under it is open. What a collapsed
  // group must still report is the worst outcome beneath it.
  it("rolls the driver's state up from its stages", () => {
    expect(exec(messages, "sub_b").nodes[0]?.state).toBe("running");
    const failed = [
      msg([
        driver("orc_1"),
        invocation("invoke_subagent_orc_1_stage_a", "sub_a", { status: "completed" }),
        invocation("invoke_subagent_orc_1_stage_b", "sub_b", { status: "failed" }),
      ]),
    ];
    expect(exec(failed, "sub_a").nodes[0]?.state).toBe("fail");
  });

  // The driver declares its stage list up front, so a pipeline can say "two stages, one
  // running, one not started" from its first frame instead of growing rows as they
  // arrive.
  it("adds a declared stage the transcript has not reached", () => {
    const early = [
      msg([
        driver("orc_1", { input: { stages: [{ name: "a" }, { name: "b" }, { name: "c" }] } }),
        invocation("invoke_subagent_orc_1_stage_a", "sub_a"),
      ]),
    ];
    const kids = exec(early, "sub_a").nodes[0]?.children ?? [];
    expect(kids).toHaveLength(3);
    expect(kids.slice(1).map((k) => k.state)).toEqual(["pending", "pending"]);
    // A stage with no invocation yet hosts nothing, which is what lets the detail pane
    // say so rather than waiting on content that cannot arrive.
    expect(kids[1]?.transcript).toBeUndefined();
  });

  // The task is what the pipeline was ASKED to do, the same slot `state.inputs` fills
  // for a workflow run.
  it("renders the driver's task as the run's input", () => {
    expect(exec(messages, "sub_b").inputs).toEqual({ Task: "review the diff" });
  });

  // `input` is whatever the model produced, so a shape this adapter does not recognise
  // must yield no rows rather than throw on a page whose other half renders fine.
  // TWO stages, so the malformed input is judged against the GROUP shape: at one the
  // pipeline promotes and the assertion below would describe the flat shape instead.
  it("survives a driver whose input is not the expected shape", () => {
    for (const bad of [undefined, null, "text", 42, { stages: "nope" }, { stages: [1, null] }]) {
      const odd = [
        msg([
          driver("orc_1", { input: bad } as Partial<ToolCall>),
          invocation("invoke_subagent_orc_1_stage_a", "sub_a"),
          invocation("invoke_subagent_orc_1_stage_b", "sub_b"),
        ]),
      ];
      const run = exec(odd, "sub_a");
      expect(run.nodes[0]?.children).toHaveLength(2);
      expect(run.inputs).toBeUndefined();
    }
  });

  // The page's own rule is `run.nodes.length > 1 || any node has children`, so a group
  // over its only child forces open a tree pane holding one navigable row and one row
  // that is not navigable at all. Promotion here is the twin of the transcript's.
  it("promotes a lone stage to the root, rendering no group row", () => {
    const solo = [
      msg([
        driver("orc_solo", { input: { task: "do it", stages: [{ name: "a" }] } }),
        invocation("invoke_subagent_orc_solo_stage_a", "sub_solo"),
      ]),
    ];
    const run = exec(solo, "sub_solo");
    expect(run.nodes).toHaveLength(1);
    expect(run.nodes[0]?.path).toBe(subagentPath("sub_solo"));
    expect(run.nodes[0]?.children).toHaveLength(0);
    expect(run.nodes[0]?.transcript).toBe(true);
    // The pipeline is still named and still states what it was asked to do: both are
    // ExecRun fields the header renders, not row fields.
    expect(run.label).toBe("Subagent pipeline");
    expect(run.inputs).toEqual({ Task: "do it" });
  });
});
