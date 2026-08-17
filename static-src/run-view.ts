// ---------------------------------------------------------------------------
// One workflow run: its node tree, its captured outputs, and its controls.
//
// Opened from the History list (a review) or from the Workflows tab (a live,
// launcher-owned tab whose close cancels the run).
//
// Two flavours, one difference that matters: an OWNED tab (the Workflows tab's
// Run button) carries controls and its close cancels the run; a REVIEW (a History
// row) carries neither. Pause and Resume are KAS's own verbs, Cancel is on both
// live statuses, and a finished run offers none in either flavour.
//
// The flavour is the opener's, not the status's, and that is the whole design.
// GET /api/sessions does not filter by status, so History genuinely lists running
// and paused runs — but History is where finished work is READ. A tab that can
// pause or cancel is a live surface, and the live surface is the Workflows tab.
// An earlier revision let History's tabs carry controls because a live run there
// was otherwise a dead end; the dead end is the honest answer, and the run is
// reachable through the door that launched it.
//
// So there is no composer, no input bar, and for a review no controls either,
// because there is nobody to type to and nothing here owns the work.
//
// The tree is rendered from KAS's own `state` shape, passed through verbatim by
// GET /api/runs/{id}. vibekit deliberately does not re-model it: the
// node plan is KAS's structure, and a second representation of it here would be
// one more thing to keep in sync for no gain.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { el } from "@cplieger/reactive";
import { openRunTab } from "./tabs.js";
import { mountRunDecisionDock, rerenderDocks } from "./decision-dock.js";
import { cancelRun, pauseRun, resumeRun, retryRun } from "./actions/runs.js";
import { RUN_CONTROLS, CONTROL_LABEL, type RunVerb } from "./run-controls.js";

/** Verb → its action. Separate from run-controls.ts's table on purpose: that
 *  module is the pure RULE and must stay importable without the actions
 *  framework; this is the wiring. */
const RUN_ACTION: Record<RunVerb, { dispatch: (id: string) => Promise<unknown> }> = {
  pause: pauseRun,
  resume: resumeRun,
  cancel: cancelRun,
  retry: retryRun,
};

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

/** The run this view is currently showing, so an SSE invalidation knows whether
 *  it is about the run on screen. Cleared when the view loads a different one;
 *  a closed tab simply stops matching, because the next open reassigns it. */
let shownRun = "";

/** Whether the run on screen was opened as a live, owned tab.
 *
 *  Controls render only for those. A run reached from History is a REVIEW even
 *  when the run is still moving: History is where finished work is read, and a
 *  tab you can act on belongs to the surface that launched the run. Set by the
 *  opener rather than derived from status, because the same `running` run is
 *  actionable through one door and a record through the other. */
let shownRunOwned = false;
/** Whether the shown run was launched manually (no parent chat). Only a
 *  parentless run offers Retry; a parented one is the agent's to recover. */
let shownRunParentless = false;

/** Point the shared run view (one DOM element serves every run tab) at a run,
 *  and give its dock somewhere to render. The dock host is mounted ONCE with a
 *  dynamic match — the run on screen — so tab switches re-key it without
 *  re-mounting. */
function showRun(workflowID: string, owned: boolean, parentless: boolean): void {
  shownRun = workflowID;
  shownRunOwned = owned;
  shownRunParentless = parentless;
  const dock = document.getElementById("run-dock");
  if (dock !== null) {
    mountRunDecisionDock(dock, () => shownRun);
  }
  rerenderDocks();
  void load(workflowID);
}

/** Open a run REVIEW: read-only, closing the tab closes nothing. The History
 *  page's row opens these.
 *
 *  Read-only even for a run that is still going. GET /api/sessions does not
 *  filter by status, so History does list running and paused runs, and an earlier
 *  revision let those carry controls on the reasoning that a live run in History
 *  was otherwise a dead end. That is the wrong trade: History is where finished
 *  work is read, and a tab that can pause or cancel is a live surface wearing a
 *  record's clothes. The live door is the Workflows tab. */
export function openRunView(workflowID: string, name: string, parentless = false): void {
  openRunTab(workflowID, name, () => {
    showRun(workflowID, false, parentless);
  });
}

/** Open a LAUNCHER-OWNED run tab: closing it CANCELS the run (user decision —
 *  the × means stop; a workflow that outlived its tab would spend credits and
 *  edit files with nothing on screen). The Workflows tab's Run button opens
 *  these. Cancel is fire-and-forget: the terminal run_complete tears the
 *  server-side bridge down, and the run_finished event settles every list. */
