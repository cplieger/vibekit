// @vitest-environment happy-dom
// Unit tests for messages.ts pure exports: EVENT_BOUNDARY_META and formatToolActivity.
import { describe, it, expect, vi } from "vitest";

// Set up the minimal DOM element that messages.ts needs at module level
// BEFORE any imports resolve.
document.body.innerHTML = '<div id="messages"></div>';

// Mock heavy DOM-dependent modules that messages.ts imports transitively.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
vi.mock("./subagent.js", () => ({
  isSubAgent: vi.fn(() => false),
  isSubAgentActive: vi.fn(() => false),
  appendToSubAgent: vi.fn(),
  createSubAgentCard: vi.fn(),
  updateSubAgentCard: vi.fn(),
  resetSubAgents: vi.fn(),
}));
vi.mock("./tool-group.js", () => ({
  breakToolGroup: vi.fn(),
  getOrCreateToolGroup: vi.fn(),
  maybeCollapseGroup: vi.fn(),
  formatDuration: vi.fn(),
  untrackInProgress: vi.fn(),
}));
vi.mock("./tool-card.js", () => ({
  buildToolCard: vi.fn(() => document.createElement("div")),
  insertDiffPreview: vi.fn(),
}));
vi.mock("./api-client.js", () => ({ apiPost: vi.fn() }));
vi.mock("./plan-actions.js", () => ({
  planToMarkdown: vi.fn(() => ""),
  writePlanDraft: vi.fn(),
  runPlan: vi.fn(),
}));
vi.mock("./editor-openers.js", () => ({ openPlanDraftPath: vi.fn() }));
vi.mock("./messages-actions.js", () => ({
  addEditActions: vi.fn(),
  initMessageActions: vi.fn(),
  refreshConflictBadges: vi.fn(),
}));
vi.mock("./crew-card.js", () => ({
  addCrew: vi.fn(),
  updateCrew: vi.fn(),
  buildCrewCardForReplay: vi.fn(),
  clearCrews: vi.fn(),
  addToolToCrewRow: vi.fn(),
  getCrewToolEl: vi.fn(),
  onCrewToolCompleted: vi.fn(),
  setSubagentActivity: vi.fn(),
}));
vi.mock("./store.js", () => ({
  getActiveId: () => "test-chat",
}));
vi.mock("./linkify.js", () => ({ linkifyPaths: vi.fn() }));
vi.mock("./code-blocks.js", () => ({ setShellRunCallback: vi.fn() }));
vi.mock("./permission.js", () => ({
  showPermissionDialog: vi.fn(),
  hidePermission: vi.fn(),
}));

const { EVENT_BOUNDARY_META, formatToolActivity } = await import("./messages.js");

describe("EVENT_BOUNDARY_META", () => {
  it("has entries for all expected event kinds", () => {
    expect.assertions(4);
    const kinds = Object.keys(EVENT_BOUNDARY_META);
    expect(kinds).toContain("model_switched");
    expect(kinds).toContain("compacted");
    expect(kinds).toContain("compaction_failed");
    expect(kinds).toContain("agent_switched");
  });

  it("every entry has required fields", () => {
    const entries = Object.entries(EVENT_BOUNDARY_META);
    expect.assertions(entries.length * 3);
    for (const [, meta] of entries) {
      expect(meta!.boundary).toBeTruthy();
      expect(meta!.icon).toBeDefined();
      expect(meta!.defaultLabel).toBeTruthy();
    }
  });

  it("model_switched labelFn produces expected output", () => {
    expect.assertions(2);
    const meta = EVENT_BOUNDARY_META.model_switched!;
    expect(meta.labelFn!("gpt-4")).toBe("Switched to gpt-4");
    expect(meta.labelFn!("")).toBe("Context reset");
  });

  it("compaction_failed labelFn produces expected output", () => {
    expect.assertions(2);
    const meta = EVENT_BOUNDARY_META.compaction_failed!;
    expect(meta.labelFn!("timeout")).toBe("Compaction failed: timeout");
    expect(meta.labelFn!("")).toBe("Compaction failed");
  });

  it("agent_switched labelFn produces expected output", () => {
    expect.assertions(2);
    const meta = EVENT_BOUNDARY_META.agent_switched!;
    expect(meta.labelFn!("planner")).toBe("planner");
    expect(meta.labelFn!("")).toBe("Agent switched");
  });

  it("compacted has no labelFn (uses defaultLabel)", () => {
    expect.assertions(2);
    const meta = EVENT_BOUNDARY_META.compacted!;
    expect(meta.labelFn).toBeUndefined();
    expect(meta.defaultLabel).toBe("Conversation compacted");
  });
});

describe("formatToolActivity", () => {
  it("strips 'Running: ' prefix", () => {
    expect.assertions(1);
    expect(formatToolActivity("Running: npm test")).toBe("npm test");
  });

  it("passes through titles without prefix", () => {
    expect.assertions(1);
    expect(formatToolActivity("read_file")).toBe("read_file");
  });

  it("truncates long titles at 50 chars with ellipsis", () => {
    expect.assertions(2);
    const long = "a".repeat(60);
    const result = formatToolActivity(long);
    expect(result.length).toBe(48); // 47 chars + ellipsis char
    expect(result).toBe("a".repeat(47) + "\u2026");
  });

  it("does not truncate titles at exactly 50 chars", () => {
    expect.assertions(1);
    const exact = "b".repeat(50);
    expect(formatToolActivity(exact)).toBe(exact);
  });

  it("handles empty string", () => {
    expect.assertions(1);
    expect(formatToolActivity("")).toBe("");
  });

  it("strips prefix then truncates", () => {
    expect.assertions(1);
    const long = "Running: " + "x".repeat(60);
    const result = formatToolActivity(long);
    expect(result).toBe("x".repeat(47) + "\u2026");
  });
});
