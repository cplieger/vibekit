// @vitest-environment happy-dom
import { describe, it, expect, vi } from "vitest";

// Mock heavy DOM-dependent modules that renderer.ts imports transitively.
vi.mock("./messages.js", () => ({
  clearMessages: vi.fn(),
  addUserMessage: vi.fn(),
  startStreamingMessage: vi.fn(() => document.createElement("div")),
  appendToAssistant: vi.fn(),
  finalizeAssistantEl: vi.fn(),
  addToolCall: vi.fn(),
  updateToolCall: vi.fn(),
  addPlan: vi.fn(),
  addSystemMessage: vi.fn(),
  addBoundaryDivider: vi.fn(),
  addCrew: vi.fn(),
  updateCrew: vi.fn(),
  addReasoningBlock: vi.fn(),
  EVENT_BOUNDARY_META: {},
}));
vi.mock("./bus.js", () => ({ onSSE: vi.fn() }));

import { checkpointTagForTurn } from "./renderer.js";

describe("checkpointTagForTurn", () => {
  it.each([
    { turnIndex: 0, oldestTag: "", expected: "", desc: "empty oldestTag returns empty" },
    { turnIndex: 0, oldestTag: "0", expected: "0", desc: "turnIndex at oldest boundary" },
    { turnIndex: 1, oldestTag: "0", expected: "1", desc: "turnIndex above oldest" },
    { turnIndex: 5, oldestTag: "3", expected: "5", desc: "turnIndex well above oldest" },
    { turnIndex: 2, oldestTag: "3", expected: "", desc: "turnIndex below oldest returns empty" },
    { turnIndex: 0, oldestTag: "1.3", expected: "", desc: "tool suffix parsed: turnIndex below" },
    { turnIndex: 1, oldestTag: "1.3", expected: "1", desc: "tool suffix parsed: turnIndex at oldest" },
    { turnIndex: 3, oldestTag: "1.3", expected: "3", desc: "tool suffix parsed: turnIndex above" },
    { turnIndex: 0, oldestTag: "abc", expected: "", desc: "NaN from malformed tag returns empty" },
    { turnIndex: 0, oldestTag: ".", expected: "", desc: "dot-only tag returns empty (NaN)" },
    { turnIndex: 0, oldestTag: "0", expected: "0", desc: "zero turnIndex with zero oldest" },
  ])("$desc (turnIndex=$turnIndex, oldestTag=$oldestTag)", ({ turnIndex, oldestTag, expected }) => {
    expect.assertions(1);
    expect(checkpointTagForTurn(turnIndex, oldestTag)).toBe(expected);
  });
});
