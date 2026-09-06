// Assistant body composition: the one dispatcher turning a message's `blocks`
// array into DOM, composed from the fundamentals/ primitives.

import type { Message, Block, ToolCall, PlanStatus, FileChange, SteerMark } from "./types.js";
import { effect, el } from "@cplieger/reactive";
import { KEY_ATTR as RECONCILE_KEY } from "./reconcile.js";
import { getActiveId } from "./store.js";
import {
  ensureBlockTextSig,
  ensureBlockThinkingSig,
  ensureToolCallSig,
  peekToolCallSig,
  clearToolCallSig,
} from "./store-signals.js";
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
import { planElement, updatePlanElement } from "./messages-plan.js";
import {
  buildToolGroupShell,
  groupBody,
  refreshGroupHeader,
  autoCollapseGroup,
} from "./tool-group.js";

// Re-exported for messages.ts to inject into messages-tools' status-flip path.
export { refreshGroupHeader };
import { iconForSubagent, isSubagentInvocation, subagentLabel, subagentName } from "./roles.js";
import { parseStepSubtask } from "./step-subtask.js";
import { buildRunCard, type RunCardView } from "./fundamentals/run-card.js";
import { invalidateRun, runState, forgetRun } from "./run-store.js";
import { runPendingAsks } from "./decision-dock.js";
import { hasTab } from "./tabs.js";
import { buildPath } from "./router.js";

// Callbacks injected by messages.ts, which owns avatar markup and the
// streaming-effect registry.

interface BlockCbs {
  /** Register a cleanup disposed on turn finalize / message unmount. */
  pushStreamingEffect(msgId: string, cleanup: () => void): void;
  /** Build an avatar row for a top-level assistant bubble. */
  makeRow(): HTMLDivElement;
  /** Put an undelivered steer's text back in the message box. Injected, not
   *  imported: the composer is above this module in the dependency order. */
  restoreSteer(text: string): void;
}

