// ---------------------------------------------------------------------------
// App: orchestrator. Wires modules, owns the singleton SessionStore,
// registers SSE handlers, and handles auth + initial route.
//
// Server is the source of truth. Sending a prompt posts a command; the
// server broadcasts SSE events that drive all rendering. No optimistic
// local mutations.
// ---------------------------------------------------------------------------

import type {
  ServerEvent,
  ModelInfo,
  SessionEffortLevel,
  SessionMode,
  SessionModel,
} from "./types.js";
import { setCatalogModes } from "./roles.js";
import {
  MODEL_CONTEXT_SIZES,
  parseContextSize,
  contextSizeFor,
  activeSession,
  getActiveId,
  getActive,
  get,
  getSessions,
  isThinking,
} from "./store.js";
import { loadList } from "./store-load.js";
import { computed, effect } from "@cplieger/reactive";
import { dispatch, onBus, BUS_TAB_CHANGED } from "./bus.js";
import { findGlyph } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { $, byId } from "./dom.js";
import { guardAction, initSidebarSwipe } from "./platform.js";
import { initRolePicker } from "./role-picker.js";
import * as transport from "./transport.js";
import { loadSettings, restoreAll, initUI, initPostAuthUI, setUserEmail } from "./settings.js";
import { apiGet, apiGetTyped } from "./api-client.js";
import { decodeWhoamiResponse } from "./wire/decoders.gen.js";
import {
  setOnEmpty,
  restoreTabState,
  getSavedTabState,
  restorableSingletonIDs,
  getActiveTabId,
  getActiveTabRoute,
  activateTab,
  openTab,
  markBootDone,
  TAB_VIEWS,
} from "./tabs.js";
import { parseRoute, replaceRoute, onPopState, suppressPush } from "./router.js";
import { chatSkeleton } from "./skeleton.js";
import type { Route } from "./router.js";
import { refreshPickerIfVisible, setPickerModels, initModelPicker } from "./picker.js";
import { setStatus, refreshRuntimeLine } from "./status.js";
import { initShellPanel } from "./shell.js";
import { showLoginModal, hideLoginModal, initLoginModal } from "./modals.js";
import { initEditor } from "./editor-core.js";
import { openFile } from "./editor-openers.js";
import { openAtLine } from "./navigate.js";
import { initAttachmentPillCallbacks } from "./attachment-pill.js";
import { initFileBrowser, loadFileBrowser, restoreFileBrowser } from "./files.js";
import { initFilePicker } from "./files-picker.js";
import { initChatAttach } from "./files-drop.js";
import { initTaskListPill } from "./task-list.js";
import { initAwaySummary } from "./away-summary.js";
import { initAttention } from "./attention.js";
import { initTerminalStream } from "./terminal-stream.js";
import { initTooltips } from "./tooltip.js";
import { isRetentionEnabled, onRetentionChange, refreshRetention } from "./retention.js";
import { initKeyboardShortcuts } from "./keys.js";
import { openShortcutsSheet } from "./shortcuts.js";
import {
  handleFindKey,
  toggleFindForActiveTab,
  findAffordanceForActiveTab,
} from "./find-dispatch.js";
import { forceSettingsTab, loadSettingsTabData } from "./settings-tabs.js";
import { flushURLHighlight } from "./settings-highlight.js";
import { forceGitTab } from "./git-tabs.js";
import { loadGitRepos } from "./git.js";
import { restoreLastModel } from "./session-context.js";
import {
  openChatTab,
  createSession,
  switchSession,
  sendPrompt,
  installStoreSubscribers,
} from "./chat.js";
import { initModelSwitcher, applyLocalModel, setCatalogEfforts } from "./model-switcher.js";
import { makeExpandable } from "./pill-expand.js";
import { loadAccountUsage } from "./account-usage.js";
import { initGovernance } from "./governance.js";
import { initPromptInput, sendComposer } from "./prompt-input.js";
import { initComposerState } from "./composer-state.js";
import { initPendingSteers } from "./pending-steers.js";
import { initChatOptions } from "./chat-options.js";
import { mountDecisionDock } from "./decision-dock.js";
import { initRuntimeHealth } from "./runtime-health.js";
// commands-menu stripped — slash commands replaced by dedicated UI buttons
import { refreshContextUI } from "./context-ui.js";
import { registerAllSSEDecoders } from "./wire/registry.gen.js";
import { applyShareTarget } from "./share-target.js";

