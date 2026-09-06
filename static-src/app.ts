// App: the composition root. Wires modules, registers SSE handlers, and handles
// auth plus the initial route.

import type { ServerEvent, ModelInfo, SessionModel } from "./types.js";
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
  registerEvictionExemption,
  startEvictionSweep,
} from "./store.js";
import { loadList } from "./store-load.js";
import { settleDeepLinkedChat } from "./deep-link.js";
import { computed, effect, touch } from "@cplieger/reactive";
import { dispatch, onBus, onSSE, BUS_TAB_CHANGED, BUS_TRANSPORT_GAP } from "./bus.js";
import { findGlyph } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { $, byId } from "./dom.js";
import { guardDuplicateActivation, initSidebarSwipe } from "./platform.js";
import { initPointerTier } from "./pointer-tier.js";
import { initRolePicker } from "./role-picker.js";
import * as transport from "./transport.js";
import {
  adoptThemeFromSettings,
  loadSettings,
  restoreAll,
  initUI,
  initPostAuthUI,
  setUserEmail,
} from "./settings.js";
import { apiGetTyped } from "./api-client.js";
import { decodeConfigTemplateResponse, decodeWhoamiResponse } from "./wire/decoders.gen.js";
import type { ConfigTemplateResponse } from "./wire/types.gen.js";
import {
  setOnEmpty,
  activateRestoredTab,
  getActiveTabRoute,
  openTab,
  setSettingsTab,
  setGitTab,
  setDocsTab,
  tabIdForRoute,
  activeChatRef,
} from "./tabs.js";
import { markBootDone } from "./view-swap.js";
import { ingestTabsChanged, listTabs } from "./tabs-sync.js";
import { parseRoute, replaceRoute, onPopState, suppressPush } from "./router.js";
import type { Route } from "./router.js";
import {
  refreshPickerIfVisible,
  setPickerModels,
  initModelPicker,
  setCatalogPhase,
} from "./picker.js";
import { refreshCatalog, CATALOG_REQUEST_TIMEOUT_MS } from "./model-catalog.js";
import { setStatus, refreshRuntimeLine, initStatusVersions } from "./status.js";
import { initShellPanel } from "./shell.js";
import { showLoginModal, hideLoginModal, initLoginModal } from "./modals.js";
import { initEditor } from "./editor-core.js";
import { openFile, activateFile, closeEditorFile } from "./editor-openers.js";
import { registerTabOpeners } from "./tab-materialize.js";
import { runTabProjectsChat, showRun } from "./run-view.js";
import { showSubagent, subagentTabProjectsChat } from "./subagent-view.js";
import { hasExecutingRunForChat, rebuildLiveRuns, runChatID } from "./run-store.js";
import { openAtLine } from "./navigate.js";
import { initAttachmentPillCallbacks } from "./attachment-pill.js";
import { initFileBrowser, restoreFileBrowser } from "./files.js";
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
import { forceSettingsTab } from "./settings-tabs.js";
import { flushURLHighlight } from "./settings-highlight.js";
import { forceGitTab } from "./git-tabs.js";
import { restoreLastModel, restoreLastEffort } from "./session-context.js";
import {
  createSession,
  switchSession,
  sendPrompt,
  installStoreSubscribers,
  activateChatView,
  closeChatTab,
  chatTabDot,
} from "./chat.js";
import { initModelSwitcher, pickModel } from "./model-switcher.js";
import { setCatalogEfforts } from "./effort.js";
import { makeExpandable } from "./pill-expand.js";
import { loadAccountUsage } from "./account-usage.js";
import { initGovernance } from "./governance.js";
import { initPromptInput, sendComposer } from "./prompt-input.js";
import { initComposerState } from "./composer-state.js";
import { initPendingSteers } from "./pending-steers.js";
import { initRunBar } from "./run-bar.js";
import { initChatOptions } from "./chat-options.js";
import { mountDecisionDock } from "./decision-dock.js";
import { initRuntimeHealth } from "./runtime-health.js";
import { loadVersions } from "./versions.js";
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
import { installRunDotSubscriber } from "./run-dots.js";
import { installSubagentDotSubscriber } from "./subagent-dots.js";
import "./handlers/steer.js";
import { initPushMessages } from "./handlers/push-message.js";
import { cancelTurn } from "./actions/chat.js";
import { copyClipboard } from "./actions/messages.js";
import { setCopyCallback } from "./code-blocks.js";
import { subscribeToActions } from "./actions/index.js";
import { initActions } from "./actions/boot.js";
import { error as toastError } from "./toast.js";

