// ---------------------------------------------------------------------------
// The group projection: one walk over a conversation, one slice per member.
//
// Pure, so these are plain value assertions with no DOM. What they pin is the half
// that is invisible on screen until it is wrong in a specific way: which member a
// block lands in. Every stage of a pipeline is a selectable row on the delegate's
// page and each row's transcript is mounted from its own slice, so a block filed
// under the wrong id renders one stage's work inside another's — which reads as a
// working page.
//
// The bucketing is a bare `agent_subtask_id` match, so the failure modes are all
// silent: a leak between siblings, an invocation block rendered as a row where the
// page header already carries it, a `sourceKeys` array off by one against `blocks`
// (which would subscribe a delegate's prose to a neighbouring block's signal), and
// one member's liveness read off the chat rather than its own invocation.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { sliceSubagentGroup } from "./subagent-slice.js";
import { blockKey } from "./store-signals.js";
import type { Block, Message, ToolCall } from "./types.js";

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

/** An ordinary (non-invocation) tool call a delegate made while working. */
function work(id: string, subtask: string): ToolCall {
  return {
    id,
    title: "Read File",
    status: "completed",
    kind: "read",
    ts: 0,
    agent_subtask_id: subtask,
  } as unknown as ToolCall;
}

function text(text: string, subtask?: string): Block {
  return { type: "text", text, ...(subtask === undefined ? {} : { agent_subtask_id: subtask }) };
}

function toolUse(toolCallID: string, subtask?: string): Block {
  return {
    type: "tool_use",
    tool_call_id: toolCallID,
    ...(subtask === undefined ? {} : { agent_subtask_id: subtask }),
  } as Block;
}

function message(id: string, blocks: Block[], calls: ToolCall[] = []): Message {
  return { id, role: "assistant", ts: 0, content: "", blocks, tool_calls: calls } as Message;
}

/** The prose of a slice's own blocks. A member's blocks are a mix of text and
 *  `tool_use` rows, so the text is read out on its own where the case is about which
 *  member wrote what. */
function prose(blocks: readonly Block[] | undefined): (string | undefined)[] {
  return (blocks ?? []).filter((b) => b.type === "text").map((b) => b.text);
}

/** A two-stage pipeline: a driver, one invocation per stage, and each stage's own
 *  work — the shape the page renders as a tree with two selectable rows. */
function pipeline(): Message[] {
  const stageA = invocation("invoke_subagent_orc_1_stage_plan", "sub_a");
  const stageB = invocation("invoke_subagent_orc_1_stage_review", "sub_b", {
    status: "in_progress",
  });
  return [
    message(
      "m1",
      [
        text("parent prose"),
        toolUse("orc_1"),
        toolUse(stageA.id, "sub_a"),
        text("stage A wrote this", "sub_a"),
        toolUse("read_1", "sub_a"),
        toolUse(stageB.id, "sub_b"),
        text("stage B wrote this", "sub_b"),
      ],
      [driver("orc_1"), stageA, stageB, work("read_1", "sub_a")],
    ),
  ];
}

describe("one walk answers for every member", () => {
  it("projects a slice per stage of the pipeline, keyed by subtask id", () => {
    const p = sliceSubagentGroup(pipeline(), "sub_a", false);
    expect([...p.slices.keys()].sort()).toEqual(["sub_a", "sub_b"]);
    expect(p.group.pipeline).toBe("orc_1");
  });

  // The point of the change: the SIBLING is projected whichever stage the tab names,
  // because its row is selectable on that stage's page and its transcript is mounted
  // from this slice. Asking for either stage must answer for both.
  it("answers for the sibling whichever member is asked about", () => {
    for (const asked of ["sub_a", "sub_b"]) {
      const p = sliceSubagentGroup(pipeline(), asked, false);
      expect(prose(p.slices.get("sub_a")?.blocks)).toEqual(["stage A wrote this"]);
      expect(prose(p.slices.get("sub_b")?.blocks)).toEqual(["stage B wrote this"]);
    }
  });

  // A stage name may contain the separator, so the join reads the FIRST occurrence:
  // splitting on the last hands back a driver that does not exist, the group comes
  // back empty, and the pipeline renders as one unrelated delegate.
  it("groups a stage whose name contains the separator", () => {
    const odd = invocation("invoke_subagent_orc_1_stage_run_stage_two", "sub_odd");
    const messages = [
      message(
        "m1",
        [toolUse(odd.id, "sub_odd"), text("odd stage", "sub_odd")],
        [driver("orc_1"), invocation("invoke_subagent_orc_1_stage_plan", "sub_a"), odd],
      ),
    ];
    const p = sliceSubagentGroup(messages, "sub_odd", false);
    expect(p.group.pipeline).toBe("orc_1");
    expect([...p.slices.keys()].sort()).toEqual(["sub_a", "sub_odd"]);
  });
});

