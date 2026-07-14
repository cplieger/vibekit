// ---------------------------------------------------------------------------
// Specs board: a live, read-only view of the workspace's .kiro/specs/<name>
// directories. Each spec renders as a requirements→design→tasks card: the
// document trio (click to open in the editor) plus the task tree with
// per-task status badges (markdown checkbox state, execution status, and
// property-based-test result). Nested subtasks are indented + collapsible.
//
// Data source: GET /api/specs (server enumerates the specs dir + sources
// task trees from the KAS getTaskStatuses request; see internal/hub/spec.go).
// Live: the spec_task_changed SSE (from KAS taskStatusChanged, emitted while
// a spec execution runs) triggers a debounced refetch; a slow poll while the
// board is visible catches markdown-checkbox progress driven through a
// Spec-mode chat (which doesn't emit taskStatusChanged). Server-canonical:
// every update refetches; nothing is rendered optimistically.
//
// This board is read-only by design. The KAS spec invoke verbs
// (executeTask / runAllTasks / generateDocument / analyzeRequirements /
// createSpec) each drive a fire-and-forget agent turn with no acp turn-end
// signal, so they can't be hosted as a server-canonical vibekit chat turn
// without re-architecting the turn model (see internal/hub/spec.go). Spec
// work is driven through the existing Spec-mode chat; this board surfaces its
// progress.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { onSSE } from "./bus.js";
import { reconcile } from "./reconcile.js";
import { toggleSpecsView } from "./tabs.js";
import { openFile } from "./editor-openers.js";
import { pollAction, registerCleanup } from "./actions/index.js";
import { fetchSpecs } from "./actions/specs.js";
import type { Spec, SpecTaskNode } from "./types.js";

/** Slow refresh while the board is visible. Catches markdown-checkbox
 *  progress from a Spec-mode chat (which updates tasks.md but emits no
 *  taskStatusChanged). Live task-status deltas still arrive instantly via
 *  the spec_task_changed SSE. pauseWhenHidden keeps it quiet in background. */
const POLL_INTERVAL_MS = 8_000;

/** Debounce window for coalescing an SSE burst (a cancel flips many tasks
 *  at once) into one refetch. */
const SSE_REFETCH_DEBOUNCE_MS = 250;

// --- Status label maps (single source of truth for text + a11y) ---

const MARKDOWN_LABELS: Readonly<Record<string, string>> = {
  completed: "Done",
  in_progress: "In progress",
  queued: "Queued",
  not_started: "To do",
};

const EXEC_LABELS: Readonly<Record<string, string>> = {
  running: "Running",
  succeed: "Succeeded",
  failed: "Failed",
  queued: "Queued",
  aborted: "Aborted",
};

const PBT_LABELS: Readonly<Record<string, string>> = {
  passed: "PBT passed",
  failed: "PBT failed",
  not_run: "PBT not run",
  unexpected_pass: "PBT unexpected pass",
};

/** Recursively count leaf-inclusive completed vs total tasks. Exported for
 *  the board summary + unit tests. */
export function taskCounts(tasks: readonly SpecTaskNode[]): { done: number; total: number } {
  let done = 0;
  let total = 0;
  const walk = (nodes: readonly SpecTaskNode[]): void => {
    for (const n of nodes) {
      total += 1;
      if (n.markdown_status === "completed") {
        done += 1;
      }
      if (n.sub_tasks.length > 0) {
        walk(n.sub_tasks);
      }
    }
  };
  walk(tasks);
  return { done, total };
}

// --- Collapse state (preserved across re-renders, per spec) ---

const collapsed = new Map<string, Set<string>>();

function collapsedSet(specName: string): Set<string> {
  let s = collapsed.get(specName);
  if (s === undefined) {
    s = new Set();
    collapsed.set(specName, s);
  }
  return s;
}

// --- Rendering ---

/** Build one status badge span. */
function badge(cls: string, label: string): HTMLElement {
  return el("span", { className: `spec-badge ${cls}` }, label);
}

