// ---------------------------------------------------------------------------
// App: orchestrator. Wires modules, owns the singleton SessionStore,
// registers SSE handlers, and handles auth + initial route.
//
// Server is the source of truth. Sending a prompt posts a command; the
// server broadcasts SSE events that drive all rendering. No optimistic
// local mutations.
// ---------------------------------------------------------------------------

import type { ServerEvent, ModelInfo } from "./types.js";
import type { WhoamiResponse } from "./wire/types.gen.js";
import {
  MODEL_CONTEXT_SIZES, parseContextSize, contextSizeFor, sessionsVersion,
  getActiveId, getActive, get, getSessions, isThinking,
  loadList,
} from "./store.js";
import { effect } from "./signals.js";
import { dispatch } from "./bus.js";
import { $ } from "./dom.js";
import { guardAction, initSidebarSwipe } from "./platform.js";
import * as transport from "./transport.js";
import { loadSettings, restoreAll, initUI, setUserEmail } from "./settings.js";
import { apiGet } from "./api-client.js";
import {
  setOnEmpty, restoreTabState, getActiveTabId, activateTab,
  openTab, TAB_VIEWS,
} from "./tabs.js";
import { parseRoute, replaceRoute, onPopState, suppressPush } from "./router.js";
import { chatSkeleton } from "./skeleton.js";
import type { Route, SettingsTab } from "./router.js";
import { refreshPickerIfVisible, setPickerModels } from "./picker.js";
import { setStatus } from "./status.js";
import { initShellPanel } from "./shell.js";
import { showLoginModal, hideLoginModal, initLoginModal } from "./modals.js";
import { initEditor } from "./editor-core.js";
import { openFile } from "./editor-openers.js";
import { initFileBrowser, loadFileBrowser, restoreFileBrowser } from "./files.js";
import { initFilePicker } from "./files-picker.js";
import { initChatAttach } from "./files-drop.js";
import { initTaskListPill } from "./task-list.js";
import { initAwaySummary } from "./away-summary.js";
import { initAgentTerminals } from "./agent-terminal.js";
import { initTooltips } from "./tooltip.js";
import { initHistory } from "./history.js";
import { isRetentionEnabled, onRetentionChange, refreshRetention } from "./retention.js";
import { initKeyboardShortcuts } from "./keys.js";
import { loadToolsList } from "./tools.js";
import { loadKiroConfig } from "./kiro-config.js";
import { forceSettingsTab } from "./settings-tabs.js";
import { loadGitRepos } from "./git.js";
import { restoreLastModel } from "./session-context.js";
import {
  openChatTab, createSession, createPlannerSession, switchSession,
  sendPrompt, installStoreSubscribers,
} from "./chat.js";
import { initModelSwitcher } from "./model-switcher.js";
import { initFollowAlong } from "./follow.js";
import { initAutoApprove } from "./auto-approve.js";
import { initSupervisedPill } from "./supervised-pill.js";
import { makeExpandable } from "./pill-expand.js";
import { initPromptInput } from "./prompt-input.js";
// commands-menu stripped — slash commands replaced by dedicated UI buttons
import { refreshContextUI } from "./context-ui.js";
import { registerAllSSEDecoders } from "./wire/registry.gen.js";
import { applyShareTarget } from "./share-target.js";

import "./handlers/index.js";
import { wireCheckpointRestore } from "./handlers/turn.js";
import { cancelTurn } from "./actions/chat.js";
import { subscribeToActions, pendingCount } from "./actions/index.js";
// Register the conflict SSE handler at startup so badges land
// without the user having to first open the chat that triggered
// them. The module is small; the side-effect import is worth the
// immediacy.
import "./conflicts.js";

const noop = (): void => {};

function dismissLoadingScreen(): void {
  document.getElementById("app-loading")?.remove();
  $.appRoot.classList.remove("app-hidden");
}

// ============================================================
// Init
// ============================================================

