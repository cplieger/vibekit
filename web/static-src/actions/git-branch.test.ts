// @vitest-environment happy-dom
// Tests for checkoutBranch action (optimistic anchor + empty body).

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../store.js", () => ({
  get: () => ({ id: "c1", model: "m1" }),
  setThinking: vi.fn(),
  enqueuePrompt: vi.fn(),
  setModel: vi.fn(),
  setSupervisedMode: vi.fn(),
  setAutoApproveCrew: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: () => 0,
  setFrozen: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

import { checkoutBranch } from "./git-branch.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";

const mockFetch = vi.fn();

beforeEach(() => {
  vi.useFakeTimers();
  resetDefine();
  resetRegistry();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.useRealTimers();
});

describe("checkoutBranch", () => {
  it("dispatches successfully (UI is caller's responsibility)", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await checkoutBranch.dispatch({ repo: "", branch: "feature", create: false });
    expect(mockFetch).toHaveBeenCalled();
    // The action no longer mutates DOM; verify the request was sent.
    const call = mockFetch.mock.calls[0]!;
    const url = call[0] as string;
    expect(url).toContain("/api/git/checkout");
  });

  it("returns null on HTTP error", async () => {
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"));
    const p = checkoutBranch.dispatch({ repo: "", branch: "feature", create: false });
    await vi.advanceTimersByTimeAsync(300); // first retry
    await vi.advanceTimersByTimeAsync(600); // second retry
    const result = await p;
    expect(result).toBeNull();
  });

  it("handles empty 200 body gracefully", async () => {
    mockFetch.mockResolvedValue(new Response("", { status: 200 }));
    const result = await checkoutBranch.dispatch({ repo: "", branch: "dev", create: true });
    expect(result).toBeUndefined();
  });
});
