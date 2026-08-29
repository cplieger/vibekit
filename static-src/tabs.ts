// ---------------------------------------------------------------------------
// tabs.ts — the tab strip as a PROJECTION of the server-owned tab set.
//
// The tab set is one server-owned collection (internal/tabs), and this module
// renders it. It computes no membership, derives no order, and persists nothing:
// a gesture DISPATCHES a mutation and the `tabs_changed` frame that follows is
// what paints. That is the whole of the design, and everything below is a
// consequence of it.
//
// THREE OWNERS, and the split is what stops any of them drifting:
//
//   - `TabSubject` is the SHARED half (internal/vibekit/domain_tabs.go): id,
//     kind, ref, parent, pinned, owns. Persisted, transmitted, identical on every
//     connected device.
//   - `TabViewSpec` is the LOCAL half (tab-view.ts), produced from a subject by
//     the total factory in tab-materialize.ts: the view selector, the typed
//     route, the activation and teardown hooks, the icon. Never persisted.
//   - `tabs-sync.ts` decides WHICH frames reach here, in what order, and when a
//     frame means "you have fallen behind, ask again". Its `TabsTarget` is the
//     seam this file implements, and the three version rules, the serialized
//     queue, `permute` and `whenOpen` all live there rather than here.
//
// WHAT WENT, and why none of it can come back:
//
//   - `reconcileRemoteTabs` and the persistence subscriber. Membership arrived as
//     a whole-list document, so the client had to read "absent from the incoming
//     list" as "closed elsewhere" — which closed tabs nobody closed, on the live
//     instance, on 2026-08-25. Removal is now STATED per id in `removed_ids`, and
//     `order` is a permutation that never implies closure.
//   - `editorTabID` / `isEditorTabID` and the `__…__` singleton ids. Ids are
//     opaque and server-minted, so nothing branches on a prefix; `(kind, ref)`
//     names a subject and `tabIdFor` is the one lookup from one to the other.
//   - `inArrangement`, `getSavedTabState`, `restorableSingletonIDs`. There is no
//     local arrangement to publish, no snapshot to protect from a boot-time
//     overwrite, and no availability filter — a tab the server holds is open, and
//     a feature that has gone away is the server's problem to stop opening.
//   - `promoteTab`. `TabSubject.Parent` is set at open and never reassigned,
//     which is what makes a parent cycle unrepresentable; there is deliberately
//     no reparent command to spend that property on.
//
// WHAT IS STILL LOCAL, and legitimately so: the ACTIVE tab (a phone must not
// move the desktop's cursor — device-view.ts), the activity DOT (live state; a
// dot restored from a previous process would be a claim about a turn that ended
// before the page loaded), the NAME override (six run sites and two chat sites
// know a better label than a subject can carry), the pinned-ahead-of-unpinned
// PARTITION (a rendering rule over a stored order), and the DOM.
// ---------------------------------------------------------------------------

import { pushRoute } from "./router.js";
import type { Route, SettingsTab, GitTab, DocsTab } from "./router.js";
// The nine tab kinds have ONE definition and it is the Go const block in
// internal/vibekit/domain_tabs.go, emitted here by wire-codegen as a registered
// enum. It was a hand-written union derived from TAB_VIEWS' keys, which is two
// enumerations of one vocabulary in two languages with nothing holding them
// together — and the client's per-kind handling has to be TOTAL, so an unknown
// kind reaching it is exactly the failure the type exists to prevent.
import type { TabKind, TabSubject, TabsChangedPayload } from "./types.js";
// The per-kind LOCAL tables and the view contract live in tab-view.ts, which is
// DOM-free so the factory that produces a spec from a TabSubject can reach them
// without reaching this module's document. This file owns the STORE and the DOM
// that paints from them.
import { TAB_ICONS, type TabDotStatus, type TabViewSpec } from "./tab-view.js";
import { materializeTab, subagentRef, subjectForRoute } from "./tab-materialize.js";
import {
  registerTabsTarget,
  permute,
  whenOpen,
  listTabs,
  markLocalOp,
  type TabsTarget,
} from "./tabs-sync.js";
import {
  openTabCommand,
  closeTabCommand,
  pinTabCommand,
  reorderTabsCommand,
  REORDER_STALE,
} from "./actions/tabs.js";
import { newOpID } from "./transport.js";
import { join as joinKey } from "@cplieger/keyenc";
import { ICON_CLOSE, ICON_PIN_FILLED, ICON_TAB_SUBTAB } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { activeView, setActiveView } from "./device-view.js";
import { $ } from "./dom.js";
import { swapViews } from "./view-swap.js";
import { signal, effect, el } from "@cplieger/reactive";
import { attachDrag, isDragHandled, setReorderCallback } from "./tabs-drag.js";
import { showContextMenu } from "./context-menu.js";
import type { ContextMenuItem } from "./context-menu.js";
import { downloadChatExport } from "./chat-export.js";
import { BUS_TAB_CHANGED, emitBus } from "./bus.js";

// --- Types ---

/** Re-exported so the consumers that read a tab's kind keep importing it from
 *  the tab module, while the DEFINITION stays single and server-side. */
export type { TabKind };

/** Re-exported for the same reason: the definition lives in tab-view.ts with the
 *  rest of the view contract. */
export type { TabDotStatus, TabViewSpec };

/** One row of the strip: the shared half, the local half, and the two things
 *  this device is allowed to say about a tab on its own.
 *
 *  `subject` is the truth and is replaced wholesale by every frame that carries
 *  it, so nothing here can disagree with the collection. `spec` is a SNAPSHOT
 *  taken at materialization — safe because every subject fact it copies (`owns`,
 *  `parentId`) is immutable after open, which is exactly why `pinned` is not
 *  among them and is read from the subject instead.
 *
 *  `name` and `dotStatus` are the two mutable local fields, and each has a
 *  reason. A name because six run sites and two chat sites legitimately know a
 *  better label than a subject can carry (see tab-materialize.ts's header). A dot
 *  because it is LIVE state derived from a chat's or a run's current condition,
 *  which no persisted record may claim. */
interface TabRow {
  subject: TabSubject;
  spec: TabViewSpec;
  name: string;
  dotStatus?: TabDotStatus | undefined;
}

/** What `openTab` needs. `kind` plus `ref` names the subject; everything else is
 *  a choice about this particular open. */
export interface OpenTabArgs {
  kind: TabKind;
  /** A chat id, an absolute path, a run id. Empty (or absent) for a singleton,
   *  the one kind whose identity is its kind. */
  ref?: string;
  /** An already-open tab to nest under, making this one a SUB-TAB. Absent for
   *  top level. A parent that is not open promotes the tab to top level rather
   *  than refusing it — the server's rule and `insertRow`'s, for the same
   *  reason: a tab nobody can see is worse than a tab in the wrong place. */
  parent?: string;
  /** Whether closing this tab tears down what it shows. Default true.
   *
   *  `owns: false` makes it a VIEW, which is what lets a sub-tab watching work
   *  another chat owns be closed without killing that work. Not derivable from
   *  the kind: a launcher-owned run and a run REVIEW share `(kind, ref)` and
   *  differ only here. */
  owns?: boolean;
  /** A label this caller knows and a subject cannot carry. */
  name?: string;
  /** Whether to activate once the tab exists. Default true; the automatic offers
   *  (a run sub-tab a progress frame opened, a bulk restore) pass false, because
   *  the strip is the reader's. */
  activate?: boolean;
}

// --- Store ---

interface Callbacks {
  onActivate: ((id: string) => void) | null;
  onEmpty: (() => void) | null;
  /** Notified with the id of every tab that leaves the projection. A
   *  NOTIFICATION slot, like onEmpty: it must not mutate the store. */
  onClosed: ((id: string) => void) | null;
}

interface Internal {
  emptyTimer: ReturnType<typeof setTimeout> | null;
  renderQueued: boolean;
  /** Whether a tab has ever entered the projection. The DOM subscriber keys its
   *  no-op on this rather than on an empty store, because the two differ on
   *  exactly one transition: the one INTO empty. See the render effect. */
  everOpened: boolean;
}

