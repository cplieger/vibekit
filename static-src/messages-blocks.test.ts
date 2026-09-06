// ---------------------------------------------------------------------------
// Two grouping rules of the block dispatcher, which are deliberately OPPOSITE
// and were documented as the same thing.
//
// A tool GROUP is contiguous, and it is contiguous because it REGISTERS in the
// per-container auto-collapse registry declaring `continues: ["tool_use"]` — so a
// further tool call extends it and every other arrival supersedes it, which is
// what turns a run broken by prose into two groups. A SUBAGENT card registers
// NOTHING: `st.subagents` is keyed by subtask id and no arrival closes it, so a
// delegate's blocks join the card it opened however much the parent agent emitted
// in between. One registry, two opposite behaviours, both declared rather than
// implied by which append path a mounter happens to use.
//
// Four comments and three steering passages all claimed the subagent case was
// contiguous too. It never was, and nothing caught it, which is why these are
// tests rather than a corrected sentence.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import type { Message, ToolCall } from "./types.js";

// The dispatcher's import graph reaches the shared DOM registry, which throws on
// a missing app root. These ids have to exist before the import is evaluated.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

const {
  buildAssistantBody,
  updateAssistantBody,
  finalizeAssistantBody,
  getLiveAnchor,
  disposeAssistantBody,
  resetBlockRenders,
  buildDetachedBody,
  openContainerKeys,
} = await import("./messages-blocks.js");
const { blockKey, blockTextSigs, blockThinkingSigs, ensureBlockTextSig, clearAllBlockSigs } =
  await import("./store-signals.js");
const { setActive } = await import("./store.js");
const { outcomeIcon } = await import("./icons.js");
const { iconEl } = await import("./icon-el.js");

/** The shared failure mark, as markup, for the delegate-card assertions below. */
const failMark = (): string => (iconEl(outcomeIcon("fail")) as HTMLElement).outerHTML;

/** The chat every render in this file belongs to. It is part of the per-tool
 *  signal key, so a mount and its writer have to name the same one — and it
 *  must be the store's ACTIVE chat: the live-anchor fallback scan only
 *  considers the active chat's renders (a parked view's still-live bubble is
 *  DOM the reader cannot see). */
const CHAT_ID = "c-blocks";
setActive(CHAT_ID);

function text(t: string, subtask = ""): Record<string, unknown> {
  return { type: "text", text: t, agent_subtask_id: subtask };
}

function toolUse(id: string, subtask = ""): Record<string, unknown> {
  return { type: "tool_use", tool_call_id: id, agent_subtask_id: subtask };
}

function call(id: string, title: string): ToolCall {
  return { id, title, kind: "execute", status: "completed" } as unknown as ToolCall;
}

/** The id `render` last used, for a test that needs to drive a dispose path. */
let lastMsgID = "";

function render(blocks: Record<string, unknown>[], toolCalls: ToolCall[] = []): HTMLElement {
  const wrap = document.createElement("div");
  lastMsgID = `m-${String(Math.random())}`;
  buildAssistantBody(
    wrap,
    {
      id: lastMsgID,
      role: "assistant",
      content: "",
      blocks,
      tool_calls: toolCalls,
    } as unknown as Message,
    CHAT_ID,
    false,
  );
  return wrap;
}

/** The `.assistant-blocks` children, as a readable shape. */
function shape(wrap: HTMLElement): string[] {
  return [...(wrap.querySelector(".assistant-blocks")?.children ?? [])].map((e) => {
    if (e.classList.contains("subagent-container")) {
      return `pipeline(${String((e as HTMLElement).dataset["pipeline"])})`;
    }
    if (e.classList.contains("subagent-block")) {
      return `card(${String((e as HTMLElement).dataset["subtask"])})`;
    }
    if (e.classList.contains("run-card")) {
      return `run(${String((e as HTMLElement).dataset["run"])})`;
    }
    if (e.classList.contains("tool-group")) {
      return `group(${String(e.querySelectorAll(".tool-call").length)})`;
    }
    return `text(${String(e.textContent).trim().slice(0, 16)})`;
  });
}

describe("subagent grouping is keyed by subtask id, NOT by contiguity", () => {
  it("puts non-adjacent same-subtask blocks in ONE card", () => {
    const wrap = render([
      text("delegate first", "sub-A"),
      text("parent interleaved"),
      text("delegate second", "sub-A"),
    ]);
    const cards = wrap.querySelectorAll(".subagent-block");
    expect(cards).toHaveLength(1);
    const body = cards[0]?.querySelector(".subagent-body")?.textContent ?? "";
    expect(body).toContain("delegate first");
    expect(body).toContain("delegate second");
  });

  it("keeps two DIFFERENT subtasks in two cards", () => {
    const wrap = render([text("a", "sub-A"), text("b", "sub-B"), text("a again", "sub-A")]);
    expect(wrap.querySelectorAll(".subagent-block")).toHaveLength(2);
  });

  it("costs ORDER, not grouping: an interleaved parent block lands after the card", () => {
    // The card is appended at the subtask's FIRST appearance and later blocks are
    // appended into it, so prose that arrived between two of a delegate's halves
    // renders below the whole card. This is the accepted consequence of keying by
    // id; assert it so a future reader meets it as a decision, not a surprise.
    const wrap = render([
      text("delegate first", "sub-A"),
      text("parent interleaved"),
      text("delegate second", "sub-A"),
    ]);
    expect(shape(wrap)).toEqual(["card(sub-A)", "text(parent interleav)"]);
  });
});

// ---------------------------------------------------------------------------
// A WORKFLOW STEP is the other delegate kind, and it is the one with no
// invocation tool call: KAS announces a step with a
// `_kiro/workflow/node_start` notification, so `bindSubagent` never runs and
// whatever the card is named at creation is what the reader sees for the whole
// run. It was "Subagent" for every step, on the shared agent hexagon, so a
// three-step run rendered as three identical anonymous boxes.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// A WORKFLOW RUN is the launch tool call's card, and a WORKFLOW STEP is the one
// block kind with NO destination at all: a block whose `agent_subtask_id` parses as
// `wf:<workflowId>:<nodePath>` is DROPPED.
//
// The DROP is the property to pin, and it is explicit rather than a removed route
// for one reason: a block with no destination falls through to `blocksEl` and
// renders as loose top-level content in the launching turn, which is worse than
// either hosting it or dropping it. So every case here asserts the step's text is
// NOWHERE in the wrap, not merely that it is not in a row.
//
// The card itself is still keyed on the launch's `workflow_id`, and both arrival
// orders must still build exactly one card: the launch is persisted in its turn
// while a step's frames are not, so a refresh delivers them in either order.
// ---------------------------------------------------------------------------

