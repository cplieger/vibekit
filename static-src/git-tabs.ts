// ---------------------------------------------------------------------------
// Git view tab bar: switches between Changes / Pull requests / Sources
// panels inside the git view. Mirrors the pattern in settings-tabs.ts.
// ---------------------------------------------------------------------------

import { signal, subscribe } from "@cplieger/reactive";
import { wireArrowNav } from "./arrow-nav.js";
import { pushRoute } from "./router.js";
import type { GitTab } from "./router.js";
import { setGitTab as setGitTabRoute } from "./tabs.js";

// GitTab lives in router.ts (the URL source of truth, alongside SettingsTab);
// re-exported here so existing `import { GitTab } from "./git-tabs.js"` callers
// keep working.
export type { GitTab };

const GIT_TABS: readonly GitTab[] = ["changes", "prs", "sources"] as const;

type Listener = (tab: GitTab) => void;

// Deduped signal mirroring settings-tabs.ts: a same-value write is a no-op, so
// re-selecting the active tab no longer re-swaps panels. subscribe() fires
// immediately on attach to init panel visibility; setGitTab keeps its own
// early-return guard.
const activeTab = signal<GitTab>("changes");

/** Subscribe to tab changes. Fires immediately with the current tab. */
export function onGitTabChange(fn: Listener): () => void {
  return subscribe(activeTab, fn);
}

/** Switch to a tab. No-op if already active. Updates the URL (pushState)
 *  and the git view tab's route so the Pull requests / Sources sub-tabs are
 *  deep-linkable and back/forward navigable, mirroring settings-tabs.ts. */
export function setGitTab(tab: GitTab): void {
  if (tab === activeTab.peek()) {
    return;
  }
  setGitTabRoute(tab);
  pushRoute({ kind: "git", tab });
  activeTab.value = tab;
}

/** Current active tab. */
export function getGitTab(): GitTab {
  return activeTab.peek();
}

/** Externally force the active sub-tab WITHOUT pushing a URL — used by the
 *  router when back/forward navigation lands on a /git/<tab> URL. Mirrors
 *  forceSettingsTab. Safe to call before the git view tab exists (the
 *  TabSpec route sync is a no-op then; openTab sets the route directly). */
export function forceGitTab(tab: GitTab): void {
  setGitTabRoute(tab);
  activeTab.value = tab;
}

/** Wire the static tab buttons + panel visibility. Idempotent (only
 *  acts when the tab bar exists in the DOM, i.e. on the git view). */
export function initGitTabs(): void {
  const bar = document.getElementById("git-tab-bar");
  if (bar === null) {
    return;
  }

  for (const tab of GIT_TABS) {
    const btn = bar.querySelector<HTMLButtonElement>(`[data-git-tab="${tab}"]`);
    btn?.addEventListener("click", () => {
      setGitTab(tab);
    });
  }

  // Mobile: <select> mirroring the desktop pill bar. Hidden via CSS
  // on wide viewports; on narrow it replaces the bar.
  const select = document.getElementById("git-tab-select") as HTMLSelectElement | null;
  if (select !== null) {
    select.addEventListener("change", () => {
      const v = select.value;
      if (GIT_TABS.includes(v as GitTab)) {
        setGitTab(v as GitTab);
      }
    });
  }

  wireArrowNav(bar, "[data-git-tab]", { orientation: "horizontal" });

  onGitTabChange((tab) => {
    for (const t of GIT_TABS) {
      const btn = bar.querySelector<HTMLButtonElement>(`[data-git-tab="${t}"]`);
      btn?.classList.toggle("active", t === tab);
      btn?.setAttribute("aria-selected", t === tab ? "true" : "false");
    }
    if (select !== null) {
      select.value = tab;
    }
    for (const panel of document.querySelectorAll<HTMLDivElement>("[data-git-panel]")) {
      panel.classList.toggle("hidden", panel.dataset["gitPanel"] !== tab);
    }
  });
}