function init(): void {
  setOnEmpty(() => createSession());

  // Register SSE payload decoders before opening the transport.
  // Decoders run in transport.ts before each event reaches dispatch();
  // an event whose payload fails validation is dropped (with a
  // structured console.error) instead of feeding handlers a partial
  // shape. The decoder set is generated from Go structs by
  // cmd/wire-codegen — see wire/registry.gen.ts.
  registerAllSSEDecoders();

  transport.init(
    (evt: ServerEvent) => { dispatch(evt); },
    (status) => {
      setStatus(status);
      if (status === "connected") void loadList();
    },
  );

  installStoreSubscribers();

  // Refresh picker whenever the active session's available_models
  // shifts. Models come both from a pre-conversation REST fetch at
  // startup (kiro-cli chat --list-models) and per-session from the
  // ACP bridge's session/new response; this listener is the live
  // update path — session-sourced lists are authoritative and
  // overwrite whatever the REST path seeded.
  let lastModelSig = "";
  effect(() => {
    sessionsVersion.value;
    const active = getActive();
    if (active === undefined) return;
    const sig = active.id + ":" + active.available_models.map(m => m.id).join(",");
    if (sig !== lastModelSig) {
      lastModelSig = sig;
      fetchModelsFromSession();
    }
  });

  setupInput();
  initUI();
  initShellPanel();
  initEditor();
  initFileBrowser();
  initFilePicker();
  initChatAttach();
  initTaskListPill();
  // Wire toolbar history button. Hidden when retention is 0 (no archive).
  $.historyBtn.addEventListener("click", () => {
    void import("./history.js").then(({ showHistoryView }) => showHistoryView()).catch(() => {});
  });
  // Sync history button visibility with retention setting.
  const syncHistoryBtn = (): void => {
    $.historyBtn.classList.toggle("hidden", !isRetentionEnabled());
  };
  onRetentionChange(syncHistoryBtn);
  syncHistoryBtn();
  initAwaySummary();
  initAgentTerminals();
  initTooltips();
  initHistory();
  initLoginModal(onLoginSuccess);
  initSidebarSwipe(
    $.chatArea,
    $.sidebar,
  );
  initKeyboardShortcuts({
    newChat: () => { createSession(); $.sidebar.classList.remove("open"); },
    toggleShell: () => $.shellBtn.click(),
    toggleFiles: () => $.filesBtn.click(),
    toggleGit: () => $.gitBtn.click(),
    toggleSettings: () => $.settingsBtn.click(),
    sendMessage: () => $.promptForm.dispatchEvent(new Event("submit")),
  });

  // Action-framework global: live-log every action error to the
  // browser console so failures are visible in DevTools regardless of
  // toast policy (suppressed-toast actions still get logged).
  // Inlined from a former actions/console-log.ts module — single boot
  // wiring, not worth a separate module.
  subscribeToActions((inst) => {
    if (inst.status !== "error" || inst.error === undefined) return;
    const meta: string[] = [];
    if (inst.completedAt !== undefined) meta.push(`${String(inst.completedAt - inst.startedAt)}ms`);
    if (inst.attempts !== undefined && inst.attempts > 1) meta.push(`${String(inst.attempts)} attempts`);
    if (inst.error.status !== undefined) meta.push(`HTTP ${String(inst.error.status)}`);
    if (inst.error.code !== undefined) meta.push(inst.error.code);
    console.error(
      `[action] ${inst.name} failed (${meta.join(", ")}): ${inst.error.message}`,
      inst.error,
    );
  });

  // Global progress indicator: toggle a CSS class on the 2px top
  // stripe whenever any action is in-flight. Edge-only toggling
  // (0→N and N→0) avoids flicker from rapid intermediate changes.
  // The falling edge is debounced by 200ms so very-fast actions
  // (0→1→0 within a single frame) still produce a visible flash.
  const progressEl = document.getElementById("global-progress");
  if (progressEl !== null) {
    let wasActive = false;
    let offTimer: ReturnType<typeof setTimeout> | undefined;
    subscribeToActions(() => {
      const active = pendingCount() > 0;
      if (active !== wasActive) {
        wasActive = active;
        if (active) {
          if (offTimer !== undefined) { clearTimeout(offTimer); offTimer = undefined; }
          progressEl.classList.add("active");
        } else {
          offTimer = setTimeout(() => { offTimer = undefined; progressEl.classList.remove("active"); }, 200);
        }
      }
    });
  }

  void checkAuthAndStart();
}