interface State {
  tabs: TabRow[];
  active: string;
}

const state: State = { tabs: [], active: "" };
const callbacks: Callbacks = { onActivate: null, onEmpty: null, onClosed: null };
const internal: Internal = { emptyTimer: null, renderQueued: false, everOpened: false };

/** Names a caller supplied for a subject, keyed by `(kind, ref)` rather than by
 *  tab id — which is the whole point of the map rather than a field.
 *
 *  A name arrives at the DISPATCH site, before the server has minted an id and
 *  before the frame that paints the row. Keying on the subject's identity is what
 *  lets `materializeTab`'s derived default be overridden at the moment the row is
 *  BUILT, instead of the row rendering "New conversation" for a frame and then
 *  snapping to the real title. It also survives a re-list, which rebuilds every
 *  row from scratch.
 *
 *  Bounded by `forgetRow`: an entry goes when its tab leaves the projection. */
const nameOverrides = new Map<string, string>();

/** Reactive version counter. Effects subscribed via `tabsEffect()` re-run on
 *  every emit(). State is mutated in place; this counter is the signal those
 *  mutations trip. */
const stateVersion = signal(0);

/** Reactive counter for DOT writes, which deliberately do not `emit()`.
 *
 *  A second signal rather than a second emit: `emit()` queues a re-render, and a
 *  dot is not a structural change (see setTabStatus). Only `subscribeTabCues`
 *  reads this one, so a dot write reaches the out-of-page attention surfaces
 *  without waking anything else. */
const dotVersion = signal(0);

/** All registered module-level effects. Tracked so _resetForTest can dispose
 *  them and start fresh; production never disposes. */
const moduleEffects: (() => void)[] = [];

function emit(): void {
  stateVersion.value = stateVersion.peek() + 1;
}

/** Register an effect that re-runs on every state mutation. */
function tabsEffect(fn: (s: State) => void): () => void {
  const cleanup = effect(() => {
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    stateVersion.value; // subscribe
    fn(state);
  });
  moduleEffects.push(cleanup);
  return cleanup;
}

function subjectKey(kind: TabKind, ref: string): string {
  return joinKey(kind, ref);
}

function rowOfID(id: string): TabRow | undefined {
  return state.tabs.find((t) => t.subject.id === id);
}

// --- The name a row renders ---

/** The label for a subject: the caller's override when one was supplied, else
 *  the factory's derived default. One expression, so a rebuilt row cannot lose an
 *  override and an override cannot outlive its tab. */
function nameFor(subject: TabSubject, spec: TabViewSpec): string {
  return nameOverrides.get(subjectKey(subject.kind, subject.ref)) ?? spec.name;
}

function buildRow(subject: TabSubject): TabRow {
  const spec = materializeTab(subject);
  const row: TabRow = { subject, spec, name: nameFor(subject, spec) };
  if (spec.dotStatus !== undefined) {
    row.dotStatus = spec.dotStatus;
  }
  return row;
}

/** Drop everything keyed off a departed tab, and tell the one listener. Called
 *  from both removal paths (an applied `removed_ids`, and a snapshot that no
 *  longer holds the tab) so neither can leak. */
function forgetRow(row: TabRow): void {
  nameOverrides.delete(subjectKey(row.subject.kind, row.subject.ref));
  callbacks.onClosed?.(row.subject.id);
}

// --- Ordering ---

/** A row's sub-rows, in projection order. Reads the SUBJECT's parent, so the
 *  tab module needs nothing from the chat store to lay itself out. */
function childrenOf(id: string): TabRow[] {
  return state.tabs.filter((t) => t.subject.parent === id);
}

/** Insert a row at its canonical position: a sub-tab immediately after its
 *  parent's existing children, a top-level tab at the end.
 *
 *  Keeping `state.tabs` parent-anchored means the render walk and the keyboard
 *  order need no grouping logic of their own — the array already reads the way
 *  the strip looks. */
function insertRow(row: TabRow): void {
  // The one place a tab enters the projection, so the one place this is recorded.
  internal.everOpened = true;
  const parent = row.subject.parent;
  if (parent === "") {
    state.tabs.push(row);
    applyPinOrder();
    return;
  }
  const pIdx = state.tabs.findIndex((t) => t.subject.id === parent);
  if (pIdx < 0) {
    // An orphan (parent not open here) behaves as top-level rather than
    // vanishing: a tab nobody can see is worse than a tab in the wrong place.
    state.tabs.push(row);
    applyPinOrder();
    return;
  }
  let at = pIdx + 1;
  while (at < state.tabs.length && state.tabs[at]?.subject.parent === parent) {
    at++;
  }
  state.tabs.splice(at, 0, row);
}

/** Group `state.tabs` into [parent, ...its whole descendant tree] runs, in array
 *  order. Shared by the pin partition, which must move a parent and everything
 *  under it as one unit — splitting them would put a child under a stranger.
 *
 *  Membership is tested against every row already IN a group, not just each
 *  group's first element: a sub-tab can itself have one, and matching only the
 *  head made such a grandchild an orphan top-level group. */
function tabGroups(): TabRow[][] {
  const groups: TabRow[][] = [];
  for (const t of state.tabs) {
    const owner =
      t.subject.parent === ""
        ? undefined
        : groups.findLast((g) => g.some((m) => m.subject.id === t.subject.parent));
    if (owner === undefined) {
      groups.push([t]);
      continue;
    }
    owner.push(t);
  }
  return groups;
}

/** Reorder `state.tabs` so every pinned group precedes every unpinned one,
 *  stably and with each parent's children still behind it.
 *
 *  A RENDERING rule over the stored order, applied here rather than server-side:
 *  the collection keeps the order it was given and `TabSubject.Pinned` says which
 *  rows float, so an unpin leaves the tab exactly where it was.
 *
 *  In the ARRAY rather than in the render, because two mechanisms read DOM order
 *  back as the truth: a drop reads the new order out of the strip (tabs-drag.ts)
 *  and the keyboard arrows walk `el.parentElement.children`. A render-time sort
 *  would make both disagree with what is stored. It is also what makes a pinned
 *  tab undraggable below an unpinned one with no change to the drag subsystem:
 *  every drop commits through a reorder whose frame re-partitions, so an illegal
 *  drop snaps back. */
function applyPinOrder(): void {
  const groups = tabGroups();
  const pinned = groups.filter((g) => g[0]?.subject.pinned === true);
  if (pinned.length === 0 || pinned.length === groups.length) {
    return;
  }
  state.tabs = [...pinned, ...groups.filter((g) => g[0]?.subject.pinned !== true)].flat();
}

/** Expand a top-level order into the EXACT SET the server's `reorder_tabs`
 *  demands: every open tab exactly once, each parent immediately followed by its
 *  descendant tree.
 *
 *  The drag reads back top-level ids only (children are folded into their parent
 *  for the duration of a drag), and the exact-set check refuses anything else. So
 *  the expansion is not a convenience: without it every drop on a strip holding a
 *  sub-tab would be a 409. */
function expandOrder(order: readonly string[]): string[] {
  const remaining = new Map(state.tabs.map((t) => [t.subject.id, t]));
  const out: string[] = [];
  const take = (id: string): void => {
    if (!remaining.has(id)) {
      return;
    }
    remaining.delete(id);
    out.push(id);
    for (const c of childrenOf(id)) {
      take(c.subject.id);
    }
  };
  for (const id of order) {
    take(id);
  }
  // Anything the gesture did not name keeps its position at the end. Parents go
  // through take() so their children follow them rather than trailing the strip.
  for (const t of [...remaining.values()]) {
    take(t.subject.id);
  }
  return out;
}

// --- The sync target ---