describe("a workflow run's card is its launch, and its steps are dropped", () => {
  // `st.runs` is per RENDER and every case here shares one chat id, so a reset keeps
  // each case's cards its own.
  beforeEach(() => {
    resetBlockRenders();
  });

  const launch = (id: string, wf: string): ToolCall =>
    ({
      id,
      title: "Run Workflow",
      kind: "other",
      status: "completed",
      workflow_id: wf,
    }) as unknown as ToolCall;

  const cards = (wrap: HTMLElement): HTMLElement[] => [
    ...wrap.querySelectorAll<HTMLElement>(".assistant-blocks > .run-card"),
  ];
  const stepNodes = (wrap: HTMLElement): string[] =>
    [...wrap.querySelectorAll<HTMLElement>(".run-card .run-step")].map(
      (e) => e.dataset["node"] ?? "",
    );

  it("makes the launch tool call the card, not a tool row", () => {
    const wrap = render([toolUse("t1")], [launch("t1", "wf_1")]);
    expect(cards(wrap)).toHaveLength(1);
    expect(cards(wrap)[0]?.dataset["run"]).toBe("wf_1");
    // The point of the change: it is NOT also a tool card.
    expect(wrap.querySelectorAll(".assistant-blocks > .tool-group")).toHaveLength(0);
  });

  it("renders a step's text NOWHERE, not even as a top-level block", () => {
    // The strong form of the drop. Removing the routing without dropping would put
    // this prose at the top level of the launching turn, which is why the assertion
    // is on the whole wrap rather than on the absence of a row.
    const wrap = render(
      [toolUse("t1"), text("checking the build", "wf:wf_1:wf_1/build")],
      [launch("t1", "wf_1")],
    );
    expect(cards(wrap)).toHaveLength(1);
    expect(cards(wrap)[0]?.dataset["run"]).toBe("wf_1");
    expect(wrap.textContent).not.toContain("checking the build");
    expect(shape(wrap)).toEqual(["run(wf_1)"]);
  });

  it("is order-independent: a step arriving BEFORE its launch still builds one card", () => {
    const stepFirst = render(
      [text("checking the build", "wf:wf_1:wf_1/build"), toolUse("t1")],
      [launch("t1", "wf_1")],
    );
    expect(cards(stepFirst)).toHaveLength(1);
    expect(cards(stepFirst)[0]?.dataset["run"]).toBe("wf_1");
    expect(stepFirst.textContent).not.toContain("checking the build");
  });

  it("gives two runs their own cards, and neither any rows", () => {
    // Rows come from the run's STATE now, and this suite mocks no network, so no
    // state resolves and no row is ever painted here. `run-card.test.ts` owns the
    // rows; what this owns is that a step block contributes nothing.
    const wrap = render(
      [
        toolUse("t1"),
        text("a", "wf:wf_1:wf_1/lint"),
        text("b", "wf:wf_1:wf_1/build"),
        toolUse("t2"),
        text("c", "wf:wf_2:wf_2/deploy"),
      ],
      [launch("t1", "wf_1"), launch("t2", "wf_2")],
    );
    expect(cards(wrap).map((c) => c.dataset["run"])).toEqual(["wf_1", "wf_2"]);
    expect(stepNodes(wrap)).toEqual([]);
    expect(shape(wrap)).toEqual(["run(wf_1)", "run(wf_2)"]);
  });

  it("builds no card at all for a step whose launch is not resident", () => {
    // The card's ONLY creator is the launch tool call now, and that is a stated
    // consequence rather than an oversight: a run whose launching turn has paged out
    // has no card, and `run-bar.ts` plus the run's own sub-tab are its surfaces.
    const wrap = render([text("x", "wf:wf_1:wf/loop/iter-1/work")], []);
    expect(cards(wrap)).toHaveLength(0);
    expect(shape(wrap)).toEqual([]);
  });

  it("falls back to a delegate box for a malformed step id rather than losing the block", () => {
    // No second colon: not a step id this client can split, and dropping the
    // block would lose a delegate's whole transcript.
    const wrap = render([text("orphan text", "wf:no-node-path")], []);
    expect(cards(wrap)).toHaveLength(0);
    const box = wrap.querySelector(".subagent-block");
    expect(box?.getAttribute("data-subtask")).toBe("wf:no-node-path");
    expect(box?.textContent).toContain("orphan text");
  });

  it("leaves an ordinary tool call alone: no workflow id means no card", () => {
    const wrap = render([toolUse("t1")], [call("t1", "Run Command")]);
    expect(cards(wrap)).toHaveLength(0);
    expect(wrap.querySelectorAll(".tool-call")).toHaveLength(1);
  });

  // A run OUTLIVES the turn that started it: `run_workflow` returns as soon as the
  // run is created, so the launching turn ends while the run carries on for
  // minutes. The card's store subscription and its clock therefore hang off the
  // MESSAGE's lifetime, not the turn's — `pushStreamingEffect` is disposed at turn
  // end too, and registering there froze the card at exactly the moment it
  // mattered most. Invisible to the type checker and to the eye in a short test,
  // so it is pinned by which dispose path clears it.
  it("keeps the card alive through turn end and drops it only on unmount", () => {
    const wrap = render([toolUse("t1")], [launch("t1", "wf_live")]);
    expect(cards(wrap)).toHaveLength(1);

    // Turn end: the caret is sealed and the streaming effects go, but the card is
    // still the run's surface and must still be in the transcript.
    finalizeAssistantBody(lastMsgID);
    expect(cards(wrap)).toHaveLength(1);
    // Its parent, not `isConnected`: `wrap` is a detached node in this suite, so
    // nothing here is connected to a document and that check would only ever
    // measure the fixture.
    expect(cards(wrap)[0]?.parentElement?.className).toBe("assistant-blocks");

    // Unmount is what releases it. Nothing observable is asserted beyond "this
    // does not throw and clears the render state": the disposers are internal, and
    // a second dispose must be a no-op because a chat switch resets every render
    // AND the reconcile removes each row.
    disposeAssistantBody(lastMsgID);
    disposeAssistantBody(lastMsgID);
  });
});

// ---------------------------------------------------------------------------
// ONE RUN CARD PER RUN, and it lives in the LAUNCHING message.
//
// This used to need a chat-level registry (`runCardHosts`): the server folds a
// run's later frames into a new assistant message per turn-segment, so a later
// message's step blocks had to find the card the first one built rather than build
// a second — the reported symptom was four identical boxes across two turns, all
// reading one store cell. With those blocks dropped, the launch TOOL CALL is the
// only creator and a tool call belongs to exactly one message, so the dedupe is
// unreachable and the registry is gone. These cases pin that: a step-bearing
// message contributes NOTHING, whichever order the two arrive in.
// ---------------------------------------------------------------------------

describe("one run card per run, in the message that launched it", () => {
  beforeEach(() => {
    resetBlockRenders();
  });

  const launch = (id: string, wf: string): ToolCall =>
    ({
      id,
      title: "Run Workflow",
      kind: "other",
      status: "completed",
      workflow_id: wf,
    }) as unknown as ToolCall;

  function renderMsg(
    blocks: Record<string, unknown>[],
    toolCalls: ToolCall[] = [],
    chatID = CHAT_ID,
  ): { wrap: HTMLElement; id: string } {
    const wrap = document.createElement("div");
    const id = `m-${String(Math.random())}`;
    buildAssistantBody(
      wrap,
      { id, role: "assistant", content: "", blocks, tool_calls: toolCalls } as unknown as Message,
      chatID,
      false,
    );
    return { wrap, id };
  }

  it("keeps the card in the launching message and gives a later step message none", () => {
    const a = renderMsg([toolUse("t1")], [launch("t1", "wf_seg")]);
    const b = renderMsg([text("second segment work", "wf:wf_seg:wf_seg/deploy")]);

    expect(a.wrap.querySelectorAll(".run-card")).toHaveLength(1);
    expect(b.wrap.querySelectorAll(".run-card")).toHaveLength(0);
    // And the later segment's own turn stays empty rather than growing loose prose.
    expect(b.wrap.textContent).not.toContain("second segment work");
  });

  it("is order-independent across messages: the step message contributes nothing", () => {
    const b = renderMsg([text("early frame", "wf:wf_race:wf_race/lint")]);
    const a = renderMsg([toolUse("t2")], [launch("t2", "wf_race")]);

    expect(b.wrap.querySelectorAll(".run-card")).toHaveLength(0);
    expect(b.wrap.textContent).not.toContain("early frame");
    expect(a.wrap.querySelectorAll(".run-card")).toHaveLength(1);
  });

  it("scopes the card to its own RENDER: two launches of one run build two cards", () => {
    // The chat-level dedupe is gone, so nothing folds a second LAUNCH of the same
    // workflow id into the first message's card. Two launch calls are two runs to
    // the reader whatever their ids say, and a launch belongs to one message.
    const a = renderMsg([toolUse("t3")], [launch("t3", "wf_x")]);
    const other = renderMsg([toolUse("t3b")], [launch("t3b", "wf_x")], "c-other-chat");

    expect(a.wrap.querySelectorAll(".run-card")).toHaveLength(1);
    expect(other.wrap.querySelectorAll(".run-card")).toHaveLength(1);
  });

  it("a detached render builds its own card and never adopts the transcript's", () => {
    // Adopting would MOVE the card's DOM node out of the transcript and into the
    // page. Driven by LAUNCH calls, because a step block reaches neither surface.
    const detached = document.createElement("div");
    buildDetachedBody(
      detached,
      {
        id: "m-det",
        role: "assistant",
        content: "",
        blocks: [toolUse("t4d")],
        tool_calls: [launch("t4d", "wf_det")],
      } as unknown as Message,
      CHAT_ID,
      "sub-1",
      false,
    );
    const a = renderMsg([toolUse("t4")], [launch("t4", "wf_det")]);

    expect(detached.querySelectorAll(".run-card")).toHaveLength(1);
    expect(a.wrap.querySelectorAll(".run-card")).toHaveLength(1);
    expect(a.wrap.querySelector(".run-card")).not.toBe(detached.querySelector(".run-card"));
  });

  it("keeps the card alive across a sibling render's dispose", () => {
    // Nothing is shared between renders any more, so one message's unmount cannot
    // reach another's card. The registry this replaced needed a release rule for
    // exactly this, and getting it wrong took a live card away.
    const a = renderMsg([toolUse("t5")], [launch("t5", "wf_re")]);
    const b = renderMsg([toolUse("t6")], [launch("t6", "wf_re2")]);
    expect(a.wrap.querySelectorAll(".run-card")).toHaveLength(1);

    disposeAssistantBody(b.id);
    expect(a.wrap.querySelectorAll(".run-card")).toHaveLength(1);
  });
});

// ---------------------------------------------------------------------------
// A PIPELINE is the fourth destination, and the only delegate relationship the
// wire states outright. `orchestrate_subagent` produces three kinds of tool call:
// an `Orchestrate Sub-agent` driver with NO subtask, then per stage a
// `Sub-agent: <name>` invocation with a fresh subtask uuid, then that stage's own
// work under the same uuid. The stage's tool-call ID embeds its driver's —
// `invoke_subagent_<orchestrateId>_stage_<name>` — which is the parent pointer,
// measured on 59 of the 60 stage calls in 36 live chat files (the exception is a
// plain unorchestrated subagent, which has no parent).
//
// Before this join the driver and its stages rendered as flat siblings with no
// relation the DOM could express, and BOTH spun: the driver as an ordinary tool
// card at 0.8s, the stage box at 0.6s, so two rings beat against each other for
// one delegated task. So these tests pin two things: the nesting, and that exactly
// one level carries a ring.
// ---------------------------------------------------------------------------

