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

import { initGitTabs, onGitTabChange, getGitTab, readGitTab } from "./git-tabs.js";
import { initChangesTab, refreshChanges, changesFind } from "./git-changes-tab.js";
import { initPRsTab, prsFind } from "./git-prs-tab.js";
import { refreshPRs } from "./actions/git-prs.js";
import { initSourcesTab, refreshSources } from "./git-sources-tab.js";
import { initStatusBanner } from "./git-status-banner.js";
import { initGitBadge, refreshGitBadge as refreshBadgeImpl } from "./git-badge.js";
import { refreshGitStatus } from "./git-status-store.js";
import { registerFind } from "./find-registry.js";
import type { PageFind } from "./find-registry.js";
import type { SearchPopup } from "./search-popup.js";

/** The git view's find, routed to the ACTIVE panel — the same shape docs.ts uses
 *  for its six sub-tabs, because the question is the same one: a page with
 *  sub-tabs has one search affordance and it belongs to whatever is on screen.
 *
 *  Sources DECLINES. It lists forge accounts and cloneable repositories fetched
 *  per forge, not one filterable inventory, so `open` answers false there and the
 *  chord falls through to the browser's own find. `available` is what collapses
 *  the toolbar's magnifier rather than leaving it as a button that does nothing. */
const gitFind: PageFind = {
  open: () => activeFind()?.open() ?? false,
  toggle: () => {
    activeFind()?.toggle();
  },
  focused: () => activeFind()?.focused() ?? false,
  // Both panels FILTER — they narrow rows already fetched — so the toolbar shows a
  // funnel here rather than the magnifier it shows over a chat. The fallback value
  // is never rendered: `available` is false on the tab that has no box.
  kind: () => activeFind()?.kind() ?? "filter",
  available: () => activeFind() !== null,
};

function activeFind(): SearchPopup | null {
  // The REACTIVE read, so the toolbar's affordance effect re-runs on a sub-tab
  // switch. Outside an effect it is an ordinary read.
  switch (readGitTab()) {
    case "changes":
      return changesFind;
    case "prs":
      return prsFind;
    default:
      return null;
  }
}

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
    // Through the LEAF registry, like /docs and /history: importing
    // find-dispatch here would drag find-in-chat and scroll.ts's
    // self-initialising singleton into the git view.
    registerFind("git", gitFind);
    initStatusBanner({
      // Connect-forge CTA from the banner: switch to the Sources tab,
      // which holds the per-forge account UI.
      onConnectForge: () => {
        void (async () => {
          const { setGitTab } = await import("./git-tabs.js");
          setGitTab("sources");
        })().catch(() => {
          /* noop */
        });
      },
      // Authenticate-gh CTA: deferred for now (the new multi-repo
      // model handles auth via per-forge "Add account" buttons in
      // the Sources tab; the legacy `gh auth login` device-flow
      // wrapper isn't wired through this banner any more).
      onAuthenticateGh: () => {
        void (async () => {
          const { setGitTab } = await import("./git-tabs.js");
          setGitTab("sources");
        })().catch(() => {
          /* noop */
        });
      },
    });

    // When the user switches into a tab, run a fresh fetch so they
    // see up-to-date state. Keeps each module's data ownership tight.
    // The subscription fires immediately with the current tab, which
    // doubles as the Changes tab's initial load (initChangesTab itself
    // no longer fires one — EX-1). Activation is explicit user
    // navigation, so it opts into the server-side per-repo git fetch
    // (?fetch=1) for fresh ahead/behind data (18-F3).
    onGitTabChange((tab) => {
      // A filter belongs to ONE panel, so the box does not survive a sub-tab
      // switch: it would otherwise sit open over the Pull-requests list still
      // narrowing the Changes list behind it. Closing is what lifts the filter —
      // search-popup's close clears the query and repaints — so switching back
      // finds the panel whole rather than narrowed by an empty box.
      changesFind.close();
      prsFind.close();
      switch (tab) {
        case "changes":
          void refreshChanges(true);
          break;
        case "prs":
          // NOT the force the Changes tab passes above, and the asymmetry is the
          // point. `?fetch=1` there runs a local `git fetch`, the only way to
          // learn remote state at all. Here every row is already remote and the
          // server caches the listings, so arriving at the tab should cost no
          // subprocess when the answer is known. The refresh button forces.
          void refreshPRs.dispatch({ force: false });
          break;
        case "sources":
          void refreshSources();
          break;
      }
    });
  } else {
    // Subsequent invocations refresh the currently active tab so the
    // entry from another part of the app (Files → click commit, agent
    // ended a turn, etc.) sees fresh state. Re-entering the git view
    // is user navigation → fetch=1, same as tab activation.
    switch (getGitTab()) {
      case "changes":
        void refreshChanges(true);
        break;
      case "prs":
        void refreshPRs.dispatch({ force: false });
        break;
      case "sources":
        void refreshSources();
        break;
    }
  }
}

/** Compatibility export used by app.ts boot path. The legacy name
 *  loadGitRepos came from the single-repo era; keep the symbol so
 *  app.ts doesn't need to know which model is active. */
export function loadGitRepos(): void {
  initGitPanel();
}

/** Refresh the changes-tab view. Used by handlers/turn.ts when the
 *  agent finishes a turn that touched files. Also kicks the sidebar
 *  badge so the dot reflects the new state. */
export function refreshGitBadge(): void {
  void refreshChanges();
  void refreshBadgeImpl();
}

/** Mark git state as dirty so every git surface refetches.
 *
 *  The one automatic refresh of the status store, and the reason it holds no timer
 *  any more: a repo-mutating tool call completing is the FACT that the tree
 *  changed, where a 15-second poll and a `turn_ended` nudge were both guesses. Its
 *  caller is `handlers/messages.ts`, which sees each completion.
 *
 *  The legacy name comes from when the badge had its own dirty flag. */
export function markGitDirty(): void {
  void refreshGitStatus();
  void refreshChanges();
  void refreshBadgeImpl();
}