/** Adopt a whole snapshot: the boot read, and the answer to a gap or a 409.
 *
 *  A snapshot is the COMPLETE set at a version tabs-sync has already checked
 *  against the local one (`readList` discards anything below it, which is what
 *  stops a stale GET closing a tab a committed mutation has just given us). So a
 *  tab absent from an adopted snapshot really is closed, and that is a different
 *  statement from the one this refactor deletes: `order` in a DELTA never implies
 *  closure, because a delta describes one mutation rather than the whole set.
 *
 *  Rows for subjects that survive are REUSED rather than rebuilt, so a name
 *  override, a dot and the spec's identity all ride through a re-list untouched —
 *  and re-running `materializeTab` on every listed tab would re-enter the lazy
 *  singleton imports for no reason. */
function reset(subjects: readonly TabSubject[]): void {
  const before = new Map(state.tabs.map((t) => [t.subject.id, t]));
  const next: TabRow[] = [];
  for (const subject of subjects) {
    const existing = before.get(subject.id);
    if (existing === undefined) {
      next.push(buildRow(subject));
      continue;
    }
    before.delete(subject.id);
    existing.subject = subject;
    next.push(existing);
  }
  state.tabs = next;
  if (next.length > 0) {
    internal.everOpened = true;
  }
  // Everything the snapshot does not hold is gone. The teardown runs with
  // `remote: true` in every case: a snapshot is not this device's mutation, so
  // re-dispatching a teardown here would kill work a second time.
  for (const row of before.values()) {
    forgetRow(row);
    tearDown(row, { remote: true });
  }
  applyPinOrder();

  // The active tab, in the two shapes a snapshot arrives in.
  //
  // BOOT (nothing active yet): point at the tab this SCREEN was last on, or at
  // the first one, WITHOUT running its `onShow`. The saved id resolves because
  // ids are server-minted and persisted with the collection, so nothing has to be
  // translated. app.ts performs the one boot activation afterwards, which is what
  // keeps a re-list from re-fetching a view's content.
  //
  // A LATER re-list that dropped the active tab: fall back to the first tab and
  // activate it properly, because the reader is looking at a view whose tab is
  // gone. `local: false` — a set this device did not author must not respawn a
  // chat here.
  if (state.active === "") {
    const saved = activeView();
    state.active = saved !== "" && hasRow(saved) ? saved : (state.tabs[0]?.subject.id ?? "");
    emit();
    return;
  }
  if (!hasRow(state.active)) {
    emit();
    activateFirst(false);
    return;
  }
  emit();
}

/** Apply ONE committed mutation, already version-checked by tabs-sync.
 *
 *  `local` is true when this frame echoes a mutation THIS device dispatched, and
 *  it decides one thing: whether a removal's teardown re-dispatches. A locally
 *  closed tab's teardown has not run yet (nothing renders or tears down
 *  optimistically), so this is where an owned run's cancel and a retention-off
 *  chat's delete actually go out — `remote: false`. The same frame on another
 *  device runs every LOCAL cleanup and dispatches nothing, or two screens would
 *  each kill the same work.
 *
 *  The three parts are applied in the order the server states them, and each is
 *  independent: `changed` is an upsert by id, `removed_ids` is the ONLY statement
 *  of closure, and `order` is a permutation. */
function apply(delta: TabsChangedPayload, local: boolean): void {
  const changed = delta.changed;
  if (changed !== undefined) {
    const existing = rowOfID(changed.id);
    if (existing === undefined) {
      insertRow(buildRow(changed));
    } else {
      // The SUBJECT is replaced wholesale — a pin, or any later field the server
      // grows — while the spec, the name override and the dot stay. Nothing here
      // may re-materialize: `owns` and `parent` are immutable after open, so the
      // spec cannot have gone stale, and rebuilding it would re-run onShow wiring
      // for a tab that only changed its pin.
      existing.subject = changed;
      existing.name = nameFor(changed, existing.spec);
      applyPinOrder();
    }
  }

  const removed = delta.removed_ids ?? [];
  let lostActive = false;
  for (const id of removed) {
    const at = state.tabs.findIndex((t) => t.subject.id === id);
    if (at < 0) {
      continue;
    }
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by the index check
    const row = state.tabs[at]!;
    // REMOVE, then tear down. The order is this module's re-entrancy guarantee
    // rather than a detail: a teardown must observe a state the tab has already
    // left, or a hook that closes its own tab recurses until the stack dies —
    // which the editor's teardown did, making every editor tab unclosable.
    state.tabs.splice(at, 1);
    if (state.active === id) {
      lostActive = true;
    }
    forgetRow(row);
    tearDown(row, { remote: !local });
  }

  const order = delta.order;
  if (order !== undefined) {
    // A PERMUTATION, through the sync layer's own rule: an id the order does not
    // name keeps its relative position and sorts LAST, and is never closed. That
    // is the property the whole refactor exists for, so it lives in one place and
    // is tested against a Set rather than against a row.
    state.tabs = permute(state.tabs, (t) => t.subject.id, order);
    applyPinOrder();
  }

  if (lostActive) {
    // An active view naming no open tab falls back to the FIRST tab, which is the
    // same rule the boot read applies.
    activateFirst(local);
  }
  emit();
}

/** Run a departed row's local teardown, if it has one and owns what it shows.
 *
 *  `owns: false` tears down nothing: the tab was a VIEW, and dismissing a view
 *  must not kill the work it was watching. */
function tearDown(row: TabRow, opts: { remote: boolean }): void {
  if (!row.spec.owns) {
    return;
  }
  row.spec.onClose?.(opts);
}

/** Whether the projection holds a tab with this id. `whenOpen`'s question, and
 *  the reason it can answer the response-first and event-first interleavings with
 *  one mechanism. */
function hasRow(id: string): boolean {
  return state.tabs.some((t) => t.subject.id === id);
}

const target: TabsTarget = { reset, apply, has: hasRow };
registerTabsTarget(target);

// --- Activation (this device's alone) ---

/** Activate an existing tab. */
export function activateTab(id: string): void {
  const row = rowOfID(id);
  if (row === undefined || state.active === id) {
    return;
  }
  state.active = id;
  setActiveView(id);
  emit();
  row.spec.onShow?.();
  callbacks.onActivate?.(id);
}

/** Fall back to the first tab, or to nothing when the strip is empty.
 *
 *  `local` gates the empty-strip respawn: a strip emptied by ANOTHER device's
 *  close must not create a chat here. Nobody asked for one, and it would then
 *  propagate back as an addition every other device has to absorb — which is the
 *  shape of the loop that minted a chat every 1.5s on the live instance. */
function activateFirst(local: boolean): void {
  const first = state.tabs[0];
  if (first !== undefined) {
    state.active = "";
    activateTab(first.subject.id);
    return;
  }
  state.active = "";
  setActiveView("");
  if (local) {
    scheduleEmpty();
  }
}

/** Point the strip at the tab this SCREEN was last on, and run its `onShow`.
 *
 *  Boot's one activation, called by app.ts after `listTabs()` resolves. `reset`
 *  has already chosen WHICH tab (the saved active view, or the first); this is
 *  what makes it load. The two are separate because `reset` also runs on every
 *  later re-list, where re-fetching the active view's content would be a round
 *  trip for nothing. */
export function activateRestoredTab(): void {
  const id = state.active;
  if (id === "") {
    return;
  }
  // Cleared so activateTab's already-active guard does not swallow the onShow
  // that is the whole point of this call.
  state.active = "";
  activateTab(id);
}

// --- Mutations: every one is a dispatch ---

/** Open a tab for something that already exists, and activate it.
 *
 *  Resolves once the tab is IN the projection, so a caller's continuation runs
 *  against a row that exists. Two interleavings, one mechanism: the response
 *  usually lands first and `whenOpen` waits for the frame that paints the row;
 *  when the frame beat it, `whenOpen` resolves on the spot.
 *
 *  Never rejects, and never throws at ~30 call sites. A refused open leaves the
 *  strip exactly as it was and the action framework has already raised its
 *  toast — including the one refusal with a remedy, which says "close a tab
 *  first" rather than reporting an error. */
