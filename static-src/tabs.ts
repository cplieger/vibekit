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

type TabKind = keyof typeof TAB_VIEWS;

/** Everything needed to render and route a tab. */
export interface TabSpec {
  id: string;
  name: string;
  kind: TabKind;
  /** Optional per-tab icon (SVG string) overriding the kind's default.
   *  Used to give chat tabs a per-agent-role glyph. */
  iconSvg?: string | undefined;
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
const callbacks: Callbacks = { onActivate: null, onEmpty: null };
const internal: Internal = { emptyTimer: null, renderQueued: false };

/** Reactive version counter. Effects subscribed via `tabsEffect()` re-run
 *  on every emit(). State is mutated in place; this counter is the
 *  signal those mutations trip. */
const stateVersion = signal(0);
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

  // `owns: false` tears down nothing: the tab is a view, and dismissing a view
  // must not kill the work it was watching.
  if (!opts?.skipOnClose && tab.owns !== false) {
    tab.onClose?.();
  }
  state.tabs.splice(idx, 1);

  if (state.active === id) {
    const next = state.tabs[Math.min(idx, state.tabs.length - 1)];
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

/** Set a status indicator on a tab: "thinking" (accent disc with the glow beat +
 *  travelling wave), "permission" (still yellow disc in a hard ring), "waiting"
 *  (the same beat + wave in yellow — the agent declared waiting_on_user and
 *  needs input), or "" to clear. The visual vocabulary is shared with
 *  @cplieger/web-terminal-ui's .wt-status-dot; see css/61-mcp-tools.css. */
export function setTabStatus(id: string, status: "" | "thinking" | "permission" | "waiting"): void {
  const el = document.querySelector(`[data-tab-id="${CSS.escape(id)}"] .tab-status-dot`);
  if (el === null) {
    return;
  }
  el.classList.toggle("hidden", status === "");
  el.classList.toggle("tab-dot-thinking", status === "thinking");
  el.classList.toggle("tab-dot-permission", status === "permission");
  el.classList.toggle("tab-dot-waiting", status === "waiting");
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

/** Mark an editor tab as having unsaved changes (a steady accent dot).
 *  Reuses the shared .tab-status-dot: editor tabs never receive setTabStatus
 *  (thinking/permission are chat-tab states), so there is no contention. */
export function setTabDirty(id: string, dirty: boolean): void {
  const el = document.querySelector(`[data-tab-id="${CSS.escape(id)}"] .tab-status-dot`);
  if (el === null) {
    return;
  }
  el.classList.toggle("hidden", !dirty);
  el.classList.toggle("tab-dot-dirty", dirty);
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
/** Return all open tab IDs. Used by system handlers to reconcile tabs
 *  without DOM scraping. */
export function getOpenTabIDs(): string[] {
  return state.tabs.map((t) => t.id);
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

/** Swap a chat tab's icon (SVG string) in place — used when the chat's
 *  agent/role changes on an empty chat. Updates both the live DOM node and
 *  the stored spec so a later re-render keeps the new icon. No-op if the
 *  tab isn't open or the icon is unchanged. */
export function setTabIcon(id: string, svg: string): void {
  const spec = state.tabs.find((t) => t.id === id);
  if (spec === undefined || spec.iconSvg === svg) {
    return;
  }
  spec.iconSvg = svg;
  for (const child of $.tabList.children) {
    if ((child as HTMLElement).dataset["tabId"] === id) {
      child.querySelector(".tab-icon")?.replaceChildren(iconEl(svg));
      break;
    }
  }
}

function createTabEl(tab: TabSpec): HTMLElement {
  const node = el("div", {
    className: "tab",
    "data-tab-id": tab.id,
    "data-kind": tab.kind,
    role: "tab",
  });

  const icon = el("span", { className: "tab-icon" }, iconEl(tab.iconSvg ?? ICONS[tab.kind]));

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

  const statusDot = el("span", { className: "tab-status-dot hidden" });

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

  node.append(icon, name, pin, statusDot, close);
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

export function openEditorView(
  filePath: string,
  onShow: () => void,
  onClose?: () => void,
  opts?: { activate?: boolean },
): void {
  const id = `editor:${filePath}`;
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
  if (internal.emptyTimer !== null) {
    clearTimeout(internal.emptyTimer);
  }
  internal.emptyTimer = null;
  internal.renderQueued = false;
  savedUIState = null;
  bootDone = false;
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
