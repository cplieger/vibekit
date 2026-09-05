// ---------------------------------------------------------------------------
// Assistant body composition — the ONE block dispatcher.
//
// The store guarantees every assistant message carries a chronological
// `blocks` array (text / thinking / tool_use), each stamped with an
// `agent_subtask_id` (empty for the parent agent, set for a subagent). This
// module is the single path that turns that array into DOM, composed entirely
// from the fundamentals/ primitives:
//
//   text     → fundamentals/text-bubble  (streaming markdown or replay)
//   thinking → fundamentals/reasoning     (auto-collapses on first sibling text)
//   tool_use → tool-card (grouped per-container via tool-group), OR
//              fundamentals/todo          (the todo_list tool → a checklist), OR
//              fundamentals/subagent-block (the invoke_sub_agent tool → a
//                                           collapsible block whose body hosts
//                                           the subagent's own blocks, rendered
//                                           by this same dispatcher — full parity)
//
// Blocks sharing a non-empty agent_subtask_id are grouped into one
// SubagentBlock, whether or not they are adjacent: the card is keyed by subtask
// id in a per-message Map that nothing closes, so a delegate's blocks join the
// card it opened even when the parent agent emitted something between them. The
// consequence worth knowing is ORDER, not grouping — the card is appended at the
// subtask's FIRST appearance, so a parent block that arrived between two of a
// delegate's blocks renders AFTER the whole card. (Tool GROUPS are the opposite
// and deliberately so: `toolGroups` is keyed by container and any text block
// closes it, so those really are contiguous runs.) The subagent's tool cards /
// reasoning / text render inside its card exactly as they do at the top level.
// There is no separate legacy path and no text-preview fold.
//
// A PIPELINE (the `orchestrate_subagent` tool) nests, and it is the one delegate
// relationship the wire states outright. KAS emits three kinds of tool call for
// one pipeline: the `Orchestrate Sub-agent` driver with NO subtask of its own,
// then per stage a `Sub-agent: <name>` invocation carrying a fresh subtask uuid,
// then that stage's own work under the same uuid. The stage's tool-call ID is
// `invoke_subagent_<orchestrateToolCallId>_stage_<stageName>`, so the stage names
// its parent and `indexPipelines` reads the join straight off it — no wire field
// was added. The driver becomes the PIPELINE box's header and each stage's box is
// appended into that box's body, which is the same one-invocation-hosts-N-children
// shape the run card uses for workflow steps.
//
// EXCEPT at exactly ONE stage, which renders no container: that stage's own card
// is promoted where the container would have gone. See `pipelineHasContainer`.
// ---------------------------------------------------------------------------

import type { Message, Block, ToolCall, PlanStatus, FileChange, SteerMark } from "./types.js";
import type { BlockRange } from "./block-window.js";
import { effect, el } from "@cplieger/reactive";
import { KEY_ATTR as RECONCILE_KEY } from "./reconcile.js";
import { getActiveId } from "./store.js";
import {
  ensureBlockTextSig,
  ensureBlockThinkingSig,
  ensureToolCallSig,
  peekToolCallSig,
  clearToolCallSig,
  clearBlockSig,
} from "./store-signals.js";
import { recordBlockHeight } from "./block-heights.js";
import { lineDelta } from "./diff.js";
import { isInternalToolTitle, isToolActive } from "./tool-schema.js";
import type { TurnSummaryData } from "./fundamentals/turn-footer.js";
import {
  buildAssistantBubble,
  type AssistantBubble,
  type AssistantBubbleOpts,
} from "./fundamentals/text-bubble.js";
import { buildReasoning, type ReasoningView } from "./fundamentals/reasoning.js";
import {
  buildSubagentBlock,
  type SubagentOpener,
  type SubagentView,
} from "./fundamentals/subagent-block.js";
import { buildTodoList, updateTodoList, type TodoItem } from "./fundamentals/todo.js";
import { buildSteerNote } from "./fundamentals/steer-note.js";
import { mountToolCallCard, disposeToolSlot } from "./messages-tools.js";
import { expandToolDetails } from "./tool-card.js";
import { planElement, updatePlanElement } from "./messages-plan.js";
import {
  buildToolGroupShell,
  groupBody,
  refreshGroupHeader,
  autoCollapseGroup,
} from "./tool-group.js";

// Re-exported so messages.ts can inject it into messages-tools' status-flip
// path (initToolCallbacks) — the same header renderer the block dispatcher uses.
export { refreshGroupHeader };
import { iconForSubagent, isSubagentInvocation, subagentLabel, subagentName } from "./roles.js";
import { parseStepSubtask, type StepSubtask } from "./step-subtask.js";
import { buildRunCard, type RunCardView, type RunDisclosure } from "./fundamentals/run-card.js";
import { invalidateRun, runState, forgetRun } from "./run-store.js";
import { runPendingAsks } from "./decision-dock.js";
import { hasTab } from "./tabs.js";
import { buildPath } from "./router.js";

// ---------------------------------------------------------------------------
// Callbacks injected by messages.ts (kept there so avatar markup + the
// streaming-effect registry live in one place).
// ---------------------------------------------------------------------------

interface BlockCbs {
  /** Register a cleanup disposed on turn finalize / message unmount. */
  pushStreamingEffect(msgId: string, cleanup: () => void): void;
  /** Register a cleanup disposed when this BLOCK leaves the window. */
  pushBlockEffect(msgId: string, blockIndex: number, cleanup: () => void): void;
  /** Run the cleanups for the blocks a window drop removed: the drop's half of
   *  `pushBlockEffect`'s contract. */
  disposeBlockEffects(msgId: string, indices: Iterable<number>): void;
  /** Build an avatar row for a top-level assistant bubble. */
  makeRow(): HTMLDivElement;
  /** Put an undelivered steer's text back in the message box. Injected rather
   *  than imported so `fundamentals/` and this dispatcher keep pointing
   *  downward — the composer is above them both. */
  restoreSteer(text: string): void;
}

let cbs: BlockCbs = {
  pushStreamingEffect: () => {
    /* until init */
  },
  pushBlockEffect: () => {
    /* until init */
  },
  disposeBlockEffects: () => {
    /* until init */
  },
  makeRow: () => el("div") as HTMLDivElement,
  restoreSteer: () => {
    /* until init */
  },
};

export function initBlockRenderer(c: BlockCbs): void {
  cbs = c;
}

// ---------------------------------------------------------------------------
// The live-anchor registry: which bubble Following pins to.
//
// A last-writer-wins slot, maintained where `.streaming` is granted and
// revoked, so the follow path costs one field read instead of a whole-tree
// selector walk per frame. Two rules carry over from the walk it replaced,
// both because a pin above the live edge strands the reader mid-transcript:
//
//   - THE NEWEST top-level bubble wins (registration order is mount order, so
//     last writer IS the newest). `sealLiveBubble` is per-MESSAGE state, so a
//     turn split across two assistant messages (a mid-turn model switch does
//     that) leaves the earlier message's trailing bubble streaming until the
//     turn finalizes — when the newer seals first, the CLEAR falls back to
//     that older survivor by scanning the render map.
//   - Delegate-hosted bubbles NEVER register. A subagent's or workflow step's
//     live text streams inside a box that is collapsed by default, and
//     `height: 0` + `overflow: hidden` clips that content without taking it
//     out of layout — so it reports offsets the reader cannot see. With no
//     top-level bubble streaming, null is the right answer: the document
//     bottom is where the box's own rolling tail and its footer are.
// ---------------------------------------------------------------------------

let liveAnchor: { messageID: string; el: HTMLElement } | null = null;

/** The element Following pins to, or null for the document bottom.
 *
 *  Self-healing: a mid-turn rebuild replaces a message's render, and the OLD
 *  bubble's seal belongs to the old render — so the slot can keep pointing at
 *  an element no longer in the transcript, whose offsetTop/offsetHeight read 0
 *  and pin the follow scroll to the TOP of the transcript. Measured live
 *  (2026-08-31): a mid-stream repaint snapped scrollTop 786 → 0 while the turn
 *  kept streaming. The registry is the truth: when the anchor is not its own
 *  message's CURRENT top-level live bubble, re-derive from the render map,
 *  exactly like clearLiveAnchor. */
export function getLiveAnchor(): HTMLElement | null {
  if (liveAnchor !== null && renders.get(liveAnchor.messageID)?.topLiveEl !== liveAnchor.el) {
    liveAnchor = null;
    rescanLiveAnchor();
  }
  return liveAnchor?.el ?? null;
}

/** Point the slot at the newest still-live top-level bubble of the active
 *  chat, or leave it null. Registration order is mount order, so the last
 *  match is the newest. */
function rescanLiveAnchor(): void {
  const activeChat = getActiveId();
  for (const [id, st] of renders) {
    if (!st.detached && st.chatID === activeChat && st.topLiveEl !== null) {
      liveAnchor = { messageID: id, el: st.topLiveEl };
    }
  }
}

/** Identity-guarded clear: only the registered element's own seal clears the
 *  slot, then the newest still-live top-level bubble (if any) takes it back.
 *  Only the ACTIVE chat's renders are candidates: with parked views resident,
 *  another chat's still-live bubble is DOM the reader cannot see, and a pin to
 *  it would strand Following on a hidden element. */
function clearLiveAnchor(el: HTMLElement): void {
  if (liveAnchor?.el !== el) {
    return;
  }
  liveAnchor = null;
  rescanLiveAnchor();
}

// ---------------------------------------------------------------------------
// The open-container registry: what the reader left each collapsible container
// at, keyed by its own id under `sub:`, `pipe:`, `run:`, `step:` and `tool:`.
// Read at MOUNT too — a window drop keeps these keys, so a creation site
// restores the reader's choice, not its own default. A DETACHED render (the
// subagent page) neither registers nor restores.
// ---------------------------------------------------------------------------

const openContainers = new Map<string, boolean>();

function setContainerOpen(key: string, open: boolean): void {
  openContainers.set(key, open);
}

/** What the reader last left `key` at, or undefined when nothing has recorded it
 *  — which is NOT the same as closed. The creation site owns the default. */
function containerOpen(key: string): boolean | undefined {
  return openContainers.get(key);
}

/** The subtask ids whose container chain is open, in the id shape blocks carry:
 *  a delegate's uuid, or `wf:<workflowId>:<nodePath>` for a workflow step. */
export function openContainerKeys(): ReadonlySet<string> {
  const out = new Set<string>();
  for (const [key, open] of openContainers) {
    if (!open) {
      continue;
    }
    if (key.startsWith("sub:")) {
      out.add(key.slice(4));
      continue;
    }
    if (key.startsWith("step:")) {
      const rest = key.slice(5);
      const sep = rest.indexOf(":");
      if (sep > 0 && runCardOpen(rest.slice(0, sep))) {
        out.add(`wf:${rest}`);
      }
    }
  }
  return out;
}

/** Whether `workflowID`'s card counts as open. The one container that mounts OPEN,
 *  so an absent key means open here and only a reader who collapsed it says
 *  otherwise — the rule both the step-row join above and the card's own re-mount
 *  read. */
function runCardOpen(workflowID: string): boolean {
  return containerOpen(`run:${workflowID}`) !== false;
}

/** Carry a dropped tool card's own disclosure into the registry, so the re-mount
 *  restores what the reader chose. `aria-expanded` on `.tool-disclosure` is the only
 *  record a card's details were opened — the boxes above have keys, a card had
 *  nothing. */
function recordDisclosure(el: HTMLElement, block: Block | undefined): void {
  const toolID = block?.type === "tool_use" ? (block.tool_call_id ?? "") : "";
  const toggle = el.querySelector<HTMLElement>(".tool-disclosure");
  if (toolID !== "" && toggle !== null) {
    setContainerOpen(`tool:${toolID}`, toggle.getAttribute("aria-expanded") === "true");
  }
}

/** Drop a render's container keys. `runs` prefix-deletes its step rows too. */
function pruneContainers(st: MsgRender): void {
  if (st.detached) {
    return; // never registered
  }
  for (const tc of st.tools) {
    openContainers.delete(`tool:${tc.id}`);
  }
  for (const subtask of st.subagents.keys()) {
    openContainers.delete(`sub:${subtask}`);
  }
  for (const pipelineID of st.pipelines.keys()) {
    openContainers.delete(`pipe:${pipelineID}`);
  }
  for (const runID of st.runs.keys()) {
    openContainers.delete(`run:${runID}`);
    const prefix = `step:${runID}:`;
    for (const key of openContainers.keys()) {
      if (key.startsWith(prefix)) {
        openContainers.delete(key);
      }
    }
  }
}

// ---------------------------------------------------------------------------
// Per-message render state
// ---------------------------------------------------------------------------

