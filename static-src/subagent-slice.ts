// ---------------------------------------------------------------------------
// subagent-slice: what ONE delegate is, projected out of a chat's messages.
//
// A subagent execution has no record of its own anywhere. There is no
// `/api/subagents/{id}`, no `subagent_*` SSE event and no store keyed by
// subtask: what exists is `agent_subtask_id` stamped on every block and every
// tool call the delegate produced, persisted in the chat file so it survives
// replay. So "the introspect subagent's output" is a QUERY over a conversation,
// and this module is that query.
//
// Pure and DOM-free, over `Message[]` plus a subtask id. Two consumers, which is
// the whole reason it is not a private helper in either of them: the tab factory
// (tab-materialize.ts) needs the delegate's LABEL for a tab restored on boot, and
// the subagent page (subagent-view.ts) needs its blocks. Neither can import the
// other, and neither can import the transcript's dispatcher, which reaches the
// whole message stack.
//
// It deliberately does NOT read the store. The caller supplies the messages,
// because the two callers subscribe to the store differently — the factory peeks
// untracked, the page reads inside an effect — and a module that fetched for them
// would make one of those wrong.
// ---------------------------------------------------------------------------

import type { Block, Message, ToolCall } from "./types.js";
import { isSubagentInvocation } from "./roles.js";
import { isToolActive } from "./tool-schema.js";
import { blockKey } from "./store-signals.js";

/** The prefix KAS puts on a PIPELINE STAGE's tool-call id. The full shape is
 *  `invoke_subagent_<orchestrateToolCallId>_stage_<stageName>`, so a stage names its
 *  driver in its own id — the same way a workflow step names its run in
 *  `wf:<workflowId>:<nodePath>`. No wire field carries this and none is needed;
 *  measured across 36 live chat files, 59 of 60 stage calls have that shape.
 *
 *  Duplicated from `messages-blocks.ts` deliberately rather than imported: that
 *  module reaches the whole transcript stack, and this one is a leaf two other leaves
 *  read. The pair is pinned by `subagent-slice.test.ts` against the same literals. */
const STAGE_PREFIX = "invoke_subagent_";
const STAGE_SEP = "_stage_";

/** The orchestrate tool-call id a stage belongs to, or "" when the id is not
 *  stage-shaped (a plain `invoke_sub_agent` call has no pipeline).
 *
 *  `indexOf` for the separator, because only the RIGHT half can contain one. A driver
 *  id is machine-minted by KAS and a stage name is author-supplied, so the first
 *  occurrence is the seam and a stage called `run_stage_two` still resolves to its own
 *  driver. Measured over the 65 distinct stage ids on the live volume: every driver
 *  half is a `toolu_bdrk_*` tool-use id, and stage names carry underscores freely
 *  (`review_fable-sub-agent-start`, `perf_hotfix-sub-agent-start`).
 *
 *  `messages-blocks.ts` `stagePipelineID` used `lastIndexOf` and its comment named
 *  exactly the case that breaks it; both were corrected together. Nothing on the
 *  volume trips it today (zero ids carry two separators), so the defect was latent:
 *  such a stage would have resolved to a driver that does not exist and rendered as a
 *  flat sibling of its own pipeline. */
export function pipelineOf(toolCallID: string): string {
  if (!toolCallID.startsWith(STAGE_PREFIX)) {
    return "";
  }
  const rest = toolCallID.slice(STAGE_PREFIX.length);
  const sep = rest.indexOf(STAGE_SEP);
  if (sep <= 0 || sep + STAGE_SEP.length >= rest.length) {
    return "";
  }
  return rest.slice(0, sep);
}

/** The stage's own name, from the same id. "" when it is not stage-shaped. */
export function stageName(toolCallID: string): string {
  if (pipelineOf(toolCallID) === "") {
    return "";
  }
  const rest = toolCallID.slice(STAGE_PREFIX.length);
  return rest.slice(rest.indexOf(STAGE_SEP) + STAGE_SEP.length);
}