export async function openTab(args: OpenTabArgs): Promise<void> {
  const ref = args.ref ?? "";
  if (args.name !== undefined && args.name !== "") {
    // BEFORE the dispatch, so the row is built with the right label rather than
    // rendering the factory's placeholder for a frame and then snapping.
    nameOverrides.set(subjectKey(args.kind, ref), args.name);
  }
  const opID = newOpID();
  markLocalOp(opID);
  const reply = await openTabCommand.dispatch({
    kind: args.kind,
    ref,
    parent: args.parent ?? "",
    owns: args.owns ?? true,
    opID,
  });
  if (reply === null) {
    return;
  }
  const id = reply.subject.id;
  // `created: false` means this mutation committed NOTHING, so no frame is
  // coming for it and the row is normally already here. The one case where it is
  // not is a tab another mutation opened whose frame is still in flight — a
  // `create_chat` that opened its own chat tab server-side is exactly that — so
  // the wait is taken when the row is missing whatever the flag says. `whenOpen`
  // resolves the moment the row lands and gives up on a bound, so neither branch
  // can hang.
  if (reply.created || !hasRow(id)) {
    await whenOpen(id);
  }
  if (args.activate !== false) {
    activateTab(id);
  }
}

/** Close a tab and its descendants, as ONE server-side mutation.
 *
 *  Nothing local happens here. The strip changes when the frame arrives, which is
 *  what makes a failed close leave the tab exactly where it was instead of a
 *  half-drawn row, and what makes the teardown run once per closed tab rather
 *  than once per device. Closing an id that is not open is not an error: two
 *  devices can close one tab. */
export async function closeTab(id: string): Promise<void> {
  const opID = newOpID();
  markLocalOp(opID);
  await closeTabCommand.dispatch({ id, opID });
}

/** Pin or unpin a top-level tab.
 *
 *  Refused locally for a SUB-TAB before it costs a round trip: its position is
 *  its parent's, exactly as with drag. */
export async function setTabPinned(id: string, pinned: boolean): Promise<void> {
  const row = rowOfID(id);
  if (row === undefined) {
    return;
  }
  if (row.subject.parent !== "" || row.subject.pinned === pinned) {
    return;
  }
  const opID = newOpID();
  markLocalOp(opID);
  await pinTabCommand.dispatch({ id, pinned, opID });
}

/** Publish the arrangement a drag committed.
 *
 *  ON COMMIT ONLY, never per pointer move: an order is a whole-collection write,
 *  so a per-frame publish would be an fsync and a broadcast per pixel.
 *
 *  A 409 re-lists and NEVER re-sends. The exact-set check refused because the set
 *  moved under the drag, so the arrangement the gesture committed describes a
 *  collection that no longer exists — re-sending it would refuse again, and the
 *  honest answer is the current set with the drag snapped back. */
function publishReorder(order: readonly string[]): void {
  const expanded = expandOrder(order);
  void (async () => {
    const opID = newOpID();
    markLocalOp(opID);
    const outcome = await reorderTabsCommand.dispatch({ order: expanded, opID });
    if (outcome === REORDER_STALE) {
      await listTabs();
    }
  })();
}

setReorderCallback(publishReorder);

// --- Local writers ---

/** Override a tab's label.
 *
 *  Recorded against the SUBJECT as well as the row, so a re-list keeps it: a
 *  snapshot rebuilds rows from the factory, whose derived default is the chat
 *  store's name — which is the same value in the normal case and a placeholder
 *  for a chat this client holds no row for. */
export function renameTab(id: string, name: string): void {
  const row = rowOfID(id);
  if (row === undefined || row.name === name) {
    return;
  }
  nameOverrides.set(subjectKey(row.subject.kind, row.subject.ref), name);
  row.name = name;
  emit();
}

/** What each dot state is CALLED. This is not decoration: the dot is a 9px
 *  graphical object, and a screen-reader user gets nothing at all from it, so
 *  the phrase is the state's only channel for them — which matters most here,
 *  because the whole feature exists for tabs you are not looking at.
 *
 *  One table feeding both the announced name and the hover tooltip, so what a
 *  sighted user reads and what a screen reader hears cannot drift. Exhaustive
 *  over TabDotStatus by type, so a new state cannot ship unnamed. */
const DOT_PHRASE: Readonly<Record<TabDotStatus, string>> = {
  idle: "idle",
  working: "working",
  waiting: "waiting for you",
  input: "needs a decision",
  // "operation" rather than "turn": the latch behind this state is set for every
  // `error` frame naming the chat, which includes `switch_failed` and
  // `bridge_start_failed` — failures with no turn in them. The breadth is
  // deliberate and useful, so the phrase is what had to widen. It is the only
  // channel a screen-reader user has here, so it must not claim more than its
  // producer supports.
  failed: "last operation failed",
  done: "turn finished",
  dirty: "unsaved changes",
};

const CLS_DOT = "tab-status-dot";
const CLS_DOT_SR = "tab-status-sr";

function elementOf(id: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(`[data-tab-id="${CSS.escape(id)}"]`);
}

/** Paint one tab's dot: the attribute CSS keys off, the tooltip a pointer
 *  reveals, and the word a screen reader hears.
 *
 *  The announced word is a SEPARATE element after `.tab-name`, not a child of
 *  the dot, and the position is the reason. A tab's accessible name is computed
 *  from its contents, and the dot is the leading element on a chat row — so a
 *  word inside it would announce "working Fix the parser" rather than "Fix the
 *  parser, working".
 *
 *  An empty status removes the attribute rather than setting it empty, so
 *  `[data-status]` alone is the CSS reveal condition and there is no second flag
 *  to keep in sync. */
function paintDot(row: HTMLElement, status: TabDotStatus | ""): void {
  const dot = row.querySelector<HTMLElement>(`.${CLS_DOT}`);
  const sr = row.querySelector<HTMLElement>(`.${CLS_DOT_SR}`);
  if (dot === null || sr === null) {
    return;
  }
  if (status === "") {
    dot.removeAttribute("data-status");
    dot.removeAttribute("title");
    sr.textContent = "";
    return;
  }
  const phrase = DOT_PHRASE[status];
  dot.dataset["status"] = status;
  dot.title = phrase;
  sr.textContent = `, ${phrase}`;
}

/** Set a chat tab's activity dot. The state is derived by `tabStatusFor`
 *  (store.ts), which owns the precedence; this is the writer.
 *
 *  Records the value on the ROW before painting, so a row rebuilt later starts
 *  from the real state instead of the factory's seed. Deliberately does NOT
 *  `emit()`: a dot is not a structural change, so it must not queue a re-render,
 *  and the paint below is the whole visible effect. */
export function setTabStatus(id: string, status: TabDotStatus | ""): void {
  recordDotStatus(id, status);
  const node = elementOf(id);
  if (node === null) {
    return;
  }
  paintDot(node, status);
}

/** Mark an editor tab as having unsaved changes (a steady accent disc).
 *  Reuses the shared .tab-status-dot on the ONE attribute setTabStatus writes,
 *  which is what makes the two halves mutually exclusive by construction rather
 *  than by convention. */
export function setTabDirty(id: string, dirty: boolean): void {
  setTabStatus(id, dirty ? "dirty" : "");
}

/** Park a dot state on its row. "" means no state, which is an ABSENT field
 *  rather than an empty string, so `createTabEl` can tell "nothing was ever
 *  painted" from "painted, then cleared" with one `?? default`. */
function recordDotStatus(id: string, status: TabDotStatus | ""): void {
  const row = rowOfID(id);
  if (row === undefined) {
    return;
  }
  const before = row.dotStatus;
  if (status === "") {
    delete row.dotStatus;
  } else {
    row.dotStatus = status;
  }
  // Only a CHANGED dot moves the attention surfaces, and the guard is what keeps
  // the store effect's sweep over every open chat (chat.ts) from waking the fold
  // once per tab on a change that touched one of them. The PAINT is deliberately
  // unguarded: an unchanged state still has to be re-applied to a row that was
  // rebuilt since the last write.
  if (before !== row.dotStatus) {
    dotVersion.value = dotVersion.peek() + 1;
  }
}

