// The one owner of a workflow run's state: refetched on invalidation, cached
// verbatim, never accumulated from an SSE payload. Its readers, the coalescing,
// the cache bound and the live-run inventory: vibekit-client.md "The run store";
// why the run events cannot reconstruct a run: vibekit-acp.md.

import { signal, touch, type Signal } from "@cplieger/reactive";
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
  /** KAS's node PLAN, forwarded verbatim. `unknown` because the client walks it
   *  structurally, so typing it would re-model a structure vibekit does not own;
   *  `run-exec-source.ts` narrows it at the point of use. Contents: vibekit-acp.md. */
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

/** Read a run's state WITHOUT subscribing. Module-private: one caller, so exporting
 *  it would add a surface only a test reaches. */
function peekRunState(workflowID: string): RunState | undefined {
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

/** Forget a run's cached state — the cache's ONLY bound, so it must be called by
 *  a reader that knows it was the last one (`messages-blocks.ts`). Which condition
 *  qualifies and why: vibekit-client.md "The run store". */
export function forgetRun(workflowID: string): void {
  cells.delete(workflowID);
  stale.delete(workflowID);
  plans.delete(workflowID);
  launchedBy.delete(workflowID);
}

/** What this run is CALLED, or `""` when nothing has been fetched for it yet: the
 *  launcher's label for this execution first, the recipe's name second. UNTRACKED,
 *  like `runPlan`. Both reasons: vibekit-client.md "The run store". */
export function runLabelOf(workflowID: string): string {
  const state = peekRunState(workflowID);
  const label = state?.runLabel ?? "";
  return label === "" ? (state?.workflowName ?? "") : label;
}

/** A run's node plan, read WITHOUT subscribing.
 *
 *  Untracked deliberately: its only reader is the exec-view adapter, which runs
 *  inside a pass the state signal already woke, so a tracked read would add a
 *  dependency that can never fire independently. */
export function runPlan(workflowID: string): unknown {
  return plans.get(workflowID);
}

/** Which chat's agent launched a run, learned from the SSE envelope, and empty for
 *  a parentless run. A fact ABOUT a run rather than the reading handler's, and the
 *  one `parentSessionId` cannot supply: vibekit-client.md, `run-dots.ts`. */
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

// The live-runs inventory: which chats have a run in flight. Event-fed, rebuilt
// from `GET /api/runs/live`, and a row carries two facts because two readers ask
// two questions — vibekit-client.md "The run store".

/** One live run: the chat that launched it ("" for a parentless run), and whether
 *  it is still EXECUTING as opposed to parked. `executing` is
 *  `hasExecutingRunForChat`'s alone; every other reader takes the whole row. */
interface LiveRunRow {
  readonly chat: string;
  readonly executing: boolean;
}

/** workflow id → its live row. Distinct from `launchedBy`, whose entries
 *  deliberately OUTLIVE a run so a finished one can be re-opened under its
 *  parent; this map holds live runs only. */
const liveRunChats = new Map<string, LiveRunRow>();

/** Bumped by every writer of the map above, so a reactive reader can subscribe to
 *  the inventory CHANGING. A plain `Map` is not a signal, so a `computed` over it
 *  would track nothing and never re-evaluate. */
const liveRunsVersion = signal(0);

/** `peek()` on the write is the idiom `run-dots.ts` records: a `+ 1` off `.value`
 *  subscribes the writing effect to the signal it is about to write, which the
 *  reactive layer refuses with `Cycle detected`. */
function bumpLiveRuns(): void {
  liveRunsVersion.value = liveRunsVersion.peek() + 1;
}

/** Record a run as live, saying whether it is executing. Parentless runs ("" chat)
 *  are tracked too: they exempt no chat, but their presence mirrors the server's
 *  inventory, which is what the dot painter reads.
 *
 *  `executing` is the CALLER's statement rather than a default — why, and what each
 *  of the four callers knows: vibekit-client.md "The run store". */
export function noteRunLive(workflowID: string, chatID: string, executing: boolean): void {
  if (workflowID === "") {
    return;
  }
  liveRunChats.set(workflowID, { chat: chatID, executing });
  bumpLiveRuns();
}

/** Drop a run that reached a terminal status. */
export function noteRunSettled(workflowID: string): void {
  liveRunChats.delete(workflowID);
  bumpLiveRuns();
}

/** Whether this chat has a run that is still EXECUTING — the store-eviction
 *  exemption, and the only reader that filters on `executing`. It asks "are frames
 *  still arriving into this chat's transcript", which a PARKED run answers no to.
 *  Why the narrowing: vibekit-client.md "The run store". */
export function hasExecutingRunForChat(chatID: string): boolean {
  return anyRunForChat(chatID, (r) => r.executing);
}

/** Whether this chat has ANY live run, parked ones included — a DIFFERENT question
 *  from the one above, which is why these are two predicates rather than one
 *  filtered. Its consumer is the ask sweep in `handlers/run.ts`; narrowing it to
 *  `executing` would strand a parked run's ask. vibekit-client.md has the rest. */
export function hasLiveRunForChat(chatID: string): boolean {
  return anyRunForChat(chatID, () => true);
}

/** The live runs this chat launched, in the order they were recorded. Parked runs
 *  are INCLUDED like `hasLiveRunForChat`; parentless ones are EXCLUDED, their own
 *  tab dot already surfacing them (`run-dots.ts`).
 *
 *  The one TRACKED read of the inventory here, because this caller is a reactive
 *  effect where the two booleans' are not: vibekit-client.md "The run store". */
export function liveRunIDsForChat(chatID: string): string[] {
  touch(liveRunsVersion);
  if (chatID === "") {
    return [];
  }
  const out: string[] = [];
  for (const [id, r] of liveRunChats) {
    if (r.chat === chatID) {
      out.push(id);
    }
  }
  return out;
}

/** The scan both readers share. Not an index: the single-run rule bounds live runs
 *  to a handful, and a second map keyed by chat would be one more thing the
 *  rebuild could leave inconsistent. */
function anyRunForChat(chatID: string, pass: (r: LiveRunRow) => boolean): boolean {
  if (chatID === "") {
    return false;
  }
  for (const r of liveRunChats.values()) {
    if (r.chat === chatID && pass(r)) {
      return true;
    }
  }
  return false;
}

/** Externally-owned "this client now knows about this run", REGISTERED rather than
 *  imported because `run-dots.ts` imports this module and the reverse edge would
 *  close a cycle. Unregistered, the rebuild seeds state and repaints nothing. */
let noteRunKnown: ((workflowID: string) => void) | null = null;

/** Register the observer the rebuild reports each live run to. Last wins. */
export function registerLiveRunObserver(fn: (workflowID: string) => void): void {
  noteRunKnown = fn;
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
      // Three seeds per row, and the two beyond the inventory are what a reload
      // would otherwise lose — vibekit-client.md "The run store".
      liveRunChats.set(r.workflow_id, { chat: r.chat_id, executing: r.executing });
      noteRunChat(r.workflow_id, r.chat_id);
      noteRunKnown?.(r.workflow_id);
      invalidateRun(r.workflow_id);
    }
  }
  // ONE bump for the whole rebuild rather than one per row.
  bumpLiveRuns();
}

