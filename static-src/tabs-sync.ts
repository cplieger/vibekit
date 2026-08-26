// ---------------------------------------------------------------------------
// tabs-sync: the SYNC half of the tab projection.
//
// The tab set is server-owned. `tabs.ts` holds the rows and paints them; this
// module decides WHICH frames reach it, in what order, and when a frame means
// "you have fallen behind, ask again". It holds no rows, touches no DOM, and
// knows nothing about a TabViewSpec — which is what lets the three rules below
// be tested against a Set.
//
// FOUR MECHANISMS, and each exists because a simpler shape was tried and lost:
//
//   1. THE VERSION RULES. The collection carries one monotonic version, bumped
//      on every committed mutation and captured in the same critical section as
//      the tabs it describes (internal/tabs.Store.List). Three rules, exhaustive:
//      at or below local is a duplicate or a stale frame and is ignored; exactly
//      one past local applies; more than one past means a frame was missed, so
//      stop applying and re-list.
//
//      ONLY AN EVENT MAY ADVANCE THE LOCAL VERSION. A command response carries
//      the version for diagnostics and callers must not adopt it. An earlier
//      revision had the response supply it, which defeats the whole mechanism: a
//      response-adopted v+2 makes another device's in-flight v+1 read as stale,
//      so it is dropped and no gap is ever detectable again.
//
//   2. ONE SERIALIZED QUEUE, in arrival order. The SSE spec dispatches frames as
//      it receives them, and the version rules are only well-defined against a
//      sequential applier: two handlers fanning out into parallel async work
//      could apply v+2 before v+1, at which point rule 1 discards the frame that
//      was actually next. The re-list is the one await inside the drain, and
//      frames that arrive during it are queued and re-tested afterwards against
//      the version the list established — which is why a gap does NOT clear the
//      queue.
//
//   3. THE STALE-SNAPSHOT GUARD. A re-list can lose a race with a local open: the
//      GET is issued, an open commits v+1, and the answer describes v. Adopting
//      it would close a tab this device just opened, which is the 2026-08-25
//      defect in new clothes. A snapshot BELOW the local version is therefore
//      discarded rather than applied; the next event or the next gap re-lists.
//
//   4. OP CORRELATION, for one question only: is this frame the echo of a
//      mutation THIS device asked for? The teardown of a locally closed tab has
//      already been dispatched, so re-dispatching it on the echo would kill work
//      twice — and on another device the same frame must run the local cleanup
//      without dispatching anything. `op_id` answers that and nothing else: no
//      TTL cache, no 409 branch, no authority over membership.
//
// WHAT THIS MODULE DELIBERATELY DOES NOT DO: it binds no SSE handler and
// subscribes to no bus. The composition root feeds it (`ingestTabsChanged`,
// `listTabs`) and registers the target, exactly the way `tabs.ts` already takes
// `setReorderCallback`. Keeping the binding out means the rules can be exercised
// without a transport.
// ---------------------------------------------------------------------------

import { apiGetTyped } from "./api-client.js";
import type { TabSubject, TabsChangedPayload } from "./types.js";
import { decodeTabList } from "./wire/decoders.gen.js";

/** How long a mutation this device dispatched stays correlatable.
 *
 *  An entry is normally consumed by its own event within a round trip. The sweep
 *  exists for the frames that never come — an idempotent open commits nothing so
 *  it emits nothing, a refused mutation emits nothing, and a connection that dies
 *  between the POST and the frame emits nothing this client will see. Without a
 *  bound the set is a slow leak keyed by a value nothing will ever match. */
const OP_TTL_MS = 60_000;

/** How long `whenOpen` waits for the frame that should carry a tab.
 *
 *  It RESOLVES on expiry rather than rejecting, and that is deliberate: every
 *  caller's continuation is an activation, and activating a tab the projection
 *  does not hold is already a no-op. Rejecting would turn a dropped frame into an
 *  unhandled rejection at ~30 call sites, which reports a failure the reader can
 *  do nothing about for a tab that is very likely on screen anyway. */
const OPEN_WAIT_MS = 10_000;

/** What the sync layer needs from the thing holding the rows.
 *
 *  Three verbs, no getters beyond `has`. Declared HERE, at the consumer, so the
 *  projection is not obliged to publish an interface for its own store — and so a
 *  test can satisfy it with a Set. */
