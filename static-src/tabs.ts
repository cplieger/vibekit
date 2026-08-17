// ---------------------------------------------------------------------------
// Tab bar: single observable store of open tabs. Rendering, localStorage
// persistence, and URL routing are three subscribers of the same store;
// any mutation (open, close, activate, rename, reorder) flows through one
// primitive and fans out.
//
// Per-device UI state: tab order + active tab persist in localStorage
// via ui-state.ts. Not synced to the server.
// ---------------------------------------------------------------------------

import { pushRoute } from "./router.js";
import type { Route, SettingsTab, GitTab, DocsTab } from "./router.js";
import {
  ICON_CLOSE,
  ICON_TAB_CHAT,
  ICON_TAB_PLAN,
  ICON_TAB_SETTINGS,
  ICON_TAB_GIT,
  ICON_TAB_FILES,
  ICON_TAB_RUN,
  ICON_TAB_EDITOR,
  ICON_TAB_HISTORY,
  ICON_TAB_DOCS,
  ICON_PIN_FILLED,
} from "./icons.js";
import { iconEl } from "./icon-el.js";
import * as uiState from "./ui-state.js";
import { $ } from "./dom.js";
import { viewTransition as uipViewTransition } from "@cplieger/ui-primitives/view-transition";
import { signal, effect, el } from "@cplieger/reactive";
import { attachDrag, isDragHandled, setReorderCallback } from "./tabs-drag.js";
import { showContextMenu } from "./context-menu.js";
import type { ContextMenuItem } from "./context-menu.js";
import { downloadChatExport } from "./chat-export.js";
import { BUS_TAB_CHANGED, emitBus } from "./bus.js";

// --- Types ---

/** Default view selector for each tab kind. Callers can omit `view` from
 *  TabSpec when the standard mapping applies. */
export const TAB_VIEWS = {
  chat: "#chat-view",
  plan: "#chat-view",
  settings: "#settings-view",
  git: "#git-view",
  files: "#files-view",
  editor: "#editor-view",
  history: "#history-view",
  docs: "#docs-view",
  run: "#run-view",
} as const;

export type TabKind = keyof typeof TAB_VIEWS;

/** Everything needed to render and route a tab. */
export interface TabSpec {
  id: string;
  name: string;
  kind: TabKind;
  /* There is deliberately no per-tab icon override. It existed for exactly one
   * purpose — a per-agent-role glyph on chat tabs — and a chat tab's leading
   * element is the activity dot now, so the field had one producer
   * (`iconForMode`) feeding a slot that no longer renders one. `iconForMode`
   * itself stays: the mode pill and its picker are where a chat's role reads
   * out. */
  /** The activity dot's last painted state, so a row that is CREATED already
   *  knows what it should show.
   *
   *  The dot used to live only in the DOM, and `setTabStatus` wrote only to the
   *  live node — so any path that built a row without a following state change
   *  showed the seeded `idle` whatever the chat was doing. Two such paths, both
   *  ordinary: the boot restore populates sessions BEFORE it opens their tabs (so
   *  the store effect has already run by the time the rows exist, and nothing
   *  makes it run again), and `promoteTab` deliberately discards and rebuilds a
   *  row without touching session state. A working, failed, waiting or input
   *  background tab read as idle until something unrelated repainted it, which is
   *  the feature failing on exactly the path it exists for.
   *
   *  LIVE state, and it must never be persisted: the persistence subscriber
   *  derives `tab_order` / `pinned_tabs` from ids and `active_view` from the
   *  active id, so no TabSpec field can reach localStorage by construction, and a
   *  dot restored from a previous process would be a claim about a turn that
   *  ended before the page loaded. `openChatTab` seeds it from CURRENT session
   *  state instead. */
  dotStatus?: TabDotStatus | undefined;
  /** CSS selector for the view element to show. */
  view: string;
  /** The URL route this tab maps to. */
  route: Route;
  /** Called when the tab becomes active. */
  onShow?: (() => void) | undefined;
  /** Called when the tab is closed. */
  onClose?: (() => void) | undefined;
  /** The tab this one hangs off, making it a SUB-TAB: it renders indented under
   *  its parent, sorts immediately after it, is not independently draggable, is
   *  not persisted in `tab_order`, and closes when its parent closes.
   *
   *  Deliberately a property of the TAB, not a lookup into chat state. The
   *  indent used to key off the chat store's `parent_chat_id`, which coupled the
   *  tab module to one feature's data model and meant only chats could ever have
   *  children. Nothing in this contract knows what a run, a tangent or a diff
   *  view is, so a future one is a two-field opt-in rather than a second
   *  mechanism. */
  parentId?: string | undefined;
  /** Pinned: this tab sorts ahead of every unpinned one and stays reachable when
   *  the strip is long. Persisted per device beside `tab_order`.
   *
   *  Pinning a TAB inverts the flaw that kills pinning a message: it is a
   *  present-tense judgement rather than a prediction, because you know right now
   *  which conversation matters. It is also the whole feature — no folders, no
   *  tags, nothing to maintain.
   *
   *  Meaningful on a top-level tab only. A sub-tab's position is its parent's,
   *  the same rule that denies it a drag handle. */
  pinned?: boolean | undefined;
  /** Whether closing this tab tears down what it shows. Default true.
   *
   *  `owns: false` makes the tab a VIEW: closing it dismisses the view and
   *  nothing else, so a sub-tab watching work another chat owns cannot kill that
   *  work by being closed. The parent still tears down its own children when it
   *  closes — that cascade is about the OWNER going away, not about the view. */
  owns?: boolean | undefined;
}

// --- Singleton tab IDs (single source of truth) ---

const TAB_SETTINGS = "__settings__";
const TAB_GIT = "__git__";
const TAB_FILES = "__files__";
const TAB_HISTORY = "__history__";
const TAB_DOCS = "__docs__";

/** Id prefix for the one non-singleton tab kind keyed by a path. */
const EDITOR_TAB_PREFIX = "editor:";

const ICONS: Readonly<Record<TabKind, string>> = {
  chat: ICON_TAB_CHAT,
  plan: ICON_TAB_PLAN,
  settings: ICON_TAB_SETTINGS,
  git: ICON_TAB_GIT,
  files: ICON_TAB_FILES,
  editor: ICON_TAB_EDITOR,
  history: ICON_TAB_HISTORY,
  docs: ICON_TAB_DOCS,
  run: ICON_TAB_RUN,
};

