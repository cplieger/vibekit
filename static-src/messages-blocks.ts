// Assistant body composition: the one dispatcher turning a message's `blocks`
// array into DOM, composed from the fundamentals/ primitives.

import type {
  Message,
  Block,
  ToolCall,
  ToolStatus,
  PlanStatus,
  FileChange,
  SteerMark,
} from "./types.js";
import type { TurnOutcome } from "./turns.js";
import type { BlockRange } from "./block-window.js";
import { effect, el } from "@cplieger/reactive";
import { KEY_ATTR as RECONCILE_KEY } from "./reconcile.js";
import { getActiveId, isTruncatedSnapshot, setMountedBlockProbe } from "./store.js";
import {
  blockKey,
  ensureBlockTextSig,
  ensureBlockThinkingSig,
  ensureToolCallSig,
  peekToolCallSig,
  clearToolCallSig,
  clearBlockSig,
} from "./store-signals.js";
import { recordBlockHeight } from "./block-heights.js";
import { lineDelta } from "./diff.js";
import { isInternalToolTitle, isSubagentInvocation, isToolActive } from "./tool-schema.js";
import type { TurnSummaryData } from "./fundamentals/turn-footer.js";
import {
  buildAssistantBubble,
  type AssistantBubble,
  type AssistantBubbleOpts,
} from "./fundamentals/text-bubble.js";
import { buildReasoning, type ReasoningView } from "./fundamentals/reasoning.js";
import {
  buildSubagentCard,
  buildSubagentContainer,
  type SubagentCard,
  type SubagentContainer,
  type SubagentOpener,
} from "./fundamentals/subagent-block.js";
import { bindSubagentTail } from "./subagent-tail.js";
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

// Re-exported for messages.ts to inject into messages-tools' status-flip path.
export { refreshGroupHeader };
import { iconForSubagent, subagentLabel, subagentName } from "./roles.js";
import { parseStepSubtask } from "./step-subtask.js";
import { buildRunCard, type RunCardView, type RunDisclosure } from "./fundamentals/run-card.js";
import { invalidateRun, runState, forgetRun } from "./run-store.js";
import { runPendingAsks } from "./decision-dock.js";
import { hasTab } from "./tabs.js";
import { buildPath } from "./router.js";

// Callbacks injected by messages.ts, which owns avatar markup and the
// streaming-effect registry.

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
  /** Put an undelivered steer's text back in the message box. Injected, not
   *  imported: the composer is above this module in the dependency order. */
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

// The messages a later message of their own TURN renders content after: the one fact a
// per-message render cannot derive, so the paint hands it over. Installed once per full
// pass beside the fold plan (`block-window.ts` `supersededMessages` derives it), and read
// by `indexGroups` alone.
//
// A per-pass INPUT rather than a callback, for the reason `st.boxExpanded` is one: it is
// data the projection already computed, and a `has` is what the derivation wants. Stale
// only where turn membership moved without a full pass, which cannot happen — a message
// arriving or leaving is a `shape` or `load` cause, and both take the full path. A miss
// therefore reads as NOT superseded, which is the pre-turn-scope behaviour: expanded.

let supersededMsgs: ReadonlySet<string> = new Set();

export function setSupersededMessages(ids: ReadonlySet<string>): void {
  supersededMsgs = ids;
}

// Which collapsible containers are open, so a re-mount restores what the reader chose.
// A DELEGATE has no key, because its card is not a disclosure. Detached renders register
// nothing: a disclosure the reader set is a property of the transcript.

const openContainers = new Map<string, boolean>();

function setContainerOpen(key: string, open: boolean): void {
  openContainers.set(key, open);
}

/** What the READER last left `key` at, or undefined when they have not decided —
 *  which is NOT the same as closed. The creation site owns the default, and under the
 *  newest-element policy that default is `st.boxExpanded`'s verdict.
 *
 *  Only a reader's own toggle writes here: every view gates its registry write on the
 *  disclosure's `source === "user"`, so an auto collapse and a failure auto-open leave
 *  no entry. An entry therefore IS the persisted latch — a box the reader has decided
 *  about is re-created with its auto path off for life. */
function containerOpen(key: string): boolean | undefined {
  return openContainers.get(key);
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

/** Drop a render's container keys. */
function pruneContainers(st: MsgRender): void {
  if (st.detached) {
    return; // never registered
  }
  for (const tc of st.tools) {
    openContainers.delete(`tool:${tc.id}`);
  }
  for (const pipelineID of st.pipelines.keys()) {
    openContainers.delete(`pipe:${pipelineID}`);
  }
  for (const runID of st.runs.keys()) {
    openContainers.delete(`run:${runID}`);
  }
}

// Per-message render state

interface MsgRender {
  /** The chat whose messages this render belongs to. Carried, not read from the
   *  store: the subagent page renders one delegate of whatever chat its tab names,
   *  so the active chat would key its cards under a chat that never writes them. */
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
  /** subtask id → its delegate card. Minted by the delegate's INVOCATION block,
   *  which is the only block of a delegate's this render draws. */
  subagents: Map<string, SubagentCard>;
  /** orchestrate tool-call id → the PIPELINE container that call opened. Keyed by the
   *  invocation because the orchestrate call has no subtask of its own. */
  pipelines: Map<string, SubagentContainer>;
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
  /** Cleanups that outlive the TURN, bucketed by block index with `-1` for the message's own:
   *  a window drop drains only the buckets it removed, `disposeAll` drains them all. Separate
   *  from `pushStreamingEffect`, disposed at turn end — right for a caret, wrong for a run card
   *  whose run carries on for minutes after `run_workflow` returns. */
  disposers: Map<number, (() => void)[]>;
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
   *  from `openReasoning`, which answers which one is still open. */
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
  /** Container key (same keys as `containerAt`) → whether that box renders EXPANDED
   *  under the newest-element policy. Resolved once per pass by `indexGroups` through
   *  `rendersExpanded`, and overwritten wholesale there, so a box the walk no longer
   *  seats leaves no stale verdict behind. On `st` rather than on `GroupIndex` because
   *  the two LAZY creation sites (`pipelineBoxFor`, `runCardFor`) are reached without
   *  `idx` in hand, and one home beats two. */
  boxExpanded: Map<string, boolean>;
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
  /** Container key (`sub:`/`pipe:`, the `containerAt` spelling) → the disposer that
   *  releases that box's binding. Membership is also the already-bound guard.
   *
   *  Keyed by the BOX rather than by the invocation call, because a box outlives its
   *  invocation BLOCK by design (`isContainerRoot`) and the block's own disposer bucket
   *  is therefore the wrong lifetime — so whoever releases the box releases the binding,
   *  and an entry can never name a box that is gone. */
  boxBindings: Map<string, () => void>;
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

/** Detached render id → the STORE key of each of its own block indices. A detached
 *  render's blocks are a re-indexed SLICE, so nothing else can turn a store
 *  coordinate into an index that render answers to. Dropped with the render. */
const detachedSources = new Map<string, Map<string, number>>();

// A store delta repaints only where a sink for that block is MOUNTED, and one store
// block can be mounted in several surfaces at once — the transcript keys its sinks by
// the store's message id, a delegate page by a derived one. So this is a union.
setMountedBlockProbe((messageID, blockIndex) => {
  if (renders.get(messageID)?.blockText.has(blockIndex) === true) {
    return true;
  }
  return detachedHolds(blockKey(messageID, blockIndex));
});

function detachedHolds(storeKey: string): boolean {
  for (const [id, sources] of detachedSources) {
    const own = sources.get(storeKey);
    if (own !== undefined && renders.get(id)?.blockText.has(own) === true) {
      return true;
    }
  }
  return false;
}

// ---------------------------------------------------------------------------
// Public API (called by messages.ts)

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
  syncTruncationNote(wrap, chatID, m.id);
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
    detached,
    bubbles: [],
    liveBubble: null,
    topLiveEl: null,
    reasonings: [],
    openReasoning: new Map(),
    toolGroups: new Map(),
    containerAt: new Map(),
    boxExpanded: new Map(),
    steerNotes: new Map(),
    insertBefore: null,
    msgID: m.id,
    tools: m.tool_calls ?? [],
    blocks: m.blocks ?? [],
    boxBindings: new Map(),
  };
  renders.set(m.id, st);
  indexPipelines(st, m);
  // ONE index per pass: the mount and the collapse sync ask it different
  // questions about the same run boundaries.
  const idx = indexGroups(st, m, marks, live);
  renderRange(st, m, want.from, want.to, live, marks, idx);
  syncContainerCollapse(st, idx);
}

