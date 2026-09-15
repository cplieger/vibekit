// The server→client half of the transport: `@cplieger/sse`'s client wrapped
// around vibekit's bus. The library owns the connection (resume cursor, backoff, the
// silence watchdog, the hidden-tab close, hold-and-drain around a wake); this module
// owns the envelope decode into the bus, the digest subjects' version map, the
// `revalidate` body that turns a digest answer into refetches, and the boot hydration
// gate.
//
// Who holds the connection is the library's `attachToWorker` ladder: one SharedWorker
// per browser profile (sse-worker.ts) that this tab attaches to over a port, or this
// tab's own stream when no worker can be had. Under the host the digest is the host's,
// one per profile, and the run this tab receives carries its verdict.

import {
  type DigestResult,
  type Frame,
  type LifecycleEvent,
  type Removed,
  type RevalidateContext,
  type SharedWorkerLike,
  type State,
  type TabAttachment,
  type TabFallback,
  type TabRevalidateContext,
  attachToWorker,
  createDigestClient,
  createStream,
} from "@cplieger/sse";

import { registerCleanup } from "./actions/index.js";
import { BUS_PAGE_RESUMED, BUS_RECONCILE, decodeEnvelope, emitBus } from "./bus.js";
import { invalidateCachedRuns, rebuildLiveRuns } from "./run-store.js";
import { setSSEStatus } from "./send-state.js";
import { fetchCatalog } from "./session-catalog.js";
import { get } from "./store.js";
import { loadList, loadMessages } from "./store-load.js";
import { adoptSubscriptionTag, persistedTag } from "./sse-tag.js";
import { observeStamp, setObserveSink, versionMap } from "./subject-versions.js";
import { listTabs } from "./tabs-sync.js";
import type { ConnectionStatus, ServerEvent, SubjectStamp } from "./types.js";

type MsgHandler = (evt: ServerEvent) => void;
type StatusHandler = (s: ConnectionStatus) => void;

/** The digest's own budget, matching the server's `RouteTimeout` on `POST /api/sync`. */
const DIGEST_TIMEOUT_MS = 10_000;

/** How long the hydration gate waits for `markHydrated` before releasing what it held
 *  anyway. Generous, because it is racing three sequential HTTP round-trips (settings,
 *  whoami, the chat list) on a cold container, and the cost of expiring early is only
 *  that the store's own missing-session guards drop the frames — which is exactly the
 *  behaviour the gate replaced. */
const HYDRATE_TIMEOUT_MS = 20_000;

/** Ceiling on the held queue. A busy workspace's connect hook is a handful of frames,
 *  so reaching this means hydration is not coming and the stream should move rather
 *  than grow a buffer without bound. */
const MAX_PENDING_FRAMES = 2000;

let onMsg: MsgHandler = () => {
  /* noop */
};
let onStatus: StatusHandler = () => {
  /* noop */
};

/** This tab's seat: attached to the profile's worker host, or running the per-tab
 *  stream when the ladder gave up on the worker. */
let attachment: TabAttachment | null = null;

/** The SSE-Client tag this tab presents for the profile, whichever seat it holds. */
let presented = "";

/** A worker spawner standing in for `new SharedWorker(...)`, so a suite can attach this
 *  tab to a host it holds in the page. */
let spawnOverride: (() => SharedWorkerLike) | null = null;

/** Whether the chat store has been populated, so a frame can find the chat it names. */
let hydrated = false;
/** Frames that arrived before hydration, in arrival order. */
let pending: ServerEvent[] = [];
/** Watchdog that opens the gate if hydration never reports in. */
let hydrateTimer: ReturnType<typeof setTimeout> | null = null;

/** Numbers the revalidations, so a run's two run-store readers share a token no other
 *  run can match; `run-store.ts` `answeredCause` owns what the token means. */
let runSeq = 0;

const digest = createDigestClient({ url: "/api/sync", timeoutMs: DIGEST_TIMEOUT_MS });

