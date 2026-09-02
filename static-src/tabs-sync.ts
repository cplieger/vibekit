// ---------------------------------------------------------------------------
// tabs-sync: the SYNC half of the tab projection.
//
// The tab set is server-owned. `tabs.ts` holds the rows and paints them; this
// module decides WHICH frames reach it, in what order, and when a frame means
// "you have fallen behind, ask again". It holds no rows, touches no DOM, and
// knows nothing about a TabViewSpec — which is what lets the rules below be
// tested against a Set.
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
//      the version the mutation committed, and the pending-op machine CONSUMES
//      it — but consuming is not adopting: the machine compares it against the
//      watermark and never writes it there. An earlier revision had the response
//      supply the watermark, which defeats the whole mechanism: a
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
//   4. THE PENDING-OP MACHINE, which owns op correlation. Every mutation this
//      device dispatches optimistically (an adopt painted from its response, a
//      remove applied at gesture time) is a PendingOp keyed by the dispatch's
//      `op_id`, and the machine reconciles the three answers that can arrive for
//      it — the response, the echo frame, and an authoritative snapshot — in
//      whichever order the network delivers them. It is the ONE consumer of
//      `takeLocalOp`: the drain asks it whether a frame is this device's own
//      echo and forwards that answer to `apply`, so a locally dispatched
//      removal's teardown runs once rather than once per device.
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

/** The re-list cadence for a remove in `verifying`, per attempt, capped at the
 *  last entry. Bounded backoff rather than a fixed tick: a server that is down
 *  for a minute should not be asked thirty times, and the next authoritative
 *  frame or snapshot settles the op ahead of any tick anyway. */
const VERIFY_BACKOFF_MS = [1_000, 2_000, 4_000, 8_000, 16_000, 30_000] as const;

/** What the sync layer needs from the thing holding the rows.
 *
 *  Two verbs, no getters. Declared HERE, at the consumer, so the projection is
 *  not obliged to publish an interface for its own store — and so a test can
 *  satisfy it with a Set. */