/** One delegate's transcript, flattened out of the conversation that ran it. */
export interface SubagentSlice {
  /** The tool call that DISPATCHED the delegate, or undefined when the chat's
   *  resident window does not hold it. It carries the name, the status and the
   *  wall clock, so the page's header is built from it and from nothing else. */
  readonly invocation: ToolCall | undefined;
  /** The delegate's own blocks, in conversation order, with `agent_subtask_id`
   *  CLEARED and the invocation dropped.
   *
   *  Both edits are what make this render as a main agent's transcript rather
   *  than as a box inside one: the dispatcher routes a block by its subtask id,
   *  so a block that still carried one would rebuild the collapsed card, and the
   *  invocation would come back as an ordinary tool row when it is the page's
   *  own header. */
  readonly blocks: Block[];
  /** Every tool call the blocks above reference, by identity.
   *
   *  Carried rather than looked up later because the dispatcher resolves a
   *  `tool_use` block through `message.tool_calls` and silently renders NOTHING
   *  for a call it cannot find — so a slice with blocks and no calls is a blank
   *  page, not a degraded one. */
  readonly toolCalls: ToolCall[];
  /** The per-(message, block-index) streaming keys of the blocks above, index
   *  aligned with `blocks`.
   *
   *  The REAL coordinates, deliberately, not the synthetic ones the page renders
   *  under: a text delta writes `blockTextSigs` at the real key and never bumps
   *  the coarse message signal, so a reader that wants a delegate's prose to
   *  stream has to subscribe to exactly these. Producing them here is what keeps
   *  the page from having to carry the source pairing itself. */
  readonly sourceKeys: string[];
  /** Whether the delegate is still working.
   *
   *  From the INVOCATION's own status when it is resident, because that is the
   *  fact — a delegate can finish while its conversation carries on for another
   *  ten minutes, and reading the chat's `thinking` flag would leave a settled
   *  delegate under a streaming caret. The chat's flag is only the fallback for a
   *  delegate whose invocation has been paged out. */
  readonly live: boolean;
}

/** The tool call that dispatched `subtaskID`, or undefined.
 *
 *  Exported on its own because the tab factory wants only this: a label for a
 *  row, with no reason to walk the blocks. */
export function findSubagentInvocation(
  messages: readonly Message[],
  subtaskID: string,
): ToolCall | undefined {
  if (subtaskID === "") {
    return undefined;
  }
  for (const m of messages) {
    for (const tc of m.tool_calls ?? []) {
      if ((tc.agent_subtask_id ?? "") === subtaskID && isSubagentInvocation(tc)) {
        return tc;
      }
    }
  }
  return undefined;
}

/** Project one delegate out of a conversation.
 *
 *  Walks EVERY message rather than stopping at the first match, because a turn
 *  split by a mid-turn model switch puts one delegate's blocks in two assistant
 *  messages, and a slice that stopped early would silently truncate its output.
 *
 *  `chatLive` is the fallback for `live` only — see that field's own note. */
export function sliceSubagent(
  messages: readonly Message[],
  subtaskID: string,
  chatLive: boolean,
): SubagentSlice {
  const blocks: Block[] = [];
  const toolCalls: ToolCall[] = [];
  const sourceKeys: string[] = [];
  const invocation = findSubagentInvocation(messages, subtaskID);
  const live = invocation === undefined ? chatLive : isToolActive(invocation.status);
  if (subtaskID === "") {
    return { invocation: undefined, blocks, toolCalls, sourceKeys, live };
  }
  const invocationID = invocation?.id ?? "";
  for (const m of messages) {
    const own = m.blocks ?? [];
    for (let i = 0; i < own.length; i++) {
      const block = own[i];
      if (block === undefined || (block.agent_subtask_id ?? "") !== subtaskID) {
        continue;
      }
      // The invocation is the PAGE's header, so its block does not become a row.
      if (block.type === "tool_use" && (block.tool_call_id ?? "") === invocationID) {
        continue;
      }
      // A fresh object with the attribution removed: mutating the store's block
      // would delete the id the TRANSCRIPT groups on, so the inline card would
      // scatter its contents the moment this page was opened.
      const { agent_subtask_id: _dropped, ...rest } = block;
      blocks.push(rest);
      sourceKeys.push(blockKey(m.id, i));
    }
    for (const tc of m.tool_calls ?? []) {
      if ((tc.agent_subtask_id ?? "") === subtaskID && tc.id !== invocationID) {
        toolCalls.push(tc);
      }
    }
  }
  return { invocation, blocks, toolCalls, sourceKeys, live };
}