let cbs: BlockCbs = {
  pushStreamingEffect: () => {
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

// The live-anchor registry: which bubble Following pins to. Last-writer-wins, so
// the newest top-level bubble wins. Delegate-hosted bubbles never register — their
// box is collapsed with `height: 0` + `overflow: hidden`, so it reports offsets the
// reader cannot see.

let liveAnchor: { messageID: string; el: HTMLElement } | null = null;

/** The element Following pins to, or null for the document bottom.
 *
 *  Self-healing: a mid-turn rebuild replaces a message's render, so the slot can
 *  point at a detached element. When the anchor is not its own message's current
 *  top-level live bubble, re-derive from the render map. */
export function getLiveAnchor(): HTMLElement | null {
  if (liveAnchor !== null && renders.get(liveAnchor.messageID)?.topLiveEl !== liveAnchor.el) {
    liveAnchor = null;
    rescanLiveAnchor();
  }
  return liveAnchor?.el ?? null;
}

/** Point the slot at the newest still-live top-level bubble of the active chat, or
 *  leave it null. Registration order is mount order, so the last match is newest. */
function rescanLiveAnchor(): void {
  const activeChat = getActiveId();
  for (const [id, st] of renders) {
    if (!st.detached && st.chatID === activeChat && st.topLiveEl !== null) {
      liveAnchor = { messageID: id, el: st.topLiveEl };
    }
  }
}

/** Identity-guarded clear: only the registered element's own seal clears the slot.
 *  Only the ACTIVE chat's renders are rescan candidates — a pin into a parked chat
 *  would strand Following on an element the reader cannot see. */
function clearLiveAnchor(el: HTMLElement): void {
  if (liveAnchor?.el !== el) {
    return;
  }
  liveAnchor = null;
  rescanLiveAnchor();
}

// The open-container registry: which collapsible containers are open right now,
// keyed `sub:<subtaskID>`, read by the resume counter's reachability test. Detached
// renders register nothing — reachability is a property of the transcript.

const openContainers = new Set<string>();

function setContainerOpen(key: string, open: boolean): void {
  if (open) {
    openContainers.add(key);
  } else {
    openContainers.delete(key);
  }
}

/** The subtask ids whose container is open. A workflow step's `wf:` id is never a
 *  member — its blocks are dropped, so they are unreachable however the card folds. */
export function openContainerKeys(): ReadonlySet<string> {
  const out = new Set<string>();
  for (const key of openContainers) {
    if (key.startsWith("sub:")) {
      out.add(key.slice(4));
    }
  }
  return out;
}

/** Drop a render's container keys. */
function pruneContainers(st: MsgRender): void {
  if (st.detached) {
    return; // never registered
  }
  for (const subtask of st.subagents.keys()) {
    openContainers.delete(`sub:${subtask}`);
  }
}

// Per-message render state

/** What a container is told has arrived, from the point of view of the things open
 *  in it — NOT the wire's `Block["type"]`: a todo checklist is a `tool_use` block
 *  that must still CLOSE a tool group, because it is not a tool card.
 *
 *  Adding a kind is a TWO-part change — widen this union AND teach that kind's
 *  mounter to name itself, or the new kind is inert because no site names it. */
type ContinuationKind = "tool_use";

/** One open, auto-collapsible thing in one block container. Two registrants: a
 *  reasoning trace tolerates nothing, a tool group tolerates further tool calls. */
interface OpenCollapsible {
  /** Arrivals that CONTINUE this registrant. Empty means the next element of any
   *  kind ends it, which is what makes the first sibling text seal a trace. */
  readonly continues: readonly ContinuationKind[];
  /** Collapse or seal. MUST be idempotent — `supersede` drops the entry, but the
   *  callback's own bookkeeping (a `toolGroups` entry) is the callback's to clear. */
  readonly collapse: () => void;
}

interface MsgRender {
  /** The chat whose messages this render belongs to. Carried, not read from the
   *  store: the subagent page renders one delegate of whatever chat its tab names,
   *  so the active chat would key its cards under a chat that never writes them. */
  chatID: string;
  /** The `.assistant-blocks` container holding all top-level + subagent blocks. */
  blocksEl: HTMLElement;
  /** Count of blocks already mounted (index into m.blocks). */
  rendered: number;
  /** block index → a call that brings that block's DOM up to a full text. Read by
   *  `syncMountedText`. */
  blockText: Map<number, (full: string) => void>;
  /** subtask id → its SubagentBlock view. */
  subagents: Map<string, SubagentView>;
  /** orchestrate tool-call id → the PIPELINE box that call opened. Keyed by the
   *  invocation because the orchestrate call has no subtask of its own. */
  pipelines: Map<string, SubagentView>;
  /** stage subtask id → the orchestrate tool-call id that owns it. Built by
   *  `indexPipelines`; a stage's TEXT block carries only the bare subtask uuid. */
  stagePipeline: Map<string, string>;
  /** orchestrate tool-call id → its stage subtask ids, in first-seen order. */
  pipelineStages: Map<string, string[]>;
  /** orchestrate tool-call id → its declared `stages` length, `0` when absent or
   *  malformed. A FLOOR rather than the answer — see `pipelineStageCount`. */
  pipelineDeclared: Map<string, number>;
  /** workflow id → the run card THIS render hosts. Keyed by run: its only creator
   *  is the launch tool call, which belongs to exactly one message. */
  runs: Map<string, RunCardView>;
  /** workflow id → the ARMED render effect's disposer, absent while the card is
   *  suspended. Outside `disposers` because pause must stop the effect and release
   *  the clock WITHOUT running the card's final dispose. */
  runEffects: Map<string, () => void>;
  /** Cleanups that live as long as the MESSAGE, not as long as the turn.
   *
   *  Separate from `pushStreamingEffect`, which also disposes at TURN END: a run
   *  outlives its launching turn, so a card registered there would freeze. */
  disposers: (() => void)[];
  /** subtask id → the tool-call ids routed into that box, for the footer's ledger.
   *  The INVOCATION call is not a member — it is the box itself. */
  subagentMembers: Map<string, Set<string>>;
  /** Whether this render lives OUTSIDE the transcript (the subagent page). Two
   *  consequences: turn-lifetime cleanups go into `disposers`, because messages.ts
   *  has never heard of this render; and a cleanup must not clear a shared per-tool
   *  signal, which the transcript may still be reading. */
  detached: boolean;
  /** Every mounted bubble handle (for finalize end()). */
  bubbles: AssistantBubble[];
  /** The one bubble currently carrying the streaming caret, or null. `bubbles` is
   *  append-only with no notion of its tail, so without this pointer nothing can
   *  seal the block that WAS the tail when a new one opens. */
  liveBubble: AssistantBubble | null;
  /** This message's TOP-LEVEL streaming bubble root, or null: the live-anchor
   *  registry's per-message half. Delegate-hosted bubbles never set it. */
  topLiveEl: HTMLElement | null;
  /** Every mounted reasoning handle, for the turn-end seal. A different question
   *  from `autoCollapse`, which answers which one is still open. */
  reasonings: ReasoningView[];
  /** container → the open, auto-collapsible things in it, in mount order. One
   *  registry for the whole supersede rule, consulted once per append. */
  autoCollapse: Map<HTMLElement, OpenCollapsible[]>;
  /** The open tool group per container (consecutive tool cards share one). Answers
   *  where the next card gets appended, which `autoCollapse` does not. */
  toolGroups: Map<HTMLElement, HTMLDivElement>;
  /** Steer-mark ids already mounted, which is what makes `flushSteerNotes`
   *  idempotent: two call sites deliberately overlap and a mark renders once. */
  steerNotes: Set<string>;
}

const renders = new Map<string, MsgRender>();

// Public API (called by messages.ts)

/** Build the assistant body from scratch. Renders every block, then the plan. */
export function buildAssistantBody(
  wrap: HTMLElement,
  m: Message,
  chatID: string,
  live: boolean,
  marks: readonly SteerMark[] = [],
): void {
  buildBody(wrap, m, chatID, live, false, marks);
  mountPlan(wrap, m);
}

function buildBody(
  wrap: HTMLElement,
  m: Message,
  chatID: string,
  live: boolean,
  detached: boolean,
  marks: readonly SteerMark[] = [],
): void {
  const blocksEl = el("div", { className: "assistant-blocks" });
  wrap.appendChild(blocksEl);
  const st: MsgRender = {
    chatID,
    blocksEl,
    rendered: 0,
    blockText: new Map(),
    subagents: new Map(),
    pipelines: new Map(),
    stagePipeline: new Map(),
    pipelineStages: new Map(),
    pipelineDeclared: new Map(),
    runs: new Map(),
    runEffects: new Map(),
    disposers: [],
    subagentMembers: new Map(),
    detached,
    bubbles: [],
    liveBubble: null,
    topLiveEl: null,
    reasonings: [],
    autoCollapse: new Map(),
    toolGroups: new Map(),
    steerNotes: new Set(),
  };
  renders.set(m.id, st);
  const blocks = m.blocks ?? [];
  indexPipelines(st, m);
  renderRange(st, m, 0, blocks.length, live, marks);
}

/** Where a mount's turn-lifetime cleanup goes: messages.ts for a transcript render
 *  (disposed at turn end as well as on unmount), the render's own `disposers` for a
 *  DETACHED one — messages.ts does not know it exists, and a detached cleanup that
 *  cleared a shared signal would reach into the transcript's live cards. */
function pushLifetimeEffect(st: MsgRender, msgId: string, cleanup: () => void): void {
  if (st.detached) {
    st.disposers.push(cleanup);
    return;
  }
  cbs.pushStreamingEffect(msgId, cleanup);
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
): void {
  updateBody(wrap, m, chatID, streaming, false, marks);
  mountPlan(wrap, m);
}

/** The `tool`-cause fast path: refresh ONE mounted message through the same update
 *  path a full pass would run for it, touching no other render.
 *
 *  Returns false when nothing is mounted for `msgID` — only the full pass mounts. */
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
  updateAssistantBody(wrap, m, chatID, live, marks);
  return true;
}

/** Ids of renders still carrying live text: an unsealed live bubble, or any bubble
 *  whose caret has not drained. Detached renders report too; the transcript caller
 *  drops ids it never mounted. */
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
): void {
  const st = renders.get(m.id);
  if (st === undefined) {
    // Should not happen (build runs first), but stay self-healing.
    buildBody(wrap, m, chatID, streaming, detached, marks);
    return;
  }
  const blocks = m.blocks ?? [];
  // On EVERY pass: a stage's blocks can reach the dispatcher before its own
  // invocation tool call is in the store (out-of-order SSE).
  indexPipelines(st, m);
  // BEFORE the range, so an adopted box precedes whatever this pass mounts.
  rehomeStages(st, streaming);
  if (blocks.length > st.rendered) {
    renderRange(st, m, st.rendered, blocks.length, streaming, marks);
  }
  // OUTSIDE the block-growth guard: a steer read between two chunks adds no block,
  // so gating on growth would strand its note until the next block arrived.
  flushSteerNotes(st, marks, m.id, blocks.length);
  syncMountedText(st, m);
}

