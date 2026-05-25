// @vitest-environment happy-dom
// Unit tests for crew-card.ts pure functions: signature, titleFor, formatToolActivity.
import { describe, it, expect, vi } from "vitest";

// Set up the minimal DOM that transitive imports need at module level.
document.body.innerHTML = '<div id="messages"></div>';

// Mock heavy DOM-dependent modules that crew-card.ts imports transitively.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
vi.mock("./tool-group.js", () => ({
  breakToolGroup: vi.fn(),
  trackInProgress: vi.fn(),
}));
vi.mock("./tool-card.js", () => ({
  buildToolCard: vi.fn(() => document.createElement("div")),
}));
vi.mock("./transport.js", () => ({
  send: vi.fn(),
}));
vi.mock("./store.js", () => ({
  getActiveId: vi.fn(() => ""),
}));

import { signature, titleFor } from "./crew-card.js";
import { formatToolActivity } from "./format-tool-activity.js";
import type { Crew } from "./types.js";

// --- signature ---

describe("signature", () => {
  const cases: { name: string; crew: Crew; expected: string }[] = [
    {
      name: "single working subagent",
      crew: {
        group: "g1",
        subagents: [
          { session_id: "s1", session_name: "n1", agent_name: "a1", initial_query: "", status: "working", group: "g1" },
        ],
      },
      expected: "g1|s1:working:;",
    },
    {
      name: "multiple subagents with status_msg",
      crew: {
        group: "g2",
        subagents: [
          { session_id: "s1", session_name: "n1", agent_name: "a1", initial_query: "", status: "terminated", status_msg: "done", group: "g2" },
          { session_id: "s2", session_name: "n2", agent_name: "a2", initial_query: "", status: "error", status_msg: "timeout", group: "g2" },
        ],
      },
      expected: "g2|s1:terminated:done;s2:error:timeout;",
    },
    {
      name: "with pending stages",
      crew: {
        group: "g3",
        subagents: [
          { session_id: "s1", session_name: "n1", agent_name: "a1", initial_query: "", status: "working", group: "g3" },
        ],
        pending_stages: [
          { name: "stage-a", agent_name: "a2" },
        ],
      },
      expected: "g3|s1:working:;p:stage-a;",
    },
    {
      name: "no subagents, no pending stages",
      crew: { group: "empty", subagents: [] },
      expected: "empty|",
    },
    {
      name: "pending_stages undefined",
      crew: { group: "x", subagents: [{ session_id: "s1", session_name: "n", agent_name: "a", initial_query: "", status: "pending", group: "x" }] },
      expected: "x|s1:pending:;",
    },
  ];

  for (const { name, crew, expected } of cases) {
    it(name, () => {
      expect(signature(crew)).toBe(expected);
    });
  }
});

// --- titleFor ---

describe("titleFor", () => {
  const cases: { name: string; crew: Crew; expected: string }[] = [
    {
      name: "strips crew- prefix",
      crew: { group: "crew-research", subagents: [] },
      expected: "Crew: research",
    },
    {
      name: "no crew- prefix",
      crew: { group: "analysis", subagents: [] },
      expected: "Crew: analysis",
    },
    {
      name: "empty group after stripping prefix",
      crew: { group: "crew-", subagents: [] },
      expected: "Crew",
    },
    {
      name: "empty group",
      crew: { group: "", subagents: [] },
      expected: "Crew",
    },
  ];

  for (const { name, crew, expected } of cases) {
    it(name, () => {
      expect(titleFor(crew)).toBe(expected);
    });
  }
});

// --- formatToolActivity ---

describe("formatToolActivity", () => {
  const cases: { name: string; input: string; expected: string }[] = [
    {
      name: "strips Running: prefix",
      input: "Running: readFile",
      expected: "readFile",
    },
    {
      name: "no prefix, short title",
      input: "fsWrite",
      expected: "fsWrite",
    },
    {
      name: "truncates at 50 chars",
      input: "a".repeat(60),
      expected: "a".repeat(47) + "\u2026",
    },
    {
      name: "exactly 50 chars, no truncation",
      input: "b".repeat(50),
      expected: "b".repeat(50),
    },
    {
      name: "strips prefix then truncates",
      input: "Running: " + "c".repeat(60),
      expected: "c".repeat(47) + "\u2026",
    },
    {
      name: "empty string",
      input: "",
      expected: "",
    },
    {
      name: "Running: prefix only",
      input: "Running: ",
      expected: "",
    },
  ];

  for (const { name, input, expected } of cases) {
    it(name, () => {
      expect(formatToolActivity(input)).toBe(expected);
    });
  }
});
