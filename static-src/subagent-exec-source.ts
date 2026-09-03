// ---------------------------------------------------------------------------
// The SUBAGENT adapter: a delegate's slice of a chat, folded into the exec view's
// model.
//
// The workflow half of that page is `run-exec-source.ts`; this is the second
// adapter, and writing it is the whole test of whether `exec-view/` earned its
// extraction. It needed two additions and no changes: `ExecRun.focus`, because a
// subagent has one door PER delegate where a run has one door meaning "the run",
// and `ExecPageOpts.icon`, because the header's glyph was the workflow's.
//
// WHERE THE DATA COMES FROM, and it is the one place this source is better off than
// the workflow's. A delegate's blocks live in the chat file stamped with
// `agent_subtask_id`, so they survive replay and a finished delegate's transcript is
// readable weeks later. A workflow step's is live-only (`run_step` persists nothing),
// which is why that adapter's empty note has three cases and this one has two.
//
// TWO SHAPES, and the page picks between them by content rather than by a flag:
//
//   a plain `invoke_sub_agent`  -> ONE leaf. No tree pane (one root, no children),
//                                 no timeline (it needs two leaves), so the whole
//                                 width goes to the transcript being read.
//   an `orchestrate_subagent`   -> the driver as a root with its stages beneath it.
//                                 The tree appears, and the timeline shows which
//                                 stages actually overlapped — which is the fact a
//                                 pipeline has and a column cannot express, since
//                                 two stages as consecutive rows look identical
//                                 whether they ran in sequence or at once.
//                                 EXCEPT at exactly one stage, which is the FIRST
//                                 shape again: that stage becomes the root and no
//                                 group row is produced, as the transcript promotes.
//
// A stage's page therefore shows the WHOLE pipeline with that stage selected. That is
// why the ref names a subtask and never a driver: one door, and it lands you on the
// delegate you clicked without hiding its siblings.
// ---------------------------------------------------------------------------

import type { Message, ToolCall } from "./types.js";
import { humanName, truncate } from "./strings.js";
import { subagentLabel, subagentName } from "./roles.js";
import { stateOf, inFlight, type ExecState } from "./exec-view/status.js";
import type { ExecFact, ExecNode, ExecRun } from "./exec-view/model.js";
import { groupOf, type SubagentSlice } from "./subagent-slice.js";

/** What a delegate with no resident invocation reads as. */
const FALLBACK = "Subagent";

/** The node path a delegate's content is filed under.
 *
 *  The subtask id, which is already stable and instance-unique — it is a fresh uuid
 *  per dispatch, so a stage re-run in a second pipeline is a different node, which is
 *  exactly what `ExecNode.path` requires. A stage NAME would not do: two pipelines in
 *  one turn can both have a `review` stage.
 *
 *  Exported because the view files content under it (`page.bodyFor(path)`) and the
 *  focus names it, so a second spelling would put a delegate's transcript in a host
 *  nothing selects. */
export function subagentPath(subtaskID: string): string {
  return subtaskID;
}

/** The driver's path. Prefixed, because a driver is addressed by its TOOL-CALL id
 *  while every stage is addressed by a subtask uuid, and an unprefixed tool-call id
 *  could in principle collide with one. */
function driverPath(pipelineID: string): string {
  return `pipeline:${pipelineID}`;
}

/** A delegate's state, from its invocation TOOL CALL's status.
 *
 *  Not `stateOf` directly, and that is the seam working rather than a workaround.
 *  `WireStatus` is the vocabulary KAS's run nodes speak (`running`, `paused`,
 *  `aborted`); a tool call speaks `ToolStatus` (`pending`, `in_progress`,
 *  `completed`, `failed`), and `in_progress` is not a member. `stateOf` is a tolerant
 *  reader that folds anything it does not know onto `pending`, so handing it a tool
 *  status made every RUNNING delegate render as a hollow not-started ring — caught by
 *  its own test rather than by reading. Each adapter maps its own words, which is what
 *  `status.ts` says the contract is, and widening `stateOf` with a vocabulary the
 *  workflow does not speak would have been the wrong fix. */
function toolState(status: string | undefined): ExecState {
  return status === "in_progress" ? "running" : stateOf(status);
}

/** The identity facts for one delegate, in the order they answer questions: what it
 *  is, which stage it was, and the handles for finding it again.
 *
 *  Deliberately short next to `run-exec-source.ts`'s twelve. A workflow step reports
 *  an agent, a model, an effort tier, a completion signal and a retry count because
 *  KAS's `inspect` carries them per node; a delegate's invocation tool call carries
 *  none of that, and inventing rows for it would be a page claiming facts it does not
 *  have. `ExecFact` being a list rather than named fields is what makes two facts and
 *  twelve equally legal. */