import "./handlers/chat.js";
import "./handlers/messages.js";
import "./handlers/turn.js";
import "./handlers/system.js";
import "./handlers/open-external-url.js";
import "./handlers/safety.js";
import "./handlers/run.js";
import "./handlers/steer.js";
import { initPushMessages } from "./handlers/push-message.js";
import { cancelTurn } from "./actions/chat.js";
import { copyClipboard } from "./actions/messages.js";
import { setCopyCallback } from "./code-blocks.js";
import { subscribeToActions } from "./actions/index.js";
import { initActions } from "./actions/boot.js";
import { error as toastError } from "./toast.js";
// Register the conflict SSE handler at startup so badges land
// without the user having to first open the chat that triggered
// them. The module is small; the side-effect import is worth the
// immediacy.

function dismissLoadingScreen(): void {
  document.getElementById("app-loading")?.remove();
  $.appRoot.classList.remove("app-hidden");
}

// ============================================================
// Init
// ============================================================

function init(): void {
  initActions();

  setOnEmpty(() => {
    createSession();
  });

  // Register SSE payload decoders before opening the transport.
  // Decoders run in transport.ts before each event reaches dispatch();
  // an event whose payload fails validation is dropped (with a
  // structured console.error) instead of feeding handlers a partial
  // shape. The decoder set is generated from Go structs by
  // cmd/wire-codegen — see wire/registry.gen.ts.
  registerAllSSEDecoders();

  transport.init(
    (evt: ServerEvent) => {
      dispatch(evt);
    },
    (status) => {
      setStatus(status);
      if (status === "connected") {
        void loadList();
      }
    },
  );

  installStoreSubscribers();

  // The out-of-page attention surfaces: the tab-title count, the installed app's
  // icon badge and the tab icon, all folded from the chat tabs' dots. Wired here,
  // before any tab is opened, because it captures the served <title> as its base
  // and subscribes to the tab store's dot and set signals.
  initAttention();

  // Refresh picker whenever the active session's available_models
  // shifts. Models come both from a pre-conversation REST fetch at
  // startup (kiro-cli chat --list-models) and per-session from the
  // ACP bridge's session/new response; this listener is the live
  // update path — session-sourced lists are authoritative and
  // overwrite whatever the REST path seeded.
  const modelSig = computed(() => {
    void activeSession.value;
    const active = getActive();
    if (active === undefined) {
      return "";
    }
    return active.id + ":" + active.available_models.map((m) => m.id).join(",");
  });
  effect(() => {
    // The computed dedups by value (Object.is) and is glitch-free, so the
    // effect re-runs only when the active session's id or available_models
    // actually change — each distinct catalog triggers exactly one fetch.
    // An empty signature means no active session (the computed's only "" path).
    if (modelSig.value === "") {
      return;
    }
    fetchModelsFromSession();
  });

  setupInput();
  initUI();
  initShellPanel();
  setCopyCallback((text) => void copyClipboard.dispatch(text, { silent: true }));
  initEditor();
  initFileBrowser();
  initFilePicker();
  initChatAttach();
  // One opener for BOTH pill homes — the composer's staged row and a sent turn's
  // header. Injected here because attachment-pill.ts is a leaf and one of its
  // consumers is a pure `fundamentals/` view; routed through navigate.ts because
  // a clicked path is that module's subject.
  initAttachmentPillCallbacks({ open: openAtLine });
  initTaskListPill();
  // The search button routes through the same dispatcher as Ctrl-F, so the two
  // cannot mean different things. It used to call find-in-chat directly, which
  // made it a dead control on /files and /file/{path}: the chat view is hidden
  // there, so the guard returned and the click did nothing at all.
  $.findBtn.addEventListener("click", () => {
    toggleFindForActiveTab();
  });
  // …and it collapses where it has no destination: `/settings`, a run view, the
  // git view's Sources tab, and an editor tab over a diff, an image or rendered
  // markdown. `is-collapsed` rather than the `.hidden` utility, because that one
  // is `display: none` and cannot animate out — 12-chat.css fades the button and
  // takes its width and its gap with it, so the floating toolbar shrinks.
  //
  // It also paints WHICH of the two things this page has: a magnifier where the
  // box reaches past what is on screen, a funnel where it only narrows rows
  // already loaded. Same glyph producer as the box it opens (`findGlyph`), so the
  // button cannot promise a search and open a filter.
  //
  // Called from inside an `effect` so the signals the answer reads — the editor's
  // mode, the git sub-tab — re-run it themselves; the bus subscription covers the
  // tab switch, which is not a signal.
  const syncFindAffordance = (): void => {
    const { available, kind } = findAffordanceForActiveTab();
    $.findBtn.classList.toggle("is-collapsed", !available);
    if (!available) {
      // Nothing to repaint: the control is on its way out, and swapping its glyph
      // mid-fade would be a second thing moving.
      return;
    }
    const verb = kind === "search" ? "Search" : "Filter";
    $.findBtn.replaceChildren(iconEl(findGlyph(kind, 18)));
    $.findBtn.setAttribute("aria-label", verb);
    $.findBtn.setAttribute("data-tooltip", `${verb} (Ctrl+F)`);
  };
  effect(() => {
    syncFindAffordance();
  });
  onBus(BUS_TAB_CHANGED, syncFindAffordance);
  $.docsBtn.addEventListener("click", () => {
    void import("./docs.js")
      .then(({ showDocsView }) => {
        showDocsView();
      })
      .catch(() => {
        /* noop */
      });
  });
  $.historyBtn.addEventListener("click", () => {
    void import("./history.js")
      .then(({ showHistoryView }) => {
        showHistoryView();
      })
      .catch(() => {
        /* noop */
      });
  });
  // Sync history button visibility with retention setting. Retention = 0 is
  // "no retention" (ephemeral chats, nothing survives a close) → hide History;
  // anything else keeps closed chats → show it.
  const syncHistoryBtn = (): void => {
    $.historyBtn.classList.toggle("hidden", !isRetentionEnabled());
  };
  onRetentionChange(syncHistoryBtn);
  syncHistoryBtn();
  initAwaySummary();
  initTerminalStream();
  initTooltips();
  // initGovernance() moved to initPostAuth(): its /api/governance snapshot
  // (and settings.ts's version/git-badge fetches) shouldn't fire on the
  // login screen (B2).
  initLoginModal(onLoginSuccess);
  initSidebarSwipe($.chatArea, $.sidebar);
  initKeyboardShortcuts({
    newChat: () => {
      createSession();
      $.sidebar.classList.remove("open");
    },
    toggleShell: () => {
      $.shellBtn.click();
    },
    toggleFiles: () => {
      $.filesBtn.click();
    },
    toggleGit: () => {
      $.gitBtn.click();
    },
    toggleSettings: () => {
      $.settingsBtn.click();
    },
    sendMessage: () => {
      sendComposer();
    },
    showShortcuts: openShortcutsSheet,
  });

  // Find (Ctrl-F / Cmd-F), scoped by the ACTIVE TAB (find-dispatch.ts owns the
  // routing). Capture phase so the browser's native find can be pre-empted
  // before it opens; ONE listener, because a second capture-phase keydown on the
  // same chord would be a third meaning nobody can predict.
  document.addEventListener("keydown", handleFindKey, true);
  document.addEventListener("keydown", focusComposerOnTyping);

  // Action-framework global: live-log every action error to the
  // browser console so failures are visible in DevTools regardless of
  // toast policy (suppressed-toast actions still get logged).
  // Inlined from a former actions/console-log.ts module — single boot
  // wiring, not worth a separate module.
  subscribeToActions((inst) => {
    if (inst.status !== "error" || inst.error === undefined) {
      return;
    }
    const meta: string[] = [];
    if (inst.completedAt !== undefined) {
      meta.push(`${String(inst.completedAt - inst.startedAt)}ms`);
    }
    if (inst.attempts !== undefined && inst.attempts > 1) {
      meta.push(`${String(inst.attempts)} attempts`);
    }
    if (inst.error.status !== undefined) {
      meta.push(`HTTP ${String(inst.error.status)}`);
    }
    if (inst.error.code !== undefined) {
      meta.push(inst.error.code);
    }
    console.error(
      `[action] ${inst.name} failed (${meta.join(", ")}): ${inst.error.message}`,
      inst.error,
    );
  });

  // Register the service worker unconditionally at boot (independent of the
  // push opt-in): an active SW with a fetch handler is a PWA install-criteria
  // requirement, so browsers only offer "Install app" once one controls the
  // page. register() is idempotent — the push-enable flow reuses it.
  if ("serviceWorker" in navigator) {
    void navigator.serviceWorker.register("/sw.js").catch((err: unknown) => {
      console.warn("sw: registration failed", err);
    });
  }
  // The other half of the push channel: the worker posts here to route a
  // notification click and to toast a push that arrived while this page was
  // focused (where it shows no OS notification at all).
  initPushMessages();

  void checkAuthAndStart();
}