/** Render one task node (recursive). depth drives indentation via a CSS
 *  custom property. specName scopes the collapse state. Exported for tests. */
export function renderTaskNode(node: SpecTaskNode, depth: number, specName: string): HTMLElement {
  const hasKids = node.sub_tasks.length > 0;
  const isCollapsed = hasKids && collapsedSet(specName).has(node.task_id);

  const wrap = el("div", {
    className: `spec-node${isCollapsed ? " collapsed" : ""}`,
    "data-task-id": node.task_id,
  });

  const row = el("div", { className: "spec-node-row", style: `--depth:${String(depth)}` });

  if (hasKids) {
    const toggle = el("button", {
      type: "button",
      className: "spec-toggle",
      "aria-expanded": isCollapsed ? "false" : "true",
      "aria-label": isCollapsed ? "Expand" : "Collapse",
    });
    row.appendChild(toggle);
  } else {
    row.appendChild(el("span", { className: "spec-toggle-spacer" }));
  }

  const mdLabel = MARKDOWN_LABELS[node.markdown_status] ?? node.markdown_status;
  row.appendChild(
    el("span", {
      className: "spec-check",
      "data-status": node.markdown_status,
      title: mdLabel,
      role: "img",
      "aria-label": mdLabel,
    }),
  );

  row.appendChild(el("span", { className: "spec-task-text" }, node.task_id));

  if (node.is_optional) {
    row.appendChild(el("span", { className: "spec-badge spec-badge-optional" }, "Optional"));
  }
  if (node.execution_status !== undefined && node.execution_status !== "") {
    const label = EXEC_LABELS[node.execution_status] ?? node.execution_status;
    row.appendChild(badge(`spec-badge-exec exec-${node.execution_status}`, label));
  }
  if (node.pbt_result !== undefined) {
    const pbt = node.pbt_result;
    const label = PBT_LABELS[pbt.status] ?? `PBT ${pbt.status}`;
    row.appendChild(badge(`spec-badge-pbt pbt-${pbt.status}`, label));
  }

  wrap.appendChild(row);

  // PBT failing counterexample: an expandable detail below the row.
  const failingExample = node.pbt_result?.failing_example;
  if (failingExample !== undefined && failingExample !== "") {
    const det = el(
      "details",
      { className: "spec-pbt-detail", style: `--depth:${String(depth)}` },
      el("summary", {}, "Failing example"),
      el("pre", { className: "spec-pbt-example" }, failingExample),
    );
    wrap.appendChild(det);
  }

  if (hasKids) {
    const kids = el("div", { className: "spec-node-children" });
    for (const c of node.sub_tasks) {
      kids.appendChild(renderTaskNode(c, depth + 1, specName));
    }
    wrap.appendChild(kids);
  }

  return wrap;
}

/** One document-trio chip. Present docs are buttons that open the file;
 *  absent docs render as a muted "not created" chip. */
function docChip(label: string, present: boolean, path: string | undefined): HTMLElement {
  if (present && path !== undefined && path !== "") {
    return el("button", { type: "button", className: "spec-doc", "data-path": path }, label);
  }
  return el(
    "span",
    { className: "spec-doc spec-doc-missing", "data-tooltip": `${label} not created yet` },
    label,
  );
}

/** Fill a spec card's body in place (used by both mount and update so the
 *  card element stays stable and collapse state persists). Exported for tests. */
