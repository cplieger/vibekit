// @vitest-environment happy-dom
// Tests for tools.ts action configuration: scope, retry, dedupe, idempotencyKey.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

import { installTools, saveTools, runDiagnostics, loadTools, seedMcp } from "./tools.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.useFakeTimers();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("tools.install", () => {
  it("has correct name and POSTs to /api/tools/install", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ output: "ok" }), { status: 200 }));
    expect(installTools.name).toBe("tools.install");
    await installTools.dispatch(undefined);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools/install");
    expect(opts.method).toBe("POST");
  });

  it("includes Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    await installTools.dispatch(undefined);
    const headers = mockFetch.mock.calls[0]![1].headers as Record<string, string>;
    expect(headers["Idempotency-Key"]).toEqual(expect.any(String));
  });

  it("retries on network error", async () => {
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response(JSON.stringify({}), { status: 200 }));

    const p = installTools.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(mockFetch).toHaveBeenCalledTimes(3);
    expect(recentLog()[0]?.status).toBe("success");
  });
});

describe("tools.save", () => {
  it("PUTs to /api/tools with body", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const data = { mcp: { server1: { enabled: true } } };
    await saveTools.dispatch(data as Record<string, Record<string, Record<string, unknown>>>);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body as string)).toEqual(data);
  });
});

describe("tools.run_diagnostics", () => {
  it("dedupes concurrent dispatches", async () => {
    mockFetch.mockImplementation(() =>
      new Promise((r) => setTimeout(() => r(new Response(JSON.stringify({}), { status: 200 })), 50)),
    );
    const p1 = runDiagnostics.dispatch(undefined);
    const p2 = runDiagnostics.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });
});

describe("tools.load", () => {
  it("GETs /api/tools and dedupes", async () => {
    mockFetch.mockImplementation(() =>
      new Promise((r) => setTimeout(() => r(new Response(JSON.stringify({}), { status: 200 })), 50)),
    );
    const p1 = loadTools.dispatch(undefined);
    const p2 = loadTools.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools");
    expect(opts.method).toBe("GET");
  });
});

describe("tools.seed_mcp", () => {
  it("POSTs to /api/mcp with correct body shape", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    await seedMcp.dispatch({ name: "my-server", install: "npm install" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/mcp");
    expect(opts.method).toBe("POST");
    const body = JSON.parse(opts.body as string);
    expect(body.name).toBe("my-server");
    expect(body.transport).toBe("stdio");
    expect(body.install).toBe("npm install");
  });

  it("omits install field when not provided", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    await seedMcp.dispatch({ name: "srv" });
    const body = JSON.parse(mockFetch.mock.calls[0]![1].body as string);
    expect(body).not.toHaveProperty("install");
  });

  it("serializes with other tools actions via shared scope", async () => {
    const log: number[] = [];
    mockFetch.mockImplementation(async () => {
      log.push(Date.now());
      await new Promise<void>((r) => setTimeout(r, 50));
      return new Response(JSON.stringify({}), { status: 200 });
    });

    const p1 = installTools.dispatch(undefined);
    const p2 = seedMcp.dispatch({ name: "x" });
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    // Second call starts after first finishes (serialized via scope "tools")
    expect(log[1]! - log[0]!).toBeGreaterThanOrEqual(50);
  });
});
