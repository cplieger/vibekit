// ---------------------------------------------------------------------------
// App: orchestrator. Wires modules, registers SSE handlers, and routes.
//
// WIRING, and no longer a job of its own. Two jobs left this file: the boot
// sequence is `boot.ts`, and the whoami read plus its three-state verdict is
// `identity.ts`. What stays is construction, injection, `applyRoute` — the
// switch over every route kind, which reaches most of the app's surfaces and so
// belongs where the surfaces are constructed — and the pre-session catalog
// fetch, which stays because `picker.ts` takes its Retry as an injected thunk
// and `model-catalog.ts` takes its reader and sinks as parameters, so the one
// place that can see the endpoint, the phase sink and the picker is here.
//
// Server is the source of truth. Sending a prompt posts a command; the
// server broadcasts SSE events that drive all rendering. No optimistic
// local mutations.
// ---------------------------------------------------------------------------

import type { ServerEvent } from "./types.js";
import { getActiveId, get, getSessions, isThinking } from "./store.js";
import { admitLocation, settleDeepLinkedChat } from "./deep-link.js";
import { effect } from "@cplieger/reactive";
import { dispatch, onBus, onSSE, BUS_TAB_CHANGED, BUS_TRANSPORT_GAP } from "./bus.js";
import { findGlyph } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { $, byId } from "./dom.js";
import { guardDuplicateActivation, initSidebarSwipe } from "./platform.js";
import { initPointerTier } from "./pointer-tier.js";
import { initPointerModeToggle, revealPointerModeToggle } from "./pointer-mode.js";
import { initPageTitleFit } from "./page-title.js";
import { initRolePicker } from "./role-picker.js";
import * as transport from "./transport.js";
import { initUI, renderIdentity } from "./settings.js";
import { initPostAuth, onTransportStatus, startBoot } from "./boot.js";
import { snapshotDeclaration } from "./snapshot-declaration.js";
import { resolveIdentity } from "./identity.js";
import { fetchCatalog } from "./session-catalog.js";
import {
  setOnEmpty,
  openTab,
  setSettingsTab,
  setGitTab,
  setDocsTab,
  activeChatRef,
} from "./tabs.js";
import { markBootDone } from "./view-swap.js";
import { ingestTabsChanged, listTabs } from "./tabs-sync.js";
import { replaceRoute, onPopState } from "./router.js";
import type { Route, RouteOrigin } from "./router.js";
import { initModelPicker } from "./picker.js";
import { refreshRuntimeLine } from "./status.js";
import { initShellPanel } from "./shell.js";
import { hideLoginModal, initLoginModal } from "./modals.js";
import { initEditor } from "./editor-core.js";
import { openFile, activateFile, closeEditorFile } from "./editor-openers.js";
import { registerTabOpeners } from "./tab-materialize.js";
import { showRun } from "./run-view.js";
import { showSubagent } from "./subagent-view.js";
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
import { isRetentionEnabled, onRetentionChange } from "./retention.js";
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
import { makeExpandable } from "./pill-expand.js";
import { loadAccountUsage } from "./account-usage.js";
import { initPromptInput, sendComposer } from "./prompt-input.js";
import { initComposerState } from "./composer-state.js";
import { initPendingSteers } from "./pending-steers.js";
import { initRunBar } from "./run-bar.js";
import { initChatOptions } from "./chat-options.js";
import { mountDecisionDock } from "./decision-dock.js";
import { registerAllSSEDecoders } from "./wire/registry.gen.js";

import "./handlers/chat.js";
import "./handlers/messages.js";
import "./handlers/turn.js";
import "./handlers/system.js";
import "./handlers/open-external-url.js";
import "./handlers/safety.js";
import "./handlers/run.js";
import { installRunDotSubscriber } from "./run-dots.js";
import { installSubagentDotSubscriber } from "./subagent-dots.js";
import { installChatRunDotSubscriber } from "./chat-run-dots.js";
import "./handlers/steer.js";
import { initPushMessages } from "./handlers/push-message.js";
import { initLaunchQueue } from "./share-target.js";
import { cancelTurn } from "./actions/chat.js";
import { copyClipboard } from "./actions/messages.js";
import { setCopyCallback } from "./code-blocks.js";
import { subscribeToActions } from "./actions/index.js";
import { initActions } from "./actions/boot.js";
// Init