export function fillSpecCard(card: HTMLElement, spec: Spec): void {
  const counts = taskCounts(spec.tasks);

  const header = el(
    "div",
    { className: "spec-card-header" },
    el("h3", { className: "spec-name" }, spec.name),
    spec.updated_at !== undefined && spec.updated_at !== ""
      ? el(
          "span",
          { className: "spec-updated", title: "tasks.md last modified" },
          new Date(spec.updated_at).toLocaleString(),
        )
      : null,
  );

  const docs = el(
    "div",
    { className: "spec-docs" },
    docChip("Requirements", spec.has_requirements, spec.requirements_path),
    docChip("Design", spec.has_design, spec.design_path),
    docChip("Tasks", spec.has_tasks, spec.tasks_path),
  );

  const children: (HTMLElement | null)[] = [header, docs];

  if (counts.total > 0) {
    children.push(
      el(
        "div",
        { className: "spec-progress" },
        el(
          "span",
          { className: "spec-progress-count" },
          `${String(counts.done)} / ${String(counts.total)} done`,
        ),
      ),
    );
  }

  if (spec.error !== undefined && spec.error !== "") {
    children.push(el("div", { className: "spec-inline-error" }, "Task status unavailable."));
  }

  if (spec.tasks.length > 0) {
    const tree = el("div", { className: "spec-tree" });
    for (const n of spec.tasks) {
      tree.appendChild(renderTaskNode(n, 0, spec.name));
    }
    children.push(tree);
  } else if (spec.error === undefined || spec.error === "") {
    const hint = spec.has_tasks
      ? "tasks.md has no tasks yet."
      : "No tasks.md yet — create it from a Spec-mode chat.";
    children.push(el("div", { className: "spec-empty-tasks" }, hint));
  }

  card.replaceChildren(...children.filter((c): c is HTMLElement => c !== null));
}

/** Build a full spec card. Exported for tests. */
export function buildSpecCard(spec: Spec): HTMLElement {
  const card = el("div", { className: "spec-card", "data-spec": spec.name });
  fillSpecCard(card, spec);
  return card;
}

/** Stable content signature so an unchanged spec skips a rebuild (preserving
 *  collapse state + avoiding needless re-animation on each poll tick). */
function specHash(spec: Spec): string {
  return JSON.stringify(spec);
}

// --- Controller ---

class SpecsController {
  private stopPoll: (() => void) | null = null;
  private active = false;
  private refetchTimer: ReturnType<typeof setTimeout> | undefined;

  start(): void {
    this.active = true;
    this.renderLoadingIfEmpty();
    void this.load();
    this.stopPoll?.();
    this.stopPoll = pollAction(fetchSpecs, undefined, {
      interval: POLL_INTERVAL_MS,
      onSuccess: (res) => {
        if (this.active) {
          this.render(res.specs);
        }
      },
    });
  }

  teardown(): void {
    this.active = false;
    this.stopPoll?.();
    this.stopPoll = null;
    if (this.refetchTimer !== undefined) {
      clearTimeout(this.refetchTimer);
      this.refetchTimer = undefined;
    }
    fetchSpecs.cancel();
  }

  scheduleRefetch(): void {
    if (!this.active) {
      return;
    }
    if (this.refetchTimer !== undefined) {
      clearTimeout(this.refetchTimer);
    }
    this.refetchTimer = setTimeout(() => {
      this.refetchTimer = undefined;
      void this.load();
    }, SSE_REFETCH_DEBOUNCE_MS);
  }

  private async load(): Promise<void> {
    const res = await fetchSpecs.dispatch(undefined);
    if (!this.active) {
      return;
    }
    if (res === null) {
      this.renderError();
      return;
    }
    this.render(res.specs);
  }

  private renderLoadingIfEmpty(): void {
    const list = $.specsList;
    if (list.querySelector("[data-spec]") !== null) {
      return; // already have cards; keep them during refresh
    }
    list.replaceChildren(el("div", { className: "spec-loading" }, "Loading specs…"));
  }

  private renderError(): void {
    const list = $.specsList;
    if (list.querySelector("[data-spec]") !== null) {
      return; // keep last-good render on a transient failure
    }
    const retry = el("button", { type: "button", className: "btn-small" }, "Retry");
    retry.addEventListener("click", () => {
      void this.load();
    });
    // spec-empty-board: same card chrome as the empty state — the error
    // renders bare into #specs-list (no .list-container wrapper), so the
    // class supplies the border/radius frame itself.
    list.replaceChildren(
      el(
        "div",
        { className: "list-empty spec-empty-board" },
        el("p", {}, "Couldn't load specs. Check your connection and try again."),
        retry,
      ),
    );
  }