async function checkAuthAndStart(): Promise<void> {
  const settings = await loadSettings();
  restoreLastModel(settings.last_model);

  suppressPush(true);
  try {
    restoreAll(settings);
  } catch {
    /* best-effort */
  }
  suppressPush(false);

  let authenticated = false;
  const d = await apiGetTyped("/api/whoami", decodeWhoamiResponse);
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
  initPostAuth();

  // Degraded-runtime probe (kiro-cli missing → app-global banner);
  // re-checks on every transport gap so recovery self-heals.
  initRuntimeHealth();

  // Pre-conversation catalog so the picker has content before the
  // first chat's session/new lands. Fire-and-forget; session-sourced
  // updates overwrite this the moment a bridge spawns.
  void fetchModelsFromREST();
  // Read retention setting so tab-close knows whether to keep or delete, and so
  // restoreSingletonTabs can tell whether History is reachable at all. Kept
  // concurrent with loadList below and awaited at the restore instead of here:
  // serialising it would add a round trip to every boot, while not awaiting it at
  // all leaves the restore reading the default (enabled) whenever /api/settings
  // is the slower of the two.
  const retentionReady = refreshRetention();

  const skel = chatSkeleton();
  $.messages.appendChild(skel);

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
      if (!ok) {
        // Surface the boot failure BEFORE falling back to the empty state,
        // so the fresh "New conversation" reads as a fallback rather than
        // silently impersonating the user's (unreachable) chats (B2).
        toastError("Couldn't load your chats.", {
          label: "Reload",
          onClick: () => {
            location.reload();
          },
        });
      }
      if (!shareWillCreate) {
        createSession();
      }
    } else {
      // Open every chat tab WITHOUT activating (B8): activation runs
      // activateChatView (messages fetch + conflicts prefetch) per chat,
      // so activating all N at boot cost 2N requests for chats the user
      // isn't looking at. Exactly one tab is activated below.
      for (const s of getSessions()) {
        openChatTab(s.id, s.name, { activate: false });
      }
      await retentionReady;
      restoreSingletonTabs();
      restoreTabState();
      if (getActiveTabId() === "" && getSessions().length > 0) {
        activateTab(getSessions()[0]!.id); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
    }
  } catch {
    skel.remove();
    toastError("Couldn't load your chats.", {
      label: "Reload",
      onClick: () => {
        location.reload();
      },
    });
    if (!shareWillCreate) {
      createSession();
    }
  }
  suppressPush(false);

  applyShareTarget();
  applyInitialRoute();
  // Boot restores are done — view swaps animate from here on (B3).
  markBootDone();
}