export interface TabsTarget {
  /** Replace the whole projection from a snapshot. Called by the boot list and
   *  by every re-list; never by an event. The snapshot has already been
   *  version-checked AND overlaid: rows a pending remove took out are filtered,
   *  rows a pending adopt painted are merged back. */
  reset: (tabs: readonly TabSubject[]) => void;
  /** Apply ONE committed mutation, already version-checked.
   *
   *  `local` is true when this frame echoes a mutation this device dispatched.
   *  See mechanism 4 — it decides whether a removal's teardown re-dispatches. */
  apply: (delta: TabsChangedPayload, local: boolean) => void;
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
let listInFlight: Promise<boolean> | null = null;

/** op_ids this device minted, with the time they were minted. */
const localOps = new Map<string, number>();

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
 *  same op is a duplicate and must not claim local authorship twice. The
 *  pending-op machine is the one caller (mechanism 4). */
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

// ---------------------------------------------------------------------------
// The pending-op machine (mechanism 4).
//
// One record per optimistic mutation, keyed by the dispatch's opID — the one
// identifier that exists before the server has minted anything. Three states:
//
//   awaiting-response        dispatched; neither the response nor the frame has
//                            arrived. An adopt has no id yet (server-minted);
//                            a remove knows its id and captured subtree.
//   confirmed-awaiting-frame the response committed (id/committedVersion known)
//                            and the echo frame has not landed.
//   verifying                REMOVE-ONLY. The dispatch got NO answer, so the
//                            close may or may not have committed. Nothing is
//                            restored and nothing retires: the removal stays
//                            applied and one re-list runs per backoff tick until
//                            authoritative evidence arrives.
//
// Six transitions, each pinned by a test (tabs-sync.test.ts):
//
//   1. Dispatch creates the op in awaiting-response.
//   2. A frame with matching op_id confirms in ANY state, verifying included:
//      onConfirm(), retire.
//   3. Response success fills id/committedVersion, then retires immediately when
//      the watermark already covers it (localVersion >= committedVersion) OR the
//      mutation committed nothing — a close whose `closed` list is EMPTY is a
//      SEMANTIC confirmation of absence whatever the watermark says (another
//      device already closed it; this client may be arbitrarily behind), and an
//      open with `created: false` commits nothing and emits no frame. Otherwise
//      the op moves to confirmed-awaiting-frame.
//   4. Any frame or adopted snapshot that advances the watermark to
//      committedVersion or past it absorbs a confirmed-awaiting-frame op: the
//      mutation is in what the projection now holds even when correlation was
//      lost. onConfirm(), retire.
//   5. DEFINITIVE response failure — the server answered an error, so nothing
//      committed: rollback(), retire.
//   6. TIMEOUT — no answer: the op enters verifying. It settles by the FIRST of
//      a matching frame (transition 2) or an authoritative list/snapshot: row
//      PRESENT → rollback(), retire; ABSENT → onConfirm(), retire. A failed or
//      stale list keeps it verifying. Restore happens ONLY on authoritative
//      presence, because a conservative restore could resurrect a family whose
//      records a committed close already deleted.
//
// A RETIRED OP IGNORES EVERY LATER SIGNAL. Retiring deletes the record, and
// every entry point looks the op up first — so a frame-confirmed op whose
// dispatch later reports failure does NOT roll back.
//
// `onConfirm` is MODE-FREE: it is the CLIENT-LOCAL teardown only (the
// retention-off record delete is the server's close transaction). The callback
// re-checks the CURRENT projection before any chat-scoped teardown — a row with
// the same (kind, ref) open again at confirm time means the user reopened inside
// the window, and the chat's client state must survive — which is why it runs
// AFTER the frame or snapshot that settles it has been applied.
// ---------------------------------------------------------------------------

/** What the machine needs from a remove at dispatch. The full captured subtree
 *  (rows, specs, owned view state) is the projection's business and rides the
 *  `onConfirm`/`rollback` closures — this module holds no rows, so the tab ids
 *  are the whole overlap between a capture and the version rules. */
export interface PendingRemoveSpec {
  /** The tab the close names. Presence of THIS id in an authoritative list is
   *  what settles a verifying op. */
  id: string;
  /** Every tab id the reversible visual removal took out (the row and its
   *  descendants). Keys the changed-upsert suppression and the snapshot filter. */
  capturedTabIDs: readonly string[];
  /** The deferred client-local teardown. Runs exactly once, on confirmation. */
  onConfirm: () => void;
  /** Restore the captured subtree. Runs exactly once, on definitive failure or
   *  on authoritative presence. */
  rollback: () => void;
}

interface PendingAdopt {
  kind: "adopt";
  opID: string;
  state: "awaiting-response" | "confirmed-awaiting-frame";
  /** Gesture order, for the suppression override below: only an open gestured
   *  AFTER a close may resurrect a row that close captured. */
  seq: number;
  /** Server-minted, so set at response — which is why the op is keyed by opID. */
  id?: string;
  /** For snapshot merge-back. Set at response, with the id. */
  subject?: TabSubject;
  committedVersion?: number;
}

interface PendingRemove {
  kind: "remove";
  opID: string;
  state: "awaiting-response" | "confirmed-awaiting-frame" | "verifying";
  /** Gesture order; see PendingAdopt.seq. */
  seq: number;
  id: string;
  capturedTabIDs: readonly string[];
  onConfirm: () => void;
  rollback: () => void;
  committedVersion?: number;
  verifyAttempts: number;
  verifyTimer: ReturnType<typeof setTimeout> | null;
}

type PendingOp = PendingAdopt | PendingRemove;

const pendingOps = new Map<string, PendingOp>();

/** Monotonic gesture order across ops of both kinds. Which of two ops the USER
 *  performed second is a fact the op set itself cannot answer once both are
 *  pending, and the suppression override turns on it. */
let opSeq = 0;

/** Notified whenever the LAST pending remove settles. One slot, one consumer:
 *  tabs.ts, whose deferred empty-state respawn re-arms on it. */
let onRemovesSettled: (() => void) | null = null;

/** Transition 1 for an open: record the dispatch. The op has no id yet — the
 *  server mints it — so the opID is the whole correlation. */
export function beginAdopt(opID: string): void {
  pendingOps.set(opID, { kind: "adopt", opID, state: "awaiting-response", seq: ++opSeq });
}

/** Transition 1 for a close: record the dispatch and the reversible removal the
 *  caller has already applied. The capture itself stays with the caller (see
 *  PendingRemoveSpec); the machine keeps the ids and the two callbacks. */
export function beginRemove(opID: string, spec: PendingRemoveSpec): void {
  pendingOps.set(opID, {
    kind: "remove",
    opID,
    state: "awaiting-response",
    seq: ++opSeq,
    id: spec.id,
    capturedTabIDs: spec.capturedTabIDs,
    onConfirm: spec.onConfirm,
    rollback: spec.rollback,
    verifyAttempts: 0,
    verifyTimer: null,
  });
}

/** Transition 3 for an open: the response committed `subject` at
 *  `committedVersion`. `created: false` means the mutation committed NOTHING —
 *  no frame is coming, so the op retires on the spot. */
export function adoptCommitted(
  opID: string,
  subject: TabSubject,
  committedVersion: number,
  created: boolean,
): void {
  const op = pendingOps.get(opID);
  if (op?.kind !== "adopt") {
    return;
  }
  op.id = subject.id;
  op.subject = subject;
  op.committedVersion = committedVersion;
  if (!created || localVersion >= committedVersion) {
    confirmOp(op);
    return;
  }
  op.state = "confirmed-awaiting-frame";
}

/** Transition 3 for a close: the response committed `closed` at
 *  `committedVersion`. An EMPTY list is a SEMANTIC confirmation of absence —
 *  another device already closed the tab, and the local watermark may be
 *  arbitrarily behind the version that did it — so it confirms regardless of any
 *  version comparison. */
export function removeCommitted(
  opID: string,
  closed: readonly string[],
  committedVersion: number,
): void {
  const op = pendingOps.get(opID);
  if (op?.kind !== "remove") {
    return;
  }
  cancelVerify(op);
  op.committedVersion = committedVersion;
  if (closed.length === 0 || localVersion >= committedVersion) {
    confirmOp(op);
    return;
  }
  op.state = "confirmed-awaiting-frame";
}

/** Transition 5: the server ANSWERED an error, so nothing committed and the
 *  reversible removal can be honestly undone. A retired op ignores this — a
 *  frame already confirmed the mutation, and rolling back now would revert a
 *  close the collection holds. */
export function opFailed(opID: string): void {
  const op = pendingOps.get(opID);
  if (op === undefined) {
    return;
  }
  failOp(op);
}

/** Transition 6: the dispatch got NO answer, so the mutation may or may not
 *  have committed. NO restore and NO retire — the removal stays applied and the
 *  op verifies: one re-list per backoff tick until a matching frame or an
 *  authoritative list settles it. Remove-only; an adopt's dispatch carries no
 *  deadline, and nothing is painted before its response anyway. */
export function opTimedOut(opID: string): void {
  const op = pendingOps.get(opID);
  if (op?.kind !== "remove" || op.state === "verifying") {
    return;
  }
  op.state = "verifying";
  armVerify(op);
}

/** Whether any remove is pending, in ANY state — `verifying` included, because
 *  network unavailability can hold one there indefinitely and an empty strip
 *  mid-outage must not auto-respawn a chat. Read by `scheduleEmpty`'s deferral. */
export function removesPending(): boolean {
  for (const op of pendingOps.values()) {
    if (op.kind === "remove") {
      return true;
    }
  }
  return false;
}

/** Register the removes-settled notification. Called once, from the composition
 *  root (tabs.ts). Last registration wins, like `registerTabsTarget`. */
export function setOnRemovesSettled(fn: (() => void) | null): void {
  onRemovesSettled = fn;
}

/** Retire: delete the record and cancel its timer. Every later signal for this
 *  opID now finds nothing, which is what retired-op immunity IS. */
function retire(op: PendingOp): void {
  if (op.kind === "remove") {
    cancelVerify(op);
  }
  pendingOps.delete(op.opID);
}

function confirmOp(op: PendingOp): void {
  retire(op);
  if (op.kind === "remove") {
    op.onConfirm();
    noteRemoveSettled();
  }
}

function failOp(op: PendingOp): void {
  retire(op);
  if (op.kind === "remove") {
    op.rollback();
    noteRemoveSettled();
  }
}

function noteRemoveSettled(): void {
  if (!removesPending()) {
    onRemovesSettled?.();
  }
}

function cancelVerify(op: PendingRemove): void {
  if (op.verifyTimer !== null) {
    clearTimeout(op.verifyTimer);
    op.verifyTimer = null;
  }
}

/** One timer per verifying remove, re-armed after each attempt and canceled on
 *  every settlement path (retire clears it). Each tick issues ONE re-list; a
 *  failed or stale answer leaves the op verifying and the next tick asks again,
 *  while a successful one settles it inside `readList` before the re-arm check
 *  runs. */
function armVerify(op: PendingRemove): void {
  const delay = VERIFY_BACKOFF_MS[Math.min(op.verifyAttempts, VERIFY_BACKOFF_MS.length - 1)] ?? 0;
  op.verifyTimer = setTimeout(() => {
    op.verifyTimer = null;
    op.verifyAttempts++;
    void relist().finally(() => {
      if (pendingOps.get(op.opID) === op && op.state === "verifying") {
        armVerify(op);
      }
    });
  }, delay);
}

/** Whether this frame echoes a mutation this device dispatched: the marked-op
 *  set (consumed — a duplicate frame must not claim authorship twice) or a
 *  pending op, which by construction was dispatched here. */
function frameIsLocal(opID: string | undefined): boolean {
  const marked = takeLocalOp(opID);
  return marked || (opID !== undefined && pendingOps.has(opID));
}

/** The changed-upsert suppression, keyed by the pending removes' captured TAB
 *  IDS and never by ref: a frame re-upserting a row the reversible removal took
 *  out (a pin committed before the close, delivered after the gesture) must not
 *  resurrect it, while a remote device reopening the same chat mints a NEW tab
 *  id, which must paint unsuppressed.
 *
 *  The LOCAL reopen inside the window is the override — open wins locally —
 *  and it is deliberately narrow: the adopt must have been gestured AFTER the
 *  newest remove that captured the id (an open the user performed before the
 *  close is not a reopen, and its late echo must stay suppressed or the close's
 *  own gesture un-applies), and the frame must carry the adopt's OWN subject id
 *  (the same-id reopen against a not-yet-processed close; a reopen the server
 *  answered with a fresh id passes the capture test on its own). */
function suppressChanged(changed: TabSubject): boolean {
  let capturedBy = -1;
  for (const op of pendingOps.values()) {
    if (op.kind === "remove" && op.capturedTabIDs.includes(changed.id)) {
      capturedBy = Math.max(capturedBy, op.seq);
    }
  }
  if (capturedBy < 0) {
    return false;
  }
  for (const op of pendingOps.values()) {
    if (
      op.kind === "adopt" &&
      op.seq > capturedBy &&
      op.subject?.kind === changed.kind &&
      op.subject.ref === changed.ref &&
      op.subject.id === changed.id
    ) {
      return false;
    }
  }
  return true;
}

/** The delta as the projection should see it: `changed` stripped when a pending
 *  remove suppresses it. `removed_ids` and `order` always pass — a removal of a
 *  row the projection no longer holds is already a no-op there, and an order is
 *  a permutation over what it holds. */
function overlayFrame(delta: TabsChangedPayload): TabsChangedPayload {
  if (delta.changed === undefined || !suppressChanged(delta.changed)) {
    return delta;
  }
  const { changed: _suppressed, ...rest } = delta;
  return rest;
}

/** Transitions 2 and 4, run AFTER the frame reached the projection so a
 *  confirmation callback observes the post-apply state (the reopen re-check
 *  reads the CURRENT projection). */
function settleFrame(opID: string | undefined): void {
  if (opID !== undefined) {
    const op = pendingOps.get(opID);
    if (op !== undefined) {
      confirmOp(op);
    }
  }
  absorbCommitted();
}

/** Transition 4: every confirmed-awaiting-frame op the watermark now covers is
 *  absorbed — the mutation is part of what the projection holds, whether or not
 *  its own echo was ever correlated. */
function absorbCommitted(): void {
  for (const op of [...pendingOps.values()]) {
    if (
      op.state === "confirmed-awaiting-frame" &&
      op.committedVersion !== undefined &&
      localVersion >= op.committedVersion
    ) {
      confirmOp(op);
    }
  }
}

/** A snapshot with the pending overlay applied: rows a pending remove took out
 *  are filtered (the visual removal must survive a re-list that raced the
 *  close), and a pending adopt's subject is merged back (a stale-but-adoptable
 *  list must not unpaint a row this device committed). Both overlays apply only
 *  while the list can predate the mutation — committedVersion unknown, or past
 *  the list's version. */
function overlayList(tabs: readonly TabSubject[], version: number): TabSubject[] {
  let out = [...tabs];
  for (const op of pendingOps.values()) {
    if (
      op.kind === "remove" &&
      (op.committedVersion === undefined || op.committedVersion > version)
    ) {
      out = out.filter((t) => !op.capturedTabIDs.includes(t.id));
    }
  }
  for (const op of pendingOps.values()) {
    const subject = op.kind === "adopt" ? op.subject : undefined;
    if (
      subject !== undefined &&
      (op.committedVersion === undefined || op.committedVersion > version) &&
      !out.some((t) => t.id === subject.id)
    ) {
      out.push(subject);
    }
  }
  return out;
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
      const local = frameIsLocal(delta.op_id);
      localVersion = delta.version;
      target?.apply(overlayFrame(delta), local);
      settleFrame(delta.op_id);
    }
  } finally {
    draining = false;
  }
}

