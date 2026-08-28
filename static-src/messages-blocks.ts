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
// Before that join the two rendered as flat siblings with no relation the DOM
// could express, and both spun: the driver as an ordinary tool card at 0.8s, the
// stage box at 0.6s, so two rings beat against each other for one delegated task.
// The pipeline box is therefore built with the `container` activity variant, which
// keeps its glyph and shows activity dots only while it is collapsed.
// ---------------------------------------------------------------------------

import type { Message, Block, ToolCall, PlanStatus, FileChange, SteerMark } from "./types.js";
import { effect, el } from "@cplieger/reactive";
import { KEY_ATTR as RECONCILE_KEY } from "./reconcile.js";
import {
  ensureBlockTextSig,
  ensureBlockThinkingSig,
  ensureToolCallSig,
  peekToolCallSig,
  clearToolCallSig,
} from "./store-signals.js";
import { lineDiff, stats } from "./diff.js";
import { isToolActive } from "./tool-schema.js";
import type { TurnSummaryData } from "./fundamentals/turn-footer.js";
import { buildAssistantBubble, type AssistantBubble } from "./fundamentals/text-bubble.js";
import { buildReasoning, type ReasoningView } from "./fundamentals/reasoning.js";
import {
  buildSubagentBlock,
  type SubagentOpener,
  type SubagentView,
} from "./fundamentals/subagent-block.js";
import { buildTodoList, updateTodoList, type TodoItem } from "./fundamentals/todo.js";
import { buildSteerNote } from "./fundamentals/steer-note.js";
import { toolSpecFor } from "./messages-tools.js";
import { planElement, updatePlanElement } from "./messages-plan.js";
import { buildToolGroupShell, groupBody, refreshGroupHeader } from "./tool-group.js";

// Re-exported so messages.ts can inject it into messages-tools' status-flip
// path (initToolCallbacks) — the same header renderer the block dispatcher uses.
export { refreshGroupHeader };
import { iconForSubagent, isSubagentInvocation, subagentLabel, subagentName } from "./roles.js";
import { ICON_TAB_RUN } from "./icons.js";
import { buildRunCard, type RunCardView } from "./fundamentals/run-card.js";
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
  makeRow: () => el("div") as HTMLDivElement,
  restoreSteer: () => {
    /* until init */
  },
};

export function initBlockRenderer(c: BlockCbs): void {
  cbs = c;
}

/**
 * The live TOP-LEVEL text bubble under `root`, or null.
 *
 * This is what Following pins to (`scroll.ts` `setAnchorProvider`), and it lives
 * here because this module is what puts `.streaming` on a bubble and what builds
 * the delegate boxes. Two rules, and both exist because a pin above the live edge
 * strands the reader mid-transcript:
 *
 *   - THE LAST match, not the first. `sealLiveBubble` is per-MESSAGE state, so a
 *     turn split across two assistant messages (a mid-turn model switch does
 *     that) leaves the earlier message's trailing bubble marked until the turn
 *     finalizes. A `querySelector` returns document order, which is the OLDEST
 *     of them.
 *   - NOT a delegate's bubble. A subagent's or workflow step's live text streams
 *     inside a box that is collapsed by default, and `height: 0` +
 *     `overflow: hidden` clips that content without taking it out of layout — so
 *     it reports offsets inside a box contributing no height to the document,
 *     and the pin they produce points at nothing the reader can see. With no
 *     top-level bubble streaming, null is the right answer: the document bottom
 *     is where the box's own rolling tail and its footer are.
 */