// One-time post-auth initialization: fetches gated behind a successful
// whoami so the login screen doesn't fan out API calls — the governance
// snapshot, /api/version, and the git-badge poll (/api/git/status-all +
// /api/forges every 15s) all used to fire before auth resolved (B2).
// Runs on boot when already authenticated, or after the first login.
let postAuthInitDone = false;
function initPostAuth(): void {
  if (postAuthInitDone) {
    return;
  }
  postAuthInitDone = true;
  // Governance snapshot + live-update subscription. Gates MCP availability
  // (Settings → Tools), renders the read-only Organization-policy
  // disclosure (Settings → General), and gates the code-reference chip.
  initGovernance();
  // Version info (Settings → About) + git panel wiring incl. badge poll.
  initPostAuthUI();
}

/** Reopen the singleton tabs (Settings / Source Control / Kiro docs / History /
 *  Files) that were open last session, without activating them, so
 *  restoreTabState() can restore their saved order and active state (B7). Chat
 *  tabs are reopened by the boot loop and editor tabs by restoreAll();
 *  singletons were previously never reopened, so `hasTab(saved.active_view)` was
 *  always false for them and their position in the saved order was silently
 *  dropped.
 *
 *  Each `onShow` must be a plain LOADER, never the module's toggle-style opener:
 *  a toggle fired from the onShow of an already-open, already-active tab closes
 *  the tab it was meant to fill. The docs and History cases reach theirs through
 *  a lazy import, because those two modules are lazy everywhere else and a static
 *  import here would pull them into the main bundle. */
