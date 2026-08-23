// Tests for checkoutBranch action (optimistic anchor + empty body).

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../store.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  activeSession: undefined,
  getActive: undefined,
  getActiveId: undefined,
  get: () => ({ id: "c1", model: "m1" }),
  setThinking: vi.fn(),
  enqueuePrompt: vi.fn(),
  setModel: vi.fn(),
  setSupervisedMode: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: () => 0,
  setFrozen: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
}));
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { checkoutBranch } from "./git-branch.js";

const mockFetch = vi.fn();

beforeEach(() => {
  vi.useFakeTimers();
  resetActionFramework();
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