function factsOf(invocation: ToolCall | undefined, stage: string): ExecFact[] {
  const facts: ExecFact[] = [];
  const add = (label: string, value: string, mono = false): void => {
    if (value !== "") {
      facts.push(mono ? { label, value, mono } : { label, value });
    }
  };
  if (invocation !== undefined) {
    add("Agent", subagentName(invocation));
  }
  add("Stage", stage);
  if (invocation !== undefined) {
    // NO duration row, for the reason `output` carries none: the detail pane's own
    // header states the node's elapsed time and its tree row states it again, so a
    // third copy in the facts list is the same number three ways on one screen — and
    // the first attempt spelled it `180s` while both others said `3m 0s`, because this
    // list would have had to reach for the app's formatter to agree with them.
    //
    // Last, and monospace: handles rather than facts a reader acts on.
    add("Subtask", invocation.agent_subtask_id ?? "", true);
    add("Call", invocation.id, true);
  }
  return facts;
}

/** The one-line summary under a row's label: the delegate's own agent id, so a
 *  pipeline's tree reads as which agent ran which stage. Empty when the label already
 *  says it, since repeating it is noise on a row two words wide. */
function subtitleOf(invocation: ToolCall | undefined, label: string): string {
  if (invocation === undefined) {
    return "";
  }
  const id = subagentName(invocation);
  if (id === "" || humanName(id) === label) {
    return "";
  }
  return id;
}

/** The prose an `orchestrate_subagent` driver declares for the whole pipeline.
 *
 *  `task` rather than `stages`: the stage list is the tree below, and the task is the
 *  one thing the pipeline was ASKED to do — the same slot `state.inputs` fills for a
 *  workflow run, which had no reader anywhere before that page. Truncated because it
 *  is a paragraph and the header renders one line per input. */
function driverInputs(driver: ToolCall | undefined): Record<string, string> | undefined {
  if (driver === undefined) {
    return undefined;
  }
  const input = driver.input;
  if (input === null || input === undefined || typeof input !== "object") {
    return undefined;
  }
  const task = (input as Record<string, unknown>)["task"];
  if (typeof task !== "string" || task.trim() === "") {
    return undefined;
  }
  return { Task: truncate(task.trim(), 400) };
}

/** Fold one delegate into a leaf. */
function toLeaf(subtaskID: string, stage: string, invocation: ToolCall | undefined): ExecNode {
  const label =
    invocation === undefined
      ? stage === ""
        ? FALLBACK
        : humanName(stage)
      : subagentLabel(invocation);
  const state: ExecState = invocation === undefined ? "pending" : toolState(invocation.status);
  const out: ExecNode = {
    path: subagentPath(subtaskID),
    label,
    kind: "step",
    state,
    children: [],
    // Every delegate can host one: its blocks are in the chat file, so unlike a
    // workflow step there is no such thing as a leaf here that streams nothing.
    transcript: true,
  };
  const sub = subtitleOf(invocation, label);
  if (sub !== "") {
    out.subtitle = sub;
  }
  // The timeline's whole input, and it is derivable rather than absent: a tool call's
  // `ts` is stamped in millis when KAS reports the call — for an invocation, when the
  // delegate was dispatched — and `duration_ms` is its span. `end` stays absent while
  // the call is in flight, which is what `ExecNode` means by "still going", and a
  // settled call with no duration recorded gets no end rather than a zero-width bar.
  //
  // Without this a pipeline's timeline would draw nothing, and the overlap between
  // stages is the one fact a pipeline has that a column cannot express: two stages as
  // consecutive rows look identical whether they ran in sequence or at once.
  if (invocation !== undefined && invocation.ts > 0) {
    out.start = new Date(invocation.ts).toISOString();
    const ms = invocation.duration_ms ?? 0;
    if (ms > 0 && !inFlight(state)) {
      out.end = new Date(invocation.ts + ms).toISOString();
    }
  }
  const facts = factsOf(invocation, stage);
  if (facts.length > 0) {
    out.facts = facts;
  }
  // NO `output`, and that is the one field this adapter deliberately leaves empty.
  //
  // For a workflow step, `capturedOutput` is a durable field beside a transcript that
  // is live-only, so the detail pane's Output region is the only place a finished
  // step's result exists. A delegate's blocks ARE its transcript and its last text
  // block IS its report, so filling `output` renders that report twice on one screen:
  // once in the region above and once, in order and at full width, in the transcript
  // below it. Measured in the sidecar before it was removed. The transcript is the
  // better of the two — it keeps the report in the context of the work that produced
  // it — so the region goes rather than the blocks.
  return out;
}

/** The state a pipeline's driver reads as: the worst outcome beneath it.
 *
 *  Its own tool-call status is taken only when it says something terminal, for the
 *  reason `run-exec-source.ts` gives about containers: a driver reads `in_progress`
 *  for as long as anything under it is open, which tells a reader nothing they cannot
 *  see, while "a stage inside this failed" is what a collapsed group must still say. */