function restoreSingletonTabs(): void {
  // History is gated because its own entry point is: the toolbar button hides
  // when retention is off (nothing is kept to list), so restoring the tab
  // reopened a page the user could neither reach nor get back to.
  const ids = restorableSingletonIDs(getSavedTabState().tab_order, {
    __history__: isRetentionEnabled(),
  });
  for (const id of ids) {
    switch (id) {
      case "__settings__":
        openTab(
          {
            id,
            name: "Settings",
            kind: "settings",
            view: TAB_VIEWS.settings,
            route: { kind: "settings", tab: "general" },
            onShow: () => {
              loadSettingsTabData("general");
            },
          },
          { activate: false },
        );
        break;
      case "__git__":
        openTab(
          {
            id,
            name: "Source Control",
            kind: "git",
            view: TAB_VIEWS.git,
            route: { kind: "git", tab: "changes" },
            onShow: loadGitRepos,
          },
          { activate: false },
        );
        break;
      case "__docs__":
        openTab(
          {
            id,
            name: "Kiro docs",
            kind: "docs",
            view: TAB_VIEWS.docs,
            route: { kind: "docs", tab: "steering" },
            onShow: () => {
              void import("./docs.js")
                .then(({ forceDocsTab, loadDocs }) => {
                  forceDocsTab("steering");
                  loadDocs();
                })
                .catch(() => {
                  /* noop */
                });
            },
          },
          { activate: false },
        );
        break;
      case "__history__":
        openTab(
          {
            id,
            name: "History",
            kind: "history",
            view: TAB_VIEWS.history,
            route: { kind: "history" },
            onShow: () => {
              void import("./history.js")
                .then(({ loadHistoryView }) => {
                  loadHistoryView();
                })
                .catch(() => {
                  /* noop */
                });
            },
            // Unlike the docs case, this one needs a close hook: the page holds a
            // dispatch, an AbortController and a debounce timer, and the toggle
            // path tears them down through its own onClose.
            onClose: () => {
              void import("./history.js")
                .then(({ teardownHistoryView }) => {
                  teardownHistoryView();
                })
                .catch(() => {
                  /* noop */
                });
            },
          },
          { activate: false },
        );
        break;
      case "__files__":
        // restoreAll() already restored the browser path from ui-state.
        openTab(
          {
            id,
            name: "Files",
            kind: "files",
            view: TAB_VIEWS.files,
            route: { kind: "files", path: "." },
            onShow: loadFileBrowser,
          },
          { activate: false },
        );
        break;
      default:
        break;
    }
  }
}

