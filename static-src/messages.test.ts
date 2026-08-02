// @vitest-environment happy-dom
// Unit tests for the event-boundary metadata (EVENT_BOUNDARY_META) derived
// from messages-events.ts's EVENT_RENDER_MAP.
import { describe, it, expect, vi } from "vitest";

// Set up the minimal DOM element that messages.ts needs at module level
// BEFORE any imports resolve.
document.body.innerHTML = '<div id="messages"></div>';

// Mock heavy DOM-dependent modules that messages.ts imports transitively.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
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
vi.mock("./messages-actions.js", () => ({
  initMessageActions: vi.fn(),
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

const { EVENT_RENDER_MAP } = await import("./messages-events.js");

/** Derive boundary metadata from EVENT_RENDER_MAP for test assertions. */
const EVENT_BOUNDARY_META: Readonly<
  Partial<
    Record<
      string,
      { boundary: string; icon: string; defaultLabel: string; labelFn?: (c: string) => string }
    >
  >
> = Object.fromEntries(
  Object.entries(EVENT_RENDER_MAP)
    .filter(([, v]) => v.kind === "boundary")
    .map(([k, v]) => [
      k,
      v as {
        boundary: string;
        icon: string;
        defaultLabel: string;
        labelFn?: (c: string) => string;
      },
    ]),
);

describe("EVENT_BOUNDARY_META", () => {
  it("has entries for all expected event kinds", () => {
    expect.assertions(3);
    const kinds = Object.keys(EVENT_BOUNDARY_META);
    expect(kinds).toContain("model_switched");
    expect(kinds).toContain("compacted");
    expect(kinds).toContain("compaction_failed");
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
    const meta = EVENT_BOUNDARY_META["model_switched"]!;
    expect(meta.labelFn!("gpt-4")).toBe("Switched to gpt-4");
    expect(meta.labelFn!("")).toBe("Context reset");
  });

  it("compaction_failed labelFn produces expected output", () => {
    expect.assertions(2);
    const meta = EVENT_BOUNDARY_META["compaction_failed"]!;
    expect(meta.labelFn!("timeout")).toBe("Compaction failed: timeout");
    expect(meta.labelFn!("")).toBe("Compaction failed");
  });

  it("compacted has no labelFn (uses defaultLabel)", () => {
    expect.assertions(2);
    const meta = EVENT_BOUNDARY_META["compacted"]!;
    expect(meta.labelFn).toBeUndefined();
    expect(meta.defaultLabel).toBe("Conversation compacted");
  });

  it("interrupted is a visible boundary (recovered-turn badge), not skipped", () => {
    expect.assertions(2);
    const meta = EVENT_BOUNDARY_META["interrupted"]!;
    expect(meta).toBeDefined();
    expect(meta.defaultLabel).toBe("Interrupted by server restart");
  });
});
