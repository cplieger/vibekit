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

import { initGitTabs, onGitTabChange, getGitTab, readGitTab, type GitTab } from "./git-tabs.js";
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

/** Wire and paint the git view. Idempotent and FETCHLESS: the data half is
 *  `refreshGitView`, which `tabs.ts` calls right after this. */
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

    // The reader switched sub-tabs. The WHOLE callback is gated on `painted`,
    // including the two closes: `subscribe` fires immediately on attach, and that
    // fire is a DOM sync rather than a switch, so neither half of the callback's
    // stated job is true of it.
    onGitTabChange((tab) => {
      if (painted) {
        // A filter belongs to ONE panel, so the box does not survive a sub-tab
        // switch: it would otherwise sit open over the Pull-requests list still
        // narrowing the Changes list behind it. Closing is what lifts the filter —
        // search-popup's close clears the query and repaints — so switching back
        // finds the panel whole rather than narrowed by an empty box.
        changesFind.close();
        prsFind.close();
        refreshGitTab(tab);
      }
      painted = true;
    });
  }
}

/** Whether the subscribe-time paint has run. Same gate, same reason, as
 *  `settings-tabs.ts`'s. */
let painted = false;

/** Refetch ONE sub-tab. */
function refreshGitTab(tab: GitTab): void {
  switch (tab) {
    case "changes":
      // A sub-tab arrival is explicit navigation, so it opts into the server-side
      // per-repo `git fetch` for fresh ahead/behind data.
      void refreshChanges(true);
      break;
    case "prs":
      // NOT the force the Changes tab passes above, and the asymmetry is the point.
      // `?fetch=1` there runs a local `git fetch`, the only way to learn remote state
      // at all. Here every row is already remote and the server caches the listings,
      // so arriving at the tab should cost no subprocess when the answer is known.
      // The refresh button forces.
      void refreshPRs.dispatch({ force: false });
      break;
    case "sources":
      void refreshSources();
      break;
  }
}

/** Refetch the ACTIVE sub-tab. A git tab's `refresh`, and the two invalidation
 *  triggers' way in. `initGitPanel` first because it is one-shot and this dispatches
 *  into the panels it wires. */
export function refreshGitView(): void {
  initGitPanel();
  refreshGitTab(getGitTab());
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
 *  The automatic refresh of the status store, and the reason it holds no timer any
 *  more: something actually writing to the tree is the FACT that it changed, where
 *  a 15-second poll and a `turn_ended` nudge were both guesses.
 *
 *  `paths` are the WORKSPACE-RELATIVE paths the caller knows changed, and passing
 *  them narrows the scan to the repositories that own them — one repo's two git
 *  subprocesses instead of the whole tree's hundred-odd, which is what makes a
 *  trigger per edit affordable. Omit them only when the caller genuinely cannot
 *  name what moved (a shell command); a wrong path is worse than none, because it
 *  scopes the scan away from the repo that actually changed.
 *
 *  Callers, and between them every writer: `handlers/messages.ts` (an agent's
 *  repo-mutating tool call completing), the editor's save, the file browser's
 *  actions, and the shell panel closing.
 *
 *  The legacy name comes from when the badge had its own dirty flag. */
export function markGitDirty(paths?: readonly string[]): void {
  void refreshGitStatus(paths);
  void refreshChanges();
  void refreshBadgeImpl();
}