function onLoginSuccess(): void {
  hideLoginModal();
  dismissLoadingScreen();
  initPostAuth();
  void apiGetTyped("/api/whoami", decodeWhoamiResponse).then((d) => {
    if (d?.email !== undefined) {
      setUserEmail(d.email);
    }
  });
  // Fetch the pre-conversation catalog so the picker is populated
  // before the first chat's session/new arrives. Session-sourced
  // updates overwrite this the moment a bridge spawns.
  void fetchModelsFromREST();
  if (getSessions().length === 0) {
    createSession();
  }
  // The unauthenticated boot path returns before applyInitialRoute(), so
  // flip the boot flag here too — view swaps animate from first login on.
  markBootDone();
}

/** One catalog entry, mapped from the wire `SessionModel` to the picker's
 *  `ModelInfo`. Shared by both feeds (the pre-session REST template and the
 *  per-session bridge catalog) because a field carried by one and dropped by the
 *  other is invisible until a control silently loses its input: that is how the
 *  model's default effort tier went missing here while the server sent it.
 *  Fields are spread conditionally rather than assigned undefined — the client
 *  compiles under exactOptionalPropertyTypes. */
function toModelInfo(m: SessionModel): ModelInfo {
  return {
    model_id: m.id,
    model_name: m.name,
    ...(m.description === undefined || m.description === "" ? {} : { description: m.description }),
    rate_multiplier: m.rate_multiplier ?? 1,
    ...(m.has_effort === undefined ? {} : { has_effort: m.has_effort }),
    ...(m.default_effort_level === undefined || m.default_effort_level === ""
      ? {}
      : { default_effort_level: m.default_effort_level }),
  };
}

async function fetchModelsFromREST(): Promise<void> {
  // Pre-conversation catalog: kiro-cli 2.14's session-less
  // _kiro/config/template, surfaced via /api/config-template on the
  // utility bridge. One fetch seeds BOTH pickers before any chat
  // session has spawned: the model list (so users never see an empty
  // picker on first load) and the role picker's mode base (bundled
  // modes + the user's global ~/.kiro/agents — richer than the static
  // BUILTIN_MODES fallback; workspace agents still merge in from
  // /api/workspace/kiro-config inside the role picker). Once a
  // session/new response lands, the per-session path below overwrites
  // with the authoritative catalog for that chat.
  const d = await apiGet<{
    modes: SessionMode[];
    models: SessionModel[];
    default_model?: string;
    effort_levels?: SessionEffortLevel[];
    effort_active?: string;
  }>("/api/config-template");
  if (d === null) {
    return;
  }
  // Pre-session effort vocabulary: a chat with no bridge has no session catalog,
  // so without this the effort control has neither its tier list nor the level
  // the next session would run at.
  setCatalogEfforts(d.effort_levels ?? [], d.effort_active ?? "");
  if (d.modes.length > 0) {
    setCatalogModes(d.modes);
  }
  if (d.models.length > 0) {
    populatePickerModels(d.models.map(toModelInfo), "");
  }
}

function fetchModelsFromSession(): void {
  // Live per-chat catalog: kiro-cli's session/new response carries
  // modes.availableModels which the bridge applies onto vibekit.Chat.
  // Whenever that list changes on the active session we push the
  // authoritative list into the picker, overwriting whatever the
  // REST fetch seeded at startup.
  const active = getActive();
  if (active === undefined || active.available_models.length === 0) {
    return;
  }
  const mapped: ModelInfo[] = active.available_models.map(toModelInfo);
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
      if (size !== undefined) {
        MODEL_CONTEXT_SIZES[m.model_id] = size;
      }
    }
  }
  setPickerModels(models);
  refreshPickerIfVisible(activeModel === "" ? undefined : activeModel);
}

// ============================================================
// Input handling
// ============================================================

