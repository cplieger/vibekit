// The one owner of a workflow run's state: refetched on invalidation, cached
// verbatim, never accumulated from an SSE payload. Its readers, the coalescing,
// the cache bound and the live-run inventory: vibekit-client.md "The run store";
// why the run events cannot reconstruct a run: vibekit-acp.md.

import { signal, touch, type Signal } from "@cplieger/reactive";
import { apiGetOrError, apiGetTyped } from "./api-client.js";
import { decodeLiveRunsResponse, decodeRunControlsResponse } from "./wire/decoders.gen.js";
import type { ConnectedPayload, LiveRun, RunControlsResponse } from "./wire/types.gen.js";
import {
  classifyRunNodeStatus,
  classifyRunStatus,
  runStatusActive,
  type ClassifiedRunNodeStatus,
  type ClassifiedRunStatus,
} from "./run-status.js";

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
  status: ClassifiedRunNodeStatus;
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
  status?: ClassifiedRunStatus;
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

interface RawRunNode extends Omit<RunNode, "status" | "children"> {
  status: string;
  children?: RawRunNode[];
}

interface RawRunState extends Omit<RunState, "status" | "root"> {
  status?: string;
  root?: RawRunNode;
}

interface RawRunInspect extends Omit<RunInspect, "state"> {
  state?: RawRunState;
}

function classifyRunNode(node: RawRunNode): RunNode {
  const { status, children, ...rest } = node;
  const out: RunNode = { ...rest, status: classifyRunNodeStatus(status) };
  if (children !== undefined) {
    out.children = children.map(classifyRunNode);
  }
  return out;
}

function classifyRunState(state: RawRunState): RunState {
  const { status, root, ...rest } = state;
  const out: RunState = { ...rest };
  const classified = classifyRunStatus(status);
  if (classified !== undefined) {
    out.status = classified;
  }
  if (root !== undefined) {
    out.root = classifyRunNode(root);
  }
  return out;
}

/** Per-run signals, created on demand. A signal per run rather than one version
 *  counter so a card re-renders for its OWN run only: a workspace running four
 *  scheduled workflows would otherwise repaint every card on every frame of any
 *  of them. */
const cells = new Map<string, Signal<RunState | undefined>>();

/** Runs with a fetch in flight, and runs invalidated while one was. Together they
 *  collapse an event storm into at most two requests: KAS emits a `run_progress` per
 *  node event, and the state that matters is the one AFTER the last of them. `stale`
 *  carries the CAUSE the trailing fetch will run under, so the token below survives a
 *  coalesce. */
const inFlight = new Set<string>();
const stale = new Map<string, string>();

/** The invalidation CAUSE behind each run's current answer: recorded when its fetch is
 *  ISSUED and kept once that fetch has answered, which is what makes one cause cost one
 *  request per run however the two callers interleave — a second invalidation naming a
 *  cause this run was already READ for is a no-op, because that read was issued after
 *  the cause. A failed read records NOTHING, so a repeat retries rather than inheriting
 *  a claim nothing answered. Dropped by `forgetRun`. */
const answeredCause = new Map<string, string>();

/** The ladder behind a run read that produced nothing: three attempts at 1s doubling,
 *  one per workflow id.
 *
 *  Bounded because a failed read is not always transient — `handleRun` answers 503 for
 *  `workflow.ErrUnknownMethod`, an engine with no workflow support at all, which no
 *  number of attempts can talk into describing a run. Dropped by a read that ANSWERED,
 *  by a read the server SETTLED, and by `forgetRun`. */
const RUN_RETRY_LIMIT = 3;
const RUN_RETRY_BASE_MS = 1000;

/** The status a read gets for a run the server can describe no further, and the one
 *  failure the ladder is skipped for outright.
 *
 *  `handleRun` grades a failed inspect three ways and this is the narrow arm: the engine
 *  answered ABOUT this run and refused, so the answer is the same however often it is
 *  asked. Its two siblings — 503 for an engine with no workflow verbs, 502 for a read
 *  that never reached one — say nothing about the run, so both keep the ladder, and 503
 *  is why the ladder is bounded rather than infinite. */