/** Set (or clear, with "") a tab's hover tooltip. Used for the agent's
 *  self-declared "what I'm working on" description on chat tabs. Direct DOM write
 *  like setTabStatus — reapplied by the store effect after any re-render, so
 *  transient loss on rebuild self-heals the same way the status dot does. */
export function setTabTooltip(id: string, text: string): void {
  const node = elementOf(id);
  if (node === null) {
    return;
  }
  if (text === "") {
    node.removeAttribute("title");
  } else {
    node.title = text;
  }
}

// --- Lookups ---

/** Whether a tab is open for this subject.
 *
 *  Keyed by `(kind, ref)` rather than by id, and that re-key is the point: ids
 *  are opaque and server-minted, so a consumer holding a chat id or a path can no
 *  longer construct one. A singleton's ref is empty. */
export function hasTab(kind: TabKind, ref = ""): boolean {
  return tabIdFor(kind, ref) !== "";
}

/** The open tab's id for this subject, or "" when none is open.
 *
 *  ONE lookup for every consumer that holds a chat id, a path or a run id and
 *  needs to reach an id-keyed writer (`activateTab`, `setTabStatus`,
 *  `renameTab`). Without it each of them would re-implement the scan, which is
 *  exactly how `editorTabID` came to be composed by hand in three modules. */
export function tabIdFor(kind: TabKind, ref = ""): string {
  return state.tabs.find((t) => t.subject.kind === kind && t.subject.ref === ref)?.subject.id ?? "";
}

/** The open tab's id for the subject a URL route names, or "" when none is open.
 *
 *  What a BACK or FORWARD press has to ask before it applies a route: a history
 *  entry names a location this browser was at, which is not the same thing as a
 *  location that still exists. Answering "" is the whole signal — the router
 *  redirects rather than opening the tab the entry names.
 *
 *  Here rather than in app.ts because the projection is what knows, and both
 *  halves of the answer already live in this module's neighbours: the route-to-
 *  subject mapping is the factory's inverse (`subjectForRoute`) and the lookup is
 *  `tabIdFor`. */
export function tabIdForRoute(route: Route): string {
  const { kind, ref } = subjectForRoute(route);
  return tabIdFor(kind, ref);
}

export function getActiveTabId(): string {
  return state.active;
}

/** Route of the currently active tab, or null when no tab is active. Used by
 *  app.ts to canonicalize the URL to a restored non-chat tab. */
export function getActiveTabRoute(): Route | null {
  return rowOfID(state.active)?.spec.route ?? null;
}

/** Kind of the currently active tab, or null when no tab is active.
 *
 *  This is what a key binding scoped BY VIEW reads. It is the SUBJECT's kind
 *  rather than the route's, because an editor tab's kind is "editor" while its
 *  route's is "file", and a binding keyed on the route would be speaking a second
 *  vocabulary for the same question. */
export function getActiveTabKind(): TabKind | null {
  // Subscribe: the toolbar's find affordance is derived from this inside an
  // effect, and the tab SET is what decides the answer. Outside an effect this
  // read is free.
  // eslint-disable-next-line @typescript-eslint/no-unused-expressions
  stateVersion.value;
  return rowOfID(state.active)?.subject.kind ?? null;
}

/** The chat tabs and their current dot states, for the out-of-page attention
 *  fold (attention.ts). A pure projection read: the dot state is parked on the
 *  row, so nothing here reads the DOM and a row whose element has not been built
 *  yet still counts.
 *
 *  The list is heterogeneous, so the filter is the whole correctness argument:
 *
 *   - `kind === "chat"` is the only cue-bearing kind. It excludes the five
 *     singletons and every run tab, which carry no chat dot, and it excludes
 *     editor tabs, whose `dirty` mark rides the same element and is not a chat
 *     state. `isCueStatus` rejects `dirty` as well, so it cannot reach the fold by
 *     either route.
 *   - `owns` excludes a VIEW tab — one watching work another chat owns. Such a tab
 *     is a window onto a chat, not the chat, so counting it would count one chat
 *     twice whenever the chat's own tab is also open. A SUB-TAB is NOT excluded: a
 *     tangent carries a parent and the default `owns`, because it is its own chat
 *     with its own bridge and its own cue.
 *
 *  The id is the TAB id, which is what every other key in that module is (the
 *  rows-in-view scan reads `data-tab-id`, the switch acknowledgement reads the
 *  bus event's `to`, and the forget hook reads a closed tab's id). One key
 *  vocabulary, so no two of those can disagree. */
export function cueCandidates(): { id: string; status: string }[] {
  return state.tabs
    .filter((t) => t.subject.kind === "chat" && t.spec.owns)
    .map((t) => ({ id: t.subject.id, status: t.dotStatus ?? "" }));
}

/** Subscribe to everything that can change the attention fold's input, and
 *  return the disposer.
 *
 *  TWO signals, because there are two disjoint write paths and covering one is
 *  not covering the input: `stateVersion` for the tab SET (every projection
 *  mutation ends in `emit()`) and `dotVersion` for every dot write
 *  (`recordDotStatus`, which deliberately does not emit). A funnel on `emit()`
 *  alone would leave the count stale on every status change; one on the dot alone
 *  would leave it stale after a chat closed.
 *
 *  Deliberately NOT registered in `moduleEffects`: the caller owns this
 *  subscription's lifetime, so `_resetForTest` must not silently unsubscribe it
 *  while the caller still believes it is live. */
export function subscribeTabCues(fn: () => void): () => void {
  return effect(() => {
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    stateVersion.value; // subscribe: the tab set
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    dotVersion.value; // subscribe: any tab's dot
    fn();
  });
}

/** Register the notification for a tab leaving the projection. One slot, one
 *  consumer: attention.ts, which drops the chat's acknowledgement. */
export function setOnTabClosed(fn: (id: string) => void): void {
  callbacks.onClosed = fn;
}

// --- Empty-state timer ---

export function setOnEmpty(fn: () => void): void {
  callbacks.onEmpty = fn;
}

function scheduleEmpty(): void {
  if (internal.emptyTimer !== null) {
    clearTimeout(internal.emptyTimer);
  }
  // Longer than the closed row's exit animation on purpose: the respawned tab's
  // row must be built into an EMPTY strip, or its entry animation plays against
  // the departing row and it lands beside it until that row collapses. 500ms
  // clears the longer of the two exits in 10-shell-app.css (`.tab.exiting` at
  // 0.18s, `.exiting-merge` at --dur-standard) — and the merge one cannot be the
  // last row's anyway, since it requires a parent that is still open.
  internal.emptyTimer = setTimeout(() => {
    internal.emptyTimer = null;
    callbacks.onEmpty?.();
  }, 500);
}

// --- Module subscribers ---

/** The id last announced on BUS_TAB_CHANGED. Module state rather than a closure
 *  so _resetForTest can clear it with the rest. */
let lastAnnouncedTab = "";

function registerModuleSubscribers(): void {
  // Clear the empty timer on any activation.
  tabsEffect(() => {
    if (state.active !== "" && internal.emptyTimer !== null) {
      clearTimeout(internal.emptyTimer);
      internal.emptyTimer = null;
    }
  });

  // View / route sync.
  tabsEffect((s) => {
    if (s.tabs.length === 0 && s.active === "") {
      return;
    }
    const active = rowOfID(s.active);
    if (active !== undefined) {
      showView(active);
    } else {
      syncSidebarButtons(null);
    }
    // Announce a REAL switch. This effect re-runs on every projection mutation,
    // so the guard is what separates "the active tab changed" from "something
    // else about the tabs did" — a subscriber that tears a feature down cannot be
    // handed the second one. Emitted here rather than inside showView because
    // showView's DOM swap runs inside a view transition, so its timing is not the
    // state change's.
    if (s.active !== lastAnnouncedTab) {
      lastAnnouncedTab = s.active;
      emitBus(BUS_TAB_CHANGED, { to: s.active, kind: active?.subject.kind ?? null });
    }
  });

  // DOM rendering.
  //
  // Guarded on "no tab has ever opened" rather than on "the store is empty",
  // which is what the subscriber above still tests. The two guards differ on
  // exactly one transition, and it is a transition only this subscriber has work
  // for: closing the LAST tab leaves both state fields at their initial values,
  // so an empty-store guard skipped the render that had to remove the closed row.
  // The row therefore kept its slot in the strip, un-animated, until the NEXT
  // render — which is the one the 500ms empty-state respawn triggers, so the
  // closed row's exit and its replacement's entry played together.
  tabsEffect(() => {
    if (!internal.everOpened) {
      return;
    }
    if (internal.renderQueued) {
      return;
    }
    internal.renderQueued = true;
    requestAnimationFrame(() => {
      internal.renderQueued = false;
      renderDOM();
    });
  });
}

