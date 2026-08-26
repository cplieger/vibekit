// Tests for checkoutBranch action (optimistic anchor + empty body).

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

// The TOTAL store mock, plus the one reader this graph reaches. No case here
// touches the store at all — this file's subject is a git request — so the
// helper's defaults stand except for `get`.
//
// The hand-listed factory this replaced had drifted in BOTH directions: it still
// named `enqueuePrompt` and `setFrozen`, neither of which store.ts exports any
// more (the prompt queue is deleted and nothing freezes a chat), and its
// `activeSession: undefined` made an effect in this graph die at import with a
// swallowed `effect error: Cannot read properties of undefined (reading
// 'value')`. The helper carries a real signal there, so the effect runs.
vi.mock("../store.js", async () => ({
  ...(await import("../__test-helpers__/store-mock.js")).storeMock,
  get: () => ({ id: "c1", model: "m1" }),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds. The tab projection widened
  // this graph: `apiGetTyped` is how tabs-sync reads `GET /api/tabs`, and other
  // modules reached through it import `apiGet`. Nothing here calls either.
  apiGetTyped: vi.fn(),
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