/** Where a mount's turn-lifetime cleanup goes. A transcript render hands it to
 *  messages.ts, which disposes at TURN END as well as on unmount; a DETACHED render
 *  keeps its own, or a cleanup clearing a shared signal would reach into the
 *  transcript's live cards. */
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
  // The INDEX leads, because `rehomeStages` reaches `pipelineBoxFor` through
  // `stageHostFor`, so a box can be CREATED before this pass's index exists — and a
  // creation site reading a stale `st.boxExpanded` is what would force the collapse
  // sync to also open things. Value-neutral for the index itself: its only reads of
  // `st.pipelines` are `hostsPipelineBox` and `driverNeedsBox`, both disjunctions
  // with `pipelineHasContainer`, which is exactly the gate `stageHostFor` checks
  // before creating one, so the disjunction's value cannot change. `indexPipelines`
  // still runs first and is what both read.
  const idx = indexGroups(st, m, marks, streaming);
  // BEFORE the range, so an adopted box precedes whatever this pass mounts.
  rehomeStages(st, streaming);
  if (want.to > st.window.to) {
    renderRange(st, m, st.window.to, want.to, streaming, marks, idx);
  }
  // OUTSIDE that guard, deliberately. A steer read between two chunks of the
  // same block adds no block, so gating this on block growth would strand its
  // note until the next one arrived — which on a long text block is the whole
  // rest of the turn. The two calls coincide whenever a block DID arrive, and
  // `st.steerNotes` is what makes that harmless.
  flushSteerNotes(st, marks, m.id, st.window.from, st.window.to);
  syncContainerCollapse(st, idx);
  syncMountedText(st, m);
  syncTruncationNote(wrap, chatID, m.id);
}

/** The class the CSS slice keys on. */
const CLS_TRUNCATION_NOTE = "msg-truncated-note";

/** What the note says. The reader's question is "is this the whole reply", so the
 *  answer leads and the remedy follows; no byte counts, which are diagnostics a
 *  reader cannot act on. */
const TRUNCATION_NOTE_TEXT = "Earlier output is not shown. It arrives when the turn ends.";

/** Mount or drop the withheld-output note at the TOP of a truncated message's body. A
 *  STATIC note, never a show-more: the withheld bytes are not on the wire. Idempotent,
 *  because the marker is set on the connect frame and cleared at turn end and neither
 *  moment rebuilds the body, so both paths ask and read the answer fresh. */
function syncTruncationNote(wrap: HTMLElement, chatID: string, msgID: string): void {
  const existing = wrap.querySelector(`:scope > .${CLS_TRUNCATION_NOTE}`);
  if (!isTruncatedSnapshot(chatID, msgID)) {
    existing?.remove();
    return;
  }
  if (existing !== null) {
    return;
  }
  // FIRST child, so it reads as a preface to the body rather than as a footnote
  // to whatever the pass happened to mount last.
  wrap.prepend(el("div", { className: CLS_TRUNCATION_NOTE }, TRUNCATION_NOTE_TEXT));
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

/** Finalize: flush every markdown stream + settle every reasoning trace. */
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
    // settle(), not seal(): the turn ending is not another element being posted, and
    // the trigger for a fold is positional. Every trace something DID follow was
    // already sealed at its own mount or by `sealReasoning`, so what this leaves
    // expanded is exactly the trace that is still the newest element in its lane.
    r.settle();
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
  detachedSources.clear();
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
  sourceKeys: readonly string[],
): void {
  const id = detachedID(m.id, subtask);
  noteDetachedSources(id, sourceKeys);
  buildBody(host, { ...m, id }, chatID, live, true);
}

/** Append newly-arrived blocks and bring mounted ones up to the store's text. */
export function updateDetachedBody(
  host: HTMLElement,
  m: Message,
  chatID: string,
  subtask: string,
  live: boolean,
  sourceKeys: readonly string[],
): void {
  const id = detachedID(m.id, subtask);
  noteDetachedSources(id, sourceKeys);
  updateBody(host, { ...m, id }, chatID, live, true);
}

/** Record where this render's blocks came FROM, so the mounted-block probe can
 *  answer a store-space question about them. `sourceKeys` is index-aligned with
 *  `m.blocks` (subagent-slice.ts mints both in one walk). */