// --- View / route (subscriber) ---

const ALL_VIEWS_SELECTOR = "[data-tab-view]";

/** Icon buttons that should show `.active` when their singleton tab is active.
 *  Non-singleton kinds (chat, editor, run) are never in this map. */
const ACTIVE_BTN: Readonly<Partial<Record<TabKind, () => HTMLButtonElement>>> = {
  settings: () => $.settingsBtn,
  git: () => $.gitBtn,
  files: () => $.filesBtn,
  history: () => $.historyBtn,
};

function syncSidebarButtons(activeKind: TabKind | null): void {
  for (const [kind, getter] of Object.entries(ACTIVE_BTN)) {
    getter().classList.toggle("active", kind === activeKind);
  }
}

function showView(row: TabRow): void {
  // Swap the visible view ONLY when it is not already the right one.
  //
  // The effect that calls this re-runs on EVERY projection mutation, not just
  // on an activation, so without this guard closing a background tab would
  // re-run the swap for a view that never changed — cancelling and replaying
  // the entry fade on the view the reader is already looking at. Same for a
  // chat-to-chat switch: both rows resolve to the SAME view element, and
  // re-animating it would fade content that never left the screen.
  //
  // Read the current state from the DOM rather than remembering the last
  // selector. Nothing else writes these classes today, so a cached answer would
  // be correct — and silently wrong the first time something did, leaving a view
  // hidden with no way back. The scan is a handful of nodes.
  const target = document.querySelector(row.spec.view);
  const shown = [...document.querySelectorAll(ALL_VIEWS_SELECTOR)].filter(
    (n) => !(n as HTMLElement).classList.contains("hidden"),
  );
  if (shown.length !== 1 || shown[0] !== target) {
    swapViews(() => {
      for (const node of document.querySelectorAll(ALL_VIEWS_SELECTOR)) {
        (node as HTMLElement).classList.add("hidden");
      }
      target?.classList.remove("hidden");
      return target as HTMLElement | null;
    });
  }

  // Mobile toolbar title reads directly from the tab name.
  $.toolbarTitle.textContent = row.subject.kind === "chat" ? "" : row.name;

  // Close mobile sidebar after switching.
  $.sidebar.classList.remove("open");

  syncSidebarButtons(row.subject.kind);

  pushRoute(row.spec.route);
}

function renderDOM(): void {
  const list = $.tabList;
  if (!list.hasAttribute("role")) {
    list.setAttribute("role", "tablist");
  }

  const existing = new Map<string, HTMLElement>();
  for (const node of [...list.children]) {
    const id = (node as HTMLElement).dataset["tabId"];
    if (id !== undefined) {
      existing.set(id, node as HTMLElement);
    }
  }

  const activeIDs = new Set(state.tabs.map((t) => t.subject.id));

  // Remove orphans with an exit animation, and CHOOSE that animation from what
  // the row is. A sub-tab whose parent survives merges UP into it
  // (`.exiting-merge`); everything else swipes out sideways (`.exiting` alone),
  // which is what makes a parent and its whole subtree read as one block leaving
  // rather than as N children folding into a row that is departing too.
  //
  // The parent is read off the DOM rather than the store because the row has
  // already left the projection by the time this runs — removal is what put it in
  // this loop. `activeIDs` is the survivor set, so an orphan (a sub-tab whose
  // parent is not open here) answers false and takes the sideways exit, correctly:
  // there is no row on screen for it to merge into.
  for (const [id, node] of existing) {
    if (!activeIDs.has(id)) {
      const parent = node.dataset["parentId"];
      node.classList.add("exiting");
      if (parent !== undefined && activeIDs.has(parent)) {
        node.classList.add("exiting-merge");
      }
      node.addEventListener(
        "animationend",
        () => {
          node.remove();
        },
        { once: true },
      );
      existing.delete(id);
    }
  }

  // Insert + position. Skip over exiting elements when checking whether a tab is
  // already in the right spot — they're still in the DOM (animating out) but
  // shouldn't affect sibling ordering.
  let prev: HTMLElement | null = null;
  for (const row of state.tabs) {
    let node = existing.get(row.subject.id);
    if (node === undefined) {
      node = createTabEl(row);
      if (prev !== null) {
        prev.after(node);
      } else {
        list.prepend(node);
      }
      node.classList.add("entering");
    } else {
      const nameEl = node.querySelector(".tab-name");
      if (nameEl !== null && nameEl.textContent !== row.name) {
        nameEl.textContent = row.name;
      }
      let expectedNext: ChildNode | null = prev !== null ? prev.nextSibling : list.firstChild;
      while (
        expectedNext !== null &&
        // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
        (expectedNext as HTMLElement).classList?.contains("exiting")
      ) {
        expectedNext = expectedNext.nextSibling;
      }
      if (node !== expectedNext) {
        if (prev !== null) {
          prev.after(node);
        } else {
          list.prepend(node);
        }
      }
    }
    node.classList.toggle("active", row.subject.id === state.active);
    node.setAttribute("aria-selected", row.subject.id === state.active ? "true" : "false");
    node.tabIndex = row.subject.id === state.active ? 0 : -1;
    // Sub-tab indent and pin marker, both from the SUBJECT. Toggled here rather
    // than only at creation so a pin applied to an already-rendered tab shows as
    // soon as its frame lands.
    node.classList.toggle("tab-child", row.subject.parent !== "");
    node.classList.toggle("tab-pinned", row.subject.pinned);
    // The parent id rides the row for ONE reader: the exit above, which has to
    // know whether this row's parent is still open at a moment when the row is no
    // longer in the projection to ask. Written beside the indent it belongs to, so
    // the class and the attribute cannot come to disagree.
    if (row.subject.parent === "") {
      delete node.dataset["parentId"];
    } else {
      node.dataset["parentId"] = row.subject.parent;
    }
    prev = node;
  }
}

