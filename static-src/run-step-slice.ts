// ---------------------------------------------------------------------------
// run-step-slice: one WORKFLOW RUN's steps, projected out of the launching
// chat's messages.
//
// A chat-parented run executes on the launching chat's session, so every block
// and every tool call its steps produce arrives on that chat's connection and is
// stamped `wf:<workflowId>:<nodePath>` (`internal/translate/wire.go`
// ACPWorkflowMeta.SubtaskID). The run's own tab used to be unable to read any of
// it — `run_step` is emitted for PARENTLESS runs only — so a reader who watched a
// run from its sub-tab saw the plan, the timings and the captured output, and
// nothing of how any step got there. This module is the query that closes that:
// the blocks themselves, keyed by node path so the sub-tab's detail pane can host
// them. It is the ONLY surface that draws them — the transcript's run card is that
// run's record and renders no step content (see `blocks` below).
//
// `subagent-slice.ts` is the exact precedent and this is its sibling; the four
// rules below are carried across with their reasons rather than rediscovered.
// Pure and DOM-free, over `readonly Message[]` plus a workflow id, so the caller
// owns how it reads the store — `run-view.ts` reads inside an effect, exactly as
// the subagent page does.
// ---------------------------------------------------------------------------

import type { Block, Message, ToolCall } from "./types.js";
import { blockKey } from "./store-signals.js";
import { STEP_PREFIX, parseStepSubtask } from "./step-subtask.js";

/** One step's transcript, flattened out of the conversation that ran it. */
export interface RunStepSlice {
  /** The step's own blocks, in conversation order, with `agent_subtask_id`
   *  CLEARED.
   *
   *  Clearing it is what makes these render at all: `messages-blocks.ts`
   *  `placeBlock` DROPS a block whose id parses as a `wf:` step, because the
   *  transcript's run card is that run's record and renders no step content — so a
   *  block still carrying one would reach this page and be discarded. Fresh objects,
   *  because mutating the store's block would take the id the run TAB slices on out
   *  of the chat file's copy. */
  readonly blocks: Block[];
  /** Every tool call the blocks above reference, by identity.
   *
   *  Carried rather than looked up later because `placeBlock`'s `tool_use` arm
   *  resolves a call through `message.tool_calls` and returns silently when it is
   *  absent — so a slice with blocks and no calls is a BLANK pane, not a degraded
   *  one. */
  readonly toolCalls: ToolCall[];
  /** The per-(message, block-index) streaming keys of the blocks above, index
   *  aligned with `blocks`.
   *
   *  The REAL coordinates rather than the synthetic ones the page renders under,
   *  because that is how `blockTextSigs` is keyed: a reader wanting a step's prose
   *  to stream in the same tick as its delta subscribes to exactly these. */
  readonly sourceKeys: string[];
  /** Whether more content may still arrive on this channel.
   *
   *  The CHAT's own liveness, because this channel has no per-step signal: a
   *  step's blocks arrive on the launching chat's stream and nothing in a block
   *  says the step has finished. The NODE's `ExecState` is the per-step fact and
   *  the consumer already holds it. */
  readonly live: boolean;
}

/** The `agent_subtask_id` a step's blocks carry.
 *
 *  Built through `step-subtask.ts`'s own prefix rather than a second `"wf:"`
 *  literal, so the two halves of the join cannot drift. */
export function stepSubtaskID(workflowID: string, nodePath: string): string {
  return `${STEP_PREFIX}${workflowID}:${nodePath}`;
}

interface Draft {
  blocks: Block[];
  toolCalls: ToolCall[];
  sourceKeys: string[];
}

/** Project one run's steps out of a conversation, keyed by node path.
 *
 *  ONE walk answers for every step, because a run's blocks are interleaved with
 *  the chat's own and with each other — a `parallel` node has several steps
 *  streaming at once — so a per-step walk would be N passes over the same window.
 *
 *  Walks EVERY message rather than stopping at the first match, because a turn
 *  split by a mid-turn model switch puts one step's blocks in two assistant
 *  messages and a slice that stopped early would silently truncate its output.
 *
 *  A malformed `wf:` id yields no entry at all: `parseStepSubtask` returns null,
 *  which is the same fall-through the transcript takes, rather than keying a bogus
 *  path that would mint an orphan host. */
export function sliceRunSteps(
  messages: readonly Message[],
  workflowID: string,
  chatLive: boolean,
): Map<string, RunStepSlice> {
  const drafts = new Map<string, Draft>();
  if (workflowID === "") {
    return new Map();
  }

  const draftFor = (nodePath: string): Draft => {
    let d = drafts.get(nodePath);
    if (d === undefined) {
      d = { blocks: [], toolCalls: [], sourceKeys: [] };
      drafts.set(nodePath, d);
    }
    return d;
  };

  /** The node path this subtask id names WITHIN this run, or "" for anything
   *  else — another run's step, a subagent's uuid, the chat's own work. */
  const pathOf = (subtask: string | undefined): string => {
    const parsed = parseStepSubtask(subtask ?? "");
    return parsed?.workflowID === workflowID ? parsed.nodePath : "";
  };

  for (const m of messages) {
    const own = m.blocks ?? [];
    for (let i = 0; i < own.length; i++) {
      const block = own[i];
      if (block === undefined) {
        continue;
      }
      const path = pathOf(block.agent_subtask_id);
      if (path === "") {
        continue;
      }
      const { agent_subtask_id: _dropped, ...rest } = block;
      const d = draftFor(path);
      d.blocks.push(rest);
      d.sourceKeys.push(blockKey(m.id, i));
    }
    for (const tc of m.tool_calls ?? []) {
      const path = pathOf(tc.agent_subtask_id);
      if (path === "") {
        continue;
      }
      draftFor(path).toolCalls.push(tc);
    }
  }

  const out = new Map<string, RunStepSlice>();
  for (const [path, d] of drafts) {
    out.set(path, { ...d, live: chatLive });
  }
  return out;
}