/** Read the collection and adopt it. The boot read, the answer to a detected gap,
 *  the answer to a `reorder_tabs` 409, and each verify tick are all this one
 *  call.
 *
 *  A 409 re-lists and NEVER re-sends: the exact-set check refused the order
 *  because the set moved under the drag, so the arrangement the gesture committed
 *  describes a set that no longer exists.
 *
 *  Answers whether a snapshot was ADOPTED, which is not the same as whether the
 *  request succeeded: a snapshot below the local version is discarded on purpose
 *  (mechanism 3) and answers false with nothing wrong. Boot is the one caller that
 *  reads it — there the strip is empty, so an unadopted read leaves the reader
 *  with no tabs and nothing saying why. */
export function listTabs(): Promise<boolean> {
  return relist();
}

function relist(): Promise<boolean> {
  listInFlight ??= readList().finally(() => {
    listInFlight = null;
  });
  return listInFlight;
}

async function readList(): Promise<boolean> {
  const list = await apiGetTyped("/api/tabs", decodeTabList);
  if (list === null) {
    // Unreachable or undecodable. The projection is left exactly as it stands:
    // an arrangement is re-derivable and a client that cannot read it must still
    // work with what it has. The next event that detects a gap re-lists — and a
    // failed list settles NO pending op, so a verifying remove stays verifying.
    return false;
  }
  if (list.version < localVersion) {
    // MECHANISM 3. This snapshot describes a set we are already ahead of, so
    // adopting it would remove a tab a committed mutation has already given us.
    // Discard it; the frame that advanced us past it was authoritative. A
    // discarded snapshot is NOT authoritative evidence either, so it settles no
    // verifying op.
    return false;
  }
  localVersion = list.version;

  // The snapshot is authoritative, so it settles what a frame could not.
  // Decisions and retirement happen BEFORE the reset (a settled op must not
  // overlay the snapshot it was settled by); the callbacks run AFTER it, so a
  // confirmation's reopen re-check and a rollback's restore both observe the
  // post-reset projection.
  const confirms: PendingRemove[] = [];
  const rollbacks: PendingRemove[] = [];
  for (const op of [...pendingOps.values()]) {
    if (op.kind !== "remove") {
      continue;
    }
    if (op.state === "verifying") {
      // Transition 6's settlement: membership decides, PRESENT → restore,
      // ABSENT → confirm. A verifying op has no committedVersion (no answer ever
      // came), so a version comparison cannot answer this one.
      retire(op);
      (list.tabs.some((t) => t.id === op.id) ? rollbacks : confirms).push(op);
    }
  }
  target?.reset(overlayList(list.tabs, list.version));
  for (const op of confirms) {
    op.onConfirm();
  }
  for (const op of rollbacks) {
    op.rollback();
  }
  if (confirms.length > 0 || rollbacks.length > 0) {
    noteRemoveSettled();
  }
  // Transition 4 over the snapshot: an op whose committed version the adopted
  // list covers is absorbed, correlation or not.
  absorbCommitted();
  return true;
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
 *  pending correlation and op. */
export function _resetTabsSyncForTest(): void {
  target = null;
  localVersion = 0;
  queue.length = 0;
  draining = false;
  listInFlight = null;
  localOps.clear();
  for (const op of pendingOps.values()) {
    if (op.kind === "remove") {
      cancelVerify(op);
    }
  }
  pendingOps.clear();
  opSeq = 0;
  onRemovesSettled = null;
}
