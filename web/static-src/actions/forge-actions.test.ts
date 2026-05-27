// @vitest-environment happy-dom
// Tests for forge.ts: startDeviceFlow, signOut, cloneRepo, deleteLocal, connectPAT.
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

import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
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
          setTimeout(() => r(new Response(JSON.stringify({}), { status: 200 })), 50),
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
    mockFetch.mockResolvedValue(new Response("", { status: 204 }));
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

  it("includes Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    await cloneRepo.dispatch({ url: "https://x.com/r.git" });
    const headers = mockFetch.mock.calls[0]![1].headers as Record<string, string>;
    expect(headers["Idempotency-Key"]).toEqual(expect.any(String));
  });

  it("suppresses error toast on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "exists" }), { status: 409 }));
    const { cloneRepo } = await import("./forge.js");
    await cloneRepo.dispatch({ url: "x" });
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("retries on network error", async () => {
    vi.useFakeTimers();
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response(JSON.stringify({}), { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    const p = cloneRepo.dispatch({ url: "x" });
    await vi.advanceTimersByTimeAsync(300);
    await p;
    expect(mockFetch).toHaveBeenCalledTimes(2);
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
    const headers = mockFetch.mock.calls[0]![1].headers as Record<string, string>;
    expect(headers["Idempotency-Key"]).toEqual(expect.any(String));
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
