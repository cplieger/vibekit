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
import { getActiveTabRoute, setSettingsTab as setTabRoute } from "./tabs.js";
import { fitTabBar } from "./tab-bar-fit.js";
import { rovingFocus } from "@cplieger/ui-primitives/roving-focus";
import { setPageSubtitle } from "./page-title.js";

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

// --- Per-tab data loaders ---
//
// Two doors reach a loader: the tab's own ACTIVATION, through `refreshSettingsPanel`,
// and a sub-tab SWITCH, through the subscriber below. Both are gated there rather
// than latched here, because a panel loads once per activation and once per switch
// rather than once per page.
const tabLoaders = new Map<SettingsTab, () => void>();

/** Whether the subscribe-time paint has run. See the `painted` gate below. */
let painted = false;

/** Run the tab's registered loader. `?.` is defensive rather than a tolerance for a
 *  missing loader: `initSettingsTabs` populates the map at boot. */
export function loadSettingsTabData(tab: SettingsTab): void {
  tabLoaders.get(tab)?.();
}

/** Reload the panel the reader is looking at. A settings tab's `refresh`, and it lives
 *  here because this module owns the private `activeTab` and exports no getter. */
export function refreshSettingsPanel(): void {
  loadSettingsTabData(activeTab.peek());
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
    // The title bar's subtitle names the active section. The bar's own
    // segmented control names it too, so 12-chat.css suppresses this while that
    // control shows its labels and reveals it when tab-bar-fit.ts drops them.
    setPageSubtitle("settings", TAB_LABELS[tab]);
    // TWO gates, and neither subsumes the other. `painted` refuses the SUBSCRIBE-TIME
    // fire whatever is on screen — that is `initSettingsTabs` painting the default
    // panel at boot, and a loader there is what put General's `kiro-cli settings`
    // spawns on the boot path. The visibility term refuses a LATER fire while the
    // panel is off screen, which is a panel the ACTIVATION is about to load: without
    // it, `applyRoute`'s `forceSettingsTab`-then-`openTab` order loads it twice.
    if (painted && getActiveTabRoute()?.kind === "settings") {
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
