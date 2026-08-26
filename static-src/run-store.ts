// ---------------------------------------------------------------------------
// The one owner of a workflow run's state.
//
// `GET /api/runs/{id}` is a passthrough of KAS's `_kiro/workflow/inspect`, and it
// is the ONLY honest source for a run: the three run SSE events are deliberately
// too thin to reconstruct one from (`run_start` re-fires on every resume, and
// `node_complete` carries neither `iteration` nor `branchId`, so two passes of one
// loop are indistinguishable on the wire). The contract is therefore
// invalidate-and-refetch, and this module is where that happens once instead of
// once per surface.
//
// THREE readers, which is why it exists: the transcript's run card, the
// `/run/{id}` view, and the tab dot for a parentless run. Before it, the view
// fetched on every event and the dot kept a parallel map of statuses, so a run
// open in both places fetched twice for the same frame and the two could disagree.
// `git-status-store.ts` is the precedent — one owner of a poll, signal-backed,
// several lookups off one rebuild.
//
// It is NOT a second model of a run. It caches what the endpoint returned,
// verbatim, and every derived question (which leaves, how many done, how long) is
// a function over that value rather than an accumulation. Nothing here reads an
// SSE payload for content; `handlers/run.ts` only says "this run changed".
// ---------------------------------------------------------------------------

import { signal, type Signal } from "@cplieger/reactive";
import { apiGet } from "./api-client.js";

/** One node of KAS's execution tree, from `state.root`.
 *
 *  Recursive through `children`. Every field beyond `nodeId`/`type`/`status` is
 *  optional because KAS fills each only when it applies: `agentName` and
 *  `sessionId` on a step that ran, `iteration` inside a repeat, `branchId` inside
 *  a parallel, `capturedOutput` when the step declared `captureOutput`,
 *  `watchTerminal` on a watch node. Named after KAS's own `NodeStateSchema`; do
 *  not rename a field to read better, or the passthrough stops being one. */
export interface RunNode {
  nodeId: string;
  type: "step" | "sequence" | "repeat" | "parallel" | "watch";
  status: "pending" | "running" | "paused" | "completed" | "failed" | "aborted" | "skipped";
  agentName?: string;
  modelId?: string;
  effortLevel?: string;
  sessionId?: string;
  startedAt?: string;
  endedAt?: string;
  iteration?: number;
  branchId?: string;
  children?: RunNode[];
  artifacts?: Record<string, string>;
  capturedOutput?: string;
  watchTerminal?: boolean;
  failureReason?: string;
  completionSignal?: "success" | "need_input" | "error";
  continuationAttempts?: number;
}

/** A run's whole state. `runLabel` is the name a launcher gave this execution and
 *  `workflowName` the recipe's; `stopInitiator`/`stopReason` are set only when a
 *  person stopped it, which is what separates "the user cancelled this" from "it
 *  failed" in the card's alert. */
export interface RunState {
  workflowId: string;
  workflowName?: string;
  runLabel?: string;
  status?: "running" | "paused" | "completed" | "failed" | "aborted";
  inputs?: Record<string, string>;
  artifacts?: Record<string, string>;
  capturedOutputs?: Record<string, string>;
  root?: RunNode;
  pauseReason?: string;
  pauseDetail?: { class?: string; code?: string; occurredAt?: string };
  stopInitiator?: string;
  stopReason?: string;
  parentSessionId?: string;
}

export interface RunInspect {
  workflowId: string;
  state?: RunState;
}

/** Per-run signals, created on demand. A signal per run rather than one version
 *  counter so a card re-renders for its OWN run only: a workspace running four
 *  scheduled workflows would otherwise repaint every card on every frame of any
 *  of them. */
const cells = new Map<string, Signal<RunState | undefined>>();

/** Runs with a fetch in flight, and runs invalidated while one was. Together they
 *  collapse an event storm into at most two requests: KAS emits a `run_progress`
 *  per node event, so a twenty-step run produces dozens, and the state that
 *  matters is the one AFTER the last of them. */
const inFlight = new Set<string>();
const stale = new Set<string>();

