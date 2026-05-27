// ---------------------------------------------------------------------------
// Settings tab bar: observable store of which tab panel inside Settings
// is active, plus DOM sync for the horizontal pill bar (desktop) and
// native select (mobile).
//
// Architecture mirrors tabs.ts: one state primitive, subscribers that
// reflect state in the DOM and the URL. Any module that wants to jump
// to a specific tab calls setSettingsTab() — which also pushes the
// matching URL so deep-linking and back-button work.
//
// Panel layout contract:
//   <div id="settings-view">
//     <header class="settings-header">...title + tab-bar...</header>
//     <div data-settings-panel="general">...</div>
//     <div data-settings-panel="tools">...</div>
//     <div data-settings-panel="permissions">...</div>
//     <div data-settings-panel="instructions">...</div>
//   </div>
//
// Exactly one panel is visible at a time; the rest get .hidden.
// ---------------------------------------------------------------------------

import { $, maybeViewTransition } from "./dom.js";
import type { SettingsTab } from "./router.js";
import { pushRoute } from "./router.js";
import { setSettingsTab as setTabRoute } from "./tabs.js";
import { wireArrowNav } from "./arrow-nav.js";

export const TABS: readonly SettingsTab[] = [
  "general",
  "tools",
  "permissions",
  "instructions",
  "git",
] as const;

export const TAB_LABELS: Readonly<Record<SettingsTab, string>> = {
  general: "General",
  tools: "Tools",
  permissions: "Permissions",
  instructions: "Custom instructions",
  git: "Git & forges",
};

// --- Store ---

let activeTab: SettingsTab = "general";
type Listener = (tab: SettingsTab) => void;
const listeners = new Set<Listener>();

/** Subscribe to tab changes. Fires immediately with the current tab. */
function onTabChange(fn: Listener): () => void {
  listeners.add(fn);
  fn(activeTab);
  return (): void => {
    listeners.delete(fn);
  };
}

/** Switch to a tab. No-op if already active. Updates URL and notifies
 *  listeners (DOM panel visibility, load callbacks, etc). */
function setSettingsTab(tab: SettingsTab): void {
  if (tab === activeTab) {
    return;
  }
  activeTab = tab;
  setTabRoute(tab);
  pushRoute({ kind: "settings", tab });
  for (const fn of listeners) {
    fn(tab);
  }
}

// --- DOM wiring ---

/** Build the tab bar (desktop pills + mobile select) and wire panel
 *  visibility. Called once from settings.ts initUI. */
export function initSettingsTabs(): void {
  const bar = $.settingsTabBar;
  const select = $.settingsTabSelect;

  bar.setAttribute("role", "tablist");
  bar.setAttribute("aria-label", "Settings sections");

  // Desktop: render pill buttons. The HTML has them declared statically;
  // here we just attach click handlers and mark the initial one active.
  for (const tab of TABS) {
    const btn = bar.querySelector<HTMLButtonElement>(`[data-settings-tab="${tab}"]`);
    if (btn === null) {
      continue;
    }
    btn.setAttribute("role", "tab");
    btn.id = `settings-tab-${tab}`;
    btn.setAttribute("aria-label", TAB_LABELS[tab]);
    btn.setAttribute("aria-controls", `settings-panel-${tab}`);
    btn.addEventListener("click", () => {
      setSettingsTab(tab);
    });
  }

  // Mobile: select. Options are declared in HTML, just bind change.
  select.addEventListener("change", () => {
    const v = select.value;
    if (isSettingsTab(v)) {
      setSettingsTab(v);
    }
  });

  // Arrow key navigation for the desktop tab bar.
  wireArrowNav(bar, "[data-settings-tab]", { orientation: "horizontal" });

  // Sync pill + select + panel visibility on every tab change.
  onTabChange((tab) => {
    for (const t of TABS) {
      const btn = bar.querySelector<HTMLButtonElement>(`[data-settings-tab="${t}"]`);
      btn?.classList.toggle("active", t === tab);
      btn?.setAttribute("aria-selected", t === tab ? "true" : "false");
      btn?.setAttribute("tabindex", t === tab ? "0" : "-1");
    }
    select.value = tab;
    const swap = (): void => {
      for (const panel of document.querySelectorAll<HTMLDivElement>("[data-settings-panel]")) {
        const panelTab = panel.dataset["settingsPanel"]!;
        const isActive = panelTab === tab;
        panel.classList.toggle("hidden", !isActive);
        panel.setAttribute("role", "tabpanel");
        panel.id = `settings-panel-${panelTab}`;
        panel.setAttribute("aria-labelledby", `settings-tab-${panelTab}`);
      }
    };
    maybeViewTransition(swap);
    // Update the page title to the active tab's label.
    const title = document.getElementById("settings-page-title");
    if (title !== null) {
      title.textContent = TAB_LABELS[tab];
    }
  });
}

/** Type guard for the narrow SettingsTab union. */
function isSettingsTab(v: string): v is SettingsTab {
  return TABS.includes(v as SettingsTab);
}

/** Externally force the active tab without pushing a URL — used by the
 *  router when back/forward navigation lands on a /settings/<tab> URL.
 *  Safe to call even if Settings isn't currently the active app tab;
 *  the tab state is preserved for when it opens. */
export function forceSettingsTab(tab: SettingsTab): void {
  activeTab = tab;
  setTabRoute(tab);
  for (const fn of listeners) {
    fn(tab);
  }
}
