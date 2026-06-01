// @vitest-environment happy-dom
import { describe, it, expect } from "vitest";
import { TABS, TAB_LABELS } from "./settings-tabs.js";

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
    { tab: "git", label: "Git & forges" },
  ] as const)("$tab → $label", ({ tab, label }) => {
    expect(TAB_LABELS[tab]).toBe(label);
  });

  it("TABS has exactly 5 entries", () => {
    expect(TABS).toHaveLength(5);
  });
});
