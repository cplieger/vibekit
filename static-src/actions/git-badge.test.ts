// Tests for the git badge's FORGES action.
//
// This file used to test a combined action that fetched /api/git/status-all and
// /api/forges together. The status half moved to git-status-store.ts (one shared
// poll, so per-path status has a reader outside the git view), so what remains
// here is the forges fetch and its dedupe. The moved behaviour is covered by
// git-status-store.test.ts.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
}));
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { refreshForges } from "./git-badge.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const forgesResp = { forges: [{ id: "gh:1", connected: true }] };

describe("refreshForges", () => {
  it("has the expected action name", () => {
    expect(refreshForges.name).toBe("git-badge.forges");
  });

  it("fetches /api/forges and does NOT fetch status-all", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url === "/api/forges") {
        return Promise.resolve(new Response(JSON.stringify(forgesResp), { status: 200 }));
      }
      return Promise.resolve(new Response("", { status: 404 }));
    });

    const result = await refreshForges.dispatch(undefined);
    expect(result).toEqual(forgesResp);
    const urls = mockFetch.mock.calls.map((c) => c[0]);
    expect(urls).toContain("/api/forges");
    // The badge must not re-fetch git status: that is the shared store's job,
    // and a second fetch here is the duplication this split removed.
    expect(urls).not.toContain("/api/git/status-all");
  });

  it("dedupes concurrent dispatches", async () => {
    mockFetch.mockImplementation(
      () =>
        new Promise((r) =>
          setTimeout(() => r(new Response(JSON.stringify(forgesResp), { status: 200 })), 10),
        ),
    );

    vi.useFakeTimers();
    const p1 = refreshForges.dispatch(undefined);
    const p2 = refreshForges.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(10);
    const [r1, r2] = await Promise.all([p1, p2]);
    vi.useRealTimers();

    expect(r1).toEqual(r2);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("resolves null on a failed fetch rather than throwing", async () => {
    mockFetch.mockResolvedValue(new Response("", { status: 500 }));
    await expect(refreshForges.dispatch(undefined)).resolves.toBeNull();
  });
});