function statusOf(kind: string): ConnectionStatus {
  switch (kind) {
    case "connecting":
      return "connecting";
    case "open":
      return "connected";
    default:
      return "disconnected";
  }
}

function onLifecycle(ev: LifecycleEvent): void {
  switch (ev.kind) {
    case "state":
      onStatus(statusOf(ev.to));
      return;
    case "frame_rejected": {
      const msg = ev.error instanceof Error ? ev.error.message : String(ev.error);
      console.error(`sse: decoder rejected ${ev.type}:`, msg);
      return;
    }
    default:
      console.debug("[sse]", JSON.stringify(ev));
  }
}

// --- The hydration gate ---

/** Hold every incoming frame until the chat store is populated, then release them in
 *  arrival order. The connect hook's two snapshots are sent once per connection, so a
 *  frame an empty store drops has no second chance — and ORDER is load-bearing, because
 *  a `message_chunk` released before the `message_created` it extends is orphaned.
 *  INSIDE `onFrame` rather than the library's hold, which is for revalidation, and rather
 *  than starting the stream after hydration: a fresh hello has no replay, so frames
 *  published between the `GET /api/chats` response and the stream open would be lost. */
function holdUntilHydrated(): void {
  hydrated = false;
  pending = [];
  if (hydrateTimer !== null) {
    clearTimeout(hydrateTimer);
  }
  // Never wedge the stream on a hydration that failed (an auth bounce, a dead
  // /api/chats). The gate is an ordering aid, not a correctness requirement: the store's
  // missing-session guards still hold underneath it.
  hydrateTimer = setTimeout(() => {
    if (!hydrated) {
      console.warn("sse: hydration did not report in, releasing held frames");
      markHydrated();
    }
  }, HYDRATE_TIMEOUT_MS);
}

/** Open the gate and drain. Idempotent, and once open it stays open — a reconnect does
 *  not re-hold, because by then the store is populated and the fresh hello's own
 *  revalidation is the recovery path. */
export function markHydrated(): void {
  if (hydrateTimer !== null) {
    clearTimeout(hydrateTimer);
    hydrateTimer = null;
  }
  if (hydrated) {
    return;
  }
  hydrated = true;
  const held = pending;
  pending = [];
  for (const evt of held) {
    deliver(evt);
  }
}

// --- Frames ---

/** The library's `onFrame`: decode, then apply now or hold. A throw here rejects the
 *  frame — the library advances the cursor past it and schedules `revalidate("hello")`,
 *  whose digest names whatever the dropped frame would have moved, because its stamp
 *  was never observed. */
function applyFrame(frame: Frame): void {
  const evt = decodeEnvelope(JSON.parse(frame.data));
  if (!hydrated) {
    pending.push(evt);
    if (pending.length >= MAX_PENDING_FRAMES) {
      // A queue this long means hydration is not coming. Late is better than dropped,
      // and the store's own missing-session guards are the floor.
      console.warn(`sse: ${String(pending.length)} frames held, releasing early`);
      markHydrated();
    }
    return;
  }
  deliver(evt);
}

/** Dispatch one envelope to the bus and THEN observe its stamp: a version is recorded
 *  after the state it certifies is applied, never before. */
function deliver(evt: ServerEvent): void {
  if (evt.type === "subject_changed") {
    // The refused frame's projection moved and nothing carried it; the action is the
    // refetch and the stamp is deliberately NOT observed, so the digest still names it
    // if the refetch fails.
    void runStampAction(evt.subject, nextRunToken("subject_changed"));
    return;
  }
  onMsg(evt);
  if (evt.subject !== undefined && projectionHeld(evt.subject)) {
    observeStamp(evt.subject);
  }
}

