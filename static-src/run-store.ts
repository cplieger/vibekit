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
import { apiGet, apiGetTyped } from "./api-client.js";
import { decodeLiveRunsResponse } from "./wire/decoders.gen.js";

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
  /** KAS's node PLAN, forwarded verbatim by `GET /api/runs/{id}`.
   *
   *  `unknown` on purpose: this is the one part of the reply the client walks
   *  structurally rather than reading named fields off, and typing it as a shape
   *  would be a second representation of a structure vibekit does not own — the
   *  same reason `state` is passed through rather than re-modelled.
   *  `run-exec-source.ts` narrows it at the point of use.
   *
   *  It had ZERO readers until the exec view. Its only content the state tree
   *  lacks is a repeat's `maxIterations` / `onMaxIterations` / `stopCondition`, so
   *  a loop's bound and its exit condition were on the wire and had never been on
   *  screen. `update` appends to it, which is why it is refetched with the state
   *  rather than read once. */
  nodePlan?: unknown;
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

/** Per-run node plans, beside the signal rather than inside it.
 *
 *  Not a signal of its own: a plan is static for a run's life apart from an
 *  `update` append, and it is only ever read in the same pass as the state that
 *  woke the reader — so a second signal would fire a second render for a value
 *  nobody can observe changing on its own. Kept in step with the cells by
 *  `fetchRun` and dropped by `forgetRun`. */
const plans = new Map<string, unknown>();

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
      // The plan BEFORE the state, because the state assignment is what wakes
      // every reader: a subscriber that re-rendered between the two would draw a
      // repeat's bound from the previous plan.
      if (d.nodePlan === undefined) {
        plans.delete(workflowID);
      } else {
        plans.set(workflowID, d.nodePlan);
      }
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
  plans.delete(workflowID);
  launchedBy.delete(workflowID);
}

/** A run's node plan, read WITHOUT subscribing.
 *
 *  Untracked deliberately: its only reader is the exec-view adapter, which runs
 *  inside a pass the state signal already woke, so a tracked read would add a
 *  dependency that can never fire independently. */