function dismissLoadingScreen(): void {
  document.getElementById("app-loading")?.remove();
  $.appRoot.classList.remove("app-hidden");
}

// Init

function init(): void {
  // FIRST, before anything measures or renders: `data-pointer` on <html> decides
  // every control height, hit target and icon size, so a consumer that reads a box
  // before it is set reads the wrong tier.
  initPointerTier();

  initActions();

  // The tab factory's injected half: these three behaviours live in modules that
  // themselves call `materializeTab`, so registering here is what keeps the factory
  // out of a cycle. The five singleton kinds reach their loaders lazily.
  registerTabOpeners({
    chat: { show: activateChatView, close: closeChatTab, dot: chatTabDot },
    editor: { show: activateFile, close: closeEditorFile },
    run: {
      // `parentless` is the run's own fact, not the tab strip's, so it comes from the
      // run store's record of which chat launched this run. No `cancel` half: a run
      // tab is a VIEW, so its × stops nothing.
      show: (workflowID) => {
        showRun(workflowID, runChatID(workflowID) === "");
      },
    },
    // No close half for the same reason: a subagent page is a projection of blocks the
    // chat store owns, so it starts nothing and can stop nothing.
    subagent: { show: showSubagent },
  });

  setOnEmpty(() => {
    // DETACHED deliberately: this is a notification slot that must not mutate the
    // store it was called from, and nothing here reads the new chat's id.
    void createSession();
  });

  // The tab projection's SYNC half, fed here rather than binding itself, so its three
  // version rules can be exercised against a Set with no transport. Two inputs: every
  // `tabs_changed` frame, applied in ARRIVAL order (the handler must not fan out — the
  // version rules are only well-defined against a sequential applier), and a transport
  // GAP, where the delta stream cannot be trusted and the answer is the whole set.
  onSSE("tabs_changed", (_chatID, p) => {
    ingestTabsChanged(p);
  });
  onBus(BUS_TRANSPORT_GAP, () => {
    void listTabs();
  });

  // Before the transport opens: decoders run in transport.ts ahead of dispatch(), and an
  // event whose payload fails validation is dropped rather than handed on partial. The
  // set is generated from Go structs by cmd/wire-codegen.
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

  // After installStoreSubscribers: both write tab state, and this one paints a run tab
  // the boot restore may not have opened yet — an unopened tab has no spec to park a
  // dot state on, so the effect's own sweep picks it up once it exists.
  installRunDotSubscriber();

  // The same dot for a SUBAGENT's row. Here for the reason above: an effect running at
  // import would paint against a strip that has not been restored yet.
  installSubagentDotSubscriber();

  // The out-of-page attention surfaces, folded from the chat tabs' dots. Before any tab
  // is opened, because it captures the served <title> as its base.
  initAttention();

  // Models arrive both from a pre-conversation REST fetch at startup and per-session
  // from the bridge's session/new response; this listener is the live update path, and
  // session-sourced lists overwrite whatever the REST path seeded.
  const modelSig = computed(() => {
    touch(activeSession);
    const active = getActive();
    if (active === undefined) {
      return "";
    }
    return active.id + ":" + active.available_models.map((m) => m.id).join(",");
  });
  effect(() => {
    // The computed dedups by value and is glitch-free, so each distinct catalog triggers
    // exactly one fetch. An empty signature means no active session.
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
  // One opener for BOTH pill homes. Injected because attachment-pill.ts is a leaf and
  // one of its consumers is a pure `fundamentals/` view.
  initAttachmentPillCallbacks({ open: openAtLine });
  initTaskListPill();
  // Through the same dispatcher as Ctrl-F, so the two cannot mean different things. A
  // direct find-in-chat call made this a dead control on /files and /file/{path}.
  $.findBtn.addEventListener("click", () => {
    toggleFindForActiveTab();
  });
  // …and it collapses where it has no destination. `is-collapsed` rather than `.hidden`,
  // which is `display: none` and cannot animate out. It also paints WHICH of the two
  // things this page has — a magnifier where the box reaches past what is on screen, a
  // funnel where it only narrows loaded rows — from the same glyph producer as the box it
  // opens, so the button cannot promise a search and open a filter.
  //
  // Called from inside an `effect` so the signals the answer reads re-run it themselves;
  // the bus subscription covers the tab switch, which is not a signal.
  const syncFindAffordance = (): void => {
    const { available, kind } = findAffordanceForActiveTab();
    $.findBtn.classList.toggle("is-collapsed", !available);
    if (!available) {
      // Nothing to repaint: the control is on its way out, and swapping its glyph
      // mid-fade would be a second thing moving.
      return;
    }
    const verb = kind === "search" ? "Search" : "Filter";
    $.findBtn.replaceChildren(iconEl(findGlyph(kind)));
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
  // Retention = 0 is "no retention" (ephemeral chats, nothing survives a close) → hide
  // History; anything else keeps closed chats → show it.
  const syncHistoryBtn = (): void => {
    $.historyBtn.classList.toggle("hidden", !isRetentionEnabled());
  };
  onRetentionChange(syncHistoryBtn);
  syncHistoryBtn();
  initAwaySummary();
  initTerminalStream();
  initTooltips();
  initLoginModal(onLoginSuccess);
  initSidebarSwipe($.chatArea, $.sidebar);
  initKeyboardShortcuts({
    newChat: () => {
      // DETACHED: closing the sidebar is independent of whether the chat lands, and
      // awaiting would delay it behind a round trip for no gain.
      void createSession();
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

  // Find (Ctrl-F / Cmd-F), scoped by the ACTIVE TAB. Capture phase so the browser's
  // native find is pre-empted before it opens; ONE listener, because a second
  // capture-phase keydown on the same chord is a third meaning nobody can predict.
  document.addEventListener("keydown", handleFindKey, true);
  document.addEventListener("keydown", focusComposerOnTyping);

  // Live-log every action error to the console regardless of toast policy, so a
  // suppressed-toast action is still visible in DevTools.
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

  // Unconditional, independent of the push opt-in: an active SW with a fetch handler is
  // a PWA install-criteria requirement, so browsers only offer "Install app" once one
  // controls the page. register() is idempotent.
  if ("serviceWorker" in navigator) {
    void navigator.serviceWorker.register("/sw.js").catch((err: unknown) => {
      console.warn("sw: registration failed", err);
    });
  }
  // The other half of the push channel: the worker posts here to route a notification
  // click and to toast a push that arrived while this page was focused.
  initPushMessages();

  void checkAuthAndStart();
}

async function checkAuthAndStart(): Promise<void> {
  // Null means the settings fetch FAILED, which is not "the settings are the defaults".
  // Nothing is restored on that path — the theme keeps the pre-paint cache, the model and
  // effort seeds stay unset — and boot continues so a reload can recover.
  const settings = await loadSettings();
  if (settings !== null) {
    restoreLastModel(settings.last_model);
    restoreLastEffort(settings.last_effort, settings.last_effort_model);
    // The toggle was constructed during initUI against the pre-paint cache, so this is
    // where the server's choice replaces that hint, and where the cache is carried across
    // once if the server has none.
    adoptThemeFromSettings(settings);

    suppressPush(true);
    try {
      restoreAll(settings);
    } catch {
      /* best-effort */
    }
    suppressPush(false);
  }

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
    // Nothing will hydrate the store behind a login modal, so release the held frames
    // rather than leaving the stream stalled until the watchdog fires.
    transport.markHydrated();
    showLoginModal();
    return;
  }

  initPostAuth();

  // Degraded-runtime probe (kiro-cli missing → app-global banner); re-checks on every
  // transport gap so recovery self-heals.
  initRuntimeHealth();

  // Fire-and-forget: one read per page load, and the lines repaint through a signal when
  // it lands, so nothing waits on the `--version` subprocess the server spawns.
  initStatusVersions();
  void loadVersions();

  // Pre-conversation catalog so the picker has content before the first chat's
  // session/new lands. Session-sourced updates overwrite it the moment a bridge spawns.
  void fetchModelsFromREST();
  // Kept concurrent with the two boot reads below and awaited before the tab list is
  // adopted: serialising it would add a round trip to every boot, while not awaiting it
  // leaves a close reading the default (enabled) whenever /api/settings is the slower.
  const retentionReady = refreshRetention();

  suppressPush(true);
  // If share-target intends to create a session, skip the default empty-state
  // createSession so there is no unused "New conversation" tab beside the planner.
  const wantsAgent = new URLSearchParams(location.search).get("agent");
  const shareWillCreate = wantsAgent === "planner";
  try {
    const ok = await loadList();
    // The chat store is populated (or provably unreachable), so the frames held since the
    // connection opened can be released — chief among them the one `turn_state` per busy
    // chat, which is never re-broadcast. Here rather than after the tabs open, because the
    // frames only need a chat ROW to land on.
    transport.markHydrated();
    if (!ok || getSessions().length === 0) {
      if (!ok) {
        // Before falling back to the empty state, so the fresh "New conversation" reads
        // as a fallback rather than impersonating the user's unreachable chats.
        toastError("Couldn't load your chats.", {
          label: "Reload",
          onClick: () => {
            location.reload();
          },
        });
      }
      if (!shareWillCreate) {
        // AWAITED: applyInitialRoute() below resolves the URL against the strip, and
        // detaching would let the route apply before the tab appears.
        await createSession();
      }
    }
    // THE TAB SET, read whole from the server: no per-kind reopen switch, no editor-file
    // list and no saved order to re-apply, because a tab the collection holds is open and
    // the slice position IS the order. On EVERY path, chats or no chats — a chat list and
    // a tab set are different collections.
    await retentionReady;
    if (!(await listTabs())) {
      // The strip is empty here, so an unadopted read leaves the reader with no tabs and
      // nothing saying why. Nothing retries on its own: there is no gap to detect on a
      // boot connection, and a timer would re-list against a strip already in use.
      toastError("Couldn't restore your tabs.", {
        label: "Reload",
        onClick: () => {
          location.reload();
        },
      });
    }
    activateRestoredTab();
  } catch {
    transport.markHydrated();
    toastError("Couldn't load your chats.", {
      label: "Reload",
      onClick: () => {
        location.reload();
      },
    });
    if (!shareWillCreate) {
      // AWAITED for the reason above: applyInitialRoute() reads the strip.
      await createSession();
    }
  }
  suppressPush(false);

  await applyShareTarget();
  applyInitialRoute();
  // Only now, with the restored tab's content already painted underneath: the app root is
  // visibility:hidden, which preserves layout, so activation and scroll measurement ran
  // normally behind it. The per-view skeleton still covers any message fetch that
  // outlives the splash.
  dismissLoadingScreen();
  // Boot restores are done — view swaps animate from here on.
  markBootDone();
}

// One-time post-auth initialization: the fetches gated behind a successful whoami, so the
// login screen does not fan out API calls. Runs on boot when already authenticated, or
// after the first login.
let postAuthInitDone = false;
function initPostAuth(): void {
  if (postAuthInitDone) {
    return;
  }
  postAuthInitDone = true;
  // Gates MCP availability, renders the read-only Organization-policy disclosure, and
  // gates the code-reference chip.
  initGovernance();
  // Version info (Settings → About) + git panel wiring incl. badge poll.
  initPostAuthUI();
  // Boot is one of the live-runs inventory's two rebuild triggers (the other is
  // transport:gap). The three eviction exemptions are registered here because store.ts is
  // a leaf and must not import run-store.ts or tabs.ts.
  //
  // The first is the EXECUTING predicate rather than the any-live-run one: a run parked on
  // a question writes nothing into the transcript, so exempting its chat pinned that whole
  // message window for the life of the page. The third is its narrower sibling — a run's
  // SUB-TAB projects steps out of the launching chat's window for as long as it is open,
  // including long after the executing exemption has lapsed.
  void rebuildLiveRuns();
  registerEvictionExemption(hasExecutingRunForChat);
  registerEvictionExemption(subagentTabProjectsChat);
  registerEvictionExemption(runTabProjectsChat);
  startEvictionSweep();
  // Re-read the pre-session catalog after a gap: the server may have restarted, so the
  // utility session is new and the answer can differ. HERE rather than beside the boot
  // fetch, which sits past checkAuthAndStart's unauthenticated return — a page session that
  // came in through the login modal would otherwise never re-probe.
  onBus(BUS_TRANSPORT_GAP, () => {
    void fetchModelsFromREST();
  });
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
  // RESETS a live boot loop rather than being refused by it: a login is exactly the new
  // information that may have fixed the read.
  void fetchModelsFromREST({ reset: true });
  if (getSessions().length === 0) {
    // DETACHED: nothing below reads it, and `markBootDone()` must not wait on a round
    // trip — it only flips the flag that lets view swaps animate.
    void createSession();
  }
  // The unauthenticated boot path returns before applyInitialRoute(), so flip the boot
  // flag here too.
  markBootDone();
}

/** One catalog entry, mapped from the wire `SessionModel` to the picker's `ModelInfo`.
 *  Shared by both feeds because a field carried by one and dropped by the other is
 *  invisible until a control silently loses its input. Fields are spread conditionally
 *  rather than assigned undefined — the client compiles under exactOptionalPropertyTypes. */
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

function fetchModelsFromREST(opts: { readonly reset?: boolean } = {}): Promise<void> {
  // One fetch seeds BOTH pickers before any chat session has spawned: the model list, and
  // the role picker's mode base (bundled modes plus the user's global ~/.kiro/agents,
  // richer than the static BUILTIN_MODES fallback). Once a session/new response lands, the
  // per-session path below overwrites it with that chat's authoritative catalog.

  // model-catalog.ts owns the POLICY; what stays here is the endpoint and the surfaces it
  // feeds.
  return refreshCatalog<ConfigTemplateResponse>(
    {
      // Through the GENERATED decoder: the inline `apiGet<{modes: …}>` this replaced was a
      // CLAIM rather than a check, so a server answering `{}` or `modes: null` produced a
      // TypeError inside the boot path.
      read: (signal) =>
        apiGetTyped(
          "/api/config-template",
          decodeConfigTemplateResponse,
          signal,
          CATALOG_REQUEST_TIMEOUT_MS,
        ),
      // Only a USABLE answer reaches here: an `unavailable` template emits an empty effort
      // list by construction, so a login-triggered fetch that degraded used to replace the
      // tiers a successful boot fetch had landed.
      apply: (d) => {
        // ONE rule over all three: an EMPTY list is the absence of a vocabulary rather
        // than a value, so it never replaces one an earlier answer landed. Per list because
        // each arrives empty on its own — the effort tiers ride the model, and KAS resolves
        // its model list asynchronously, so a merely COLD cache reports as `empty`.
        if (d.effort_levels.length > 0) {
          // A chat with no bridge has no session catalog, so without this the effort
          // control has neither its tier list nor the level the next session would run at.
          setCatalogEfforts(d.effort_levels, d.effort_active ?? "");
        }
        if (d.modes.length > 0) {
          setCatalogModes(d.modes);
        }
        if (d.models.length > 0) {
          populatePickerModels(d.models.map(toModelInfo), "");
        }
        // The model pill names a non-default reasoning tier, and it can only know which
        // tier is default from the catalog this fetch just landed. Nothing else repaints
        // the pill on this path.
        const active = getActive();
        if (active !== undefined) {
          refreshContextUI(active);
        }
      },
      setPhase: setCatalogPhase,
    },
    opts,
  );
}

function fetchModelsFromSession(): void {
  // Live per-chat catalog: session/new carries modes.availableModels, and whenever that
  // list changes on the active session it overwrites whatever the REST fetch seeded.
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

/** Merge a model list into the picker cache and context-size table. `activeModel` moves
 *  the active highlight; pass "" when no session is active yet. */
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

// Input handling

function setupInput(): void {
  // The composer is two peers meeting on one element, wired here rather than from each
  // other: prompt-input owns its BEHAVIOUR and composer-state its per-chat STATE.
  // composer-state cannot be wired from prompt-input — send-state imports prompt-input and
  // transport imports send-state, so reaching the draft action from there closes a cycle.
  initComposerState();
  initPromptInput(
    (text: string) => {
      // Keys off the PROJECTION's active subject, never the chat store's pointer: with
      // closes optimistic the store retains a closed chat's row until the machine confirms,
      // and the empty-state surface must create rather than send into the chat being closed.
      if (activeChatRef() === "") {
        // DETACHED, and the prompt rides INSIDE the create: `createSession(text)` sends
        // once the chat exists, so nothing here needs the id.
        void createSession(text);
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

  const doCreate = guardDuplicateActivation(() => {
    // DETACHED: the only follow-up is closing the sidebar, which does not depend on the
    // chat. The guard absorbs a duplicated pointer dispatch of one press; the create's own
    // op id covers a deliberate repeat.
    void createSession();
    $.sidebar.classList.remove("open");
  });
  $.newChatBtn.addEventListener("click", doCreate);
  $.menuToggle.addEventListener("click", () => $.sidebar.classList.toggle("open"));
  $.sidebarClose.addEventListener("click", () => {
    $.sidebar.classList.remove("open");
  });

  // The model switcher owns its button click, popover, queue and outside-click dismissal.
  initModelSwitcher();
  // The empty-chat model picker. Its visibility is derived from store state; only the
  // selection callback is injected, because it lives in model-switcher.ts, which imports
  // picker.ts. pickModel, not applyLocalModel: a hero-picker pick must PERSIST like a pill
  // pick, or the next header echo clobbers it back. The Retry's promise is RETURNED so
  // picker.ts can announce the answer it settles on.
  initModelPicker(pickModel, () => fetchModelsFromREST());
  // The role picker owns the prompt-bar role pill (expand, list, selection).
  initRolePicker();
  // Queued-prompt chips (pending sends buffered while a turn is in flight).
  initPendingSteers();
  // The composer band's live-run rows; a pure projection of the run store.
  initRunBar();
  initChatOptions();
  // The interaction dock takes its host as an argument so a future run tab's bottom bar
  // can host one too.
  mountDecisionDock($.decisionDock);

  // Each card is its trigger's SIBLING (see 15-input.css .pill-slot), so it is looked up
  // by id rather than queried inside the button.
  makeExpandable($.contextIndicator, byId("context-card"));
  // Lazily on open, because usage changes slowly and may be rate-limited;
  // loadAccountUsage throttles. The agent-runtime line re-probes /api/health on the same
  // trigger.
  makeExpandable($.statusDot, $.statusCard, {
    onExpand: () => {
      loadAccountUsage();
      void refreshRuntimeLine();
    },
  });
}

// URL routing

/** Where a route came from, because the two answer "this names nothing that is open"
 *  differently.
 *
 *  A `deeplink` MAY open what it names; a `history` entry may only ACTIVATE something
 *  already open, because it names a location this browser was at rather than one that
 *  still exists. Closing a tab leaves its URL an entry or more back, and applying such an
 *  entry as a deep link re-opened the tab — then broadcast it to every other device. */
type RouteOrigin = "deeplink" | "history";

function applyRoute(route: Route, origin: RouteOrigin = "deeplink"): void {
  // The back/forward guard. Ask the projection FIRST, because every branch below is an
  // opener and from a Route alone they cannot be told apart.
  //
  // The redirect target is the ACTIVE TAB's route, so the URL ends up naming what is on
  // screen. `replaceRoute` rather than `history.go(-1)`: skipping the entry would walk back
  // through however many dead ones sit behind it and can leave the app, while a replace
  // consumes exactly the one location that no longer resolves.
  if (origin === "history" && tabIdForRoute(route) === "") {
    replaceRoute(getActiveTabRoute() ?? { kind: "chat", id: "" });
    return;
  }
  switch (route.kind) {
    case "chat":
      if (route.id !== "" && get(route.id) !== undefined) {
        // The chat EXISTS, so `switchSession` either activates its tab or OPENS one.
        // Voided: a refusal has already raised its own notice through `openTabCommand`.
        void switchSession(route.id);
      } else if (route.id !== "") {
        // The id names NO ROW. Everything that decision needs — whether asking the server
        // can be answered at all, what its answer licenses, whether a verdict that arrived
        // a round trip late still describes the screen — lives in `deep-link.ts`. None of
        // it is routing. Voided: every outcome is returned rather than thrown, and the
        // module raises whatever notice its own evidence licenses.
        void settleDeepLinkedChat(route.id);
      } else if (getActiveId() !== "") {
        replaceRoute({ kind: "chat", id: getActiveId() });
      }
      break;
    // The FIVE singleton routes each open their tab and then CORRECT its sub-tab: a
    // singleton's `ref` is empty, so a subject cannot carry one and the factory builds the
    // canonical one. `setSettingsTab` / `setGitTab` / `setDocsTab` are that channel and stay
    // synchronous, because the panel swap is local state the router owns.
    //
    // Every one goes through `openTab` and NONE through the matching `toggle*View` helper:
    // a toggle CLOSES the tab when it is already active, so a router that toggled would
    // DESTROY the tab the URL names. `openTab` is idempotent by subject, which is what a
    // route means. None of them passes an onShow — the factory reaches each page's own
    // loader through a lazy import, so every door loads the same way.
    case "settings":
      forceSettingsTab(route.tab);
      void openTab({ kind: "settings" }).then(() => {
        setSettingsTab(route.tab);
        // A `?highlight=` fires after the panel's loader, so the control it names exists by
        // the time we look for it. One-shot, so a later popstate does not re-flash it.
        flushURLHighlight();
      });
      break;
    case "git":
      forceGitTab(route.tab);
      void openTab({ kind: "git" }).then(() => {
        setGitTab(route.tab);
      });
      break;
    case "files":
      restoreFileBrowser(route.path);
      void openTab({ kind: "files" });
      break;
    case "file":
      openFile(route.path, route.line);
      break;
    case "docs":
      void openTab({ kind: "docs" }).then(() => {
        setDocsTab(route.tab);
      });
      break;
    case "history":
      void openTab({ kind: "history" });
      break;
    case "run":
      void import("./run-view.js")
        .then(({ openRunView }) => {
          // Deep link: the run's name is not in the URL, so the tab is titled by id until
          // the fetch supplies the real name. It still nests under the launching chat when
          // this client knows which one it was.
          //
          // The fourth argument is what makes a COPIED STEP LINK land on the step: the run
          // card's row href carries the node as `#node=<path>`. `""` means "the run" and
          // lets the page auto-follow.
          openRunView(route.id, route.id, "", route.node ?? "");
        })
        .catch(() => {
          /* noop */
        });
      break;
    case "subagent":
      // A delegate's page has nothing to fetch — its blocks are already in the chat store,
      // or they are not resident and the page says so — so this is just the tab.
      void import("./subagent-view.js")
        .then(({ openSubagentView }) => {
          openSubagentView(route.chat, route.id);
        })
        .catch(() => {
          /* noop */
        });
      break;
  }
}

function applyInitialRoute(): void {
  const route = parseRoute(location.pathname);
  if (route.kind !== "chat" || route.id !== "") {
    applyRoute(route);
    return;
  }
  // Default "/" route. Canonicalize the URL to what is actually visible:
  //   - active chat → /chat/{id}, whether or not it has messages yet;
  //   - restored non-chat tab → its route, so the restored view and the URL agree (their
  //     boot-time pushRoute was suppressed).
  const active = getActive();
  if (getActiveId() !== "" && active !== undefined) {
    replaceRoute({ kind: "chat", id: getActiveId() });
    return;
  }
  const tabRoute = getActiveTabRoute();
  if (tabRoute !== null && tabRoute.kind !== "chat") {
    replaceRoute(tabRoute);
  }
}

onPopState((route: Route) => {
  applyRoute(route, "history");
});

/** Redirect a bare keystroke to the composer, so a fresh chat can be typed into without
 *  clicking the box first — the message-app convention.
 *
 *  Deliberately narrow: only a plain printable character with no modifier, and it bails
 *  whenever focus already sits somewhere that wants keys (any
 *  input/textarea/select/contenteditable, the terminal surface, an open dialog). */
function focusComposerOnTyping(e: KeyboardEvent): void {
  if (e.ctrlKey || e.metaKey || e.altKey || e.isComposing) {
    return;
  }
  // `key.length === 1` excludes Enter, Escape, Tab, the arrows and the F-keys without
  // enumerating them.
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
  // Only when a transcript is on screen; typing on Settings or the file browser must not
  // yank focus into a composer the user cannot see.
  const chatView = document.getElementById("chat-view");
  if (chatView === null || chatView.classList.contains("hidden")) {
    return;
  }
  const input = $.promptInput;
  if (input.disabled) {
    return;
  }
  // Let the SAME keystroke land in the box: preventing the default and appending by hand
  // would drop dead keys and IME composition.
  input.focus();
}

document.addEventListener("DOMContentLoaded", init);
