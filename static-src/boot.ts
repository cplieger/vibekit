// ---------------------------------------------------------------------------
// The boot: five reads in the FIRST FRAME, each answer adopted in its own region as
// it lands. Nothing waits on the identity verdict, not even an empty workspace.
//
// TWO ORDERINGS ARE REAL. The tab set follows the chat fold, because a chat tab's
// row is named from the chat store (tab-materialize.ts `chatName`); retention
// precedes it, because closing a tab has to know whether the record is kept.
//
// `applyRoute` is INJECTED: importing it would close a cycle.
// ---------------------------------------------------------------------------

import { subscribeByName } from "./actions/index.js";
import { logout } from "./actions/settings.js";
import { GLOBAL_BANNER, showBanner } from "./banner-stack.js";
import { $ } from "./dom.js";
import { bootMode, clearReloadGuard, noteBootAlive, reloadCount } from "./reload-guard.js";
import { loadList } from "./store-load.js";
import {
  getActive,
  getActiveId,
  getSessions,
  registerEvictionExemption,
  startEvictionSweep,
} from "./store.js";
import {
  clearBootSnapshot,
  paintBootSnapshot,
  readBootSnapshot,
  startBootSnapshot,
} from "./boot-snapshot.js";
import type { BootSnapshot } from "./boot-snapshot.js";
import { clearDeviceKeys } from "./ls-keys.js";
import { resetFoldState } from "./fold-state.js";
import {
  adoptThemeFromSettings,
  initPostAuthUI,
  loadSettings,
  renderIdentity,
  restoreAll,
} from "./settings.js";
import type { EffectiveSettings } from "./persist.js";
import { restoreLastEffort, restoreLastModel } from "./session-context.js";
import { resolveIdentity } from "./identity.js";
import type { IdentityVerdict } from "./identity.js";
import { fetchCatalog } from "./session-catalog.js";
import * as transport from "./transport.js";
import { showLoginModal } from "./modals.js";
import { activateRestoredTab, getActiveTabRoute, hasTab } from "./tabs.js";
import { listTabs } from "./tabs-sync.js";
import {
  claimLocation,
  navigationOrigin,
  parseRoute,
  releaseLocation,
  replaceRoute,
  suppressPush,
} from "./router.js";
import type { Route, RouteOrigin } from "./router.js";
import { createSession } from "./chat.js";
import { initGovernance } from "./governance.js";
import { initRuntimeHealth } from "./runtime-health.js";
import { initStatusVersions, setStatus } from "./status.js";
import type { ConnectionStatus } from "./types.js";
import { loadVersions } from "./versions.js";
import { refreshRetention } from "./retention.js";
import { hasExecutingRunForChat, registerRunStateDemand } from "./run-store.js";
import { chatTabFoldsRun } from "./chat-run-dots.js";
import { runTabProjectsChat } from "./run-view.js";
import { subagentTabProjectsChat } from "./subagent-view.js";
import { markBootDone } from "./view-swap.js";
import { applyShareTarget } from "./share-target.js";
import { error as toastError } from "./toast.js";

/** What the boot chain needs from the composition root. */
export interface BootDeps {
  /** Navigate to a route. Owned by `app.ts`, which is the only place that can
   *  reach every view a route can name. RESOLVES when the view is open, which is
   *  what the router's location claim is held across. */
  applyRoute: (route: Route, origin?: RouteOrigin) => Promise<void>;
}

let deps: BootDeps | null = null;