function cell(workflowID: string): Signal<RunState | undefined> {
  let c = cells.get(workflowID);
  if (c === undefined) {
    c = signal<RunState | undefined>(undefined);
    cells.set(workflowID, c);
  }
  return c;
}

/** Subscribe to a run's state. `undefined` until the first fetch resolves, which
 *  is what a caller renders a loading row for. */
export function runState(workflowID: string): RunState | undefined {
  return cell(workflowID).value;
}

/** Read a run's state WITHOUT subscribing. For a caller that must not re-run when
 *  the run changes — the dot's sweep already has its own dependency. */
export function peekRunState(workflowID: string): RunState | undefined {
  return cells.get(workflowID)?.peek();
}

/** Re-read a run from the server. Safe to call on every SSE frame: a second call
 *  while a fetch is in flight sets a flag rather than issuing a request, and one
 *  trailing fetch runs when the first settles. */
export function invalidateRun(workflowID: string): void {
  if (workflowID === "") {
    return;
  }
  if (inFlight.has(workflowID)) {
    stale.add(workflowID);
    return;
  }
  void fetchRun(workflowID);
}

async function fetchRun(workflowID: string): Promise<void> {
  inFlight.add(workflowID);
  try {
    const d = await apiGet<RunInspect>(`/api/runs/${encodeURIComponent(workflowID)}`);
    if (d?.state !== undefined) {
      cell(workflowID).value = d.state;
    }
  } finally {
    inFlight.delete(workflowID);
  }
  if (stale.delete(workflowID)) {
    await fetchRun(workflowID);
  }
}

/** Forget a run's cached state.
 *
 *  The cache's ONLY bound, and it has to be called by a reader that knows it was
 *  the last one: three surfaces read a cell (the transcript's run card, the run
 *  view, the tab dot) and none of them can drop it unilaterally. The transcript
 *  card's unmount plus "no run tab open" is the one condition that is both cheap to
 *  test and genuinely last, so `messages-blocks.ts` is the caller.
 *
 *  Without it a long-lived page accumulates one state object per run it ever saw,
 *  which a workspace with a scheduled workflow reaches in a day. A later
 *  `invalidateRun` re-creates the cell, so forgetting early costs one fetch and
 *  never a wrong answer. */
export function forgetRun(workflowID: string): void {
  cells.delete(workflowID);
  stale.delete(workflowID);
  launchedBy.delete(workflowID);
}

/** Which chat's agent launched a run, learned from the SSE envelope.
 *
 *  It lives here rather than in the handler that reads it because it is a fact
 *  ABOUT a run and every surface needs it for the same reason: a run's tab belongs
 *  under its launching chat, so re-opening one from a transcript link or a deep
 *  link has to know the parent. `parentSessionId` on the fetched state cannot
 *  answer it — that is an ACP session id, not a chat id, and the client indexes
 *  nothing by session.
 *
 *  Empty for a parentless run, which has no launching chat by definition. */
const launchedBy = new Map<string, string>();

export function noteRunChat(workflowID: string, chatID: string): void {
  if (workflowID === "" || chatID === "") {
    return;
  }
  launchedBy.set(workflowID, chatID);
}

export function runChatID(workflowID: string): string {
  return launchedBy.get(workflowID) ?? "";
}

// ---------------------------------------------------------------------------
// Derived reads. Functions over the cached value, never stored beside it: a
// second copy of "how many steps finished" is a second thing that can be wrong.
// ---------------------------------------------------------------------------

/** The run's LEAF nodes in plan order — the steps and watches a reader thinks of
 *  as "the work". A `sequence`, `repeat` or `parallel` node is scaffolding: it has
 *  no agent, no duration of its own and nothing to read, so the card renders the
 *  leaves and lets the containers contribute only their iteration and branch
 *  labels through the leaves beneath them. */
export function leafNodes(root: RunNode | undefined): RunNode[] {
  if (root === undefined) {
    return [];
  }
  const out: RunNode[] = [];
  const walk = (n: RunNode): void => {
    const kids = n.children ?? [];
    if (kids.length === 0) {
      out.push(n);
      return;
    }
    for (const k of kids) {
      walk(k);
    }
  };
  walk(root);
  return out;
}

