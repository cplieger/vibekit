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
import type { Route, SettingsTab } from "./router.js";
import {
  ICON_CLOSE,
  ICON_TAB_CHAT,
  ICON_TAB_PLAN,
  ICON_TAB_SETTINGS,
  ICON_TAB_GIT,
  ICON_TAB_FILES,
  ICON_TAB_EDITOR,
  ICON_TAB_FOLLOW,
  ICON_TAB_HISTORY,
} from "./icons.js";
import * as uiState from "./ui-state.js";
import { $ } from "./dom.js";
import { signal, effect } from "./signals.js";
import { get as storeGet } from "./store.js";
import { attachDrag, isDragHandled, setReorderCallback } from "./tabs-drag.js";

// --- Types ---

type TabKind = "chat" | "plan" | "settings" | "git" | "files" | "editor" | "follow" | "history";

/** Everything needed to render and route a tab. */
export interface TabSpec {
  id: string;
  name: string;
  kind: TabKind;
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
  follow: ICON_TAB_FOLLOW,
  history: ICON_TAB_HISTORY,
};

/** Parse an SVG string into a DOM node via DOMParser.
 *  Used instead of innerHTML to avoid XSS surface in the tab rendering path. */
const svgParser = new DOMParser();
function iconEl(svg: string): Node {
  const doc = svgParser.parseFromString(svg, "image/svg+xml");
  return document.importNode(doc.documentElement, true);
}

/** Default view selector for each tab kind. Callers can omit `view` from
 *  TabSpec when the standard mapping applies. */
export const TAB_VIEWS: Record<TabKind, string> = {
  chat: "#chat-view",
  plan: "#chat-view",
  settings: "#settings-view",
  git: "#git-view",
  files: "#files-view",
  editor: "#editor-view",
  follow: "#follow-view",
  history: "#history-view",
};

// --- Store ---

interface Callbacks {
  onActivate: ((id: string) => void) | null;
  onEmpty: (() => void) | null;
}

interface Internal {
  emptyTimer: ReturnType<typeof setTimeout> | null;
  vtPending: Promise<void> | null;
  renderQueued: boolean;
}

interface State {
  tabs: TabSpec[];
  active: string;
}

const state: State = { tabs: [], active: "" };
const callbacks: Callbacks = { onActivate: null, onEmpty: null };
const internal: Internal = { emptyTimer: null, vtPending: null, renderQueued: false };

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

