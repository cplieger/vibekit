// ---------------------------------------------------------------------------
// Projecting one run's steps out of the launching chat's messages.
//
// The join key is `wf:<workflowId>:<nodePath>`, stamped server-side on every block
// and tool call a chat-parented run's steps produce. What these cases pin is the
// four rules that make the projection safe rather than merely present: it is scoped
// to ONE run, it never mutates the store's own objects, it spans a turn split in
// two, and it carries the streaming coordinates a consumer needs to subscribe with.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { blockKey } from "./store-signals.js";
import { sliceRunSteps, stepSubtaskID } from "./run-step-slice.js";
import type { Block, Message, ToolCall } from "./types.js";

function msg(id: string, blocks: Block[], toolCalls: ToolCall[] = []): Message {
  return { id, role: "assistant", ts: 0, content: "", blocks, tool_calls: toolCalls };
}

function text(body: string, subtask?: string): Block {
  return subtask === undefined
    ? { type: "text", text: body }
    : { type: "text", text: body, agent_subtask_id: subtask };
}

function tool(id: string, subtask?: string): ToolCall {
  return {
    id,
    title: "Execute Bash",
    kind: "execute",
    status: "completed",
    ...(subtask === undefined ? {} : { agent_subtask_id: subtask }),
  } as ToolCall;
}

describe("stepSubtaskID", () => {
  // The id is built through `step-subtask.ts`'s own prefix rather than a second
  // "wf:" literal, so the two halves of the join cannot drift. This case is what
  // pins that spelling against the server's (`ACPWorkflowMeta.SubtaskID`).
  it("spells the server's step subtask id", () => {
    expect(stepSubtaskID("wf_1", "root/fix-loop/coder")).toBe("wf:wf_1:root/fix-loop/coder");
  });
});