  private render(specs: Spec[]): void {
    const list = $.specsList;

    // Drop any non-keyed placeholder (loading / empty / error) before reconcile.
    for (const child of [...list.children]) {
      if ((child as HTMLElement).getAttribute("data-spec") === null) {
        child.remove();
      }
    }

    if (specs.length === 0) {
      collapsed.clear();
      list.replaceChildren(
        el(
          "div",
          { className: "list-empty spec-empty-board" },
          el("p", {}, "No specs yet."),
          el(
            "p",
            { className: "text-muted text-sm" },
            "Specs live in .kiro/specs/. Pick Spec mode in a chat to create one, then track its requirements, design, and tasks here.",
          ),
        ),
      );
      return;
    }

    reconcile(list, specs, {
      key: (s) => s.name,
      mount: (s) => {
        const card = buildSpecCard(s);
        card.dataset["hash"] = specHash(s);
        return card;
      },
      update: (elm, s) => {
        const h = specHash(s);
        if (elm.dataset["hash"] === h) {
          return; // unchanged — preserve collapse state + avoid re-animation
        }
        elm.dataset["hash"] = h;
        fillSpecCard(elm, s);
      },
      onRemove: (_elm, key) => {
        collapsed.delete(key);
      },
    });
  }
}

const ctrl = new SpecsController();
registerCleanup(() => {
  ctrl.teardown();
});

/** Toggle the Specs board tab (sidebar button + route entry point). */
export function showSpecsView(): void {
  toggleSpecsView(
    () => {
      ctrl.start();
    },
    () => {
      ctrl.teardown();
    },
  );
}

let wired = false;

/** Wire the toolbar button, the collapse/doc-open delegation, and the live
 *  SSE refetch. Idempotent. */
export function initSpecs(): void {
  if (wired) {
    return;
  }
  wired = true;

  $.specsBtn.addEventListener("click", () => {
    showSpecsView();
  });

  // One delegated click handler for the whole board: open a document in the
  // editor, or toggle a subtask group's collapse.
  $.specsList.addEventListener("click", (e) => {
    const target = e.target as HTMLElement;
    const doc = target.closest<HTMLElement>(".spec-doc[data-path]");
    if (doc !== null) {
      const path = doc.getAttribute("data-path");
      if (path !== null && path !== "") {
        openFile(path);
      }
      return;
    }
    const toggle = target.closest<HTMLElement>(".spec-toggle");
    if (toggle !== null) {
      const node = toggle.closest<HTMLElement>(".spec-node");
      const card = toggle.closest<HTMLElement>("[data-spec]");
      if (node === null || card === null) {
        return;
      }
      const taskId = node.getAttribute("data-task-id") ?? "";
      const specName = card.getAttribute("data-spec") ?? "";
      const nowCollapsed = node.classList.toggle("collapsed");
      toggle.setAttribute("aria-expanded", nowCollapsed ? "false" : "true");
      toggle.setAttribute("aria-label", nowCollapsed ? "Expand" : "Collapse");
      const set = collapsedSet(specName);
      if (nowCollapsed) {
        set.add(taskId);
      } else {
        set.delete(taskId);
      }
    }
  });

  // Live updates: KAS emits spec_task_changed while a spec execution runs.
  // Refetch (debounced) so the board reflects the new status. Server-
  // canonical — we refetch rather than patch from the delta.
  onSSE("spec_task_changed", () => {
    ctrl.scheduleRefetch();
  });
}

/** Reset module state (wiring guard + collapse state + controller) for test
 *  isolation. Production never calls this. */
export function _resetForTest(): void {
  ctrl.teardown();
  collapsed.clear();
  wired = false;
}
