// ---------------------------------------------------------------------------
// Git view tab bar: switches between Changes / Pull requests / Sources
// panels inside the git view. Mirrors the pattern in settings-tabs.ts.
// ---------------------------------------------------------------------------

import { wireArrowNav } from "./arrow-nav.js";

export type GitTab = "changes" | "prs" | "sources";

const GIT_TABS: readonly GitTab[] = ["changes", "prs", "sources"] as const;

let activeTab: GitTab = "changes";
type Listener = (tab: GitTab) => void;
const listeners = new Set<Listener>();

/** Subscribe to tab changes. Fires immediately with the current tab. */
export function onGitTabChange(fn: Listener): () => void {
  listeners.add(fn);
  fn(activeTab);
  return (): void => {
    listeners.delete(fn);
  };
}

/** Switch to a tab. No-op if already active. */
export function setGitTab(tab: GitTab): void {
  if (tab === activeTab) {
    return;
  }
  activeTab = tab;
  for (const fn of listeners) {
    fn(tab);
  }
}

/** Current active tab. */
export function getGitTab(): GitTab {
  return activeTab;
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
