// ---------------------------------------------------------------------------
// The boot sequence: settings, identity, chats, tabs, route, splash.
//
// Moved out of `app.ts` unchanged. The composition root is wiring — it
// constructs modules and injects what they may not import — and a five-`await`
// serial chain that decides whether the app is usable at all is not wiring; it
// is a job with its own order, its own failure branches and its own reasons for
// each. Keeping it in `app.ts` meant a 1,026-line file doing three of them.
//
// What lands here is exactly what was there, in the same order: `startBoot` is
// the old `checkAuthAndStart`, plus `initPostAuth`, `applyInitialRoute` and
// `dismissLoadingScreen`. No await was reordered and no branch was rewritten —
// this move is the precondition for that work, not the work.
//
// `applyRoute` is INJECTED rather than imported. It is a switch over every route
// kind that reaches most of the app's surfaces, so it stays in the composition
// root; importing it here would close the cycle `app.ts` → `boot.ts` →
// `app.ts`. Same shape as the other seams the root wires (`initToolCallbacks`,
// `initAttachmentPillCallbacks`).
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
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
  restoreAll,
  setUserEmail,
} from "./settings.js";
import { restoreLastEffort, restoreLastModel } from "./session-context.js";
import { resolveIdentity, emailOf } from "./identity.js";
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

export function dismissLoadingScreen(): void {
  document.getElementById("app-loading")?.remove();
  $.appRoot.classList.remove("app-hidden");
}