describe("a pipeline's stages render inside the orchestrate call that started them", () => {
  // EVERY TEST GETS ITS OWN DRIVER ID, and that is a requirement rather than
  // tidiness: `store-signals.ts` `ensureToolCallSig` is `SignalMap.ensure`, which
  // returns the signal already registered for an id and IGNORES the initial value.
  // Nothing here disposes a render, so a reused id hands a later test the first
  // test's tool call, and the bind effect then applies that stale status and input
  // over the correct ones. Reusing "d1" cost four confusing failures.
  const driver = (id: string, stages = 0, status = "completed"): ToolCall =>
    ({
      id,
      title: "Orchestrate Sub-agent",
      kind: "other",
      status,
      input: stages > 0 ? { task: "do the thing", stages: Array.from({ length: stages }) } : {},
    }) as unknown as ToolCall;

  /** A stage invocation. Its id embeds its driver's, which IS the join. */
  const stage = (driverID: string, name: string, subtask: string, status = "completed"): ToolCall =>
    ({
      id: stageID(driverID, name),
      title: `Sub-agent: ${name}`,
      kind: "other",
      status,
      agent_subtask_id: subtask,
    }) as unknown as ToolCall;

  const stageID = (driverID: string, name: string): string =>
    `invoke_subagent_${driverID}_stage_${name}`;

  /** A plain subagent invocation: no pipeline in its id, so nothing to nest under. */
  const loneSubagent = (id: string, subtask: string, status = "completed"): ToolCall =>
    ({
      id,
      title: "Sub-agent: general-task-execution",
      kind: "other",
      status,
      agent_subtask_id: subtask,
    }) as unknown as ToolCall;

  const boxes = (wrap: HTMLElement): HTMLElement[] => [
    ...wrap.querySelectorAll<HTMLElement>(".assistant-blocks > .subagent-container"),
  ];
  const nested = (wrap: HTMLElement): string[] =>
    [
      ...wrap.querySelectorAll<HTMLElement>(
        ".subagent-container > .subagent-body > .subagent-block",
      ),
    ].map((e) => e.dataset["subtask"] ?? "");
  const nameOf = (e: Element | undefined): string =>
    e?.querySelector(".subagent-name")?.textContent ?? "";

  it("makes the orchestrate call the pipeline box, not a tool row", () => {
    const wrap = render([toolUse("d-bare")], [driver("d-bare")]);
    expect(boxes(wrap)).toHaveLength(1);
    expect(boxes(wrap)[0]?.dataset["pipeline"]).toBe("d-bare");
    // The point of the change: it is NOT also a tool card.
    expect(wrap.querySelectorAll(".assistant-blocks > .tool-group")).toHaveLength(0);
  });

  it("puts a stage's box inside the pipeline's body, not beside it", () => {
    const wrap = render(
      [
        toolUse("d-one"),
        toolUse(stageID("d-one", "plan"), "u-one"),
        text("planning", "u-one"),
        toolUse(stageID("d-one", "code"), "u-one-b"),
      ],
      [driver("d-one", 2), stage("d-one", "plan", "u-one"), stage("d-one", "code", "u-one-b")],
    );
    // ONE top-level object for the whole pipeline, where there were two.
    expect(shape(wrap)).toEqual(["pipeline(d-one)"]);
    expect(nested(wrap)).toEqual(["u-one", "u-one-b"]);
    const body = wrap.querySelector(".subagent-container .subagent-block .subagent-body");
    expect(body?.textContent).toContain("planning");
  });

  it("gives one pipeline its several stages, in first-seen order", () => {
    const wrap = render(
      [
        toolUse("d-two"),
        toolUse(stageID("d-two", "plan"), "u-two-a"),
        toolUse(stageID("d-two", "code"), "u-two-b"),
      ],
      [driver("d-two", 2), stage("d-two", "plan", "u-two-a"), stage("d-two", "code", "u-two-b")],
    );
    expect(shape(wrap)).toEqual(["pipeline(d-two)"]);
    expect(nested(wrap)).toEqual(["u-two-a", "u-two-b"]);
  });

  it("keeps two pipelines in two boxes", () => {
    const wrap = render(
      [
        toolUse("d-p1"),
        toolUse(stageID("d-p1", "plan"), "u-p1"),
        toolUse(stageID("d-p1", "code"), "u-p1-b"),
        toolUse("d-p2"),
        toolUse(stageID("d-p2", "plan"), "u-p2"),
        toolUse(stageID("d-p2", "code"), "u-p2-b"),
      ],
      [
        driver("d-p1", 2),
        stage("d-p1", "plan", "u-p1"),
        stage("d-p1", "code", "u-p1-b"),
        driver("d-p2", 2),
        stage("d-p2", "plan", "u-p2"),
        stage("d-p2", "code", "u-p2-b"),
      ],
    );
    expect(shape(wrap)).toEqual(["pipeline(d-p1)", "pipeline(d-p2)"]);
    expect(nested(wrap)).toEqual(["u-p1", "u-p1-b", "u-p2", "u-p2-b"]);
  });

  it("is order-independent: a stage arriving BEFORE its driver builds the same one box", () => {
    const wrap = render(
      [
        toolUse(stageID("d-rev", "plan"), "u-rev"),
        text("planning", "u-rev"),
        toolUse(stageID("d-rev", "code"), "u-rev-b"),
        toolUse("d-rev"),
      ],
      [driver("d-rev", 2), stage("d-rev", "plan", "u-rev"), stage("d-rev", "code", "u-rev-b")],
    );
    expect(shape(wrap)).toEqual(["pipeline(d-rev)"]);
    expect(nested(wrap)).toEqual(["u-rev", "u-rev-b"]);
    // The driver still names the box, so `bindPipeline` FOUND it rather than
    // building a second one beside it.
    expect(nameOf(boxes(wrap)[0])).toBe("Subagent pipeline · 2 stages");
  });

  it("leaves a plain unorchestrated subagent at the top level", () => {
    // No `invoke_subagent_<driver>_stage_<name>` id, so no parent to nest under.
    // This is the shape of the one stage-less call in the live chat files.
    const wrap = render(
      [toolUse("tc-lone", "u-lone"), text("working", "u-lone")],
      [loneSubagent("tc-lone", "u-lone")],
    );
    expect(boxes(wrap)).toHaveLength(0);
    expect(shape(wrap)).toEqual(["card(u-lone)"]);
  });

  it("falls back to the top level for a malformed stage id rather than losing the block", () => {
    // Prefix present, separator absent: not an id this client can split, and
    // nesting it under a guessed parent would be worse than not nesting it.
    const wrap = render(
      [toolUse("invoke_subagent_d-bad", "u-bad"), text("orphan", "u-bad")],
      [loneSubagent("invoke_subagent_d-bad", "u-bad")],
    );
    expect(boxes(wrap)).toHaveLength(0);
    expect(shape(wrap)).toEqual(["card(u-bad)"]);
    expect(wrap.querySelector(".subagent-block")?.textContent).toContain("orphan");
  });

  it("keeps the driver id whole when a stage NAME contains the separator", () => {
    // `indexOf`, not `lastIndexOf`: only the RIGHT half can carry a separator,
    // since a driver id is machine-minted and a stage name is author-supplied, so
    // a stage called `run_stage_two` must still resolve to its own driver.
    const wrap = render(
      [
        toolUse("d-sep"),
        toolUse(stageID("d-sep", "run_stage_two"), "u-sep"),
        toolUse(stageID("d-sep", "code"), "u-sep-b"),
      ],
      [
        driver("d-sep", 2),
        stage("d-sep", "run_stage_two", "u-sep"),
        stage("d-sep", "code", "u-sep-b"),
      ],
    );
    expect(boxes(wrap)[0]?.dataset["pipeline"]).toBe("d-sep");
    expect(nested(wrap)).toEqual(["u-sep", "u-sep-b"]);
  });

  it("names the box from the driver's own declared stage count", () => {
    const wrap = render([toolUse("d-three")], [driver("d-three", 3)]);
    expect(nameOf(boxes(wrap)[0])).toBe("Subagent pipeline · 3 stages");
  });

  it("names it plainly when the driver declares no stages", () => {
    const wrap = render([toolUse("d-none")], [driver("d-none")]);
    expect(nameOf(boxes(wrap)[0])).toBe("Subagent pipeline");
  });

  // -------------------------------------------------------------------------
  // ONE RING PER PIPELINE, and it belongs to the stage. This is the half of the
  // change a reader sees: before it, the driver's tool card spun at 0.8s and the
  // stage box spun at 0.6s, side by side, for one delegated task.
  // -------------------------------------------------------------------------

  it("spins the STAGE and not the pipeline", () => {
    // The driver declares 2 and only one has arrived: a legitimate mid-run state,
    // and one that keeps the container this test is about.
    const wrap = render(
      [toolUse("d-spin"), toolUse(stageID("d-spin", "plan"), "u-spin")],
      [driver("d-spin", 2, "in_progress"), stage("d-spin", "plan", "u-spin", "in_progress")],
    );
    const pipeline = boxes(wrap)[0];
    const inner = wrap.querySelector<HTMLElement>(
      ".subagent-container > .subagent-body > .subagent-block",
    );
    // Both are running, so before the container variant both carried a ring.
    expect(pipeline?.classList.contains("running")).toBe(true);
    expect(inner?.classList.contains("running")).toBe(true);

    expect(inner?.querySelector(".subagent-spinner")).not.toBeNull();
    expect(pipeline?.querySelector(":scope > .subagent-header > .subagent-spinner")).toBeNull();
  });

  it("keeps the pipeline's identity glyph while it runs, with no outcome mark yet", () => {
    const wrap = render([toolUse("d-glyph")], [driver("d-glyph", 2, "in_progress")]);
    const icon = boxes(wrap)[0]?.querySelector(".subagent-icon");
    // A leaf empties this slot to become a ring; a container must not.
    expect(icon?.querySelector("svg")).not.toBeNull();
    expect(icon?.classList.contains("subagent-spinner")).toBe(false);
    // ONE mark, and while it runs that mark is still the identity glyph — not an
    // outcome silhouette.
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
    expect(icon?.querySelector("svg")?.outerHTML).not.toBe(failMark());
  });

  it("gives the pipeline its outcome mark once it settles", () => {
    // TWO declared, not one: at one this would pass by exercising the
    // settled-with-no-stage fallback, which has its own named test below.
    const wrap = render([toolUse("d-fail")], [driver("d-fail", 2, "failed")]);
    const icon = boxes(wrap)[0]?.querySelector(".subagent-icon");
    expect(icon?.classList.contains("is-fail")).toBe(true);
    expect(icon?.querySelector("svg")?.outerHTML).toBe(failMark());
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
  });

  it("carries activity dots, and a leaf carries none", () => {
    const wrap = render([toolUse("d-dots")], [driver("d-dots", 2, "in_progress")]);
    const pipeline = boxes(wrap)[0];
    expect(
      pipeline?.querySelector(".subagent-busy")?.querySelectorAll(".activity-dot"),
    ).toHaveLength(3);
    // The CSS gate is `.collapsed.running`, and collapsed-by-default is what
    // makes them visible now.
    expect(pipeline?.classList.contains("collapsed")).toBe(true);
    expect(pipeline?.classList.contains("running")).toBe(true);

    const leaf = render(
      [toolUse("tc-leaf", "u-leaf")],
      [loneSubagent("tc-leaf", "u-leaf", "in_progress")],
    );
    expect(leaf.querySelector(".subagent-busy")).toBeNull();
  });

  it("gives the pipeline no rolling tail, because its body holds cards not prose", () => {
    // `blockLines` splits a child on the newlines its own text carries, and a
    // stage card's DOM carries none — so a tail here would fold the whole stage
    // into one line of glued words. The dots are its progress cue instead.
    const wrap = render(
      [toolUse("d-tail"), toolUse(stageID("d-tail", "plan"), "u-tail")],
      [driver("d-tail", 2, "in_progress"), stage("d-tail", "plan", "u-tail", "in_progress")],
    );
    expect(boxes(wrap)[0]?.querySelector(":scope > .subagent-tail")).toBeNull();
    // The stage keeps its own.
    const inner = wrap.querySelector(".subagent-container > .subagent-body > .subagent-block");
    expect(inner?.querySelector(":scope > .subagent-tail")).not.toBeNull();
  });

  // -------------------------------------------------------------------------
  // ONE STAGE MEANS NO CONTAINER. A container over a single card is two
  // disclosures, two headers and two ledgers for one piece of work, and 7 of the
  // 8 pipelines on the live volume declare exactly one stage — so promotion is
  // the common case rather than an edge.
  //
  // Blocks arrive incrementally and out of order, so the count comes from the
  // DRIVER's declared `stages` (complete the moment its tool call lands: the
  // server writes `ToolCall.Input` on the create frame and never on an update)
  // with the stages seen so far as the lower bound while the driver is not yet
  // resident. A sibling arriving later therefore RE-PARENTS the promoted card
  // into a container instead of rebuilding it, which is what carries its
  // streaming text, its disclosure state and its page link across the upgrade.
  // -------------------------------------------------------------------------

  /** Render a message, then grow that SAME message with more blocks and calls —
   *  the incremental-arrival shape, where a pipeline that had one stage on the
   *  first pass gains a second on the next. */
  function renderThenGrow(
    first: { blocks: Record<string, unknown>[]; calls: ToolCall[] },
    second: { blocks: Record<string, unknown>[]; calls: ToolCall[] },
    between?: (wrap: HTMLElement) => void,
  ): HTMLElement {
    const wrap = document.createElement("div");
    const id = `m-${String(Math.random())}`;
    const msg = (b: Record<string, unknown>[], c: ToolCall[]): Message =>
      ({ id, role: "assistant", content: "", blocks: b, tool_calls: c }) as unknown as Message;
    buildAssistantBody(wrap, msg(first.blocks, first.calls), CHAT_ID, true);
    between?.(wrap);
    updateAssistantBody(
      wrap,
      msg([...first.blocks, ...second.blocks], [...first.calls, ...second.calls]),
      CHAT_ID,
      true,
    );
    return wrap;
  }

  it("renders no container for a pipeline of ONE stage", () => {
    const wrap = render(
      [toolUse("d-solo"), toolUse(stageID("d-solo", "plan"), "u-solo"), text("planning", "u-solo")],
      [driver("d-solo", 1), stage("d-solo", "plan", "u-solo")],
    );
    expect(boxes(wrap)).toHaveLength(0);
    expect(shape(wrap)).toEqual(["card(u-solo)"]);
    const body = wrap.querySelector(".assistant-blocks > .subagent-block > .subagent-body");
    expect(body?.textContent).toContain("planning");
  });

  it("a promoted stage keeps its page link", () => {
    const wrap = render(
      [toolUse("d-link"), toolUse(stageID("d-link", "plan"), "u-link")],
      [driver("d-link", 1), stage("d-link", "plan", "u-link")],
    );
    // Direct-child `.subagent-foot`, because a container IS a `.subagent-block`
    // too: a descendant selector would find the nested stage's own link and pass
    // whether or not the stage was promoted.
    const link = wrap.querySelector<HTMLAnchorElement>(
      ".assistant-blocks > .subagent-block > .subagent-foot > a.subagent-open",
    );
    expect(link?.getAttribute("href")).toBe("/chat/c-blocks/subagent/u-link");

    // The negative half: a CONTAINER deliberately has none, because opening any
    // stage's page already shows the whole pipeline.
    const multi = render(
      [
        toolUse("d-link2"),
        toolUse(stageID("d-link2", "plan"), "u-link2"),
        toolUse(stageID("d-link2", "code"), "u-link2-b"),
      ],
      [
        driver("d-link2", 2),
        stage("d-link2", "plan", "u-link2"),
        stage("d-link2", "code", "u-link2-b"),
      ],
    );
    expect(boxes(multi)[0]?.querySelector(":scope > .subagent-foot a.subagent-open")).toBeNull();
  });

  it("upgrades a promoted stage into a container when a sibling arrives", () => {
    // The driver declares NOTHING, so the first pass counts the one stage it can
    // see and promotes it; the second stage then has to re-parent that card.
    const wrap = renderThenGrow(
      {
        blocks: [
          toolUse("d-up"),
          toolUse(stageID("d-up", "plan"), "u-up-a"),
          text("planning hard", "u-up-a"),
          // A member with a countable kind, so the pipeline's ledger has a row to
          // show: `setSummary` withholds the footer for an empty summary.
          toolUse("cmd-up", "u-up-a"),
        ],
        calls: [driver("d-up"), stage("d-up", "plan", "u-up-a"), call("cmd-up", "npm test")],
      },
      {
        blocks: [toolUse(stageID("d-up", "code"), "u-up-b")],
        calls: [stage("d-up", "code", "u-up-b")],
      },
    );
    expect(shape(wrap)).toEqual(["pipeline(d-up)"]);
    expect(nested(wrap)).toEqual(["u-up-a", "u-up-b"]);
    // MOVED, not rebuilt: the first stage's own streamed text came with it.
    const moved = wrap.querySelector(
      '.subagent-container > .subagent-body > .subagent-block[data-subtask="u-up-a"]',
    );
    expect(moved?.querySelector(":scope > .subagent-body")?.textContent).toContain("planning hard");
    expect(moved?.querySelector(".subagent-foot a.subagent-open")).not.toBeNull();
    // The upgraded container paints its OWN header: the count comes from the
    // stages it now holds, and the status from the DRIVER, which has settled — not
    // from `live`, which would read as running over finished work.
    expect(nameOf(boxes(wrap)[0])).toBe("Subagent pipeline · 2 stages");
    expect(boxes(wrap)[0]?.classList.contains("running")).toBe(false);
    expect(boxes(wrap)[0]?.querySelector(".subagent-icon")?.classList.contains("is-ok")).toBe(true);
    // The third channel the self-paint writes. Direct child of the container's OWN
    // foot, because the promoted stage carries a ledger of its own one level down.
    expect(
      boxes(wrap)[0]?.querySelector(":scope > .subagent-foot > .subagent-footer"),
    ).not.toBeNull();
  });

  it("keeps both stages in one container when the driver declared only one", () => {
    // The declared count is a FLOOR, not the answer: at `declared ?? observed` this
    // pipeline read as promoted and BOTH stages rendered as flat siblings, which is
    // the shape the stage-to-driver join was added to remove.
    const wrap = render(
      [
        toolUse("d-short"),
        toolUse(stageID("d-short", "plan"), "u-short-a"),
        toolUse(stageID("d-short", "code"), "u-short-b"),
      ],
      [
        driver("d-short", 1),
        stage("d-short", "plan", "u-short-a"),
        stage("d-short", "code", "u-short-b"),
      ],
    );
    expect(shape(wrap)).toEqual(["pipeline(d-short)"]);
    expect(nested(wrap)).toEqual(["u-short-a", "u-short-b"]);
    expect(nameOf(boxes(wrap)[0])).toBe("Subagent pipeline · 2 stages");
  });

  it("names a stage-built container before the driver's block arrives", () => {
    // The one order in which the CONSTRUCTOR's own label is the title: the driver's
    // CALL is resident, so its declared count is known, but its BLOCK never renders,
    // so `bindPipeline` never runs and nothing paints the box afterwards. No status
    // assertion: with no driver painted, the constructor's `live ? … : …` is a
    // deliberate guess rather than a fact.
    const wrap = render(
      [toolUse(stageID("d-pre", "plan"), "u-pre"), toolUse(stageID("d-pre", "code"), "u-pre-b")],
      [driver("d-pre", 2), stage("d-pre", "plan", "u-pre"), stage("d-pre", "code", "u-pre-b")],
    );
    expect(shape(wrap)).toEqual(["pipeline(d-pre)"]);
    expect(nested(wrap)).toEqual(["u-pre", "u-pre-b"]);
    expect(nameOf(boxes(wrap)[0])).toBe("Subagent pipeline · 2 stages");
  });

  it("carries a promoted stage's OPEN disclosure through the upgrade", () => {
    // The disclosure controller and the `sub:<subtask>` key are both properties of
    // the node, not of its position, so re-parenting must preserve both. A rebuild
    // would fold the card the reader had opened.
    const wrap = renderThenGrow(
      {
        blocks: [toolUse("d-open"), toolUse(stageID("d-open", "plan"), "u-open-a")],
        calls: [driver("d-open"), stage("d-open", "plan", "u-open-a")],
      },
      {
        blocks: [toolUse(stageID("d-open", "code"), "u-open-b")],
        calls: [stage("d-open", "code", "u-open-b")],
      },
      (w) => {
        const header = w.querySelector<HTMLElement>(
          '.assistant-blocks > .subagent-block[data-subtask="u-open-a"] > .subagent-header',
        );
        header?.click();
        expect(openContainerKeys().has("u-open-a")).toBe(true);
      },
    );
    const moved = wrap.querySelector<HTMLElement>(
      '.subagent-container > .subagent-body > .subagent-block[data-subtask="u-open-a"]',
    );
    expect(moved?.classList.contains("collapsed")).toBe(false);
    expect(openContainerKeys().has("u-open-a")).toBe(true);
    // Its sibling arrived collapsed, as every delegate card does.
    const sibling = wrap.querySelector<HTMLElement>(
      '.subagent-container > .subagent-body > .subagent-block[data-subtask="u-open-b"]',
    );
    expect(sibling?.classList.contains("collapsed")).toBe(true);
  });

  it("upgrades when a late driver declares two stages", () => {
    // Only ONE stage ever arrives; the upgrade is driven purely by the driver's
    // declared count landing after its stage did.
    const wrap = renderThenGrow(
      {
        blocks: [toolUse(stageID("d-late", "plan"), "u-late"), text("early work", "u-late")],
        calls: [stage("d-late", "plan", "u-late")],
      },
      { blocks: [toolUse("d-late")], calls: [driver("d-late", 2)] },
    );
    expect(shape(wrap)).toEqual(["pipeline(d-late)"]);
    expect(nested(wrap)).toEqual(["u-late"]);
    expect(nameOf(boxes(wrap)[0])).toBe("Subagent pipeline · 2 stages");
    const moved = wrap.querySelector(
      '.subagent-container > .subagent-body > .subagent-block[data-subtask="u-late"]',
    );
    expect(moved?.querySelector(":scope > .subagent-body")?.textContent).toContain("early work");
  });

  it("still renders a box for a driver that settled having dispatched no stage", () => {
    // The one case where a promoted-count driver keeps its box: nothing stands in
    // for it, and a block that renders nothing is a lost block. Deferred to the
    // settle so it can never displace a stage still on its way.
    const wrap = render([toolUse("d-nodisp")], [driver("d-nodisp", 1, "failed")]);
    expect(boxes(wrap)).toHaveLength(1);
    const icon = boxes(wrap)[0]?.querySelector(".subagent-icon");
    expect(icon?.classList.contains("is-fail")).toBe(true);
    expect(icon?.querySelector("svg")?.outerHTML).toBe(failMark());
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
  });
});