async function checkAuthAndStart(): Promise<void> {
  const settings = await loadSettings();
  restoreLastModel(settings.last_model);

  suppressPush(true);
  try { restoreAll(settings); } catch { /* best-effort */ }
  suppressPush(false);

  let authenticated = false;
  const d = await apiGet<WhoamiResponse>("/api/whoami");
  if (d !== null) {
    const email = d.email;
    if (email !== undefined && email !== "") {
      setUserEmail(email);
      authenticated = true;
    }
  }

  if (!authenticated) {
    setUserEmail("");
    showLoginModal();
    return;
  }

  dismissLoadingScreen();

  // Pre-conversation catalog so the picker has content before the
  // first chat's session/new lands. Fire-and-forget; session-sourced
  // updates overwrite this the moment a bridge spawns.
  void fetchModelsFromREST();
  // Read retention setting so tab-close knows whether to archive or delete.
  void refreshRetention();

  const skel = chatSkeleton();
  $.messages.appendChild(skel);
  wireCheckpointRestore($.messages);

  suppressPush(true);
  // If share-target intends to create a session (e.g. ?agent=planner),
  // skip the default empty-state createSession so we don't end up with
  // an unused "New conversation" tab next to the planner.
  const wantsAgent = new URLSearchParams(location.search).get("agent");
  const shareWillCreate = wantsAgent === "planner";
  try {
    const ok = await loadList();
    skel.remove();
    if (!ok || getSessions().length === 0) {
      if (!shareWillCreate) createSession();
    } else {
      for (const s of getSessions()) {
        openChatTab(s.id, s.name, s.agent);
      }
      restoreTabState();
      if (getActiveTabId() === "" && getSessions().length > 0) {
        activateTab(getSessions()[0]!.id);
      }
    }
  } catch {
    skel.remove();
    if (!shareWillCreate) createSession();
  }
  suppressPush(false);

  applyShareTarget();
  applyInitialRoute();
}

function onLoginSuccess(): void {
  hideLoginModal();
  dismissLoadingScreen();
  void apiGet<WhoamiResponse>("/api/whoami").then((d) => {
    if (d?.email !== undefined) setUserEmail(d.email);
  });
  // Fetch the pre-conversation catalog so the picker is populated
  // before the first chat's session/new arrives. Session-sourced
  // updates overwrite this the moment a bridge spawns.
  void fetchModelsFromREST();
  if (getSessions().length === 0) createSession();
}

async function fetchModelsFromREST(): Promise<void> {
  // Pre-conversation catalog: `kiro-cli chat --list-models --format
  // json`, surfaced via /api/models. Populates the picker before any
  // chat session has spawned so users never see an empty list on
  // first load. Once a session/new response lands, the per-session
  // path below overwrites with the authoritative catalog for that
  // chat.
  const d = await apiGet<{ models: ModelInfo[] }>("/api/models");
  if (d === null || d.models === undefined || d.models.length === 0) return;
  populatePickerModels(d.models, "");
}

function fetchModelsFromSession(): void {
  // Live per-chat catalog: kiro-cli's session/new response carries
  // modes.availableModels which the bridge applies onto api.Chat.
  // Whenever that list changes on the active session we push the
  // authoritative list into the picker, overwriting whatever the
  // REST fetch seeded at startup.
  const active = getActive();
  if (active === undefined || active.available_models.length === 0) return;
  const mapped: ModelInfo[] = active.available_models.map((m) => ({
    model_id: m.id,
    model_name: m.name,
    ...(m.description === "" ? {} : { description: m.description }),
    rate_multiplier: m.rate_multiplier ?? 1,
  }));
  populatePickerModels(mapped, active.model);
  if (active.usage.context_size === 0 && active.model !== "") {
    active.usage.context_size = contextSizeFor(active.model);
  }
  refreshContextUI(active);
}

/** Merge a model list into the picker cache + context-size table.
 *  `activeModel` is used by refreshPickerIfVisible to move the active
 *  highlight; pass "" when no session is active yet. */