// Derived reads: functions over the cached value, never stored beside it — a second
// copy of "how many steps finished" is a second thing that can be wrong.

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

/** What KAS calls this node in a node PATH, which for a repeat's per-iteration
 *  container is not what it calls it in the state tree — the two spellings and why
 *  the frame's is canonical: vibekit-acp.md "Workflow runs on the wire".
 *
 *  A repeat child carrying no `iteration` falls back to its `nodeId`: a row in the
 *  wrong place beats content that vanishes, the same call the server's own
 *  `runNodePath` makes when a frame carries no path. */
export function nodePathSegment(node: RunNode, parent: RunNode | undefined): string {
  if (parent?.type === "repeat" && node.iteration !== undefined) {
    return `iter-${String(node.iteration)}`;
  }
  return node.nodeId;
}

export interface NodeAddress {
  /** The joined segments, in the spelling the server joins into a step's subtask
   *  id (`wf:<workflowId>:<a/b/c>`). */
  readonly path: string[];
  /** Whether the walk PLACED the target in this tree. False means `path` is the
   *  bare `nodeId` fallback below rather than an address. */
  readonly placed: boolean;
}

/** A leaf's stable address within its run, plus whether the walk placed it.
 *
 *  Rebuilt from the tree rather than read off the node, because `NodeState` carries
 *  no path — the join and who owns it: vibekit-client.md "The run card". */
export function nodeAddressOf(root: RunNode | undefined, target: RunNode): NodeAddress {
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
  if (root !== undefined) {
    walk(root, undefined, []);
  }
  if (found.length > 0) {
    return { path: found, placed: true };
  }
  // An UNPLACED node keeps the bare id, which `placed: false` stops a consumer
  // spending as an address (its first segment is a leaf id where the endpoint
  // asserts the run id). A row still needs a key, so the value stays.
  return { path: [target.nodeId], placed: false };
}

/** The address's path alone, for a consumer that only needs a render key.
 *
 *  Kept as the thin wrapper because that is the whole of what a row KEY wants;
 *  anything that puts the value on the wire reads `nodeAddressOf` instead. */
export function nodePathOf(root: RunNode | undefined, target: RunNode): string[] {
  return nodeAddressOf(root, target).path;
}

export interface RunCounters {
  total: number;
  done: number;
  failed: number;
  /** The 1-based position of the RUNNING leaf, or 0 when none is — the header's
   *  "step N of M", and not `done + 1`: vibekit-client.md "The run store". */
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

/** Whether a pause REASON means a step is waiting on a person — the reason half of
 *  `isNeedInputPark`, which is the question every surface asks.
 *
 *  Three sentences KAS writes: a step's own `send_message` park, plus two a plain
 *  Resume re-parks under (it clears the run's reason and leaves the node's signal).
 *  The interpolated pair is matched by its two ENDS, because the node id sits in the
 *  middle. Exported only for `run-store-pause.node.test.ts`, which pins them against
 *  `needInputPause` (internal/agent/run_ask.go) — the owner of both facts. */
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

/** The paused node whose own completion signal says it is waiting on a person.
 *  Depth-first, first match wins. Why the per-NODE signal is the only thing left of
 *  a park inside a parallel branch: vibekit-acp.md. */
function needInputNode(n: RunNode | undefined): RunNode | undefined {
  if (n === undefined) {
    return undefined;
  }
  if (n.status === "paused" && n.completionSignal === "need_input") {
    return n;
  }
  for (const child of n.children ?? []) {
    const hit = needInputNode(child);
    if (hit !== undefined) {
      return hit;
    }
  }
  return undefined;
}

/** Whether a run is parked on a PERSON — the one pause a reader has to act on.
 *
 *  TWO ARMS, because neither answers alone: the run's own pause reason, which is what
 *  a plain step's park writes, and a paused node's completion signal, which is the
 *  only thing left of a park that happened inside a parallel branch.
 *
 *  Gated on `paused`, like the dot vocabulary's own arm: a reason or a signal
 *  outliving its pause must never paint a finished run as awaiting input. */
export function isNeedInputPark(state: RunState | undefined): boolean {
  if (state?.status !== "paused") {
    return false;
  }
  return isNeedInputPause(state.pauseReason) || needInputNode(state.root) !== undefined;
}
