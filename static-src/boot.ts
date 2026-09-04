// ---------------------------------------------------------------------------
// The boot: settings, identity, chats and retention are read in the FIRST FRAME,
// and each answer is adopted in its own region as it lands. Nothing here is a
// chain, because none of those four reads consumes another's answer.
//
// TWO ORDERINGS ARE REAL. The tab set is adopted after the chat fold: a chat
// tab's row is named from the chat store (tab-materialize.ts `chatName`), and
// activating one whose chat the store lacks paints an error row and fires a
// second /api/chats (chat.ts `activateChatView`). And retention is awaited before
// the tab set, because closing a tab has to know whether the record is kept.
//
// `applyRoute` is INJECTED, not imported: it reaches most of the app's surfaces,
// so it stays in the composition root, and importing it would close the cycle
// app.ts → boot.ts → app.ts.
// ---------------------------------------------------------------------------

import { loadList } from "./store-load.js";
import {
  getActive,
  getActiveId,
  getSessions,
  registerEvictionExemption,
  startEvictionSweep,
} from "./store.js";
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

  // FOUR READS, ONE FRAME. Nothing below waits for a request it does not need.
  // `identity` is consumed twice and needs no rejection handler: every failure IS
  // its `unavailable` arm (identity.ts).
  const settingsRead = loadSettings();
  const identity = resolveIdentity();
  const chatsRead = loadList();
  const retentionRead = refreshRetention();

  // The workspace region owns every view the boot swaps, so it is what the
  // animation flag waits on — a slow whoami must not hold the first tab switch's
  // transition back, and the identity region paints no view.
  const workspace = restoreWorkspace(chatsRead, retentionRead, identity).finally(() => {
    markBootDone();
  });
  await Promise.allSettled([
    settingsRead.then(adoptSettings),
    identity.then(adoptIdentity),
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

/** The identity answer: one sidebar row, and the post-auth fan-out.
 *
 *  `signed_out` raises the login modal over an already-painted shell and holds the
 *  post-auth fetches back, so the login screen makes no API calls. `unavailable`
 *  means vibekit could not ASK, so it comes up working with a re-read offered —
 *  never a sign-in prompt. */
function adoptIdentity(v: IdentityVerdict): void {
  renderIdentity(v);
  if (v.state === "signed_out") {
    // Nothing will hydrate the store behind a login modal, so release the held
    // frames rather than leaving the stream stalled until the watchdog fires.
    // Their consumers no-op on a store with no chats, which is correct here.
    transport.markHydrated();
    showLoginModal();
    return;
  }
  if (v.state === "unavailable") {
    toastError(`Couldn't confirm who is signed in: ${v.reason}`, {
      label: "Retry",
      onClick: () => {
        void resolveIdentity().then(adoptIdentity);
      },
    });
  }
  initPostAuth();
}

/** The chats answer, then the tab set, then the route.
 *
 *  One region because the three are genuinely ordered (see the header): the store
 *  names the strip's rows, and the strip is what the URL is resolved against. */
async function restoreWorkspace(
  chatsRead: Promise<boolean>,
  retentionRead: Promise<void>,
  identity: Promise<IdentityVerdict>,
): Promise<void> {
  // If share-target intends to create a session (e.g. ?agent=planner),
  // skip the default empty-state createSession so we don't end up with
  // an unused "New conversation" tab next to the planner.
  const shareWillCreate = new URLSearchParams(location.search).get("agent") === "planner";

  // A rejected read is the same answer as `false` — the chats are not in the
  // store — so the boot has one failure path rather than two.
  const chatsOK = await chatsRead.catch(() => false);
  // Release the frames held since the connection opened: they need a chat ROW to
  // land on and nothing more, so waiting for the tabs would delay the busy dot for
  // no gain. Idempotent. See transport.ts holdUntilHydrated.
  transport.markHydrated();

  suppressPush(true);
  try {
    const starterWanted = (!chatsOK || getSessions().length === 0) && !shareWillCreate;
    if (!chatsOK || starterWanted) {
      // The ONLY place the verdict is consulted, and both uses are uncommon: a
      // boot whose chats loaded never waits for whoami. Behind a login modal there
      // is no chat-load complaint to make and no starter chat to mint —
      // `onLoginSuccess` does that once the identity is real.
      const signedOut = (await identity).state === "signed_out";
      if (!chatsOK && !signedOut) {
        // Before the fallback below, so the fresh chat does not read as the user's.
        toastError("Couldn't load your chats.", { label: "Reload", onClick: reload });
      }
      if (starterWanted && !signedOut) {
        // AWAITED: applyInitialRoute resolves against the strip further down.
        await createSession();
      }
    }
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
    activateRestoredTab();
    await applyShareTarget();
    applyInitialRoute();
  } finally {
    // A throw above must not leave the placeholder shimmering forever.
    clearTabStripSkeleton();
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

/** Whether the transport has ever reported a connection. The boot's own read
 *  covers the first one; see `onTransportStatus`. */
let sawConnected = false;

/** The transport's status callback: paint the indicator, and reload the chat list
 *  on a RE-connect.
 *
 *  Only on a re-connect. This fired on the FIRST connect too, and since the
 *  EventSource is opened before the boot runs, every cold boot fetched the whole
 *  chat list twice — the connect hook's copy and the boot's own. A reconnect is
 *  the case the reload is for: frames were missed while the stream was down. */
export function onTransportStatus(status: ConnectionStatus): void {
  setStatus(status);
  if (status !== "connected") {
    return;
  }
  if (!sawConnected) {
    sawConnected = true;
    return;
  }
  void loadList();
}