const RUN_GONE_STATUS = 404;

interface RunRetry {
  timer: ReturnType<typeof setTimeout> | undefined;
  attempts: number;
}

const runRetries = new Map<string, RunRetry>();

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
 *  the run changes. */
export function peekRunState(workflowID: string): RunState | undefined {
  return cells.get(workflowID)?.peek();
}

/** Apply a `run_progress` frame to the cached tree, and report whether it landed.
 *
 *  `false` means the caller must refetch, and there are exactly three reasons for
 *  it: the frame names no node (`loop_iteration` and `steps_queued` change the
 *  tree's SHAPE, `paused` is run-level with its reason on `inspect` alone), the
 *  run is not cached at all (nothing to patch — a client that missed the start),
 *  or the path addresses a node this tree does not hold yet (a step inside a
 *  freshly-created iteration container). So the refetch survives as the
 *  gap-recovery path it always should have been, and a progressing run costs no
 *  HTTP round trips.
 *
 *  Idempotent, which is what makes it safe against KAS's duplicate frames across
 *  a resume: every write is an assignment addressed by path, never an increment.
 *
 *  The tree is copied down the matched path rather than mutated in place. The
 *  signal's value is what readers hold, and a reader that keeps the previous
 *  value to compare against — the exec view does — must not find it rewritten
 *  underneath. Siblings are shared by reference: only the spine changes. */
export function applyRunProgress(p: RunProgressFrame): boolean {
  if (p.workflow_id === "" || p.node_path === undefined || p.node_path === "") {
    return false;
  }
  const c = cells.get(p.workflow_id);
  const root = c?.peek()?.root;
  if (c === undefined || root === undefined) {
    return false;
  }
  const next = patchNode(root, undefined, p.node_path.split("/"), p);
  if (next === undefined) {
    return false;
  }
  if (next === root) {
    // The frame addressed a node this tree holds and moved nothing about it: a
    // watch poll re-stating `running`, or a duplicate frame across a resume.
    // Landed, so no refetch — and no assignment, because a new object identity
    // for an unchanged tree wakes every subscriber for a repaint of the same
    // pixels. `patchedLeaf` is where the sameness is decided.
    return true;
  }
  const state = c.peek();
  if (state === undefined) {
    return false;
  }
  c.value = { ...state, root: next };
  return true;
}

/** The fields of a `run_progress` payload this store reads. Declared here rather
 *  than imported from the generated type so the store's contract is the four
 *  fields it applies, and a test can hand it a literal. */
export interface RunProgressFrame {
  workflow_id: string;
  node_path?: string;
  status?: string;
  started_at?: string;
  ended_at?: string;
  failure_reason?: string;
}

/** Rebuild `node`'s subtree with the addressed descendant patched, or `undefined`
 *  when this tree does not hold it.
 *
 *  `trail` is the path still to walk. Matching uses `nodePathSegment`, the same
 *  translation `nodePathOf` uses in the other direction, so a repeat's
 *  `iter-<n>` frame segment finds the `<repeatId>#<n>` container it names. */
function patchNode(
  node: RunNode,
  parent: RunNode | undefined,
  trail: string[],
  p: RunProgressFrame,
): RunNode | undefined {
  const [head, ...rest] = trail;
  if (head === undefined || nodePathSegment(node, parent) !== head) {
    return undefined;
  }
  if (rest.length === 0) {
    return patchedLeaf(node, p);
  }
  const kids = node.children;
  if (kids === undefined) {
    return undefined;
  }
  for (const [i, k] of kids.entries()) {
    const patched = patchNode(k, node, rest, p);
    if (patched === k) {
      // Found, and unchanged. Rebuilding the spine over an identical child would
      // hand `applyRunProgress` a new root for a tree that did not move.
      return node;
    }
    if (patched !== undefined) {
      return { ...node, children: kids.with(i, patched) };
    }
  }
  return undefined;
}