// --- Store ---

interface Callbacks {
  onActivate: ((id: string) => void) | null;
  onEmpty: (() => void) | null;
  /** Notified with the id of every tab that leaves the store. `closeTab` is the
   *  only production path that removes one, so this is complete by construction.
   *  A NOTIFICATION slot, like onEmpty: it must not mutate the store. */
  onClosed: ((id: string) => void) | null;
}

interface Internal {
  emptyTimer: ReturnType<typeof setTimeout> | null;
  renderQueued: boolean;
}

interface State {
  tabs: TabSpec[];
  active: string;
}

const state: State = { tabs: [], active: "" };
const callbacks: Callbacks = { onActivate: null, onEmpty: null, onClosed: null };
const internal: Internal = { emptyTimer: null, renderQueued: false };

/** Reactive version counter. Effects subscribed via `tabsEffect()` re-run
 *  on every emit(). State is mutated in place; this counter is the
 *  signal those mutations trip. */
const stateVersion = signal(0);

/** Reactive counter for DOT writes, which deliberately do not `emit()`.
 *
 *  A second signal rather than a second emit: `emit()` runs the persistence
 *  subscriber and queues a re-render, and a dot is not a structural change (see
 *  setTabStatus). Only `subscribeTabCues` reads this one, so a dot write reaches
 *  the out-of-page attention surfaces without waking anything else. */
const dotVersion = signal(0);
type Subscriber = (s: State) => void;

/** All registered module-level effects. Tracked so _resetForTest can
 *  dispose them and start fresh; production never disposes. */
const moduleEffects: (() => void)[] = [];

function emit(): void {
  stateVersion.value = stateVersion.peek() + 1;
}

/** Register an effect that re-runs on every state mutation. The callback
 *  receives the current State by reference (mutations are visible). */
function tabsEffect(fn: Subscriber): () => void {
  const cleanup = effect(() => {
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    stateVersion.value; // subscribe
    fn(state);
  });
  moduleEffects.push(cleanup);
  return cleanup;
}

// --- Public API ---

/** Open a tab (or activate it if already open). Pass `{ activate: false }`
 *  for bulk boot-time restores: the tab is inserted and rendered without
 *  being activated (no view swap, no onShow data fetch), so restoring N
 *  tabs doesn't fan out N fetches — the caller activates exactly one tab
 *  at the end (B8). */
export function openTab(spec: TabSpec, opts?: { activate?: boolean }): void {
  const idx = state.tabs.findIndex((t) => t.id === spec.id);
  if (idx >= 0) {
    // Tab already open — only callbacks (onShow/onClose) are updated;
    // name, kind, view, and route remain unchanged from the original open.
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    state.tabs[idx] = { ...state.tabs[idx]!, onShow: spec.onShow, onClose: spec.onClose };
    if (opts?.activate !== false) {
      activateTab(spec.id);
    }
    return;
  }
  insertSpec(spec);
  if (opts?.activate === false) {
    emit();
    return;
  }
  activateTab(spec.id);
}

/** A tab's sub-tabs, in creation order.
 *
 *  Reads `spec.parentId` rather than chat state, which is what decouples the tab
 *  module from the chat store and lets any tab kind have children. */
function childrenOf(id: string): TabSpec[] {
  return state.tabs.filter((t) => t.parentId === id);
}

/** Insert a spec at its canonical position: a sub-tab immediately after its
 *  parent's existing children (creation order within the group), a top-level tab
 *  at the end. Keeping `state.tabs` parent-anchored means the render walk and
 *  the keyboard order need no grouping logic of their own — the array already
 *  reads the way the strip looks. */
function insertSpec(spec: TabSpec): void {
  const parent = spec.parentId;
  if (parent === undefined) {
    state.tabs.push(spec);
    applyPinOrder();
    return;
  }
  const pIdx = state.tabs.findIndex((t) => t.id === parent);
  if (pIdx < 0) {
    // An orphan (parent already closed) behaves as top-level rather than
    // vanishing: a tab nobody can see is worse than a tab in the wrong place.
    state.tabs.push(spec);
    applyPinOrder();
    return;
  }
  let at = pIdx + 1;
  while (at < state.tabs.length && state.tabs[at]?.parentId === parent) {
    at++;
  }
  state.tabs.splice(at, 0, spec);
}

/** Group `state.tabs` into [parent, ...its whole descendant tree] runs, in array
 *  order. Shared by the pin partition, which must move a parent and everything
 *  under it as one unit — splitting them would put a child under a stranger.
 *
 *  Membership is tested against every tab already IN a group, not just each
 *  group's first element. A sub-tab can itself have one (the transcript menu
 *  parents a side conversation on the ACTIVE chat, which may already be a side
 *  conversation), and matching only the head made such a grandchild an orphan
 *  top-level group — so pinning any other tab could sort it away from the tab its
 *  own `parentId` names. */