export function liveTextAnchor(root: HTMLElement): HTMLElement | null {
  const live = root.querySelectorAll<HTMLElement>(".message.assistant.streaming");
  for (let i = live.length - 1; i >= 0; i--) {
    const bubble = live[i];
    if (bubble?.closest(".subagent-body") === null) {
      return bubble;
    }
  }
  return null;
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
  /** Count of blocks already mounted (index into m.blocks). */
  rendered: number;
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
  /** workflow id → the run card that invocation opened. Keyed by RUN, not by
   *  subtask, because one card holds every step of one run — the step rows inside
   *  it are keyed by node path (see `runContainerFor`). */
  runs: Map<string, RunCardView>;
  /** Cleanups that live as long as the MESSAGE, not as long as the turn.
   *
   *  Separate from `pushStreamingEffect` and that separation is load-bearing: that
   *  one is disposed at TURN END as well as on unmount (its own comment says so),
   *  which is correct for a caret or a tool-card status effect and exactly wrong
   *  for a run card. `run_workflow` returns as soon as the run STARTS, so the
   *  launching turn ends while the run carries on for minutes — releasing the
   *  card's store subscription and its clock there would freeze it at the moment
   *  it matters most. Disposed by `disposeAssistantBody` / `resetBlockRenders`,
   *  the real unmount paths. */
  disposers: (() => void)[];
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
  /** Every mounted reasoning handle (for finalize seal()). */
  reasonings: ReasoningView[];
  /** The still-open reasoning block per container (auto-collapse on sibling). */
  openReasoning: Map<HTMLElement, ReasoningView>;
  /** The open tool group per container (consecutive tool cards share one). */
  toolGroups: Map<HTMLElement, HTMLDivElement>;
  /** Steer-mark ids already mounted into this message's block stream. This set
   *  is what makes `flushSteerNotes` idempotent: it is called from two places
   *  that deliberately overlap, and a mark must render exactly once. */
  steerNotes: Set<string>;
}

const renders = new Map<string, MsgRender>();

// ---------------------------------------------------------------------------
// Public API (called by messages.ts)
// ---------------------------------------------------------------------------

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
    runs: new Map(),
    disposers: [],
    subagentMembers: new Map(),
    detached,
    bubbles: [],
    liveBubble: null,
    reasonings: [],
    openReasoning: new Map(),
    toolGroups: new Map(),
    steerNotes: new Set(),
  };
  renders.set(m.id, st);
  const blocks = m.blocks ?? [];
  indexPipelines(st, m);
  renderRange(st, m, 0, blocks.length, live, marks);
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
  // Ahead of the render, and on EVERY pass rather than only when blocks arrive: a
  // stage's blocks can reach the dispatcher before its own invocation tool call is
  // in the store (out-of-order SSE), and this index is the only thing that knows
  // which pipeline a stage belongs to.
  indexPipelines(st, m);
  if (blocks.length > st.rendered) {
    renderRange(st, m, st.rendered, blocks.length, streaming, marks);
  }
  // OUTSIDE that guard, deliberately. A steer read between two chunks of the
  // same block adds no block, so gating this on block growth would strand its
  // note until the next one arrived — which on a long text block is the whole
  // rest of the turn. The two calls coincide whenever a block DID arrive, and
  // `st.steerNotes` is what makes that harmless.
  flushSteerNotes(st, marks, m.id, blocks.length);
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