/** Bring every mounted block up to the store's current text for that block.
 *
 *  The FALLBACK path: the per-block signal effect is only created for a block the
 *  renderer judged live, so a misjudged one would freeze at the text it mounted
 *  with. Safe over a subscribed block — both writers own their own watermark. */
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
    // end(), not finishNow(): the turn is over, but the last block's reveal is text
    // the model really did produce last, so let it land.
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
    // A bubble mid-reveal holds a frame loop; finish it before its DOM goes.
    for (const b of st.bubbles) {
      b.finishNow();
    }
    disposeAll(st);
  }
  renders.delete(msgId);
}

/** The block-layer half of pausing a parked message: finish any reveal, then suspend
 *  the run cards (effects stop, clock holds release) without the final dispose. The
 *  streaming and tool-card effects are the callers' registries. */
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

/** Re-arm a resumed message's run cards; the effect's first run re-reads the run's
 *  cell. Idempotent per card. */
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

// The DETACHED render: one delegate's blocks, on its own page

/** The render id a detached body is keyed under. Derived rather than the bare message
 *  id, because `renders` is one map and the transcript already holds that entry. */
function detachedID(messageID: string, subtask: string): string {
  return `${messageID}#${subtask}`;
}

/** Render one delegate's own transcript into `host`, as the main agent's.
 *
 *  `m` is a SYNTHETIC message (subagent-slice.ts) with `agent_subtask_id` cleared:
 *  `containerFor` routes by that field, so a block still carrying it would rebuild
 *  the collapsed box this page exists to open. */
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

/** Run and clear a render's message-lifetime cleanups. Idempotent: both dispose paths
 *  can reach one render, and a store subscription disposed twice must not throw. */
function disposeAll(st: MsgRender): void {
  pruneContainers(st);
  for (const fn of st.disposers.splice(0)) {
    fn();
  }
}