export async function startBoot(d: BootDeps): Promise<void> {
  deps = d;

  // Claim the location the DOCUMENT loaded at, before anything can push: it is the one
  // thing here that knows what the reader asked for, and it stands until the route has
  // been applied. Released in the workspace region's `finally` below.
  claimLocation(location.pathname + location.hash);

  try {
    // Past a threshold this boot withholds what it can (reload-guard.ts). Armed first:
    // the stability clear is what makes a boot that STAYS up cost the next one nothing.
    noteBootAlive();
    const reduced = bootMode() === "reduced";
    if (reduced) {
      // `autofocus` has already fired by the time a deferred module runs, so a blur is
      // the only lever left. The point is the on-screen KEYBOARD, which shortens the
      // viewport, not the zoom, which the composer's own 16px floor closes.
      $.promptInput.blur();
      announceReloadLoop();
    }

    // `identity` needs no rejection handler: every failure IS its `unavailable` arm
    // (identity.ts). Nor does `snapshotRead`, which resolves null for every failure.
    const settingsRead = loadSettings();
    const identity = resolveIdentity();
    const chatsRead = loadList();
    const retentionRead = refreshRetention();
    const snapshotRead = readBootSnapshot();

    // The workspace region owns every view the boot swaps, so it is what the animation
    // flag waits on: a slow whoami must not hold the first tab switch's transition back.
    const workspace = restoreWorkspace(chatsRead, retentionRead, snapshotRead).finally(() => {
      // The route has been applied (or the region failed), so ordinary pushes resume.
      // This covers a throw inside the region; the catch below covers one before it.
      releaseLocation();
      markBootDone();
    });
    await Promise.allSettled([
      settingsRead.then(adoptSettings),
      identity.then((v) => adoptIdentity(v, workspace)),
      workspace,
    ]);
  } catch (err) {
    // The region's `finally` above cannot run for a throw before that region exists,
    // and an unreleased claim freezes the address bar for the life of the page.
    releaseLocation();
    throw err;
  }
}

/** The settings answer: theme, the model and effort seeds, the UI-state restore.
 *
 *  Null means the fetch FAILED, which is not "the settings are the defaults", so
 *  nothing is restored and boot continues: the theme keeps the pre-paint cache and
 *  the seeds stay unset. Inventing a value here would persist it on the next
 *  write. */
function adoptSettings(settings: EffectiveSettings | null): void {
  if (settings === null) {
    return;
  }
  restoreLastModel(settings.last_model);
  restoreLastEffort(settings.last_effort, settings.last_effort_model);
  // Where the server's choice replaces the pre-paint cache, and where that cache
  // is carried across if the server has none.
  adoptThemeFromSettings(settings);

  suppressPush(true);
  try {
    restoreAll(settings);
  } catch {
    /* best-effort */
  }
  suppressPush(false);
}

/** The identity answer: one sidebar row, the post-auth fan-out, and the two things
 *  that genuinely need a verdict.
 *
 *  `signed_out` raises the login modal over an already-painted shell and holds the
 *  post-auth fetches back, so the login screen makes no API calls. `unavailable`
 *  means vibekit could not ASK, so it comes up working with a re-read offered.
 *  `workspace` is awaited because "is there anything to show" needs the chats, the
 *  tab set and any share to have landed; that region has painted by then. */
async function adoptIdentity(v: IdentityVerdict, workspace: Promise<boolean>): Promise<void> {
  renderIdentity(v);
  if (v.state === "signed_out") {
    // Nothing hydrates the store behind a login modal, so release the held frames
    // rather than stalling the stream until the watchdog fires.
    transport.markHydrated();
    // And nothing this device remembers may survive to a login screen.
    forgetDeviceState();
    showLoginModal();
    return;
  }
  if (v.state === "unavailable") {
    toastError(`Couldn't confirm who is signed in: ${v.reason}`, {
      label: "Retry",
      onClick: () => {
        void resolveIdentity().then((next) => adoptIdentity(next, workspace));
      },
    });
  }
  initPostAuth();

  // A rejected region is the same answer as `false`: the chats are not in the store.
  const chatsOK = await workspace.catch(() => false);
  if (!chatsOK) {
    // Before the fallback below, so a fresh chat does not read as the user's.
    toastError("Couldn't load your chats.", { label: "Reload", onClick: reload });
  }
  if (getSessions().length === 0) {
    // The chat STORE is the test rather than `chatsOK`: a failed fetch over a
    // painted snapshot has rows on screen already, and a share has already created
    // its chat inside the region.
    await createSession();
  }
}

