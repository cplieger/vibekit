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
import { activateRestoredTab, getActiveTabRoute } from "./tabs.js";
import { listTabs } from "./tabs-sync.js";
import { parseRoute, replaceRoute, suppressPush } from "./router.js";
import type { Route } from "./router.js";
import { createSession } from "./chat.js";
import { initGovernance } from "./governance.js";
import { initRuntimeHealth } from "./runtime-health.js";
import { initStatusVersions, setStatus } from "./status.js";
import type { ConnectionStatus } from "./types.js";
import { loadVersions } from "./versions.js";
import { refreshRetention } from "./retention.js";
import { hasLiveRunForChat, rebuildLiveRuns } from "./run-store.js";
import { subagentTabProjectsChat } from "./subagent-view.js";
import { markBootDone } from "./view-swap.js";
import { applyShareTarget } from "./share-target.js";
import { error as toastError } from "./toast.js";

/** What the boot chain needs from the composition root. */
export interface BootDeps {
  /** Navigate to a route. Owned by `app.ts`, which is the only place that can
   *  reach every view a route can name. */
  applyRoute: (route: Route) => void;
}

let deps: BootDeps | null = null;

export async function startBoot(d: BootDeps): Promise<void> {
  deps = d;

  // `identity` needs no rejection handler: every failure IS its `unavailable` arm
  // (identity.ts). Nor does `snapshotRead`, which resolves null for every failure.
  const settingsRead = loadSettings();
  const identity = resolveIdentity();
  const chatsRead = loadList();
  const retentionRead = refreshRetention();
  const snapshotRead = readBootSnapshot();

  // The workspace region owns every view the boot swaps, so it is what the
  // animation flag waits on: a slow whoami must not hold the first tab switch's
  // transition back.
  const workspace = restoreWorkspace(chatsRead, retentionRead, snapshotRead).finally(() => {
    markBootDone();
  });
  await Promise.allSettled([
    settingsRead.then(adoptSettings),
    identity.then((v) => adoptIdentity(v, workspace)),
    workspace,
  ]);
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

  // BEST-EFFORT: everything below is the authoritative restore, and a hint must not
  // cost the reader that. A throw leaves `resumed` false, so a half-painted resume
  // falls through to the tab set's own activation.
  let resumed = false;
  try {
    resumed = resumeSnapshot(hint);
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

  suppressPush(true);
  try {
    // Runs on every path: a chat list and a tab set are different collections, and
    // only the first can be empty here without the second being meaningless.
    try {
      await retentionRead;
    } catch {
      /* the close path reads the default */
    }
    if (!(await listTabs())) {
      // Nothing retries on its own: there is no gap to detect on a boot connection,
      // and a timer would re-list under a reader already using the strip.
      toastError("Couldn't restore your tabs.", { label: "Reload", onClick: reload });
    }
    clearTabStripSkeleton();
    if (!resumed) {
      activateRestoredTab();
    }
  } finally {
    // A throw above must not leave the placeholder shimmering forever.
    clearTabStripSkeleton();
    suppressPush(false);
  }

  // OUTSIDE the window: both WRITE the URL and a suppressed write is a no-op —
  // `applyInitialRoute`'s `replaceRoute` is what makes the address bar agree with
  // the screen, and a `?agent=planner` launch needs the chat the share created.
  await applyShareTarget();
  applyInitialRoute();
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
  // The vibekit + kiro-cli build pair. Fire-and-forget: the lines repaint through a
  // signal, so nothing waits on the `--version` subprocess behind it.
  initStatusVersions();
  void loadVersions();
  // So the pickers have content before the first chat's session/new lands.
  void fetchCatalog();
  // The live-runs inventory (the other rebuild trigger is transport:gap). Its
  // eviction exemption is registered here beside the subagent-tab one because
  // store.ts is a leaf and may not import run-store.ts or tabs.ts.
  void rebuildLiveRuns();
  registerEvictionExemption(hasLiveRunForChat);
  registerEvictionExemption(subagentTabProjectsChat);
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

function applyInitialRoute(): void {
  const route = parseRoute(location.pathname);
  if (route.kind !== "chat" || route.id !== "") {
    deps?.applyRoute(route);
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
}