export function resetBlockRenders(): void {
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
  for (const fn of st.disposers.splice(0)) {
    fn();
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
): void {
  const blocks = m.blocks ?? [];
  const lastIdx = blocks.length - 1;
  if (to > from) {
    // A block is being appended, so whatever held the caret is no longer the
    // tail. Seal it here rather than in mountText: the new tail may be a
    // thinking block or a tool card, and the previous text block stops
    // streaming either way. Idempotent — `end()` nulls its own stream and
    // `classList.remove` is a no-op when the class is absent.
    sealLiveBubble(st);
  }
  for (let i = from; i < to; i++) {
    const block = blocks[i];
    if (block === undefined) {
      continue;
    }
    // BEFORE the block, so a note anchored at index i lands above it. This is
    // the whole of "chronologically at the point it was injected".
    flushSteerNotes(st, marks, m.id, i);
    // Only the trailing block of a live message streams; earlier blocks are
    // sealed (a new block started because the run kind / subtask changed).
    placeBlock(st, m, block, i, live && i === lastIdx);
  }
  // A note anchored at the CURRENT end has no block to sit above yet, and the
  // loop above can never reach it. Mounting it here is what puts it below
  // everything so far and above everything that arrives next.
  flushSteerNotes(st, marks, m.id, to);
  st.rendered = to;
}

/** Mount every not-yet-drawn steer note whose anchor this render has reached.
 *
 *  Idempotent by mark id, which is required rather than defensive: `renderRange`
 *  and `updateAssistantBody` both call it and their ranges overlap by design.
 *
 *  Closing the open reasoning trace AND the open tool group first is
 *  CORRECTNESS, not tidiness. A group is keyed by container and stays open until
 *  something closes it, so a steer landing mid tool-loop would otherwise be
 *  appended after the group's container while the later tool cards still went
 *  INTO that group — rendering them above the note that preceded them. */
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
    sealReasoning(st, st.blocksEl);
    closeToolGroup(st, st.blocksEl);
    st.blocksEl.appendChild(
      buildSteerNote({
        text: mark.text,
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
    });
    sa.root.dataset["subtask"] = subtask;
    st.subagents.set(subtask, sa);
    stageHostFor(st, subtask, live).appendChild(sa.root);
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

/** Where a stage's own box goes: its pipeline's body when the stage belongs to
 *  one, otherwise the top level. Creating the pipeline box here is what makes the
 *  two arrival orders equivalent — the orchestrate call and its first stage race
 *  on the wire, and after a refresh the orchestrate call is persisted while the
 *  stage's blocks are not. Whichever arrives first builds the box; the other
 *  finds it. Same contract as `runCardFor`. */
function stageHostFor(st: MsgRender, subtask: string, live: boolean): HTMLElement {
  const pipelineID = st.stagePipeline.get(subtask);
  if (pipelineID === undefined) {
    return st.blocksEl;
  }
  return pipelineBoxFor(st, pipelineID, live).body;
}

/** Get or build the PIPELINE box for one orchestrate tool call.
 *
 *  The `container` activity variant is the whole point: its stages carry the
 *  spinners, so this card keeps its identity glyph and shows activity dots only
 *  while it is collapsed. See fundamentals/subagent-block.ts SubagentActivity. */
function pipelineBoxFor(st: MsgRender, pipelineID: string, live: boolean): SubagentView {
  const existing = st.pipelines.get(pipelineID);
  if (existing !== undefined) {
    return existing;
  }
  const box = buildSubagentBlock("Pipeline", live ? "in_progress" : "completed", {
    activity: "container",
  });
  box.setIcon(ICON_TAB_RUN);
  box.root.dataset["pipeline"] = pipelineID;
  st.pipelines.set(pipelineID, box);
  st.blocksEl.appendChild(box.root);
  return box;
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
function runCardFor(st: MsgRender, workflowID: string, name: string): RunCardView {
  const existing = st.runs.get(workflowID);
  if (existing !== undefined) {
    return existing;
  }
  // The footer link re-opens the run's tab. Injected here rather than imported by
  // the card, so `fundamentals/` keeps pointing only downward — and lazily, because
  // `run-view.ts` reaches the whole run page and the transcript must not carry it.
  const card = buildRunCard(workflowID, name, (id, label) => {
    void import("./run-view.js")
      .then(({ openRunView }) => {
        openRunView(id, label);
      })
      .catch(() => {
        /* noop: the link degrades to its href on the next click */
      });
  });
  st.runs.set(workflowID, card);
  st.blocksEl.appendChild(card.root);
  const stop = effect(() => {
    // Two inputs on different clocks, which is why they arrive together rather than
    // through two calls: `inspect` says what the run's steps are doing, and the dock
    // says which of them is blocked on a person. The run's status cannot carry the
    // second — KAS blocks the asking step's turn and leaves the run `running` — and
    // both reads are signal-backed, so this one effect repaints on either.
    card.render(runState(workflowID), runPendingAsks(workflowID));
  });
  st.disposers.push(() => {
    stop();
    releaseRunClock(workflowID);
    // The store's only bound, and this is the one place that can apply it: three
    // surfaces read a run's cell and none of them is last on its own. A card
    // unmounting (chat switch, or the reconcile dropping its row) with no run tab
    // open IS last, and a later invalidate re-creates the cell, so forgetting early
    // costs one fetch rather than a wrong answer. A tab still open keeps it.
    if (!hasTab("run", workflowID)) {
      forgetRun(workflowID);
    }
  });
  // The first read the card ever gets. Every later one arrives through the
  // effect above, driven by the run SSE events.
  invalidateRun(workflowID);
  holdRunClock(workflowID, card);
  return card;
}

/** Adopt the launch tool call into its run's card: the recipe name from the
 *  call's input as a placeholder label, and a failed launch reported on the card
 *  rather than lost.
 *
 *  A launch that FAILED never created a run, so `GET /api/runs/{id}` has nothing
 *  and the card would sit at "starting" forever. The tool call is the only witness
 *  in that case, which is why its status is folded in here. */
function bindRunCard(st: MsgRender, workflowID: string, tc: ToolCall): void {
  const card = runCardFor(st, workflowID, recipeNameOf(tc));
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
// ---------------------------------------------------------------------------

const clockHolders = new Map<string, RunCardView>();
let clockTimer: ReturnType<typeof setInterval> | undefined;

function holdRunClock(workflowID: string, card: RunCardView): void {
  clockHolders.set(workflowID, card);
  clockTimer ??= setInterval(() => {
    for (const c of clockHolders.values()) {
      c.tick();
    }
  }, 1000);
}

function releaseRunClock(workflowID: string): void {
  clockHolders.delete(workflowID);
  if (clockHolders.size === 0 && clockTimer !== undefined) {
    clearInterval(clockTimer);
    clockTimer = undefined;
  }
}

function placeBlock(st: MsgRender, m: Message, block: Block, i: number, live: boolean): void {
  const container = containerFor(st, block, live);
  const subtask = block.agent_subtask_id ?? "";

  switch (block.type) {
    case "text": {
      sealReasoning(st, container);
      closeToolGroup(st, container);
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
      // A WORKFLOW LAUNCH becomes the run's card, not a tool row. The call sits
      // in the parent agent's own block stream (it has no subtask of its own), so
      // this branch is ahead of the subtask checks below rather than inside them.
      //
      // A card already built by a step whose frame arrived first is FOUND here
      // rather than replaced, which is what keeps the two orders equivalent.
      const runID = workflowInvocation(tc);
      if (subtask === "" && runID !== "") {
        sealReasoning(st, container);
        closeToolGroup(st, container);
        bindRunCard(st, runID, tc);
        return;
      }
      // A PIPELINE LAUNCH becomes the pipeline's box, not a tool row. Like the
      // workflow launch above it sits in the parent agent's own block stream with
      // no subtask of its own, so this branch is ahead of the subtask checks; and
      // like it, a box already built by a stage whose frame arrived first is FOUND
      // rather than replaced.
      if (subtask === "" && isPipelineInvocation(tc)) {
        sealReasoning(st, container);
        closeToolGroup(st, container);
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
        sealReasoning(st, container);
        closeToolGroup(st, container);
        mountTodo(st, m.id, container, tc);
        return;
      }
      sealReasoning(st, container);
      mountToolCard(st, container, tc);
      return;
    }
  }
}

// ---------------------------------------------------------------------------
// Block mounters
// ---------------------------------------------------------------------------

function mountText(
  st: MsgRender,
  msgId: string,
  container: HTMLElement,
  block: Block,
  i: number,
  live: boolean,
): void {
  const initial = block.text ?? "";
  const bubble = buildAssistantBubble(initial, live);
  st.bubbles.push(bubble);
  if (live) {
    st.liveBubble = bubble;
  }
  st.blockText.set(i, (full) => {
    bubble.setText(full);
  });
  // Top-level bubbles carry an avatar row; subagent-body bubbles don't (the
  // subagent header is the identity — matches the IDE's indented nesting).
  if (container === st.blocksEl) {
    const row = cbs.makeRow();
    row.appendChild(bubble.root);
    container.appendChild(row);
  } else {
    container.appendChild(bubble.root);
  }
  if (live && !st.detached) {
    const sig = ensureBlockTextSig(msgId, i, initial);
    const cleanup = effect(() => {
      bubble.setText(sig.value);
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
  st.openReasoning.set(container, view);
  container.appendChild(view.root);
  if (live && !st.detached) {
    const sig = ensureBlockThinkingSig(msgId, i, initial);
    const cleanup = effect(() => {
      view.setText(sig.value);
    });
    pushLifetimeEffect(st, msgId, cleanup);
  }
}

function mountToolCard(st: MsgRender, container: HTMLElement, tc: ToolCall): void {
  const group = toolGroupFor(st, container);
  const card = toolSpecFor(st.chatID).mount(tc);
  if (card instanceof HTMLElement) {
    card.setAttribute(RECONCILE_KEY, tc.id);
    // Cards live in the group's body region (the disclosure-collapsible
    // container), not on the group root beside the header.
    groupBody(group).appendChild(card);
    refreshGroupHeader(group);
  }
}

function mountTodo(st: MsgRender, msgId: string, container: HTMLElement, tc: ToolCall): void {
  const list = buildTodoList(parseTodoItems(tc));
  list.dataset["toolId"] = tc.id;
  container.appendChild(list);
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

/** Wire the PIPELINE invocation tool's status and label onto its box header, and
 *  the box's footer ledger onto every stage's members.
 *
 *  Deliberately not folded into `bindSubagent`: the label comes from the stage
 *  COUNT rather than a subagent name, the icon is fixed, and the ledger sums
 *  across stages instead of over one subtask's members. */
function bindPipeline(st: MsgRender, msgId: string, tc: ToolCall, live: boolean): void {
  const box = pipelineBoxFor(st, tc.id, live);
  box.setName(pipelineLabel(tc));
  box.setStatus(tc.status);
  box.setSummary(pipelineSummary(st, tc));
  const sig = ensureToolCallSig(st.chatID, tc.id, tc);
  let last = tc;
  const cleanup = effect(() => {
    const next = sig.value;
    if (next === last) {
      return;
    }
    if (next.status !== last.status) {
      box.setStatus(next.status);
    }
    const label = pipelineLabel(next);
    if (label !== pipelineLabel(last)) {
      box.setName(label);
    }
    box.setSummary(pipelineSummary(st, next));
    last = next;
  });
  pushLifetimeEffect(st, msgId, () => {
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
    // The ledger re-derives on every invocation update. The members settle
    // BEFORE the invocation does (the delegate finishes last), so the settle
    // tick sees their final diffs; earlier ticks keep the running numbers
    // honest at no extra subscription cost.
    sa.setSummary(subagentSummary(st, subtask, next));
    last = next;
  });
  pushLifetimeEffect(st, msgId, () => {
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
      const s = stats(lineDiff(d.old_text ?? "", d.new_text));
      const cur = changed[d.path] ?? { lines_added: 0, lines_removed: 0 };
      changed[d.path] = {
        lines_added: cur.lines_added + s.adds,
        lines_removed: cur.lines_removed + s.dels,
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

function toolGroupFor(st: MsgRender, container: HTMLElement): HTMLDivElement {
  let group = st.toolGroups.get(container);
  if (group === undefined) {
    group = buildToolGroupShell();
    container.appendChild(group);
    st.toolGroups.set(container, group);
  }
  return group;
}

function closeToolGroup(st: MsgRender, container: HTMLElement): void {
  st.toolGroups.delete(container);
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

/** The prefix the server stamps on a WORKFLOW STEP's subtask id
 *  (`internal/translate/wire.go` ACPWorkflowMeta.SubtaskID:
 *  `"wf:" + workflowId + ":" + nodePath`). */
const STEP_PREFIX = "wf:";

/** A step subtask id, split into the two containers it names. */
interface StepSubtask {
  workflowID: string;
  nodePath: string;
}

/** Parse `wf:<workflowId>:<a/b/c>`, or null for a subagent's uuid.
 *
 *  One `indexOf` rather than a `split`, because a node path may not contain a
 *  colon but nothing here should depend on that: taking the FIRST colon after the
 *  prefix makes the workflow id unambiguous and hands everything after it to the
 *  path, whatever it contains. A malformed id (no second colon, or an empty half)
 *  returns null and falls through to the subagent branch, which renders it as a
 *  delegate box rather than losing the block. */
function parseStepSubtask(subtask: string): StepSubtask | null {
  if (!subtask.startsWith(STEP_PREFIX)) {
    return null;
  }
  const rest = subtask.slice(STEP_PREFIX.length);
  const sep = rest.indexOf(":");
  if (sep <= 0 || sep === rest.length - 1) {
    return null;
  }
  return { workflowID: rest.slice(0, sep), nodePath: rest.slice(sep + 1) };
}

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

/** Learn which pipeline each stage subtask belongs to, from the message's tool
 *  calls alone.
 *
 *  This is the join, and it is done from the TOOL CALL ARRAY rather than from the
 *  frames precisely so it has no ordering dependency: a stage's text block carries
 *  only a bare subtask uuid, which names nothing, while the stage's invocation
 *  carries both that uuid and its pipeline's id. Scanning the whole array means a
 *  stage whose text arrived before its invocation is still placed correctly on the
 *  next pass, so the two arrival orders agree.
 *
 *  Idempotent and append-only: it runs on every render pass, and a stage keeps the
 *  pipeline it was first seen under. */
function indexPipelines(st: MsgRender, m: Message): void {
  for (const tc of m.tool_calls ?? []) {
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

/** The pipeline box's header label: its stage count, which is the one fact worth
 *  reading on a collapsed box. The count comes from the invocation's own declared
 *  `stages` when it has them and otherwise from the stages seen so far, so a
 *  pipeline still names itself honestly while it is being discovered. The `task`
 *  field is deliberately not used — it is a paragraph of prose, and this is a
 *  one-line header that ellipsizes. */
function pipelineLabel(tc: ToolCall): string {
  const n = declaredStageCount(tc);
  if (n === 1) {
    return "Pipeline · 1 stage";
  }
  if (n > 1) {
    return `Pipeline · ${String(n)} stages`;
  }
  return "Pipeline";
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