export interface TabsTarget {
  /** Replace the whole projection from a snapshot. Called by the boot list and
   *  by every re-list; never by an event. */
  reset: (tabs: readonly TabSubject[]) => void;
  /** Apply ONE committed mutation, already version-checked.
   *
   *  `local` is true when this frame echoes a mutation this device dispatched.
   *  See mechanism 4 — it decides whether a removal's teardown re-dispatches. */
  apply: (delta: TabsChangedPayload, local: boolean) => void;
  /** Whether the projection currently holds a tab with this id. Read by
   *  `whenOpen` to answer the response-first case without waiting. */
  has: (id: string) => boolean;
}

let target: TabsTarget | null = null;

/** The version the projection reflects. Advanced by an applied event and by an
 *  adopted snapshot, and by nothing else. */
let localVersion = 0;

/** Frames waiting to be applied, in ARRIVAL order. */
const queue: TabsChangedPayload[] = [];

/** Whether the drain loop is running. One loop at a time is what makes the
 *  version rules well-defined; see mechanism 2. */
let draining = false;

/** The re-list in flight, so a gap detected while one is already running joins it
 *  rather than issuing a second GET. Boot's own list shares this slot, which is
 *  what stops an event that arrives mid-boot from fetching the collection twice. */
let listInFlight: Promise<void> | null = null;

/** op_ids this device minted, with the time they were minted. */
const localOps = new Map<string, number>();

/** Callers blocked on a tab id appearing in the projection. */
const openWaiters = new Map<string, (() => void)[]>();

/** Register the projection. Called once, from the composition root. Last
 *  registration wins, like `setReorderCallback`. */
export function registerTabsTarget(next: TabsTarget): void {
  target = next;
}

/** The version the projection reflects. Diagnostic and test-facing: nothing in
 *  the app branches on it, because every rule that consumes it is in this file. */
export function tabsVersion(): number {
  return localVersion;
}

/** Record that this device dispatched a mutation under `opID`, so its echo can be
 *  told from another device's.
 *
 *  Called at the DISPATCH SITE, never inside an action's `run()`: the actions
 *  framework re-invokes `run()` per retry attempt, so an id minted there would be
 *  fresh on every attempt and correlate nothing. */
export function markLocalOp(opID: string): void {
  sweepOps();
  localOps.set(opID, Date.now());
}

/** Whether `opID` names a mutation this device dispatched. Consuming is
 *  deliberate: one frame per committed mutation, so a second frame carrying the
 *  same op is a duplicate and must not claim local authorship twice. */
function takeLocalOp(opID: string | undefined): boolean {
  if (opID === undefined) {
    return false;
  }
  return localOps.delete(opID);
}

function sweepOps(): void {
  const cutoff = Date.now() - OP_TTL_MS;
  for (const [id, at] of localOps) {
    if (at < cutoff) {
      localOps.delete(id);
    }
  }
}

/** Hand one `tabs_changed` frame to the queue.
 *
 *  Returns nothing and never throws: a frame is a fact about the collection, and
 *  a caller has no decision to make about it. The version rules run in the drain
 *  rather than here, so arrival order is preserved even when a frame arrives
 *  while a re-list is in flight. */
export function ingestTabsChanged(delta: TabsChangedPayload): void {
  queue.push(delta);
  void drain();
}

async function drain(): Promise<void> {
  if (draining) {
    return;
  }
  draining = true;
  try {
    while (queue.length > 0) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by the loop condition
      const delta = queue.shift()!;
      if (delta.version <= localVersion) {
        // RULE 1. A duplicate, or a frame from before the snapshot we hold. The
        // emit-anyway removal the coordinator sends when a chat is gone but its
        // tab close failed is stamped at version + 1, so it lands here only for a
        // client that has already moved past it — which is the stated cost of
        // that path, not a defect in this one.
        continue;
      }
      if (delta.version > localVersion + 1) {
        // RULE 3. A frame was missed, so nothing after it can be trusted as a
        // delta: this frame's `order` may name tabs we never received and its
        // `changed` may sit on top of a set we do not hold. Stop applying and ask
        // for the set.
        //
        // The queue is NOT cleared. Frames behind this one are re-tested against
        // the version the list establishes, so the ones the snapshot already
        // contains fall out through rule 1 and a genuinely newer one still
        // applies.
        await relist();
        continue;
      }
      // RULE 2. Exactly one past local: this frame IS the next mutation.
      localVersion = delta.version;
      target?.apply(delta, takeLocalOp(delta.op_id));
      settleOpenWaiters();
    }
  } finally {
    draining = false;
  }
}