function createTabEl(row: TabRow): HTMLElement {
  const kind = row.subject.kind;
  const id = row.subject.id;
  const node = el("div", {
    className: "tab",
    "data-tab-id": id,
    "data-kind": kind,
    role: "tab",
  });

  const name = el("span", { className: "tab-name" }, row.name);

  // A SPAN, not a button, and that is forced by the row's own role: `role="tab"`
  // is Children Presentational in WAI-ARIA, so every descendant of this row is
  // pruned from the accessibility tree. A <button> in here was therefore a
  // control assistive tech could not name or reach while still holding a place in
  // the page tab sequence — one dead tab stop per open tab, and axe's
  // `nested-interactive` (serious) on every row. Closing from the keyboard is the
  // APG's own contract for a deletable tab: Delete on the focused row.
  const close = el("span", { className: "tab-close", "aria-hidden": "true" }, iconEl(ICON_CLOSE));
  close.addEventListener("pointerup", (e) => {
    e.stopPropagation();
    void closeTab(id);
  });

  // The dot is decoration; `statusSR` is what a screen reader hears. Both ride
  // every row and paintDot writes both, so no node is added or removed as a state
  // changes — the same reasoning as the pin marker below.
  const statusDot = el("span", { className: CLS_DOT, "aria-hidden": "true" });
  const statusSR = el("span", { className: `${CLS_DOT_SR} sr-only` });

  // The pin marker rides every row and CSS reveals it under `.tab-pinned`, so
  // renderDOM toggles one class instead of adding and removing a node. The glyph
  // is decorative; the .sr-only word beside it is what a screen reader hears,
  // because colour and shape alone are one channel.
  const pin = el(
    "span",
    { className: "tab-pin" },
    iconEl(ICON_PIN_FILLED),
    el("span", { className: "sr-only" }, "Pinned"),
  );

  // The nesting marker a sub-tab carries INSTEAD of its kind glyph. It is not a
  // `.tab-icon`: that class means "grab me to reorder this row", and a sub-tab's
  // position is its parent's, so a run sub-tab used to show a grab cursor on a
  // row no drag can move.
  const nest = el(
    "span",
    { className: "tab-nest", "aria-hidden": "true" },
    iconEl(ICON_TAB_SUBTAB),
  );

  if (row.subject.parent !== "") {
    // A SUB-TAB is the parent chat row's layout with the nesting arrow in front:
    // arrow, dot, name. Both halves of that come from the same reading — a row
    // that says what is happening in it beats a row that says what KIND it is,
    // and a sub-tab already says its kind by sitting under its parent. So the
    // kind glyph is spent on the marker and the dot takes the position it holds
    // on every parent chat row rather than the trailing slot, where it sat far
    // from the name it describes with the × between them.
    //
    // Keyed on the SUBJECT's parent, the same predicate `renderDOM` toggles
    // `tab-child` with, so the indent and the marker cannot disagree. Safe to
    // decide once at creation: `Parent` is set at open and never reassigned.
    node.append(nest, statusDot, name, statusSR, pin, close);
    // The `idle` floor is the chat rule below, and it does not generalize: a run
    // sub-tab has no state until its first frame lands, and an idle ring there
    // would claim one. 12-tabs.css reserves the slot instead, so the name does
    // not move when the real state arrives.
    paintDot(node, row.dotStatus ?? (kind === "chat" ? "idle" : ""));
  } else if (kind === "chat") {
    // A chat tab LEADS with its activity dot, in the slot the per-mode role glyph
    // used to hold. That is the replacement, not a supplement: the strip exists
    // to say what is happening in the chats you are not looking at, and a chat's
    // role does not change between glances while its activity does.
    node.append(statusDot, name, statusSR, pin, close);
    // Painted from the ROW, falling back to `idle`. The fallback is why the dot
    // is seeded at all rather than left blank for the store effect to fill: the
    // effect paints on a later tick, so an unseeded dot would leave the row one
    // frame narrower and shift its name.
    paintDot(node, row.dotStatus ?? "idle");
  } else {
    // Every other kind keeps its glyph — none of them has an activity concept —
    // and uses the same element in the trailing slot for the editor's unsaved
    // mark. No `idle` floor here: an editor tab with nothing unsaved has no state
    // to show.
    const icon = el("span", { className: "tab-icon" }, iconEl(TAB_ICONS[kind]));
    node.append(icon, name, statusSR, pin, statusDot, close);
    paintDot(node, row.dotStatus ?? "");
  }
  // A sub-tab is not independently draggable: its position is its parent's.
  // attachTabInteraction wires click/keyboard AND drag, so the flag rides along.
  attachTabInteraction(node, id, row.subject.parent === "");

  // Right-click context menu for chat tabs: pin/unpin, then export (md/json).
  // Non-chat tabs keep the native browser menu.
  //
  // There is no "Promote to its own tab" any more. `TabSubject.Parent` is set at
  // open and never reassigned, which is what makes a parent cycle
  // unrepresentable and why no reparent command exists to spend that property on.
  node.addEventListener("contextmenu", (e) => {
    if (kind !== "chat") {
      return;
    }
    e.preventDefault();
    // Read the CURRENT row: an applied frame replaces the subject, so the
    // closure's copy can be a generation behind on exactly the field this menu
    // reports.
    const current = rowOfID(id);
    if (current === undefined) {
      return;
    }
    const items: ContextMenuItem[] = [];
    if (current.subject.parent === "") {
      const pinned = current.subject.pinned;
      items.push({
        label: pinned ? "Unpin" : "Pin",
        action: () => {
          void setTabPinned(id, !pinned);
        },
      });
    }
    items.push(
      {
        label: "Export as Markdown",
        action: () => {
          downloadChatExport(current.subject.ref, current.name, "md");
        },
      },
      {
        label: "Export as JSON",
        action: () => {
          downloadChatExport(current.subject.ref, current.name, "json");
        },
      },
    );
    showContextMenu(items, { x: e.clientX, y: e.clientY });
  });

  return node;
}

// --- Interaction (click, middle-click, drag, keyboard) ---

function attachTabInteraction(node: HTMLElement, id: string, draggable: boolean): void {
  // Click to activate (any target outside .tab-close).
  node.addEventListener("pointerup", (e) => {
    if (isDragHandled()) {
      return;
    }
    if ((e.target as HTMLElement).closest(".tab-close") !== null) {
      return;
    }
    if (!e.isPrimary) {
      return;
    }
    activateTab(id);
  });

  // Middle-click to close.
  node.addEventListener("auxclick", (e) => {
    if (e.button === 1) {
      e.preventDefault();
      void closeTab(id);
    }
  });

  // Keyboard navigation: Enter/Space activates, Delete closes, arrows move focus.
  node.addEventListener("keydown", (e) => {
    switch (e.key) {
      case "Enter":
      case " ":
        e.preventDefault();
        activateTab(id);
        break;
      case "Delete":
      case "Backspace": {
        e.preventDefault();
        // Focus has to be moved by hand, or Delete drops the keyboard user out of
        // the strip and onto <body>: the row they were standing on is the element
        // being removed. The APG's Delete contract is that focus lands on the tab
        // that took the closed one's place, so the successor list is "everything
        // after me, then everything before me in reverse".
        //
        // Moved BEFORE the dispatch, and that is the one thing this gesture had to
        // change: a close is a round trip now, so waiting for the removal to land
        // would leave focus on <body> for the whole flight. The first surviving
        // sibling is where focus belongs either way, and a close that is refused
        // leaves the strip intact with focus on a neighbour, which is harmless.
        const siblings = [...(node.parentElement?.children ?? [])] as HTMLElement[];
        const self = siblings.indexOf(node);
        const successors = [...siblings.slice(self + 1), ...siblings.slice(0, self).reverse()];
        successors.find((n) => n.dataset["tabId"] !== id)?.focus();
        void closeTab(id);
        break;
      }
      case "ArrowDown":
      case "ArrowRight":
      case "ArrowUp":
      case "ArrowLeft":
      case "Home":
      case "End": {
        e.preventDefault();
        const tabs = [...(node.parentElement?.children ?? [])] as HTMLElement[];
        const i = tabs.indexOf(node);
        let targetEl: HTMLElement | undefined;
        if (e.key === "ArrowDown" || e.key === "ArrowRight") {
          targetEl = tabs[i + 1] ?? tabs[0];
        } else if (e.key === "ArrowUp" || e.key === "ArrowLeft") {
          targetEl = tabs[i - 1] ?? tabs[tabs.length - 1];
        } else if (e.key === "Home") {
          targetEl = tabs[0];
        } else {
          targetEl = tabs[tabs.length - 1];
        }
        targetEl?.focus();
        break;
      }
    }
  });

  if (draggable) {
    attachDrag(node);
  }
}

// --- Singleton tab helpers ---

/** Open or toggle a singleton (always-one-instance) tab. If it's already the
 *  active tab, close it; otherwise open/activate.
 *
 *  Both halves are round trips now, and the CLOSE half is why this cannot be
 *  await-free at its call sites without care: a toggle that resolves before the
 *  frame lands would let a second click race the first. The four public toggles
 *  return the promise so a caller that has a reason to sequence can. */
async function toggleSingleton(kind: TabKind): Promise<void> {
  const open = tabIdFor(kind);
  if (open !== "" && state.active === open) {
    await closeTab(open);
    return;
  }
  await openTab({ kind });
}