describe("tool grouping IS contiguous, which is the contrast", () => {
  it("splits a run of tool calls broken by prose into two groups", () => {
    const wrap = render(
      [toolUse("t1"), toolUse("t2"), text("prose between"), toolUse("t3")],
      [call("t1", "Run Command"), call("t2", "Run Command"), call("t3", "Run Command")],
    );
    expect(shape(wrap)).toEqual(["group(2)", "text(prose between)", "group(1)"]);
  });

  it("keeps an unbroken run in one group", () => {
    const wrap = render(
      [toolUse("t1"), toolUse("t2"), toolUse("t3")],
      [call("t1", "Run Command"), call("t2", "Run Command"), call("t3", "Run Command")],
    );
    expect(shape(wrap)).toEqual(["group(3)"]);
  });
});

// ---------------------------------------------------------------------------
// The auto-collapse REGISTRY, which is what makes the rule above total.
//
// A tool group declares `continues: ["tool_use"]`, so a further tool call joins
// it and EVERY other destination in a container supersedes it. That used to be
// seven hand-written `closeToolGroup` calls — all of them correct, and the eighth
// destination added would have forgotten. The table below is one row per closer,
// so a ninth destination has an obvious place to be added.
// ---------------------------------------------------------------------------

describe("the auto-collapse registry: which arrivals close a tool group", () => {
  /** Member counts of the top-level groups, in order. One entry means the run
   *  was unbroken; two means something closed the first group. */
  function groups(wrap: HTMLElement): number[] {
    return [...wrap.querySelectorAll(".assistant-blocks > .tool-group")].map(
      (g) => g.querySelectorAll(".tool-call").length,
    );
  }

  function thinking(t: string, subtask = ""): Record<string, unknown> {
    return { type: "thinking", thinking: t, agent_subtask_id: subtask };
  }

  const todoCall = { id: "td", title: "todo_list", kind: "other", status: "completed" };

  /** `[what arrives between two tool calls, the middle blocks, its tool calls, the
   *  member counts of the resulting groups]`. The continuation row is the only one
   *  expecting ONE group holding all three cards; every closer splits the run. */
  const arrivals: [string, Record<string, unknown>[], ToolCall[], number[]][] = [
    [
      "a further tool call (the one TOLERATED continuation)",
      [toolUse("t2")],
      [call("t2", "Read")],
      [3],
    ],
    ["a text block", [text("prose")], [], [1, 1]],
    ["a thinking block", [thinking("weighing")], [], [1, 1]],
    ["a todo checklist", [toolUse("td")], [todoCall as unknown as ToolCall], [1, 1]],
    ["a delegate's box", [text("delegate prose", "sub-A")], [], [1, 1]],
    // A genuine card ARRIVAL closes the group; a DROPPED step block appends nothing
    // at all, so it must not. The two rows together are what say the supersede is
    // driven by an append rather than by a block existing.
    [
      "a run card",
      [toolUse("tw")],
      [
        {
          id: "tw",
          title: "Run Workflow",
          kind: "other",
          status: "completed",
          workflow_id: "wf_8",
        } as unknown as ToolCall,
      ],
      [1, 1],
    ],
    [
      "a DROPPED workflow step (appends nothing)",
      [text("step prose", "wf:wf_9:wf_9/build")],
      [],
      [2],
    ],
  ];

  it.each(arrivals)("%s", (what, middle, extra, expected) => {
    const wrap = render(
      [toolUse("t1"), ...middle, toolUse("t9")],
      [call("t1", "Read"), ...extra, call("t9", "Read")],
    );
    expect(
      groups(wrap),
      `${what} must ${expected.length === 1 ? "extend" : "close"} the group`,
    ).toEqual(expected);
  });

  it("is keyed by CONTAINER: a parent-lane block does not close a DELEGATE's group", () => {
    // The registry is per container, so the parent's prose supersedes the parent's
    // own registrants and nothing inside the delegate's body. Without that keying a
    // delegate's tool run would be split by every parent block that interleaved.
    const wrap = render(
      [toolUse("t1", "sub-A"), text("parent prose"), toolUse("t2", "sub-A")],
      [call("t1", "Read"), call("t2", "Read")],
    );
    const body = wrap.querySelector(".subagent-block .subagent-body");
    expect(body?.querySelectorAll(":scope > .tool-group")).toHaveLength(1);
    expect(body?.querySelectorAll(".tool-call")).toHaveLength(2);
  });

  it("does not enrol a subagent box: a following block neither closes nor folds it", () => {
    // The non-contiguity contrast the file's header comment draws, now enforced
    // rather than implied. Two halves: a later same-subtask block still joins the
    // card, and the card's own disclosure is untouched by the arrival.
    const wrap = render(
      [text("delegate prose", "sub-A"), text("parent prose"), text("more delegate", "sub-A")],
      [],
    );
    const cards = wrap.querySelectorAll<HTMLElement>(".assistant-blocks > .subagent-block");
    expect(cards).toHaveLength(1);
    expect(cards[0]?.querySelectorAll(".subagent-body .message.assistant")).toHaveLength(2);
  });

  it("leaves a subagent box the reader opened open when the next block lands", () => {
    // A box mounts collapsed and the only toggle is the reader's, so the falsifiable
    // half is that nothing re-folds it. If the box registered with `continues: []`,
    // the parent's prose arriving after it would put `.collapsed` back.
    const wrap = render([text("delegate prose", "sub-A")], []);
    const box = wrap.querySelector<HTMLElement>(".subagent-block")!;
    box.classList.remove("collapsed");

    updateAssistantBody(
      wrap,
      {
        id: lastMsgID,
        role: "assistant",
        content: "",
        blocks: [text("delegate prose", "sub-A"), text("parent prose")],
        tool_calls: [],
      } as unknown as Message,
      CHAT_ID,
      false,
    );

    expect(wrap.querySelector(".assistant-blocks")?.children).toHaveLength(2);
    expect(box.classList.contains("collapsed")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Legacy internal engine bookkeeping never mounts a card.
//
// KAS's session-boot cloud-config fetch is suppressed server-side at translate
// (keyed on _meta.kiro.toolId), so a live stream never delivers one. What
// still carries it is a transcript PERSISTED BEFORE the suppression, where the
// call sits at in_progress forever — its completion frame was lost to the boot
// displacement — which is the user-visible "Fetching your cloud config spinner
// never expires". Title-keyed here: the persisted ToolCall carries no tool id,
// and the title is a KAS constant, not model prose.
// ---------------------------------------------------------------------------

describe("legacy internal tool calls are dropped at the dispatcher", () => {
  it("renders no card for a persisted cloud-config fetch", () => {
    const stuck = {
      id: "t-cc",
      title: "Fetching your cloud config",
      kind: "other",
      status: "in_progress",
    } as unknown as ToolCall;
    const wrap = render([toolUse("t-cc")], [stuck]);
    expect(wrap.querySelector(".tool-call")).toBeNull();
    expect(wrap.querySelector(".tool-group")).toBeNull();
  });

  it("drops it as if it never existed, so real neighbours still group", () => {
    const wrap = render(
      [toolUse("t1"), toolUse("t-cc"), toolUse("t2")],
      [
        call("t1", "Run Command"),
        call("t-cc", "Fetching your cloud config"),
        call("t2", "Run Command"),
      ],
    );
    expect(shape(wrap)).toEqual(["group(2)"]);
  });
});

// ---------------------------------------------------------------------------
// A mounted block tracks the store's text whether or not it was mounted live.
//
// The fast path for a live turn is the per-block signal effect: a chunk writes
// the signal and the DOM updates with no reconcile. That effect only exists for
// a block the renderer judged LIVE, and `store.appendChunk` falls back to
// scheduling a full repaint for any block with no signal — so the repaint is the
// only channel left for a block whose liveness was misjudged, and it used to
// mount new blocks and never revisit a mounted one. The measured symptom was an
// assistant reply frozen at its first streamed chunk (`<p>Here's v</p>`), with
// no ellipsis, healed only by reloading the page.
//
// Liveness is misjudged cheaply: `isLikelyLiveStreaming` requires the chat's
// `thinking` flag, and any mid-turn event that clears it (a `.kiro/agents` parse
// error arriving at session construction was the real one) makes every later
// block of that turn mount for replay.
// ---------------------------------------------------------------------------

describe("a mounted block picks up text that arrived after it mounted", () => {
  function growingMessage(blocks: Record<string, unknown>[]): Message {
    return {
      id: "m-grow",
      role: "assistant",
      content: "",
      blocks,
      tool_calls: [],
    } as unknown as Message;
  }

  it("renders a text block's tail on the next repaint, mounted for REPLAY", () => {
    const wrap = document.createElement("div");
    const blocks = [text("Here's v")];
    const m = growingMessage(blocks);
    // live: false — the frozen case.
    buildAssistantBody(wrap, m, CHAT_ID, false);
    expect(wrap.querySelector(".message.assistant")?.textContent).toBe("Here's v");

    blocks[0]!["text"] = "Here's version two of the plan.";
    updateAssistantBody(wrap, m, CHAT_ID, false);
    // The grown bubble is on the incremental renderer now, which holds its last
    // character provisionally until the next write or the finalize — the same
    // contract a live turn has. What matters here is that the tail arrived at all.
    expect(wrap.querySelector(".message.assistant")?.textContent).toContain(
      "Here's version two of the plan",
    );

    finalizeAssistantBody(m.id);
    expect(wrap.querySelector(".message.assistant")?.textContent).toBe(
      "Here's version two of the plan.",
    );
  });

  it("renders a thinking block's tail the same way", () => {
    const wrap = document.createElement("div");
    const blocks: Record<string, unknown>[] = [
      { type: "thinking", thinking: "I", agent_subtask_id: "" },
    ];
    const m = growingMessage(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, false);
    expect(wrap.querySelector(".reasoning-body")?.textContent).toBe("I");

    blocks[0]!["thinking"] = "I should read the file first.";
    updateAssistantBody(wrap, m, CHAT_ID, false);
    expect(wrap.querySelector(".reasoning-body")?.textContent).toBe(
      "I should read the file first.",
    );
  });

  it("adds nothing when the block did not grow", () => {
    // The sweep runs on every repaint of every mounted block, so a settled
    // transcript must cost a length comparison and produce no DOM churn.
    const wrap = document.createElement("div");
    const blocks = [text("settled")];
    const m = growingMessage(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, false);
    const before = wrap.querySelector(".message.assistant")?.innerHTML;

    updateAssistantBody(wrap, m, CHAT_ID, false);
    updateAssistantBody(wrap, m, CHAT_ID, false);
    expect(wrap.querySelector(".message.assistant")?.innerHTML).toBe(before);
  });

  it("reaches a block inside a subagent card too", () => {
    const wrap = document.createElement("div");
    const blocks = [text("delegate ", "sub-A")];
    const m = growingMessage(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, false);

    blocks[0]!["text"] = "delegate finished its walk.";
    updateAssistantBody(wrap, m, CHAT_ID, false);
    expect(wrap.querySelector(".subagent-body")?.textContent).toContain(
      "delegate finished its walk",
    );
  });
});

// ---------------------------------------------------------------------------
// The live per-block effect consumes {full, delta} under a WATERMARK GUARD
// (design B5): the delta is appended only when it bridges the text accepted so
// far to `full`; anything else resyncs from `full`. The signal layer's
// synchronous flush makes a mismatch impossible in the steady state, so these
// pin the self-healing paths — a missed write, a replayed write, and a mount
// onto a signal that advanced before this render subscribed (the unpark case).
// ---------------------------------------------------------------------------

describe("the live block effect's watermark guard", () => {
  beforeEach(() => {
    resetBlockRenders();
    clearAllBlockSigs();
  });

  function liveMount(blocks: Record<string, unknown>[]): { wrap: HTMLElement; id: string } {
    const wrap = document.createElement("div");
    const id = `m-wm-${String(Math.random())}`;
    buildAssistantBody(
      wrap,
      {
        id,
        role: "assistant",
        content: "",
        blocks,
        tool_calls: [],
      } as unknown as Message,
      CHAT_ID,
      true,
    );
    return { wrap, id };
  }

  /** The bubble's settled text: dispose drains the reveal and finalizes the
   *  markdown stream synchronously, so the DOM is exact. */
  function settledText(wrap: HTMLElement, id: string): string {
    disposeAssistantBody(id);
    return wrap.querySelector(".message.assistant")?.textContent ?? "";
  }

  it("resyncs from full after a missed write — no loss, no duplication", () => {
    const { wrap, id } = liveMount([text("")]);
    const sig = blockTextSigs.get(blockKey(id, 0));
    expect(sig).toBeDefined();
    sig!.value = { full: "Hello ", delta: "Hello " };
    // The "lo brave " write never reaches the effect; the next one arrives
    // with a delta that cannot bridge the accepted text to full.
    sig!.value = { full: "Hello brave world", delta: "world" };
    expect(settledText(wrap, id)).toBe("Hello brave world");
  });

  it("ignores a replayed write — no duplication", () => {
    const { wrap, id } = liveMount([text("")]);
    const sig = blockTextSigs.get(blockKey(id, 0));
    sig!.value = { full: "Hi ", delta: "Hi " };
    sig!.value = { full: "Hi there", delta: "there" };
    // The same value re-published as a fresh object: the effect observes it
    // again, and the guard must not append "there" twice.
    sig!.value = { full: "Hi there", delta: "there" };
    expect(settledText(wrap, id)).toBe("Hi there");
  });

  it("a rebind's first observed value never double-appends", () => {
    // The unpark shape: the signal advanced while no render was subscribed,
    // so its LAST value still carries a nonempty delta. A fresh mount reads
    // the store's full text as its initial, observes that stale pair once,
    // and must treat it as already-on-screen rather than growth.
    const seeded = ensureBlockTextSig("m-wm-rebind", 0, "");
    seeded.value = { full: "abc", delta: "c" };
    const wrap = document.createElement("div");
    buildAssistantBody(
      wrap,
      {
        id: "m-wm-rebind",
        role: "assistant",
        content: "",
        blocks: [text("abc")],
        tool_calls: [],
      } as unknown as Message,
      CHAT_ID,
      true,
    );
    expect(settledText(wrap, "m-wm-rebind")).toBe("abc");
  });

  it("a thinking block follows full text across a missed write", () => {
    const { wrap, id } = liveMount([{ type: "thinking", thinking: "", agent_subtask_id: "" }]);
    const sig = blockThinkingSigs.get(blockKey(id, 0));
    expect(sig).toBeDefined();
    sig!.value = { full: "why", delta: "why" };
    sig!.value = { full: "why not now", delta: "now" };
    expect(wrap.querySelector(".reasoning-body")?.textContent).toBe("why not now");
  });
});

// ---------------------------------------------------------------------------
// The row over an empty bubble carries `.is-empty`, which is what hides it
// (css/13-messages.css). The bubble reports its own blank state — nothing
// rendered AND not streaming — and mountText writes it onto the row, so the
// class must track every way a bubble stops (or starts) being blank.
// ---------------------------------------------------------------------------

describe("an empty bubble's row is marked .is-empty", () => {
  function message(blocks: Record<string, unknown>[]): Message {
    return {
      id: `m-empty-${String(Math.random())}`,
      role: "assistant",
      content: "",
      blocks,
      tool_calls: [],
    } as unknown as Message;
  }

  // The row is the bubble's wrapper. Queried by relation rather than by class:
  // the real `.msg-row` factory is injected by messages.ts (initBlockCallbacks),
  // which this harness deliberately leaves out; messages-paint-causes.test.ts
  // covers the fully-wired `.msg-row.is-empty` combination.
  const rowOf = (wrap: HTMLElement): HTMLElement | null =>
    wrap.querySelector(".message.assistant")?.parentElement ?? null;

  it("marks a reserved slot's row at mount and clears it when the text lands", () => {
    const wrap = document.createElement("div");
    const blocks = [text("")];
    const m = message(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, false);
    expect(rowOf(wrap)?.classList.contains("is-empty")).toBe(true);

    blocks[0]!["text"] = "the frame arrived after all.";
    updateAssistantBody(wrap, m, CHAT_ID, false);
    expect(rowOf(wrap)?.classList.contains("is-empty")).toBe(false);
  });

  it("never marks a live bubble's row — the caret needs the row visible", () => {
    const wrap = document.createElement("div");
    const m = message([text("")]);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    expect(wrap.querySelector(".message.assistant.streaming")).not.toBeNull();
    expect(rowOf(wrap)?.classList.contains("is-empty")).toBe(false);
  });

  it("marks the row when a live bubble seals with nothing revealed", () => {
    // A reserved slot whose text never arrives: the caret drops at finalize and
    // the row must hide with it.
    const wrap = document.createElement("div");
    const m = message([text("")]);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    expect(rowOf(wrap)?.classList.contains("is-empty")).toBe(false);

    finalizeAssistantBody(m.id);
    expect(wrap.querySelector(".message.assistant.streaming")).toBeNull();
    expect(rowOf(wrap)?.classList.contains("is-empty")).toBe(true);
  });

  it("leaves a content-bearing row unmarked from the start", () => {
    const wrap = document.createElement("div");
    buildAssistantBody(wrap, message([text("hello")]), CHAT_ID, false);
    expect(rowOf(wrap)?.classList.contains("is-empty")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// The streaming caret has an END, and there is exactly one of it.
//
// The caret is a single CSS pseudo-element on `.message.assistant.streaming`,
// added by `buildAssistantBubble(initial, live)` and removed only by that
// bubble's `end()`. `renderRange` decides liveness as `live && i === lastIdx`,
// evaluated once when a block MOUNTS, and mounting is append-only — so a turn
// shaped "prose → tool call → prose → tool call → prose" opened three text
// blocks, each of which was the tail when it mounted, and nothing ever sealed
// the one that stopped being the tail. The reader saw a caret per block for the
// whole turn (3, 4, 5 of them), and the accent wash stayed on every one of them.
// ---------------------------------------------------------------------------

describe("exactly one streaming caret, and it ends", () => {
  const CARET = ".message.assistant.streaming";

  function liveMessage(blocks: Record<string, unknown>[]): Message {
    return {
      id: `m-caret-${String(Math.random())}`,
      role: "assistant",
      content: "",
      blocks,
      tool_calls: [],
    } as unknown as Message;
  }

  it("moves the caret to the new tail instead of accumulating one per block", () => {
    const wrap = document.createElement("div");
    const blocks = [text("first paragraph")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(1);

    // Three more text blocks arrive, each becoming the tail in turn. This is the
    // ordinary shape of a turn that interleaves prose and tool calls.
    for (const next of ["second paragraph", "third paragraph", "fourth paragraph"]) {
      blocks.push(text(next));
      updateAssistantBody(wrap, m, CHAT_ID, true);
      expect(wrap.querySelectorAll(CARET)).toHaveLength(1);
    }
    // ...and it is the LAST bubble that carries it.
    const bubbles = [...wrap.querySelectorAll(".message.assistant")];
    expect(bubbles).toHaveLength(4);
    expect(bubbles.at(-1)?.classList.contains("streaming")).toBe(true);
  });

  it("seals the previous text block when the new tail is a tool call", () => {
    const wrap = document.createElement("div");
    const blocks = [text("about to run something")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(1);

    blocks.push(toolUse("t1"));
    updateAssistantBody(wrap, m, CHAT_ID, true);
    // Prose that has been superseded by a tool call is not streaming; a tool
    // card carries its own status affordance and never a caret.
    expect(wrap.querySelectorAll(CARET)).toHaveLength(0);
  });

  it("leaves no caret behind after finalize", () => {
    const wrap = document.createElement("div");
    const blocks = [text("one")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    blocks.push(text("two"));
    updateAssistantBody(wrap, m, CHAT_ID, true);

    finalizeAssistantBody(m.id);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(0);
  });

  it("is idempotent: finalize twice, and a repaint after it, stay caret-free", () => {
    const wrap = document.createElement("div");
    const blocks = [text("one")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, true);

    finalizeAssistantBody(m.id);
    finalizeAssistantBody(m.id);
    updateAssistantBody(wrap, m, CHAT_ID, false);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(0);
  });

  it("never opens a caret on a replayed message", () => {
    const wrap = document.createElement("div");
    const blocks = [text("a"), toolUse("t1"), text("b")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, false);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(0);
  });

  it("holds one caret across the whole message when a subagent streams", () => {
    // `mountText` is the same function for a top-level bubble and one inside a
    // SubagentBlock body, so a delegate's tail block used to add a caret of its
    // own nested inside the collapsible box.
    const wrap = document.createElement("div");
    const blocks = [text("parent prose")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, true);

    blocks.push(text("delegate prose", "sub-A"));
    updateAssistantBody(wrap, m, CHAT_ID, true);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(1);

    blocks.push(text("parent again"));
    updateAssistantBody(wrap, m, CHAT_ID, true);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(1);

    finalizeAssistantBody(m.id);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(0);
  });

  it("keeps the caret when the only arrival is a DROPPED workflow step", () => {
    // A workflow step's frames land in the launching chat by the dozen while that
    // chat's own reply is still streaming. Nothing is placed for one, so sealing on
    // its arrival would end the reader's caret for a render that draws nothing —
    // which is what the seal ahead of the drop did.
    const wrap = document.createElement("div");
    const blocks = [text("still writing")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(1);

    blocks.push(text("step output", "wf:wf_1:wf_1/build"));
    updateAssistantBody(wrap, m, CHAT_ID, true);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(1);
    expect(wrap.textContent).not.toContain("step output");

    // A real arrival still seals, so the skip is scoped rather than a disabled rule.
    blocks.push(toolUse("t1"));
    updateAssistantBody(wrap, m, CHAT_ID, true);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// Which bubble Following pins to.
//
// The pin puts the anchor's bottom at the viewport's bottom, so an anchor that
// sits ABOVE the live edge parks the reader mid-transcript with the newest work
// off-screen — and, before the scroll listener learned to ignore the controller's
// own landing, latched the auto-scroll off entirely. The anchor is a REGISTRY
// maintained at the `.streaming` grant/seal sites, so the follow path reads one
// field instead of walking the tree per frame.
// ---------------------------------------------------------------------------
describe("getLiveAnchor", () => {
  const CARETS = ".message.assistant.streaming";

  // The registry is module state written by every render in this file, so each
  // case starts from a clean slate rather than from the previous case's caret.
  beforeEach(() => {
    resetBlockRenders();
  });

  function liveMsg(blocks: Record<string, unknown>[]): Message {
    return {
      id: `m-${String(Math.random())}`,
      role: "assistant",
      content: "",
      blocks,
      tool_calls: [],
    } as unknown as Message;
  }

  it("has no anchor when nothing is streaming", () => {
    const wrap = document.createElement("div");
    buildAssistantBody(wrap, liveMsg([text("done")]), CHAT_ID, false);
    expect(getLiveAnchor()).toBeNull();
  });

  it("takes the LAST live bubble, not the first in document order", () => {
    // One turn, two assistant messages: a mid-turn model switch splits it, and
    // the seal that drops `.streaming` is per-MESSAGE state, so the earlier
    // message's trailing bubble stays marked until the turn finalizes. A
    // `querySelector` would hand back that one, which is the older of the two.
    const wrap = document.createElement("div");
    buildAssistantBody(wrap, liveMsg([text("the earlier message")]), CHAT_ID, true);
    buildAssistantBody(wrap, liveMsg([text("the message still streaming")]), CHAT_ID, true);

    const live = [...wrap.querySelectorAll<HTMLElement>(CARETS)];
    expect(live).toHaveLength(2);
    expect(getLiveAnchor()).toBe(live[1]);
  });

  it("falls back to the older split-turn survivor when the newest seals", () => {
    // The registry's clear is identity-guarded and falls back by scanning the
    // render map: when the NEWER message's bubble finalizes first, the older
    // still-streaming bubble takes the slot back — the case the tree walk
    // solved by taking the LAST match.
    const wrap = document.createElement("div");
    const older = liveMsg([text("the earlier message")]);
    const newer = liveMsg([text("the message that finalizes first")]);
    buildAssistantBody(wrap, older, CHAT_ID, true);
    buildAssistantBody(wrap, newer, CHAT_ID, true);

    finalizeAssistantBody(newer.id);
    const live = [...wrap.querySelectorAll<HTMLElement>(CARETS)];
    expect(live).toHaveLength(1);
    expect(getLiveAnchor()).toBe(live[0]);
  });

  it("clears for good when every bubble has sealed", () => {
    const wrap = document.createElement("div");
    const m = liveMsg([text("prose")]);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    expect(getLiveAnchor()).not.toBeNull();

    finalizeAssistantBody(m.id);
    expect(getLiveAnchor()).toBeNull();
  });

  it("a disposed render releases its anchor", () => {
    const wrap = document.createElement("div");
    const m = liveMsg([text("prose")]);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    expect(getLiveAnchor()).not.toBeNull();

    disposeAssistantBody(m.id);
    expect(getLiveAnchor()).toBeNull();
  });

  it("reads the registry, not the tree — no selector call on the follow path", () => {
    const wrap = document.createElement("div");
    buildAssistantBody(wrap, liveMsg([text("prose")]), CHAT_ID, true);

    const spyAll = vi.spyOn(document, "querySelectorAll");
    const spyWrap = vi.spyOn(wrap, "querySelectorAll");
    try {
      expect(getLiveAnchor()).not.toBeNull();
      expect(spyAll).not.toHaveBeenCalled();
      expect(spyWrap).not.toHaveBeenCalled();
    } finally {
      spyAll.mockRestore();
      spyWrap.mockRestore();
    }
  });

  it("has no anchor when only a DELEGATE is streaming", () => {
    // The dispatcher seals the parent's bubble when the delegate's block
    // appends, so this is the ordinary shape of a running subagent. Null is the
    // answer rather than the delegate's bubble: that bubble sits inside a box
    // collapsed to `height: 0` with `overflow: hidden`, which clips it without
    // taking it out of layout, so its offsets describe a position the reader
    // cannot see. The document bottom is where the box's tail and footer are.
    const wrap = document.createElement("div");
    const blocks = [text("parent prose")];
    const m = liveMsg(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    blocks.push(text("delegate prose", "sub-A"));
    updateAssistantBody(wrap, m, CHAT_ID, true);

    const live = [...wrap.querySelectorAll<HTMLElement>(CARETS)];
    expect(live).toHaveLength(1);
    expect(live[0]?.closest(".subagent-body")).not.toBeNull();
    expect(getLiveAnchor()).toBeNull();
  });

  it("prefers a top-level bubble over a later delegate bubble", () => {
    // Two messages again, and this is the case that makes "last match" and
    // "skip delegates" two separate rules rather than one: the delegate's bubble
    // is the LAST in document order while the top-level one is what the reader
    // is following.
    const wrap = document.createElement("div");
    const withDelegate = liveMsg([text("older parent prose")]);
    const blocks = withDelegate.blocks as unknown as Record<string, unknown>[];
    buildAssistantBody(wrap, withDelegate, CHAT_ID, true);
    blocks.push(text("delegate prose", "sub-A"));
    updateAssistantBody(wrap, withDelegate, CHAT_ID, true);
    buildAssistantBody(wrap, liveMsg([text("newer parent prose")]), CHAT_ID, true);

    const live = [...wrap.querySelectorAll<HTMLElement>(CARETS)];
    expect(live).toHaveLength(2);
    expect(live[1]?.closest(".subagent-body")).toBeNull();
    expect(getLiveAnchor()).toBe(live[1]);
  });

  it("self-heals when the registry no longer owns the anchored element", () => {
    // resumeMessage and updateAssistantBody's self-healing path replace a
    // message's DOM wholesale, so the registry entry for that id points at the
    // NEW state while the anchor slot still holds the old, now-detached
    // element. Following that element pinned the scroller to a box with no
    // layout — the live-caught "scroll keeps snapping to the start of the
    // turn" (instrumented mid-stream: scrollTop 786 → 0).
    const wrap = document.createElement("div");
    const m = liveMsg([text("prose")]);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    expect(getLiveAnchor()).not.toBeNull();

    // The same message rebuilt SETTLED into a fresh row: the registry's
    // topLiveEl goes null while the anchor slot still names the old bubble.
    const wrap2 = document.createElement("div");
    buildAssistantBody(wrap2, m, CHAT_ID, false);
    expect(getLiveAnchor()).toBeNull();
  });

  it("the self-heal falls back to another still-live message", () => {
    const wrap = document.createElement("div");
    const older = liveMsg([text("older, still streaming")]);
    const newer = liveMsg([text("newer")]);
    buildAssistantBody(wrap, older, CHAT_ID, true);
    buildAssistantBody(wrap, newer, CHAT_ID, true);
    const live = [...wrap.querySelectorAll<HTMLElement>(CARETS)];
    expect(getLiveAnchor()).toBe(live[1]);

    // The anchored (newer) message rebuilds settled elsewhere; the heal scans
    // the registry and hands the slot back to the older live bubble.
    const wrap2 = document.createElement("div");
    buildAssistantBody(wrap2, newer, CHAT_ID, false);
    expect(getLiveAnchor()).toBe(live[0]);
  });
});

// ---------------------------------------------------------------------------
// Which thinking block mounts OPEN, and what closes it.
//
// A live trace mounts open ("Thinking…") while it is the last block of its OWN
// lane — the server extends the newest block of the delta's own subtask, so
// lane-tail is what wires its growth signal. DISCLOSURE is the append rule's:
// ANYTHING posted after it in its container seals it (collapse + label flip) —
// text, a tool group, a delegate's box, a steer note, another trace — because
// the wire carries no thinking-ended signal, so the next element's arrival is
// the end signal. The turn's finalize seals a trace nothing followed.
// ---------------------------------------------------------------------------

describe("thinking blocks mount open per LANE and seal on the next sibling", () => {
  function thinking(t: string, subtask = ""): Record<string, unknown> {
    return { type: "thinking", thinking: t, agent_subtask_id: subtask };
  }

  function liveMsg(blocks: Record<string, unknown>[], toolCalls: ToolCall[] = []): Message {
    return {
      id: `m-think-${String(Math.random())}`,
      role: "assistant",
      content: "",
      blocks,
      tool_calls: toolCalls,
    } as unknown as Message;
  }

  function traces(wrap: HTMLElement): HTMLDetailsElement[] {
    return [...wrap.querySelectorAll<HTMLDetailsElement>("details.msg-reasoning")];
  }

  function labelOf(d: HTMLDetailsElement | undefined): string {
    return d?.querySelector(".reasoning-label")?.textContent ?? "";
  }

  it("mounts the tail thinking block of a live message open", () => {
    const wrap = document.createElement("div");
    buildAssistantBody(wrap, liveMsg([thinking("weighing")]), CHAT_ID, true);
    const [t] = traces(wrap);
    expect(t?.open).toBe(true);
    expect(labelOf(t)).toBe("Thinking…");
  });

  it("seals the parent trace when a delegate's box mounts after it", () => {
    // The parent's trace is at index 0, the delegate's prose at index 1. The
    // delegate box landing in the parent's container is "something posted
    // after", so the trace closes — even though the parent's lane may still be
    // growing (the wiring stays live; disclosure is the append rule's).
    const wrap = document.createElement("div");
    buildAssistantBody(
      wrap,
      liveMsg([thinking("parent trace"), text("delegate prose", "sub-A")]),
      CHAT_ID,
      true,
    );
    const [t] = traces(wrap);
    expect(t?.open).toBe(false);
    expect(labelOf(t)).not.toBe("Thinking…");
  });

  it("seals the trace and starts a NEW tool group below it when a tool call follows", () => {
    // The pileup fix: tool cards used to keep joining the group ABOVE the
    // trace, so a think→tool loop rendered as one stack of cards over a pile
    // of traces that read as consecutive thinking blocks.
    const wrap = document.createElement("div");
    buildAssistantBody(
      wrap,
      liveMsg(
        [toolUse("t1"), thinking("weighing"), toolUse("t2")],
        [call("t1", "Read File"), call("t2", "Write File")],
      ),
      CHAT_ID,
      true,
    );
    const kinds = [...(wrap.querySelector(".assistant-blocks")?.children ?? [])].map((e) => {
      if (e.classList.contains("tool-group")) {
        return `group(${String(e.querySelectorAll(".tool-call").length)})`;
      }
      if (e.matches("details.msg-reasoning")) {
        return "trace";
      }
      return "other";
    });
    expect(kinds).toEqual(["group(1)", "trace", "group(1)"]);
    expect(traces(wrap)[0]?.open).toBe(false);
  });

  it("seals the trace when a run card mounts after it", () => {
    const wrap = document.createElement("div");
    buildAssistantBody(
      wrap,
      liveMsg(
        [thinking("planning"), toolUse("tw2")],
        [
          {
            id: "tw2",
            title: "Run Workflow",
            kind: "other",
            status: "completed",
            workflow_id: "wf_9",
          } as unknown as ToolCall,
        ],
      ),
      CHAT_ID,
      true,
    );
    const [t] = traces(wrap);
    expect(t?.open).toBe(false);
  });

  it("mounts a trace with a later same-lane sibling sealed", () => {
    // Both top-level: the text block after it is the lane's newer content, so
    // the trace is settled however live the message is.
    const wrap = document.createElement("div");
    buildAssistantBody(wrap, liveMsg([thinking("first"), text("the answer")]), CHAT_ID, true);
    const [t] = traces(wrap);
    expect(t?.open).toBe(false);
  });

  it("seals the open trace when the next thinking block spawns in its container", () => {
    const wrap = document.createElement("div");
    const blocks: Record<string, unknown>[] = [thinking("first")];
    const m = liveMsg(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    expect(traces(wrap)[0]?.open).toBe(true);

    blocks.push(thinking("second"));
    updateAssistantBody(wrap, m, CHAT_ID, true);

    const all = traces(wrap);
    expect(all).toHaveLength(2);
    expect(all[0]?.open).toBe(false);
    expect(labelOf(all[0])).toBe("Thinking completed");
    expect(all[1]?.open).toBe(true);
    expect(labelOf(all[1])).toBe("Thinking…");
  });

  it("leaves a delegate's own open trace alone when the parent thinks", () => {
    // Different containers: the delegate's trace lives in its card's body, the
    // parent's at top level, and neither seals the other.
    const wrap = document.createElement("div");
    const blocks: Record<string, unknown>[] = [thinking("delegate trace", "sub-A")];
    const m = liveMsg(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, true);

    blocks.push(thinking("parent trace"));
    updateAssistantBody(wrap, m, CHAT_ID, true);

    const all = traces(wrap);
    expect(all).toHaveLength(2);
    for (const t of all) {
      expect(t.open).toBe(true);
    }
  });

  it("finalize seals every open trace", () => {
    const wrap = document.createElement("div");
    const blocks: Record<string, unknown>[] = [
      thinking("delegate trace", "sub-A"),
      thinking("parent trace"),
    ];
    const m = liveMsg(blocks);
    buildAssistantBody(wrap, m, CHAT_ID, true);
    finalizeAssistantBody(m.id);
    for (const t of traces(wrap)) {
      expect(t.open).toBe(false);
      expect(labelOf(t)).toBe("Thinking completed");
    }
  });
});
