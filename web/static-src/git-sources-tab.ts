// ---------------------------------------------------------------------------
// Git Sources tab.
//
// Mounts the forge-auth panel into #git-sources-mount. The panel
// renders one section per forge kind (GitHub, GitLab, Codeberg,
// Gitea/Forgejo) with accounts inside each, and a per-account
// collapsible repo list that lets the user clone / remove / open
// individual repos. All the data fetching (forges, repos per forge,
// local clones) lives inside forge-auth.ts now — this module is the
// shell that places the panel under the tab and refreshes it on
// SSE events.
// ---------------------------------------------------------------------------

import { onSSE } from "./bus.js";
import { renderForgesPanel } from "./forge-auth.js";

export function initSourcesTab(): void {
  // Refetch when forges change (login / logout).
  onSSE("forges_changed", () => { void refreshSources(); });
  void refreshSources();
}

/** Re-render the Sources tab. The forge-auth panel is self-contained
 *  (mounts on #forges-panel which we put inside #git-sources-mount).
 *  The panel itself fetches accounts + repos + local-clone names. */
export async function refreshSources(): Promise<void> {
  const root = document.getElementById("git-sources-mount");
  if (root === null) return;
  if (root.querySelector("#forges-panel") === null) {
    const inner = document.createElement("div");
    inner.id = "forges-panel";
    root.replaceChildren(inner);
  }
  await renderForgesPanel();
}
