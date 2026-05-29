// @vitest-environment happy-dom
// Tests for git-badge.ts: refreshGitBadge dedupe + parallel fetch.
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
import { refreshGitBadge } from "./git-badge.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const statusResp = {
  repos: [{ repo: "main", is_repo: true, branch: "main", ahead: 0, behind: 0, has_dirty: false }],
};
const forgesResp = { forges: [{ id: "gh:1", connected: true }] };

describe("refreshGitBadge", () => {
  it("has correct action name", () => {
    expect(refreshGitBadge.name).toBe("git-badge.refresh");
  });

  it("fetches both status-all and forges in parallel", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url === "/api/git/status-all") {
        return Promise.resolve(new Response(JSON.stringify(statusResp), { status: 200 }));
      }
      if (url === "/api/forges") {
        return Promise.resolve(new Response(JSON.stringify(forgesResp), { status: 200 }));
      }
      return Promise.resolve(new Response("", { status: 404 }));
    });

    const result = await refreshGitBadge.dispatch(undefined);
    expect(result).not.toBeNull();
    expect(result!.status).toEqual(statusResp);
    expect(result!.forges).toEqual(forgesResp);
    // Both endpoints called
    const urls = mockFetch.mock.calls.map((c) => c[0]);
    expect(urls).toContain("/api/git/status-all");
    expect(urls).toContain("/api/forges");
  });

  it("dedupes concurrent dispatches", async () => {
    mockFetch.mockImplementation(
      (url: string) =>
        new Promise((r) =>
          setTimeout(() => {
            if (url === "/api/git/status-all") {
              r(new Response(JSON.stringify(statusResp), { status: 200 }));
            } else {
              r(new Response(JSON.stringify(forgesResp), { status: 200 }));
            }
          }, 10),
        ),
    );

    vi.useFakeTimers();
    const p1 = refreshGitBadge.dispatch(undefined);
    const p2 = refreshGitBadge.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(10);
    const [r1, r2] = await Promise.all([p1, p2]);
    vi.useRealTimers();

    // Both resolve to the same result, but fetch only called twice (one per endpoint)
    expect(r1).toEqual(r2);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("returns null fields when sub-fetches fail", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url === "/api/git/status-all") {
        return Promise.resolve(new Response("", { status: 500 }));
      }
      return Promise.resolve(new Response(JSON.stringify(forgesResp), { status: 200 }));
    });

    const result = await refreshGitBadge.dispatch(undefined);
    // status fetch failed → null, forges succeeded
    expect(result).not.toBeNull();
    expect(result!.status).toBeNull();
    expect(result!.forges).toEqual(forgesResp);
  });
});