export async function startBoot(d: BootDeps): Promise<void> {
  deps = d;
  // Null means the settings fetch failed, which is NOT the same as "the settings
  // are the defaults". Nothing is restored on that path: the theme keeps the
  // pre-paint cache the inline snippet already applied, the model and effort
  // seeds stay unset (a new chat opens on the model's own default), and boot
  // continues so the app is usable and the failure is recoverable by a reload.
  // Inventing values here is what the client-side default mirrors used to do.
  const settings = await loadSettings();
  if (settings !== null) {
    restoreLastModel(settings.last_model);
    restoreLastEffort(settings.last_effort, settings.last_effort_model);
    // The theme, from the payload already in hand. The toggle was constructed
    // during initUI against the pre-paint cache, so this is where the server's
    // choice replaces that hint — and where the cache is carried across once if
    // the server has none, which is the only value the retired arrangement
    // document hands over (see settings.ts).
    adoptThemeFromSettings(settings);

    suppressPush(true);
    try {
      restoreAll(settings);
    } catch {
      /* best-effort */
    }
    suppressPush(false);
  }

  // `unavailable` is NOT a sign-out, and reading it as one is the defect the
  // three-state answer exists to remove — `identity.ts` owns that mapping and a
  // failed request lands in the same arm. Rendering a retry affordance for it is
  // D3's; coming up anyway is this line's.
  const verdict = await resolveIdentity();
  if (verdict.state === "signed_out") {
    setUserEmail("");
    // Nothing will hydrate the store behind a login modal, so release the held
    // frames rather than leaving the stream stalled until the watchdog fires.
    // Their consumers no-op on a store with no chats, which is correct here.
    transport.markHydrated();
    showLoginModal();
    return;
  }
  setUserEmail(emailOf(verdict));

  initPostAuth();

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
  // Read the retention setting so a tab close knows whether the chat record is
  // kept or deleted. Kept concurrent with the two boot reads below and awaited
  // before the tab list is adopted: serialising it would add a round trip to
  // every boot, while not awaiting it at all leaves a close reading the default
  // (enabled) whenever /api/settings is the slower of them.
  const retentionReady = refreshRetention();

  suppressPush(true);
  // If share-target intends to create a session (e.g. ?agent=planner),
  // skip the default empty-state createSession so we don't end up with
  // an unused "New conversation" tab next to the planner.
  const wantsAgent = new URLSearchParams(location.search).get("agent");
  const shareWillCreate = wantsAgent === "planner";
  try {
    const ok = await loadList();
    // The chat store is populated (or provably unreachable), so the SSE frames
    // held since the connection opened can be released — chief among them the
    // one `turn_state` per busy chat, which is the ONLY channel carrying an
    // in-flight turn to a new client and is never re-broadcast. Released here
    // rather than after the tabs open, because the frames only need a chat ROW
    // to land on, and holding them longer would delay the busy dot for no gain.
    // See transport.ts holdUntilHydrated.
    transport.markHydrated();
    if (!ok || getSessions().length === 0) {
      if (!ok) {
        // Surface the boot failure BEFORE falling back to the empty state, so the
        // fresh "New conversation" reads as a fallback rather than silently
        // impersonating the user's (unreachable) chats.
        toastError("Couldn't load your chats.", {
          label: "Reload",
          onClick: () => {
            location.reload();
          },
        });
      }
      if (!shareWillCreate) {
        // AWAITED: boot continues into applyInitialRoute() below, which resolves
        // the URL against the strip. Detaching would let the route apply against
        // an empty strip and then have a tab appear underneath it.
        await createSession();
      }
    }
    // THE TAB SET, read whole from the server. This is the entire boot restore:
    // no per-kind reopen switch, no editor-file list, no singleton availability
    // filter and no saved order to re-apply, because a tab the collection holds is
    // open and the slice position IS the order. `listTabs` adopts the snapshot
    // through the projection's `reset`, which materializes every row from its
    // subject and points the strip at the tab this SCREEN was last on.
    //
    // It runs on EVERY path, chats or no chats: a chat list and a tab set are
    // different collections, and only the first of them can be empty here without
    // the second being meaningless.
    //
    // The FIRST boot after the cutover opens nothing, because tabs.json starts
    // empty and nothing is migrated from the retired arrangement document. That is
    // accepted rather than papered over — the app is alpha, and an arrangement is
    // re-derivable by opening the tabs again.
    await retentionReady;
    if (!(await listTabs())) {
      // The strip is empty at this point, so an unadopted read leaves the reader
      // with no tabs at all and nothing saying why — the same silent dead end the
      // chats half above already refuses. Nothing retries on its own: there is no
      // gap to detect on a boot connection, and a timer here would re-list against
      // a strip the reader may have started using.
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
      // AWAITED for the same reason as the branch above: applyInitialRoute() runs
      // after this block and reads the strip.
      await createSession();
    }
  }
  suppressPush(false);

  await applyShareTarget();
  applyInitialRoute();
  // The splash comes down only now, with the restored tab's content already
  // painted underneath it (the app root is visibility:hidden, which preserves
  // layout, so activation and scroll measurement ran normally behind it).
  // Dropping it at auth-resolved left the transcript container covering the
  // chat-list load with a top-aligned boot skeleton — the one transcript
  // occupant that predated any view. That skeleton is deleted with this
  // ordering; the per-view skeleton still covers any message fetch that
  // outlives the splash.
  dismissLoadingScreen();
  // Boot restores are done — view swaps animate from here on (B3).
  markBootDone();
}

// One-time post-auth initialization: fetches gated behind a successful
// whoami so the login screen doesn't fan out API calls — the governance
// snapshot, /api/version, and the git-badge poll (/api/git/status-all +
// /api/forges every 15s) all used to fire before auth resolved (B2).
// Runs on boot when already authenticated, or after the first login.
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
  // Version info (Settings → About) + git panel wiring incl. badge poll.
  initPostAuthUI();
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

/** The transport's status callback: paint the indicator, and reload the chat
 *  list on connect.
 *
 *  Lives here because the reload is a boot concern — it is the same `loadList`
 *  the chain above runs, fired again when the stream comes back. */
export function onTransportStatus(status: ConnectionStatus): void {
  setStatus(status);
  if (status === "connected") {
    void loadList();
  }
}