function setupInput(): void {
  // The composer is two peers meeting on one element, wired here rather than
  // from each other: prompt-input owns its BEHAVIOUR (send, history, the IME
  // guard) and composer-state owns its per-chat STATE (the draft and the staged
  // attachments). composer-state cannot be wired from prompt-input — send-state
  // imports prompt-input to push the button state and transport imports
  // send-state, so reaching the draft action from there would close an import
  // cycle. Nothing owns the box's SIZE: it tracks its own content.
  initComposerState();
  initPromptInput(
    (text: string) => {
      if (getActiveId() === "") {
        createSession(text);
      } else {
        sendPrompt(text);
      }
    },
    () => {
      // Cancel the active chat's in-flight turn. No-op if nothing running.
      if (getActiveId() === "") {
        return;
      }
      if (!isThinking(getActiveId())) {
        return;
      }
      void cancelTurn.dispatch(getActiveId());
    },
  );

  const doCreate = guardAction(() => {
    createSession();
    $.sidebar.classList.remove("open");
  });
  $.newChatBtn.addEventListener("click", doCreate);
  $.menuToggle.addEventListener("click", () => $.sidebar.classList.toggle("open"));
  $.sidebarClose.addEventListener("click", () => {
    $.sidebar.classList.remove("open");
  });

  // The model switcher owns its button click, popover, queue, and
  // outside-click dismissal. See model-switcher.ts.
  initModelSwitcher();
  // The empty-chat model picker. Its visibility is derived from store state;
  // only the selection callback is injected, because it lives in
  // model-switcher.ts, which imports picker.ts.
  initModelPicker(applyLocalModel);
  // The role picker owns the prompt-bar role pill (expand, list, selection).
  initRolePicker();
  // Queued-prompt chips (pending sends buffered while a turn is in flight).
  initPendingSteers();
  initChatOptions();
  // The interaction dock: permission asks, elicitation forms and agent
  // questions. Hosted by the chat's bottom bar; it takes its host as an
  // argument so a future run tab's bottom bar can host one too.
  mountDecisionDock($.decisionDock);

  // Expandable pills: context and status dot. Each card is its trigger's
  // SIBLING (see 15-input.css .pill-slot), so it is looked up by id rather
  // than queried inside the button.
  makeExpandable($.contextIndicator, byId("context-card"));
  // Fetch account/subscription usage lazily when the popup opens (it changes
  // slowly and may be rate-limited); loadAccountUsage throttles. The
  // agent-runtime line re-probes /api/health on the same trigger.
  makeExpandable($.statusDot, $.statusCard, {
    onExpand: () => {
      loadAccountUsage();
      refreshRuntimeLine();
    },
  });
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
        id: "__settings__",
        name: "Settings",
        kind: "settings",
        view: TAB_VIEWS.settings,
        route: { kind: "settings", tab: route.tab },
        // First-activation panel data (tools list, instructions lists,
        // native policy) — idempotent, shared with the pill-click path
        // in settings-tabs.ts (B9).
        onShow: () => {
          loadSettingsTabData(route.tab);
          // A `?highlight=` on this URL fires after the panel's loader, so the
          // control it names exists by the time we look for it. One-shot, so a
          // later popstate back here does not re-flash it.
          flushURLHighlight();
        },
      });
      break;
    case "git":
      forceGitTab(route.tab);
      openTab({
        id: "__git__",
        name: "Source Control",
        kind: "git",
        view: TAB_VIEWS.git,
        route: { kind: "git", tab: route.tab },
        onShow: loadGitRepos,
      });
      break;
    case "files":
      restoreFileBrowser(route.path);
      openTab({
        id: "__files__",
        name: "Files",
        kind: "files",
        view: TAB_VIEWS.files,
        route: { kind: "files", path: route.path },
        onShow: loadFileBrowser,
      });
      break;
    case "file":
      openFile(route.path, route.line);
      break;
    case "docs":
      void import("./docs.js")
        .then(({ showDocsView }) => {
          showDocsView(route.tab);
        })
        .catch(() => {
          /* noop */
        });
      break;
    case "history":
      void import("./history.js")
        .then(({ showHistoryView }) => {
          showHistoryView();
        })
        .catch(() => {
          /* noop */
        });
      break;
    case "run":
      void import("./run-view.js")
        .then(({ openRunView }) => {
          // Deep link: the run's name is not in the URL, so the tab is titled
          // by id until the fetch supplies the real name.
          openRunView(route.id, route.id);
        })
        .catch(() => {
          /* noop */
        });
      break;
  }
}