/** The addressed node with the frame's fields written over it, or the SAME node
 *  when the frame moves none of them.
 *
 *  Every field is set only when the frame carries it, because a frame states what
 *  changed: `node_complete` carries no `started_at` and must not lose the one
 *  node_start left. `exactOptionalPropertyTypes` is why each is a conditional
 *  spread rather than an assignment of a possibly-undefined value.
 *
 *  Returning the same reference for an unchanged node is what the callers above
 *  read to leave the spine and the signal alone. */
function patchedLeaf(node: RunNode, p: RunProgressFrame): RunNode {
  const status = nodeStatus(p.status);
  const startedAt = nonEmpty(p.started_at);
  const endedAt = nonEmpty(p.ended_at);
  const failureReason = nonEmpty(p.failure_reason);
  const moved =
    (status !== undefined && status !== node.status) ||
    (startedAt !== undefined && startedAt !== node.startedAt) ||
    (endedAt !== undefined && endedAt !== node.endedAt) ||
    (failureReason !== undefined && failureReason !== node.failureReason);
  if (!moved) {
    return node;
  }
  return {
    ...node,
    ...(status === undefined ? {} : { status }),
    ...(startedAt === undefined ? {} : { startedAt }),
    ...(endedAt === undefined ? {} : { endedAt }),
    ...(failureReason === undefined ? {} : { failureReason }),
  };
}

/** The value, or `undefined` for absent and for the empty string — which is what
 *  an omitted `omitempty` string field decodes to and means "unchanged", never
 *  "clear this". */
function nonEmpty(v: string | undefined): string | undefined {
  return v === undefined || v === "" ? undefined : v;
}

/** KAS's NodeState status words, as the tree spells them. The frame carries the
 *  status as a plain string — it is forwarded from KAS rather than enumerated
 *  server-side — so this is where it is narrowed. */
const NODE_STATUSES = [
  "pending",
  "running",
  "paused",
  "completed",
  "failed",
  "aborted",
  "skipped",
] as const;

/** The frame's status, or `undefined` for absent, empty, or a word this client
 *  does not know.
 *
 *  An unrecognised status is DROPPED rather than written: the field is a typed
 *  union every renderer switches on, so a new upstream word landing in it would
 *  reach those switches with no case. Dropping leaves the node's previous status
 *  and the next refetch carries the truth. */
function nodeStatus(v: string | undefined): RunNode["status"] | undefined {
  return NODE_STATUSES.find((s) => s === v);
}

/** Re-read a run from the server. Safe to call on every SSE frame: a second call while a
 *  fetch is in flight sets a flag rather than issuing a request, and one trailing fetch
 *  runs when the first settles. `cause` names WHY, for a caller that can say two
 *  invalidations are the same event; the default is uncaused, which always fetches,
 *  because an SSE frame is its own cause and must not be swallowed by an earlier one. */
export function invalidateRun(workflowID: string, cause = ""): void {
  if (workflowID === "") {
    return;
  }
  if (cause !== "" && answeredCause.get(workflowID) === cause) {
    return;
  }
  if (inFlight.has(workflowID)) {
    stale.set(workflowID, cause);
    return;
  }
  void fetchRun(workflowID, cause);
}

/** Re-read every run this client holds state for. The gap-recovery half of the push
 *  contract: `run_progress` frames are APPLIED rather than refetched, so an outage that
 *  swallows them leaves a node reading `running` with its clock ticking and nothing to
 *  notice it. Bounded by the cache, collapsed by `invalidateRun`'s in-flight guard;
 *  `cause` is the caller's token, see `answeredCause`. */
export function invalidateCachedRuns(cause = ""): void {
  for (const id of cells.keys()) {
    invalidateRun(id, cause);
  }
}

