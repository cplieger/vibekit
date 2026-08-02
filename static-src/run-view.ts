// ---------------------------------------------------------------------------
// Read-only review of one previous workflow run.
//
// Opened from the History list. A completed run has nothing to steer, so this
// view has no composer, no input bar and no cancel — it is the run's node tree
// and its captured outputs, and closing the tab closes nothing else. (The
// live-run tab, which CAN be cancelled and whose close kills the run, is a
// different surface.)
//
// The tree is rendered from KAS's own `state` shape, passed through verbatim by
// GET /api/workflow-runs/{id}. vibekit deliberately does not re-model it: the
// node plan is KAS's structure, and a second representation of it here would be
// one more thing to keep in sync for no gain.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { el } from "@cplieger/reactive";
import { openRunTab } from "./tabs.js";

/** One node of a run's state tree, as KAS reports it. Recursive: a sequence or
 *  repeat node carries children, a step node is a leaf. */
interface RunNode {
  nodeId: string;
  type: string;
  status: string;
  startedAt?: string;
  endedAt?: string;
  iteration?: number;
  agentName?: string;
  children?: RunNode[];
}

interface RunState {
  workflowId: string;
  workflowName?: string;
  status?: string;
  inputs?: Record<string, unknown>;
  artifacts?: Record<string, unknown>;
  capturedOutputs?: Record<string, string>;
  root?: RunNode;
}

interface RunInspect {
  workflowId: string;
  state?: RunState;
}

export function openRunView(workflowID: string, name: string): void {
  openRunTab(workflowID, name, () => {
    void load(workflowID);
  });
}

async function load(workflowID: string): Promise<void> {
  const container = document.getElementById("run-body");
  if (container === null) {
    return;
  }
  container.replaceChildren(el("div", { className: "list-empty" }, "Loading run…"));

  const d = await apiGet<RunInspect>(`/api/workflow-runs/${encodeURIComponent(workflowID)}`);
  if (d?.state === undefined) {
    container.replaceChildren(
      el("div", { className: "list-empty" }, "Couldn't load this run. It may have been deleted."),
    );
    return;
  }
  const state = d.state;
  const parts: HTMLElement[] = [];

  parts.push(
    el(
      "div",
      { className: "run-summary" },
      el("span", { className: "run-status" }, state.status ?? "unknown"),
      el("span", { className: "text-muted text-sm" }, state.workflowId),
    ),
  );

  if (state.root !== undefined) {
    parts.push(el("h3", { className: "section-title" }, "Steps"));
    parts.push(el("div", { className: "run-tree" }, renderNode(state.root, 0)));
  }

  // Captured outputs are the point of reviewing a finished run: they are what
  // the steps produced. Empty strings are dropped — every node gets a key
  // whether or not it captured anything.
  const outputs = Object.entries(state.capturedOutputs ?? {}).filter(([, v]) => v.trim() !== "");
  if (outputs.length > 0) {
    parts.push(el("h3", { className: "section-title" }, "Captured output"));
    for (const [node, value] of outputs) {
      parts.push(
        el(
          "div",
          { className: "run-output" },
          el("div", { className: "run-output-node" }, node),
          el("pre", { className: "run-output-body" }, value),
        ),
      );
    }
  }

  container.replaceChildren(...parts);
}

/** Render one node and its children. Depth drives indentation only. */
function renderNode(node: RunNode, depth: number): HTMLElement {
  const label =
    node.iteration === undefined ? node.nodeId : `${node.nodeId} #${String(node.iteration)}`;
  const row = el(
    "div",
    { className: `run-node run-node-${node.status}` },
    el("span", { className: "run-node-dot" }),
    el("span", { className: "run-node-id" }, label),
    el("span", { className: "run-node-type" }, node.type),
    node.agentName !== undefined && node.agentName !== ""
      ? el("span", { className: "text-muted text-sm" }, node.agentName)
      : null,
    el("span", { className: "run-node-dur" }, duration(node)),
  );
  row.style.paddingLeft = `calc(var(--sp-3) * ${String(depth)})`;

  const wrap = el("div", {}, row);
  for (const child of node.children ?? []) {
    wrap.appendChild(renderNode(child, depth + 1));
  }
  return wrap;
}

/** Elapsed time for a node, blank when it never finished. */
function duration(node: RunNode): string {
  if (node.startedAt === undefined || node.endedAt === undefined) {
    return "";
  }
  const ms = Date.parse(node.endedAt) - Date.parse(node.startedAt);
  if (!Number.isFinite(ms) || ms < 0) {
    return "";
  }
  return ms < 1000 ? `${String(ms)}ms` : `${(ms / 1000).toFixed(1)}s`;
}