// (Per-settings-tab data loading moved to the lazy loader map in
// settings-tabs.ts — registered by settings.ts initUI, fired once per tab
// on first activation. The retired "git" settings tab was removed with it;
// /settings/git now canonicalizes to General in parseSettingsTab.)

function applyInitialRoute(): void {
  const route = parseRoute(location.pathname);
  if (route.kind !== "chat" || route.id !== "") {
    applyRoute(route);
    return;
  }
  // Default "/" route. Canonicalize the URL to what's actually visible:
  //   - active chat WITH server-persisted messages → /chat/{id};
  //   - active chat with zero messages → stay on "/" (B4): the id is a
  //     client-side ghost the server doesn't know about — reloading
  //     /chat/{ghost} can't resolve it and would mint a fresh ghost id on
  //     every load. handlers/chat.ts flips the URL to the real id once the
  //     server echoes chat_created for it.
  //   - restored non-chat tab (Settings, git, …) → its route, so the
  //     restored view and the URL agree (their boot-time pushRoute was
  //     suppressed).
  const active = getActive();
  if (getActiveId() !== "" && active !== undefined && active.message_count > 0) {
    replaceRoute({ kind: "chat", id: getActiveId() });
    return;
  }
  const tabRoute = getActiveTabRoute();
  if (tabRoute !== null && tabRoute.kind !== "chat") {
    replaceRoute(tabRoute);
  }
}

onPopState((route: Route) => {
  applyRoute(route);
});

/** Redirect a bare keystroke to the composer, so a fresh chat can be typed into
 *  without clicking the box first.
 *
 *  Opening a new chat left focus on `<body>`, so the first characters a user
 *  typed went nowhere — the classic "I typed my prompt and it vanished". This is
 *  the message-app convention (Slack, Discord, iMessage all do it).
 *
 *  Deliberately narrow. It only fires for a plain printable character with no
 *  modifier, and it bails whenever focus already sits somewhere that wants keys:
 *  any input/textarea/select/contenteditable, the terminal surface, or an open
 *  dialog. Modifier chords are untouched, so Ctrl+F, Cmd+K and every browser
 *  shortcut still work — including the transcript search whose own handler runs
 *  on the capture phase ahead of this one. */
function focusComposerOnTyping(e: KeyboardEvent): void {
  if (e.ctrlKey || e.metaKey || e.altKey || e.isComposing) {
    return;
  }
  // A single printable character. `key.length === 1` excludes Enter, Escape,
  // Tab, the arrows and the F-keys without enumerating them.
  if (e.key.length !== 1) {
    return;
  }
  const active = document.activeElement;
  if (active instanceof HTMLElement) {
    if (
      active instanceof HTMLInputElement ||
      active instanceof HTMLTextAreaElement ||
      active instanceof HTMLSelectElement ||
      active.isContentEditable ||
      active.closest("#shell-panel, dialog[open], .wt-root") !== null
    ) {
      return;
    }
  }
  // Only when a transcript is actually on screen; typing on Settings or the file
  // browser must not yank focus into a composer the user cannot see.
  const chatView = document.getElementById("chat-view");
  if (chatView === null || chatView.classList.contains("hidden")) {
    return;
  }
  const input = $.promptInput;
  if (input.disabled) {
    return;
  }
  // Focus and let the SAME keystroke land in the box: preventing the default and
  // appending by hand would drop dead keys and IME composition.
  input.focus();
}

document.addEventListener("DOMContentLoaded", init);
