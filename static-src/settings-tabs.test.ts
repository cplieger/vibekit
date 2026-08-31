import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { TABS, TAB_LABELS } from "./settings-tabs.js";
import type * as ModSettingsTabs from "./settings-tabs.js";

/** Cache-buster for the re-imports below.
 *
 * `vi.resetModules()` does not re-evaluate a module in Browser Mode: the module
 * map is URL-keyed, so a following `await import()` hands back the CACHED
 * instance and every test after the first observes stale module state. Busting
 * the specifier per evaluation is what actually mints a fresh instance. The `.ts`
 * extension is load-bearing — written `.js` the suite still passes while coverage
 * silently attributes every evaluation to a file that does not exist.
 *
 * Only the module under test is busted. Its own dependencies keep their plain
 * specifiers, so `vi.mock` still intercepts them and a shared module the test
 * also imports is the same instance the fresh module got.
 */
let bootSeq = 0;

// swapViews spy: runs its callback synchronously (so the panel swap still
// happens) while recording every invocation. One invocation == one swap.
const { swapViewsSpy } = vi.hoisted(() => ({
  swapViewsSpy: vi.fn((fn: () => HTMLElement | null) => {
    fn();
  }),
}));

vi.mock("./view-swap.js", () => ({
  swapViews: swapViewsSpy,
}));

// Mock ./dom.js so `$` resolves against the test-built DOM. The registry
// resolves the tab bar / select lazily from the live DOM, mirroring the real
// lazy getters in dom.ts so initSettingsTabs() works against whatever was
// built in beforeEach.
vi.mock("./dom.js", () => {
  const idFor: Record<string, string> = {
    settingsTabBar: "settings-tab-bar",
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
  // one button per tab, one panel per tab, and a page title.
  function buildSettingsDom(): void {
    document.body.replaceChildren();
    const bar = document.createElement("div");
    bar.id = "settings-tab-bar";
    for (const t of TABS) {
      const btn = document.createElement("button");
      btn.setAttribute("data-settings-tab", t);
      bar.appendChild(btn);
      const panel = document.createElement("div");
      panel.setAttribute("data-settings-panel", t);
      document.body.appendChild(panel);
    }
    const title = document.createElement("div");
    title.id = "settings-page-title";
    document.body.append(bar, title);
  }

  beforeEach(() => {
    // Fresh module per test → activeTab defaults to "general" and exactly one
    // onTabChange subscriber exists (one initSettingsTabs() per test).
    vi.resetModules();
    bootSeq++;
    buildSettingsDom();
    swapViewsSpy.mockClear();
  });

  afterEach(() => {
    document.body.replaceChildren();
  });

  it("forceSettingsTab on already-active tab does not re-run the panel swap", async () => {
    const { initSettingsTabs, forceSettingsTab } = (await import(
      /* @vite-ignore */ `./settings-tabs.ts?boot=${bootSeq}`
    )) as typeof ModSettingsTabs;
    initSettingsTabs(); // subscribe fires immediately → one swap for the default "general" tab
    swapViewsSpy.mockClear();

    forceSettingsTab("general"); // already active → deduped signal → no notify → no swap

    expect(swapViewsSpy).not.toHaveBeenCalled();
  });

  it("two consecutive forceSettingsTab(tools) swap once", async () => {
    const { initSettingsTabs, forceSettingsTab } = (await import(
      /* @vite-ignore */ `./settings-tabs.ts?boot=${bootSeq}`
    )) as typeof ModSettingsTabs;
    initSettingsTabs();
    swapViewsSpy.mockClear();

    forceSettingsTab("tools"); // general → tools: one swap
    forceSettingsTab("tools"); // tools → tools: deduped, no swap

    expect(swapViewsSpy).toHaveBeenCalledTimes(1);
  });
});
