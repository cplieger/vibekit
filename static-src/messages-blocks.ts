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
// ---------------------------------------------------------------------------

import type { Message, Block, ToolCall, PlanStatus, FileChange } from "./types.js";
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
import { buildSubagentBlock, type SubagentView } from "./fundamentals/subagent-block.js";
import { buildTodoList, updateTodoList, type TodoItem } from "./fundamentals/todo.js";
import { toolSpec } from "./messages-tools.js";
import { planElement, updatePlanElement } from "./messages-plan.js";
import { buildToolGroupShell, groupBody, refreshGroupHeader } from "./tool-group.js";

// Re-exported so messages.ts can inject it into messages-tools' status-flip
// path (initToolCallbacks) — the same header renderer the block dispatcher uses.
export { refreshGroupHeader };
import { humanName } from "./strings.js";
import { iconForSubagent } from "./roles.js";
import { buildRunCard, type RunCardView } from "./fundamentals/run-card.js";
import { invalidateRun, runState, forgetRun } from "./run-store.js";
import { hasTab } from "./tabs.js";

// ---------------------------------------------------------------------------
// Callbacks injected by messages.ts (kept there so avatar markup + the
// streaming-effect registry live in one place).
// ---------------------------------------------------------------------------

interface BlockCbs {
  /** Register a cleanup disposed on turn finalize / message unmount. */
  pushStreamingEffect(msgId: string, cleanup: () => void): void;
  /** Build an avatar row for a top-level assistant bubble. */
  makeRow(): HTMLDivElement;
}

let cbs: BlockCbs = {
  pushStreamingEffect: () => {
    /* until init */
  },
  makeRow: () => el("div") as HTMLDivElement,
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
}

const renders = new Map<string, MsgRender>();

// ---------------------------------------------------------------------------
// Public API (called by messages.ts)
// ---------------------------------------------------------------------------

/** Build the assistant body from scratch. Renders every block, then the plan. */
export function buildAssistantBody(wrap: HTMLElement, m: Message, live: boolean): void {
  const blocksEl = el("div", { className: "assistant-blocks" });
  wrap.appendChild(blocksEl);
  const st: MsgRender = {
    blocksEl,
    rendered: 0,
    blockText: new Map(),
    subagents: new Map(),
    runs: new Map(),
    disposers: [],
    subagentMembers: new Map(),
    bubbles: [],
    liveBubble: null,
    reasonings: [],
    openReasoning: new Map(),
    toolGroups: new Map(),
  };
  renders.set(m.id, st);
  const blocks = m.blocks ?? [];
  renderRange(st, m, 0, blocks.length, live);
  mountPlan(wrap, m);
}

/** Incrementally sync the assistant body: mount newly-arrived blocks, bring
 *  already-mounted ones up to the store's text, and update the plan. */
export function updateAssistantBody(wrap: HTMLElement, m: Message, streaming: boolean): void {
  const st = renders.get(m.id);
  if (st === undefined) {
    // Should not happen (build runs first), but stay self-healing.
    buildAssistantBody(wrap, m, streaming);
    return;
  }
  const blocks = m.blocks ?? [];
  if (blocks.length > st.rendered) {
    renderRange(st, m, st.rendered, blocks.length, streaming);
  }
  syncMountedText(st, m);
  mountPlan(wrap, m);
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

function renderRange(st: MsgRender, m: Message, from: number, to: number, live: boolean): void {
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
    // Only the trailing block of a live message streams; earlier blocks are
    // sealed (a new block started because the run kind / subtask changed).
    placeBlock(st, m, block, i, live && i === lastIdx);
  }
  st.rendered = to;
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
 *  step's work inside the invocation that launched it rather than beside it. */
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
    sa = buildSubagentBlock("Subagent", live ? "in_progress" : "completed");
    sa.root.dataset["subtask"] = subtask;
    st.subagents.set(subtask, sa);
    st.blocksEl.appendChild(sa.root);
  }
  return sa.body;
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
    card.render(runState(workflowID));
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
        mountTodo(m.id, container, tc);
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
  if (live) {
    const sig = ensureBlockTextSig(msgId, i, initial);
    const cleanup = effect(() => {
      bubble.setText(sig.value);
    });
    cbs.pushStreamingEffect(msgId, cleanup);
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
  if (live) {
    const sig = ensureBlockThinkingSig(msgId, i, initial);
    const cleanup = effect(() => {
      view.setText(sig.value);
    });
    cbs.pushStreamingEffect(msgId, cleanup);
  }
}

function mountToolCard(st: MsgRender, container: HTMLElement, tc: ToolCall): void {
  const group = toolGroupFor(st, container);
  const card = toolSpec.mount(tc);
  if (card instanceof HTMLElement) {
    card.setAttribute(RECONCILE_KEY, tc.id);
    // Cards live in the group's body region (the disclosure-collapsible
    // container), not on the group root beside the header.
    groupBody(group).appendChild(card);
    refreshGroupHeader(group);
  }
}

function mountTodo(msgId: string, container: HTMLElement, tc: ToolCall): void {
  const list = buildTodoList(parseTodoItems(tc));
  list.dataset["toolId"] = tc.id;
  container.appendChild(list);
  const sig = ensureToolCallSig(tc.id, tc);
  let last = tc;
  const cleanup = effect(() => {
    const next = sig.value;
    if (next === last) {
      return;
    }
    updateTodoList(list, parseTodoItems(next));
    last = next;
  });
  cbs.pushStreamingEffect(msgId, () => {
    cleanup();
    clearToolCallSig(tc.id);
  });
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
  const sig = ensureToolCallSig(tc.id, tc);
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
  cbs.pushStreamingEffect(msgId, () => {
    cleanup();
    clearToolCallSig(tc.id);
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
    const tc = peekToolCallSig(id);
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

/** The tool call that OPENS a subagent (vs. one of its nested tool calls).
 *  Matched by title only — the nested calls share the same agent_subtask_id
 *  but never carry these invocation titles. */
function isSubagentInvocation(tc: ToolCall): boolean {
  const t = tc.title;
  return (
    t === "invokeSubAgent" ||
    t === "invoke_sub_agent" ||
    t === "Orchestrate Sub-agent" ||
    t === "Sub-agent execution" ||
    t.startsWith("Sub-agent:")
  );
}

function subagentLabel(tc: ToolCall): string {
  const title = tc.title;
  if (title.startsWith("Sub-agent:")) {
    const name = title.slice("Sub-agent:".length).trim();
    if (name !== "") {
      return name;
    }
  }
  const nm = subagentName(tc);
  if (nm !== "") {
    return humanName(nm);
  }
  if (title !== "" && title !== "invokeSubAgent" && title !== "invoke_sub_agent") {
    return title;
  }
  return "Subagent";
}

/** The raw subagent id from the invocation tool's input (e.g. "introspect",
 *  "context-gatherer"), or "" when the input carries none. Keys the header
 *  icon (roles.ts iconForSubagent); subagentLabel humanizes the same value. */
function subagentName(tc: ToolCall): string {
  const input = tc.input;
  if (input !== undefined && input !== null && typeof input === "object") {
    const nm = (input as Record<string, unknown>)["name"];
    if (typeof nm === "string") {
      return nm;
    }
  }
  return "";
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