function noteDetachedSources(id: string, sourceKeys: readonly string[]): void {
  const sources = new Map<string, number>();
  for (const [own, key] of sourceKeys.entries()) {
    sources.set(key, own);
  }
  detachedSources.set(id, sources);
}

/** Flush every markdown stream and SETTLE every reasoning trace — `finalizeAssistantBody`
 *  for a detached render, so it folds nothing either: the trace nothing followed is still
 *  the newest element of its lane, and only a successor being posted seals one. */
export function finalizeDetachedBody(messageID: string, subtask: string): void {
  finalizeAssistantBody(detachedID(messageID, subtask));
}

/** Drop a detached render. The page's own unmount, and the only thing that fires
 *  its disposers — messages.ts never sees this id. */
export function disposeDetachedBody(messageID: string, subtask: string): void {
  const id = detachedID(messageID, subtask);
  detachedSources.delete(id);
  disposeAssistantBody(id);
}

/** Run and clear a render's message-lifetime cleanups. Idempotent: both dispose paths
 *  can reach one render, and a store subscription disposed twice must not throw. */
function disposeAll(st: MsgRender): void {
  pruneContainers(st);
  for (const key of [...st.disposers.keys()]) {
    runDisposers(st, key);
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
  idx: GroupIndex,
): void {
  const blocks = m.blocks ?? [];
  const lastIdx = blocks.length - 1;
  // Only a TAIL append moves the tail, and nothing re-establishes a caret sealed by
  // mistake. Nor does a range whose every block is DROPPED: nothing is placed, so
  // ending the caret would stop the reader's streaming reply for an arrival that
  // renders nothing. Idempotent.
  let places = false;
  for (let i = from; i < to; i++) {
    const block = blocks[i];
    if (block !== undefined && !isDroppedStep(block) && !isDroppedDelegateBlock(st, block)) {
      places = true;
      break;
    }
  }
  if (to > st.window.to && places) {
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
  syncContainerCollapse(st, idx);
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

/** Release everything the mounted indices OUTSIDE `keep` own, and leave no effect subscribed to
 *  a detached node. `openContainers` keys deliberately SURVIVE: a drop is a window move, not a
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
  const orphaned = new Map<string, RunCardView>();
  for (const i of removed) {
    const hosted = dropBlock(st, m, blocks[i], i);
    if (hosted !== undefined) {
      orphaned.set(hosted.runID, hosted.card);
    }
  }
  cbs.disposeBlockEffects(m.id, removed);
  for (const [id, note] of [...st.steerNotes]) {
    if (note.index < keep.from || note.index > keep.to) {
      note.el.remove();
      st.steerNotes.delete(id);
    }
  }
  pruneOrphanedCards(st, m, keep);
  pruneEmptyContainers(st);
  rebindSurvivingBoxes(st);
  st.window = keep;
  // After the LOOP: `st` is a candidate claimant, and mid-loop its `blockEls` still holds
  // ordinals this same drop is about to take.
  for (const [runID, card] of orphaned) {
    resolveRunCardFate(st, runID, card);
  }
  // The marks are re-flushed against the narrowed window, so a note whose anchor
  // is still inside it survives a drop that removed its neighbour.
  flushSteerNotes(st, marks, m.id, st.window.from, st.window.to);
}

/** Re-home or release `runID`'s card once the drop that took its launch block is
 *  complete: one card per run, hosted by the earliest render still holding mounted
 *  blocks inside it, which can be `st` itself. */
function resolveRunCardFate(st: MsgRender, runID: string, card: RunCardView): void {
  const claim = liveRunClaimant(st, card);
  if (claim === undefined) {
    // Nothing mounted inside it, so the run's own state goes back — or `runCardFor`
    // hands the next claimant a DETACHED node and re-homing never fires.
    st.runs.delete(runID);
    releaseRunCard(st, runID, card);
    card.root.remove();
    return;
  }
  // RE-HOMED: the card is a CONTAINER, and removing it takes a render's mounted blocks
  // out of the document while that render still counts them.
  const seat = seatAbove(claim.host, claim.host.blocksEl, claim.at, card.root);
  if (claim.host !== st) {
    adoptRunCard(claim.host, st, runID, card, seat);
    return;
  }
  // The claim is already this render's, so only the SEAT can be wrong: the launch
  // ordinal it was placed at is gone. Guarded because ANY re-seat blurs whatever the
  // card holds focus on, and a drop runs while the reader scrolls.
  if (card.root.nextElementSibling !== seat) {
    st.blocksEl.insertBefore(card.root, seat);
  }
}

/** Whether `el` sits in a subtree the page is not rendering: a folded card's body or a
 *  parked view. A `closest()` test, never a geometry read — reading a descendant's box
 *  there forces the browser to render what it skipped, which is the cost being avoided. */
export function geometrySkipped(el: Element): boolean {
  return (
    el.closest(".turn[data-folded] > .turn-body") !== null ||
    el.closest(".transcript-view:not(.is-active)") !== null
  );
}

/** Release one block: its measured height into the cache, its disclosure state, its element,
 *  its text sink, its streaming signals and its block-lifetime cleanups. Answers with the run
 *  card the block hosted, whose fate its caller decides once the whole range is gone. */
function dropBlock(
  st: MsgRender,
  m: Message,
  block: Block | undefined,
  i: number,
): { runID: string; card: RunCardView } | undefined {
  const hosted = hostedRun(st, block);
  const el = st.blockEls.get(i);
  st.blockEls.delete(i);
  if (el !== undefined && !isContainerRoot(st, el, hosted?.card)) {
    // MEASURED on the way out, so the spacer replacing it holds the height it held, and
    // only a REAL reading: a detached element answers 0 and a short spacer leaves the
    // document shorter than the content it stands in for. The estimate over-prices.
    // Skipped inside an unrendered subtree, where the read answers no height a spacer
    // could hold and forces the browser to render what it chose to skip.
    const px = geometrySkipped(el) ? 0 : el.offsetHeight;
    if (px > 0) {
      recordBlockHeight(m.id, i, px);
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
    forgetSubagentCard(st, el);
    el.remove();
  }
  st.blockText.delete(i);
  clearBlockSig(m.id, i);
  runDisposers(st, i);
  return hosted;
}

/** Drop the render state of the delegate card `el` IS, if it is one: an `st.subagents`
 *  entry pointing at a removed node would refuse to build the next card. */
function forgetSubagentCard(st: MsgRender, el: HTMLElement): void {
  const subtask = el.dataset["subtask"] ?? "";
  if (subtask !== "" && st.subagents.get(subtask)?.root === el) {
    st.subagents.delete(subtask);
  }
}

/** Whether `el` is a CONTAINER whose lifetime this render owns somewhere other than the
 *  block that stamped it. Released like an ordinary block it would be removed out from
 *  under blocks `st.window` still counts as mounted. A DELEGATE's card counts, and
 *  `pruneOrphanedCards` ends that one instead. */
function isContainerRoot(st: MsgRender, el: HTMLElement, card: RunCardView | undefined): boolean {
  if (el === card?.root) {
    return true;
  }
  for (const box of st.pipelines.values()) {
    if (box.root === el) {
      return true;
    }
  }
  for (const sa of st.subagents.values()) {
    if (sa.root === el) {
      return true;
    }
  }
  return false;
}

/** Remove every delegate card the surviving range no longer holds a block for. Takes
 *  `keep` rather than `st.window` so it can run BEFORE `pruneEmptyContainers`, which a
 *  pipeline losing its last stage has to look empty to; a card holds no blocks, so DOM
 *  emptiness cannot answer this. */
function pruneOrphanedCards(st: MsgRender, m: Message, keep: BlockRange): void {
  if (st.subagents.size === 0) {
    return;
  }
  const blocks = m.blocks ?? [];
  const live = new Set<string>();
  for (let i = keep.from; i < keep.to; i++) {
    const subtask = blocks[i]?.agent_subtask_id ?? "";
    if (subtask !== "") {
      live.add(subtask);
    }
  }
  for (const [subtask, sa] of st.subagents) {
    if (!live.has(subtask)) {
      releaseBox(st, `sub:${subtask}`);
      sa.root.remove();
      st.subagents.delete(subtask);
    }
  }
}

/** Release the binding `key`'s box holds, if it still holds the one it registered.
 *
 *  Every path that RELEASES a box calls this, because the binding's own block-lifetime
 *  disposer cannot: a box survives the drop of its invocation block, so that bucket is
 *  reached later than the box dies — or never, when the block sits outside the window.
 *  Without it `boxBindings` keeps an entry for a card that is gone, the effect keeps
 *  painting a detached node, and the next card built for that subtask is refused the
 *  binding it needs, so it renders its status once and never moves again. */
function releaseBox(st: MsgRender, key: string): void {
  st.boxBindings.get(key)?.();
}

/** The run card THIS render hosts for `block`'s workflow, for any `tool_use` block
 *  naming one. The pair `dropBlock` needs twice: to leave the card out of the generic
 *  release, and to decide its fate afterwards. */
function hostedRun(
  st: MsgRender,
  block: Block | undefined,
): { runID: string; card: RunCardView } | undefined {
  if (block?.type !== "tool_use") {
    return undefined;
  }
  const tc = st.tools.find((c) => c.id === block.tool_call_id);
  const runID = tc === undefined ? "" : workflowInvocation(tc);
  const card = runID === "" ? undefined : st.runs.get(runID);
  return card === undefined ? undefined : { runID, card };
}

/** The render holding mounted blocks INSIDE `card`, and the lowest such ordinal. `st` is a
 *  candidate like any other: a step frame folding into the still-open launching turn leaves ONE
 *  message holding both the launch and blocks inside the card. DOM order decides between several,
 *  because `renders` is keyed in BUILD order and a scroll up builds earlier messages last. */
function liveRunClaimant(
  st: MsgRender,
  card: RunCardView,
): { host: MsgRender; at: number } | undefined {
  let out: { host: MsgRender; at: number } | undefined;
  for (const other of renders.values()) {
    if (other.detached || other.chatID !== st.chatID) {
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

/** Re-subscribe every CONTAINER the drop left STANDING whose invocation block it took:
 *  that block's cleanup released the binding, no path re-binds an existing box, and the
 *  box would keep a frozen header and ledger until its invocation re-mounts.
 *
 *  Both kinds of survivor, and the delegate CARD is one — `isContainerRoot` keeps its
 *  element while `disposeBlockEffects` releases its binding, so a card that outlived the
 *  drop is exactly the shape that freezes. Its status follows its invocation CALL, which
 *  is message-level, not that call's block, whose residency the window decides. */
function rebindSurvivingBoxes(st: MsgRender): void {
  for (const pipelineID of st.pipelines.keys()) {
    const inv = st.tools.find((tc) => tc.id === pipelineID && isPipelineInvocation(tc));
    if (inv !== undefined && !st.boxBindings.has(`pipe:${pipelineID}`)) {
      bindPipeline(st, st.msgID, inv, false, invocationIndex(st, inv.id));
    }
  }
  for (const [subtask, sa] of st.subagents) {
    const inv = st.tools.find(
      (tc) => (tc.agent_subtask_id ?? "") === subtask && isSubagentInvocation(tc),
    );
    if (inv !== undefined && !st.boxBindings.has(`sub:${subtask}`)) {
      bindSubagent(st, subtask, st.msgID, sa, inv, invocationIndex(st, inv.id));
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
  for (const [pipelineID, box] of st.pipelines) {
    if (box.body.firstElementChild === null) {
      releaseBox(st, `pipe:${pipelineID}`);
      st.openReasoning.delete(box.body);
      box.root.remove();
      st.pipelines.delete(pipelineID);
    }
  }
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
  // finishNow, not end: the tail MOVED, so this block's residual reveal is no longer
  // live text, and "exactly one streaming caret" is the invariant this function
  // keeps. The turn's LAST block gets the graceful drain instead.
  st.liveBubble?.finishNow();
  st.liveBubble = null;
}

/** Get or build a delegate's CARD. ANY of its blocks seats it, because the resident window
 *  is a range over block ordinals and the invocation that NAMES the card can fall outside
 *  it — so a pass that never sees the invocation leaves the card at its fallback name
 *  rather than dropping the delegate. A stage's host pipeline is `st.stagePipeline`'s. */
function subagentCardFor(st: MsgRender, subtask: string, live: boolean): SubagentCard {
  const existing = st.subagents.get(subtask);
  if (existing !== undefined) {
    return existing;
  }
  // The invocation CALL is message-level, so it is here even when the invocation BLOCK is
  // outside this range. Read BEFORE the build because the card's tail is latched on its
  // starting status and never comes back: built settled, a delegate that is still working
  // would lose its tail for the rest of the render.
  const inv = st.tools.find(
    (tc) => (tc.agent_subtask_id ?? "") === subtask && isSubagentInvocation(tc),
  );
  const sa = buildSubagentCard(
    "Subagent",
    inv?.status ?? (live ? "in_progress" : "completed"),
    subagentOpenerFor(st, subtask),
  );
  sa.root.dataset["subtask"] = subtask;
  st.subagents.set(subtask, sa);
  // And the BINDING, for the reason `pipelineBoxFor` binds its own box: a card THIS path
  // seated has no subscription and no ledger until its invocation block mounts, which a
  // window need never reach — so its status would stay frozen at whatever it last
  // painted. `bindSubagent` paints first and returns early for a CARD already bound, so
  // `placeBlock`'s own bind is a no-op whichever of the two got here first.
  if (inv !== undefined) {
    bindSubagent(st, subtask, st.msgID, sa, inv, invocationIndex(st, inv.id));
  }
  // The card lands in its HOST (top level or a pipeline body), at the store index that
  // establishes it — the same index `indexGroups` prices its run break at.
  const host = stageHostFor(st, subtask, live);
  placeContainer(st, host, sa.root, st.containerAt.get(`sub:${subtask}`));
  return sa;
}

/** What the delegate card's HEAD opens, or nothing.
 *
 *  Injected rather than imported so `fundamentals/` keeps pointing downward, and lazy
 *  because `subagent-view.ts` reaches the whole page. A DETACHED render gets none: it
 *  IS the page. The chat id is the RENDER's, the one the blocks came from. */
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

/** Re-derive every mounted delegate card's host and move the ones that no longer sit in it.
 *  A host is chosen when the card is BUILT, from a stage count that is only a lower bound
 *  then — a lone stage is PROMOTED to the top level and its pipeline grows a container
 *  later — so without this the stage stays a sibling of its own pipeline. Idempotent:
 *  re-appending a card in place would re-fire its mount animation. */
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
function paintPipeline(st: MsgRender, box: SubagentContainer, driver: ToolCall): void {
  box.setName(pipelineLabel(st, driver.id));
  box.setStatus(driver.status);
  box.setSummary(pipelineSummary(st, driver));
}

/** Get or build the PIPELINE box for one orchestrate tool call.
 *
 *  It also ADOPTS any stage of its own sitting at the top level, the upgrade path
 *  after a lone stage was promoted. A RE-PARENT, never a rebuild: the move carries
 *  the disclosure, the observers and every effect with the node. */
function pipelineBoxFor(st: MsgRender, pipelineID: string, live: boolean): SubagentContainer {
  const existing = st.pipelines.get(pipelineID);
  if (existing !== undefined) {
    return existing;
  }
  const key = `pipe:${pipelineID}`;
  const box = buildSubagentContainer(
    pipelineLabel(st, pipelineID),
    live ? "in_progress" : "completed",
    st.detached
      ? {}
      : {
          // The reader's own state first, then THIS pass's newest-element verdict. The
          // absent-entry fallback is a floor rather than a path: `indexGroups` runs
          // ahead of every creation site in the pass, so a box with no seat is one
          // being established by content that has just arrived.
          startOpen: containerOpen(key) ?? st.boxExpanded.get(key) ?? true,
          userDecided: containerOpen(key) !== undefined,
          onOpenChange: (open: boolean): void => {
            setContainerOpen(key, open);
          },
        },
  );
  box.root.dataset["pipeline"] = pipelineID;
  st.pipelines.set(pipelineID, box);
  // Same-pass adoption; `rehomeStages` is the general case.
  const promoted = (st.pipelineStages.get(pipelineID) ?? [])
    .map((subtask) => st.subagents.get(subtask))
    .filter((v): v is SubagentCard => v?.root.parentElement === st.blocksEl);
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
  // effect re-runs only when its tool call CHANGES, and a settled driver never does.
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

/** The disclosure a transcript run card reads and writes: the reader's own recorded
 *  state, this pass's newest-element verdict, and where a reader's flip goes.
 *
 *  `st` is a parameter because the verdict lives on the render: `indexGroups` resolves
 *  it once per pass, and this is read at the card's creation site. */
function runDisclosure(st: MsgRender, workflowID: string): RunDisclosure {
  const key = `run:${workflowID}`;
  return {
    wasOpen: () => containerOpen(key),
    defaultOpen: st.boxExpanded.get(key) ?? true,
    onOpenChange: (open) => {
      setContainerOpen(key, open);
    },
  };
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
  // The parent is THIS render's chat; the run store's own record of it is fed by SSE
  // and answers nothing before the first frame.
  const chatID = st.chatID;
  const card = buildRunCard(
    workflowID,
    launch === undefined ? name : recipeNameOf(launch),
    (id, label, focusNode) => {
      void import("./run-view.js")
        .then(({ openRunView }) => {
          // The third argument is what makes a step row a DOOR: two args means "the
          // run", a row passes its own node path and means "the run, at this step".
          openRunView(id, label, chatID, focusNode ?? "");
        })
        .catch(() => {
          /* noop: the link degrades to its href on the next click */
        });
    },
    st.detached ? undefined : runDisclosure(st, workflowID),
  );
  st.runs.set(workflowID, card);
  placeContainer(st, st.blocksEl, card.root, st.containerAt.get(`run:${workflowID}`));
  pushDisposer(st, -1, () => {
    releaseRunCard(st, workflowID, card);
  });
  // The first read the card ever gets; every later one arrives through the effect.
  invalidateRun(workflowID);
  armRunCard(st, workflowID, card);
  if (launch !== undefined) {
    card.setLaunch(launch.status, launch.output);
  }
  return card;
}

/** Give up this render's claim on `workflowID`'s card: its effect, its clock hold, the host
 *  slot, and the store's cell. Idempotent, which is what lets the message lifetime and the
 *  block lifetime share one function. */
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

/** Move `workflowID`'s card out of `host` and into `st`, claim and all; `seat` is the node to
 *  place it before, absent meaning the mount position. Moving the NODE keeps the element, so no
 *  effect churns and no entry animation replays. */
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

/** Adopt the launch tool call into its run's card, and answer with the card's root: the recipe
 *  name from the call's input as a placeholder label, and a failed launch reported rather than
 *  lost. A launch that FAILED never created a run, so `GET /api/runs/{id}` has nothing and the
 *  card would sit at "starting" forever with the tool call its only witness. */
function bindRunCard(st: MsgRender, workflowID: string, tc: ToolCall): HTMLElement {
  const card = runCardFor(st, workflowID, recipeNameOf(tc), true);
  card.setLaunch(tc.status, tc.output);
  return card.root;
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

/** Whether this block IS a delegate's invocation: the `tool_use` block whose call
 *  dispatched it, and the one block of a delegate's the transcript draws — as its card. */
function isSubagentCardBlock(st: MsgRender, block: Block): boolean {
  if (block.type !== "tool_use" || (block.agent_subtask_id ?? "") === "") {
    return false;
  }
  const tc = st.tools.find((c) => c.id === block.tool_call_id);
  return tc !== undefined && isSubagentInvocation(tc);
}

/** Whether the TRANSCRIPT renders nothing for this DELEGATE's block: everything it
 *  produced except the invocation above. The `st.detached` gate is the subagent PAGE's own
 *  correctness — it renders these same blocks, so a drop applying there leaves it blank —
 *  and is deliberately the second lock on that door. */
function isDroppedDelegateBlock(st: MsgRender, block: Block): boolean {
  if (st.detached || (block.agent_subtask_id ?? "") === "") {
    return false;
  }
  return !isSubagentCardBlock(st, block);
}

function placeBlock(
  st: MsgRender,
  m: Message,
  block: Block,
  i: number,
  live: boolean,
  idx: GroupIndex,
): void {
  // DROPPED before anything is placed: a WORKFLOW STEP renders nowhere here and a
  // DELEGATE renders only its card, the content living in the tab that owns it.
  // Explicit rather than unrouted, or these fall through as loose top-level content.
  if (isDroppedStep(block)) {
    return;
  }
  if (isDroppedDelegateBlock(st, block)) {
    // SEATED from any of its blocks: the invocation that names the card can sit outside
    // the resident window, and a card standing only with it would take the delegate's
    // only route to its output when the window moved.
    subagentCardFor(st, block.agent_subtask_id ?? "", live);
    return;
  }
  const container = st.blocksEl;
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
        // Stamped like every other kind, so a search hit on the launch call
        // resolves to the card it opened rather than to the whole message.
        stampBlock(st, bindRunCard(st, runID, tc), m.id, i);
        return;
      }
      // A PIPELINE LAUNCH becomes the pipeline's box, not a tool row — same shape as
      // the workflow launch above, and ahead of the subtask checks for the same reason.
      if (subtask === "" && isPipelineInvocation(tc)) {
        bindPipeline(st, m.id, tc, live, i);
        return;
      }
      // The subagent invocation BECOMES the delegate's card rather than a tool row, and
      // is its only creator. Stamped ahead of the bind, which returns early for a card
      // already bound, so a search hit on the invocation resolves to the card.
      if (subtask !== "" && isSubagentInvocation(tc)) {
        const sa = subagentCardFor(st, subtask, live);
        stampBlock(st, sa.root, m.id, i);
        bindSubagent(st, subtask, m.id, sa, tc, i);
        return;
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

// Block mounters

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
  // Only a LIVE transcript bubble joins the anchor registry; its seal callback clears
  // this message's slot.
  const topLive = live && !st.detached;
  // Every bubble carries a row: the only container a bubble is mounted into is this
  // render's own `.assistant-blocks`. Created BEFORE the bubble so the initial blank
  // report lands on it.
  const row = cbs.makeRow();
  const opts: AssistantBubbleOpts = {
    onBlankChange: (blank): void => {
      row.classList.toggle("is-empty", blank);
    },
  };
  if (topLive) {
    opts.onSeal = (root): void => {
      if (st.topLiveEl === root) {
        st.topLiveEl = null;
      }
      clearLiveAnchor(root);
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
  // The ROW is the element whose removal drops the block.
  row.appendChild(bubble.root);
  stampBlock(st, row, msgId, i);
  appendBlock(st, container, row);
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
  const host = containerKeyOf(block);
  // Sealed from the STORE, not from what arrives next: a trace the store already has a
  // successor for is finished however this range reached it.
  const followed = containerFollowed(idx, host, i);
  // The disclosure is POSITIONAL and the pulse is not: a settled trace that is still
  // the newest element in its lane renders EXPANDED, while a live trace something has
  // already been posted after renders collapsed.
  const view = buildReasoning(initial, live, rendersExpanded(idx, host, i));
  st.reasonings.push(view);
  st.blockText.set(i, (full) => {
    view.setText(full);
  });
  // Append (sealing any open predecessor) BEFORE registering the new view, or
  // appendBlock would seal the trace being mounted.
  stampBlock(st, view.root, msgId, i);
  appendBlock(st, container, view.root);
  // `openReasoning` is a container's genuinely-live TRAILING trace, which is why head
  // insertion never reaches it.
  if (followed) {
    view.seal();
  } else {
    st.openReasoning.set(container, view);
  }
  if (live && !st.detached) {
    const sig = ensureBlockThinkingSig(msgId, i, initial);
    const cleanup = effect(() => {
      // No watermark: the reasoning view's setText appends only the tail past its own
      // rendered text, so full text is already self-healing.
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
  const key = `pipe:${tc.id}`;
  if (st.boxBindings.has(key)) {
    return;
  }
  // Reserved before the first paint, which reaches `pipelineBoxFor` — and that binds the
  // box it builds, so an unreserved key re-enters here and leaks the inner subscription.
  let dispose: (() => void) | null = null;
  const release = (): void => {
    if (st.boxBindings.get(key) !== release) {
      return;
    }
    st.boxBindings.delete(key);
    dispose?.();
    releaseToolSig(st, tc.id);
  };
  st.boxBindings.set(key, release);
  const paint = (next: ToolCall): void => {
    if (!driverNeedsBox(st, next)) {
      return;
    }
    const box = pipelineBoxFor(st, tc.id, live);
    // Stamped HERE rather than at the call site: the box does not exist for a
    // single-stage pipeline, and the upgrade that builds one replaces the node it lands
    // on, so the stamp has to follow whatever this paint's box currently is.
    stampBlock(st, box.root, msgId, blockIndex);
    paintPipeline(st, box, next);
  };
  paint(tc);
  const sig = ensureToolCallSig(st.chatID, tc.id, tc);
  let last = tc;
  dispose = effect(() => {
    const next = sig.value;
    if (next === last) {
      return;
    }
    paint(next);
    last = next;
  });
  pushLifetimeEffect(st, msgId, blockIndex, release);
}

/** The footer outcome a settled invocation earns. `aborted` is its OWN outcome rather
 *  than folding onto `completed`: a delegate the reader stopped produced no result, and
 *  the footer's tint and lead word are what say so. */
function delegateOutcome(status: ToolStatus): TurnOutcome {
  switch (status) {
    case "failed":
      return "failed";
    case "aborted":
      return "cancelled";
    default:
      return "completed";
  }
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
    out.outcome = delegateOutcome(invocation.status);
    const elapsed = invocation.duration_ms ?? 0;
    if (elapsed > 0) {
      out.elapsedMs = elapsed;
    }
  }
  return out;
}

/** Write the invocation call's identity, status and footer ledger onto a card. The whole of
 *  what a card shows, so a seat that has the call but not its block reads the same. */
function paintSubagent(st: MsgRender, subtask: string, sa: SubagentCard, tc: ToolCall): void {
  sa.setName(subagentLabel(tc));
  sa.setIcon(iconForSubagent(subagentName(tc)));
  sa.setStatus(tc.status);
  sa.setSummary(subagentSummary(st, subtask, tc));
}

/** Wire the subagent invocation tool's status/name/icon onto its card, its footer ledger
 *  onto the members' current state, and — while it works — its rolling tail onto the
 *  delegate's own blocks in the store. */
function bindSubagent(
  st: MsgRender,
  subtask: string,
  msgId: string,
  sa: SubagentCard,
  tc: ToolCall,
  blockIndex: number,
): void {
  const key = `sub:${subtask}`;
  if (st.boxBindings.has(key)) {
    return;
  }
  let stopTail: (() => void) | null = null;
  let dispose: (() => void) | null = null;
  const release = (): void => {
    if (st.boxBindings.get(key) !== release) {
      return;
    }
    st.boxBindings.delete(key);
    stopTail?.();
    stopTail = null;
    dispose?.();
    releaseToolSig(st, tc.id);
  };
  st.boxBindings.set(key, release);
  paintSubagent(st, subtask, sa, tc);
  // The tail exists only while the delegate does, so its subscription does too: a settled
  // card has no tail element, and the footer is its last word.
  stopTail =
    isToolActive(tc.status) && !st.detached
      ? bindSubagentTail(st.chatID, subtask, (lines) => {
          sa.setTail(lines);
        })
      : null;
  const sig = ensureToolCallSig(st.chatID, tc.id, tc);
  let last = tc;
  dispose = effect(() => {
    const next = sig.value;
    if (next === last) {
      return;
    }
    if (next.status !== last.status) {
      sa.setStatus(next.status);
      if (!isToolActive(next.status)) {
        stopTail?.();
        stopTail = null;
      }
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
  pushLifetimeEffect(st, msgId, blockIndex, release);
}

/** The facts a delegate's footer can state honestly; credits and the resolved model are
 *  absent, nothing on this wire carrying them per delegate. Members come out of the
 *  MESSAGE's tool calls rather than the per-tool signals, which a mounted card mints and
 *  which would report zeros; membership is EITHER side's stamp. The store mutates that
 *  array in place, so a member's late diff is here by the time the invocation settles. */
function subagentSummary(st: MsgRender, subtask: string, invocation: ToolCall): TurnSummaryData {
  let commands = 0;
  let reads = 0;
  const changed: Record<string, FileChange> = {};
  const viaBlock = new Set<string>();
  for (const b of st.blocks) {
    if (b.type === "tool_use" && (b.agent_subtask_id ?? "") === subtask) {
      viaBlock.add(b.tool_call_id ?? "");
    }
  }
  for (const tc of st.tools) {
    if (
      ((tc.agent_subtask_id ?? "") !== subtask && !viaBlock.has(tc.id)) ||
      tc.id === invocation.id
    ) {
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
    out.outcome = delegateOutcome(invocation.status);
    const elapsed = invocation.duration_ms ?? 0;
    if (elapsed > 0) {
      out.elapsedMs = elapsed;
    }
  }
  return out;
}

// Reasoning + tool-group per-container bookkeeping

function sealReasoning(st: MsgRender, container: HTMLElement): void {
  const view = st.openReasoning.get(container);
  if (view !== undefined) {
    view.seal();
    st.openReasoning.delete(container);
  }
}

/** Append into a block container, sealing the trace open there first.
 *
 *  The ONE door for "anything posted after an open trace ends it": the wire carries no
 *  thinking-ended signal, so the next element's arrival IS the end signal, and sealing
 *  at the append keeps the rule total — a mounter added later cannot reach the DOM
 *  without it. That IS the one policy, at trace scope: a successor being posted is what
 *  folds an element, so turn end folds nothing — `finalizeAssistantBody` only settles,
 *  leaving the trace nothing followed expanded, and every container's own collapse is
 *  derived from the store by `syncContainerCollapse` rather than from the turn ending. */
function appendBlock(st: MsgRender, container: HTMLElement, el: HTMLElement): void {
  if (st.insertBefore === null) {
    // Only a TAIL append supersedes an open trace: an INSERTED block is posted
    // after nothing, and the trace it would seal is BELOW it.
    sealReasoning(st, container);
  }
  placeInContainer(st, container, el);
}

/** Place `el` in `container`: before the insertion reference while a head extension is in
 *  flight, at the end otherwise. The reference is the container's first child when the extension
 *  first touches it, captured HERE because every creation path reaches a container through this
 *  function, and it does not move as the extension proceeds — which keeps the inserted ordinals
 *  ascending. */
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

/** Bring an ALREADY-MOUNTED node down to the ordinal being inserted, and step the reference
 *  past it. The reference is the boundary between inserted and pre-existing content, so only a
 *  node at or BELOW it moves: a card the insertion itself placed is above it, and moving that one
 *  carries it past every ordinal mounted since. A step card's launch is the reachable case. */
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

/** Place a lazily-created CONTAINER where the STORE puts it: above the first mounted ordinal
 *  after `at`, the index establishing it. Seating it by the RANGE instead would leave it under
 *  ordinals the store puts after it — one run split into two groups, and one window reached two
 *  ways two documents. `at` absent appends. This seats a box and does not own its position for
 *  life: `runCardFor` reseats a step-built card down to its launch ordinal, and
 *  `pipelineBoxFor`'s adoption arm replaces the element outright. */
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
  /** container key → ascending indices at which a new run may START: one past a block that
   *  closed the container, a steer note's own anchor, or one past the block establishing a nested
   *  container's box. Every position is the STORE's, so one block answers one run start under any
   *  range — which a group's key needs, because the group outlives the pass that built it. */
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
  // Each box's SEAT: the host it is posted into and the store index it is
  // established at. The one fact only this walk knows, and the only input
  // `rendersExpanded` needs.
  const boxes = new Map<string, { host: string; at: number }>();
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
    boxes.set(id, { host, at });
    lastPost.set(host, at);
    startAt(host, at + 1);
  };
  for (const [i, block] of blocks.entries()) {
    const key = containerKeyOf(block);
    if (isDroppedStep(block)) {
      // `placeBlock` renders a workflow step nowhere, so it opens no box and posts
      // nothing: pricing a break here would split a tool run at an ordinal the
      // reader has no element for. The LAUNCH call is what prices the run's card.
      continue;
    }
    if (key !== "") {
      // A DELEGATE posts exactly one thing into its host — its card, at the index of the
      // FIRST of its blocks, which is where `placeBlock` seats it. `openBox` is
      // idempotent, so every later block of the same delegate posts nothing, and none of
      // them can price a break either, for the same reason a step's cannot. Keyed on the
      // first block rather than on the invocation because a window can hold one without
      // the other, and then the index and the seat have to still agree.
      const pipelineID = st.stagePipeline.get(key);
      let host = "";
      if (pipelineID !== undefined && hostsPipelineBox(st, pipelineID)) {
        host = `pipe:${pipelineID}`;
        openBox(host, "", i);
      }
      openBox(`sub:${key}`, host, i);
      continue;
    }
    // `key` is "" from here down: every DELEGATED block continued above, so the switch
    // only ever sees a parent-lane block.
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
        const runID = workflowInvocation(tc);
        if (runID !== "") {
          // UNCONDITIONAL: the launch is the card's only creator, and `bindRunCard`
          // re-homes it into the render holding the LAUNCH, so this block always
          // mounts one here.
          openBox(`run:${runID}`, "", i);
        } else if (isPipelineInvocation(tc)) {
          // Priced at the DRIVER's block, where the box stands: `driverNeedsBox`
          // stops asking for one at a count of 1, and the box outlives that.
          if (hostsPipelineBox(st, tc.id) || driverNeedsBox(st, tc)) {
            openBox(`pipe:${tc.id}`, "", i);
          }
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
  // TURN SCOPE. A later message of this turn renders after ALL of this one, so every run
  // and every box in the top lane is superseded — priced as one post-and-break at
  // `blocks.length`, which no real index can equal, so it supersedes each of them and
  // keys none of them. The message's own lane only: a nested container's lane is not
  // continued by the next message either (`rendersExpanded`'s no-cascade rule already
  // refuses a nested box, and a nested group stays newest-in-its-own-lane, which is the
  // existing lane rule rather than an exception to it).
  //
  // A DETACHED render is exempt: the subagent page renders one delegate's blocks and
  // nothing of the turn follows them there.
  if (!st.detached && supersededMsgs.has(m.id)) {
    post("", blocks.length, true);
  }
  for (const list of starts.values()) {
    list.sort((a, b) => a - b);
  }
  const idx: GroupIndex = { starts, lastPost };
  // AFTER the steer-mark loop and the sort, so `lastPost` is final: a note posted
  // into the top level supersedes a box established above its anchor.
  const expanded = new Map<string, boolean>();
  for (const [id, seat] of boxes) {
    expanded.set(id, rendersExpanded(idx, seat.host, seat.at));
  }
  st.boxExpanded = expanded;
  return idx;
}

/** Whether an element established at store index `at` in container `host` renders
 *  EXPANDED under the newest-element policy. THE one gate. Its first clause is the
 *  whole no-cascade rule: `""` is the message's own top-level lane, so anything whose
 *  host is a box is refused HERE rather than trusted to a flag at its creation site.
 *
 *  SCOPE, stated because "only the outermost layer opens" is ambiguous without it:
 *  this gate's outermost is the MESSAGE's block lane, and no-cascade is WITHIN a lane.
 *  The turn card sits above that lane and runs the same rule at turn scope
 *  (`fold-state.ts` `isTurnOpen`, `OPEN_TAIL = 1`), so the newest turn is expanded AND
 *  the newest box inside it is expanded — two applications of one rule at two scopes,
 *  not a cascade. Reading the turn card as this gate's outermost layer instead would
 *  collapse every group, box and trace in the transcript and make the policy a no-op. */
function rendersExpanded(idx: GroupIndex, host: string, at: number): boolean {
  return host === "" && !containerFollowed(idx, host, at);
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

/** Apply the newest-element verdict to every collapsible container in this render.
 *
 *  COLLAPSE-ONLY, which is what makes it idempotent: the verdict is monotone (once
 *  something is posted after an element in the store it stays posted, and a rewind
 *  rebuilds the transcript), so "apply the verdict every pass" and "collapse when
 *  superseded" are the same function. A container mounted already-superseded therefore
 *  folds at its first sync rather than waiting for a successor the store already holds.
 *
 *  Three arms because three kinds are collapsible, and the group's question is asked at
 *  a different ARITY rather than by a different mechanism: a box occupies one store
 *  index, so `st.boxExpanded`'s per-seat verdict answers it, while a group spans a RUN
 *  of indices whose end only `starts` knows. Both read the one index this pass built.
 *
 *  An ABSENT seat reads as EXPANDED (`=== false`, never `!== true`), which is the same
 *  reading both creation sites take. The two must agree or a box with no seat would be
 *  born expanded and folded by the same pass's sync — the one shape a collapse-only
 *  design exists to make impossible. Unreachable today (every box's seat is priced
 *  before its creator can run), so this is agreement rather than a guard. */
function syncContainerCollapse(st: MsgRender, idx: GroupIndex): void {
  for (const [key, bucket] of st.toolGroups) {
    for (const [runStart, group] of bucket) {
      if (runFollowed(idx, key, runStart)) {
        autoCollapseGroup(group);
      }
    }
  }
  for (const [pipelineID, box] of st.pipelines) {
    box.setSuperseded(st.boxExpanded.get(`pipe:${pipelineID}`) === false);
  }
  for (const [workflowID, card] of st.runs) {
    card.setSuperseded(st.boxExpanded.get(`run:${workflowID}`) === false);
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