function populatePickerModels(models: ModelInfo[], activeModel: string): void {
  for (const m of models) {
    if (m.description !== undefined && MODEL_CONTEXT_SIZES[m.model_id] === undefined) {
      const size = parseContextSize(m.description);
      if (size !== undefined) MODEL_CONTEXT_SIZES[m.model_id] = size;
    }
  }
  setPickerModels(models);
  refreshPickerIfVisible(activeModel === "" ? undefined : activeModel);
}

// ============================================================
// Input handling
// ============================================================

function setupInput(): void {
  initPromptInput((text: string) => {
    if (getActiveId() === "") createSession(text);
    else sendPrompt(text);
  }, () => {
    // Cancel the active chat's in-flight turn. No-op if nothing running.
    if (getActiveId() === "") return;
    if (!isThinking(getActiveId())) return;
    void cancelTurn.dispatch(getActiveId());
  });

  const doCreate = guardAction(() => {
    createSession();
    $.sidebar.classList.remove("open");
  });
  $.newChatBtn.addEventListener("click", doCreate);
  $.newPlanBtn.addEventListener("click", guardAction(() => {
    createPlannerSession();
    $.sidebar.classList.remove("open");
  }));
  $.menuToggle.addEventListener("click", () => $.sidebar.classList.toggle("open"));
  $.sidebarClose.addEventListener("click", () => $.sidebar.classList.remove("open"));

  // The model switcher owns its button click, popover, queue, and
  // outside-click dismissal. See model-switcher.ts.
  initModelSwitcher();
  initFollowAlong();
  initAutoApprove();
  initSupervisedPill();

  // Expandable pills: context and status dot.
  const ctxExpand = $.contextIndicator.querySelector(".pill-expand-content") as HTMLElement | null;
  if (ctxExpand !== null) makeExpandable($.contextIndicator, ctxExpand);
  const statusExpand = $.statusDot.querySelector(".pill-expand-content") as HTMLElement | null;
  if (statusExpand !== null) makeExpandable($.statusDot, statusExpand);
}

// ============================================================
// URL routing
// ============================================================

function applyRoute(route: Route): void {
  switch (route.kind) {
    case "chat":
      if (route.id !== "" && get(route.id) !== undefined) {
        switchSession(route.id);
      } else if (getActiveId() !== "") {
        replaceRoute({ kind: "chat", id: getActiveId() });
      }
      break;
    case "settings":
      forceSettingsTab(route.tab);
      openTab({
        id: "__settings__", name: "Settings", kind: "settings",
        view: TAB_VIEWS.settings,
        route: { kind: "settings", tab: route.tab },
        onShow: loadSettingsForTab(route.tab),
      });
      break;
    case "git":
      openTab({
        id: "__git__", name: "Source Control", kind: "git",
        view: TAB_VIEWS.git, route: { kind: "git" }, onShow: loadGitRepos,
      });
      break;
    case "files":
      restoreFileBrowser(route.path);
      openTab({
        id: "__files__", name: "Files", kind: "files",
        view: TAB_VIEWS.files, route: { kind: "files", path: route.path }, onShow: loadFileBrowser,
      });
      break;
    case "file":     openFile(route.path, route.line); break;
    case "history":
      void import("./history.js").then(({ showHistoryView }) => showHistoryView()).catch(() => {});
      break;
    case "follow":
      void import("./follow.js").then(({ showFollowView }) => showFollowView()).catch(() => {});
      break;
  }
}

// loadSettingsForTab returns the on-open callback for the given settings
// tab. Keeps applyRoute readable while preserving the lazy-load behaviour
// each tab currently has (tools list, kiro config list, etc. only fetch
// when their tab actually opens).
function loadSettingsForTab(tab: SettingsTab): () => void {
  switch (tab) {
    case "tools":        return loadToolsList;
    case "instructions": return loadKiroConfig;
    case "general":      return noop;
    case "permissions":  return noop;
    case "git":          return noop; // settings.ts onTabChange handles the fetch
  }
}

function applyInitialRoute(): void {
  const route = parseRoute(location.pathname);
  if (route.kind === "chat" && route.id === "" && getActiveId() !== "") {
    replaceRoute({ kind: "chat", id: getActiveId() });
  } else if (route.kind !== "chat" || route.id !== "") {
    applyRoute(route);
  }
}

onPopState((route: Route) => applyRoute(route));

document.addEventListener("DOMContentLoaded", init);