function init(): void {
  // FIRST, before anything measures or renders: `data-pointer` on <html> decides
  // every control height, hit target and icon size, so a consumer that reads a box
  // before it is set reads the wrong tier. The tier is decided HERE and nothing
  // after this line moves it — see pointer-tier.ts for the three rungs. The reveal
  // is registered with it rather than after it, so a coarse pointer arriving during
  // boot cannot be the one event nobody was listening for; the button is authored
  // HTML, so revealing it needs no wiring of its own.
  initPointerTier({ onCoarseSeen: revealPointerModeToggle });
  initPointerModeToggle();

  // AFTER the tier, because the fit it measures is against the bar's action
  // buttons and the tier is what sizes them (44px coarse, 36px fine). Reading the
  // room before `data-pointer` is set measures the wrong row.
  initPageTitleFit();

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
        showRun(workflowID);
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

  // The chat whose transcript is on screen, injected rather than imported: the
  // transport holds no store state and importing store.ts from it risks a cycle. It
  // is what the connect replay reads to decide which busy chats need their in-flight
  // transcript, and the server cannot derive it — the active chat is per-DEVICE.
  //
  // The resolver is a leaf of its own (`snapshot-declaration.ts`), because the answer
  // has four states and one of them cannot be reached from here: this runs before
  // `startBoot`, so the active chat is still "" and only the URL names the chat the
  // reader is on.
  transport.setSnapshotChatProvider(snapshotDeclaration);

  transport.init((evt: ServerEvent) => {
    dispatch(evt);
  }, onTransportStatus);

  installStoreSubscribers();

  // After installStoreSubscribers: both write tab state, and this one paints a run tab
  // the boot restore may not have opened yet — an unopened tab has no spec to park a
  // dot state on, so the effect's own sweep picks it up once it exists.
  installRunDotSubscriber();

  // The same dot for a SUBAGENT's row. Here for the reason above: an effect running at
  // import would paint against a strip that has not been restored yet.
  installSubagentDotSubscriber();

  // The WORKFLOW mark on a CHAT's row — the second mark in its leading cluster, for
  // a run that chat launched and that outlives the turn which started it. Here for
  // the same reason: the tab restore runs inside startBoot() at the end of init(), so
  // every one of these three first passes sees an empty projection and repaints on the
  // bump that restore produces.
  installChatRunDotSubscriber();

  // The out-of-page attention surfaces, folded from the chat tabs' dots. Before any tab
  // is opened, because it captures the served <title> as its base.
  initAttention();

  // There is no per-session model feed. It watched the active chat's
  // `available_models` and re-populated the picker from it, but that list was the
  // WORKSPACE catalog copied onto every chat — 29 identical copies, 5.5% of a
  // 1.25 MiB response — so the signature it deduped on could only change when
  // the workspace's own catalog did. /api/config-template is that one feed, and
  // the server prefers a live session's report over the session-less template,
  // so nothing authoritative is lost.

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
  // A relaunch FOCUSES this window rather than navigating it (manifest
  // launch_handler), so a shortcut's or a share's URL arrives in the launch queue
  // and nowhere else. Registered before the boot, because the queue delivers what
  // it buffered as soon as a consumer exists.
  initLaunchQueue();

  void startBoot({ applyRoute });
}

function onLoginSuccess(): void {
  hideLoginModal();
  // The post-auth fan-out the signed-out boot held back: governance, the version
  // pair, the git badge and the workspace catalog. Guarded, so a login after an
  // `unavailable` boot that already ran it is a no-op.
  initPostAuth();
  void resolveIdentity().then((v) => {
    // Only the signed_in arm may write the row. The other two must not blank a
    // value the login that just succeeded put on screen: a page that signs in and
    // then meets a whoami timeout would otherwise clear the sidebar's email and
    // read as a sign-out one frame after signing in.
    if (v.state === "signed_in") {
      renderIdentity(v);
    }
  });
  // RESETS a live boot loop rather than being refused by it: a login is exactly the new
  // information that may have fixed the read.
  void fetchCatalog({ reset: true });
  if (getSessions().length === 0) {
    // DETACHED: nothing below reads it, and `markBootDone()` must not wait on a round
    // trip — it only flips the flag that lets view swaps animate.
    void createSession();
  }
  // The unauthenticated boot path returns before applyInitialRoute(), so flip the boot
  // flag here too.
  markBootDone();
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
  initModelPicker(pickModel, () => fetchCatalog());
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

/** Apply a route. RESOLVES when the view it names is open, which is what lets the
 *  router hold its claim on the location for the whole application: the `run` and
 *  `subagent` arms reach their opener through a dynamic `import()`, and while that
 *  import is in flight the active row is still whatever the boot restored.
 *
 *  The arms whose opener is `openTab` deliberately do NOT return its chain: that is a
 *  server mutation bounded only by the API timeout, and awaiting it would hold
 *  `markBootDone` and the identity region for that long on every deep-linked boot.
 *  They keep a narrower version of the same exposure — see the report. */
function applyRoute(route: Route, origin: RouteOrigin = "deeplink"): Promise<void> {
  // Asked FIRST, because every branch below is an opener and from a Route alone they
  // cannot be told apart. `deep-link.ts` owns what a location is allowed to mean.
  if (admitLocation(route, origin) === "canonicalized") {
    return Promise.resolve();
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
      // RETURNED rather than voided: the router's claim on this location stands until it
      // resolves, so no unrelated projection emit can write the URL while the chunk loads.
      return import("./run-view.js")
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
    case "subagent":
      // A delegate's page has nothing to fetch — its blocks are already in the chat store,
      // or they are not resident and the page says so — so this is just the tab.
      return import("./subagent-view.js")
        .then(({ openSubagentView }) => {
          openSubagentView(route.chat, route.id);
        })
        .catch(() => {
          /* noop */
        });
  }
  return Promise.resolve();
}

onPopState((route: Route) => {
  void applyRoute(route, "history");
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
