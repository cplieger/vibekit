// @vitest-environment happy-dom
// Unit tests for crew-card.ts: titleFor, formatToolActivity (pure functions)
// and the activity-line precedence shared by buildRow + the reconcile update.
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
vi.mock("./actions/crew.js", () => ({
  sendMessage: { dispatch: vi.fn() },
}));
vi.mock("./actions/index.js", () => ({
  bindLoadingState: vi.fn(() => () => undefined),
}));

import {
  titleFor,
  buildCrewCardForReplay,
  updateCrew,
  setSubagentActivity,
  setSubagentPendingApproval,
  clearCrews,
} from "./crew-card.js";
import { formatToolActivity } from "./format-tool-activity.js";
import type { Crew } from "./types.js";

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

// --- activity-line precedence (buildRow + reconcile update share applyActivity) ---

describe("activity-line precedence", () => {
  function workingCrew(): Crew {
    return {
      group: "g",
      subagents: [
        {
          session_id: "sess-1",
          session_name: "n",
          agent_name: "a",
          initial_query: "",
          status: "working",
          group: "g",
        },
      ],
    };
  }

  it("activity precedence: transient tool activity survives a later snapshot, pending-approval wins", () => {
    clearCrews();
    const msgId = "m-activity";
    const card = buildCrewCardForReplay(msgId, workingCrew());
    const act = card.querySelector('.crew-row[data-session-id="sess-1"] .crew-row-activity');

    // Initial snapshot seeds the generic placeholder.
    expect(act?.textContent).toBe("Working\u2026");

    // A live tool sets transient activity via the SSE setter.
    setSubagentActivity("sess-1", "Reading x");
    expect(act?.textContent).toBe("Reading x");

    // A later snapshot — still working, no status_msg — must NOT clobber it.
    updateCrew(msgId, workingCrew(), () => undefined);
    expect(act?.textContent).toBe("Reading x");

    // A pending approval wins over the transient activity.
    setSubagentPendingApproval("sess-1", true);
    expect(act?.textContent).toBe("\u26a0 tool approval needed");
  });
});