interface MsgRender {
  /** The chat whose messages this render belongs to.
   *
   *  Carried rather than read from the store, because the per-tool signal is
   *  keyed on (chat, call) and this render is not always the active chat's: the
   *  subagent page renders one delegate of whatever chat its tab names. Reading
   *  the active chat here would key the page's cards under a chat that never
   *  writes them, so they would mount and never update. */
  chatID: string;
  /** The `.assistant-blocks` container holding all top-level + subagent blocks. */
  blocksEl: HTMLElement;
  /** The block indices this render currently holds. Widened on both edges by
   *  `renderRange`; `dropBlockRange` is the only writer that narrows it. */
  window: BlockRange;
  /** block index → the element whose removal drops that block, and the ONLY
   *  block→element mapping: an index is unique per MESSAGE and not per DOM subtree,
   *  because `runCardFor` routes a later message's steps into the first's card. */
  blockEls: Map<number, HTMLElement>;
  /** block index → a call that brings that block's DOM up to a full text. Wraps
   *  the handle's `setText` in an arrow rather than storing the method itself:
   *  both handles keep their state in a closure and never read `this`, but a
   *  detached method reference is a shape the linter rightly refuses to take on
   *  trust. Read by `syncMountedText`. */
  blockText: Map<number, (full: string) => void>;
  /** subtask id → its SubagentBlock view. */
  subagents: Map<string, SubagentView>;
  /** orchestrate tool-call id → the PIPELINE box that call opened. Keyed by the
   *  invocation, not by a subtask, because the orchestrate call has none of its
   *  own — the stages do. Same shape as `runs` below and for the same reason:
   *  one box holds every stage of one pipeline. */
  pipelines: Map<string, SubagentView>;
  /** stage subtask id → the orchestrate tool-call id that owns it. Built by
   *  `indexPipelines` from the message's tool calls, which is what lets a stage's
   *  TEXT block (carrying only the bare subtask uuid) find its pipeline. */
  stagePipeline: Map<string, string>;
  /** orchestrate tool-call id → its stage subtask ids, in first-seen order. The
   *  pipeline footer's ledger sums over these. */
  pipelineStages: Map<string, string[]>;
  /** orchestrate tool-call id → its declared `stages` length, `0` when that field is
   *  absent or malformed, and a FLOOR rather than the answer (`pipelineStageCount`).
   *  Complete on arrival: the server writes `ToolCall.Input` on the create frame and
   *  never on an update, so there is no partial-input window. */
  pipelineDeclared: Map<string, number>;
  /** workflow id → the run card THIS render hosts. Keyed by RUN, not by
   *  subtask, because one card holds every step of one run — the step rows inside
   *  it are keyed by node path (see `runContainerFor`). Only the HOST render of a
   *  run has an entry (`runCardHosts`); a later message of the same chat routes
   *  into the host's card and holds nothing here. */
  runs: Map<string, RunCardView>;
  /** workflow id → the ARMED render effect's disposer, absent while the card is
   *  suspended (view parked). Beside `runs` rather than inside `disposers`
   *  because pause has to stop the effect and release the clock WITHOUT running
   *  the card's final dispose, and resume has to re-arm exactly what pause
   *  stopped. */
  runEffects: Map<string, () => void>;
  /** Cleanups that outlive the TURN, bucketed by block index with `-1` for the
   *  message's own: a window drop drains only the buckets it removed, `disposeAll`
   *  drains them all. Separate from `pushStreamingEffect`, which is disposed at turn
   *  end — right for a caret, wrong for a run card, whose run carries on for minutes
   *  after `run_workflow` returns. */
  disposers: Map<number, (() => void)[]>;
  /** subtask id → the tool-call ids routed into that box, for the footer's
   *  ledger (commands, reads, changed files). The INVOCATION call is not a
   *  member — it is the box itself. */
  subagentMembers: Map<string, Set<string>>;
  /** Whether this render lives OUTSIDE the transcript.
   *
   *  One render does: the subagent page, which renders one delegate's blocks on
   *  its own tab. Two things change for it, and both are correctness rather than
   *  tidiness.
   *
   *  Turn-lifetime cleanups go into `disposers` instead of messages.ts's registry,
   *  because messages.ts has never heard of this render and would never fire them
   *  — the page owns its own unmount.
   *
   *  And a cleanup must not CLEAR a shared per-tool signal. The page and the
   *  transcript render the same `ToolCall` ids, so whichever surface unmounted
   *  first would drop the signal the other is still reading, quietly demoting it
   *  to the full-repaint fallback. */
  detached: boolean;
  /** Every mounted bubble handle (for finalize end()). */
  bubbles: AssistantBubble[];
  /** The one bubble currently carrying the streaming caret, or null. The caret
   *  is a `::after` on `.message.assistant.streaming`, and `bubbles` is
   *  append-only with no notion of which entry is at the tail — so without this
   *  pointer nothing can seal the block that WAS the tail when a new one opens,
   *  and each streamed block keeps its caret for the rest of the turn. */
  liveBubble: AssistantBubble | null;
  /** This message's TOP-LEVEL streaming bubble root, or null. The live-anchor
   *  registry's per-message half: registration writes it, the bubble's own
   *  seal clears it (identity-guarded), and the anchor's fallback scan reads
   *  it to find the newest split-turn survivor. Delegate-hosted bubbles never
   *  set it. */
  topLiveEl: HTMLElement | null;
  /** Every mounted reasoning handle (for finalize seal()). */
  reasonings: ReasoningView[];
  /** A container's genuinely-live TRAILING reasoning trace. A trace the store
   *  already has a successor for is sealed at its own mount, so head insertion
   *  never reaches this map. */
  openReasoning: Map<HTMLElement, ReasoningView>;
  /** container key → its tool groups, keyed by the STORE run each one opened at, so
   *  which group a card joins is a function of the store instead of mount order.
   *  Outer key is the container's KEY, a bijection with its element inside one
   *  render. */
  toolGroups: Map<string, Map<number, HTMLDivElement>>;
  /** Container key (`sub:`/`pipe:`/`run:`) → the STORE index that ESTABLISHES it, which is
   *  where its box belongs however far down the range first reached it. `indexGroups`
   *  writes it, `placeContainer` reads it, and both mean the same index. */
  containerAt: Map<string, number>;
  /** Steer-mark id → the block index it is anchored at and the note element.
   *  The anchor is what makes a note droppable, the element what makes it
   *  removable; the KEY is what makes `flushSteerNotes` idempotent across its
   *  two deliberately-overlapping call sites. */
  steerNotes: Map<string, { index: number; el: HTMLElement }>;
  /** Where a block being INSERTED goes in its container: before this node. A Map
   *  for the duration of a HEAD extension and null otherwise, so the append path
   *  is byte-identical outside one. Null is also what tells `appendBlock` not to
   *  seal: an inserted block is posted after nothing. */
  insertBefore: Map<HTMLElement, HTMLElement | null> | null;
  /** This render's own key in `renders`, which for a detached render is the
   *  derived id rather than the bare message id. */
  msgID: string;
  /** This message's blocks and tool calls, as of the current pass. Held rather
   *  than passed because the three LAZY container creators are reached from a
   *  range that need not contain the invocation block, and each has to bind itself
   *  from the call or the box renders with a generic header and no ledger. */
  tools: readonly ToolCall[];
  blocks: readonly Block[];
  /** Invocation tool-call ids whose box is already bound. A box can be bound by
   *  its own in-window invocation block OR lazily at creation from `tools`, so
   *  this is what makes both paths idempotent. */
  boundBoxes: Set<string>;
}

const renders = new Map<string, MsgRender>();

/** chat id → workflow id → the render whose message HOSTS that run's card.
 *
 *  The transcript-level half of `MsgRender.runs`: a run's frames span several
 *  messages, and this is what routes every later message's steps into the card
 *  the first one built. Claimed at build, released by the host's own disposer.
 *  Detached renders are never in it — the subagent page is its own surface, and
 *  adopting the transcript's card would move the DOM node out of it. */
const runCardHosts = new Map<string, Map<string, MsgRender>>();

// ---------------------------------------------------------------------------
// Public API (called by messages.ts)
// ---------------------------------------------------------------------------

/** The whole of `m`, for a caller that windows nothing. */
function wholeOf(m: Message): BlockRange {
  return { from: 0, to: (m.blocks ?? []).length };
}

/** Build the assistant body from scratch over `range`, then the plan. `range`
 *  absent is the whole message, which is what a detached render always wants. */
export function buildAssistantBody(
  wrap: HTMLElement,
  m: Message,
  chatID: string,
  live: boolean,
  marks: readonly SteerMark[] = [],
  range?: BlockRange,
): void {
  buildBody(wrap, m, chatID, live, false, marks, range);
  mountPlan(wrap, m);
}

function buildBody(
  wrap: HTMLElement,
  m: Message,
  chatID: string,
  live: boolean,
  detached: boolean,
  marks: readonly SteerMark[] = [],
  range?: BlockRange,
): void {
  const want = range ?? wholeOf(m);
  const blocksEl = el("div", { className: "assistant-blocks" });
  wrap.appendChild(blocksEl);
  const st: MsgRender = {
    chatID,
    blocksEl,
    window: { from: want.from, to: want.from },
    blockEls: new Map(),
    blockText: new Map(),
    subagents: new Map(),
    pipelines: new Map(),
    stagePipeline: new Map(),
    pipelineStages: new Map(),
    pipelineDeclared: new Map(),
    runs: new Map(),
    runEffects: new Map(),
    disposers: new Map(),
    subagentMembers: new Map(),
    detached,
    bubbles: [],
    liveBubble: null,
    topLiveEl: null,
    reasonings: [],
    openReasoning: new Map(),
    toolGroups: new Map(),
    containerAt: new Map(),
    steerNotes: new Map(),
    insertBefore: null,
    msgID: m.id,
    tools: m.tool_calls ?? [],
    blocks: m.blocks ?? [],
    boundBoxes: new Set(),
  };
  renders.set(m.id, st);
  indexPipelines(st, m);
  // ONE index per pass: the mount and the collapse sync ask it different
  // questions about the same run boundaries.
  const idx = indexGroups(st, m, marks, live);
  renderRange(st, m, want.from, want.to, live, marks, idx);
  syncGroupCollapse(st, idx);
}

/** Where a mount's turn-lifetime cleanup goes.
 *
 *  A transcript render hands it to messages.ts, which disposes at TURN END as
 *  well as on unmount. A DETACHED render keeps it, because messages.ts does not
 *  know the render exists — and because a detached cleanup that cleared a shared
 *  signal would reach into the transcript's own live cards.
 *
 *  A detached render also creates no PER-BLOCK signal (see mountText): its message
 *  id is synthetic, so `ensureBlockTextSig` would mint a key `store.appendChunk`
 *  never writes, and a bubble subscribed to it would sit frozen while the real
 *  block streamed. The page subscribes to the REAL keys instead and pushes the
 *  text in through `syncMountedText`, which is the same fallback a mis-judged
 *  live block already relies on. */
function pushLifetimeEffect(
  st: MsgRender,
  msgId: string,
  blockIndex: number,
  cleanup: () => void,
): void {
  if (st.detached) {
    pushDisposer(st, blockIndex, cleanup);
    return;
  }
  cbs.pushBlockEffect(msgId, blockIndex, cleanup);
}

/** Add a cleanup to `blockIndex`'s bucket; `-1` is the message's own. */
function pushDisposer(st: MsgRender, blockIndex: number, cleanup: () => void): void {
  const arr = st.disposers.get(blockIndex);
  if (arr === undefined) {
    st.disposers.set(blockIndex, [cleanup]);
  } else {
    arr.push(cleanup);
  }
}

/** Run and drop `blockIndex`'s bucket. */
function runDisposers(st: MsgRender, blockIndex: number): void {
  const arr = st.disposers.get(blockIndex);
  if (arr === undefined) {
    return;
  }
  st.disposers.delete(blockIndex);
  for (const fn of arr) {
    fn();
  }
}

/** Clear a per-tool signal, unless this render shares it with the transcript. */
function releaseToolSig(st: MsgRender, toolID: string): void {
  if (!st.detached) {
    clearToolCallSig(st.chatID, toolID);
  }
}

/** Incrementally sync the assistant body: mount newly-arrived blocks and steer
 *  notes, bring already-mounted blocks up to the store's text, update the plan. */
export function updateAssistantBody(
  wrap: HTMLElement,
  m: Message,
  chatID: string,
  streaming: boolean,
  marks: readonly SteerMark[] = [],
  range?: BlockRange,
): void {
  updateBody(wrap, m, chatID, streaming, false, marks, range);
  mountPlan(wrap, m);
}

/** The `tool`-cause fast path: refresh ONE mounted message through the same
 *  update path a full pass would run for it, touching no other render.
 *
 *  Returns false when nothing is mounted for `msgID` — the caller must fall
 *  back to a full pass then, because only the full pass mounts. Refresh-only by
 *  construction: the wrap is resolved from the existing render and the range is
 *  read back off `st.window`, so this can never build, re-home or re-order. */
export function refreshMessageCard(
  msgID: string,
  m: Message,
  chatID: string,
  live: boolean,
  marks: readonly SteerMark[] = [],
): boolean {
  const st = renders.get(msgID);
  const wrap = st?.blocksEl.parentElement;
  if (st === undefined || wrap === null || wrap === undefined) {
    return false;
  }
  updateAssistantBody(wrap, m, chatID, live, marks, st.window);
  return true;
}

/** The block indices `messageID`'s row currently holds, or undefined when
 *  nothing is mounted for it. The builder's completion test. */
export function mountedWindow(messageID: string): BlockRange | undefined {
  return renders.get(messageID)?.window;
}

/** The element whose removal drops `blockIndex` of `messageID`, or undefined.
 *
 *  Resolves the RENDER first, so a card hosting another message's step blocks
 *  is in the wrong render's map and cannot answer — which a subtree query for
 *  the same index cannot promise. */
