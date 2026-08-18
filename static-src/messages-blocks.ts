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

// ---------------------------------------------------------------------------
// Per-message render state
// ---------------------------------------------------------------------------

interface MsgRender {
  /** The `.assistant-blocks` container holding all top-level + subagent blocks. */
  blocksEl: HTMLElement;
  /** Count of blocks already mounted (index into m.blocks). */
  rendered: number;
  /** subtask id → its SubagentBlock view. */
  subagents: Map<string, SubagentView>;
  /** subtask id → the tool-call ids routed into that box, for the footer's
   *  ledger (commands, reads, changed files). The INVOCATION call is not a
   *  member — it is the box itself. */
  subagentMembers: Map<string, Set<string>>;
  /** Every mounted bubble handle (for finalize end()). */
  bubbles: AssistantBubble[];
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
    subagents: new Map(),
    subagentMembers: new Map(),
    bubbles: [],
    reasonings: [],
    openReasoning: new Map(),
    toolGroups: new Map(),
  };
  renders.set(m.id, st);
  const blocks = m.blocks ?? [];
  renderRange(st, m, 0, blocks.length, live);
  mountPlan(wrap, m);
}

/** Incrementally sync the assistant body: mount newly-arrived blocks and
 *  update the plan. Per-block/per-tool signals feed deltas into blocks already
 *  mounted, so this only handles structural growth. */
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
  mountPlan(wrap, m);
}

/** Finalize: flush every markdown stream + seal every reasoning trace. */
export function finalizeAssistantBody(msgId: string): void {
  const st = renders.get(msgId);
  if (st === undefined) {
    return;
  }
  for (const b of st.bubbles) {
    b.end();
  }
  for (const r of st.reasonings) {
    r.seal();
  }
}

/** Drop a message's render state (reconcile.onRemove / chat switch). */
export function disposeAssistantBody(msgId: string): void {
  renders.delete(msgId);
}

export function resetBlockRenders(): void {
  renders.clear();
}

// ---------------------------------------------------------------------------
// Block dispatch
// ---------------------------------------------------------------------------

function renderRange(st: MsgRender, m: Message, from: number, to: number, live: boolean): void {
  const blocks = m.blocks ?? [];
  const lastIdx = blocks.length - 1;
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

/** Resolve the container a block renders into: the top-level `.assistant-blocks`
 *  for parent-agent blocks, or a SubagentBlock's body for a subagent's blocks. */
function containerFor(st: MsgRender, block: Block, live: boolean): HTMLElement {
  const subtask = block.agent_subtask_id ?? "";
  if (subtask === "") {
    return st.blocksEl;
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
    let lastLen = initial.length;
    const cleanup = effect(() => {
      const full = sig.value;
      if (full.length <= lastLen) {
        return;
      }
      bubble.append(full.slice(lastLen));
      lastLen = full.length;
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
