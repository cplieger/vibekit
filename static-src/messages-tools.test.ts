// @vitest-environment happy-dom
// Unit test for applyOutputUpdate: kiro-cli sends CUMULATIVE tool output on
// every tool_call_update, so the card's <pre> must be REPLACED, not appended.
import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock messages-tools.ts's heavy DOM/store deps so the import resolves without
// pulling the store, subagent modals, tool-card, etc. ansi + reactive stay
// real (applyOutputUpdate uses ansiToHtml + el for actual rendering).
vi.mock("./store-signals.js", () => ({
  ensureToolCallSig: vi.fn(() => ({ value: undefined })),
  clearToolCallSig: vi.fn(),
}));
vi.mock("./tool-group.js", () => ({
  maybeCollapseGroup: vi.fn(),
  formatDuration: vi.fn(() => ""),
  untrackInProgress: vi.fn(),
}));
vi.mock("./tool-schema.js", () => ({ isToolDone: vi.fn(() => false) }));
vi.mock("./tool-card.js", () => ({
  buildToolCard: vi.fn(() => document.createElement("div")),
  insertDiffPreview: vi.fn(),
}));

import { applyOutputUpdate } from "./messages-tools.js";

function complexCard(): HTMLDivElement {
  const card = document.createElement("div");
  card.className = "tool-call";
  const box = document.createElement("div");
  box.className = "tool-output-box";
  card.appendChild(box);
  return card;
}

function simpleCard(): HTMLDivElement {
  const card = document.createElement("div");
  card.className = "tool-call";
  const out = document.createElement("div");
  out.className = "tool-output";
  card.appendChild(out);
  return card;
}

describe("applyOutputUpdate (cumulative output → replace, not append)", () => {
  beforeEach(() => {
    document.body.replaceChildren();
  });

  it("replaces the .tool-output-box <pre> with the latest cumulative output", () => {
    const card = complexCard();
    applyOutputUpdate(card, "line one\n");
    applyOutputUpdate(card, "line one\nline two\n");
    const pre = card.querySelector(".tool-output-box pre");
    expect(pre).not.toBeNull();
    // Not appended ("line one\nline one\nline two\n") — replaced.
    expect(pre?.textContent).toBe("line one\nline two\n");
    // Exactly one <pre>, not one per update.
    expect(card.querySelectorAll(".tool-output-box pre").length).toBe(1);
  });

  it("replaces the .tool-output <pre> (simple/medium tier) with the latest output", () => {
    const card = simpleCard();
    applyOutputUpdate(card, "A");
    applyOutputUpdate(card, "AB");
    applyOutputUpdate(card, "ABC");
    const pre = card.querySelector(".tool-output pre");
    expect(pre?.textContent).toBe("ABC");
    expect(card.querySelectorAll(".tool-output pre").length).toBe(1);
  });
});