function rollUp(own: ExecState, kids: readonly ExecNode[]): ExecState {
  if (kids.length === 0) {
    return own;
  }
  const states = new Set(kids.map((k) => k.state));
  for (const s of ["input", "fail", "warn", "running", "waiting"] as const) {
    if (states.has(s)) {
      return s;
    }
  }
  if (states.has("ok")) {
    return states.has("pending") ? "running" : "ok";
  }
  return own;
}

/** Fold a delegate, and whatever it belongs to, into the exec view's model.
 *
 *  `slice` is the projection for the delegate being READ (subagent-slice.ts), passed
 *  in rather than computed here because the view already holds it: it is what the
 *  view renders into `page.bodyFor` and what it watches for streaming deltas, so
 *  producing a second copy would walk the conversation twice per repaint. */
export function subagentToExec(
  messages: readonly Message[],
  subtaskID: string,
  slice: SubagentSlice,
): ExecRun {
  const group = groupOf(messages, subtaskID);
  const focus = subagentPath(subtaskID);

  // --- the single-delegate shape --------------------------------------------
  if (group.pipeline === "") {
    const leaf = toLeaf(subtaskID, "", slice.invocation);
    return {
      id: subtaskID,
      label: leaf.label,
      state: leaf.state,
      nodes: [leaf],
      live: slice.live,
      focus,
    };
  }

  // --- the pipeline shape ----------------------------------------------------
  const stages = group.members.map((m) => toLeaf(m.subtaskID, m.stage, m.invocation));
  // A stage the transcript has not reached yet is a real node, and showing it is the
  // point of a plan: `orchestrate_subagent` declares its stage list up front, so a
  // pipeline can say "four stages, one running, two not started" from its first frame.
  for (const name of declaredStages(group.driver)) {
    if (!stages.some((s) => s.label === humanName(name) || factStage(s) === name)) {
      stages.push({
        path: `stage:${group.pipeline}:${name}`,
        label: humanName(name),
        kind: "step",
        state: "pending",
        children: [],
        facts: [{ label: "Stage", value: name }],
      });
    }
  }
  const driverState = rollUp(
    group.driver === undefined ? "running" : toolState(group.driver.status),
    stages,
  );
  const root: ExecNode = {
    // No stage COUNT here: the page header's `step N of M` states it, and this row's
    // own children are the list. The transcript's pipeline box carries the count
    // because a collapsed card has neither.
    //
    // `Stages`, not the header's own words: `ExecRun.label` one row up already reads
    // `Subagent pipeline` byte-identically, so naming the object twice stutters —
    // this row says what it HOLDS. Not the bare `Pipeline`, which names no kind.
    path: driverPath(group.pipeline),
    label: "Stages",
    kind: "parallel",
    state: driverState,
    children: stages,
    subtitle: "orchestrated subagents",
  };
  const out: ExecRun = {
    id: subtaskID,
    // The EXECUTION's name, which for a pipeline is the pipeline and not the stage the
    // tab happens to name. The header states the whole thing's progress and elapsed
    // time beside this label, so a stage's name here read as "review_gpt is on step 3
    // of 4" — measured in the sidecar. The delegate the reader opened is named by the
    // TAB and by the detail pane; this row is the one that has to be the container.
    label: "Subagent pipeline",
    state: driverState,
    // ONE stage renders no group row, the page's twin of the transcript's promotion:
    // a container over its only child is a wrapper rather than structure, and
    // `exec-view/page.ts` hides the tree, the detail-pane name row and the timeline
    // for exactly that. The pipeline's identity survives the row — its label and the
    // driver's Task input are `ExecRun` fields the page HEADER renders, and the run's
    // window comes from the LEAVES, never from a container's stamps.
    nodes: stages.length === 1 ? stages : [root],
    live: slice.live || inFlight(driverState),
    focus,
  };
  const inputs = driverInputs(group.driver);
  if (inputs !== undefined) {
    out.inputs = inputs;
  }
  return out;
}

/** A node's declared stage name, off its own facts. Reading it back rather than
 *  carrying a second field, since `ExecFact` is the model's channel for exactly this
 *  and a parallel array would be one more thing to keep aligned. */
function factStage(node: ExecNode): string {
  return node.facts?.find((f) => f.label === "Stage")?.value ?? "";
}

/** The stage names an `orchestrate_subagent` call declared.
 *
 *  Read through guards: `input` is whatever the model produced, so a `stages` that is
 *  not an array of named objects yields nothing rather than throwing on a page whose
 *  other half renders fine. Same posture `indexPlan` takes with `nodePlan`. */
function declaredStages(driver: ToolCall | undefined): string[] {
  const input = driver?.input;
  if (input === null || input === undefined || typeof input !== "object") {
    return [];
  }
  const stages = (input as Record<string, unknown>)["stages"];
  if (!Array.isArray(stages)) {
    return [];
  }
  const out: string[] = [];
  for (const s of stages) {
    if (s === null || typeof s !== "object") {
      continue;
    }
    const name = (s as Record<string, unknown>)["name"];
    if (typeof name === "string" && name !== "") {
      out.push(name);
    }
  }
  return out;
}
