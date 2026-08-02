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
import type { Route, SettingsTab, GitTab } from "./router.js";
import {
  ICON_CLOSE,
  ICON_TAB_CHAT,
  ICON_TAB_PLAN,
  ICON_TAB_SETTINGS,
  ICON_TAB_GIT,
  ICON_TAB_FILES,
  ICON_TAB_EDITOR,
  ICON_TAB_HISTORY,
} from "./icons.js";
import { iconEl } from "./icon-el.js";
import * as uiState from "./ui-state.js";
import { $ } from "./dom.js";
import { viewTransition as uipViewTransition } from "@cplieger/ui-primitives/view-transition";
import { signal, effect, el } from "@cplieger/reactive";
import { get as storeGet } from "./store.js";
import { attachDrag, isDragHandled, setReorderCallback } from "./tabs-drag.js";
import { promoteRewindChat, discardRewindChat } from "./actions/rewind.js";
import { showContextMenu } from "./context-menu.js";
import type { ContextMenuItem } from "./context-menu.js";
import { downloadChatExport } from "./chat-export.js";
import { confirm as confirmDialog } from "./confirm.js";

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
}

// --- Singleton tab IDs (single source of truth) ---

const TAB_SETTINGS = "__settings__";
const TAB_GIT = "__git__";
const TAB_FILES = "__files__";
const TAB_HISTORY = "__history__";

const ICONS: Readonly<Record<TabKind, string>> = {
  chat: ICON_TAB_CHAT,
  plan: ICON_TAB_PLAN,
  settings: ICON_TAB_SETTINGS,
  git: ICON_TAB_GIT,
  files: ICON_TAB_FILES,
  editor: ICON_TAB_EDITOR,
  history: ICON_TAB_HISTORY,
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
  state.tabs.push(spec);
  if (opts?.activate === false) {
    emit();
    return;
  }
  activateTab(spec.id);
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
 *  to avoid re-dispatching archive/delete actions against a stale session. */
export function closeTab(id: string, opts?: { skipOnClose?: boolean }): void {
  const idx = state.tabs.findIndex((t) => t.id === id);
  if (idx < 0) {
    return;
  }
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const tab = state.tabs[idx]!;

  // Check if this tab has rewind children. If so, ask the user.
  const children = state.tabs.filter((t) => {
    const s = storeGet(t.id);
    return s?.parent_chat_id === id;
  });
  if (children.length > 0 && !opts?.skipOnClose) {
    showRewindChildPrompt(
      id,
      children.map((t) => t.id),
      opts,
    );
    return;
  }

  if (!opts?.skipOnClose) {
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

/** Show a popup asking the user what to do with rewind children when
 *  their parent tab is closed. "Keep" promotes them to top-level;
 *  "Discard" closes them alongside the parent. */
function showRewindChildPrompt(
  parentId: string,
  childIds: string[],
  opts?: { skipOnClose?: boolean },
): void {
  const msg = `This chat has ${String(childIds.length)} rewind branch${childIds.length > 1 ? "es" : ""}. Discard all branches?`;
  void confirmDialog(msg, "Discard all", "destructive").then((confirmed) => {
    if (!confirmed) {
      return;
    }
    // Discard children: dispatch server-side delete for each, then close tabs.
    for (const cid of childIds) {
      void discardRewindChat.dispatch({ chatID: cid });
      closeTab(cid, { skipOnClose: true });
    }
    // Now close the parent.
    closeTab(parentId, { ...opts, skipOnClose: false });
  });
}

export function renameTab(id: string, name: string): void {
  const tab = state.tabs.find((t) => t.id === id);
  if (tab === undefined || tab.name === name) {
    return;
  }
  tab.name = name;
  emit();
}

/** Set a status indicator on a tab: "thinking" (pulsing accent dot),
 *  "permission" (amber dot), "waiting" (pulsing amber dot — the agent
 *  declared waiting_on_user and needs input), or "" to clear. */
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
  for (const id of order) {
    const t = byID.get(id);
    if (t !== undefined) {
      next.push(t);
      byID.delete(id);
    }
  }
  // Preserve any unknown tabs at the end (defensive; order drift).
  for (const t of byID.values()) {
    next.push(t);
  }
  state.tabs = next;
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
      tab_order: state.tabs.map((t) => t.id),
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
  if (saved.tab_order.length > 0) {
    reorderTabs(saved.tab_order);
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
    // Rewind child indent: chats with parent_chat_id render indented.
    const session = storeGet(tab.id);
    el.classList.toggle(
      "tab-rewind-child",
      session?.parent_chat_id !== undefined && session.parent_chat_id !== "",
    );
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

  node.append(icon, name, statusDot, close);
  attachTabInteraction(node, tab.id);

  // Right-click context menu for chat tabs: export (md/json), plus Promote
  // for rewind children. Non-chat tabs keep the native browser menu.
  node.addEventListener("contextmenu", (e) => {
    if (tab.kind !== "chat") {
      return;
    }
    e.preventDefault();
    const s = storeGet(tab.id);
    const items: ContextMenuItem[] = [
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
    ];
    if (s?.parent_chat_id !== undefined && s.parent_chat_id !== "") {
      items.push({
        label: "Promote (replace original)",
        action: () => {
          void promoteRewindChat.dispatch({ chatID: s.id });
        },
      });
    }
    showContextMenu(items, { x: e.clientX, y: e.clientY });
  });

  return node;
}

// --- Interaction (click, middle-click, drag, keyboard) ---

function attachTabInteraction(el: HTMLElement, id: string): void {
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

  attachDrag(el);
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