/** A shape signature over a slice's blocks, for deciding whether a mounted
 *  render can be UPDATED or has to be rebuilt.
 *
 *  The dispatcher's incremental update appends blocks past a watermark, so it is
 *  only correct while the prefix it already mounted is unchanged. Growth at the
 *  tail keeps it; a rewind, a refetch or a turn restructured underneath does not,
 *  and those are the only things that can shorten or reorder this list. Comparing
 *  the prefix is what tells the two apart without throwing away a reader's scroll
 *  position on every streamed chunk. */
export function blockShape(blocks: readonly Block[]): string[] {
  return blocks.map((b) => `${b.type}\u0000${b.tool_call_id ?? ""}`);
}

/** Whether `next` extends `prev` rather than replacing it. */
export function shapeExtends(prev: readonly string[], next: readonly string[]): boolean {
  if (next.length < prev.length) {
    return false;
  }
  for (let i = 0; i < prev.length; i++) {
    if (prev[i] !== next[i]) {
      return false;
    }
  }
  return true;
}

/** One member of a PIPELINE: a delegate plus the identity its stage id carries. */
export interface SubagentMember {
  /** The delegate's `agent_subtask_id`. */
  subtaskID: string;
  /** The stage's author-given name, or "" for a delegate that is not a stage. */
  stage: string;
  invocation: ToolCall;
}

/** What a delegate BELONGS to, which decides whether its page is a tree or a leaf.
 *
 *  `pipeline` is the driver's tool-call id when the delegate is a stage of one, and
 *  `members` are every stage of that pipeline in conversation order, the requested
 *  one included. Both empty for a plain `invoke_sub_agent`, which is the single-leaf
 *  case and the one the exec page renders with no tree pane at all. */
export interface SubagentGroup {
  pipeline: string;
  /** The `orchestrate_subagent` call that opened the pipeline, when it is resident.
   *  It carries the declared stage list, so it is what names a stage the transcript
   *  has not reached yet. */
  driver: ToolCall | undefined;
  members: SubagentMember[];
}

/** The title the `orchestrate_subagent` driver call carries. */
const PIPELINE_TITLE = "Orchestrate Sub-agent";

/** Resolve the pipeline a delegate belongs to, and its siblings.
 *
 *  Driven off the tool-call array rather than the blocks, for the reason
 *  `indexPipelines` is: a stage's blocks carry only a bare subtask uuid, which names
 *  nothing, while the stage's INVOCATION carries both that uuid and its driver's id.
 *  Scanning the whole array also makes the two arrival orders equivalent — the driver
 *  and its first stage race on the wire, and after a reload the driver is persisted
 *  while live blocks are not. */
export function groupOf(messages: readonly Message[], subtaskID: string): SubagentGroup {
  const own = findSubagentInvocation(messages, subtaskID);
  const pipeline = own === undefined ? "" : pipelineOf(own.id);
  if (pipeline === "") {
    return { pipeline: "", driver: undefined, members: [] };
  }
  const members: SubagentMember[] = [];
  const seen = new Set<string>();
  let driver: ToolCall | undefined;
  for (const m of messages) {
    for (const tc of m.tool_calls ?? []) {
      if (tc.id === pipeline && tc.title === PIPELINE_TITLE) {
        driver = tc;
        continue;
      }
      const sub = tc.agent_subtask_id ?? "";
      if (sub === "" || seen.has(sub) || pipelineOf(tc.id) !== pipeline) {
        continue;
      }
      if (!isSubagentInvocation(tc)) {
        continue;
      }
      seen.add(sub);
      members.push({ subtaskID: sub, stage: stageName(tc.id), invocation: tc });
    }
  }
  return { pipeline, driver, members };
}