// Block dispatch

function renderRange(
  st: MsgRender,
  m: Message,
  from: number,
  to: number,
  live: boolean,
  marks: readonly SteerMark[],
): void {
  const blocks = m.blocks ?? [];
  const lastIdx = blocks.length - 1;
  // A block is being appended, so whatever held the caret is no longer the tail.
  // Idempotent. A range whose every block is DROPPED must not seal, though: nothing
  // is placed, so ending the parent's caret would stop the reader's streaming reply
  // for an arrival that renders nothing at all.
  let places = false;
  for (let i = from; i < to; i++) {
    const block = blocks[i];
    if (block !== undefined && !isDroppedStep(block)) {
      places = true;
      break;
    }
  }
  if (places) {
    sealLiveBubble(st);
  }
  for (let i = from; i < to; i++) {
    const block = blocks[i];
    if (block === undefined) {
      continue;
    }
    // BEFORE the block, so a note anchored at index i lands above it.
    flushSteerNotes(st, marks, m.id, i);
    placeBlock(st, m, block, i, live && blockIsLive(blocks, i, lastIdx));
  }
  // A note anchored at the CURRENT end has no block to sit above yet and the loop
  // cannot reach it; mounting here puts it below everything so far.
  flushSteerNotes(st, marks, m.id, to);
  st.rendered = to;
}

/** Whether block `i` is the one its stream is still writing.
 *
 *  TEXT and tool_use blocks stream only at the ARRAY tail — exactly one streaming
 *  caret is a pinned invariant. A THINKING block streams while it is the last block
 *  of its OWN lane, which can sit behind the tail when a delegate interleaves; for a
 *  trace this decides the growth wiring and initial open state, not disclosure. */
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

/** Mount every not-yet-drawn steer note whose anchor this render has reached.
 *  Idempotent by mark id, required rather than defensive: `renderRange` and
 *  `updateAssistantBody` both call it and their ranges overlap by design. */
function flushSteerNotes(
  st: MsgRender,
  marks: readonly SteerMark[],
  msgID: string,
  upto: number,
): void {
  for (const mark of marks) {
    if (
      st.steerNotes.has(mark.id) ||
      mark.anchor.msgID !== msgID ||
      mark.anchor.blockIndex > upto
    ) {
      continue;
    }
    appendBlock(
      st,
      st.blocksEl,
      buildSteerNote({
        text: mark.text,
        origin: mark.origin,
        ...(mark.ack !== undefined ? { ack: mark.ack } : {}),
        dropped: mark.dropped === true,
        onRestore: () => {
          cbs.restoreSteer(mark.text);
        },
      }),
    );
    st.steerNotes.add(mark.id);
  }
}

/** End the bubble currently carrying the caret, if any. */
function sealLiveBubble(st: MsgRender): void {
  // finishNow, not end: the tail MOVED, so this block's residual reveal is no longer
  // live text, and "exactly one streaming caret" is the invariant this function
  // keeps. The turn's LAST block gets the graceful drain instead.
  st.liveBubble?.finishNow();
  st.liveBubble = null;
}

/** Resolve the container a block renders into: the top-level `.assistant-blocks`, or
 *  a SubagentBlock's body for a subagent's blocks.
 *
 *  A PIPELINE STAGE is the two-level case: its subtask id is a bare uuid, so the
 *  pipeline and the stage's own box come from `st.stagePipeline` (`indexPipelines`). */
function containerFor(st: MsgRender, block: Block, live: boolean): HTMLElement {
  const subtask = block.agent_subtask_id ?? "";
  if (subtask === "") {
    return st.blocksEl;
  }
  let sa = st.subagents.get(subtask);
  if (sa === undefined) {
    sa = buildSubagentBlock("Subagent", live ? "in_progress" : "completed", {
      ...subagentOpenerFor(st, subtask),
      ...(st.detached
        ? {}
        : {
            onOpenChange: (open: boolean): void => {
              setContainerOpen(`sub:${subtask}`, open);
            },
          }),
    });
    sa.root.dataset["subtask"] = subtask;
    st.subagents.set(subtask, sa);
    // The box lands in its HOST (top level or a pipeline body), so the supersede
    // belongs to the host — `appendBlock`'s job.
    const host = stageHostFor(st, subtask, live);
    appendBlock(st, host, sa.root);
  }
  return sa.body;
}

/** The delegate card's footer link, or nothing.
 *
 *  Injected rather than imported so `fundamentals/` keeps pointing downward, and lazy
 *  because `subagent-view.ts` reaches the whole page. A DETACHED render gets no link:
 *  it IS the page. The chat id is the RENDER's, the one the blocks came from. */
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

/** Where a stage's own box goes: its pipeline's body when that pipeline has a
 *  container, otherwise the top level. Building the box here makes the two arrival
 *  orders equivalent. An EXISTING container wins over the count, so that check is
 *  first — the count can rise after a container exists. */
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