/** Toggle the Settings tab, landing on the given sub-tab (default: General).
 *
 *  The sub-tab is applied AFTER the tab exists, through the same setter the
 *  router uses. A singleton's ref is empty, so a subject cannot carry a sub-tab
 *  and the factory has to build the canonical route — which makes the correction
 *  channel below load-bearing rather than a convenience. */
export async function toggleSettingsView(tab: SettingsTab = "general"): Promise<void> {
  await toggleSingleton("settings");
  setSettingsTab(tab);
}

/** Switch the Settings panel to a specific sub-tab. No-op when Settings is not
 *  open.
 *
 *  SYNCHRONOUS, deliberately, and it is not a mutation of the collection: a
 *  singleton's sub-tab is not in the subject, so this is the client's own
 *  correction channel over the route the factory built. Router-driven navigation
 *  reads it that way too — it must change the inner tab without toggling. */
export function setSettingsTab(tab: SettingsTab): void {
  setSingletonRoute("settings", { kind: "settings", tab });
}

export async function toggleGitView(tab: GitTab = "changes"): Promise<void> {
  await toggleSingleton("git");
  setGitTab(tab);
}

/** Switch the git view's sub-tab route. No-op when the git view isn't open.
 *  Mirrors setSettingsTab for the same reason. */
export function setGitTab(tab: GitTab): void {
  setSingletonRoute("git", { kind: "git", tab });
}

export async function toggleFilesView(): Promise<void> {
  await toggleSingleton("files");
}

/** Bring the file browser forward. NEVER closes it, which is the whole reason
 *  this exists beside the toggle.
 *
 *  "Toggle" and "go to" are different verbs, and this module already draws that
 *  line for sub-tabs (setSettingsTab, setGitTab, setDocsTab all refuse to
 *  toggle). The files view had only the toggle, so a caller whose intent was "the
 *  browser has to be visible for what I am about to show in it" closed it instead
 *  whenever it already was — which is what find-in-files did from the browser's
 *  own search button, leaving the bar open over a departed view. */
export async function showFilesView(): Promise<void> {
  const open = tabIdFor("files");
  if (open !== "" && state.active === open) {
    return;
  }
  await openTab({ kind: "files" });
}

export async function toggleHistoryView(): Promise<void> {
  await toggleSingleton("history");
}

/** Switch the docs browser's sub-tab route. No-op when it isn't open. */
export function setDocsTab(tab: DocsTab): void {
  setSingletonRoute("docs", { kind: "docs", tab });
}

/** Toggle the Kiro configuration browser, landing on the given sub-tab. */
export async function toggleDocsView(tab: DocsTab = "steering"): Promise<void> {
  await toggleSingleton("docs");
  setDocsTab(tab);
}

/** Point a singleton's LOCAL route at a sub-tab.
 *
 *  The spec is replaced rather than mutated, because a `TabViewSpec` is a
 *  readonly snapshot: every other field of it is immutable by contract, and the
 *  route is the one the client corrects. Emits only when the tab is active, since
 *  the route subscriber is what pushes the URL. */
function setSingletonRoute(kind: TabKind, route: Route): void {
  const id = tabIdFor(kind);
  const row = rowOfID(id);
  if (row === undefined) {
    return;
  }
  row.spec = { ...row.spec, route };
  if (state.active === id) {
    emit();
  }
}

// --- Multi-instance openers ---

/** Open (or focus) a workflow run's tab.
 *
 *  Not a singleton: several runs can be open side by side, keyed by run id.
 *
 *  `parent` makes it a SUB-TAB of the chat that launched it, which is what a run
 *  parented on a chat session should be: indented under that chat, sorted after
 *  it, closed when it closes. `owns: false` is the close contract that lets the
 *  two be joined safely — a VIEW tab tears nothing down, so the × on a run
 *  sub-tab REMOVES A VIEW and stops nothing at all, while the launching chat's ×
 *  is what cancels. A launcher-OWNED run keeps `owns: true`, so its × means stop.
 *
 *  The offer-once guard is NOT here and must not move here. It is a fact about
 *  this client's history — whether this reader has already been offered this
 *  run's tab — and it lives at `openRunSubTab` (run-view.ts) where that history
 *  is kept. */
export async function openRunTab(
  workflowID: string,
  name: string,
  opts?: { parent?: string; owns?: boolean; activate?: boolean },
): Promise<void> {
  await openTab({
    kind: "run",
    ref: workflowID,
    name,
    ...(opts?.parent === undefined ? {} : { parent: opts.parent }),
    ...(opts?.owns === undefined ? {} : { owns: opts.owns }),
    ...(opts?.activate === undefined ? {} : { activate: opts.activate }),
  });
}

/** Open (or focus) a SUBAGENT execution's own page.
 *
 *  Not a singleton and not offered automatically: a delegate lives and dies inside
 *  the turn that dispatched it, so nothing here is the run tab's proactive offer.
 *  The two reasons that offer exists both fail for a delegate — a run outlives its
 *  turn and emits a progress frame per node, and neither is true of a subagent — so
 *  this door only ever answers a reader who asked. Every caller is a click or a
 *  deep link; no SSE handler may call it.
 *
 *  `owns: false` always, so the × dismisses a view. There is nothing else it could
 *  be: the page is a projection of blocks the chat store owns, and closing it stops
 *  neither the delegate nor the transcript's own card, which keeps streaming.
 *
 *  `parent` nests it under the launching chat's tab, which is what puts a delegate
 *  beside the conversation that ran it rather than at the end of the strip. A chat
 *  with no tab here promotes it to top level rather than refusing it, the same
 *  fallback `insertRow` and the server's Open already apply.
 *
 *  It carries no NAME override: the factory derives the label from the invocation
 *  tool call in the chat store, so a tab restored on boot and a tab opened from a
 *  card's link read the same. */
export async function openSubagentTab(chatID: string, subtaskID: string): Promise<void> {
  if (chatID === "" || subtaskID === "") {
    return;
  }
  const parent = tabIdFor("chat", chatID);
  await openTab({
    kind: "subagent",
    ref: subagentRef(chatID, subtaskID),
    ...(parent === "" ? {} : { parent }),
    owns: false,
  });
}

/** Open (or focus) an editor tab for a path.
 *
 *  Only the TAB half of the editor's own `open()` is here. That function writes
 *  the file's mode, repo and pending line before the tab exists, and those are
 *  the opener's arguments rather than facts about what is open — the mode lives in
 *  `fileStates`, the line in the pushRoute the opener issues afterwards — so the
 *  editor keeps them and this opens the tab. */
export async function openEditorView(
  filePath: string,
  opts?: { activate?: boolean },
): Promise<void> {
  await openTab({
    kind: "editor",
    ref: filePath,
    ...(opts?.activate === undefined ? {} : { activate: opts.activate }),
  });
}

// --- Test helpers (no-op in production; used by tabs.test.ts) ---

/** Reset all projection state. Exported for test isolation only. */
export function _resetForTest(): void {
  state.tabs = [];
  state.active = "";
  callbacks.onActivate = null;
  callbacks.onEmpty = null;
  callbacks.onClosed = null;
  if (internal.emptyTimer !== null) {
    clearTimeout(internal.emptyTimer);
  }
  internal.emptyTimer = null;
  internal.renderQueued = false;
  internal.everOpened = false;
  nameOverrides.clear();
  lastAnnouncedTab = "";
  // Re-register the target: a test that reset tabs-sync dropped it.
  registerTabsTarget(target);
  // Dispose existing module effects and re-register so tests observe the same
  // side-effects (view sync, DOM render) as production.
  for (const c of moduleEffects) {
    c();
  }
  moduleEffects.length = 0;
  registerModuleSubscribers();
}

// Register module-level effects after all module declarations are initialized —
// effect() bodies run synchronously on subscribe, so they must run AFTER
// ACTIVE_BTN, syncSidebarButtons, renderDOM and showView are defined.
registerModuleSubscribers();
