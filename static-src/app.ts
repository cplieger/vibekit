// ---------------------------------------------------------------------------
// App: orchestrator. Wires modules, owns the singleton SessionStore, registers
// SSE handlers, and routes.
//
// WIRING, and no longer a job of its own. Three jobs left this file: the boot
// sequence is `boot.ts`, the workspace mode/model/effort catalog is
// `session-catalog.ts`, and the whoami read plus its three-state verdict is
// `identity.ts`. What stays is construction, injection and `applyRoute` — the
// switch over every route kind, which reaches most of the app's surfaces and so
// belongs where the surfaces are constructed.
//
// Server is the source of truth. Sending a prompt posts a command; the
// server broadcasts SSE events that drive all rendering. No optimistic
// local mutations.
// ---------------------------------------------------------------------------

import type { ServerEvent } from "./types.js";
import { getActiveId, get, getSessions, isThinking } from "./store.js";
import { effect } from "@cplieger/reactive";
import { dispatch, onBus, onSSE, BUS_TAB_CHANGED, BUS_TRANSPORT_GAP } from "./bus.js";
import { findGlyph } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { $, byId } from "./dom.js";
import { guardDuplicateActivation, initSidebarSwipe } from "./platform.js";
import { initRolePicker } from "./role-picker.js";
import * as transport from "./transport.js";
import { initUI, setUserEmail } from "./settings.js";
import { dismissLoadingScreen, initPostAuth, onTransportStatus, startBoot } from "./boot.js";
import { emailToAdopt, resolveIdentity } from "./identity.js";
import { fetchCatalog } from "./session-catalog.js";
import {
  setOnEmpty,
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
import { replaceRoute, onPopState } from "./router.js";
import type { Route } from "./router.js";
import { initModelPicker } from "./picker.js";
import { refreshRuntimeLine } from "./status.js";
import { initShellPanel } from "./shell.js";
import { initChatToolbarMetrics } from "./chat-toolbar-metrics.js";
import { hideLoginModal, initLoginModal } from "./modals.js";
import { initEditor } from "./editor-core.js";
import { openFile, activateFile, closeEditorFile } from "./editor-openers.js";
import { registerTabOpeners } from "./tab-materialize.js";
import { showRun } from "./run-view.js";
import { showSubagent } from "./subagent-view.js";
import { runChatID } from "./run-store.js";
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
import { initChatOptions } from "./chat-options.js";
import { mountDecisionDock } from "./decision-dock.js";
// commands-menu stripped — slash commands replaced by dedicated UI buttons
import { registerAllSSEDecoders } from "./wire/registry.gen.js";

import "./handlers/chat.js";
import "./handlers/messages.js";
import "./handlers/turn.js";
import "./handlers/system.js";
import "./handlers/open-external-url.js";
import "./handlers/safety.js";
import "./handlers/run.js";
import { installRunDotSubscriber } from "./run-dots.js";
import "./handlers/steer.js";
import { initPushMessages } from "./handlers/push-message.js";
import { cancelTurn } from "./actions/chat.js";
import { copyClipboard } from "./actions/messages.js";
import { setCopyCallback } from "./code-blocks.js";
import { subscribeToActions } from "./actions/index.js";
import { initActions } from "./actions/boot.js";
// Register the conflict SSE handler at startup so badges land
// without the user having to first open the chat that triggered
// them. The module is small; the side-effect import is worth the
// immediacy.

// ============================================================
// Init
// ============================================================

function init(): void {
  initActions();

  // The tab factory's injected half. `materializeTab` turns a server-owned
  // TabSubject into the local spec a row needs, and the three behaviours below
  // live in modules that will themselves call it — so they are registered here
  // rather than imported there, which is what keeps the factory out of a cycle.
  // The five singleton kinds need nothing: the factory reaches their loaders
  // through a lazy import.
  registerTabOpeners({
    chat: { show: activateChatView, close: closeChatTab, dot: chatTabDot },
    editor: { show: activateFile, close: closeEditorFile },
    run: {
      // `parentless` is the run's own fact, not the tab strip's, so it comes from the
      // run store's record of which chat launched this run — the same fallback
      // openRunView already uses to find a parent. A subject cannot answer it: see
      // tab-materialize.ts's header.
      //
      // No `cancel` half any more, and none is reachable: a run tab is a VIEW, so its
      // × stops nothing. Cancelling is the control row's verb.
      show: (workflowID) => {
        showRun(workflowID, runChatID(workflowID) === "");
      },
    },
    // No close half for the same reason: a subagent page is a projection of blocks the
    // chat store owns, so it starts nothing and can stop nothing.
    subagent: { show: showSubagent },
  });

  setOnEmpty(() => {
    // DETACHED deliberately: this is the empty-strip respawn, a notification slot
    // that must not mutate the store it was called from, and nothing here reads
    // the new chat's id — the create seeds its own row and opens its own tab.
    void createSession();
  });

  // The tab projection's SYNC half, fed here rather than binding itself, so its
  // three version rules can be exercised against a Set with no transport. Two
  // inputs and that is all of them:
  //
  //   - every `tabs_changed` frame, queued and applied in ARRIVAL order. The
  //     handler must not fan out: the version rules are only well-defined against
  //     a sequential applier, so `ingestTabsChanged` takes the frame and returns.
  //   - a transport GAP, which means frames were dropped, so the delta stream
  //     cannot be trusted and the answer is the whole set. This is beside the
  //     store reconcile in handlers/system.ts rather than inside it, because the
  //     tab set is its own collection with its own recovery.
  //
  // The 409 on a refused reorder re-lists through the same call, from inside
  // tabs.ts. Nothing else may read the collection.
  onSSE("tabs_changed", (_chatID, p) => {
    ingestTabsChanged(p);
  });
  onBus(BUS_TRANSPORT_GAP, () => {
    void listTabs();
  });

  // Register SSE payload decoders before opening the transport.
  // Decoders run in transport.ts before each event reaches dispatch();
  // an event whose payload fails validation is dropped (with a
  // structured console.error) instead of feeding handlers a partial
  // shape. The decoder set is generated from Go structs by
  // cmd/wire-codegen — see wire/registry.gen.ts.
  registerAllSSEDecoders();

  transport.init((evt: ServerEvent) => {
    dispatch(evt);
  }, onTransportStatus);

  installStoreSubscribers();

  // The activity dot for parentless workflow runs. After installStoreSubscribers
  // for the same reason it is after it in the file: both write tab state, and this
  // one paints a run tab that the boot restore may not have opened yet — an
  // unopened tab has no spec to park a dot state on, so the effect's own sweep is
  // what picks it up once it exists.
  installRunDotSubscriber();

  // The out-of-page attention surfaces: the tab-title count, the installed app's
  // icon badge and the tab icon, all folded from the chat tabs' dots. Wired here,
  // before any tab is opened, because it captures the served <title> as its base
  // and subscribes to the tab store's dot and set signals.
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
  // Before anything can raise a banner: `.banner-stack` stops at the toolbar's
  // left edge, and only JS can measure that edge.
  initChatToolbarMetrics();
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
      // DETACHED: the keyboard shortcut is fire-and-forget, and closing the
      // sidebar is independent of whether the chat lands. Awaiting would delay the
      // sidebar close behind a round trip for no gain.
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

  void startBoot({ applyRoute });
}

function onLoginSuccess(): void {
  hideLoginModal();
  dismissLoadingScreen();
  initPostAuth();
  void resolveIdentity().then((v) => {
    // `null` means "leave the row alone": only the signed_in arm carries an
    // email, and the other two must not blank a row the login that just
    // succeeded filled in.
    const email = emailToAdopt(v);
    if (email !== null) {
      setUserEmail(email);
    }
  });
  // Fetch the workspace catalog so the pickers are populated before the first
  // chat's session/new arrives.
  void fetchCatalog();
  if (getSessions().length === 0) {
    // DETACHED: this is the post-login starter chat, and nothing below reads it.
    // `markBootDone()` must not wait on a round trip — it only flips the flag that
    // lets view swaps animate, and delaying it would make the first post-login
    // paint depend on the create's latency.
    void createSession();
  }
  // The unauthenticated boot path returns before applyInitialRoute(), so
  // flip the boot flag here too — view swaps animate from first login on.
  markBootDone();
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
      // Create-vs-send keys off the PROJECTION's active subject, never the
      // chat store's pointer: with closes optimistic, the store retains a
      // closed chat's row until the machine confirms, and the empty-state
      // surface (strip empty, close pending) must create rather than send into
      // the chat being closed. The two agree throughout the window — the close
      // gesture moves the store pointer too — so this is the belt on the truth
      // the reader can see.
      if (activeChatRef() === "") {
        // DETACHED, and the prompt rides INSIDE the create rather than after it:
        // `createSession(text)` sends once the chat exists, so nothing here needs
        // the id. Awaiting would only delay this callback's return.
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
    // DETACHED: the New chat button's only follow-up is closing the sidebar, which
    // does not depend on the chat. The guard absorbs only a duplicated pointer
    // dispatch of one press; the create's own retry idempotency (its op id) is what
    // covers a deliberate repeat.
    void createSession();
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
  // model-switcher.ts, which imports picker.ts. pickModel, not applyLocalModel:
  // a hero-picker pick must PERSIST like a pill pick, or the next header echo
  // clobbers it back (user report, 2026-08-31).
  initModelPicker(pickModel);
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
      void refreshRuntimeLine();
    },
  });
}

