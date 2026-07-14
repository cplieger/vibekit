// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { TABS, TAB_LABELS } from "./settings-tabs.js";

// maybeViewTransition spy: runs its callback synchronously (so the panel swap
// still happens) while recording every invocation. One invocation == one swap.
const { viewTransitionSpy } = vi.hoisted(() => ({
  viewTransitionSpy: vi.fn((fn: () => void) => {
    fn();
  }),
}));

// Mock ./dom.js so the panel swap is observable through the spy. The `$`
// registry resolves the test-built tab bar / select lazily from the live DOM,
// mirroring the real lazy getters in dom.ts so initSettingsTabs() works
// against whatever was built in beforeEach.
vi.mock("./dom.js", () => {
  const idFor: Record<string, string> = {
    settingsTabBar: "settings-tab-bar",
    settingsTabSelect: "settings-tab-select",
  };
  const byIdReal = (id: string): HTMLElement => {
    const e = document.getElementById(id);
    if (e === null) {
      throw new Error(`Missing element: #${id}`);
    }
    return e;
  };
  return {
    $: new Proxy(
      {},
      {
        get(_target, prop): unknown {
          if (typeof prop === "string") {
            const id = idFor[prop];
            if (id !== undefined) {
              return byIdReal(id);
            }
          }
          return document.createElement("div");
        },
      },
    ),
    byId: byIdReal,
    maybeEl: (id: string): HTMLElement | null => document.getElementById(id),
    maybeViewTransition: viewTransitionSpy,
  };
});

describe("settings-tabs TAB_LABELS coverage", () => {
  it("every TABS entry has a non-empty label", () => {
    for (const tab of TABS) {
      const label = TAB_LABELS[tab];
      expect(label, `TAB_LABELS["${tab}"] should be a non-empty string`).toBeTruthy();
      expect(typeof label).toBe("string");
      expect(label.length).toBeGreaterThan(0);
    }
  });

  it.each([
    { tab: "general", label: "General" },
    { tab: "tools", label: "Tools" },
    { tab: "permissions", label: "Permissions" },
    { tab: "instructions", label: "Custom instructions" },
    // "git" removed: the Git & forges settings tab was retired (it had no
    // panel or pill in the DOM; /settings/git canonicalizes to General).
  ] as const)("$tab → $label", ({ tab, label }) => {
    expect(TAB_LABELS[tab]).toBe(label);
  });

  it("TABS has exactly 4 entries", () => {
    expect(TABS).toHaveLength(4);
  });
});

describe("settings-tabs forceSettingsTab dedup", () => {
  // Build the panel layout contract initSettingsTabs() expects: a tab bar with
  // one button per tab, a mobile select, one panel per tab, and a page title.
  function buildSettingsDom(): void {
    document.body.replaceChildren();
    const bar = document.createElement("div");
    bar.id = "settings-tab-bar";
    const select = document.createElement("select");
    select.id = "settings-tab-select";
    for (const t of TABS) {
      const btn = document.createElement("button");
      btn.setAttribute("data-settings-tab", t);
      bar.appendChild(btn);
      const opt = document.createElement("option");
      opt.value = t;
      select.appendChild(opt);
      const panel = document.createElement("div");
      panel.setAttribute("data-settings-panel", t);
      document.body.appendChild(panel);
    }
    const title = document.createElement("div");
    title.id = "settings-page-title";
    document.body.append(bar, select, title);
  }

  beforeEach(() => {
    // Fresh module per test → activeTab defaults to "general" and exactly one
    // onTabChange subscriber exists (one initSettingsTabs() per test).
    vi.resetModules();
    buildSettingsDom();
    viewTransitionSpy.mockClear();
  });

  afterEach(() => {
    document.body.replaceChildren();
  });

  it("forceSettingsTab on already-active tab does not re-run the panel swap", async () => {
    const { initSettingsTabs, forceSettingsTab } = await import("./settings-tabs.js");
    initSettingsTabs(); // subscribe fires immediately → one swap for the default "general" tab
    viewTransitionSpy.mockClear();

    forceSettingsTab("general"); // already active → deduped signal → no notify → no swap

    expect(viewTransitionSpy).not.toHaveBeenCalled();
  });

  it("two consecutive forceSettingsTab(tools) swap once", async () => {
    const { initSettingsTabs, forceSettingsTab } = await import("./settings-tabs.js");
    initSettingsTabs();
    viewTransitionSpy.mockClear();

    forceSettingsTab("tools"); // general → tools: one swap
    forceSettingsTab("tools"); // tools → tools: deduped, no swap

    expect(viewTransitionSpy).toHaveBeenCalledTimes(1);
  });
});