async function fetchRun(workflowID: string, cause = ""): Promise<void> {
  inFlight.add(workflowID);
  // Recorded at ISSUE, which is what lets ONE rule in `invalidateRun` serve both cases:
  // a same-cause invalidation arriving while this read is open is already covered.
  if (cause !== "") {
    answeredCause.set(workflowID, cause);
  }
  let answered = false;
  // The status of a read that produced nothing, which is what tells a SETTLED answer from
  // the absence of one: `handleRun` answers 404 only where the engine described this run
  // and refused, so no number of retries can change it. 0 (no request) and every other
  // status are worth re-asking. The OrError variant is here for exactly this — the
  // collapsing `apiGet` answers null for a 404, a 502 and a dead network alike.
  let failed = 0;
  try {
    const r = await apiGetOrError<RawRunInspect>(`/api/runs/${encodeURIComponent(workflowID)}`);
    const d = r.data;
    if (d?.state !== undefined) {
      // The plan BEFORE the state, because the state assignment is what wakes
      // every reader: a subscriber that re-rendered between the two would draw a
      // repeat's bound from the previous plan.
      if (d.nodePlan === undefined) {
        plans.delete(workflowID);
      } else {
        plans.set(workflowID, d.nodePlan);
      }
      cell(workflowID).value = classifyRunState(d.state);
      answered = true;
    } else {
      failed = r.status;
    }
  } finally {
    inFlight.delete(workflowID);
    if (answered) {
      cancelRunRetry(workflowID);
    } else {
      if (cause !== "") {
        // The read produced nothing, so it claims nothing: a cause standing over an
        // answer nobody got would turn the gap's own recovery into a no-op.
        answeredCause.delete(workflowID);
      }
      if (failed === RUN_GONE_STATUS) {
        // CANCELLED rather than merely not armed: a rung an earlier transient armed is
        // still due, and the answer it would collect is this one.
        cancelRunRetry(workflowID);
      } else {
        scheduleRunRetry(workflowID, cause, failed);
      }
    }
  }
  const next = stale.get(workflowID);
  if (next !== undefined) {
    stale.delete(workflowID);
    await fetchRun(workflowID, next);
  }
}

/** Re-ask for a run whose read produced nothing, bounded.
 *
 *  It re-enters through `invalidateRun` under the ORIGINAL cause rather than `""`, so the
 *  coalescing and the cause discipline are the ones every other reader gets: the `finally`
 *  above has already dropped `answeredCause`, so a cause cannot be swallowed by its own
 *  failed attempt, while `""` would be unswallowable by anything — wrong for the gap,
 *  where one token is shared by two readers a round trip apart.
 *
 *  `status` is the newest failure's, carried for the exhaustion line alone: reading the run
 *  through the OrError variant is what makes a settled answer visible at the decision above,
 *  and it logs nothing, so without this the one line the ladder does write would not say
 *  which failure it gave up on. */
function scheduleRunRetry(workflowID: string, cause: string, status: number): void {
  // The CONTINUATION as well as the door: its one caller is that `finally`, reached by the
  // first failed read and by every rung's, so the count is KEPT rather than reset, or the
  // ladder would have no end.
  const ladder = runRetries.get(workflowID) ?? { timer: undefined, attempts: 0 };
  if (ladder.attempts >= RUN_RETRY_LIMIT) {
    // No toast: a background chat's run is invisible either way, so a failure to re-read
    // it earns a log line rather than an overlay over whatever the reader is doing.
    console.warn(
      `[run] gave up re-reading ${workflowID} after ${String(RUN_RETRY_LIMIT)} retries (last status ${String(status)}); its card keeps what it last showed`,
    );
    runRetries.delete(workflowID);
    return;
  }
  if (ladder.timer !== undefined) {
    // A trailing fetch can fail while a rung is already armed. The newest failure owns the
    // rung, and the count it inherits is what keeps the pair inside the same three.
    clearTimeout(ladder.timer);
  }
  const delay = RUN_RETRY_BASE_MS * 2 ** ladder.attempts;
  ladder.attempts++;
  ladder.timer = setTimeout(() => {
    ladder.timer = undefined;
    invalidateRun(workflowID, cause);
  }, delay);
  runRetries.set(workflowID, ladder);
}

