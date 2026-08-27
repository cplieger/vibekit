// Tests for the one action that reads /api/forges.
//
// It was `git-badge.forges` in `actions/git-badge.ts`, named for the only
// consumer that polled it. Three modules fetched the endpoint by then, so the
// name claimed an ownership that was not real; forge-store.ts owns the poll now
// and every consumer reads through it. What is tested here is the request, the
// decode and the dedupe. The store's own behaviour is forge-store.test.ts.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

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
import { listForges } from "./forge-list.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const forgesResp = {
  forges: [{ id: "github:github.com", kind: "github", host: "github.com", connected: true }],
  kinds: ["github", "gitlab", "codeberg", "gitea"],
  oauth: { github: true },
};

describe("listForges", () => {
  it("has the expected action name", () => {
    expect(listForges.name).toBe("forges.list");
  });

  it("fetches /api/forges and does NOT fetch status-all", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url === "/api/forges") {
        return Promise.resolve(new Response(JSON.stringify(forgesResp), { status: 200 }));
      }
      return Promise.resolve(new Response("", { status: 404 }));
    });

    const result = await listForges.dispatch(undefined);
    expect(result?.forges).toHaveLength(1);
    expect(result?.oauth).toEqual({ github: true });
    const urls = mockFetch.mock.calls.map((c) => c[0]);
    expect(urls).toContain("/api/forges");
    // Git status is the other shared store's endpoint. A second fetch here is
    // the duplication these two stores exist to remove.
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
    const p1 = listForges.dispatch(undefined);
    const p2 = listForges.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(10);
    const [r1, r2] = await Promise.all([p1, p2]);
    vi.useRealTimers();

    expect(r1).toEqual(r2);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("resolves null on a failed fetch rather than throwing", async () => {
    mockFetch.mockResolvedValue(new Response("", { status: 500 }));
    await expect(listForges.dispatch(undefined)).resolves.toBeNull();
  });

  it("resolves null on a malformed payload rather than handing it on", async () => {
    // The decoder is the store's guard: `forges` missing entirely is what a
    // route returning an early empty body looks like, and a consumer that read
    // it as an empty list would render "no connected forges".
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ kinds: [] }), { status: 200 }));
    await expect(listForges.dispatch(undefined)).resolves.toBeNull();
  });
});