/** Re-derive every mounted delegate box's host from `stageHostFor` and move the ones
 *  that no longer sit in it. Idempotent: it runs on every pass, and re-appending a box
 *  already in place would re-fire its `vk-slide-up` mount animation for nothing.
 *
 *  A host is chosen when the box is BUILT, from an index learned lazily — a stage's
 *  text can reach the dispatcher before its own invocation tool call is in the store —
 *  and `containerFor` asks for one only while it is creating a box, so the mapping
 *  arriving later reached nothing and left the stage a sibling of its own pipeline. */
function rehomeStages(st: MsgRender, live: boolean): void {
  for (const [subtask, sa] of st.subagents) {
    const host = stageHostFor(st, subtask, live);
    if (sa.root.parentElement !== host) {
      host.appendChild(sa.root);
    }
  }
}

/** Write the driver's header onto its box: the label from the stage COUNT, the
 *  status and the footer ledger from the driver's own call. */
function paintPipeline(st: MsgRender, box: SubagentView, driver: ToolCall): void {
  box.setName(pipelineLabel(st, driver.id));
  box.setStatus(driver.status);
  box.setSummary(pipelineSummary(st, driver));
}

/** Get or build the PIPELINE box for one orchestrate tool call.
 *
 *  It also ADOPTS any stage of its own sitting at the top level, the upgrade path
 *  after a lone stage was promoted. A RE-PARENT, never a rebuild: the move carries
 *  the disclosure, the observers and every effect with the node. */
function pipelineBoxFor(st: MsgRender, pipelineID: string, live: boolean): SubagentView {
  const existing = st.pipelines.get(pipelineID);
  if (existing !== undefined) {
    return existing;
  }
  const box = buildSubagentBlock(
    pipelineLabel(st, pipelineID),
    live ? "in_progress" : "completed",
    { activity: "container" },
  );
  box.root.dataset["pipeline"] = pipelineID;
  st.pipelines.set(pipelineID, box);
  // Same-pass adoption; `rehomeStages` is the general case.
  const promoted = (st.pipelineStages.get(pipelineID) ?? [])
    .map((subtask) => st.subagents.get(subtask))
    .filter((v): v is SubagentView => v?.root.parentElement === st.blocksEl);
  const first = promoted[0];
  if (first === undefined) {
    appendBlock(st, st.blocksEl, box.root);
  } else {
    // Lands where the first adopted stage sat, keeping transcript order. NOT
    // `appendBlock`: swapping a node already in place posts nothing after anything.
    first.root.replaceWith(box.root);
  }
  for (const v of promoted) {
    box.body.appendChild(v.root);
  }
  // A box the STAGE path built paints itself, because nothing else will: the driver's
  // effect re-runs only when its tool call CHANGES, and a settled driver never does.
  const driver = peekToolCallSig(st.chatID, pipelineID);
  if (driver !== undefined) {
    paintPipeline(st, box, driver);
  }
  return box;
}

/** Get or build the run card for one workflow id, and subscribe it to the store. The
 *  subscription is why the card needs no event handling of its own: `run-store.ts`
 *  holds a signal per run, and one effect per card re-renders it. */
function runCardFor(st: MsgRender, workflowID: string, name: string): RunCardView {
  const existing = st.runs.get(workflowID);
  if (existing !== undefined) {
    return existing;
  }
  // The footer link re-opens the run's tab: injected rather than imported so
  // `fundamentals/` points downward, and lazy because `run-view.ts` reaches the whole
  // run page. The parent is THIS render's chat; the run store's own record of it is
  // fed by SSE and answers nothing before the first frame.
  const chatID = st.chatID;
  const card = buildRunCard(workflowID, name, (id, label, focusNode) => {
    void import("./run-view.js")
      .then(({ openRunView }) => {
        // The third argument is what makes a step row a DOOR: two args means "the
        // run", a row passes its own node path and means "the run, at this step".
        openRunView(id, label, chatID, focusNode ?? "");
      })
      .catch(() => {
        /* noop: the link degrades to its href on the next click */
      });
  });
  st.runs.set(workflowID, card);
  appendBlock(st, st.blocksEl, card.root);
  st.disposers.push(() => {
    disarmRunCard(st, workflowID, card);
    // Three surfaces read a run's cell and none is last on its own. A card unmounting
    // with no run tab open IS last, and a later invalidate re-creates the cell, so
    // forgetting early costs one fetch rather than a wrong answer.
    if (!hasTab("run", workflowID)) {
      forgetRun(workflowID);
    }
  });
  // The first read the card ever gets; every later one arrives through the effect.
  invalidateRun(workflowID);
  armRunCard(st, workflowID, card);
  return card;
}

/** Adopt the launch tool call into its run's card: the recipe name as a placeholder
 *  label, and a failed launch reported on the card rather than lost. A launch that
 *  FAILED never created a run, so the tool call is the only witness and the card
 *  would otherwise sit at "starting" forever. */
function bindRunCard(st: MsgRender, workflowID: string, tc: ToolCall): void {
  const card = runCardFor(st, workflowID, recipeNameOf(tc));
  card.setLaunch(tc.status, tc.output);
}