// ============================================================
// URL routing
// ============================================================

/** Where a route came from, because the two answer the "this names nothing that
 *  is open" case differently.
 *
 *  A `deeplink` — a cold load, a bookmark, a link someone shared — MAY open what
 *  it names: that is what a `/run/{id}` or `/file/{path}` URL is for. A `history`
 *  entry may only ACTIVATE something already open, because it names a location
 *  this browser was at rather than one that still exists. Closing a tab leaves its
 *  URL an entry or more back (every activation pushes, and a close pushes the
 *  neighbour's on top), and applying such an entry as a deep link re-opened the
 *  tab: `file`, `run` and all five singletons opened unconditionally, so a reader
 *  pressing back watched a tab they had closed reappear, and the server-owned
 *  collection then broadcast it to every other device. */
type RouteOrigin = "deeplink" | "history";

function applyRoute(route: Route, origin: RouteOrigin = "deeplink"): void {
  // The back/forward guard. Ask the projection FIRST, because every branch below
  // is an opener: each one either activates an existing tab or creates one, and
  // from a Route alone they cannot be told apart.
  //
  // The redirect target is the ACTIVE TAB's route, so the URL ends up naming what
  // is on screen — which is what the reader is looking at, since popstate changes
  // no app state by itself. `replaceRoute` rather than `history.go(-1)`: skipping
  // the entry would walk backwards through however many dead ones sit behind it
  // and can leave the app entirely, while a replace consumes exactly the one
  // location that no longer resolves. Same canonicalization applyInitialRoute
  // does for "/".
  if (origin === "history" && tabIdForRoute(route) === "") {
    replaceRoute(getActiveTabRoute() ?? { kind: "chat", id: "" });
    return;
  }
  switch (route.kind) {
    case "chat":
      if (route.id !== "" && get(route.id) !== undefined) {
        switchSession(route.id);
      } else if (getActiveId() !== "") {
        replaceRoute({ kind: "chat", id: getActiveId() });
      }
      break;
    // The FIVE singleton routes each open their tab and then CORRECT its
    // sub-tab, and the correction is not a convenience: a singleton's `ref` is
    // empty, so a subject cannot carry a sub-tab and the factory has to build the
    // canonical one. `setSettingsTab` / `setGitTab` / `setDocsTab` are that channel
    // and they stay synchronous, because the panel swap is local state the router
    // owns.
    //
    // Every one of them goes through `openTab`, and NONE through the matching
    // `toggle*View` helper, for the reason tab-materialize.ts states about the
    // factory: a toggle CLOSES the tab when it is already active, so a router that
    // toggled would DESTROY the tab the URL names. Measured on /docs and /history,
    // which did reach the toggles: with the docs tab restored and active, a cold
    // load of /docs/hooks closed it and landed on the active chat, alternating
    // pass/fail run to run as the arrangement flipped — and a back press onto an
    // open /docs entry closed it the same way. `openTab` is idempotent by subject,
    // so it activates an open tab and opens a closed one, which is what a route
    // means.
    //
    // None of them passes an onShow any more. The factory reaches each page's own
    // loader through a lazy import, so /settings reached from a deep link, from
    // the gear and from another device's open all load the same way — a
    // divergence that was real: three of the Settings doors passed no loader at
    // all.
    case "settings":
      forceSettingsTab(route.tab);
      void openTab({ kind: "settings" }).then(() => {
        setSettingsTab(route.tab);
        // A `?highlight=` on this URL fires after the panel's loader, so the
        // control it names exists by the time we look for it. One-shot, so a
        // later popstate back here does not re-flash it.
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
          // Deep link: the run's name is not in the URL, so the tab is titled
          // by id until the fetch supplies the real name. It still nests under the
          // launching chat when this client knows which one it was — openRunView
          // consults the run store for that, so a shared link lands beside the
          // conversation rather than at the end of the strip.
          openRunView(route.id, route.id);
        })
        .catch(() => {
          /* noop */
        });
      break;
    case "subagent":
      // A delegate's page has nothing to fetch — its blocks are already in the
      // chat store, or they are not resident and the page says so — so this is
      // just the tab. `openSubagentView` is idempotent by subject: it activates an
      // open tab and opens a closed one, which is what a route means.
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

// (Per-settings-tab data loading moved to the lazy loader map in
// settings-tabs.ts — registered by settings.ts initUI, fired once per tab
// on first activation. The retired "git" settings tab was removed with it;
// /settings/git now canonicalizes to General in parseSettingsTab.)

onPopState((route: Route) => {
  applyRoute(route, "history");
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