export function blockElement(messageID: string, blockIndex: number): HTMLElement | undefined {
  return renders.get(messageID)?.blockEls.get(blockIndex);
}

/** Ids of renders still carrying live text: an unsealed live bubble, or any
 *  bubble whose caret has not drained (`.streaming` is granted and revoked by
 *  the bubble itself, so the class read IS the caret test — no subtree scan).
 *  Detached renders report too; the transcript caller drops ids it never
 *  mounted. */
export function liveRenderIDs(): string[] {
  const out: string[] = [];
  for (const [id, st] of renders) {
    if (st.liveBubble !== null || st.bubbles.some((b) => b.root.classList.contains("streaming"))) {
      out.push(id);
    }
  }
  return out;
}

function updateBody(
  wrap: HTMLElement,
  m: Message,
  chatID: string,
  streaming: boolean,
  detached: boolean,
  marks: readonly SteerMark[] = [],
  range?: BlockRange,
): void {
  const st = renders.get(m.id);
  if (st === undefined) {
    // Should not happen (build runs first), but stay self-healing.
    buildBody(wrap, m, chatID, streaming, detached, marks, range);
    return;
  }
  const want = range ?? wholeOf(m);
  st.tools = m.tool_calls ?? [];
  st.blocks = m.blocks ?? [];
  // Ahead of the render, and on EVERY pass rather than only when blocks arrive: a
  // stage's blocks can reach the dispatcher before its own invocation tool call is
  // in the store (out-of-order SSE), and this index is the only thing that knows
  // which pipeline a stage belongs to.
  indexPipelines(st, m);
  const idx = indexGroups(st, m, marks, streaming);
  if (want.to > st.window.to) {
    renderRange(st, m, st.window.to, want.to, streaming, marks, idx);
  }
  // OUTSIDE that guard, deliberately. A steer read between two chunks of the
  // same block adds no block, so gating this on block growth would strand its
  // note until the next one arrived — which on a long text block is the whole
  // rest of the turn. The two calls coincide whenever a block DID arrive, and
  // `st.steerNotes` is what makes that harmless.
  flushSteerNotes(st, marks, m.id, st.window.from, st.window.to);
  syncGroupCollapse(st, idx);
  syncMountedText(st, m);
}

/** Bring every mounted block up to the store's current text for that block.
 *
 *  The per-block signal effect is the FAST path: a chunk writes the signal and
 *  the DOM updates with no reconcile at all. This is the FALLBACK, and it exists
 *  because that effect is only created for a block the renderer judged live.
 *  `store.appendChunk` already schedules a full repaint for any block with no
 *  signal, but until this sweep the repaint only mounted NEW blocks and never
 *  revisited the text of a mounted one — so a block whose liveness was misjudged
 *  froze at whatever text existed when it mounted, with no ellipsis and no way
 *  back except a reload. A misjudgement is cheap to cause: any mid-turn event
 *  that clears the chat's `thinking` flag does it for the rest of the turn.
 *
 *  Safe to run over a subscribed block too. Both handles own their own rendered
 *  watermark, so whichever writer arrives first wins and the other compares two
 *  lengths and returns. */
function syncMountedText(st: MsgRender, m: Message): void {
  const blocks = m.blocks ?? [];
  for (const [i, setText] of st.blockText) {
    const block = blocks[i];
    if (block === undefined) {
      continue;
    }
    setText((block.type === "thinking" ? block.thinking : block.text) ?? "");
  }
}

/** Finalize: flush every markdown stream + seal every reasoning trace. */
export function finalizeAssistantBody(msgId: string): void {
  const st = renders.get(msgId);
  if (st === undefined) {
    return;
  }
  for (const b of st.bubbles) {
    // end(), not finishNow(): the turn is over but the last block's reveal may
    // still be behind the live edge, and that residue is text the model really
    // did produce last. It keeps flowing (with its caret) until it lands.
    b.end();
  }
  st.liveBubble = null;
  for (const r of st.reasonings) {
    r.seal();
  }
}

/** Drop a message's render state (reconcile.onRemove / chat switch). */
export function disposeAssistantBody(msgId: string): void {
  const st = renders.get(msgId);
  if (st !== undefined) {
    // A bubble mid-reveal holds a frame loop. Its DOM is about to go, so finish
    // it here rather than letting it drain into a detached node.
    for (const b of st.bubbles) {
      b.finishNow();
    }
    disposeAll(st);
  }
  renders.delete(msgId);
}

/** The block-layer half of pausing a parked message: finish any reveal (a
 *  frame loop writing into what is about to be hidden), then suspend the run
 *  cards — their render effects stop and their clock holds release, without
 *  the final dispose (`forgetRun` stays a real unmount's business). The
 *  streaming and tool-card effects are the callers' registries (messages.ts,
 *  messages-tools.ts); this covers what only the render state can reach. */
export function pauseAssistantBody(msgId: string): void {
  const st = renders.get(msgId);
  if (st === undefined) {
    return;
  }
  for (const b of st.bubbles) {
    b.finishNow();
  }
  st.liveBubble = null;
  for (const [workflowID, card] of st.runs) {
    disarmRunCard(st, workflowID, card);
  }
}

/** Re-arm a resumed message's run cards: the effect's first run re-reads the
 *  run's cell (the store kept ingesting frames while the view was parked), and
 *  the clock hold comes back with it. Idempotent per card, so a card the
 *  catch-up paint already armed is left alone. */
export function resumeAssistantBody(msgId: string): void {
  const st = renders.get(msgId);
  if (st === undefined) {
    return;
  }
  for (const [workflowID, card] of st.runs) {
    armRunCard(st, workflowID, card);
  }
}

export function resetBlockRenders(): void {
  liveAnchor = null;
  for (const st of renders.values()) {
    for (const b of st.bubbles) {
      b.finishNow();
    }
    disposeAll(st);
  }
  renders.clear();
}

// ---------------------------------------------------------------------------
// The DETACHED render: one delegate's blocks, on its own page
// ---------------------------------------------------------------------------

/** The render id a detached body is keyed under.
 *
 *  Derived rather than the bare message id, because `renders` is one map and the
 *  transcript is already holding an entry for that message: a shared key would
 *  make whichever surface mounted second clobber the other's render state, and
 *  then dispose the wrong one. */
function detachedID(messageID: string, subtask: string): string {
  return `${messageID}#${subtask}`;
}

/** Render one delegate's own transcript into `host`, as the main agent's.
 *
 *  `m` is a SYNTHETIC message the caller assembled (subagent-slice.ts): the
 *  delegate's blocks with their `agent_subtask_id` cleared, its tool calls, and a
 *  derived id. Clearing the attribution is what makes this a transcript rather
 *  than a card — `containerFor` routes by that field, so a block still carrying it
 *  would rebuild the collapsed box this page exists to open.
 *
 *  Everything else is the transcript's: real tool cards, real diffs, real
 *  reasoning traces, streaming markdown. That reuse is the whole point of the
 *  page, and it is why this lives here rather than in the view — `renders`, the
 *  dispatcher and the disposal rules are this module's, and a second renderer
 *  beside them would drift. */
export function buildDetachedBody(
  host: HTMLElement,
  m: Message,
  chatID: string,
  subtask: string,
  live: boolean,
): void {
  buildBody(host, { ...m, id: detachedID(m.id, subtask) }, chatID, live, true);
}

/** Append newly-arrived blocks and bring mounted ones up to the store's text. */
export function updateDetachedBody(
  host: HTMLElement,
  m: Message,
  chatID: string,
  subtask: string,
  live: boolean,
): void {
  updateBody(host, { ...m, id: detachedID(m.id, subtask) }, chatID, live, true);
}

/** Flush every markdown stream and seal every reasoning trace. */
export function finalizeDetachedBody(messageID: string, subtask: string): void {
  finalizeAssistantBody(detachedID(messageID, subtask));
}

/** Drop a detached render. The page's own unmount, and the only thing that fires
 *  its disposers — messages.ts never sees this id. */
export function disposeDetachedBody(messageID: string, subtask: string): void {
  disposeAssistantBody(detachedID(messageID, subtask));
}

/** Run and clear a render's message-lifetime cleanups. Idempotent: both dispose
 *  paths can reach one render (a chat switch resets every render AND the
 *  reconcile removes each row), and a store subscription disposed twice must not
 *  throw. */
function disposeAll(st: MsgRender): void {
  pruneContainers(st);
  for (const key of [...st.disposers.keys()]) {
    runDisposers(st, key);
  }
}

// ---------------------------------------------------------------------------
// Block dispatch
// ---------------------------------------------------------------------------

function renderRange(
  st: MsgRender,
  m: Message,
  from: number,
  to: number,
  live: boolean,
  marks: readonly SteerMark[],
  idx: GroupIndex,
): void {
  const blocks = m.blocks ?? [];
  const lastIdx = blocks.length - 1;
  if (to > st.window.to) {
    // Only a TAIL append moves the tail: a head insertion posts nothing after
    // the live block, and nothing re-establishes a caret sealed by mistake.
    // Idempotent — `end()` nulls its own stream and `classList.remove` is a
    // no-op when the class is absent.
    sealLiveBubble(st);
  }
  // FIRST, not last: the in-loop steer-note bound reads `st.window.from`, and a
  // head extension's ordinals all sit below the un-merged value.
  st.window = { from: Math.min(st.window.from, from), to: Math.max(st.window.to, to) };
  for (let i = from; i < to; i++) {
    const block = blocks[i];
    if (block === undefined) {
      continue;
    }
    // BEFORE the block, so a note anchored at index i lands above it. This is
    // the whole of "chronologically at the point it was injected".
    flushSteerNotes(st, marks, m.id, st.window.from, i);
    placeBlock(st, m, block, i, live && blockIsLive(blocks, i, lastIdx), idx);
  }
  // A note anchored at the CURRENT end has no block to sit above yet, and the
  // loop above can never reach it. Mounting it here is what puts it below
  // everything so far and above everything that arrives next.
  flushSteerNotes(st, marks, m.id, st.window.from, st.window.to);
}

// The window's two moving edges: ONE call per edge, never one for both. A
// relocation retracts a row at both ends, and a single compensated call would
// correct by a delta that includes the below-the-reader removal.

/** Mount `keep`'s ordinals below the mounted head IN PLACE: the head extension.
 *
 *  Positional rather than a row rebuild, so nothing replays an animation, drops a
 *  selection or forgets a reader-set disclosure — safe because grouping and sealing
 *  are derived from the store. Bounded by `keep.to` too, or a move to a DISJOINT
 *  range mounts everything between the two and the tail drop takes it straight
 *  back. */
export function mountHeadRange(
  m: Message,
  keep: BlockRange,
  live: boolean,
  marks: readonly SteerMark[],
): void {
  const st = renders.get(m.id);
  const from = keep.from;
  if (st === undefined || from >= st.window.from) {
    return;
  }
  const to = Math.min(st.window.from, keep.to);
  if (from >= to) {
    return;
  }
  st.tools = m.tool_calls ?? [];
  st.blocks = m.blocks ?? [];
  indexPipelines(st, m);
  const idx = indexGroups(st, m, marks, live);
  st.insertBefore = new Map();
  try {
    renderRange(st, m, from, to, live, marks, idx);
  } finally {
    st.insertBefore = null;
  }
  syncGroupCollapse(st, idx);
}

/** Retract `m`'s mounted window at the HEAD to `keep.from`. Collected as a
 *  head-side change: everything it removes is above the reader. */
export function dropHead(m: Message, keep: BlockRange, marks: readonly SteerMark[]): void {
  const st = renders.get(m.id);
  if (st === undefined || keep.from <= st.window.from) {
    return;
  }
  dropBlockRange(st, m, { from: keep.from, to: st.window.to }, marks);
}

/** Retract `m`'s mounted window at the TAIL to `keep.to`. Collected as a
 *  tail-side change: it runs BARE, because its delta is below the reader and
 *  compensating it would drag their view. */
export function dropTail(m: Message, keep: BlockRange, marks: readonly SteerMark[]): void {
  const st = renders.get(m.id);
  if (st === undefined || keep.to >= st.window.to) {
    return;
  }
  dropBlockRange(st, m, { from: st.window.from, to: keep.to }, marks);
}

/** Release everything the mounted indices OUTSIDE `keep` own, and leave no effect
 *  subscribed to a detached node.
 *
 *  Reached only through `dropHead` and `dropTail`, never as one two-sided call.
 *  `openContainers` keys deliberately SURVIVE: a drop is a window move, not a
 *  render dispose, so a box the reader opened comes back open. */
function dropBlockRange(
  st: MsgRender,
  m: Message,
  keep: BlockRange,
  marks: readonly SteerMark[],
): void {
  const removed: number[] = [];
  for (let i = st.window.from; i < st.window.to; i++) {
    if (i < keep.from || i >= keep.to) {
      removed.push(i);
    }
  }
  if (removed.length === 0) {
    return;
  }
  const blocks = m.blocks ?? [];
  for (const i of removed) {
    dropBlock(st, m, blocks[i], i);
  }
  cbs.disposeBlockEffects(m.id, removed);
  for (const [id, note] of [...st.steerNotes]) {
    if (note.index < keep.from || note.index > keep.to) {
      note.el.remove();
      st.steerNotes.delete(id);
    }
  }
  pruneEmptyContainers(st);
  rebindSurvivingBoxes(st);
  st.window = keep;
  // The marks are re-flushed against the narrowed window, so a note whose anchor
  // is still inside it survives a drop that removed its neighbour.
  flushSteerNotes(st, marks, m.id, st.window.from, st.window.to);
}

