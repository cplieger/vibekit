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
// Contiguous blocks sharing a non-empty agent_subtask_id are grouped into one
// SubagentBlock; the subagent's tool cards / reasoning / text render inside it
// exactly as they do at the top level. There is no separate legacy path and no
// text-preview fold.
// ---------------------------------------------------------------------------

import type { Message, Block, ToolCall, PlanStatus } from "./types.js";
import { effect, el } from "@cplieger/reactive";
import { KEY_ATTR as RECONCILE_KEY } from "./reconcile.js";
import {
  ensureBlockTextSig,
  ensureBlockThinkingSig,
  ensureToolCallSig,
  clearToolCallSig,
} from "./store-signals.js";
import { buildAssistantBubble, type AssistantBubble } from "./fundamentals/text-bubble.js";
import { buildReasoning, type ReasoningView } from "./fundamentals/reasoning.js";
import { buildSubagentBlock, type SubagentView } from "./fundamentals/subagent-block.js";
import { buildTodoList, updateTodoList, type TodoItem } from "./fundamentals/todo.js";
import {
  buildTurnFooter,
  updateTurnFooter,
  hasTurnSummary,
  type TurnSummaryData,
} from "./fundamentals/turn-footer.js";
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
  makeRow(side: "assistant"): HTMLDivElement;
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
    bubbles: [],
    reasonings: [],
    openReasoning: new Map(),
    toolGroups: new Map(),
  };
  renders.set(m.id, st);
  const blocks = m.blocks ?? [];
  renderRange(st, m, 0, blocks.length, live);
  mountPlan(wrap, m);
  mountFooter(wrap, m);
}

/** Incrementally sync the assistant body: mount newly-arrived blocks, update
 *  the plan and footer. Per-block/per-tool signals feed deltas into blocks
 *  already mounted, so this only handles structural growth. */
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
  mountFooter(wrap, m);
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
          bindSubagent(m.id, sa, tc);
        }
        return;
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
    const row = cbs.makeRow("assistant");
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

/** Wire the subagent invocation tool's status/name/icon onto its block header. */
function bindSubagent(msgId: string, sa: SubagentView, tc: ToolCall): void {
  sa.setName(subagentLabel(tc));
  sa.setIcon(iconForSubagent(subagentName(tc)));
  sa.setStatus(tc.status);
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
    last = next;
  });
  cbs.pushStreamingEffect(msgId, () => {
    cleanup();
    clearToolCallSig(tc.id);
  });
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
// Plan + turn footer (siblings after the block region)
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

function mountFooter(wrap: HTMLElement, m: Message): void {
  // Build with conditional assignment (exactOptionalPropertyTypes forbids
  // setting an optional field to undefined).
  const data: TurnSummaryData = {};
  if (m.turn_credits !== undefined) {
    data.credits = m.turn_credits;
  }
  if (m.turn_elapsed_ms !== undefined) {
    data.elapsedMs = m.turn_elapsed_ms;
  }
  if (m.changed_files !== undefined) {
    data.changedFiles = m.changed_files;
  }
  const existing = wrap.querySelector<HTMLDivElement>(":scope > .turn-footer");
  if (!hasTurnSummary(data)) {
    existing?.remove();
    return;
  }
  if (existing === null) {
    wrap.appendChild(buildTurnFooter(data));
  } else {
    updateTurnFooter(existing, data);
  }
}

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