export function runPlan(workflowID: string): unknown {
  return plans.get(workflowID);
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

/** A parentless run's own surface, and NOT a chat id.
 *
 *  Its lifecycle frames arrive with an EMPTY envelope chat id, but its ASKS are
 *  keyed to this synthetic value, because the dock queues per chat and a card with
 *  no key reaches no host. Two spellings of "no launching chat", so noteRunChat has
 *  to refuse both. */
const RUN_CHAT_PREFIX = "run:";

export function noteRunChat(workflowID: string, chatID: string): void {
  // The synthetic key is rejected HERE rather than at each caller, because "which
  // chat launched this run" is this module's own question: recording it would nest
  // the run's tab under a conversation that does not exist, and the caller that
  // reads it (`runChatID`) cannot tell a real id from a synthetic one afterwards.
  if (workflowID === "" || chatID === "" || chatID.startsWith(RUN_CHAT_PREFIX)) {
    return;
  }
  launchedBy.set(workflowID, chatID);
}

export function runChatID(workflowID: string): string {
  return launchedBy.get(workflowID) ?? "";
}

// ---------------------------------------------------------------------------
// The live-runs inventory: which chats have a run in flight.
//
// The eviction sweep's exemption source — a chat whose agent has a run going
// must keep its transcript window even while the reader is elsewhere, because
// the run's frames stream into it. Fed by the run lifecycle events (started and
// progress add, a terminal finish removes; a pause keeps — the run is still
// this process's to resume), and REBUILT from `GET /api/runs/live` at boot and
// after a transport gap, because events this client never saw (a paused run
// across a reload, a start inside an outage) leave the event-fed view blind.
// The server side is presence over its run leases, so a row here is exactly
// "vibekit put this run on the wire and nothing terminal released it".
// ---------------------------------------------------------------------------

/** workflow id → launching chat id ("" for a parentless run). Distinct from
 *  `launchedBy`, whose entries deliberately OUTLIVE a run so a finished one can
 *  be re-opened under its parent; this map holds live runs only. */
const liveRunChats = new Map<string, string>();

/** Record a run as live. Parentless runs ("" chat) are tracked too — they
 *  exempt no chat, but their presence mirrors the server's inventory. */
export function noteRunLive(workflowID: string, chatID: string): void {
  if (workflowID === "") {
    return;
  }
  liveRunChats.set(workflowID, chatID);
}

/** Drop a run that reached a terminal status. */
export function noteRunSettled(workflowID: string): void {
  liveRunChats.delete(workflowID);
}

/** Whether any live run was launched by this chat. A scan, not an index: the
 *  single-run rule bounds live runs to a handful, and a second map keyed by
 *  chat would be one more thing the rebuild could leave inconsistent. */
export function hasLiveRunForChat(chatID: string): boolean {
  if (chatID === "") {
    return false;
  }
  for (const c of liveRunChats.values()) {
    if (c === chatID) {
      return true;
    }
  }
  return false;
}

/** Rebuild the inventory from the server. A FAILED fetch keeps the event-fed
 *  state — degrading to cautious means a stale exemption (memory), never a
 *  wrongly-evicted live chat (correctness); the next gap or boot retries. */
export async function rebuildLiveRuns(): Promise<void> {
  const d = await apiGetTyped("/api/runs/live", decodeLiveRunsResponse);
  if (d === null) {
    return;
  }
  liveRunChats.clear();
  for (const r of d.runs) {
    if (r.workflow_id !== "") {
      liveRunChats.set(r.workflow_id, r.chat_id);
    }
  }
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

/** What KAS calls this node in a node PATH, which is not always what it calls it
 *  in the state tree.
 *
 *  One node kind diverges: a repeat's per-iteration container is `<repeatId>#<n>`
 *  in the state tree and `iter-<n>` in the `nodePath` KAS stamps on a step frame.
 *  Every other node contributes its own `nodeId` and the two spellings agree.
 *
 *  Derived from the container's own `iteration` rather than by rewriting the `#`
 *  suffix off the id, so nothing here depends on how KAS spells a generated id.
 *  A repeat child carrying no `iteration` falls back to its `nodeId`: a row in
 *  the wrong place beats content that vanishes, which is the same call the
 *  server's own `runNodePath` makes when a frame carries no path. */
export function nodePathSegment(node: RunNode, parent: RunNode | undefined): string {
  if (parent?.type === "repeat" && node.iteration !== undefined) {
    return `iter-${String(node.iteration)}`;
  }
  return node.nodeId;
}

/** A leaf's stable address within its run, in the spelling the server joins into
 *  a step's subtask id (`wf:<workflowId>:<a/b/c>`).
 *
 *  Rebuilt here rather than read off the node, because `NodeState` carries no
 *  path: KAS puts the path on the FRAME and the id on the node, so the two sides
 *  of the join are described differently and the client owns the reconciliation.
 *  A repeat's iterations share a `nodeId`, so the path is what separates them.
 *
 *  The frame's spelling is canonical and this TRANSLATES the tree into it, which
 *  is what `nodePathSegment` is for — before it, every step inside a loop got two
 *  rows, one from the tree and one unpainted from its own content frames. */
export function nodePathOf(root: RunNode | undefined, target: RunNode): string[] {
  if (root === undefined) {
    return [target.nodeId];
  }
  const found: string[] = [];
  const walk = (n: RunNode, parent: RunNode | undefined, trail: string[]): boolean => {
    const here = [...trail, nodePathSegment(n, parent)];
    if (n === target) {
      found.push(...here);
      return true;
    }
    for (const k of n.children ?? []) {
      if (walk(k, n, here)) {
        return true;
      }
    }
    return false;
  };
  walk(root, undefined, []);
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

/** Whether a pause reason means a step is waiting on a PERSON.
 *
 *  Two literals, and KAS writes both: `Step requested user input via
 *  send_message.` for a step's own question, and `Step '<id>' is waiting for user
 *  input.` when a plain Resume re-parks one — resume clears the run's pause reason
 *  and leaves the step node's signal, so the next step execution parks again under
 *  a fallback sentence naming the node. A third spelling, `… is waiting for the
 *  next user message.`, is the same condition on a step that asked for a message
 *  rather than an answer.
 *
 *  It lives HERE rather than in either renderer because two surfaces ask it (the
 *  transcript's run card and the `/run/{id}` page's alert) and the answer is a
 *  property of run state, which is this module's subject. The interpolated form is
 *  matched by its two ends because the node id sits in the middle; both spellings
 *  are specific enough that no involuntary pause reason can reach them.
 *
 *  The SERVER holds the same rule (`needInputPause`, internal/agent/run_ask.go) and
 *  neither copy can go: the server's decides whether to reconstruct an ask for a
 *  restart-orphaned run, this one decides what a reader is told when no ask has
 *  reached this client. */
export function isNeedInputPause(reason: string | undefined): boolean {
  if (reason === undefined || reason === "") {
    return false;
  }
  if (reason === "Step requested user input via send_message.") {
    return true;
  }
  return (
    reason.startsWith("Step '") &&
    (reason.endsWith("' is waiting for user input.") ||
      reason.endsWith("' is waiting for the next user message."))
  );
}