/** Release one block: its measured height into the cache, its disclosure state, its
 *  element, its text sink, its streaming signals and its block-lifetime cleanups. */
function dropBlock(st: MsgRender, m: Message, block: Block | undefined, i: number): void {
  const el = st.blockEls.get(i);
  if (el !== undefined) {
    // MEASURED on the way out, so the spacer replacing it holds the height it held, and
    // only a REAL reading: a detached element answers 0 and a short spacer leaves the
    // document shorter than the content it stands in for. The estimate over-prices.
    if (el.offsetHeight > 0) {
      recordBlockHeight(m.id, i, el.offsetHeight);
    }
    recordDisclosure(el, block);
    st.bubbles = st.bubbles.filter((b) => {
      if (b.root !== el && !el.contains(b.root)) {
        return true;
      }
      // A reveal in flight holds a frame loop, and its DOM is about to go.
      b.finishNow();
      if (st.liveBubble === b) {
        st.liveBubble = null;
      }
      return false;
    });
    st.reasonings = st.reasonings.filter((view) => {
      if (view.root !== el && !el.contains(view.root)) {
        return true;
      }
      for (const [container, open] of st.openReasoning) {
        if (open === view) {
          st.openReasoning.delete(container);
        }
      }
      return false;
    });
    if (st.topLiveEl !== null && (st.topLiveEl === el || el.contains(st.topLiveEl))) {
      clearLiveAnchor(st.topLiveEl);
      st.topLiveEl = null;
    }
    el.remove();
    st.blockEls.delete(i);
  }
  st.blockText.delete(i);
  clearBlockSig(m.id, i);
  runDisposers(st, i);
  if (block?.type === "tool_use") {
    const tc = st.tools.find((c) => c.id === block.tool_call_id);
    const runID = tc === undefined ? "" : workflowInvocation(tc);
    const card = runID === "" ? undefined : st.runs.get(runID);
    if (runID !== "" && card !== undefined) {
      const claim = liveRunClaimant(st, card);
      if (claim === undefined) {
        // Nothing mounted inside it, so the run's own state goes back — or `runCardFor`
        // hands the next claimant a DETACHED node and re-homing never fires.
        st.runs.delete(runID);
        releaseRunCard(st, runID, card);
        card.root.remove();
      } else {
        // RE-HOMED: the card is a CONTAINER, and removing it takes another render's
        // mounted blocks out of the document while that render still counts them. One
        // card per run, by the earliest in-window claimant once the launch is gone.
        const seat = seatAbove(claim.host, claim.host.blocksEl, claim.at, card.root);
        adoptRunCard(claim.host, st, runID, card, seat);
      }
    }
  }
}

/** The render, other than `st`, holding mounted blocks INSIDE `card`, and the lowest
 *  such ordinal. DOM order decides between several, which is what "earliest" means:
 *  `renders` is keyed in BUILD order and a scroll up builds earlier messages last. */
