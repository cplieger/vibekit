// ---------------------------------------------------------------------------
// Git view orchestrator.
//
// The git page is a tabbed multi-repo dashboard:
//
//   Tab 1 (Changes)        — per-repo collapsible sections of pending
//                              file changes; commit / stage / discard /
//                              pull / stash / pop actions per repo.
//   Tab 2 (Pull requests)  — per-repo collapsible PR lists; create /
//                              merge / close.
//   Tab 3 (Sources)        — forge accounts (login / logout / PAT)
//                              and per-forge cloneable repo lists with
//                              Clone / Trash / Open ↗ actions.
//
// Each tab module owns its own data fetch + render + event wiring;
// this file just wires the tab nav and triggers the initial loads.
//
// Compared to the previous single-active-repo design, there is no
// "selected repo" — every tab operates across all cloned repos at
// once. Per-repo actions hit the same single-repo endpoints as before
// with `repo=<name>` in the body.
// ---------------------------------------------------------------------------

import { initGitTabs, onGitTabChange, getGitTab } from "./git-tabs.js";
import { initChangesTab, refreshChanges } from "./git-changes-tab.js";
import { initPRsTab, refreshPRs } from "./git-prs-tab.js";
import { initSourcesTab, refreshSources } from "./git-sources-tab.js";
import { initStatusBanner } from "./git-status-banner.js";
import { initGitBadge, refreshGitBadge as refreshBadgeImpl } from "./git-badge.js";

let initialized = false;

/** Initialise the git view. Idempotent — only the first call wires
 *  listeners; subsequent calls trigger a refresh of the active tab
 *  (matches the existing loadGitRepos contract). */
export function initGitPanel(): void {
  if (!initialized) {
    initialized = true;
    initGitTabs();
    initChangesTab();
    initPRsTab();
    initSourcesTab();
    initGitBadge();
    initStatusBanner({
      // Connect-forge CTA from the banner: switch to the Sources tab,
      // which holds the per-forge account UI.
      onConnectForge: () => { void (async () => {
        const { setGitTab } = await import("./git-tabs.js");
        setGitTab("sources");
      })(); },
      // Authenticate-gh CTA: deferred for now (the new multi-repo
      // model handles auth via per-forge "Add account" buttons in
      // the Sources tab; the legacy `gh auth login` device-flow
      // wrapper isn't wired through this banner any more).
      onAuthenticateGh: () => { void (async () => {
        const { setGitTab } = await import("./git-tabs.js");
        setGitTab("sources");
      })(); },
    });

    // When the user switches into a tab, run a fresh fetch so they
    // see up-to-date state. Keeps each module's data ownership tight.
    onGitTabChange((tab) => {
      switch (tab) {
        case "changes": void refreshChanges(); break;
        case "prs":     void refreshPRs(); break;
        case "sources": void refreshSources(); break;
      }
    });
  } else {
    // Subsequent invocations refresh the currently active tab so the
    // entry from another part of the app (Files → click commit, agent
    // ended a turn, etc.) sees fresh state.
    switch (getGitTab()) {
      case "changes": void refreshChanges(); break;
      case "prs":     void refreshPRs(); break;
      case "sources": void refreshSources(); break;
    }
  }
}

/** Compatibility export used by app.ts boot path. The legacy name
 *  loadGitRepos came from the single-repo era; keep the symbol so
 *  app.ts doesn't need to know which model is active. */
export function loadGitRepos(): void {
  initGitPanel();
}

/** No-op compatibility export for settings.ts which used to call
 *  syncGitRepo on boot to restore the active repo from server-side
 *  AppSettings. There's no active repo any more, so this does
 *  nothing — kept until the import is cleaned out of settings.ts. */
export function syncGitRepo(_repo: string): void { /* no-op */ }

/** Refresh the changes-tab view. Used by handlers/turn.ts when the
 *  agent finishes a turn that touched files. Also kicks the sidebar
 *  badge so the dot reflects the new state. */
export function refreshGitBadge(): void {
  void refreshChanges();
  void refreshBadgeImpl();
}

/** Mark git state as dirty so the changes view refetches. The legacy
 *  name comes from when the badge had its own dirty flag; today it
 *  triggers both the tab refresh and the sidebar badge re-derivation. */
export function markGitDirty(): void {
  void refreshChanges();
  void refreshBadgeImpl();
}
