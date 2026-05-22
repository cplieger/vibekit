// @vitest-environment happy-dom
import { describe, it, expect, vi } from "vitest";

// Mock heavy DOM-dependent transitive imports.
vi.mock("./modals.js", () => ({ closeModal: vi.fn() }));
vi.mock("./upload.js", () => ({ uploadFiles: vi.fn() }));
vi.mock("./chat.js", () => ({ attachPathToActiveChat: vi.fn() }));
vi.mock("./scroll.js", () => ({
  scrollController: { autoScrollIfAnchored: vi.fn() },
  default: { autoScrollIfAnchored: vi.fn() },
}));
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

import { filterEntries } from "./files-picker.js";
import type { FileEntry } from "./files-picker.js";

describe("filterEntries", () => {
  const entries: FileEntry[] = [
    { name: "README.md", isDir: false },
    { name: "package.json", isDir: false },
    { name: "src", isDir: true },
    { name: "Dockerfile", isDir: false },
    { name: "docs", isDir: true },
  ];

  it("empty query returns all entries", () => {
    expect(filterEntries(entries, "")).toEqual(entries);
  });

  it("exact match returns single result", () => {
    const result = filterEntries(entries, "Dockerfile");
    expect(result).toHaveLength(1);
    expect(result[0]!.name).toBe("Dockerfile");
  });

  it("case-insensitive partial match", () => {
    const result = filterEntries(entries, "doc");
    expect(result).toHaveLength(2);
    expect(result.map((e) => e.name).sort()).toEqual(["Dockerfile", "docs"]);
  });

  it("no match returns empty array", () => {
    expect(filterEntries(entries, "zzz")).toEqual([]);
  });

  it("directory entries included when matching", () => {
    const result = filterEntries(entries, "src");
    expect(result).toHaveLength(1);
    expect(result[0]!.isDir).toBe(true);
  });
});