export function openLiveRunView(workflowID: string, name: string): void {
  openRunTab(
    workflowID,
    name,
    () => {
      // A launcher-owned run came from LaunchRun, which is always parentless.
      showRun(workflowID, true, true);
    },
    {
      onClose: () => {
        void cancelRun.dispatch(workflowID, { silent: true });
      },
    },
  );
}

/** Re-read the run on screen, if it is this one.
 *
 *  This is the whole client half of the invalidation contract: a run event says
 *  only "something changed", and the state comes from `inspect`. Reconstructing a
 *  run from its events would garble it — `run_start` re-fires on every resume and
 *  `node_complete` carries neither iteration nor branch, so two passes of one
 *  loop are indistinguishable on the wire. */
export function refreshRunView(workflowID: string): void {
  if (workflowID !== "" && workflowID === shownRun) {
    void load(workflowID);
  }
}

async function load(workflowID: string): Promise<void> {
  const container = document.getElementById("run-body");
  if (container === null) {
    return;
  }
  // Only the FIRST paint shows a loading row. A refetch driven by an
  // invalidation would otherwise blank a run the user is reading, several times a
  // minute on a busy run.
  if (container.childElementCount === 0) {
    container.replaceChildren(el("div", { className: "list-empty" }, "Loading run…"));
  }

  const d = await apiGet<RunInspect>(`/api/runs/${encodeURIComponent(workflowID)}`);
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
      el("span", { className: "run-id" }, state.workflowId),
      buildRunControls(workflowID, state.status ?? ""),
    ),
  );

  if (state.root !== undefined) {
    parts.push(el("h3", { className: "section-title" }, "Steps"));
    parts.push(el("div", { className: "run-tree" }, renderNode(state.root, 0)));
  }

  // Captured outputs are the point of reviewing a finished run: they are what
  // the steps produced. An EMPTY one is RENDERED, not dropped, and that is the
  // whole point of this block.
  //
  // A node gets a key only when it captured (KAS defaults captureOutput to true
  // and omits the key when a step sets it false), and the captured value is that
  // step's last assistant text. So an empty value is not "nothing to show": it
  // says the step ran and finished without saying anything, which is the most
  // diagnostic fact a finished run holds. Dropping it left the reader with no
  // row at all and no way to tell that step from one that never ran.
  const outputs = Object.entries(state.capturedOutputs ?? {});
  if (outputs.length > 0) {
    parts.push(el("h3", { className: "section-title" }, "Captured output"));
    for (const [node, value] of outputs) {
      parts.push(
        el(
          "div",
          { className: "run-output" },
          el("div", { className: "run-output-node" }, node),
          value.trim() === ""
            ? el(
                "div",
                { className: "run-output-empty" },
                "Empty: this step's last assistant message carried no text.",
              )
            : el("pre", { className: "run-output-body" }, value),
        ),
      );
    }
  }

  container.replaceChildren(...parts);
}

/** The run's control row. Empty for an unknown status, which renders nothing
 *  rather than an empty container. */
function buildRunControls(workflowID: string, status: string): HTMLElement | null {
  let verbs = RUN_CONTROLS[status];
  // Two different gates, because the two doors mean different things.
  //
  // An OWNED tab (the launcher's) carries the status's live verbs. A REVIEW
  // (opened from History) carries none of them — reaching a live run's controls
  // is the launching tab's job. But RETRY is not a live verb: it acts on a
  // finished run, and History is exactly where a failed one is found, so a
  // review offers retry and nothing else.
  //
  // Both are further gated on the run being PARENTLESS (user decision): an
  // agent-parented run's recovery is the agent's own, on a bridge it holds.
  if (verbs !== undefined && !shownRunOwned) {
    verbs = verbs.filter((v) => v === "retry");
  }
  if (verbs !== undefined && !shownRunParentless) {
    verbs = verbs.filter((v) => v !== "retry");
  }
  // Empty is as good as absent: a completed run offers nothing, and an empty
  // control row would be a visible container with no purpose.
  if (verbs === undefined || verbs.length === 0) {
    return null;
  }
  const row = el("div", { className: "run-controls" });
  for (const verb of verbs) {
    const dispatch = RUN_ACTION[verb];
    const btn = el(
      "button",
      {
        type: "button",
        className: verb === "cancel" ? "btn btn-sm btn-danger" : "btn btn-sm",
        onclick: () => {
          // No optimistic state flip. Every one of these verbs settles at a NODE
          // boundary, so the run is still `running` when the reply arrives and a
          // flipped label would be a lie for as long as the node takes. The
          // run_progress invalidation is what repaints this row.
          void dispatch.dispatch(workflowID);
        },
      },
      CONTROL_LABEL[verb],
    );
    row.appendChild(btn);
  }
  return row;
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
      ? el("span", { className: "run-node-agent" }, node.agentName)
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