/** Forget a run's ladder, rungs and all: a read that ANSWERED has nothing left to retry, a
 *  read the server SETTLED has nothing left to learn, and a forgotten run has nothing left
 *  to read. */
function cancelRunRetry(workflowID: string): void {
  const timer = runRetries.get(workflowID)?.timer;
  if (timer !== undefined) {
    clearTimeout(timer);
  }
  runRetries.delete(workflowID);
}

/** Externally-owned reasons a run's state cell must be KEPT, registered by the
 *  composition root so this module stays a leaf — importing `tabs.ts` here would invert
 *  the dependency direction, which is why `store.ts`'s eviction exemptions take the same
 *  shape. Nothing registered means nothing demands a cell. */
const stateDemands: ((workflowID: string) => boolean)[] = [];

/** Register one demand predicate. Returns its unregister. */
export function registerRunStateDemand(fn: (workflowID: string) => boolean): () => void {
  stateDemands.push(fn);
  return () => {
    const i = stateDemands.indexOf(fn);
    if (i >= 0) {
      stateDemands.splice(i, 1);
    }
  };
}

/** Forget a run's cached state — the cache's ONLY bound, held back by a REGISTERED
 *  DEMAND: any predicate answering true keeps everything below, because no call site can
 *  enumerate this store's readers. A refused forget is NOT retried, so a demanded cell
 *  lives as long as the page — which is why a predicate asks about state that is still
 *  live rather than about a surface that once existed. vibekit-client.md, "The run
 *  store". */
