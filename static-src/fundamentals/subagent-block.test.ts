// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for fundamentals/subagent-block.ts — the collapsible subagent host.
// Focus: the header identity glyph. While active the slot shows the spinner;
// once settled it shows the SVG icon — the shared agent hexagon by default,
// or the per-known-subagent glyph installed via setIcon (roles.ts
// iconForSubagent keys it off the invoke_sub_agent input name).
// ---------------------------------------------------------------------------

import { vi, describe, it, expect } from "vitest";

vi.mock("../icons.js", () => ({
  ICON_TAB_AGENT: '<svg data-icon="agent-hexagon"></svg>',
}));

import { buildSubagentBlock } from "./subagent-block.js";

const iconSlot = (root: HTMLElement): HTMLElement =>
  root.querySelector(".subagent-icon") as HTMLElement;

describe("buildSubagentBlock icon", () => {
  it("shows the spinner while active, the default hexagon once settled", () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(true);
    expect(iconSlot(sa.root).querySelector("svg")).toBeNull();

    sa.setStatus("completed");
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(false);
    expect(iconSlot(sa.root).querySelector('svg[data-icon="agent-hexagon"]')).not.toBeNull();
  });

  it("setIcon swaps the settled glyph (distinct icon per known subagent)", () => {
    const sa = buildSubagentBlock("Introspect", "completed");
    sa.setIcon('<svg data-icon="introspect"></svg>');
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).not.toBeNull();

    // A later status change keeps the installed glyph.
    sa.setStatus("failed");
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).not.toBeNull();
  });

  it("setIcon while active defers the glyph until the subagent settles", () => {
    const sa = buildSubagentBlock("Introspect", "in_progress");
    sa.setIcon('<svg data-icon="introspect"></svg>');
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(true);
    expect(iconSlot(sa.root).querySelector("svg")).toBeNull();

    sa.setStatus("completed");
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).not.toBeNull();
  });
});