function liveRunClaimant(
  st: MsgRender,
  card: RunCardView,
): { host: MsgRender; at: number } | undefined {
  let out: { host: MsgRender; at: number } | undefined;
  for (const other of renders.values()) {
    if (other === st || other.detached || other.chatID !== st.chatID) {
      continue;
    }
    for (let i = other.window.from; i < other.window.to; i++) {
      const el = other.blockEls.get(i);
      if (el === undefined || !card.root.contains(el)) {
        continue;
      }
      const held = out?.host.blocksEl;
      if (
        held === undefined ||
        (other.blocksEl.compareDocumentPosition(held) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0
      ) {
        out = { host: other, at: i };
      }
      break;
    }
  }
  return out;
}

/** Where something the store places at `at` belongs in `host`: before the first child of
 *  `host` holding a mounted ordinal ABOVE `at`, and the tail when there is none. `placed`
 *  is the node being seated, excluded because its own members walk up to it. */
function seatAbove(
  st: MsgRender,
  host: HTMLElement,
  at: number,
  placed: HTMLElement,
): HTMLElement | null {
  for (let i = at + 1; i < st.window.to; i++) {
    let node = st.blockEls.get(i) ?? null;
    while (node !== null && node.parentElement !== host) {
      node = node.parentElement;
    }
    if (node !== null && node !== placed) {
      return node;
    }
  }
  return null;
}

/** Re-subscribe every box the drop left STANDING whose invocation block it took.
 *  That block's cleanup released the binding, and no path re-binds an existing box —
 *  so without this a delegate whose own blocks are still in window keeps a frozen
 *  header, status and footer ledger until its invocation re-mounts.
 *
 *  `live` is `false` because it cannot be reached: every box here exists, so neither
 *  binder passes it to a creation. */
function rebindSurvivingBoxes(st: MsgRender): void {
  for (const [subtask, sa] of st.subagents) {
    const inv = st.tools.find(
      (tc) => (tc.agent_subtask_id ?? "") === subtask && isSubagentInvocation(tc),
    );
    if (inv !== undefined && !st.boundBoxes.has(inv.id)) {
      bindSubagent(st, subtask, st.msgID, sa, inv, invocationIndex(st, inv.id));
    }
  }
  for (const pipelineID of st.pipelines.keys()) {
    const inv = st.tools.find((tc) => tc.id === pipelineID && isPipelineInvocation(tc));
    if (inv !== undefined && !st.boundBoxes.has(inv.id)) {
      bindPipeline(st, st.msgID, inv, false, invocationIndex(st, inv.id));
    }
  }
}

/** Remove every container this drop left with nothing in it, and its render state
 *  with it. Its `openContainers` key stays, per the disclosure rule. */
function pruneEmptyContainers(st: MsgRender): void {
  for (const [key, bucket] of st.toolGroups) {
    for (const [runStart, group] of bucket) {
      if (groupBody(group).firstElementChild === null) {
        group.remove();
        bucket.delete(runStart);
      }
    }
    if (bucket.size === 0) {
      st.toolGroups.delete(key);
    }
  }
  for (const [subtask, sa] of st.subagents) {
    if (sa.body.firstElementChild === null) {
      st.openReasoning.delete(sa.body);
      sa.root.remove();
      st.subagents.delete(subtask);
      st.subagentMembers.delete(subtask);
    }
  }
  for (const [pipelineID, box] of st.pipelines) {
    if (box.body.firstElementChild === null) {
      st.openReasoning.delete(box.body);
      box.root.remove();
      st.pipelines.delete(pipelineID);
    }
  }
}

/** Whether block `i` is the one its stream is still writing.
 *
 *  TEXT (and tool_use) blocks stream only at the ARRAY tail: exactly one
 *  streaming caret is a pinned invariant, so a text block behind the tail is
 *  sealed even when a delegate interleaved behind it.
 *
 *  A THINKING block streams while it is the last block of its OWN lane — the
 *  server extends the newest block of the delta's own subtask, which can sit
 *  behind the array tail when a delegate interleaves. For a trace this decides
 *  the GROWTH WIRING (its signal effect) and the initial open state; DISCLOSURE
 *  is appendBlock's — anything landing after it in its container seals it, so a
 *  still-growing trace with a delegate box below it renders sealed while its
 *  text keeps accumulating. */
function blockIsLive(blocks: readonly Block[], i: number, lastIdx: number): boolean {
  const block = blocks[i];
  if (block === undefined) {
    return false;
  }
  if (block.type !== "thinking") {
    return i === lastIdx;
  }
  const lane = block.agent_subtask_id ?? "";
  for (let j = i + 1; j <= lastIdx; j++) {
    if ((blocks[j]?.agent_subtask_id ?? "") === lane) {
      return false;
    }
  }
  return true;
}

/** Mount every not-yet-drawn steer note anchored inside `[from, to]`. Idempotent
 *  by mark id, which is required: `renderRange` and `updateAssistantBody` both call
 *  it and their ranges overlap by design. Bounded on BOTH sides, or an open upper
 *  edge mounts a mark from far below the window at the end of the region. */
function flushSteerNotes(
  st: MsgRender,
  marks: readonly SteerMark[],
  msgID: string,
  from: number,
  to: number,
): void {
  for (const mark of marks) {
    const at = mark.anchor.blockIndex;
    if (st.steerNotes.has(mark.id) || mark.anchor.msgID !== msgID || at < from || at > to) {
      continue;
    }
    const note = buildSteerNote({
      text: mark.text,
      origin: mark.origin,
      ...(mark.ack !== undefined ? { ack: mark.ack } : {}),
      dropped: mark.dropped === true,
      onRestore: () => {
        cbs.restoreSteer(mark.text);
      },
    });
    appendBlock(st, st.blocksEl, note);
    st.steerNotes.set(mark.id, { index: at, el: note });
  }
}

/** End the bubble currently carrying the caret, if any. */
function sealLiveBubble(st: MsgRender): void {
  // finishNow, not end: the tail MOVED, so the model is already producing
  // something else and this block's residual reveal is no longer live text. Left
  // to drain it would carry its caret alongside the new tail's for the reveal's
  // lag, and "exactly one streaming caret" is the invariant this function exists
  // to keep. The cost is up to LAG_SECS of prose landing at once, at the moment a
  // new block is appearing anyway. The turn's LAST block is different and gets
  // the graceful drain — see finalizeAssistantBody.
  st.liveBubble?.finishNow();
  st.liveBubble = null;
}

/** Resolve the container a block renders into: the top-level `.assistant-blocks`
 *  for parent-agent blocks, a run card's step row for a WORKFLOW STEP, or a
 *  SubagentBlock's body for a subagent's blocks.
 *
 *  Three destinations, and the middle one is the reason this is not a two-line
 *  function. A step's subtask id is `wf:<workflowId>:<nodePath>`, so it names TWO
 *  containers: the run, and the step within it. Resolving both is what puts a
 *  step's work inside the invocation that launched it rather than beside it.
 *
 *  A PIPELINE STAGE is the same two-level shape reached a different way. Its
 *  subtask id is a bare uuid that names nothing, so the pair comes from
 *  `st.stagePipeline` (see `indexPipelines`): the pipeline, and the stage's own
 *  box within it. Without that lookup a stage box is appended to `blocksEl` as a
 *  FLAT SIBLING of the orchestrate call that started it, which is what put two
 *  unrelated boxes with two beating spinners on screen for one delegated task. */
function containerFor(st: MsgRender, block: Block, live: boolean): HTMLElement {
  const subtask = block.agent_subtask_id ?? "";
  if (subtask === "") {
    return st.blocksEl;
  }
  const step = parseStepSubtask(subtask);
  if (step !== null) {
    return runContainerFor(st, step);
  }
  let sa = st.subagents.get(subtask);
  if (sa === undefined) {
    sa = buildSubagentBlock("Subagent", live ? "in_progress" : "completed", {
      ...subagentOpenerFor(st, subtask),
      ...(st.detached
        ? {}
        : {
            startOpen: containerOpen(`sub:${subtask}`) ?? false,
            onOpenChange: (open: boolean): void => {
              setContainerOpen(`sub:${subtask}`, open);
            },
          }),
    });
    sa.root.dataset["subtask"] = subtask;
    st.subagents.set(subtask, sa);
    // The box lands in its HOST (top level or a pipeline body), at the store index that
    // establishes it — the same index `indexGroups` prices its run break at.
    const host = stageHostFor(st, subtask, live);
    placeContainer(st, host, sa.root, st.containerAt.get(`sub:${subtask}`));
    // The BINDING, not only the box: a box created for an out-of-window invocation
    // would otherwise render the generic "Subagent" header with no footer ledger.
    const inv = st.tools.find(
      (tc) => (tc.agent_subtask_id ?? "") === subtask && isSubagentInvocation(tc),
    );
    if (inv !== undefined) {
      bindSubagent(st, subtask, st.msgID, sa, inv, invocationIndex(st, inv.id));
    }
  }
  return sa.body;
}

/** The delegate card's footer link, or nothing.
 *
 *  Injected here rather than imported by the card, so `fundamentals/` keeps
 *  pointing only downward — and the opener is lazy for the reason the run card's
 *  is: `subagent-view.ts` reaches the whole page and the transcript must not carry
 *  it.
 *
 *  A DETACHED render gets no link at all. It IS the page, so a nested delegate's
 *  link would point at a sibling page this one cannot route to without knowing its
 *  chat, and a control that does nothing teaches a reader to distrust the others —
 *  the same rule that keeps the run card's link out of `full`.
 *
 *  The chat id is the RENDER's, which is the one the delegate's blocks came from —
 *  the transcript's render is the active chat's, and a render that is not would
 *  otherwise link to a delegate of a different conversation. */
function subagentOpenerFor(st: MsgRender, subtask: string): { open?: SubagentOpener } {
  if (st.detached) {
    return {};
  }
  const chatID = st.chatID;
  if (chatID === "" || subtask === "") {
    return {};
  }
  return {
    open: {
      href: buildPath({ kind: "subagent", chat: chatID, id: subtask }),
      open: () => {
        void import("./subagent-view.js")
          .then(({ openSubagentView }) => {
            openSubagentView(chatID, subtask);
          })
          .catch(() => {
            /* noop: the link degrades to its href on the next click */
          });
      },
    },
  };
}

/** Whether this pipeline's own box is in this render, or will be by the end of the
 *  pass. `stageHostFor`'s question, asked without building anything: an EXISTING
 *  box outranks the count there, so the count alone answers a different one. */
function hostsPipelineBox(st: MsgRender, pipelineID: string): boolean {
  return st.pipelines.has(pipelineID) || pipelineHasContainer(st, pipelineID);
}

/** Where a stage's own box goes: its pipeline's body when that pipeline has a
 *  container, otherwise the top level. Building the box here makes the two arrival
 *  orders equivalent — call and first stage race on the wire, and a refresh
 *  persists the call but not the stage's blocks. Same contract as `runCardFor`.
 *
 *  An EXISTING container wins over the count, so that check is first: the count can
 *  rise after a container exists, and a stage beside a container its own pipeline
 *  owns is the flat-siblings shape the join removed. */
function stageHostFor(st: MsgRender, subtask: string, live: boolean): HTMLElement {
  const pipelineID = st.stagePipeline.get(subtask);
  if (pipelineID === undefined) {
    return st.blocksEl;
  }
  const existing = st.pipelines.get(pipelineID);
  if (existing !== undefined) {
    return existing.body;
  }
  if (!pipelineHasContainer(st, pipelineID)) {
    return st.blocksEl;
  }
  return pipelineBoxFor(st, pipelineID, live).body;
}

/** Write the driver's header onto its box: the label from the stage COUNT, the
 *  status and the footer ledger from the driver's own call. */
function paintPipeline(st: MsgRender, box: SubagentView, driver: ToolCall): void {
  box.setName(pipelineLabel(st, driver.id));
  box.setStatus(driver.status);
  box.setSummary(pipelineSummary(st, driver));
}

/** Get or build the PIPELINE box for one orchestrate tool call. Built with the
 *  `container` activity variant (fundamentals/subagent-block.ts).
 *
 *  It also ADOPTS any stage of its own sitting at the top level, which is the
 *  upgrade path after a lone stage was promoted. A RE-PARENT, never a rebuild: the
 *  move carries the disclosure, the tail observer, the reveal loop, every effect
 *  and the page link with the node, so nothing streamed or toggled is lost. A box the
 *  STAGE path built also paints ITSELF — the self-paint at the tail says why. */
function pipelineBoxFor(st: MsgRender, pipelineID: string, live: boolean): SubagentView {
  const existing = st.pipelines.get(pipelineID);
  if (existing !== undefined) {
    return existing;
  }
  const box = buildSubagentBlock(
    pipelineLabel(st, pipelineID),
    live ? "in_progress" : "completed",
    {
      activity: "container",
      ...(st.detached
        ? {}
        : {
            startOpen: containerOpen(`pipe:${pipelineID}`) ?? false,
            onOpenChange: (open: boolean): void => {
              setContainerOpen(`pipe:${pipelineID}`, open);
            },
          }),
    },
  );
  box.root.dataset["pipeline"] = pipelineID;
  st.pipelines.set(pipelineID, box);
  const promoted = (st.pipelineStages.get(pipelineID) ?? [])
    .map((subtask) => st.subagents.get(subtask))
    .filter((v): v is SubagentView => v?.root.parentElement === st.blocksEl);
  const first = promoted[0];
  if (first === undefined) {
    placeContainer(st, st.blocksEl, box.root, st.containerAt.get(`pipe:${pipelineID}`));
  } else {
    // Lands where the first adopted stage sat, keeping transcript order, and not
    // through `appendBlock`: nothing is posted after an open trace by swapping a
    // node already in place.
    first.root.replaceWith(box.root);
  }
  for (const v of promoted) {
    box.body.appendChild(v.root);
  }
  // A box the STAGE path built paints itself, because nothing else will: the driver's
  // own paint may have run already and returned early (this pipeline looked promoted
  // then), and its effect re-runs only when the tool call CHANGES, which a settled
  // driver has none of. Left out, such a box keeps the countless title, reads its
  // status from `live` — running, over finished work — and never attaches its ledger.
  const driver = peekToolCallSig(st.chatID, pipelineID);
  if (driver !== undefined) {
    paintPipeline(st, box, driver);
  }
  // And the BINDING, for the same reason the subagent box binds itself: a box the
  // stage path built has no subscription and no ledger until its own invocation
  // block mounts, which a window need never reach.
  const inv = st.tools.find((tc) => tc.id === pipelineID && isPipelineInvocation(tc));
  if (inv !== undefined) {
    bindPipeline(st, st.msgID, inv, live, invocationIndex(st, inv.id));
  }
  return box;
}

/** The block index `toolID`'s invocation sits at, or `-1` — the message-lifetime
 *  bucket — when this message holds no block for it. */
function invocationIndex(st: MsgRender, toolID: string): number {
  return st.blocks.findIndex((b) => b.type === "tool_use" && b.tool_call_id === toolID);
}

/** The disclosure the transcript's cards read and write: the registry, keyed by
 *  node path for a step row and by `null` for the card itself. */
function runDisclosure(workflowID: string): RunDisclosure {
  return {
    wasOpen: (nodePath) =>
      nodePath === null ? runCardOpen(workflowID) : containerOpen(`step:${workflowID}:${nodePath}`),
    onOpenChange: (nodePath, open) => {
      setContainerOpen(
        nodePath === null ? `run:${workflowID}` : `step:${workflowID}:${nodePath}`,
        open,
      );
    },
  };
}

/** The step row inside the run card, creating the card when the invocation has
 *  not been rendered yet.
 *
 *  A step's frame can arrive before the tool call that started it is in the store
 *  (out-of-order SSE), and after a refresh the invocation is persisted while the
 *  step blocks are not — so the card must be creatable from either side. Whichever
 *  arrives first builds it; the other finds it. */
function runContainerFor(st: MsgRender, step: StepSubtask): HTMLElement {
  return runCardFor(st, step.workflowID, "Workflow run").stepBody(step.nodePath);
}

/** Get or build the run card for one workflow id, and subscribe it to the store.
 *
 *  The subscription is the whole reason the card needs no event handling of its
 *  own: `run-store.ts` owns the fetch and holds a signal per run, so one effect
 *  per card re-renders it whenever that run changes and nothing else does. */
function runCardFor(st: MsgRender, workflowID: string, name: string, owner = false): RunCardView {
  const existing = st.runs.get(workflowID);
  if (existing !== undefined) {
    reseatInserted(st, st.blocksEl, existing.root);
    return existing;
  }
  // The launch call is the card's only witness for its label and for a launch that
  // FAILED, and a card created from a step whose launch block is out of window has
  // to find it here — otherwise the card keeps the placeholder name and sits at
  // "starting" forever.
  const launch = st.tools.find((tc) => workflowInvocation(tc) === workflowID);
  if (!st.detached) {
    // ONE box per run per TRANSCRIPT, not per message. The server folds a run's
    // later frames into a NEW assistant message per turn-segment, so a
    // per-message key rebuilt the card in every segment — two boxes in the
    // launching turn, two more each later turn, all reading one store cell.
    // A later message routes into the first message's card instead; step rows
    // are keyed by node path, so cross-message routing lands in the right row.
    const host = runCardHosts.get(st.chatID)?.get(workflowID);
    const hosted = host?.runs.get(workflowID);
    if (hosted !== undefined && host !== undefined) {
      if (owner) {
        adoptRunCard(st, host, workflowID, hosted);
      }
      return hosted;
    }
    let hosts = runCardHosts.get(st.chatID);
    if (hosts === undefined) {
      hosts = new Map();
      runCardHosts.set(st.chatID, hosts);
    }
    hosts.set(workflowID, st);
  }
  // The footer link re-opens the run's tab. Injected here rather than imported by
  // the card, so `fundamentals/` keeps pointing only downward — and lazily, because
  // `run-view.ts` reaches the whole run page and the transcript must not carry it.
  const card = buildRunCard(
    workflowID,
    launch === undefined ? name : recipeNameOf(launch),
    (id, label) => {
      void import("./run-view.js")
        .then(({ openRunView }) => {
          openRunView(id, label);
        })
        .catch(() => {
          /* noop: the link degrades to its href on the next click */
        });
    },
    st.detached ? undefined : runDisclosure(workflowID),
  );
  st.runs.set(workflowID, card);
  placeContainer(st, st.blocksEl, card.root, st.containerAt.get(`run:${workflowID}`));
  pushDisposer(st, -1, () => {
    releaseRunCard(st, workflowID, card);
  });
  // The first read the card ever gets. Every later one arrives through the
  // armed effect, driven by the run SSE events.
  invalidateRun(workflowID);
  armRunCard(st, workflowID, card);
  if (launch !== undefined) {
    card.setLaunch(launch.status, launch.output);
  }
  return card;
}

/** Give up this render's claim on `workflowID`'s card: its effect, its clock hold,
 *  the host slot, and the store's cell.
 *
 *  Reached at MESSAGE lifetime by the render's own disposal and at BLOCK lifetime by
 *  a window drop of the launch block, and idempotent either way — which is what lets
 *  the two lifetimes share one function. */
function releaseRunCard(st: MsgRender, workflowID: string, card: RunCardView): void {
  disarmRunCard(st, workflowID, card);
  // Slot and cell together, and only while this render holds the claim: a re-homed
  // card outlives its old host's dispose, and its effect still reads that cell.
  const hosts = runCardHosts.get(st.chatID);
  if (hosts?.get(workflowID) !== st) {
    return;
  }
  hosts.delete(workflowID);
  if (hosts.size === 0) {
    runCardHosts.delete(st.chatID);
  }
  // The claim-holding card unmounting with no run tab open is the store cache's one
  // safe bound, and `forgetRun` states why it has to be exactly that.
  if (!hasTab("run", workflowID)) {
    forgetRun(workflowID);
  }
}

/** Move `workflowID`'s card out of `host` and into `st`, claim and all; `seat` is the node
 *  to place it before, absent meaning the mount position. Reached from both halves of one
 *  rule — the owning message mounting its launch block, or its window DROPPING it and the
 *  earliest render still holding blocks inside the card taking over. Moving the NODE keeps
 *  the element, so no effect churns and no entry animation replays. */
function adoptRunCard(
  st: MsgRender,
  host: MsgRender,
  workflowID: string,
  card: RunCardView,
  seat?: HTMLElement | null,
): void {
  host.runs.delete(workflowID);
  const stop = host.runEffects.get(workflowID);
  if (stop !== undefined) {
    host.runEffects.delete(workflowID);
    st.runEffects.set(workflowID, stop);
  }
  st.runs.set(workflowID, card);
  let hosts = runCardHosts.get(st.chatID);
  if (hosts === undefined) {
    hosts = new Map();
    runCardHosts.set(st.chatID, hosts);
  }
  hosts.set(workflowID, st);
  if (seat === undefined) {
    placeInContainer(st, st.blocksEl, card.root);
  } else {
    st.blocksEl.insertBefore(card.root, seat);
  }
  pushDisposer(st, -1, () => {
    releaseRunCard(st, workflowID, card);
  });
}

/** Whether this render holds `workflowID`'s card, or will when the block that needs
 *  it mounts. `runCardFor`'s question, asked without building anything: a card
 *  ANOTHER message's render hosts is returned from there, so nothing enters this. */
function hostsRunCard(st: MsgRender, workflowID: string): boolean {
  if (st.runs.has(workflowID) || st.detached) {
    return true;
  }
  const host = runCardHosts.get(st.chatID)?.get(workflowID);
  return host === undefined || host === st;
}

/** Adopt the launch tool call into its run's card: the recipe name from the
 *  call's input as a placeholder label, and a failed launch reported on the card
 *  rather than lost.
 *
 *  A launch that FAILED never created a run, so `GET /api/runs/{id}` has nothing
 *  and the card would sit at "starting" forever. The tool call is the only witness
 *  in that case, which is why its status is folded in here. */
function bindRunCard(st: MsgRender, workflowID: string, tc: ToolCall): void {
  const card = runCardFor(st, workflowID, recipeNameOf(tc), true);
  card.setLaunch(tc.status, tc.output);
}

/** The recipe a launch names, from the tool call's own input. A placeholder only:
 *  every render prefers the run's `runLabel`, which is what the launcher actually
 *  called this execution. */
function recipeNameOf(tc: ToolCall): string {
  const input = tc.input;
  if (input !== undefined && input !== null && typeof input === "object") {
    const rec = input as Record<string, unknown>;
    for (const key of ["workflowPath", "recipe", "name"]) {
      const v = rec[key];
      if (typeof v === "string" && v !== "") {
        // A path's last segment without its extension reads as the recipe name.
        const base = v.split("/").pop() ?? v;
        return base.replace(/\.(ya?ml|json)$/i, "");
      }
    }
  }
  return "Workflow run";
}

// ---------------------------------------------------------------------------
// The shared run clock.
//
// A run takes minutes, so a duration that only moves when a server frame arrives
// reads as frozen — and a paused run emits no frames at all. ONE interval for every
// card on screen rather than one per card: N timers ticking the same second is N
// wakeups for one repaint, and the interval stops entirely when no card is holding
// it, so an idle transcript costs nothing.
//
// Holders are REFCOUNTED per workflow id: the same run can be on screen more than
// once (a parked chat's card beside a subagent page's card of the same run), so a
// release names WHICH card let go and the interval survives until the last one
// does. A plain per-workflow slot let one surface's park stop the clock another
// surface was still showing.
// ---------------------------------------------------------------------------

const clockHolders = new Map<string, Set<RunCardView>>();
let clockTimer: ReturnType<typeof setInterval> | undefined;

function holdRunClock(workflowID: string, card: RunCardView): void {
  let holders = clockHolders.get(workflowID);
  if (holders === undefined) {
    holders = new Set();
    clockHolders.set(workflowID, holders);
  }
  holders.add(card);
  clockTimer ??= setInterval(() => {
    for (const set of clockHolders.values()) {
      for (const c of set) {
        c.tick();
      }
    }
  }, 1000);
}

function releaseRunClock(workflowID: string, card: RunCardView): void {
  const holders = clockHolders.get(workflowID);
  if (holders === undefined) {
    return;
  }
  holders.delete(card);
  if (holders.size === 0) {
    clockHolders.delete(workflowID);
  }
  if (clockHolders.size === 0 && clockTimer !== undefined) {
    clearInterval(clockTimer);
    clockTimer = undefined;
  }
}

/** Subscribe a run card to its store cell and hold the shared clock. The
 *  suspend half is `disarmRunCard`; both are idempotent so the pause path and
 *  the final dispose can overlap without double-releasing. */
function armRunCard(st: MsgRender, workflowID: string, card: RunCardView): void {
  if (st.runEffects.has(workflowID)) {
    return;
  }
  const stop = effect(() => {
    // Two inputs on different clocks, which is why they arrive together rather than
    // through two calls: `inspect` says what the run's steps are doing, and the dock
    // says which of them is blocked on a person. The run's status cannot carry the
    // second — KAS blocks the asking step's turn and leaves the run `running` — and
    // both reads are signal-backed, so this one effect repaints on either.
    card.render(runState(workflowID), runPendingAsks(workflowID));
  });
  st.runEffects.set(workflowID, stop);
  holdRunClock(workflowID, card);
}

function disarmRunCard(st: MsgRender, workflowID: string, card: RunCardView): void {
  const stop = st.runEffects.get(workflowID);
  if (stop === undefined) {
    return;
  }
  stop();
  st.runEffects.delete(workflowID);
  releaseRunClock(workflowID, card);
}

/** A standalone run card for a COLLAPSED turn's face: a DUPLICATE of the
 *  in-body card, subscribed to the same store cell so the two cannot disagree,
 *  and visible exactly when the body's copy is not (the fold hides the body;
 *  unfolding removes the face). Deliberately outside the runCardHosts registry:
 *  that registry dedupes transcript cards, and this one exists BECAUSE the
 *  transcript's copy is hidden. */
export function mountFaceRunCard(workflowID: string): { root: HTMLElement; dispose: () => void } {
  const card = buildRunCard(
    workflowID,
    "Workflow run",
    (id, label) => {
      void import("./run-view.js")
        .then(({ openRunView }) => {
          openRunView(id, label);
        })
        .catch(() => {
          /* noop: the link degrades to its href on the next click */
        });
    },
    undefined,
  );
  const stop = effect(() => {
    card.render(runState(workflowID), runPendingAsks(workflowID));
  });
  holdRunClock(workflowID, card);
  invalidateRun(workflowID);
  return {
    root: card.root,
    dispose: (): void => {
      stop();
      releaseRunClock(workflowID, card);
    },
  };
}

function placeBlock(
  st: MsgRender,
  m: Message,
  block: Block,
  i: number,
  live: boolean,
  idx: GroupIndex,
): void {
  const container = containerFor(st, block, live);
  const subtask = block.agent_subtask_id ?? "";

  switch (block.type) {
    case "text": {
      mountText(st, m.id, container, block, i, live);
      return;
    }
    case "thinking": {
      mountThinking(st, m, container, block, i, live, idx);
      return;
    }
    case "tool_use": {
      const tc = m.tool_calls?.find((c) => c.id === block.tool_call_id);
      if (tc === undefined) {
        return; // referenced tool call not in the store yet (out-of-order SSE)
      }
      // Internal engine bookkeeping (the session-boot cloud-config fetch) is
      // suppressed server-side since 2026-08-31, so this only ever matches
      // TRANSCRIPTS PERSISTED BEFORE THAT — where the fragment's card sits
      // stuck at in_progress forever, because its completion frame was lost to
      // the displacement that persisted it. Title-keyed, unlike the server's
      // _meta.kiro.toolId key, because the persisted ToolCall carries no tool
      // id; the title is a KAS constant, not model-composed.
      if (isInternalToolTitle(tc.title)) {
        return;
      }
      // A WORKFLOW LAUNCH becomes the run's card, not a tool row, and the call sits in
      // the parent agent's own block stream — so this branch is ahead of the subtask
      // checks below. A card a step's frame built first is MOVED here rather than
      // rebuilt, which is what keeps the two arrival orders equivalent.
      const runID = workflowInvocation(tc);
      if (subtask === "" && runID !== "") {
        bindRunCard(st, runID, tc);
        return;
      }
      // A PIPELINE LAUNCH becomes the pipeline's box, not a tool row. Like the
      // workflow launch above it sits in the parent agent's own block stream with
      // no subtask of its own, so this branch is ahead of the subtask checks; and
      // like it, a box already built by a stage whose frame arrived first is FOUND
      // rather than replaced.
      if (subtask === "" && isPipelineInvocation(tc)) {
        bindPipeline(st, m.id, tc, live, i);
        return;
      }
      // The subagent invocation becomes the SubagentBlock's header, not a card.
      if (subtask !== "" && isSubagentInvocation(tc)) {
        const sa = st.subagents.get(subtask);
        if (sa !== undefined) {
          bindSubagent(st, subtask, m.id, sa, tc, i);
        }
        return;
      }
      // A delegate's own tool call: a footer-ledger member of its box.
      if (subtask !== "") {
        let members = st.subagentMembers.get(subtask);
        if (members === undefined) {
          members = new Set();
          st.subagentMembers.set(subtask, members);
        }
        members.add(tc.id);
      }
      if (isTodoTool(tc)) {
        mountTodo(st, m.id, container, tc, i);
        return;
      }
      mountToolCard(st, m.id, container, subtask, tc, i, groupRunStart(idx, subtask, i));
      return;
    }
  }
}

// ---------------------------------------------------------------------------
// Block mounters
// ---------------------------------------------------------------------------

/** Record `el` as block `i`'s element and stamp both coordinates on it. The map
 *  answers every lookup; the attributes serve the one consumer that starts from an
 *  ELEMENT — the anchor ladder — which is why the owning message id is stamped
 *  beside the index: a card in this row can carry another message's numbering. */
function stampBlock(st: MsgRender, el: HTMLElement, msgId: string, i: number): void {
  el.dataset["blockIndex"] = String(i);
  el.dataset["blockMsg"] = msgId;
  st.blockEls.set(i, el);
}

function mountText(
  st: MsgRender,
  msgId: string,
  container: HTMLElement,
  block: Block,
  i: number,
  live: boolean,
): void {
  const initial = block.text ?? "";
  // Only a top-level live bubble joins the anchor registry: the seal callback
  // clears this message's slot (identity-guarded) and the registry falls back
  // to the newest surviving top-level bubble.
  const topLive = live && !st.detached && container === st.blocksEl;
  // Top-level bubbles carry a row; subagent-body bubbles don't (the subagent
  // header is the identity — matches the IDE's indented nesting). Created
  // BEFORE the bubble so the builder's initial blank report lands on it.
  const row = container === st.blocksEl ? cbs.makeRow() : null;
  const opts: AssistantBubbleOpts = {};
  if (topLive) {
    opts.onSeal = (root): void => {
      if (st.topLiveEl === root) {
        st.topLiveEl = null;
      }
      clearLiveAnchor(root);
    };
  }
  if (row !== null) {
    opts.onBlankChange = (blank): void => {
      row.classList.toggle("is-empty", blank);
    };
  }
  const bubble = buildAssistantBubble(initial, live, opts);
  st.bubbles.push(bubble);
  if (live) {
    st.liveBubble = bubble;
  }
  if (topLive) {
    st.topLiveEl = bubble.root;
    liveAnchor = { messageID: msgId, el: bubble.root };
  }
  st.blockText.set(i, (full) => {
    bubble.setText(full);
  });
  // The stamped element is the one whose removal DROPS the block, which for a
  // top-level block is the row and for a delegate-hosted one the bubble.
  if (row !== null) {
    row.appendChild(bubble.root);
    stampBlock(st, row, msgId, i);
    appendBlock(st, container, row);
  } else {
    stampBlock(st, bubble.root, msgId, i);
    appendBlock(st, container, bubble.root);
  }
  if (live && !st.detached) {
    const sig = ensureBlockTextSig(msgId, i, initial);
    // Watermark guard (design B5): append the delta only when it bridges the
    // accepted text to `full`; on any mismatch — a missed write, a replayed
    // write, a rebind onto a signal that advanced while unobserved — resync
    // from `full` instead. setText is growth-only, so a replay's resync (full
    // == accepted) is a no-op rather than a duplication.
    let accepted = initial.length;
    const cleanup = effect(() => {
      const v = sig.value;
      if (accepted + v.delta.length === v.full.length) {
        bubble.append(v.delta);
      } else {
        bubble.setText(v.full);
      }
      accepted = v.full.length;
    });
    pushLifetimeEffect(st, msgId, i, cleanup);
  }
}

function mountThinking(
  st: MsgRender,
  m: Message,
  container: HTMLElement,
  block: Block,
  i: number,
  live: boolean,
  idx: GroupIndex,
): void {
  const msgId = m.id;
  const initial = block.thinking ?? "";
  if (initial === "" && !live) {
    return; // an empty settled "Thinking completed" dropdown is worse than none
  }
  const view = buildReasoning(initial, live);
  st.reasonings.push(view);
  st.blockText.set(i, (full) => {
    view.setText(full);
  });
  // Append (sealing any open predecessor) BEFORE registering the new view, or
  // appendBlock would seal the trace being mounted.
  stampBlock(st, view.root, msgId, i);
  appendBlock(st, container, view.root);
  // Sealed from the STORE, not from what arrives next: a trace the store already
  // has a successor for is finished however this range reached it, which is what
  // keeps `openReasoning` to a container's genuinely-live trailing trace.
  if (containerFollowed(idx, containerKeyOf(block), i)) {
    view.seal();
  } else {
    st.openReasoning.set(container, view);
  }
  if (live && !st.detached) {
    const sig = ensureBlockThinkingSig(msgId, i, initial);
    const cleanup = effect(() => {
      // `.full`, no watermark: the reasoning view's setText appends only the
      // tail past its own rendered text, so full text is already self-healing.
      view.setText(sig.value.full);
    });
    pushLifetimeEffect(st, msgId, i, cleanup);
  }
}

function mountToolCard(
  st: MsgRender,
  msgId: string,
  container: HTMLElement,
  key: string,
  tc: ToolCall,
  i: number,
  runStart: number,
): void {
  const group = toolGroupFor(st, container, key, runStart);
  const card = mountToolCallCard(st.chatID, tc);
  card.setAttribute(RECONCILE_KEY, tc.id);
  stampBlock(st, card, msgId, i);
  // Cards live in the group's body region (the disclosure-collapsible
  // container), not on the group root beside the header.
  placeInContainer(st, groupBody(group), card);
  if (containerOpen(`tool:${tc.id}`) === true) {
    expandToolDetails(card); // a drop took this card while the reader had it open
  }
  refreshGroupHeader(group);
  // The slot is THIS render's, disposed with it: the transcript's card and the
  // subagent page's detached card for the same call come and go independently
  // (the slot registry is a multimap). st.disposers, not pushLifetimeEffect —
  // a transcript card outlives turn end, and park suspends it through the
  // registry rather than disposing it.
  pushDisposer(st, i, () => {
    disposeToolSlot(st.chatID, tc.id, card);
  });
}

function mountTodo(
  st: MsgRender,
  msgId: string,
  container: HTMLElement,
  tc: ToolCall,
  i: number,
): void {
  const list = buildTodoList(parseTodoItems(tc));
  list.dataset["toolId"] = tc.id;
  stampBlock(st, list, msgId, i);
  appendBlock(st, container, list);
  const sig = ensureToolCallSig(st.chatID, tc.id, tc);
  let last = tc;
  const cleanup = effect(() => {
    const next = sig.value;
    if (next === last) {
      return;
    }
    updateTodoList(list, parseTodoItems(next));
    last = next;
  });
  pushLifetimeEffect(st, msgId, i, () => {
    cleanup();
    releaseToolSig(st, tc.id);
  });
}

/** Wire the PIPELINE invocation's SHAPE and header onto its box, and the box's
 *  footer ledger onto every stage's members.
 *
 *  A PROMOTED pipeline paints nothing, so `driverNeedsBox` gates the whole paint.
 *  The writing itself is `paintPipeline`, which `pipelineBoxFor` also runs — one
 *  owner, and the WHY for painting unconditionally is stated there. Not folded into
 *  `bindSubagent`: the label comes from the stage COUNT, and the ledger sums across
 *  stages rather than one subtask's members. */
function bindPipeline(
  st: MsgRender,
  msgId: string,
  tc: ToolCall,
  live: boolean,
  blockIndex: number,
): void {
  if (st.boundBoxes.has(tc.id)) {
    return;
  }
  st.boundBoxes.add(tc.id);
  const paint = (next: ToolCall): void => {
    if (!driverNeedsBox(st, next)) {
      return;
    }
    paintPipeline(st, pipelineBoxFor(st, tc.id, live), next);
  };
  paint(tc);
  const sig = ensureToolCallSig(st.chatID, tc.id, tc);
  let last = tc;
  const cleanup = effect(() => {
    const next = sig.value;
    if (next === last) {
      return;
    }
    paint(next);
    last = next;
  });
  pushLifetimeEffect(st, msgId, blockIndex, () => {
    st.boundBoxes.delete(tc.id);
    cleanup();
    releaseToolSig(st, tc.id);
  });
}

/** The pipeline's ledger: every stage's members, summed.
 *
 *  The pipeline's own tool call owns the outcome and the wall-clock; the counts
 *  and the changed files belong to the stages that did the work, so this walks
 *  them. Changed files merge BY PATH rather than adding counts, matching the turn
 *  ledger's rule — two stages that touched one file each report that file's own
 *  totals, and adding them would double-count it. */
function pipelineSummary(st: MsgRender, invocation: ToolCall): TurnSummaryData {
  let commands = 0;
  let reads = 0;
  const changed: Record<string, FileChange> = {};
  for (const subtask of st.pipelineStages.get(invocation.id) ?? []) {
    const stage = subagentSummary(st, subtask, invocation);
    commands += stage.commands ?? 0;
    reads += stage.reads ?? 0;
    for (const [path, ch] of Object.entries(stage.changedFiles ?? {})) {
      const cur = changed[path] ?? { lines_added: 0, lines_removed: 0 };
      changed[path] = {
        lines_added: cur.lines_added + ch.lines_added,
        lines_removed: cur.lines_removed + ch.lines_removed,
      };
    }
  }
  const out: TurnSummaryData = { commands, reads, changedFiles: changed };
  if (!isToolActive(invocation.status)) {
    out.outcome = invocation.status === "failed" ? "failed" : "completed";
    const elapsed = invocation.duration_ms ?? 0;
    if (elapsed > 0) {
      out.elapsedMs = elapsed;
    }
  }
  return out;
}

/** Wire the subagent invocation tool's status/name/icon onto its block header,
 *  and its box's footer ledger onto the members' current state. */
function bindSubagent(
  st: MsgRender,
  subtask: string,
  msgId: string,
  sa: SubagentView,
  tc: ToolCall,
  blockIndex: number,
): void {
  if (st.boundBoxes.has(tc.id)) {
    return;
  }
  st.boundBoxes.add(tc.id);
  sa.setName(subagentLabel(tc));
  sa.setIcon(iconForSubagent(subagentName(tc)));
  sa.setStatus(tc.status);
  sa.setSummary(subagentSummary(st, subtask, tc));
  const sig = ensureToolCallSig(st.chatID, tc.id, tc);
  let last = tc;
  const cleanup = effect(() => {
    const next = sig.value;
    if (next === last) {
      return;
    }
    if (next.status !== last.status) {
      sa.setStatus(next.status);
    }
    const label = subagentLabel(next);
    if (label !== subagentLabel(last)) {
      sa.setName(label);
      sa.setIcon(iconForSubagent(subagentName(next)));
    }
    // The ledger re-derives on every invocation update. The members settle
    // BEFORE the invocation does (the delegate finishes last), so the settle
    // tick sees their final diffs; earlier ticks keep the running numbers
    // honest at no extra subscription cost.
    sa.setSummary(subagentSummary(st, subtask, next));
    last = next;
  });
  pushLifetimeEffect(st, msgId, blockIndex, () => {
    st.boundBoxes.delete(tc.id);
    cleanup();
    releaseToolSig(st, tc.id);
  });
}

/** The facts a delegate's footer can state honestly from the CLIENT's data:
 *  outcome, wall-clock, member command/read counts, and changed files with
 *  line counts summed from the members' own diff fragments — the same numbers
 *  the tool cards inside the box show as +N −M. Credits and the resolved
 *  model are deliberately absent: nothing on this wire carries them per
 *  delegate, and a fabricated number is worse than a missing row. */
function subagentSummary(st: MsgRender, subtask: string, invocation: ToolCall): TurnSummaryData {
  const members = st.subagentMembers.get(subtask);
  let commands = 0;
  let reads = 0;
  const changed: Record<string, FileChange> = {};
  for (const id of members ?? []) {
    const tc = peekToolCallSig(st.chatID, id);
    if (tc === undefined) {
      continue;
    }
    if (tc.kind === "execute" || tc.kind === "shell" || tc.kind === "command") {
      commands++;
    } else if (tc.kind === "read") {
      reads++;
    }
    for (const d of tc.diffs ?? []) {
      // lineDelta rather than stats(lineDiff(...)): it strips the trailing
      // newline first, so these numbers match the ones the SERVER computes for
      // the turn footer (internal/buffer/linediff.go) rather than counting the
      // empty line a final newline leaves behind.
      const s = lineDelta(d.old_text ?? "", d.new_text);
      const cur = changed[d.path] ?? { lines_added: 0, lines_removed: 0 };
      changed[d.path] = {
        lines_added: cur.lines_added + s.added,
        lines_removed: cur.lines_removed + s.removed,
      };
    }
  }
  const settled = !isToolActive(invocation.status);
  const out: TurnSummaryData = { commands, reads, changedFiles: changed };
  if (settled) {
    out.outcome = invocation.status === "failed" ? "failed" : "completed";
    const elapsed = invocation.duration_ms ?? 0;
    if (elapsed > 0) {
      out.elapsedMs = elapsed;
    }
  }
  return out;
}

// ---------------------------------------------------------------------------
// Reasoning + tool-group per-container bookkeeping
// ---------------------------------------------------------------------------

function sealReasoning(st: MsgRender, container: HTMLElement): void {
  const view = st.openReasoning.get(container);
  if (view !== undefined) {
    view.seal();
    st.openReasoning.delete(container);
  }
}

/** Append into a block container, closing the thinking block open there first.
 *
 *  The ONE door for the rule "anything posted after an open trace supersedes
 *  it". The wire carries no thinking-ended signal — a delta only extends the
 *  newest block of its own lane — so the next element's arrival IS the end
 *  signal (finalizeAssistantBody covers a trace nothing followed). Sealing at
 *  the append keeps the rule total: a mounter added later cannot forget it,
 *  because it cannot reach the DOM without it. */
function appendBlock(st: MsgRender, container: HTMLElement, el: HTMLElement): void {
  if (st.insertBefore === null) {
    // Only a TAIL append supersedes an open trace: an INSERTED block is posted
    // after nothing, and the trace it would seal is BELOW it.
    sealReasoning(st, container);
  }
  placeInContainer(st, container, el);
}

/** Place `el` in `container`: before the insertion reference while a head extension
 *  is in flight, at the end otherwise. The reference is the container's first child
 *  when the extension first touches it, captured HERE because every creation path
 *  reaches a container through this one function. It does not move as the extension
 *  proceeds, which is what keeps the inserted ordinals ascending. */
function placeInContainer(st: MsgRender, container: HTMLElement, el: HTMLElement): void {
  const refs = captureInsertRef(st, container);
  if (refs === null) {
    container.appendChild(el);
    return;
  }
  container.insertBefore(el, refs.get(container) ?? null);
}

/** Record `container`'s insertion boundary on the extension's FIRST touch even when it is
 *  null, and answer the reference map. `has` rather than `?? capture`: a container the
 *  extension created is empty then, so a re-capture takes its own first member. */
function captureInsertRef(
  st: MsgRender,
  container: HTMLElement,
): Map<HTMLElement, HTMLElement | null> | null {
  const refs = st.insertBefore;
  if (refs !== null && !refs.has(container)) {
    refs.set(container, container.firstElementChild as HTMLElement | null);
  }
  return refs;
}

/** Bring an ALREADY-MOUNTED node down to the ordinal being inserted, and step the
 *  reference past it. The reference is the boundary between inserted and pre-existing
 *  content, so only a node at or BELOW it moves: a card the insertion itself placed is
 *  above it, and moving that one carries it past every ordinal mounted since. A step
 *  card's launch is the reachable case, and the anchor ladder reads DOM order. */
function reseatInserted(st: MsgRender, container: HTMLElement, el: HTMLElement): void {
  const refs = captureInsertRef(st, container);
  if (refs === null) {
    return;
  }
  const ref = refs.get(container) ?? null;
  if (ref !== el) {
    if (
      ref === null ||
      (el.compareDocumentPosition(ref) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0
    ) {
      return;
    }
    container.insertBefore(el, ref);
  }
  refs.set(container, el.nextElementSibling as HTMLElement | null);
}

/** Place a lazily-created CONTAINER where the STORE puts it: above the first mounted
 *  ordinal after `at`, the index establishing it. The mount position is the RANGE's and
 *  nothing moves a box later, so a range reaching the container BELOW that index would
 *  leave it under ordinals the store puts after it — one run split into two groups, and
 *  one window reached two ways two documents. `at` absent appends. */
function placeContainer(
  st: MsgRender,
  host: HTMLElement,
  el: HTMLElement,
  at: number | undefined,
): void {
  const seat = at === undefined ? null : seatAbove(st, host, at, el);
  if (seat === null) {
    appendBlock(st, host, el);
    return;
  }
  // Not `appendBlock`: a box above mounted content supersedes no trace, and the reference
  // must be captured before the insert moves the first child.
  captureInsertRef(st, host);
  host.insertBefore(el, seat);
}

/** The group a card at run `runStart` joins, built on first use. */
function toolGroupFor(
  st: MsgRender,
  container: HTMLElement,
  key: string,
  runStart: number,
): HTMLDivElement {
  let bucket = st.toolGroups.get(key);
  if (bucket === undefined) {
    bucket = new Map();
    st.toolGroups.set(key, bucket);
  }
  let group = bucket.get(runStart);
  if (group === undefined) {
    group = buildToolGroupShell();
    appendBlock(st, container, group);
    bucket.set(runStart, group);
  }
  return group;
}

// ---------------------------------------------------------------------------
// Grouping and sealing, derived from the store rather than accumulated
// ---------------------------------------------------------------------------

/** Where each of a message's containers BREAKS its run of tool cards, and how far
 *  its content reaches. Built once per pass rather than answered per card: the
 *  per-card question is "what came before me in my own container", and
 *  re-classifying every earlier block is quadratic on one long tool loop. */
interface GroupIndex {
  /** container key → ascending indices at which a new run may START: one past a
   *  block that closed the container, a steer note's own anchor, or one past the
   *  block at which a nested container's box is ESTABLISHED. Every position is the
   *  STORE's, so one block answers one run start under any range — which a group's key
   *  needs, because the group outlives the pass that built it. */
  readonly starts: ReadonlyMap<string, number[]>;
  /** container key → the last block index that posts anything into it, in the
   *  STORE: a trace is finished by a successor the store holds, whether or not
   *  this range reached it. */
  readonly lastPost: ReadonlyMap<string, number>;
}

/** A block's container as a KEY: same key ⇒ same container. `containerFor`
 *  creates its container on demand, so the derivation may not call it. */
function containerKeyOf(block: Block): string {
  return block.agent_subtask_id ?? "";
}

function indexGroups(
  st: MsgRender,
  m: Message,
  marks: readonly SteerMark[],
  live: boolean,
): GroupIndex {
  const blocks = m.blocks ?? [];
  const lastIdx = blocks.length - 1;
  const tools = new Map((m.tool_calls ?? []).map((tc) => [tc.id, tc]));
  const starts = new Map<string, number[]>();
  const lastPost = new Map<string, number>();
  const built = new Set<string>();
  const startAt = (key: string, at: number): void => {
    const list = starts.get(key);
    if (list === undefined) {
      starts.set(key, [at]);
    } else {
      list.push(at);
    }
  };
  const post = (key: string, at: number, closes: boolean): void => {
    lastPost.set(key, at);
    if (closes) {
      startAt(key, at + 1);
    }
  };
  // Priced at the STORE's index (for a stage the host is its pipeline's box, whose own
  // creation posts at the top level), and RECORDED there for `placeContainer`: the run
  // break and the box's own seat are one fact, so no floor can move either.
  const openBox = (id: string, host: string, at: number): void => {
    if (built.has(id)) {
      return;
    }
    built.add(id);
    st.containerAt.set(id, at);
    lastPost.set(host, at);
    startAt(host, at + 1);
  };
  for (const [i, block] of blocks.entries()) {
    const key = containerKeyOf(block);
    const step = key === "" ? null : parseStepSubtask(key);
    if (step !== null) {
      if (hostsRunCard(st, step.workflowID)) {
        openBox(`run:${step.workflowID}`, "", i);
      }
    } else if (key !== "") {
      const pipelineID = st.stagePipeline.get(key);
      let host = "";
      if (pipelineID !== undefined && hostsPipelineBox(st, pipelineID)) {
        host = `pipe:${pipelineID}`;
        openBox(host, "", i);
      }
      openBox(`sub:${key}`, host, i);
    }
    switch (block.type) {
      case "text":
        post(key, i, true);
        break;
      case "thinking":
        if ((block.thinking ?? "") !== "" || (live && blockIsLive(blocks, i, lastIdx))) {
          post(key, i, true);
        }
        break;
      case "tool_use": {
        const tc = tools.get(block.tool_call_id ?? "");
        if (tc === undefined || isInternalToolTitle(tc.title)) {
          break; // mounts nothing, so it posts nothing
        }
        const runID = key === "" ? workflowInvocation(tc) : "";
        if (runID !== "") {
          // UNCONDITIONAL, unlike the step arm: `bindRunCard` re-homes the card into
          // the render holding the LAUNCH, so this block always mounts one here.
          openBox(`run:${runID}`, "", i);
        } else if (key === "" && isPipelineInvocation(tc)) {
          // Priced at the DRIVER's block, where the box stands: `driverNeedsBox`
          // stops asking for one at a count of 1, and the box outlives that.
          if (hostsPipelineBox(st, tc.id) || driverNeedsBox(st, tc)) {
            openBox(`pipe:${tc.id}`, "", i);
          }
        } else if (key !== "" && isSubagentInvocation(tc)) {
          break; // its box's header, not a post into the box
        } else {
          post(key, i, isTodoTool(tc));
        }
        break;
      }
    }
  }
  // A note is posted into the top level immediately BEFORE its anchor, so the
  // anchor itself is both where the next run may start and the position the note
  // counts as posted at.
  for (const mark of marks) {
    if (mark.anchor.msgID === m.id) {
      const at = mark.anchor.blockIndex;
      startAt("", at);
      lastPost.set("", Math.max(lastPost.get("") ?? -1, at));
    }
  }
  for (const list of starts.values()) {
    list.sort((a, b) => a - b);
  }
  return { starts, lastPost };
}

/** The index the run of tool cards holding block `i` started at, which is the
 *  key of the group that card joins. */
function groupRunStart(idx: GroupIndex, key: string, i: number): number {
  let start = 0;
  for (const at of idx.starts.get(key) ?? []) {
    if (at > i) {
      break;
    }
    start = at;
  }
  return start;
}

/** Whether anything is posted into `key` after block `i`: what seals a reasoning
 *  trace. */
function containerFollowed(idx: GroupIndex, key: string, i: number): boolean {
  return (idx.lastPost.get(key) ?? -1) > i;
}

/** Whether the run starting at `runStart` is FOLLOWED: a LATER run starts here,
 *  which happens only where something closed this one. The run's END, not its
 *  start — its own second and later cards post at their own indices, so
 *  `containerFollowed(runStart)` reads every multi-card run as followed. */
function runFollowed(idx: GroupIndex, key: string, runStart: number): boolean {
  const starts = idx.starts.get(key) ?? [];
  return (starts[starts.length - 1] ?? -1) > runStart;
}

/** Collapse every group in this render whose run is FOLLOWED in the store.
 *
 *  Idempotent, so a group mounted already-finished collapses at its first sync
 *  rather than waiting for a successor the store already holds. */
function syncGroupCollapse(st: MsgRender, idx: GroupIndex): void {
  for (const [key, bucket] of st.toolGroups) {
    for (const [runStart, group] of bucket) {
      if (runFollowed(idx, key, runStart)) {
        autoCollapseGroup(group);
      }
    }
  }
}

// ---------------------------------------------------------------------------
// Plan (a sibling after the block region)
// ---------------------------------------------------------------------------

function mountPlan(wrap: HTMLElement, m: Message): void {
  if (m.plan === undefined || m.plan.length === 0) {
    return;
  }
  const existing = wrap.querySelector<HTMLDivElement>(":scope > .plan-message");
  if (existing === null) {
    wrap.appendChild(planElement(m.plan));
  } else {
    updatePlanElement(existing, m.plan);
  }
}

// The turn footer is NOT mounted here any more. It is the TURN's outcome
// ledger, not a message's: a turn can hold several assistant messages (a
// mid-turn model switch splits one), so a per-message footer rendered two
// ledgers for one turn and each described a fragment. messages.ts owns it now,
// summing across the turn's body and rendering once on the card.

// ---------------------------------------------------------------------------
// Subagent + todo classification / parsing
// ---------------------------------------------------------------------------

// STEP_PREFIX / parseStepSubtask moved to step-subtask.ts: `handlers/messages.ts`
// needs the same question and must not import this module's render graph.

/** The tool call that STARTS a workflow run. Matched on the workflow id the
 *  server decoded off its `rawOutput`, never on the title: KAS titles it "Run
 *  Workflow" today and a title is display text that may be localized or
 *  reworded, while the id is the structural fact and the thing the card is keyed
 *  on. A call with no id is not a launch (or has not created its run yet) and
 *  renders as an ordinary tool card. */
function workflowInvocation(tc: ToolCall): string {
  return tc.workflow_id ?? "";
}

/** The prefix KAS puts on a PIPELINE STAGE's tool-call id. The full shape is
 *  `invoke_subagent_<orchestrateToolCallId>_stage_<stageName>`, which is the
 *  parent pointer this renderer needs and the reason no wire change was required:
 *  the stage names its pipeline in its own id, exactly as a workflow step names
 *  its run in `wf:<workflowId>:<nodePath>`. */
const STAGE_PREFIX = "invoke_subagent_";
const STAGE_SEP = "_stage_";

/** The orchestrate tool-call id a stage belongs to, or "" when the id is not
 *  stage-shaped (a plain `invoke_sub_agent` call has no pipeline).
 *
 *  `indexOf` for the separator, because only the RIGHT half can contain one: an
 *  orchestrate tool-call id is machine-minted and a stage NAME is author-supplied,
 *  so the FIRST occurrence is the seam and a stage called `run_stage_two` still
 *  resolves to its own driver. This read `lastIndexOf` and named that exact case as
 *  the reason for it, which is the spelling that breaks it — corrected 2026-08-26
 *  alongside `subagent-slice.ts`'s copy, which is pinned against the same literals.
 *  Latent rather than live: measured over the 65 distinct stage ids on the volume,
 *  every driver half is a `toolu_bdrk_*` id and none carries two separators, so no
 *  stage had yet been named in a way that tripped it. One that was would have
 *  resolved to a driver that does not exist and rendered as a flat sibling of its own
 *  pipeline. An empty half on either side returns "" and the stage renders at the top
 *  level, which is the pre-existing behaviour rather than a lost block. */
function stagePipelineID(tc: ToolCall): string {
  const id = tc.id;
  if (!id.startsWith(STAGE_PREFIX)) {
    return "";
  }
  const rest = id.slice(STAGE_PREFIX.length);
  const sep = rest.indexOf(STAGE_SEP);
  if (sep <= 0 || sep + STAGE_SEP.length >= rest.length) {
    return "";
  }
  return rest.slice(0, sep);
}

/** The tool call that STARTS a subagent-orchestration pipeline. */
function isPipelineInvocation(tc: ToolCall): boolean {
  return tc.title === "Orchestrate Sub-agent";
}

/** Learn which pipeline each stage subtask belongs to, and how many stages each
 *  DRIVER declared, from the message's tool calls alone.
 *
 *  Read from the TOOL CALL ARRAY rather than the frames so it has no ordering
 *  dependency: a stage's text block carries a bare subtask uuid, while its
 *  invocation carries that uuid AND its pipeline's id, so a stage whose text
 *  arrived first is still placed on the next pass. The driver branch precedes the
 *  subtask guard because a driver has none. A stage keeps its first pipeline. */
function indexPipelines(st: MsgRender, m: Message): void {
  for (const tc of m.tool_calls ?? []) {
    if (isPipelineInvocation(tc)) {
      st.pipelineDeclared.set(tc.id, declaredStageCount(tc));
      continue;
    }
    const subtask = tc.agent_subtask_id ?? "";
    if (subtask === "") {
      continue;
    }
    const pipelineID = stagePipelineID(tc);
    if (pipelineID === "" || st.stagePipeline.has(subtask)) {
      continue;
    }
    st.stagePipeline.set(subtask, pipelineID);
    const stages = st.pipelineStages.get(pipelineID);
    if (stages === undefined) {
      st.pipelineStages.set(pipelineID, [subtask]);
    } else {
      stages.push(subtask);
    }
  }
}

/** The pipeline box's header label: its KIND plus its stage count.
 *
 *  "Subagent pipeline", not "Pipeline": the bare word names no kind, and this app
 *  has a second container for delegated work — the run card, titled with its
 *  workflow's own recipe name. Byte-identical to `subagent-exec-source.ts`'s
 *  `ExecRun.label` for the same object, so transcript and page agree. No one-stage
 *  form, because a one-stage pipeline renders no container. `task` is unused: it is
 *  prose, and this header ellipsizes. */
function pipelineLabel(st: MsgRender, pipelineID: string): string {
  const n = pipelineStageCount(st, pipelineID);
  return n > 1 ? `Subagent pipeline · ${String(n)} stages` : "Subagent pipeline";
}

function declaredStageCount(tc: ToolCall): number {
  const input = tc.input;
  if (input !== undefined && input !== null && typeof input === "object") {
    const stages = (input as Record<string, unknown>)["stages"];
    if (Array.isArray(stages)) {
      return stages.length;
    }
  }
  return 0;
}

/** How many stages this pipeline has, as best this render can tell: the GREATER of the
 *  driver's declared count and the stages seen so far.
 *
 *  Both are lower bounds, which is why neither wins outright. Declared is the only
 *  source that knows a stage still on its way; observed is the only one that knows a
 *  driver dispatched MORE than it declared — where taking declared alone promoted
 *  every one of them to a flat sibling with no relation the DOM expresses. */
function pipelineStageCount(st: MsgRender, pipelineID: string): number {
  const declared = st.pipelineDeclared.get(pipelineID) ?? 0;
  return Math.max(declared, (st.pipelineStages.get(pipelineID) ?? []).length);
}

/** Whether this pipeline renders a CONTAINER at all.
 *
 *  ONE stage is PROMOTED instead: a container over a single card is two
 *  disclosures, two headers and two ledgers for one piece of work, and the
 *  promoted card keeps the page link a container lacks. Every other count keeps
 *  the container, ZERO included — a driver with no stage has nothing standing in
 *  for it, and a block that renders nothing is a lost block. */
function pipelineHasContainer(st: MsgRender, pipelineID: string): boolean {
  return pipelineStageCount(st, pipelineID) !== 1;
}

/** Whether the DRIVER's own block has a box to render.
 *
 *  Its own function rather than `pipelineHasContainer` at the call site because of
 *  the one exception: a driver that SETTLED having dispatched no stage would
 *  otherwise be invisible. Deferring to the settle is what stops that fallback
 *  displacing a stage still on its way. */
function driverNeedsBox(st: MsgRender, tc: ToolCall): boolean {
  if (pipelineHasContainer(st, tc.id)) {
    return true;
  }
  return !isToolActive(tc.status) && (st.pipelineStages.get(tc.id)?.length ?? 0) === 0;
}

/** kiro-cli's todo tracker surfaces as a `todo_list` tool call. Match the tool
 *  name loosely (todo_list / TodoList / "todo list" / todo-list). */
function isTodoTool(tc: ToolCall): boolean {
  return tc.title.toLowerCase().replace(/[\s_-]/g, "") === "todolist";
}

/** Tolerant parse of a todo_list tool's input into normalized items. The item
 *  shape varies (array of strings, {content|task|title|text, status|state});
 *  unknown shapes yield an empty list rather than throwing. */
function parseTodoItems(tc: ToolCall): TodoItem[] {
  const rows = todoRows(tc.input);
  const out: TodoItem[] = [];
  for (const row of rows) {
    if (typeof row === "string") {
      if (row.trim() !== "") {
        out.push({ content: row, status: "pending" });
      }
      continue;
    }
    if (row !== null && typeof row === "object") {
      const o = row as Record<string, unknown>;
      const content = firstString(o, ["content", "task", "title", "text", "name", "description"]);
      if (content !== "") {
        out.push({ content, status: normalizeTodoStatus(o["status"] ?? o["state"]) });
      }
    }
  }
  return out;
}

function todoRows(input: unknown): unknown[] {
  if (Array.isArray(input)) {
    return input;
  }
  if (input !== null && typeof input === "object") {
    const o = input as Record<string, unknown>;
    for (const key of ["todos", "items", "tasks", "list", "todo_list"]) {
      const v = o[key];
      if (Array.isArray(v)) {
        return v;
      }
    }
  }
  return [];
}

function firstString(o: Record<string, unknown>, keys: string[]): string {
  for (const k of keys) {
    const v = o[k];
    if (typeof v === "string" && v.trim() !== "") {
      return v;
    }
  }
  return "";
}

function normalizeTodoStatus(v: unknown): PlanStatus {
  const s = typeof v === "string" ? v.toLowerCase().replace(/[\s-]/g, "_") : "";
  if (s === "in_progress" || s === "active" || s === "doing" || s === "started") {
    return "in_progress";
  }
  if (
    s === "completed" ||
    s === "complete" ||
    s === "done" ||
    s === "checked" ||
    s === "finished"
  ) {
    return "completed";
  }
  return "pending";
}