/** The local snapshot, then the chats answer, then the tab set, then the route.
 *  Resolves whether the chat list was READ, which is the identity region's cue.
 *
 *  One region because these are genuinely ordered (see the header): the store
 *  names the strip's rows, and the strip is what the URL is resolved against. */
async function restoreWorkspace(
  chatsRead: Promise<boolean>,
  retentionRead: Promise<void>,
  snapshotRead: Promise<BootSnapshot | null>,
): Promise<boolean> {
  // RACED against the chat list because the paint REPLACES the chat store, so it may
  // only run BEFORE the answer that supersedes it. Store emptiness is not the test:
  // an empty list is an ANSWER, and painting over one resurrects every deleted chat.
  //
  // Only a read that SUCCEEDED is an answer: `loadList` resolves false on a network
  // failure without retrying, so it frequently settles first, and counting that as
  // an answer discards the hint on the one resume it exists for.
  const hint = await Promise.race([
    snapshotRead,
    chatsRead.then(
      (ok) => (ok ? null : snapshotRead),
      () => snapshotRead,
    ),
  ]);

  // BEST-EFFORT: the restore below is authoritative, so a throw leaves `resumed` false
  // and a half-painted resume falls through to the tab set's own activation. A REDUCED
  // boot skips the paint outright, so its first frame draws no transcript.
  let resumed = false;
  try {
    resumed = bootMode() === "full" && resumeSnapshot(hint);
  } catch {
    /* best-effort */
  }

  // A rejected read is the same answer as `false` — the chats are not in the
  // store — so the boot has one failure path rather than two.
  const chatsOK = await chatsRead.catch(() => false);
  // The rule both of these follow is at `bootChatsRead`.
  bootChatsRead = chatsOK;
  recoverFailedBootRead();
  // Release the frames held since the connection opened: they need a chat ROW and
  // nothing more, so waiting for the tabs would delay the busy dot for no gain.
  // Idempotent. See transport.ts holdUntilHydrated.
  transport.markHydrated();

  try {
    // Runs on every path: a chat list and a tab set are different collections, and only
    // the first can be empty here without the second being meaningless. BOTH awaits sit
    // OUTSIDE the suppression window below, because a window spanning an await silences
    // every push the shell makes; the deep link is the location claim's to protect.
    try {
      await retentionRead;
    } catch {
      /* the close path reads the default */
    }
    // The rule both of these follow is at `bootTabsRead`.
    await readTabSet();
    recoverFailedBootTabs();
    // The window is the RESTORE itself and nothing else: `activateRestoredTab` ends in
    // `pushRoute`, which would add a history entry for a tab nobody navigated to.
    suppressPush(true);
    try {
      clearTabStripSkeleton();
      if (!resumed) {
        activateRestoredTab();
      }
    } finally {
      suppressPush(false);
    }
  } finally {
    // A throw above must not leave the placeholder shimmering forever.
    clearTabStripSkeleton();
  }

  // OUTSIDE the window: both WRITE the URL and a suppressed write is a no-op —
  // `applyInitialRoute`'s `replaceRoute` is what makes the address bar agree with
  // the screen, and a `?agent=planner` launch needs the chat the share created.
  await applyShareTarget();
  // AWAITED: the claim is released when this region settles, and an arm that opens its
  // view through a dynamic import has not opened it yet when the call returns.
  await applyInitialRoute();
  return chatsOK;
}

/** Paint the snapshot AND run the activation it enables, in ONE window with every
 *  push suppressed. Reports whether the resume COMPLETED.
 *
 *  The activation is what needs it: `activateRestoredTab` ends in `pushRoute` (tabs.ts
 *  `activateTab`), which here would add a history entry Back walks into and rewrite
 *  `location.pathname` before `applyInitialRoute` parses it, losing a launch at
 *  /chat/{id} to whatever tab the snapshot was last on. Its OWN window rather than the
 *  boot's widened, which would swallow a real click from the painted shell. */