/** Open a tab (or activate it if already open). */
export function openTab(spec: TabSpec): void {
  const idx = state.tabs.findIndex((t) => t.id === spec.id);
  if (idx >= 0) {
    // Tab already open — only callbacks (onShow/onClose) are updated;
    // name, kind, view, and route remain unchanged from the original open.
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    state.tabs[idx] = { ...state.tabs[idx]!, onShow: spec.onShow, onClose: spec.onClose };
    activateTab(spec.id);
    return;
  }
  state.tabs.push(spec);
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
  const overlay = document.createElement("div");
  overlay.className = "modal-overlay";
  const card = document.createElement("div");
  card.className = "modal-card";

  const p = document.createElement("p");
  p.style.cssText = "margin:0 0 var(--sp-3)";
  p.textContent = `This chat has ${String(childIds.length)} rewind branch${childIds.length > 1 ? "es" : ""}. What would you like to do?`;

  const btnRow = document.createElement("div");
  btnRow.style.cssText = "display:flex;gap:var(--sp-2);justify-content:flex-end";

  const keepBtn = document.createElement("button");
  keepBtn.className = "btn-secondary";
  keepBtn.dataset["action"] = "keep";
  keepBtn.textContent = "Keep branches";

  const discardBtn = document.createElement("button");
  discardBtn.className = "btn-danger";
  discardBtn.dataset["action"] = "discard";
  discardBtn.textContent = "Discard all";

  const cancelBtn = document.createElement("button");
  cancelBtn.className = "btn-ghost";
  cancelBtn.dataset["action"] = "cancel";
  cancelBtn.textContent = "Cancel";

  btnRow.append(keepBtn, discardBtn, cancelBtn);
  card.append(p, btnRow);
  overlay.appendChild(card);
  document.body.appendChild(overlay);

  card.addEventListener("click", (e) => {
    const action = (e.target as HTMLElement).dataset["action"];
    if (!action) {
      return;
    }
    overlay.remove();
    if (action === "cancel") {
      return;
    }
    if (action === "keep") {
      // Promote all children (clear parent_chat_id via server command).
      for (const cid of childIds) {
        void import("./transport.js").then(({ send }) => {
          void send({
            type: "promote_rewind_chat",
            chat_id: cid,
            request_id: `promote-${Date.now()}`,
          });
        });
      }
    } else {
      // Discard children: dispatch server-side delete for each, then close tabs.
      for (const cid of childIds) {
        void import("./transport.js").then(({ send }) => {
          void send({
            type: "discard_rewind_chat",
            chat_id: cid,
            request_id: `discard-${Date.now()}`,
          });
        });
        closeTab(cid, { skipOnClose: true });
      }
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
 *  "permission" (amber dot), or "" to clear. */
export function setTabStatus(id: string, status: "" | "thinking" | "permission"): void {
  const el = document.querySelector(`[data-tab-id="${CSS.escape(id)}"] .tab-status-dot`);
  if (el === null) {
    return;
  }
  el.classList.toggle("hidden", status === "");
  el.classList.toggle("tab-dot-thinking", status === "thinking");
  el.classList.toggle("tab-dot-permission", status === "permission");
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
/** Return all open tab IDs. Used by system handlers to reconcile tabs
 *  without DOM scraping. */
export function getOpenTabIDs(): string[] {
  return state.tabs.map((t) => t.id);
}

// --- Activation listener ---
export function setOnActivate(fn: (id: string) => void): void {
  callbacks.onActivate = fn;
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
 *  after modules have registered their tabs. */
export function restoreTabState(): void {
  const saved = uiState.load();
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

// Queued view transition: wraps DOM swaps in startViewTransition so
// tab switches get a cross-fade. Queue prevents overlapping jank.
function viewTransition(fn: () => void): void {
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
  if (!document.startViewTransition) {
    fn();
    return;
  }
  const run = (): void => {
    const t = document.startViewTransition(fn);
    // Chain catch on the stored promise so its rejection is handled
    // even if `t.finished` rejects (browsers can skip transitions).
    internal.vtPending = t.finished
      .then(() => {
        internal.vtPending = null;
      })
      .catch(() => {
        internal.vtPending = null;
      });
    t.ready.catch(() => {
      /* noop */
    });
  };
  if (internal.vtPending !== null) {
    void internal.vtPending.then(run);
  } else {
    run();
  }
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

function createTabEl(tab: TabSpec): HTMLElement {
  const el = document.createElement("div");
  el.className = "tab";
  el.dataset["tabId"] = tab.id;
  el.dataset["kind"] = tab.kind;
  el.setAttribute("role", "tab");

  const icon = document.createElement("span");
  icon.className = "tab-icon";
  icon.replaceChildren(iconEl(ICONS[tab.kind]));

  const name = document.createElement("span");
  name.className = "tab-name";
  name.textContent = tab.name;

  const close = document.createElement("button");
  close.className = "tab-close";
  close.replaceChildren(iconEl(ICON_CLOSE));
  close.setAttribute("aria-label", `Close ${tab.name}`);
  close.addEventListener("pointerup", (e) => {
    e.stopPropagation();
    closeTab(tab.id);
  });

  const statusDot = document.createElement("span");
  statusDot.className = "tab-status-dot hidden";

  el.append(icon, name, statusDot, close);
  attachTabInteraction(el, tab.id);

  // Right-click "Promote" for rewind children.
  el.addEventListener("contextmenu", (e) => {
    const s = storeGet(tab.id);
    if (!s?.parent_chat_id) {
      return;
    }
    e.preventDefault();
    const menu = document.createElement("div");
    menu.className = "tab-context-menu";
    menu.style.position = "absolute";
    menu.style.left = `${e.clientX}px`;
    menu.style.top = `${e.clientY}px`;
    const btn = document.createElement("button");
    btn.textContent = "Promote (replace original)";
    btn.className = "tab-context-item";
    btn.addEventListener("click", () => {
      menu.remove();
      void import("./transport.js").then(({ send }) => {
        void send({
          type: "promote_rewind_chat",
          chat_id: s.id,
          request_id: `promote-${Date.now()}`,
        });
      });
    });
    menu.appendChild(btn);
    document.body.appendChild(menu);
    const dismiss = (): void => {
      menu.remove();
      document.removeEventListener("pointerdown", dismiss);
      document.removeEventListener("keydown", escDismiss);
    };
    const escDismiss = (ev: KeyboardEvent): void => {
      if (ev.key === "Escape") {
        dismiss();
      }
    };
    setTimeout(() => {
      document.addEventListener("pointerdown", dismiss);
      document.addEventListener("keydown", escDismiss);
    }, 0);
  });

  return el;
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

export function toggleGitView(onShow: () => void): void {
  toggleSingleton({
    id: TAB_GIT,
    name: "Source Control",
    kind: "git",
    view: TAB_VIEWS.git,
    route: { kind: "git" },
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

export function openEditorView(filePath: string, onShow: () => void, onClose?: () => void): void {
  const id = `editor:${filePath}`;
  const name = filePath.split("/").pop() ?? filePath;
  openTab({
    id,
    name,
    kind: "editor",
    view: TAB_VIEWS.editor,
    route: { kind: "file", path: filePath },
    onShow,
    onClose,
  });
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
  internal.vtPending = null;
  internal.renderQueued = false;
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