function tabGroups(): TabSpec[][] {
  const groups: TabSpec[][] = [];
  for (const t of state.tabs) {
    const owner =
      t.parentId === undefined
        ? undefined
        : groups.findLast((g) => g.some((m) => m.id === t.parentId));
    if (owner === undefined) {
      // A top-level tab, or an orphan — which behaves as top-level here for the
      // same reason insertSpec treats it that way.
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
 *  In the ARRAY, not in the render, because two mechanisms read DOM order back as
 *  the truth: a drop reads the new order out of the strip (tabs-drag.ts), and the
 *  keyboard arrows walk `el.parentElement.children`. A render-time sort would
 *  make both disagree with what is stored.
 *
 *  This is also what makes a pinned tab undraggable below an unpinned one, and
 *  why the drag subsystem needed no change: every drop commits through
 *  reorderTabs, which re-partitions, so an illegal drop snaps back. A snap-back
 *  is honest — the alternative was clamping the drop indicator mid-drag inside a
 *  subsystem whose index arithmetic is correctness-sensitive. */
function applyPinOrder(): void {
  const groups = tabGroups();
  const pinned = groups.filter((g) => g[0]?.pinned === true);
  if (pinned.length === 0 || pinned.length === groups.length) {
    return;
  }
  state.tabs = [...pinned, ...groups.filter((g) => g[0]?.pinned !== true)].flat();
}

/** Pin or unpin a top-level tab. No-op on a sub-tab: its position is its
 *  parent's, exactly as with drag. */
export function setTabPinned(id: string, pinned: boolean): void {
  const tab = state.tabs.find((t) => t.id === id);
  if (tab === undefined || tab.parentId !== undefined || (tab.pinned ?? false) === pinned) {
    return;
  }
  tab.pinned = pinned;
  applyPinOrder();
  emit();
}

/** Promote a sub-tab to a top-level tab.
 *
 *  Persistence is FREE: the persistence subscriber derives `tab_order` from
 *  `tabs.filter(t => t.parentId === undefined)` on every emit, so clearing the
 *  field is the whole storage story. Nothing is sent to the server either — a
 *  side conversation is already its own chat record with its own bridge, so there
 *  is nothing to copy.
 *
 *  The DOM node is DISCARDED rather than reused, which is the one non-obvious
 *  part. `attachTabInteraction` decides draggability once, at element creation,
 *  and renderDOM keeps the existing node for a tab it already knows — so a
 *  promoted tab would keep its indent and, worse, never get a drag handle. */
export function promoteTab(id: string): void {
  const tab = state.tabs.find((t) => t.id === id);
  if (tab?.parentId === undefined) {
    return;
  }
  tab.parentId = undefined;
  document.querySelector(`[data-tab-id="${CSS.escape(id)}"]`)?.remove();
  applyPinOrder();
  emit();
}

/** Activate an existing tab. */
export function activateTab(id: string): void {
  const tab = state.tabs.find((t) => t.id === id);
  if (tab === undefined || state.active === id) {
    return;
  }
  state.active = id;
  emit();
  tab.onShow?.();
  callbacks.onActivate?.(id);
}

/** Close a tab. Activates the neighbor or fires onEmpty if none remain.
 *  Pass `{ skipOnClose: true }` when the chat is already deleted remotely
 *  to avoid re-dispatching the delete action against a stale session.
 *
 *  `skipOnClose` is a fact about `id` ALONE and never travels down the cascade —
 *  see the children loop below. */
export function closeTab(id: string, opts?: { skipOnClose?: boolean }): void {
  const idx = state.tabs.findIndex((t) => t.id === id);
  if (idx < 0) {
    return;
  }
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const tab = state.tabs[idx]!;

  // Sub-tabs close with their parent, children FIRST. There is no question to
  // ask: a child is a view of something the parent owns, so the parent going
  // away takes it with it. (The prompt that used to live here offered "keep" —
  // promoting a child to top-level — which only made sense while children were
  // rewind branches with independent server-side lives.)
  //
  // Children first so a child's own teardown never runs against a parent that
  // has already gone.
  //
  // WITHOUT opts, deliberately. `skipOnClose` means "this id was already deleted
  // remotely", which is true of the root and of nothing else: a child is its own
  // persisted chat with its own bridge, and its `onClose` is what cancels its
  // turn and tears that bridge down. Forwarding the flag left the child's tab
  // gone from the UI while its process kept running with no surface to stop it.
  // A child that owns nothing still skips its teardown — through its own
  // ownership check below, which is where that decision belongs.
  for (const child of childrenOf(id)) {
    closeTab(child.id);
  }

  // REMOVE, then notify. The order is the store's re-entrancy guarantee, not a
  // detail: onClose is a TEARDOWN callback, so it must observe a state the tab
  // has already left. Notifying first made every callback that closes its own tab
  // an infinite loop — the editor's teardown did exactly that (closeEditorFile
  // ended in closeTab), so the tab still being present meant closeTab re-entered,
  // fired onClose again, and recursed until the stack died. Every editor tab was
  // unclosable by ×, middle-click and Delete.
  //
  // The index is re-found because the children cascade above spliced the array,
  // and a missing tab means a child's teardown already closed this one, whose
  // onClose has therefore already run.
  const at = state.tabs.findIndex((t) => t.id === id);
  if (at < 0) {
    return;
  }
  state.tabs.splice(at, 1);

  // The tab is gone from the store, so anything keying per-tab state off an id can
  // drop it. Fired here, after the splice and before the teardown, for the same
  // re-entrancy reason the splice comes first: a listener must observe a state the
  // tab has already left.
  callbacks.onClosed?.(id);

  // `owns: false` tears down nothing: the tab is a view, and dismissing a view
  // must not kill the work it was watching.
  if (!opts?.skipOnClose && tab.owns !== false) {
    tab.onClose?.();
  }

  if (state.active === id) {
    const next = state.tabs[Math.min(at, state.tabs.length - 1)];
    if (next !== undefined) {
      state.active = next.id;
      emit();
      next.onShow?.();
      callbacks.onActivate?.(next.id);
    } else {
      state.active = "";
      emit();
      scheduleEmpty();
    }
  } else {
    emit();
  }
}

export function renameTab(id: string, name: string): void {
  const tab = state.tabs.find((t) => t.id === id);
  if (tab === undefined || tab.name === name) {
    return;
  }
  tab.name = name;
  emit();
}

/** The activity dot's states. Six come from a chat's live state (derived by
 *  `tabStatusFor` in store.ts); "dirty" is the editor's unsaved mark, which
 *  rides the same element because a tab is never both a chat and a file.
 *
 *  Ported from @cplieger/web-terminal-ui's `.wt-status-dot`; the visual grammar
 *  and the reasoning behind each state live in css/12-tabs.css. */
export type TabDotStatus = "idle" | "working" | "waiting" | "input" | "failed" | "done" | "dirty";

/** What each state is CALLED. This is not decoration: the dot is a 9px
 *  graphical object, and a screen-reader user gets nothing at all from it, so
 *  the phrase is the state's only channel for them — which matters most here,
 *  because the whole feature exists for tabs you are not looking at.
 *
 *  One table feeding both the announced name and the hover tooltip, so what a
 *  sighted user reads and what a screen reader hears cannot drift. Exhaustive
 *  over TabDotStatus by type, so a new state cannot ship unnamed.
 *
 *  `waiting` and `input` used to share one VISUAL, which made these two phrases
 *  the only place the distinction survived. They now differ by hue and by fill
 *  (css/12-tabs.css), and the phrases stay deliberately unlike each other anyway:
 *  the visual says "this chat wants you" for both, and only the words say which
 *  kind of wanting it is. */
const DOT_PHRASE: Readonly<Record<TabDotStatus, string>> = {
  idle: "idle",
  working: "working",
  waiting: "waiting for you",
  input: "needs a decision",
  // "operation" rather than "turn": the latch behind this state is set for every
  // `error` frame naming the chat, which includes `switch_failed` and
  // `bridge_start_failed` — failures with no turn in them. The breadth is
  // deliberate and useful (a chat whose bridge would not start has failed at
  // something the reader needs to see), so the phrase is what had to widen. It is
  // the only channel a screen-reader user has here, so it must not claim more than
  // its producer supports.
  failed: "last operation failed",
  done: "turn finished",
  dirty: "unsaved changes",
};

/** Paint one tab's dot: the attribute CSS keys off, the tooltip a pointer
 *  reveals, and the word a screen reader hears.
 *
 *  The announced word is a SEPARATE element after `.tab-name`, not a child of
 *  the dot, and the position is the reason. A tab's accessible name is computed
 *  from its contents, and the dot is the leading element on a chat row — so a
 *  word inside it would announce "working Fix the parser" rather than "Fix the
 *  parser, working". The app already settled that ordering on its workflow rows
 *  ("Open nightly-sweep, failed"): the name opens, the state follows. The dot
 *  itself is therefore aria-hidden and purely visual.
 *
 *  An aria-label on the tab root would have been the other option and is worse:
 *  it REPLACES the computed name, so it would have to restate the tab name and
 *  the pinned marker, and be re-synced on every rename — which the close
 *  button's own stale `aria-label="Close <old name>"` already demonstrates.
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

const CLS_DOT = "tab-status-dot";
const CLS_DOT_SR = "tab-status-sr";

function rowOf(id: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(`[data-tab-id="${CSS.escape(id)}"]`);
}

/** Set a chat tab's activity dot. The state is derived by `tabStatusFor`
 *  (store.ts), which owns the precedence; this is the writer.
 *
 *  Records the value on the SPEC before painting, so a row created or rebuilt
 *  later starts from the real state instead of the seeded `idle` — see
 *  `TabSpec.dotStatus`. Deliberately does NOT `emit()`: a dot is not a structural
 *  change, so it must not run the persistence subscriber or a re-render, and the
 *  paint below is the whole visible effect. */
export function setTabStatus(id: string, status: TabDotStatus | ""): void {
  recordDotStatus(id, status);
  const row = rowOf(id);
  if (row === null) {
    return;
  }
  paintDot(row, status);
}

/** Park a dot state on its spec. "" means no state, which is an ABSENT field
 *  rather than an empty string, so `createTabEl` can tell "nothing was ever
 *  painted" from "painted, then cleared" with one `?? default`. */
function recordDotStatus(id: string, status: TabDotStatus | ""): void {
  const tab = state.tabs.find((candidate) => candidate.id === id);
  if (tab === undefined) {
    return;
  }
  const before = tab.dotStatus;
  if (status === "") {
    delete tab.dotStatus;
  } else {
    tab.dotStatus = status;
  }
  // Only a CHANGED dot moves the attention surfaces, and the guard is what keeps
  // the store effect's sweep over every open chat (chat.ts) from waking the fold
  // once per tab on a change that touched one of them. The PAINT below is
  // deliberately unguarded: an unchanged state still has to be re-applied to a
  // row that was rebuilt since the last write.
  if (before !== tab.dotStatus) {
    dotVersion.value = dotVersion.peek() + 1;
  }
}

/** Set (or clear, with "") a tab's hover tooltip. Used for the agent's
 *  self-declared "what I'm working on" description on chat tabs. Direct
 *  DOM write like setTabStatus — reapplied by the store effect after any
 *  re-render, so transient loss on rebuild self-heals the same way the
 *  status dot does. */
export function setTabTooltip(id: string, text: string): void {
  const el = document.querySelector<HTMLElement>(`[data-tab-id="${CSS.escape(id)}"]`);
  if (el === null) {
    return;
  }
  if (text === "") {
    el.removeAttribute("title");
  } else {
    el.title = text;
  }
}

/** Mark an editor tab as having unsaved changes (a steady accent disc).
 *  Reuses the shared .tab-status-dot on the ONE attribute setTabStatus writes,
 *  which is what makes the two halves mutually exclusive by construction rather
 *  than by convention: a chat tab never receives setTabDirty and an editor tab
 *  never receives setTabStatus, and even if one did, the second write would
 *  replace the first instead of leaving two states painted at once. */
export function setTabDirty(id: string, dirty: boolean): void {
  recordDotStatus(id, dirty ? "dirty" : "");
  const row = rowOf(id);
  if (row === null) {
    return;
  }
  paintDot(row, dirty ? "dirty" : "");
}

function reorderTabs(order: string[]): void {
  const byID = new Map(state.tabs.map((t) => [t.id, t]));
  const next: TabSpec[] = [];
  /** Take a tab, then immediately its whole descendant tree — so a persisted or
   *  dragged order that names only top-level ids still reproduces the full strip.
   *
   *  Recursive for the same reason tabGroups matches on membership: a direct-
   *  children walk left a grandchild in `byID`, and the defensive sweep below
   *  then appended it to the END of the strip, away from its parent. Terminates
   *  on the byID guard even if two specs ever named each other as parent. */
  const take = (id: string): void => {
    const t = byID.get(id);
    if (t === undefined) {
      return;
    }
    next.push(t);
    byID.delete(id);
    for (const c of childrenOf(id)) {
      if (byID.has(c.id)) {
        take(c.id);
      }
    }
  };
  for (const id of order) {
    take(id);
  }
  // Preserve any unnamed tabs at the end (defensive; order drift). Parents go
  // through take() so their children follow them rather than trailing the strip.
  for (const t of [...byID.values()]) {
    if (t.parentId === undefined) {
      take(t.id);
    }
  }
  for (const t of byID.values()) {
    next.push(t);
  }
  state.tabs = next;
  // The one funnel for BOTH a drag drop and the boot restore, so the pinned-first
  // rule holds for both without either knowing about it.
  applyPinOrder();
  emit();
}

export function hasTab(id: string): boolean {
  return state.tabs.some((t) => t.id === id);
}
export function getActiveTabId(): string {
  return state.active;
}
/** Route of the currently active tab, or null when no tab is active.
 *  Used by app.ts to canonicalize the URL to a restored non-chat tab. */
export function getActiveTabRoute(): Route | null {
  const tab = state.tabs.find((t) => t.id === state.active);
  return tab?.route ?? null;
}
/** Kind of the currently active tab, or null when no tab is active.
 *
 *  This is what a key binding scoped BY VIEW reads. The store already knows
 *  which tab is active and what kind it is, so the answer is here rather than in
 *  a DOM test for which view element happens to be unhidden — and it is the
 *  TabSpec's kind rather than the route's, because an editor tab's kind is
 *  "editor" while its route's is "file", and a binding keyed on the route would
 *  be speaking a second vocabulary for the same question. */
export function getActiveTabKind(): TabKind | null {
  const tab = state.tabs.find((t) => t.id === state.active);
  return tab?.kind ?? null;
}
/** Return all open tab IDs. Used by system handlers to reconcile tabs
 *  without DOM scraping. */
export function getOpenTabIDs(): string[] {
  return state.tabs.map((t) => t.id);
}

/** The chat tabs and their current dot states, for the out-of-page attention fold
 *  (attention.ts). A pure STORE read: the dot state is parked on the spec, so
 *  nothing here reads the DOM and a row that has not been built yet still counts.
 *
 *  The list is heterogeneous, so the filter is the whole correctness argument:
 *
 *   - `kind === "chat"` is the only cue-bearing kind. It excludes the five
 *     `__…__` singletons and every `run:` tab, which carry no dot at all, and it
 *     excludes `editor:` tabs, whose `dirty` mark rides the same element and is
 *     not a chat state. `isCueStatus` rejects `dirty` as well, so it cannot reach
 *     the fold by either route.
 *   - `owns !== false` excludes a VIEW tab — one watching work another chat owns.
 *     Such a tab is a window onto a chat, not the chat, so counting it would count
 *     one chat twice whenever the chat's own tab is also open. A SUB-TAB is NOT
 *     excluded: a tangent carries `parentId` and the default `owns`, because it is
 *     its own chat with its own bridge and its own cue.
 *
 *  Ids are unique in the store (openTab dedupes), so no further de-duplication is
 *  needed for the count to be one per chat. */
export function cueCandidates(): { id: string; status: string }[] {
  return state.tabs
    .filter((t) => t.kind === "chat" && t.owns !== false)
    .map((t) => ({ id: t.id, status: t.dotStatus ?? "" }));
}

/** Subscribe to everything that can change the attention fold's input, and return
 *  the disposer.
 *
 *  TWO signals, because there are two disjoint write paths and covering one is not
 *  covering the input: `stateVersion` for the tab SET (every list mutation ends in
 *  `emit()` — openTab, closeTab on all three of its branches, reorderTabs,
 *  activateTab, promoteTab, restoreTabState) and `dotVersion` for every dot write
 *  (`recordDotStatus`, the single writer of `TabSpec.dotStatus`, which
 *  deliberately does not emit). A funnel on `emit()` alone would leave the count
 *  stale on every status change; one on the dot alone would leave it stale after a
 *  chat closed.
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

/** Register the notification for a tab leaving the store. One slot, one
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
  internal.emptyTimer = setTimeout(() => {
    internal.emptyTimer = null;
    callbacks.onEmpty?.();
  }, 500);
}

// Snapshot of the persisted UI state, captured before the first save can
// overwrite it. Boot-time tab opens emit through the persistence subscriber
// BEFORE restoreTabState() runs, so without this capture the previous
// session's tab order / active view would be clobbered by the first
// intermediate save (B7) — reorderTabs would then "restore" the just-written
// insertion order and singleton tabs could never be restored.
let savedUIState: uiState.UIState | null = null;

function capturedUIState(): uiState.UIState {
  savedUIState ??= uiState.load();
  return savedUIState;
}

/** The tab-related UI state as persisted by the PREVIOUS session, immune to
 *  boot-time persistence writes. Used by app.ts to reopen singleton tabs
 *  before restoreTabState() applies the saved order + active view. */
export function getSavedTabState(): { tab_order: string[]; active_view: string } {
  const s = capturedUIState();
  return { tab_order: s.tab_order, active_view: s.active_view };
}

/** Drop persisted ids whose feature is no longer available, so a restore cannot
 *  reopen a page the user has no way to reach.
 *
 *  A singleton tab's entry point can disappear between sessions — History's
 *  toolbar button is hidden when chat retention is off, because nothing is kept
 *  to list. Restoring the tab anyway reopened a page with no button to reopen it
 *  and no reason to exist. An id absent from `available` is unconditional, which
 *  is every chat tab and every singleton that cannot be switched off.
 *
 *  The availability VERDICT stays with the caller: this module knows which ids
 *  come back, and app.ts knows what each feature currently depends on. Putting
 *  retention in here would make the tab store read settings. */
export function restorableSingletonIDs(
  ids: readonly string[],
  available: Readonly<Record<string, boolean>>,
): string[] {
  return ids.filter((id) => available[id] ?? true);
}

/** The id last announced on BUS_TAB_CHANGED. Module state rather than a closure
 *  so _resetForTest can clear it with the rest. */
let lastAnnouncedTab = "";

/** Register module-level subscribers. Extracted into a function so
 *  _resetForTest can re-register them after clearing the subscriber set.
 *  Effects defer their work until state has tabs — this matches the
 *  old subscribe-based pattern where callbacks only fired on emit
 *  (i.e. after at least one openTab call). */
function registerModuleSubscribers(): void {
  // Clear empty timer on any activation.
  tabsEffect(() => {
    if (state.active !== "" && internal.emptyTimer !== null) {
      clearTimeout(internal.emptyTimer);
      internal.emptyTimer = null;
    }
  });

  // Persistence.
  tabsEffect(() => {
    if (state.tabs.length === 0 && state.active === "") {
      return;
    }
    // Capture the previous session's state before the first overwrite.
    capturedUIState();
    uiState.save({
      // Top-level ids ONLY. A sub-tab's position is derived from its parent, so
      // persisting it would let a restore place a child away from its parent.
      tab_order: state.tabs.filter((t) => t.parentId === undefined).map((t) => t.id),
      // Pins are their own list rather than a prefix of tab_order: the order
      // already encodes where a pinned tab sits, but not that it is pinned, and
      // an unpin has to be able to leave the tab where it is.
      pinned_tabs: state.tabs
        .filter((t) => t.parentId === undefined && t.pinned === true)
        .map((t) => t.id),
      active_view: state.active,
    });
  });

  // View / route sync.
  tabsEffect((s) => {
    if (s.tabs.length === 0 && s.active === "") {
      return;
    }
    const active = s.tabs.find((t) => t.id === s.active);
    if (active !== undefined) {
      showView(active);
    } else {
      syncSidebarButtons(null);
    }
    // Announce a REAL switch. This effect re-runs on every store mutation, so
    // the guard is what separates "the active tab changed" from "something else
    // about the tabs did" — a subscriber that tears a feature down cannot be
    // handed the second one. Emitted here rather than inside showView because
    // showView's DOM swap runs inside a view transition, so its timing is not
    // the state change's.
    if (s.active !== lastAnnouncedTab) {
      lastAnnouncedTab = s.active;
      emitBus(BUS_TAB_CHANGED, { to: s.active, kind: active?.kind ?? null });
    }
  });

  // DOM rendering.
  tabsEffect(() => {
    if (state.tabs.length === 0 && state.active === "") {
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

/** Restore tabs to match saved order and active ID. Called once at startup
 *  after modules have registered their tabs. Reads the pre-boot snapshot
 *  (capturedUIState), NOT live localStorage — by the time this runs the
 *  boot-loop opens have already re-saved an intermediate state. */
export function restoreTabState(): void {
  const saved = capturedUIState();
  // Stamp the pins BEFORE the reorder. The partition runs inside reorderTabs, so
  // a pin applied after it would be recorded without moving its tab, and the
  // strip would come back out of order until the next mutation.
  const pins = new Set(saved.pinned_tabs);
  let pinned = false;
  for (const t of state.tabs) {
    if (t.parentId === undefined && pins.has(t.id)) {
      t.pinned = true;
      pinned = true;
    }
  }
  if (saved.tab_order.length > 0) {
    reorderTabs(saved.tab_order);
  } else if (pinned) {
    applyPinOrder();
    emit();
  }
  if (saved.active_view !== "" && hasTab(saved.active_view)) {
    activateTab(saved.active_view);
  }
}

// Wire drag-to-reorder callback.
setReorderCallback(reorderTabs);

// --- View / route (subscriber) ---

const ALL_VIEWS_SELECTOR = "[data-tab-view]";

/** Icon buttons that should show `.active` when their singleton tab is
 *  active. Non-singleton kinds (chat, plan, editor) are never in this
 *  map. */
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

// Boot fast-path flag (B3/E1): view swaps before the initial route is applied
// are bulk restores that shouldn't animate — and each queued startViewTransition
// serializes behind the previous one's `finished`, so N boot activations starve
// the deep-linked view's unhide for ~N crossfades (or forever when rendering is
// suspended). app.ts flips this right after applyInitialRoute().
let bootDone = false;

/** Mark boot restore complete — view swaps start animating from here on.
 *  Called once by app.ts right after the initial route is applied (and on
 *  login success for the unauthenticated boot path). */
export function markBootDone(): void {
  bootDone = true;
}

// Queued view transition: wraps DOM swaps so tab switches cross-fade without
// overlapping jank. Delegates to @cplieger/ui-primitives' viewTransition, which
// owns feature-detection, the serialization queue, the document.hidden
// fast-path, and the suspended-renderer watchdog (>= 2.1.2); kept as a
// void-returning wrapper so callers stay unchanged. One local fast-path runs
// the swap directly (un-queued, no animation): boot restores (markBootDone
// not yet called), see above.
function viewTransition(fn: () => void): void {
  if (!bootDone) {
    fn();
    return;
  }
  void uipViewTransition(fn);
}

function showView(tab: TabSpec): void {
  viewTransition(() => {
    for (const el of document.querySelectorAll(ALL_VIEWS_SELECTOR)) {
      (el as HTMLElement).classList.add("hidden");
    }
    document.querySelector(tab.view)?.classList.remove("hidden");
  });

  // Mobile toolbar title reads directly from the tab name.
  $.toolbarTitle.textContent = tab.kind === "chat" || tab.kind === "plan" ? "" : tab.name;

  // Close mobile sidebar after switching.
  $.sidebar.classList.remove("open");

  syncSidebarButtons(tab.kind);

  pushRoute(tab.route);
}

function renderDOM(): void {
  const list = $.tabList;
  if (!list.hasAttribute("role")) {
    list.setAttribute("role", "tablist");
  }

  const existing = new Map<string, HTMLElement>();
  for (const el of [...list.children]) {
    const id = (el as HTMLElement).dataset["tabId"];
    if (id !== undefined) {
      existing.set(id, el as HTMLElement);
    }
  }

  const activeIDs = new Set(state.tabs.map((t) => t.id));

  // Remove orphans with an exit animation.
  for (const [id, el] of existing) {
    if (!activeIDs.has(id)) {
      el.classList.add("exiting");
      el.addEventListener(
        "animationend",
        () => {
          el.remove();
        },
        { once: true },
      );
      existing.delete(id);
    }
  }

  // Insert + position. Skip over exiting elements when checking
  // whether a tab is already in the right spot — they're still in the
  // DOM (animating out) but shouldn't affect sibling ordering. Moving
  // a sibling past an exiting element would cause it to jump instead
  // of smoothly sliding up as the exiting element's height collapses.
  let prev: HTMLElement | null = null;
  for (const tab of state.tabs) {
    let el = existing.get(tab.id);
    if (el === undefined) {
      el = createTabEl(tab);
      if (prev !== null) {
        prev.after(el);
      } else {
        list.prepend(el);
      }
      el.classList.add("entering");
    } else {
      const nameEl = el.querySelector(".tab-name");
      if (nameEl !== null && nameEl.textContent !== tab.name) {
        nameEl.textContent = tab.name;
      }
      let expectedNext: ChildNode | null = prev !== null ? prev.nextSibling : list.firstChild;
      while (
        expectedNext !== null &&
        // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
        (expectedNext as HTMLElement).classList?.contains("exiting")
      ) {
        expectedNext = expectedNext.nextSibling;
      }
      if (el !== expectedNext) {
        if (prev !== null) {
          prev.after(el);
        } else {
          list.prepend(el);
        }
      }
    }
    el.classList.toggle("active", tab.id === state.active);
    el.setAttribute("aria-selected", tab.id === state.active ? "true" : "false");
    el.tabIndex = tab.id === state.active ? 0 : -1;
    // Sub-tab indent, from the SPEC. Keying this off the chat store's
    // parent_chat_id made the indent a chat-only feature and coupled the tab
    // module to chat state; `parentId` is generic and any kind can carry it.
    el.classList.toggle("tab-child", tab.parentId !== undefined);
    // Pin marker, from the spec like the indent. Toggled here rather than only at
    // creation so pinning an already-rendered tab shows immediately.
    el.classList.toggle("tab-pinned", tab.pinned === true);
    prev = el;
  }
}

function createTabEl(tab: TabSpec): HTMLElement {
  const node = el("div", {
    className: "tab",
    "data-tab-id": tab.id,
    "data-kind": tab.kind,
    role: "tab",
  });

  const name = el("span", { className: "tab-name" }, tab.name);

  const close = el(
    "button",
    { className: "tab-close", "aria-label": `Close ${tab.name}` },
    iconEl(ICON_CLOSE),
  );
  close.addEventListener("pointerup", (e) => {
    e.stopPropagation();
    closeTab(tab.id);
  });

  // The dot is decoration; `statusSR` is what a screen reader hears. Both ride
  // every row and paintDot writes both, so no node is added or removed as a
  // state changes — the same reasoning as the pin marker below.
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

  if (tab.kind === "chat") {
    // A chat tab LEADS with its activity dot, in the slot the per-mode role
    // glyph used to hold. That is the replacement, not a supplement: the strip
    // exists to say what is happening in the chats you are not looking at, and
    // a chat's role does not change between glances while its activity does.
    // The role still reads out on the mode pill and its picker; see the note on
    // TabSpec (the `iconSvg` field this removed).
    node.append(statusDot, name, statusSR, pin, close);
    // Painted from the SPEC, falling back to `idle`. The fallback is why the dot
    // is seeded at all rather than left blank for the store effect to fill: the
    // effect paints on a later tick, so an unseeded dot would leave the row one
    // frame narrower and shift its name. The spec value is what makes the row
    // honest as well as stable — a restored or rebuilt tab paints the state its
    // chat is actually in, instead of claiming idle until an unrelated update
    // repaints it.
    paintDot(node, tab.dotStatus ?? "idle");
  } else {
    // Every other kind keeps its glyph — none of them has an activity concept —
    // and uses the same element in the trailing slot for the editor's unsaved
    // mark, which is where it already was. No `idle` floor here: an editor tab
    // with nothing unsaved has no state to show.
    const icon = el("span", { className: "tab-icon" }, iconEl(ICONS[tab.kind]));
    node.append(icon, name, statusSR, pin, statusDot, close);
    paintDot(node, tab.dotStatus ?? "");
  }
  // A sub-tab is not independently draggable: its position is its parent's.
  // attachTabInteraction wires click/keyboard AND drag, so the flag rides along.
  attachTabInteraction(node, tab.id, tab.parentId === undefined);

  // Right-click context menu for chat tabs: pin/unpin or promote, then export
  // (md/json). Non-chat tabs keep the native browser menu.
  node.addEventListener("contextmenu", (e) => {
    if (tab.kind !== "chat") {
      return;
    }
    e.preventDefault();
    // Read the CURRENT spec: openTab replaces an already-open tab's spec with a
    // spread copy, so the closure's reference can be one generation behind on
    // exactly the fields this menu reports.
    const spec = state.tabs.find((t) => t.id === tab.id) ?? tab;
    const items: ContextMenuItem[] = [];
    if (spec.parentId === undefined) {
      const pinned = spec.pinned === true;
      items.push({
        label: pinned ? "Unpin" : "Pin",
        action: () => {
          setTabPinned(tab.id, !pinned);
        },
      });
    } else {
      items.push({
        label: "Promote to its own tab",
        action: () => {
          promoteTab(tab.id);
        },
      });
    }
    items.push(
      {
        label: "Export as Markdown",
        action: () => {
          downloadChatExport(tab.id, tab.name, "md");
        },
      },
      {
        label: "Export as JSON",
        action: () => {
          downloadChatExport(tab.id, tab.name, "json");
        },
      },
    );
    showContextMenu(items, { x: e.clientX, y: e.clientY });
  });

  return node;
}

// --- Interaction (click, middle-click, drag, keyboard) ---

function attachTabInteraction(el: HTMLElement, id: string, draggable: boolean): void {
  // Click to activate (any target outside .tab-close).
  el.addEventListener("pointerup", (e) => {
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
  el.addEventListener("auxclick", (e) => {
    if (e.button === 1) {
      e.preventDefault();
      closeTab(id);
    }
  });

  // Keyboard navigation: Enter/Space activates, Delete closes, arrows move focus.
  el.addEventListener("keydown", (e) => {
    switch (e.key) {
      case "Enter":
      case " ":
        e.preventDefault();
        activateTab(id);
        break;
      case "Delete":
      case "Backspace":
        e.preventDefault();
        closeTab(id);
        break;
      case "ArrowDown":
      case "ArrowRight":
      case "ArrowUp":
      case "ArrowLeft":
      case "Home":
      case "End": {
        e.preventDefault();
        const tabs = [...(el.parentElement?.children ?? [])] as HTMLElement[];
        const i = tabs.indexOf(el);
        let target: HTMLElement | undefined;
        if (e.key === "ArrowDown" || e.key === "ArrowRight") {
          target = tabs[i + 1] ?? tabs[0];
        } else if (e.key === "ArrowUp" || e.key === "ArrowLeft") {
          target = tabs[i - 1] ?? tabs[tabs.length - 1];
        } else if (e.key === "Home") {
          target = tabs[0];
        } else {
          target = tabs[tabs.length - 1];
        }
        target?.focus();
        break;
      }
    }
  });

  if (draggable) {
    attachDrag(el);
  }
}

// --- Singleton tab helpers ---

/** Open or toggle a singleton (always-one-instance) tab. If it's already the
 *  active tab, close it; otherwise open/activate. */
function toggleSingleton(spec: TabSpec): void {
  if (hasTab(spec.id) && state.active === spec.id) {
    closeTab(spec.id);
    return;
  }
  openTab(spec);
}

/** Toggle the Settings tab. If already open and active, close; otherwise
 *  open/activate, landing on the given tab (default: General). */
export function toggleSettingsView(tab: SettingsTab = "general", onShow?: () => void): void {
  toggleSingleton({
    id: TAB_SETTINGS,
    name: "Settings",
    kind: "settings",
    view: TAB_VIEWS.settings,
    route: { kind: "settings", tab },
    onShow,
  });
}

/** Switch the Settings panel to a specific tab. No-op if Settings is not
 *  currently open. Used by router-driven navigation where we don't want
 *  to toggle — only change the inner tab. */
export function setSettingsTab(tab: SettingsTab): void {
  const spec = state.tabs.find((t) => t.id === TAB_SETTINGS);
  if (spec === undefined) {
    return;
  }
  spec.route = { kind: "settings", tab };
  if (state.active === TAB_SETTINGS) {
    emit();
  }
}

/** Switch the git view's sub-tab route. No-op if the git view isn't open.
 *  Mirrors setSettingsTab: keeps the __git__ TabSpec route in sync so every
 *  emit (and thus showView → pushRoute) reflects the active sub-tab rather
 *  than resetting the URL to /git. */
export function setGitTab(tab: GitTab): void {
  const spec = state.tabs.find((t) => t.id === TAB_GIT);
  if (spec === undefined) {
    return;
  }
  spec.route = { kind: "git", tab };
  if (state.active === TAB_GIT) {
    emit();
  }
}

export function toggleGitView(tab: GitTab = "changes", onShow?: () => void): void {
  toggleSingleton({
    id: TAB_GIT,
    name: "Source Control",
    kind: "git",
    view: TAB_VIEWS.git,
    route: { kind: "git", tab },
    onShow,
  });
}

export function toggleFilesView(onShow: () => void, onClose?: () => void): void {
  toggleSingleton({
    id: TAB_FILES,
    name: "Files",
    kind: "files",
    view: TAB_VIEWS.files,
    route: { kind: "files", path: "." },
    onShow,
    onClose,
  });
}

export function toggleHistoryView(onShow: () => void, onClose?: () => void): void {
  toggleSingleton({
    id: TAB_HISTORY,
    name: "History",
    kind: "history",
    view: TAB_VIEWS.history,
    route: { kind: "history" },
    onShow,
    onClose,
  });
}

/** Switch the docs browser's sub-tab route. No-op when it isn't open. Mirrors
 *  setSettingsTab: keeps the __docs__ TabSpec route in sync so every emit (and
 *  thus showView → pushRoute) reflects the active sub-tab rather than resetting
 *  the URL to /docs. */
export function setDocsTab(tab: DocsTab): void {
  const spec = state.tabs.find((t) => t.id === TAB_DOCS);
  if (spec === undefined) {
    return;
  }
  spec.route = { kind: "docs", tab };
  if (state.active === TAB_DOCS) {
    emit();
  }
}

/** Toggle the Kiro configuration browser, landing on the given sub-tab. */
export function toggleDocsView(tab: DocsTab = "steering", onShow?: () => void): void {
  toggleSingleton({
    id: TAB_DOCS,
    name: "Kiro docs",
    kind: "docs",
    view: TAB_VIEWS.docs,
    route: { kind: "docs", tab },
    onShow,
  });
}

/** Open (or focus) the read-only review tab for one previous workflow run.
 *  Not a singleton: several runs can be reviewed side by side, keyed by id.
 *  Closing it closes nothing else — a finished run has nothing to kill. */
export function openRunTab(
  workflowID: string,
  name: string,
  onShow: () => void,
  opts?: { onClose?: () => void },
): void {
  openTab({
    id: `run:${workflowID}`,
    name,
    kind: "run",
    view: TAB_VIEWS.run,
    route: { kind: "run", id: workflowID },
    onShow,
    onClose: opts?.onClose,
  });
}

/** The tab id for an open editor file.
 *
 *  Here rather than at each caller because the convention was being composed and
 *  tested by hand in three modules (this one, the editor's dirty-indicator
 *  binding, and the reconnect-gap handler's tab reconcile), which is one string
 *  literal away from a silent mismatch. */
export function editorTabID(filePath: string): string {
  return EDITOR_TAB_PREFIX + filePath;
}

/** Whether a tab id names an editor tab. */
export function isEditorTabID(id: string): boolean {
  return id.startsWith(EDITOR_TAB_PREFIX);
}

export function openEditorView(
  filePath: string,
  onShow: () => void,
  onClose?: () => void,
  opts?: { activate?: boolean },
): void {
  const id = editorTabID(filePath);
  const name = filePath.split("/").pop() ?? filePath;
  openTab(
    {
      id,
      name,
      kind: "editor",
      view: TAB_VIEWS.editor,
      route: { kind: "file", path: filePath },
      onShow,
      onClose,
    },
    opts,
  );
}

// --- Test helpers (no-op in production; used by tabs.test.ts) ---

/** Reset all tab state. Exported for test isolation only. */
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
  savedUIState = null;
  bootDone = false;
  lastAnnouncedTab = "";
  // Dispose existing module effects and re-register so tests observe
  // the same side-effects (persistence, view sync, DOM render) as
  // production.
  for (const c of moduleEffects) {
    c();
  }
  moduleEffects.length = 0;
  registerModuleSubscribers();
}

// Register module-level effects after all module declarations are
// initialized — effect() bodies run synchronously on subscribe, so
// they must run AFTER ACTIVE_BTN, syncSidebarButtons, renderDOM, and
// showView are defined further down the file.
registerModuleSubscribers();
