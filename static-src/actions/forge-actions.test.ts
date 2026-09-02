// Tests for forge.ts: startDeviceFlow, signOut, cloneRepo, deleteLocal, connectPAT.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  errorWithAction: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  // Present-but-inert so real-ESM linking succeeds: the tab projection reaches
  // `apiGetTyped` for `GET /api/tabs` and other modules in this graph import
  // `apiGet`. Nothing here calls either.
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { resetActionFramework, headerValue } from "./__test-helpers__/action-test-setup.js";
import { getActionLog as recentLog } from "./index.js";
import * as toast from "../toast.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("forge.start_device_flow", () => {
  it("POSTs to /api/forges/oauth/github/start", async () => {
    const resp = {
      device_code: "abc",
      user_code: "1234",
      verification_uri: "https://github.com/login/device",
    };
    mockFetch.mockResolvedValue(new Response(JSON.stringify(resp), { status: 200 }));
    const { startDeviceFlow } = await import("./forge.js");
    const r = await startDeviceFlow.dispatch(undefined);
    expect(r).toEqual(resp);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/forges/oauth/github/start");
    expect(opts.method).toBe("POST");
  });

  it("dedupes concurrent dispatches", async () => {
    vi.useFakeTimers();
    mockFetch.mockImplementation(
      () =>
        new Promise((r) =>
          setTimeout(() => {
            r(new Response(JSON.stringify({}), { status: 200 }));
          }, 50),
        ),
    );
    const { startDeviceFlow } = await import("./forge.js");
    const p1 = startDeviceFlow.dispatch(undefined);
    const p2 = startDeviceFlow.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it("suppresses error toast on failure", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "rate limited" }), { status: 429 }),
    );
    const { startDeviceFlow } = await import("./forge.js");
    await startDeviceFlow.dispatch(undefined);
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("forge.sign_out", () => {
  it("DELETEs /api/forges/:id", async () => {
    mockFetch.mockResolvedValue(new Response(null, { status: 204 }));
    const { signOut } = await import("./forge.js");
    await signOut.dispatch({ forgeId: "github:user" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/forges/github%3Auser");
    expect(opts.method).toBe("DELETE");
  });

  it("is not retryable", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const { signOut } = await import("./forge.js");
    await signOut.dispatch({ forgeId: "gh:1" });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("sign out"), undefined);
    expect(recentLog()[0]?.status).toBe("error");
  });
});

describe("forge.clone_repo", () => {
  it("POSTs to /api/git/clone with url", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ output: "done" }), { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    const r = await cloneRepo.dispatch({ url: "https://github.com/user/repo.git" });
    expect(r).toEqual({ output: "done" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/clone");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ url: "https://github.com/user/repo.git" });
  });

  // No Idempotency-Key any more: the action does not retry (an interrupted
  // clone can leave a partial destination, so a retry reports a false
  // "already exists"), and the key had no other consumer.
  it("sends no Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    await cloneRepo.dispatch({ url: "https://x.com/r.git" });
    expect(headerValue(mockFetch.mock.calls[0]![1], "idempotency-key")).toBeUndefined();
  });

  it("suppresses error toast on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "exists" }), { status: 409 }));
    const { cloneRepo } = await import("./forge.js");
    await cloneRepo.dispatch({ url: "x" });
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("does NOT retry on network error", async () => {
    vi.useFakeTimers();
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response(JSON.stringify({}), { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    const p = cloneRepo.dispatch({ url: "x" });
    await vi.advanceTimersByTimeAsync(1000);
    const r = await p;
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(r).toBeNull();
    vi.useRealTimers();
  });
});

describe("forge.connect_pat", () => {
  it("POSTs to /api/forges/:id/login/pat with token", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ status: "ok" }), { status: 200 }));
    const { connectPAT } = await import("./forge.js");
    await connectPAT.dispatch({ kind: "github", host: "github.com", token: "ghp_abc" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/forges/github%3Agithub.com/login/pat");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ token: "ghp_abc" });
  });

  it("includes Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { connectPAT } = await import("./forge.js");
    await connectPAT.dispatch({ kind: "gitlab", host: "gitlab.com", token: "tok" });
    expect(headerValue(mockFetch.mock.calls[0]![1], "idempotency-key")).toEqual(expect.any(String));
  });

  it("suppresses error toast on failure", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "invalid" }), { status: 401 }),
    );
    const { connectPAT } = await import("./forge.js");
    await connectPAT.dispatch({ kind: "github", host: "github.com", token: "bad" });
    expect(toast.error).not.toHaveBeenCalled();
  });
});