/** Whether this client holds the projection a stamp certifies. A workspace-wide subject
 *  is always held; a chat's transcript (`chat`, `live_turn`) only while its window is
 *  resident, because a frame for a chat with no window was applied to nothing, and
 *  recording its version would make the digest report `changed` for — and the action
 *  refetch — a transcript nobody is looking at, refilling what the eviction sweep
 *  bounded. */
function projectionHeld(stamp: SubjectStamp): boolean {
  if (stamp.kind !== "chat" && stamp.kind !== "live_turn") {
    return true;
  }
  return get(stamp.ref)?.residency === "loaded";
}

// --- Revalidation ---

function nextRunToken(cause: string): string {
  return `${cause}:${String(++runSeq)}`;
}

/** The refetch a moved subject earns (the design's action column), as a promise the
 *  revalidation awaits. `pending` and `status` reach the client only through the connect
 *  hook, so their action is a fresh hello — the caller runs that LAST, after every other
 *  refetch has settled, which is why this returns `false` for them instead of
 *  reconnecting itself. */
function runStampAction(
  stamp: SubjectStamp | undefined,
  token: string,
  signal?: AbortSignal,
): Promise<boolean> {
  if (stamp === undefined) {
    return Promise.resolve(false);
  }
  switch (stamp.kind) {
    case "chats":
      return loadList(signal).then(() => false);
    case "tabs":
      return listTabs(signal).then(() => false);
    case "chat":
    case "live_turn":
      return loadMessages(stamp.ref, undefined, signal).then(() => false);
    case "runs":
      // The tree state a `run_progress` applies has no subject of its own, so the
      // lease set moving is the one signal that the trees may have moved too.
      invalidateCachedRuns(token);
      return rebuildLiveRuns(token, signal).then(() => false);
    case "catalog":
      return fetchCatalog(signal === undefined ? {} : { signal }).then(() => false);
    case "pending":
    case "status":
      return Promise.resolve(true);
    default:
      return Promise.resolve(false);
  }
}

/** Whether a run's cause is the page coming back, which is when the active view whose
 *  kind has no digest subject refreshes. */
function isWake(cause: RevalidateContext["cause"]): boolean {
  return cause === "visible" || cause === "pageshow" || cause === "online";
}

/** The action column over one digest verdict, whoever performed the digest. Every fetch
 *  takes `signal`, so `stop()` and the revalidation timeout cancel them and
 *  `reconnect()` does not. Resolves to whether `pending` or `status` moved. */
async function applyVerdict(
  changed: readonly State[],
  removed: readonly Removed[],
  cause: string,
  signal: AbortSignal,
): Promise<boolean> {
  const versions = versionMap();
  const token = nextRunToken(cause);
  const work: Promise<boolean>[] = [];
  const fetched = new Set<string>();
  const refetch = (kind: string, ref: string): void => {
    // `chat` and `live_turn` share one GET, and a subject named twice earns one.
    const key = `${kind === "live_turn" ? "chat" : kind}\0${ref}`;
    if (fetched.has(key)) {
      return;
    }
    fetched.add(key);
    work.push(runStampAction({ kind, ref, version: "" }, token, signal));
  };
  for (const entry of changed) {
    refetch(entry.kind, entry.ref);
  }
  for (const entry of removed) {
    versions.forget(entry);
    if (entry.kind === "chat") {
      // The chat is gone on the server: the same local drop its `chat_deleted` frame
      // would have run, through the same door.
      onMsg({ type: "chat_deleted", chat_id: "", payload: { id: entry.ref } });
    } else if (entry.kind === "live_turn") {
      // No turn is live for the chat any more. The transcript GET's `turn_open: false`
      // is what settles the client's live-turn markers, so it is the action here too.
      refetch("chat", entry.ref);
    }
  }
  // Every GET first; the hello the caller may run cancels nothing that is still in
  // flight. A loader that failed reports through its own surface and leaves its subject
  // at the old version, so the next digest names it again.
  const settled = await Promise.allSettled(work);
  return settled.some((r) => r.status === "fulfilled" && r.value);
}