/** Read the collection and adopt it. The boot read, the answer to a detected gap,
 *  and the answer to a `reorder_tabs` 409 are all this one call.
 *
 *  A 409 re-lists and NEVER re-sends: the exact-set check refused the order
 *  because the set moved under the drag, so the arrangement the gesture committed
 *  describes a set that no longer exists. */
export function listTabs(): Promise<void> {
  return relist();
}

function relist(): Promise<void> {
  listInFlight ??= readList().finally(() => {
    listInFlight = null;
  });
  return listInFlight;
}

async function readList(): Promise<void> {
  const list = await apiGetTyped("/api/tabs", decodeTabList);
  if (list === null) {
    // Unreachable or undecodable. The projection is left exactly as it stands:
    // an arrangement is re-derivable and a client that cannot read it must still
    // work with what it has. The next event that detects a gap re-lists.
    return;
  }
  if (list.version < localVersion) {
    // MECHANISM 3. This snapshot describes a set we are already ahead of, so
    // adopting it would remove a tab a committed mutation has already given us.
    // Discard it; the frame that advanced us past it was authoritative.
    return;
  }
  localVersion = list.version;
  target?.reset(list.tabs);
  settleOpenWaiters();
}

/** Resolve when the projection holds `id`.
 *
 *  This is what makes an open resolve after its EVENT rather than after its
 *  response, and it answers both interleavings with one mechanism. Response-first
 *  (the common case): the frame has not landed, so the caller waits and its
 *  activation runs against a row that exists. Event-first, and the idempotent
 *  open where the response says `created: false`: the tab is already here, so this
 *  resolves on the spot and nothing is delayed.
 *
 *  Activation belongs in the continuation for one reason: painting is the event's
 *  job, so a continuation that ran on the response could activate a tab whose row
 *  does not exist yet. */
export function whenOpen(id: string, timeoutMs: number = OPEN_WAIT_MS): Promise<void> {
  if (target?.has(id) === true) {
    return Promise.resolve();
  }
  return new Promise<void>((resolve) => {
    let done = false;
    const settle = (): void => {
      if (done) {
        return;
      }
      done = true;
      clearTimeout(timer);
      resolve();
    };
    const timer = setTimeout(settle, timeoutMs);
    const waiting = openWaiters.get(id);
    if (waiting === undefined) {
      openWaiters.set(id, [settle]);
    } else {
      waiting.push(settle);
    }
  });
}

/** Release every waiter whose tab has arrived. Run after each applied frame and
 *  after each adopted snapshot, because either can be what brings a tab in. */
function settleOpenWaiters(): void {
  if (openWaiters.size === 0) {
    return;
  }
  for (const [id, waiting] of [...openWaiters]) {
    if (target?.has(id) !== true) {
      continue;
    }
    openWaiters.delete(id);
    for (const settle of waiting) {
      settle();
    }
  }
}

/** Reorder `items` to match `order`, which is a PERMUTATION and never a
 *  membership statement.
 *
 *  An id `order` does not name keeps its relative position among the other
 *  unnamed items and sorts LAST. Two things that must both hold, and the second
 *  is the one that was wrong before: such an item is never CLOSED, and it never
 *  lands at position 0. Reading absence as closure is what closed tabs nobody
 *  closed on the live instance; sorting an unnamed item first would put a tab the
 *  server has not told us about ahead of the strip the reader arranged.
 *
 *  Pure and generic so the rule is testable without a row: `tabs.ts` passes its
 *  rows and their ids. */
export function permute<T>(
  items: readonly T[],
  idOf: (item: T) => string,
  order: readonly string[],
): T[] {
  const remaining = new Map<string, T>();
  for (const item of items) {
    remaining.set(idOf(item), item);
  }
  const next: T[] = [];
  for (const id of order) {
    const item = remaining.get(id);
    if (item !== undefined) {
      next.push(item);
      remaining.delete(id);
    }
  }
  // Everything the order did not name, in the order it already had.
  for (const item of items) {
    if (remaining.has(idOf(item))) {
      next.push(item);
    }
  }
  return next;
}

/** @internal Test seam: drop the target, the version, the queue and every
 *  pending correlation. */
export function _resetTabsSyncForTest(): void {
  target = null;
  localVersion = 0;
  queue.length = 0;
  draining = false;
  listInFlight = null;
  localOps.clear();
  for (const waiting of openWaiters.values()) {
    for (const settle of waiting) {
      settle();
    }
  }
  openWaiters.clear();
}