/** The recipe a launch names, from the tool call's own input. A placeholder only:
 *  every render prefers the run's `runLabel`. */
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

// The shared run clock: ONE interval for every card on screen, stopped when no card
// holds it. Holders are REFCOUNTED per workflow id — the same run can be on screen
// more than once (a parked chat's card, a subagent page's, the composer band's run
// bar), so a release names WHICH holder let go.

/** What the clock needs of a holder, which is one method. Narrower than `RunCardView`
 *  because the second consumer is `run-bar.ts`, not a card. */
export interface RunClockHolder {
  tick(): void;
}

const clockHolders = new Map<string, Set<RunClockHolder>>();
let clockTimer: ReturnType<typeof setInterval> | undefined;

/** Tick this run's clock once a second while `holder` is showing it. Refcounted;
 *  `releaseRunClock` is the other half and both are idempotent. */
export function holdRunClock(workflowID: string, card: RunClockHolder): void {
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

/** Stop ticking for one holder. The interval dies with the last one. */
export function releaseRunClock(workflowID: string, card: RunClockHolder): void {
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

/** Subscribe a run card to its store cell and hold the shared clock. `disarmRunCard`
 *  is the suspend half; both are idempotent. */
function armRunCard(st: MsgRender, workflowID: string, card: RunCardView): void {
  if (st.runEffects.has(workflowID)) {
    return;
  }
  const stop = effect(() => {
    // Two inputs on different clocks: `inspect` says what the steps are doing, the
    // dock says which is blocked on a person. The run's status cannot carry the second
    // — KAS blocks the asking step's turn and leaves the run `running`.
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

/** Whether this block belongs to a WORKFLOW STEP, and is therefore not rendered in the
 *  transcript at all. Keyed on the PARSE, never on the `wf:` prefix: a malformed id
 *  parses to null and keeps its existing delegate-box fallback. */
function isDroppedStep(block: Block): boolean {
  return parseStepSubtask(block.agent_subtask_id ?? "") !== null;
}

function placeBlock(st: MsgRender, m: Message, block: Block, i: number, live: boolean): void {
  // DROPPED, before `containerFor` runs: the run card is the RECORD of a run and
  // renders no step content. Explicit rather than a removed route — merely unrouting
  // would let these fall through to `st.blocksEl` as loose top-level content. At the
  // dispatch site because `containerFor` must return an element.
  if (isDroppedStep(block)) {
    return;
  }
  const container = containerFor(st, block, live);
  const subtask = block.agent_subtask_id ?? "";

  switch (block.type) {
    case "text": {
      mountText(st, m.id, container, block, i, live);
      return;
    }
    case "thinking": {
      mountThinking(st, m.id, container, block, i, live);
      return;
    }
    case "tool_use": {
      const tc = m.tool_calls?.find((c) => c.id === block.tool_call_id);
      if (tc === undefined) {
        return; // referenced tool call not in the store yet (out-of-order SSE)
      }
      // Only matches TRANSCRIPTS PERSISTED BEFORE 2026-08-31, when the engine stopped
      // emitting these: their card sits stuck at in_progress forever. Title-keyed
      // because the persisted ToolCall carries no tool id.
      if (isInternalToolTitle(tc.title)) {
        return;
      }
      // A WORKFLOW LAUNCH becomes the run's card, not a tool row. The call has no
      // subtask of its own, so this branch is ahead of the subtask checks. A card a
      // step's earlier frame already built is FOUND here rather than replaced.
      const runID = workflowInvocation(tc);
      if (subtask === "" && runID !== "") {
        bindRunCard(st, runID, tc);
        return;
      }
      // A PIPELINE LAUNCH becomes the pipeline's box, not a tool row — same shape as
      // the workflow launch above, and ahead of the subtask checks for the same reason.
      if (subtask === "" && isPipelineInvocation(tc)) {
        bindPipeline(st, m.id, tc, live);
        return;
      }
      // The subagent invocation becomes the SubagentBlock's header, not a card.
      if (subtask !== "" && isSubagentInvocation(tc)) {
        const sa = st.subagents.get(subtask);
        if (sa !== undefined) {
          bindSubagent(st, subtask, m.id, sa, tc);
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
        // A todo checklist is a tool_use block that is NOT a tool card, so it
        // supersedes an open group like any other element (via `appendBlock`).
        mountTodo(st, m.id, container, tc);
        return;
      }
      mountToolCard(st, container, tc);
      return;
    }
  }
}

// Block mounters

function mountText(
  st: MsgRender,
  msgId: string,
  container: HTMLElement,
  block: Block,
  i: number,
  live: boolean,
): void {
  const initial = block.text ?? "";
  // Only a top-level live bubble joins the anchor registry; its seal callback clears
  // this message's slot.
  const topLive = live && !st.detached && container === st.blocksEl;
  // Top-level bubbles carry a row; subagent-body bubbles don't (the header is the
  // identity). Created BEFORE the bubble so the initial blank report lands on it.
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
  if (row !== null) {
    row.appendChild(bubble.root);
    appendBlock(st, container, row);
  } else {
    appendBlock(st, container, bubble.root);
  }
  if (live && !st.detached) {
    const sig = ensureBlockTextSig(msgId, i, initial);
    // Watermark guard: append the delta only when it bridges the accepted text to
    // `full`; on any mismatch — missed write, replayed write, rebind onto a signal
    // that advanced unobserved — resync from `full`. setText is growth-only.
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
    pushLifetimeEffect(st, msgId, cleanup);
  }
}

function mountThinking(
  st: MsgRender,
  msgId: string,
  container: HTMLElement,
  block: Block,
  i: number,
  live: boolean,
): void {
  const initial = block.thinking ?? "";
  if (initial === "" && !live) {
    return; // an empty settled "Thinking completed" dropdown is worse than none
  }
  const view = buildReasoning(initial, live);
  st.reasonings.push(view);
  st.blockText.set(i, (full) => {
    view.setText(full);
  });
  // Append BEFORE registering, or the append's own supersede would seal the trace
  // being mounted. The append is also what interrupts a consecutive tool run —
  // without it, later cards keep joining the group element ABOVE the trace.
  appendBlock(st, container, view.root);
  // A trace tolerates NOTHING, which is what makes the first sibling text seal it.
  register(st, container, {
    continues: [],
    collapse: () => {
      view.seal();
    },
  });
  if (live && !st.detached) {
    const sig = ensureBlockThinkingSig(msgId, i, initial);
    const cleanup = effect(() => {
      // No watermark: the reasoning view's setText appends only the tail past its own
      // rendered text, so full text is already self-healing.
      view.setText(sig.value.full);
    });
    pushLifetimeEffect(st, msgId, cleanup);
  }
}

function mountToolCard(st: MsgRender, container: HTMLElement, tc: ToolCall): void {
  // The one site that consults the registry explicitly, because a tool card is the one
  // arrival an open group TOLERATES.
  supersede(st, container, "tool_use");
  const group = toolGroupFor(st, container);
  const card = mountToolCallCard(st.chatID, tc);
  card.setAttribute(RECONCILE_KEY, tc.id);
  // Cards live in the group's body region, not on the group root beside the header.
  groupBody(group).appendChild(card);
  refreshGroupHeader(group);
  // st.disposers, not pushLifetimeEffect: a transcript card outlives turn end, and park
  // suspends it through the registry rather than disposing it. The slot is THIS
  // render's — the transcript's and the page's cards for one call are separate.
  st.disposers.push(() => {
    disposeToolSlot(st.chatID, tc.id, card);
  });
}

function mountTodo(st: MsgRender, msgId: string, container: HTMLElement, tc: ToolCall): void {
  const list = buildTodoList(parseTodoItems(tc));
  list.dataset["toolId"] = tc.id;
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
  pushLifetimeEffect(st, msgId, () => {
    cleanup();
    releaseToolSig(st, tc.id);
  });
}

/** Wire the PIPELINE invocation's shape and header onto its box, and the box's footer
 *  ledger onto every stage's members. A PROMOTED pipeline paints nothing, so
 *  `driverNeedsBox` gates the whole paint; the writing itself is `paintPipeline`. */
function bindPipeline(st: MsgRender, msgId: string, tc: ToolCall, live: boolean): void {
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
  pushLifetimeEffect(st, msgId, () => {
    cleanup();
    releaseToolSig(st, tc.id);
  });
}

/** The pipeline's ledger: every stage's members, summed. Changed files merge BY PATH
 *  rather than adding counts — two stages that touched one file each report that
 *  file's own totals, and adding them would double-count it. */
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
): void {
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
    // The members settle BEFORE the invocation does (the delegate finishes last), so
    // the settle tick sees their final diffs.
    sa.setSummary(subagentSummary(st, subtask, next));
    last = next;
  });
  pushLifetimeEffect(st, msgId, () => {
    cleanup();
    releaseToolSig(st, tc.id);
  });
}

/** The facts a delegate's footer can state honestly from the CLIENT's data: outcome,
 *  wall-clock, member command/read counts, and changed files with line counts. Credits
 *  and the resolved model are absent — nothing on this wire carries them per delegate. */
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
      // lineDelta, not stats(lineDiff(...)): it strips the trailing newline first, so
      // these match the server's numbers (internal/buffer/linediff.go).
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

// The auto-collapse registry: one open, auto-collapsible thing per container

/** Enrol an open collapsible in its container's registry. */
function register(st: MsgRender, container: HTMLElement, entry: OpenCollapsible): void {
  const list = st.autoCollapse.get(container);
  if (list === undefined) {
    st.autoCollapse.set(container, [entry]);
  } else {
    list.push(entry);
  }
}

/** Collapse and deregister every registrant in `container` that does not tolerate an
 *  arrival of `kind`; `null` is the arrival nobody tolerates. The ONE consult door, so
 *  what continues a registrant is a DECLARED property rather than an append-path accident. */
function supersede(st: MsgRender, container: HTMLElement, kind: ContinuationKind | null): void {
  const list = st.autoCollapse.get(container);
  if (list === undefined) {
    return;
  }
  const kept: OpenCollapsible[] = [];
  for (const entry of list) {
    if (kind !== null && entry.continues.includes(kind)) {
      kept.push(entry);
      continue;
    }
    entry.collapse();
  }
  if (kept.length === 0) {
    st.autoCollapse.delete(container);
  } else {
    st.autoCollapse.set(container, kept);
  }
}

/** Append into a block container, superseding whatever is open there first.
 *
 *  The ONE door for "anything posted after an open collapsible ends it": the wire
 *  carries no thinking-ended or tool-run-ended signal, so the next element's arrival IS
 *  the end signal. Turn end is asymmetric on purpose — `finalizeAssistantBody` seals
 *  every trace and collapses no group, because a completed tool run is the turn's result. */
function appendBlock(st: MsgRender, container: HTMLElement, el: HTMLElement): void {
  supersede(st, container, null);
  container.appendChild(el);
}

/** The open tool group for a container, building and registering one if there is none.
 *  Plain `appendChild`: the caller has already run `supersede(…, "tool_use")`, and
 *  going through `appendBlock` would supersede the group being created. */
function toolGroupFor(st: MsgRender, container: HTMLElement): HTMLDivElement {
  let group = st.toolGroups.get(container);
  if (group === undefined) {
    group = buildToolGroupShell();
    container.appendChild(group);
    st.toolGroups.set(container, group);
    register(st, container, {
      continues: ["tool_use"],
      collapse: () => {
        closeToolGroup(st, container);
      },
    });
  }
  return group;
}

/** The tool group's collapse callback: drop the append-target entry, then fold the box.
 *  Keyed by container, so anything that is not a tool call splits the run in two. */
function closeToolGroup(st: MsgRender, container: HTMLElement): void {
  const group = st.toolGroups.get(container);
  st.toolGroups.delete(container);
  if (group !== undefined) {
    autoCollapseGroup(group);
  }
}

// Plan (a sibling after the block region)

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

// Subagent + todo classification / parsing

/** The tool call that STARTS a workflow run. Matched on the workflow id the server
 *  decoded off its `rawOutput`, never on the title, which is display text. A call with
 *  no id renders as an ordinary tool card. */
function workflowInvocation(tc: ToolCall): string {
  return tc.workflow_id ?? "";
}

/** The prefix KAS puts on a PIPELINE STAGE's tool-call id, whose full shape is
 *  `invoke_subagent_<orchestrateToolCallId>_stage_<stageName>`. */
const STAGE_PREFIX = "invoke_subagent_";
const STAGE_SEP = "_stage_";

/** The orchestrate tool-call id a stage belongs to, or "" when the id is not
 *  stage-shaped. `indexOf` for the separator, not `lastIndexOf`: the driver half is
 *  machine-minted and a stage NAME is author-supplied, so the FIRST occurrence is the
 *  seam and a stage called `run_stage_two` still resolves to its own driver.
 *  `subagent-slice.ts` parses the same id shape against the same literals. */
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

/** Learn which pipeline each stage subtask belongs to, and how many stages each DRIVER
 *  declared, from the message's tool calls alone. Read from the tool-call ARRAY rather
 *  than the frames so it has no ordering dependency: a stage whose text arrived before
 *  its invocation is still placed on the next pass. A stage keeps its first pipeline. */
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

/** The pipeline box's header label: its KIND plus its stage count. "Subagent pipeline",
 *  not "Pipeline" — the run card is this app's other container for delegated work.
 *  Byte-identical to `subagent-exec-source.ts`'s `ExecRun.label` for the same object. */
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

/** How many stages this pipeline has: the GREATER of the driver's declared count and
 *  the stages seen so far. Both are lower bounds — declared is the only source that
 *  knows a stage still on its way, observed the only one that knows a driver dispatched
 *  more than it declared. */
function pipelineStageCount(st: MsgRender, pipelineID: string): number {
  const declared = st.pipelineDeclared.get(pipelineID) ?? 0;
  return Math.max(declared, (st.pipelineStages.get(pipelineID) ?? []).length);
}

/** Whether this pipeline renders a CONTAINER at all. ONE stage is PROMOTED instead: a
 *  container over a single card is two disclosures and two ledgers for one piece of
 *  work. Every other count keeps the container, ZERO included — nothing stands in for a
 *  driver with no stage, and a block that renders nothing is a lost block. */
function pipelineHasContainer(st: MsgRender, pipelineID: string): boolean {
  return pipelineStageCount(st, pipelineID) !== 1;
}

/** Whether the DRIVER's own block has a box to render. Its own function because of the
 *  one exception: a driver that SETTLED having dispatched no stage would otherwise be
 *  invisible, and deferring to the settle stops that fallback displacing a live stage. */
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

/** Tolerant parse of a todo_list tool's input into normalized items. Unknown shapes
 *  yield an empty list rather than throwing. */
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