export function forgetRun(workflowID: string): void {
  if (stateDemands.some((fn) => fn(workflowID))) {
    return;
  }
  cells.delete(workflowID);
  stale.delete(workflowID);
  answeredCause.delete(workflowID);
  cancelRunRetry(workflowID);
  plans.delete(workflowID);
  controlCells.delete(workflowID);
  controlsInFlight.delete(workflowID);
  controlsStale.delete(workflowID);
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

// ---------------------------------------------------------------------------
// What may be done to a run: `GET /api/runs/{id}/controls`.
//
// A SECOND cell rather than a field on the state above, because it is a second
// fetch on its own clock: the state is re-read on every gap and shape change,
// while the answer here turns over only when the run reaches a terminal status.
// Signal-backed for the state cell's reason — the run page repaints from it, and
// the answer arrives after the first paint.
//
// It is the server's answer verbatim, and nothing here re-derives any part of
// it. The rule needs the run's status, its parentage and whether anything hosts
// it, and this process can see only the first; the previous client-side copy read
// parentage off an event-fed map that is empty after a reload, so a chat-parented
// run was classified parentless and drew a row it should not have had.
// ---------------------------------------------------------------------------

const controlCells = new Map<string, Signal<RunControlsResponse | undefined>>();
const controlsInFlight = new Set<string>();
const controlsStale = new Set<string>();

function controlCell(workflowID: string): Signal<RunControlsResponse | undefined> {
  let c = controlCells.get(workflowID);
  if (c === undefined) {
    c = signal<RunControlsResponse | undefined>(undefined);
    controlCells.set(workflowID, c);
  }
  return c;
}

/** Subscribe to what a run offers. `undefined` until the first fetch resolves,
 *  which renders no row rather than guessing one — the same rule the old table
 *  applied to an unknown status, now covering the moment before the answer
 *  lands. */
export function runControls(workflowID: string): RunControlsResponse | undefined {
  return controlCell(workflowID).value;
}

/** Re-read what a run offers. THREE triggers, never one per repaint: a tab open
 *  (`run-view.ts`), that run's own `run_finished` (`handlers/run.ts`), and a retry
 *  that succeeded (`actions/runs.ts`).
 *
 *  Coalesced with a TRAILING refetch, the state cell's discipline: any two CAN
 *  coincide, and the retry one is fired by a CLICK, so it is the likeliest to land
 *  inside another read's window. A run ending inside the tab-open read's window is
 *  the moment the answer changes, so dropping it would leave a pre-terminal verb row
 *  with nothing left to re-ask. A failed fetch leaves the previous answer standing. */
export function invalidateRunControls(workflowID: string): void {
  if (workflowID === "") {
    return;
  }
  if (controlsInFlight.has(workflowID)) {
    controlsStale.add(workflowID);
    return;
  }
  void fetchRunControls(workflowID);
}

async function fetchRunControls(workflowID: string): Promise<void> {
  // Claimed HERE rather than by the caller, so the trailing call below re-arms the
  // guard with no window a coincident invalidation could slip a third request into.
  controlsInFlight.add(workflowID);
  try {
    const d = await apiGetTyped(
      `/api/runs/${encodeURIComponent(workflowID)}/controls`,
      decodeRunControlsResponse,
    );
    if (d !== null) {
      controlCell(workflowID).value = d;
    }
  } finally {
    controlsInFlight.delete(workflowID);
  }
  if (controlsStale.delete(workflowID)) {
    await fetchRunControls(workflowID);
  }
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
export interface LiveRunRow {
  readonly chat: string;
  readonly executing: boolean;
}

/** A row handed OUT, carrying the workflow id the map holds it under. The id is the
 *  map's KEY rather than a field of the row, so a reader given the row alone cannot
 *  name the run it describes. */
export interface LiveRunEntry extends LiveRunRow {
  readonly id: string;
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
 *  of the five callers knows: vibekit-client.md "The run store". */
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
 *  effect where the two booleans' are not: vibekit-client.md "The run store". A
 *  reader takes the whole row, `executing` included: the fetched cell can be absent
 *  for a run this client saw no frames for, and the row is then the only thing that
 *  says anything about it. */
export function liveRunsForChat(chatID: string): LiveRunEntry[] {
  touch(liveRunsVersion);
  if (chatID === "") {
    return [];
  }
  const out: LiveRunEntry[] = [];
  for (const [id, r] of liveRunChats) {
    if (r.chat === chatID) {
      out.push({ id, ...r });
    }
  }
  return out;
}

/** This run's live row WITHOUT subscribing, or `undefined` when nothing holds it live.
 *  `peekRunState`'s twin, and for its reason: a `forgetRun` demand predicate runs outside
 *  any effect of its own — sometimes inside another module's — so a tracked read there
 *  would hand that effect a dependency on the whole inventory. */
export function peekLiveRun(workflowID: string): LiveRunRow | undefined {
  return liveRunChats.get(workflowID);
}

/** The ids alone, for a caller that asks only how many or which. */
export function liveRunIDsForChat(chatID: string): string[] {
  return liveRunsForChat(chatID).map((r) => r.id);
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

/** Rebuild the inventory from the server. A FAILED fetch keeps the event-fed state: a
 *  stale exemption costs memory, a wrongly-evicted live chat costs correctness, and the
 *  next gap or boot retries. `cause` is threaded into each row's own invalidation, so a
 *  gap that also ran `invalidateCachedRuns` re-reads each run once rather than twice,
 *  and a run that FINISHED during the outage is still re-read by that pass. */
export async function rebuildLiveRuns(cause = ""): Promise<void> {
  const d = await apiGetTyped("/api/runs/live", decodeLiveRunsResponse);
  if (d === null) {
    return;
  }
  adoptLiveRuns(d.runs, cause);
}

/** Adopt an inventory somebody else already read.
 *
 *  The three seeds per row are what a reload would otherwise lose — vibekit-client.md
 *  "The run store". The per-row `invalidateRun` is the one thing a caller opts into, by
 *  PASSING a cause rather than by passing a non-empty one: a gap threads `""`-or-token
 *  through legitimately and `invalidateRun(id, "")` is legal, so gating on `!== ""`
 *  would silently stop the gap door invalidating.
 *
 *  The clear and the repopulation are ONE synchronous pass under ONE bump, which is what
 *  keeps this from being a transient-empty fold for `chat-run-dots.ts`. */
export function adoptLiveRuns(rows: readonly LiveRun[], cause?: string): void {
  liveRunChats.clear();
  for (const r of rows) {
    if (r.workflow_id !== "") {
      liveRunChats.set(r.workflow_id, { chat: r.chat_id, executing: r.executing });
      noteRunChat(r.workflow_id, r.chat_id);
      noteRunKnown?.(r.workflow_id);
      if (cause !== undefined) {
        invalidateRun(r.workflow_id, cause);
      }
    }
  }
  // ONE bump for the whole adoption rather than one per row.
  bumpLiveRuns();
}

/** Take the inventory off the connect handshake, and fall back to the fetch when the
 *  frame does not state one.
 *
 *  No per-run invalidation, which is the point: C2's floor paints the square from THIS
 *  frame for an executing run, so the `inspect` is a refinement rather than a
 *  precondition. `live_runs_stated === false` means the list was WITHHELD rather than
 *  empty, and an empty list is otherwise indistinguishable from "no runs are live". */
export function adoptConnectRuns(p: ConnectedPayload): void {
  if (!p.live_runs_stated) {
    void rebuildLiveRuns("connect");
    return;
  }
  adoptLiveRuns(p.live_runs ?? []);
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
    switch (n.status) {
      case "completed":
      case "skipped":
        done++;
        break;
      case "failed":
      case "aborted":
        failed++;
        break;
      case "running":
      case "paused":
      case "unknown":
        if (current === 0) {
          current = i + 1;
        }
        break;
      case "pending":
        break;
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
    } else {
      switch (n.status) {
        case "running":
        case "paused":
        case "unknown":
          running = true;
          break;
        case "pending":
        case "completed":
        case "failed":
        case "aborted":
        case "skipped":
          break;
      }
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
  const status = state?.status;
  return status === undefined ? false : runStatusActive(status);
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

/** Only a class whose `pauseReason` does NOT already name its cause earns a label.
 *  `continuation-exhausted`'s reason states the attempt count itself, so labelling
 *  it would stutter; it renders its code bare. */
const PAUSE_CLASS_LABEL: Readonly<Record<string, string>> = {
  "transient-error": "after a transient error",
};

/** `class` is arbitrary wire text, and a bare index read on an object literal answers
 *  `Object.prototype`'s member for `constructor`/`toString`/… — which would render a
 *  function's source into the run card's alert. Own membership is the question. */
function pauseClassLabel(cls: string): string | undefined {
  return Object.hasOwn(PAUSE_CLASS_LABEL, cls) ? PAUSE_CLASS_LABEL[cls] : undefined;
}

/** The pause's machine detail as one phrase, or undefined when there is none.
 *  Upstream 2.21.1 made `pauseDetail.class` a two-member enum, and both render
 *  sites had folded the single member into prose — so an exhausted continuation
 *  budget read as "a transient error", which it is not and which points the
 *  reader at the wrong next action. An unrecognised class claims nothing.
 *  An ABSENT class takes the transient label: that is every pre-2.21.1 engine's
 *  wire, and the field is optional. Rule: `vibekit-acp.md` single-member enums. */
export function pauseDetailPhrase(detail: RunState["pauseDetail"]): string | undefined {
  const code = detail?.code;
  if (code === undefined || code === "") {
    return undefined;
  }
  const cls = detail?.class;
  const label = pauseClassLabel(cls === undefined || cls === "" ? "transient-error" : cls);
  return label === undefined ? `(${code})` : `${label} (${code})`;
}