describe("a member's content stays in its own slice", () => {
  // The parent's own prose carries no subtask id and belongs to no member; a
  // sibling's carries the sibling's. Either leaking would render one agent's work
  // inside another's box.
  it("keeps the parent's blocks and each sibling's out of the other's", () => {
    const p = sliceSubagentGroup(pipeline(), "sub_a", false);
    const a = p.slices.get("sub_a");
    expect(prose(a?.blocks)).toEqual(["stage A wrote this"]);
    expect(a?.blocks.some((b) => b.text === "parent prose")).toBe(false);
    expect(a?.blocks.some((b) => b.text === "stage B wrote this")).toBe(false);
  });

  it("clears the attribution without touching the store's own block", () => {
    const messages = pipeline();
    const source = messages[0]?.blocks?.[3];
    const projected = sliceSubagentGroup(messages, "sub_a", false).slices.get("sub_a")?.blocks[0];
    expect(projected?.agent_subtask_id).toBeUndefined();
    // The TRANSCRIPT groups on that id, so mutating the store's copy would scatter
    // the inline card's contents the moment this page was opened.
    expect(source?.agent_subtask_id).toBe("sub_a");
  });

  it("files a tool call under the member that made it", () => {
    const p = sliceSubagentGroup(pipeline(), "sub_a", false);
    expect(p.slices.get("sub_a")?.toolCalls.map((t) => t.id)).toEqual(["read_1"]);
    expect(p.slices.get("sub_b")?.toolCalls).toEqual([]);
  });
});

describe("each member's own invocation", () => {
  // The invocation IS the page header for its own delegate, so its block must not also
  // become a row. Its sibling's invocation block carries the sibling's id, so it lands
  // in that member's slice and is dropped there for the same reason.
  it("drops the invocation's own block from that member's slice", () => {
    const p = sliceSubagentGroup(pipeline(), "sub_a", false);
    for (const id of ["sub_a", "sub_b"]) {
      const slice = p.slices.get(id);
      const invocationID = slice?.invocation?.id;
      expect(invocationID).toBeDefined();
      expect(slice?.blocks.some((b) => b.tool_call_id === invocationID)).toBe(false);
      // The invocation is dropped as a ROW and kept as the header's own fact.
      expect(slice?.toolCalls.some((t) => t.id === invocationID)).toBe(false);
    }
    // Only the invocation: the work the delegate did is still a row.
    expect(p.slices.get("sub_a")?.blocks.some((b) => b.tool_call_id === "read_1")).toBe(true);
  });

  // Only the invocation. A delegate's other tool calls are the work it did and are the
  // page's rows; the dispatcher resolves a `tool_use` block through `tool_calls`, so a
  // slice with the block and not the call renders nothing at all.
  it("keeps the member's other tool calls and their blocks", () => {
    const stage = invocation("invoke_subagent_orc_1_stage_plan", "sub_a");
    const messages = [
      message(
        "m1",
        [toolUse(stage.id, "sub_a"), toolUse("read_1", "sub_a"), text("done", "sub_a")],
        [driver("orc_1"), stage, work("read_1", "sub_a")],
      ),
    ];
    const slice = sliceSubagentGroup(messages, "sub_a", false).slices.get("sub_a");
    expect(slice?.blocks.map((b) => b.tool_call_id ?? b.text)).toEqual(["read_1", "done"]);
    expect(slice?.toolCalls.map((t) => t.id)).toEqual(["read_1"]);
    expect(slice?.invocation?.id).toBe(stage.id);
  });
});