/** The last step of the per-tab stream's run when `pending` or `status` moved: those
 *  two sets reach a client only through a hello's connect hook, so the stream connects
 *  again with no cursor and the fresh hello carries both. NEVER when this run is itself
 *  a hello's: that hello's hook is already behind the run, held until it settles, so its
 *  `pending` reads as moved against the snapshot it is about to deliver, and
 *  reconnecting here would discard the held snapshot and repeat forever. Under the
 *  worker host the same decision is the host's (`sse-worker-host.ts`), taken once for
 *  the profile's stream rather than once per tab. */
function helloIfMoved(ctx: RevalidateContext, moved: boolean): void {
  if (moved && ctx.cause !== "hello") {
    attachment?.reconnect({ resetCursor: true });
  }
}

/** The per-tab stream's `revalidate`: this tab owns the connection and the map it
 *  digests. */
async function revalidate(ctx: RevalidateContext): Promise<void> {
  if (isWake(ctx.cause)) {
    // FIRST: the active view whose kind has no digest subject (a run tree, a file
    // listing) refreshes on wake as it always has, and it would otherwise stay stale
    // until the reader switched tabs.
    emitBus(BUS_PAGE_RESUMED);
  }
  if (ctx.full) {
    // The map was just cleared by a new epoch: nothing held can be asked about, so the
    // whole projection is re-read. The fresh hello's own hook frames follow and carry
    // the pending and waiting-status sets.
    emitBus(BUS_RECONCILE, { cause: `full:${ctx.cause}`, signal: ctx.signal });
    return;
  }
  const versions = versionMap();
  const snapshot = versions.snapshot();
  if (snapshot.held.length === 0) {
    // Nothing to ask about, and the answer to an empty question is empty.
    return;
  }
  const result: DigestResult = await digest.check(snapshot, ctx.signal);
  if (result.kind === "must_refetch") {
    // Bind FIRST, so the loaders the reconcile runs refill the map at the new epoch
    // instead of being refused as stale.
    versions.bind(result.epoch);
    emitBus(BUS_RECONCILE, { cause: "must_refetch", signal: ctx.signal });
    return;
  }
  helloIfMoved(ctx, await applyVerdict(result.changed, result.removed, ctx.cause, ctx.signal));
}

/** The body the worker host routes to this tab (`revalidate_run`). The host digested
 *  for the profile and the context carries its verdict; this tab's map follows the
 *  host's epoch, and an epoch that dropped what this tab held makes the run full. The
 *  run's cause is the PROFILE's, so the wake refresh runs only in a visible tab. The
 *  verdict is the profile's too: the host's map is the union of every tab's stamps and
 *  forgets nothing, so a subject this tab does not hold (never loaded here, or evicted
 *  since) is dropped, not refetched; its activation refetches it anyway. A removed chat
 *  is applied whatever this tab holds, because its drop is a workspace-list fact. */
async function tabRevalidate(ctx: TabRevalidateContext): Promise<void> {
  const versions = versionMap();
  const dropped = ctx.epoch === null ? 0 : versions.bind(ctx.epoch);
  if (isWake(ctx.cause) && document.visibilityState === "visible") {
    emitBus(BUS_PAGE_RESUMED);
  }
  if (ctx.full || dropped > 0) {
    emitBus(BUS_RECONCILE, { cause: `full:${ctx.cause}`, signal: ctx.signal });
    return;
  }
  const changed = (ctx.changed ?? []).filter((entry) => versions.has(entry));
  const removed = (ctx.removed ?? []).filter(
    (entry) => entry.kind === "chat" || versions.has(entry),
  );
  await applyVerdict(changed, removed, ctx.cause, ctx.signal);
}

// --- The module's public API ---

/** Whether this page can take the worker path: the engine has SharedWorker and the
 *  build shipped a worker script. The library's own presence test is overridden so the
 *  second condition folds in. */