function resumeSnapshot(snap: BootSnapshot | null): boolean {
  suppressPush(true);
  try {
    if (!paintBootSnapshot(snap)) {
      return false;
    }
    clearTabStripSkeleton();
    // Brought forward from the tab set: this paints the transcript. Its window is
    // stale, so `loadMessages` still refetches.
    activateRestoredTab();
    return true;
  } finally {
    suppressPush(false);
  }
}

function reload(): void {
  location.reload();
}

/** Say what the reader saw and what it cost them. Not dismissible: it explains why the
 *  screen is thinner than usual, and its link is the only control that clears the count.
 *  No DURATION claim, because the count is a sliding run with no ceiling and a sustained
 *  loop reaches numbers no span of seconds could hold. */
function announceReloadLoop(): void {
  const n = reloadCount();
  showBanner(
    GLOBAL_BANNER,
    "reload-loop",
    `This page reloaded ${String(n)} times in a row, so it started with less loaded.`,
    "warning",
    false,
    {
      label: "Start in full mode",
      onClick: () => {
        clearReloadGuard();
        reload();
      },
    },
  );
}

/** Drop the tab strip's authored placeholder (index.html #tab-strip-skeleton).
 *
 *  Removed by id rather than by clearing the container: `tabs.ts` owns the rows
 *  and reconciles them by `data-tab-id`, so the two never contend. Idempotent. */
function clearTabStripSkeleton(): void {
  document.getElementById("tab-strip-skeleton")?.remove();
}

// Everything that must not fire on the login screen, behind ONE door guarded once,
// so a boot that is already signed in and a first login reach the same set.
let postAuthInitDone = false;
export function initPostAuth(): void {
  // OUTSIDE the latch: the capture's lifetime is the SESSION, not the page, so a
  // login after a sign-out disposed it has to be able to start it again. Idempotent.
  startBootSnapshot();
  if (postAuthInitDone) {
    return;
  }
  postAuthInitDone = true;
  // Gates MCP availability, the policy disclosure and the code-reference chip.
  initGovernance();
  // Version info (Settings → About) + git panel wiring incl. the badge.
  initPostAuthUI();
  // Degraded-runtime banner; re-checks on every gap so recovery self-heals.
  initRuntimeHealth();
  // The FETCH-ONLY fan-outs a reduced boot withholds: three calls across two concerns,
  // each fire-and-forget with a usable empty state, so the app still reads a chat. The
  // two above are KEPT: one gates capability, the other reports a degraded runtime.
  if (bootMode() === "full") {
    // The vibekit + kiro-cli build pair. Fire-and-forget: the lines repaint through a
    // signal, so nothing waits on the `--version` subprocess behind it.
    initStatusVersions();
    void loadVersions();
    // So the pickers have content before the first chat's session/new lands.
    void fetchCatalog();
  }
  // Registered here because store.ts is a leaf and may not import run-store.ts or
  // tabs.ts. Registrations rather than reads, so a reduced boot keeps them.
  registerEvictionExemption(hasExecutingRunForChat);
  registerEvictionExemption(runTabProjectsChat);
  registerEvictionExemption(subagentTabProjectsChat);
  // The same shape over the RUN store's cache: who still needs a run's state cell,
  // so `forgetRun` needs no enumeration of its readers at the call site. The run TAB
  // renders that state; the chat-row fold reads it for every live run of an open chat.
  registerRunStateDemand((id) => hasTab("run", id));
  registerRunStateDemand(chatTabFoldsRun);
  startEvictionSweep();
  // The logout button leaves the page running, so without this the debounce keeps
  // writing a signed-out user's workspace to disk. Watches the ACTION so every
  // logout door is covered, by `logout.name` so a rename cannot unwire it.
  subscribeByName(logout.name, (inst) => {
    if (inst.status === "success") {
      forgetDeviceState();
    }
  });
}

