// ---------------------------------------------------------------------------
// The boot: settings, identity, chats, retention and the local snapshot are read
// in the FIRST FRAME, and each answer is adopted in its own region as it lands.
// Nothing here is a chain, and nothing waits on the identity verdict — not even an
// empty workspace, whose starter chat is the identity region's own (adoptIdentity).
//
// TWO ORDERINGS ARE REAL. The tab set is adopted after the chat fold: a chat tab's
// row is named from the chat store (tab-materialize.ts `chatName`), and activating
// one whose chat the store lacks paints an error row and fires a second /api/chats
// (chat.ts `activateChatView`). Retention is awaited before the tab set, because
// closing a tab has to know whether the record is kept.
//
// `applyRoute` is INJECTED: it reaches most of the app, so it stays in the
// composition root — importing it would close a cycle.
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

  // FIVE READS, ONE FRAME. Nothing below waits for a request it does not need.
  // `identity` needs no rejection handler: every failure IS its `unavailable` arm
  // (identity.ts). Nor does `snapshotRead`, which resolves null for every failure.
  const settingsRead = loadSettings();
  const identity = resolveIdentity();
  const chatsRead = loadList();
  const retentionRead = refreshRetention();
  const snapshotRead = readBootSnapshot();

  // The workspace region owns every view the boot swaps, so it is what the
  // animation flag waits on — a slow whoami must not hold the first tab switch's
  // transition back, and the identity region paints no view.
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
  // Where the server's choice replaces the pre-paint cache the inline snippet
  // applied, and where that cache is carried across if the server has none.
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
    // Nothing will hydrate the store behind a login modal, so release the held
    // frames rather than leaving the stream stalled until the watchdog fires.
    // Their consumers no-op on a store with no chats, which is correct here.
    transport.markHydrated();
    // And the next boot must not paint this workspace at a login screen.
    void clearBootSnapshot();
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

  // A rejected region is the same answer as `false`: the chats are not in the
  // store.
  const chatsOK = await workspace.catch(() => false);
  if (!chatsOK) {
    // Before the fallback below, so a fresh chat does not read as the user's.
    toastError("Couldn't load your chats.", { label: "Reload", onClick: reload });
  }
  if (getSessions().length === 0) {
    // NOTHING TO SHOW, and the chat STORE is the test rather than `chatsOK`: a
    // failed fetch over a painted snapshot has rows on screen already, and a share
    // has already created its chat inside the region — hence no flag for either.
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
  // Neither arm has settled yet (same turn as the reads), so arrival decides.
  //
  // Only a read that SUCCEEDED is an answer. `loadList` resolves false on a network
  // failure without retrying, so it frequently settles first — and counting that as
  // an answer discarded the hint on the one resume it exists for, an offline one.
  const hint = await Promise.race([
    snapshotRead,
    chatsRead.then(
      (ok) => (ok ? null : snapshotRead),
      () => snapshotRead,
    ),
  ]);

  // BEST-EFFORT, like `adoptSettings`'s restore: everything below is the authoritative
  // restore, and a hint must not cost the reader that. A throw leaves `resumed` false,
  // so a half-painted resume falls through to the tab set's own activation.
  let resumed = false;
  try {
    resumed = resumeSnapshot(hint);
  } catch {
    /* best-effort */
  }

  // A rejected read is the same answer as `false` — the chats are not in the
  // store — so the boot has one failure path rather than two.
  const chatsOK = await chatsRead.catch(() => false);
  // Which connection the transport hook may skip; the rule is at `bootChatsRead`.
  bootChatsRead = chatsOK;
  // Release the frames held since the connection opened: they need a chat ROW to
  // land on and nothing more, so waiting for the tabs would delay the busy dot for
  // no gain. Idempotent. See transport.ts holdUntilHydrated.
  transport.markHydrated();

  suppressPush(true);
  try {
    // THE TAB SET is the entire boot restore, and it runs on every path: a chat
    // list and a tab set are different collections, and only the first can be
    // empty here without the second being meaningless.
    try {
      await retentionRead;
    } catch {
      /* the close path reads the default */
    }
    if (!(await listTabs())) {
      // Nothing retries on its own: there is no gap to detect on a boot
      // connection, and a timer would re-list under a reader already using the
      // strip.
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

  // OUTSIDE the window: both of these WRITE the URL and a suppressed write is a no-op
  // — `applyInitialRoute`'s `replaceRoute` is what makes the address bar agree with the
  // screen, and a `?agent=planner` launch needs the chat the share created named.
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
    // The activation the tab set would otherwise run, brought forward: this paints
    // the transcript. Its window is stale, so `loadMessages` still refetches.
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

// One-time post-auth initialization: everything that must not fire on the login
// screen. The governance snapshot, /api/version, the git-badge read and the
// workspace catalog all used to fire before auth resolved.
//
// ONE door, guarded once, so a boot that is already signed in and a first login
// reach the same set. They did not: the four reads at the end of this function
// ran on the boot path only, so after a login the status card's version lines
// stayed "-" and the runtime probe never ran until the next reload.
let postAuthInitDone = false;
export function initPostAuth(): void {
  if (postAuthInitDone) {
    return;
  }
  postAuthInitDone = true;
  // Governance snapshot + live-update subscription. Gates MCP availability
  // (Settings → Tools), renders the read-only Organization-policy
  // disclosure (Settings → General), and gates the code-reference chip.
  initGovernance();
  // Version info (Settings → About) + git panel wiring incl. the badge.
  initPostAuthUI();
  // Degraded-runtime probe (kiro-cli missing → app-global banner);
  // re-checks on every transport gap so recovery self-heals.
  initRuntimeHealth();
  // The vibekit + kiro-cli build pair, for the status card's two lines and
  // Settings → About. Fire-and-forget: one read per page load, and the lines
  // repaint through a signal when it lands, so nothing waits on the `--version`
  // subprocess the server spawns to answer it.
  initStatusVersions();
  void loadVersions();
  // The workspace mode/model/effort catalog, so the pickers have content before
  // the first chat's session/new lands.
  void fetchCatalog();
  // The live-runs inventory: boot is one of its two rebuild triggers (the
  // other is transport:gap, wired in handlers/system.ts). It feeds the
  // eviction sweep's live-run exemption, registered here beside the
  // subagent-tab one because store.ts is a leaf and must not import
  // run-store.ts or tabs.ts — the composition root wires what it may not.
  void rebuildLiveRuns();
  registerEvictionExemption(hasLiveRunForChat);
  registerEvictionExemption(subagentTabProjectsChat);
  startEvictionSweep();
  // The debounced capture that feeds the next boot's first frame. Here rather
  // than in the boot body for the same reason everything else here is: there is
  // nothing worth remembering about a login screen.
  startBootSnapshot();
  // And its other end, here because the capture's lifetime is owned here: the logout
  // button leaves the page running, so without this the debounce keeps writing a
  // signed-out user's workspace to disk. Watches the ACTION, so every logout door is
  // covered, and `logout.name` so a rename cannot silently unwire it.
  subscribeByName(logout.name, (inst) => {
    if (inst.status === "success") {
      void clearBootSnapshot();
    }
  });
}

function applyInitialRoute(): void {
  const route = parseRoute(location.pathname);
  if (route.kind !== "chat" || route.id !== "") {
    deps?.applyRoute(route);
    return;
  }
  // Default "/" route. Canonicalize the URL to what's actually visible:
  //   - active chat → /chat/{id}, whether or not it has messages yet;
  //   - restored non-chat tab (Settings, git, …) → its route, so the
  //     restored view and the URL agree (their boot-time pushRoute was
  //     suppressed).
  //
  // The `message_count > 0` condition this used to carry was the ghost window in
  // all but name: a zero-message chat's id was minted in this tab's memory, so
  // /chat/{id} could not be resolved on reload and would mint a fresh id every
  // load — hence staying on "/" and letting handlers/chat.ts flip the URL once
  // the server acknowledged the chat. The id is the server's from the moment the
  // chat exists now, so a brand-new chat's URL resolves like any other and there
  // is nothing to withhold.
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

/** The outcome of the boot's OWN chat-list read, or undefined while it is still
 *  out. Three states rather than "a connection happened", because those are three
 *  different answers to whether a `connected` needs to fetch: the connection the
 *  boot rides arrives while the read is in flight and is covered by it, a later one
 *  missed frames, and a boot whose read FAILED is covered by nothing at all. */
let bootChatsRead: boolean | undefined;

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
  if (status !== "connected") {
    return;
  }
  if (bootChatsRead === undefined) {
    return;
  }
  void loadList();
}
