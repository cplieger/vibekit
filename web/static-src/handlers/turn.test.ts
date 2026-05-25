// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Table-driven tests for the ERROR_ROUTES classification table.
// Verifies that all known error codes map to the expected routing triple
// and that unknown codes fall through correctly.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect } from "vitest";

// Mock modules that have side effects requiring DOM elements at import time.
vi.mock("../scroll.js", () => ({ scroll: vi.fn(), trimOldMessages: vi.fn() }));
vi.mock("../messages.js", () => ({ showPermissionDialog: vi.fn() }));
vi.mock("../chat-commands.js", () => ({ sendPromptTo: vi.fn() }));
vi.mock("../attachments.js", () => ({ addAttachment: vi.fn() }));
vi.mock("../send-state.js", () => ({
  setLastError: vi.fn(), clearLastError: vi.fn(), setSSEStatus: vi.fn(),
}));
vi.mock("../notify.js", () => ({
  notifyIfHidden: vi.fn(), setBadge: vi.fn(),
  isAgentFinishedEnabled: () => false, isPermissionNeededEnabled: () => false,
}));
vi.mock("../git.js", () => ({ refreshGitBadge: vi.fn() }));
vi.mock("../banner-stack.js", () => ({ showBanner: vi.fn(), onTurnEnded: vi.fn() }));
vi.mock("../crew-card.js", () => ({ setSubagentPendingApproval: vi.fn() }));
vi.mock("../store.js", () => ({
  get: () => undefined, getActiveId: () => "", setThinking: vi.fn(), setWorkingLabel: vi.fn(),
  dequeuePrompt: vi.fn(), peekQueuedAttachments: vi.fn(() => []),
}));
vi.mock("../transport.js", () => ({ send: vi.fn() }));
vi.mock("../api-client.js", () => ({ apiGet: vi.fn() }));

const { ERROR_ROUTES } = await import("./turn.js");

describe("ERROR_ROUTES", () => {
  const expectedRoutes: Array<[string, { surface: string; level: string; dismissible: boolean }]> = [
    ["agent_not_found",         { surface: "banner",     level: "error",   dismissible: true }],
    ["agent_config_error",      { surface: "banner",     level: "error",   dismissible: false }],
    ["model_not_found",         { surface: "banner",     level: "warning", dismissible: true }],
    ["rate_limit",              { surface: "banner",     level: "warning", dismissible: true }],
    ["compaction_failed",       { surface: "banner",     level: "error",   dismissible: true }],
    ["switch_failed",           { surface: "send-error", level: "error",   dismissible: false }],
    ["bridge_start_failed",     { surface: "send-error", level: "error",   dismissible: false }],
    ["prompt_failed",           { surface: "send-error", level: "error",   dismissible: false }],
  ];

  it.each(expectedRoutes)(
    "routes %s correctly",
    (code, expected) => {
      const route = ERROR_ROUTES[code];
      expect(route).toBeDefined();
      expect(route!.surface).toBe(expected.surface);
      expect(route!.level).toBe(expected.level);
      expect(route!.dismissible).toBe(expected.dismissible);
    },
  );

  it("has exactly 8 entries", () => {
    expect(Object.keys(ERROR_ROUTES)).toHaveLength(8);
  });

  it("unknown codes fall through (not in table)", () => {
    expect(ERROR_ROUTES["unknown_code"]).toBeUndefined();
    expect(ERROR_ROUTES[""]).toBeUndefined();
  });

  it("all entries have valid surface values", () => {
    for (const [, route] of Object.entries(ERROR_ROUTES)) {
      expect(["banner", "send-error"]).toContain(route.surface);
    }
  });

  it("all entries have valid level values", () => {
    for (const [, route] of Object.entries(ERROR_ROUTES)) {
      expect(["error", "warning"]).toContain(route.level);
    }
  });
});