describe("sliceRunSteps", () => {
  it("keys a step's blocks under its own node path and nothing else's", () => {
    const slices = sliceRunSteps(
      [
        msg("m1", [
          text("the chat's own reply"),
          text("compiling", stepSubtaskID("wf_1", "a/coder")),
          text("verifying", stepSubtaskID("wf_1", "a/verify")),
        ]),
      ],
      "wf_1",
      false,
    );
    expect([...slices.keys()]).toEqual(["a/coder", "a/verify"]);
    expect(slices.get("a/coder")?.blocks.map((b) => b.text)).toEqual(["compiling"]);
    expect(slices.get("a/verify")?.blocks.map((b) => b.text)).toEqual(["verifying"]);
  });

  // The workflow id is the FIRST half of the key and it has to be compared whole: a
  // prefix test would let one run's card fill up with another's steps, and two runs
  // of the same recipe share every node path.
  // `wf_10` is deliberately in the fixture: the workflow id has to be compared
  // WHOLE, and a run id that merely starts with this one is the case a prefix test
  // gets wrong. Two runs of the same recipe share every node path, so the mistake
  // would fill one run's pane with another's steps.
  it("excludes another run's step at the same node path", () => {
    const slices = sliceRunSteps(
      [
        msg("m1", [
          text("mine", stepSubtaskID("wf_1", "a/b")),
          text("theirs", stepSubtaskID("wf_2", "a/b")),
          text("a longer id starting with mine", stepSubtaskID("wf_10", "a/b")),
        ]),
      ],
      "wf_1",
      false,
    );
    expect(slices.get("a/b")?.blocks.map((b) => b.text)).toEqual(["mine"]);
  });

  it("excludes a SUBAGENT's blocks, whose subtask id is a bare uuid", () => {
    const slices = sliceRunSteps(
      [msg("m1", [text("delegate work", "8f2c1f2e-0000-4000-8000-000000000000")])],
      "wf_1",
      false,
    );
    expect(slices.size).toBe(0);
  });

  // THE DEFECT THIS PREVENTS: `messages-blocks.ts` `containerFor` routes a block BY
  // its subtask id, so a block that still carried a `wf:` id would build a nested
  // run CARD inside the run page — and mutating the store's own object would delete
  // the id the TRANSCRIPT groups on, scattering that chat's card the moment this
  // page was opened.
  it("emits fresh blocks with no attribution and leaves the store's untouched", () => {
    const source = text("compiling", stepSubtaskID("wf_1", "a/coder"));
    const slices = sliceRunSteps([msg("m1", [source])], "wf_1", false);
    const emitted = slices.get("a/coder")?.blocks[0];
    expect(emitted?.agent_subtask_id).toBeUndefined();
    expect(emitted).not.toBe(source);
    expect(source.agent_subtask_id).toBe("wf:wf_1:a/coder");
  });

  // A mid-turn model switch splits ONE turn across two assistant messages, so a
  // walk that stopped at the first match would silently truncate a step's output.
  it("joins blocks split across two assistant messages, in order", () => {
    const slices = sliceRunSteps(
      [
        msg("m1", [text("first half", stepSubtaskID("wf_1", "a/coder"))]),
        msg("m2", [text("second half", stepSubtaskID("wf_1", "a/coder"))]),
      ],
      "wf_1",
      false,
    );
    expect(slices.get("a/coder")?.blocks.map((b) => b.text)).toEqual(["first half", "second half"]);
  });

  // The REAL coordinates rather than the synthetic ones the page renders under,
  // because that is how `blockTextSigs` is keyed: subscribing to these streams a
  // step's prose in the same tick as its delta, ahead of the coalesced version bump.
  it("carries the source block keys, index-aligned with the blocks", () => {
    const st = stepSubtaskID("wf_1", "a/coder");
    const slices = sliceRunSteps(
      [
        msg("m1", [text("chat"), text("one", st), text("other", stepSubtaskID("wf_1", "a/v"))]),
        msg("m2", [text("two", st)]),
      ],
      "wf_1",
      false,
    );
    const slice = slices.get("a/coder");
    expect(slice?.sourceKeys).toEqual([blockKey("m1", 1), blockKey("m2", 0)]);
    expect(slice?.sourceKeys).toHaveLength(slice?.blocks.length ?? -1);
  });

  // A `tool_use` block resolves its call through `message.tool_calls` and renders
  // NOTHING when it is absent, so a slice with blocks and no calls is a blank pane
  // rather than a degraded one.
  it("carries a step's tool calls and not the launch call", () => {
    const st = stepSubtaskID("wf_1", "a/coder");
    // The `run_workflow` invocation: the PARENT agent's call, so it carries the
    // workflow id and no subtask id at all.
    const launch = { ...tool("tc_launch"), workflow_id: "wf_1" } as ToolCall;
    const slices = sliceRunSteps(
      [msg("m1", [text("compiling", st)], [launch, tool("tc_step", st), tool("tc_chat")])],
      "wf_1",
      false,
    );
    expect(slices.get("a/coder")?.toolCalls.map((t) => t.id)).toEqual(["tc_step"]);
  });

  it("reports the chat's own liveness, which is all this channel carries", () => {
    const st = stepSubtaskID("wf_1", "a/coder");
    const messages = [msg("m1", [text("compiling", st)])];
    expect(sliceRunSteps(messages, "wf_1", true).get("a/coder")?.live).toBe(true);
    expect(sliceRunSteps(messages, "wf_1", false).get("a/coder")?.live).toBe(false);
  });

  // A malformed id falls THROUGH rather than keying a bogus path, which is what
  // keeps an orphan host out of the detail pane.
  it("drops a malformed step id instead of keying it", () => {
    const slices = sliceRunSteps(
      [msg("m1", [text("no path", "wf:wf_1"), text("no run", "wf::a/b")])],
      "wf_1",
      false,
    );
    expect(slices.size).toBe(0);
  });

  it("answers nothing for an empty workflow id", () => {
    const slices = sliceRunSteps(
      [msg("m1", [text("compiling", stepSubtaskID("wf_1", "a/coder"))])],
      "",
      false,
    );
    expect(slices.size).toBe(0);
  });
});
