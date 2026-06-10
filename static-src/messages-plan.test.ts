// @vitest-environment happy-dom
import { describe, it, expect, vi } from "vitest";

// Plain-function mocks (mockReset/clearMocks strip vi.fn implementations).
vi.mock("./store.js", () => ({ getActiveId: () => "" }));
vi.mock("./plan-actions.js", () => ({
  planToMarkdown: () => "",
  writePlanDraft: () => Promise.resolve(),
  runPlan: () => Promise.resolve(),
}));
vi.mock("./editor-openers.js", () => ({ openPlanDraftPath: () => undefined }));

import { buildPlanRow } from "./messages-plan.js";
import type { PlanEntry } from "./types.js";

function entry(over: Partial<PlanEntry>): PlanEntry {
  return { content: "task", status: "pending", priority: "low", ...over } as PlanEntry;
}

describe("buildPlanRow / updatePlanRow markup", () => {
  it("renders the status glyph + content as a text node and sets data-status", () => {
    const row = buildPlanRow(entry({ content: "Do <the> thing & stuff", status: "completed" }));
    expect(row.dataset["status"]).toBe("completed");
    // text node, not HTML — the angle brackets/ampersand survive verbatim.
    expect(row.textContent).toBe("\u2705 Do <the> thing & stuff");
    expect(row.querySelector(".plan-hi")).toBeNull();
  });

  it("appends a [high] badge element for high priority", () => {
    const row = buildPlanRow(entry({ content: "X", status: "in_progress", priority: "high" }));
    const hi = row.querySelector(".plan-hi");
    expect(hi).not.toBeNull();
    expect(hi?.textContent).toBe("[high]");
    expect(row.textContent).toBe("\ud83d\udd04 X [high]");
  });

  it("uses the empty glyph for pending", () => {
    const row = buildPlanRow(entry({ content: "Y", status: "pending", priority: "low" }));
    expect(row.textContent).toBe("\u2b1c Y");
    expect(row.querySelector(".plan-hi")).toBeNull();
  });
});