/** A leaf's stable address within its run, matching the `nodePath` the server
 *  joins into a step's subtask id (`wf:<workflowId>:<a/b/c>`).
 *
 *  Rebuilt here rather than read off the node, because `NodeState` carries no
 *  path: KAS puts the path on the FRAME and the id on the node, so the two sides
 *  of the join are described differently and the client owns the reconciliation.
 *  A repeat's iterations share a `nodeId`, so the path is what separates them. */
export function nodePathOf(root: RunNode | undefined, target: RunNode): string[] {
  if (root === undefined) {
    return [target.nodeId];
  }
  const found: string[] = [];
  const walk = (n: RunNode, trail: string[]): boolean => {
    const here = [...trail, n.nodeId];
    if (n === target) {
      found.push(...here);
      return true;
    }
    for (const k of n.children ?? []) {
      if (walk(k, here)) {
        return true;
      }
    }
    return false;
  };
  walk(root, []);
  return found.length > 0 ? found : [target.nodeId];
}

export interface RunCounters {
  total: number;
  done: number;
  failed: number;
  /** The 1-based position of the running leaf, or 0 when none is running. What
   *  the header's "step N of M" states, and it is the RUNNING one rather than
   *  `done + 1` because a parallel node has several in flight and a skipped leaf
   *  would otherwise shift the count. */
  current: number;
}

export function runCounters(state: RunState | undefined): RunCounters {
  const leaves = leafNodes(state?.root);
  let done = 0;
  let failed = 0;
  let current = 0;
  for (const [i, n] of leaves.entries()) {
    if (n.status === "completed" || n.status === "skipped") {
      done++;
    } else if (n.status === "failed" || n.status === "aborted") {
      failed++;
    }
    if (current === 0 && (n.status === "running" || n.status === "paused")) {
      current = i + 1;
    }
  }
  return { total: leaves.length, done, failed, current };
}

/** Wall-clock milliseconds a node or run has been going, or ran for.
 *
 *  `endedAt` when it finished, `now` while it runs, and 0 when it never started —
 *  a pending step must read as nothing rather than as "started at the epoch",
 *  which is what `Date.parse(undefined)` would give. */
export function elapsedMs(startedAt: string | undefined, endedAt: string | undefined): number {
  if (startedAt === undefined || startedAt === "") {
    return 0;
  }
  const from = Date.parse(startedAt);
  if (Number.isNaN(from)) {
    return 0;
  }
  const to = endedAt === undefined || endedAt === "" ? Date.now() : Date.parse(endedAt);
  if (Number.isNaN(to)) {
    return 0;
  }
  return Math.max(0, to - from);
}

/** The run's own span, from its first leaf's start to its last leaf's end.
 *
 *  Derived rather than read, because `WorkflowState` carries no run-level
 *  timestamps: only the nodes do. A run still going has no end, so the span runs
 *  to now, which is what makes the header's clock tick. */
export function runElapsedMs(state: RunState | undefined): number {
  const leaves = leafNodes(state?.root);
  let first = Number.POSITIVE_INFINITY;
  let last = 0;
  let running = false;
  for (const n of leaves) {
    if (n.startedAt !== undefined && n.startedAt !== "") {
      const t = Date.parse(n.startedAt);
      if (!Number.isNaN(t)) {
        first = Math.min(first, t);
      }
    }
    if (n.endedAt !== undefined && n.endedAt !== "") {
      const t = Date.parse(n.endedAt);
      if (!Number.isNaN(t)) {
        last = Math.max(last, t);
      }
    } else if (n.status === "running" || n.status === "paused") {
      running = true;
    }
  }
  if (!Number.isFinite(first)) {
    return 0;
  }
  return Math.max(0, (running || last === 0 ? Date.now() : last) - first);
}

/** Whether a run is still this process's to finish. Drives the elapsed clock and
 *  the card's open-by-default state. `paused` counts as live: it is stopped
 *  waiting for something, not over. */
export function runIsLive(state: RunState | undefined): boolean {
  const s = state?.status;
  return s === "running" || s === "paused";
}
