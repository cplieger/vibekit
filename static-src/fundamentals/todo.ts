// ---------------------------------------------------------------------------
// Fundamental: TodoList — a live checklist for kiro-cli's `todo_list` tool.
//
// kiro-cli's enableTodoList surfaces multi-step task tracking as a `todo_list`
// tool call whose input carries the items. Rather than show a raw generic tool
// card, we render a first-class checklist (like the IDE's todo panel and the
// TUI's checklist): a progress header + one row per item with a status glyph,
// reconciled by content so ticking an item off animates in place.
//
// Pure view over a NORMALIZED TodoItem[]; the tolerant parse from tool input
// lives in the composition (todo item shapes vary).
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { reconcile, type ReconcileSpec } from "../reconcile.js";
import type { PlanStatus } from "../types.js";

/** A normalized todo item. Status reuses PlanStatus (pending/in_progress/completed). */
export interface TodoItem {
  content: string;
  status: PlanStatus;
}

const GLYPH: Readonly<Record<PlanStatus, string>> = {
  pending: "\u2610", // ☐
  in_progress: "\u25D0", // ◐
  completed: "\u2611", // ☑
};

const rowSpec: ReconcileSpec<TodoItem> = {
  key: (t) => t.content,
  mount: (t) => buildRow(t),
  update: (row, t) => {
    row.dataset["status"] = t.status;
    const glyph = row.querySelector(".todo-glyph");
    if (glyph !== null) {
      glyph.textContent = GLYPH[t.status];
    }
  },
};

function buildRow(t: TodoItem): HTMLDivElement {
  const row = el("div", { className: "todo-row" }) as HTMLDivElement;
  row.dataset["status"] = t.status;
  row.append(
    el("span", { className: "todo-glyph", "aria-hidden": "true" }, GLYPH[t.status]),
    el("span", { className: "todo-text" }, t.content),
  );
  return row;
}

/** Build the todo checklist. */
export function buildTodoList(items: readonly TodoItem[]): HTMLDivElement {
  const root = el("div", { className: "todo-list" }) as HTMLDivElement;
  const header = el(
    "div",
    { className: "todo-header" },
    el("span", { className: "todo-title" }, "To-dos"),
    el("span", { className: "todo-progress" }),
  );
  const rows = el("div", { className: "todo-rows" });
  root.append(header, rows);
  updateTodoList(root, items);
  return root;
}

/** Reconcile the checklist rows + progress count against the latest items. */
export function updateTodoList(root: HTMLElement, items: readonly TodoItem[]): void {
  const rows = root.querySelector<HTMLElement>(".todo-rows");
  if (rows === null) {
    return;
  }
  reconcile(rows, items, rowSpec);
  const done = items.filter((t) => t.status === "completed").length;
  const progress = root.querySelector(".todo-progress");
  if (progress !== null) {
    progress.textContent = items.length > 0 ? `${String(done)}/${String(items.length)}` : "";
  }
}
