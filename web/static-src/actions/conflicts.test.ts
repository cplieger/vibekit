// @vitest-environment happy-dom
// Tests for actions/conflicts.ts: openConflictDiff, loadConflicts.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

vi.mock("../editor-openers.js", () => ({
  openFileDiff: vi.fn(),
}));

import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import * as toast from "../toast.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("conflicts.open_diff", () => {
  it("fetches both blobs and opens diff editor", async () => {
    mockFetch
      .mockResolvedValueOnce(new Response("old content", { status: 200 }))
      .mockResolvedValueOnce(new Response("new content", { status: 200 }));

    const { openConflictDiff } = await import("./conflicts.js");
    const { openFileDiff } = await import("../editor-openers.js");

    await openConflictDiff.dispatch({
      chatID: "c1",
      path: "src/main.ts",
      expectedSha: "abc123",
      actualSha: "def456",
      otherChat: "c2",
    });

    expect(mockFetch).toHaveBeenCalledTimes(2);
    expect(mockFetch.mock.calls[0]![0]).toContain("/api/checkpoints/c1/blob/abc123");
    expect(mockFetch.mock.calls[1]![0]).toContain("/api/checkpoints/c1/blob/def456");
    expect(openFileDiff).toHaveBeenCalledWith(
      "src/main.ts",
      "old content",
      "new content",
      expect.objectContaining({ oldLabel: "chat c2 left", newLabel: "this chat saw" }),
    );
  });

  it("toasts error when blob fetch fails", async () => {
    mockFetch.mockResolvedValue(new Response("", { status: 500 }));
    const { openConflictDiff } = await import("./conflicts.js");
    const r = await openConflictDiff.dispatch({
      chatID: "c1",
      path: "x.ts",
      expectedSha: "a",
      actualSha: "b",
      otherChat: "c2",
    });
    expect(r).toBeNull();
    expect(toast.error).toHaveBeenCalledWith(
      expect.stringContaining("Could not load file content"),
      undefined,
    );
  });

  it("handles empty sha by returning empty string (no fetch)", async () => {
    // expectedSha is empty → fetchBlob returns "" without fetching
    mockFetch.mockResolvedValueOnce(new Response("actual", { status: 200 }));
    const { openConflictDiff } = await import("./conflicts.js");
    const { openFileDiff } = await import("../editor-openers.js");

    await openConflictDiff.dispatch({
      chatID: "c1",
      path: "new-file.ts",
      expectedSha: "",
      actualSha: "def456",
      otherChat: "c2",
    });

    // Only one fetch (for actualSha)
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(vi.mocked(openFileDiff)).toHaveBeenCalledWith(
      "new-file.ts",
      "",
      "actual",
      expect.anything(),
    );
  });
});

describe("conflicts.load", () => {
  it("GETs conflicts for a chat (error suppressed)", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ conflicts: [] }), { status: 200 }));
    const { loadConflicts } = await import("./conflicts.js");
    const r = await loadConflicts.dispatch("chat-42");
    expect(r).toEqual({ conflicts: [] });
    const [url] = mockFetch.mock.calls[0]!;
    expect(url).toContain("/api/checkpoints/chat-42/conflicts");
  });

  it("does not toast on failure (error: false)", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "not found" }), { status: 404 }),
    );
    const { loadConflicts } = await import("./conflicts.js");
    await loadConflicts.dispatch("c1");
    expect(toast.error).not.toHaveBeenCalled();
  });
});