function workerAvailable(): boolean {
  const scope = globalThis as { readonly SharedWorker?: unknown };
  return typeof scope.SharedWorker !== "undefined" && __SSE_WORKER_URL__ !== "";
}

/** Construct the profile's worker by its content-hashed URL. The same three options on
 *  every construction, first spawn and re-spawn alike: the constructor fires `error`
 *  and does not connect when they mismatch a live worker's at the same URL and name. */
function spawnWorker(): SharedWorker {
  const url = __SSE_WORKER_URL__;
  return new SharedWorker(url, {
    name: url.slice(url.lastIndexOf("/") + 1),
    type: "classic",
    credentials: "same-origin",
  });
}

/** The per-tab stream: the ladder's `fallback`, and the whole path where no worker
 *  exists. The library seeds its `SSE-Client` from the tag and starts it. */
function createTabStream(): TabFallback {
  const versions = versionMap();
  const headers: Record<string, string> = {};
  const stream = createStream({
    url: "/api/events",
    headers,
    versions,
    onFrame: applyFrame,
    onLifecycle,
    revalidate,
    // The receipt for every keepalive received, carrying SSE-Client: what lets the
    // server read a suspended or half-open profile gone at the alive window.
    alive: { url: "/api/events/alive" },
  });
  return { stream, versions, headers };
}

export function init(msg: MsgHandler, status: StatusHandler): void {
  attachment?.detach();
  onMsg = msg;
  onStatus = (s) => {
    setSSEStatus(s);
    status(s);
  };
  holdUntilHydrated();
  presented = persistedTag();
  // Every stamp this tab records is the profile's too: under the host it reaches the
  // host's map (one digest per profile), in fallback mode the attachment's own map is
  // this one already.
  setObserveSink((subject, version, epoch) => {
    if (attachment?.mode() === "worker") {
      attachment.observe(subject, version, epoch);
    }
  });
  attachment = attachToWorker({
    supported: spawnOverride !== null || workerAvailable(),
    spawn: spawnOverride ?? spawnWorker,
    fallback: createTabStream,
    onFrame: applyFrame,
    onLifecycle,
    revalidate: tabRevalidate,
    tag: presented,
  });
}

/** The tag presented on the current connection. */
export function presentedTag(): string {
  return presented;
}

/** Adopt the tag of the push subscription this profile holds (sse-tag.ts). Called once
 *  the registration's subscription resolves, and again when the service worker reports
 *  the browser rotated it; a tag that differs from the presented one is persisted and
 *  presented from the next connect, which the attachment makes happen once, on the
 *  profile's stream or this tab's own. */
export async function adoptPushSubscription(
  sub: { readonly endpoint: string } | null,
): Promise<void> {
  await adoptSubscriptionTag(sub, presented, (tag) => {
    presented = tag;
    attachment?.setTag(tag);
  });
}

/** Undo `init` completely, for tests that boot the module more than once.
 *  `vi.resetModules()` cannot substitute in Browser Mode: the module map is URL-keyed, so
 *  a re-import hands back this instance with its stream live. */
export function _resetForTest(): void {
  attachment?.detach();
  attachment = null;
  spawnOverride = null;
  setObserveSink(null);
  if (hydrateTimer !== null) {
    clearTimeout(hydrateTimer);
    hydrateTimer = null;
  }
  onMsg = () => {
    /* noop */
  };
  onStatus = () => {
    /* noop */
  };
  hydrated = false;
  pending = [];
  runSeq = 0;
  presented = "";
}

/** Attach the next `init` to a host the suite holds instead of a SharedWorker. */
export function _spawnWorkerForTest(spawn: (() => SharedWorkerLike) | null): void {
  spawnOverride = spawn;
}

/** The two reconciliation bodies, reachable for their tests without a live stream or a
 *  live worker. */
export { revalidate as _revalidateForTest, tabRevalidate as _tabRevalidateForTest };

registerCleanup(() => {
  attachment?.detach();
});