describe("sourceKeys", () => {
  // The REAL store coordinates, index-aligned with `blocks`: a text delta writes
  // `blockTextSigs` at the real key and never bumps the coarse message signal, so the
  // page subscribes to exactly these. Misaligned, a delegate's prose would follow a
  // neighbouring block's deltas.
  it("names the store coordinate of each projected block, in order", () => {
    const messages = [
      message(
        "m1",
        [text("parent"), text("first", "sub_a"), text("second", "sub_a")],
        [driver("orc_1"), invocation("invoke_subagent_orc_1_stage_plan", "sub_a")],
      ),
      message("m2", [text("third", "sub_a")]),
    ];
    const slice = sliceSubagentGroup(messages, "sub_a", false).slices.get("sub_a");
    expect(slice?.blocks.map((b) => b.text)).toEqual(["first", "second", "third"]);
    expect(slice?.sourceKeys).toEqual([blockKey("m1", 1), blockKey("m1", 2), blockKey("m2", 0)]);
  });

  it("stays aligned when a skipped block sits between two kept ones", () => {
    const stage = invocation("invoke_subagent_orc_1_stage_plan", "sub_a");
    const messages = [
      message(
        "m1",
        [text("before", "sub_a"), toolUse(stage.id, "sub_a"), text("after", "sub_a")],
        [driver("orc_1"), stage],
      ),
    ];
    const slice = sliceSubagentGroup(messages, "sub_a", false).slices.get("sub_a");
    expect(slice?.blocks).toHaveLength(2);
    expect(slice?.sourceKeys).toEqual([blockKey("m1", 0), blockKey("m1", 2)]);
  });
});

describe("live is per member", () => {
  // A stage can finish while its siblings and the conversation carry on for another
  // ten minutes. Reading the chat's flag would leave a settled stage under a streaming
  // caret and never seal its markdown.
  it("reads each member's own invocation status, not the chat's", () => {
    const p = sliceSubagentGroup(pipeline(), "sub_a", true);
    expect(p.slices.get("sub_a")?.live).toBe(false);
    expect(p.slices.get("sub_b")?.live).toBe(true);
  });

  // The chat's flag is the fallback for a member whose invocation has been paged out:
  // nothing else can say whether more is coming.
  it("falls back to the chat while a member's invocation is not resident", () => {
    const messages = [message("m1", [text("orphan work", "sub_gone")])];
    for (const chatLive of [true, false]) {
      const slice = sliceSubagentGroup(messages, "sub_gone", chatLive).slices.get("sub_gone");
      expect(slice?.invocation).toBeUndefined();
      expect(slice?.live).toBe(chatLive);
    }
  });
});

describe("a delegate that is not a stage", () => {
  it("reports no pipeline and projects itself alone", () => {
    const plain = invocation("tooluse_plain", "sub_solo");
    const messages = [
      message(
        "m1",
        [text("parent"), toolUse(plain.id, "sub_solo"), text("solo work", "sub_solo")],
        [plain],
      ),
    ];
    const p = sliceSubagentGroup(messages, "sub_solo", false);
    expect(p.group).toEqual({ pipeline: "", driver: undefined, members: [] });
    expect([...p.slices.keys()]).toEqual(["sub_solo"]);
    expect(p.slices.get("sub_solo")?.blocks.map((b) => b.text)).toEqual(["solo work"]);
    expect(p.slices.get("sub_solo")?.invocation?.id).toBe(plain.id);
  });

  // The requested id ALWAYS has an entry, so a caller never has to tell "not
  // projected" from "projected and empty" — the page reads the second as its
  // not-resident state and says so, where a missing entry would read as a defect.
  it("still answers for a delegate with nothing resident", () => {
    const p = sliceSubagentGroup([message("m1", [text("parent")])], "sub_missing", false);
    const slice = p.slices.get("sub_missing");
    expect(slice).toBeDefined();
    expect(slice?.blocks).toEqual([]);
    expect(slice?.toolCalls).toEqual([]);
    expect(slice?.invocation).toBeUndefined();
  });

  it("projects nothing at all for the empty id", () => {
    const p = sliceSubagentGroup(pipeline(), "", false);
    expect(p.slices.size).toBe(0);
    expect(p.group.pipeline).toBe("");
  });
});
