// ---------------------------------------------------------------------------
// Settings tab bar: observable store of which tab panel inside Settings
// is active, plus DOM sync for the horizontal pill bar. Every segment shows
// an icon before its label; tab-bar-fit.ts hides all labels if one truncates.
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

import { signal, subscribe } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { swapViews } from "./view-swap.js";
import type { SettingsTab } from "./router.js";
import { pushRoute } from "./router.js";
import { setSettingsTab as setTabRoute } from "./tabs.js";
import { fitTabBar } from "./tab-bar-fit.js";
import { rovingFocus } from "@cplieger/ui-primitives/roving-focus";

export const TABS: readonly SettingsTab[] = [
  "general",
  "tools",
  "permissions",
  "instructions",
] as const;

export const TAB_LABELS: Readonly<Record<SettingsTab, string>> = {
  general: "General",
  tools: "Tools",
  permissions: "Permissions",
  instructions: "Custom instructions",
};

// --- Store ---

type Listener = (tab: SettingsTab) => void;
// Deduped signal: a same-value write is a no-op, so forcing the already-active
// tab no longer re-swaps panels (which caused a double swap on first load and
// spurious view-transitions on same-tab popstate). The outer Settings view is
// still shown/hidden by the router via setTabRoute() in forceSettingsTab(),
// independent of this signal. subscribe() still fires immediately on first
// load to init panels; setSettingsTab keeps its own early-return guard.
const activeTab = signal<SettingsTab>("general");

/** Subscribe to tab changes. Fires immediately with the current tab. */
function onTabChange(fn: Listener): () => void {
  return subscribe(activeTab, fn);
}

/** Switch to a tab. No-op if already active. Updates URL and notifies
 *  subscribers (DOM panel visibility, load callbacks, etc). */
function setSettingsTab(tab: SettingsTab): void {
  if (tab === activeTab.peek()) {
    return;
  }
  setTabRoute(tab);
  pushRoute({ kind: "settings", tab });
  activeTab.value = tab;
}

// --- Per-tab lazy data loaders (B9) ---
//
// Panel data loads are keyed to tab ACTIVATION, not to how Settings was
// opened. Each registered loader fires once, on the first activation of its
// tab — pill click, mobile select, deep link via forceSettingsTab, or a
// route-driven openTab in app.ts (which calls loadSettingsTabData directly).
// Previously loads were keyed off route-driven opens plus the gear button's
// hardcoded loadToolsList (which fetched Tools data while opening the General
// panel), and a pill click inside Settings loaded nothing at all.
const tabLoaders = new Map<SettingsTab, () => void>();
const loadedTabs = new Set<SettingsTab>();

/** Whether the subscribe-time paint has run. See the `painted` gate below. */
let painted = false;

/** Run the tab's registered lazy loader on its first use. Idempotent. */
export function loadSettingsTabData(tab: SettingsTab): void {
  if (loadedTabs.has(tab)) {
    return;
  }
  loadedTabs.add(tab);
  tabLoaders.get(tab)?.();
}

// --- DOM wiring ---

/** Build the tab bar and wire panel visibility. Called once from
 *  settings.ts initUI, which registers the per-tab lazy data loaders here. */
export function initSettingsTabs(loaders?: Partial<Record<SettingsTab, () => void>>): void {
  if (loaders !== undefined) {
    for (const [tab, fn] of Object.entries(loaders)) {
      tabLoaders.set(tab as SettingsTab, fn);
    }
  }
  const bar = $.settingsTabBar;

  bar.setAttribute("role", "tablist");
  bar.setAttribute("aria-label", "Settings sections");

  // The pill buttons are declared statically in the HTML; here we just
  // attach click handlers and mark the initial one active.
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

  // Drop every label only when one cannot fit.
  fitTabBar(bar);

  // Arrow key navigation for the tab bar.
  rovingFocus(bar, "[data-settings-tab]", { orientation: "horizontal" });

  // Sync pill + panel visibility on every tab change.
  onTabChange((tab) => {
    for (const t of TABS) {
      const btn = bar.querySelector<HTMLButtonElement>(`[data-settings-tab="${t}"]`);
      btn?.classList.toggle("active", t === tab);
      btn?.setAttribute("aria-selected", t === tab ? "true" : "false");
      btn?.setAttribute("tabindex", t === tab ? "0" : "-1");
    }
    const swap = (): HTMLElement | null => {
      let active: HTMLElement | null = null;
      for (const panel of document.querySelectorAll<HTMLDivElement>("[data-settings-panel]")) {
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
        const panelTab = panel.dataset["settingsPanel"]!;
        const isActive = panelTab === tab;
        panel.classList.toggle("hidden", !isActive);
        panel.setAttribute("role", "tabpanel");
        panel.id = `settings-panel-${panelTab}`;
        panel.setAttribute("aria-labelledby", `settings-tab-${panelTab}`);
        if (isActive) {
          active = panel;
        }
      }
      return active;
    };
    swapViews(swap);
    // Update the page title to the active tab's label.
    const title = document.getElementById("settings-page-title");
    if (title !== null) {
      title.textContent = TAB_LABELS[tab];
    }
    // Lazy panel data, on a tab SWITCH. NOT on the first call, which is
    // `subscribe` painting the default panel at boot with Settings off screen — a
    // loader there is what put General's three `kiro-cli settings` spawns on the
    // boot path. Not the only door either, and not the DEFAULT tab's: `activeTab`
    // is deduped, so re-selecting "general" notifies nobody, and what loads it is
    // the tab factory's `onShow` calling `loadSettingsTabData` (tab-materialize.ts).
    if (painted) {
      loadSettingsTabData(tab);
    }
    painted = true;
  });
}

/** Externally force the active tab without pushing a URL — used by the
 *  router when back/forward navigation lands on a /settings/<tab> URL.
 *  Safe to call even if Settings isn't currently the active app tab;
 *  the tab state is preserved for when it opens. */
export function forceSettingsTab(tab: SettingsTab): void {
  setTabRoute(tab);
  activeTab.value = tab;
}