/** Forget everything this SCREEN remembers about the workspace it was signed in to.
 *
 *  Both sign-out doors reach it: the boot's `signed_out` verdict and a successful
 *  `logout`. It does NOT un-paint the current frame; what it buys is that the next
 *  boot, and any login in this page, start from nothing. Each callee owns why it is
 *  needed. */
function forgetDeviceState(): void {
  void clearBootSnapshot();
  clearDeviceKeys();
  resetFoldState();
}

async function applyInitialRoute(): Promise<void> {
  const route = parseRoute(location.pathname);
  if (route.kind !== "chat" || route.id !== "") {
    await deps?.applyRoute(route, navigationOrigin());
    return;
  }
  // Default "/": canonicalize the URL to what is visible. An active chat wins,
  // whether or not it has messages yet; otherwise a restored non-chat tab, whose
  // own boot-time push was suppressed.
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

/** Whether the boot's own chat-list read has SETTLED, and whether it ANSWERED.
 *  `undefined` while it is still out.
 *
 *  Both halves are consumed. `onTransportStatus` reads the settled half: the
 *  connection the boot rides lands while the read is in flight and is covered by
 *  it. `recoverFailedBootRead` reads the answer. */
let bootChatsRead: boolean | undefined;

/** The same latch for the TAB set, and only the ANSWER half is consumed: a
 *  connection is not on its own a reason to re-list, because the tab set has a gap
 *  mechanism the chat list lacks (app.ts wires `transport:gap` to `listTabs`). The
 *  one hole that leaves is a BOOT read that never landed, and the boot connection
 *  raises no gap by design (transport.ts, first connection of a page load). */
let bootTabsRead: boolean | undefined;

/** Whether the EventSource is open, as last reported. */
let streamUp = false;

/** Fetch the chat list when the boot's own read FAILED under a stream that is
 *  already up.
 *
 *  Nothing else covers that case: the connection which would have carried the
 *  fetch was skipped while the read was in flight, and a stream that never dropped
 *  delivers no later `connected` — so the store kept whatever the snapshot painted
 *  until the user took the toast's Reload. */
function recoverFailedBootRead(): void {
  if (bootChatsRead === false && streamUp) {
    void loadList();
  }
}

/** The tab set's twin of `recoverFailedBootRead`, for the same uncovered case. */
function recoverFailedBootTabs(): void {
  if (bootTabsRead === false && streamUp) {
    void listTabs();
  }
}

/** Read the tab set, record the answer, and say so when it did not answer.
 *
 *  The notice re-enters HERE rather than reloading: a reload restarts the whole boot,
 *  and the GET is the only thing that failed. Its button dismisses the toast, so a
 *  retry that fails again has to raise it again. */
async function readTabSet(): Promise<void> {
  bootTabsRead = await listTabs();
  if (!bootTabsRead) {
    toastError("Couldn't restore your tabs.", {
      label: "Retry",
      onClick: () => {
        void readTabSet();
      },
    });
  }
}

/** The transport's status callback: paint the indicator, and load the chat list on
 *  every connection the boot's own read does not already cover.
 *
 *  `app.ts` opens the EventSource before the boot, so a cold boot's first `connected`
 *  lands while that read is in flight — fetching there is what made every cold boot
 *  read the whole list twice. Once it has settled EVERY connection fetches: a
 *  reconnect missed frames, and an offline boot that reaches the server minutes later
 *  has no list and no gap to declare, so nothing else would ever load it. */
export function onTransportStatus(status: ConnectionStatus): void {
  setStatus(status);
  streamUp = status === "connected";
  if (!streamUp) {
    return;
  }
  if (bootChatsRead === undefined) {
    return;
  }
  void loadList();
  if (bootTabsRead === false) {
    void listTabs();
  }
}
