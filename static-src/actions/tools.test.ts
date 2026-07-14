// @vitest-environment happy-dom
// Tests for tools.ts action configuration: scope, retry, dedupe, idempotencyKey.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiGetTyped: vi.fn().mockResolvedValue(null),
  CancellableSlot: class {
    start() {
      return new AbortController().signal;
    }
    // eslint-disable-next-line @typescript-eslint/no-empty-function
    abort() {}
  },
}));

import { installTools, saveTools, runDiagnostics, loadTools, seedMcp } from "./tools.js";
import { enableTool, deleteTool, patchTool, getToolsStatus } from "./tools.js";
import { resetActionFramework, headerValue } from "./__test-helpers__/action-test-setup.js";
import { getActionLog as recentLog } from "./index.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
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
    expect(headerValue(mockFetch.mock.calls[0]![1], "idempotency-key")).toEqual(expect.any(String));
  });

  it("does NOT auto-retry on network error (C1: a retry re-POSTs and the server cancels + restarts the in-flight multi-minute install)", async () => {
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"));

    const result = await installTools.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(1000); // would cover the old backoff windows
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(result).toBeNull();
    expect(recentLog()[0]?.status).toBe("error");
  });
});

describe("tools.save", () => {
  it("PUTs to /api/tools with body", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const data = { mcp: { server1: { enabled: true } } };
    await saveTools.dispatch(data);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body as string)).toEqual(data);
  });
});

describe("tools.run_diagnostics", () => {
  it("dedupes concurrent dispatches", async () => {
    mockFetch.mockImplementation(
      () =>
        new Promise((r) =>
          setTimeout(() => {
            r(new Response(JSON.stringify({}), { status: 200 }));
          }, 50),
        ),
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
    mockFetch.mockImplementation(
      () =>
        new Promise((r) =>
          setTimeout(() => {
            r(new Response(JSON.stringify({}), { status: 200 }));
          }, 50),
        ),
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

describe("tools.enable", () => {
  it("POSTs to the section/name/enable path", async () => {
    mockFetch.mockResolvedValue(
      new Response(
        JSON.stringify({ output: "ok", enabled_chain: ["runtimes.node", "lsp.pyright"] }),
        {
          status: 200,
        },
      ),
    );
    await enableTool.dispatch({ section: "lsp", name: "pyright" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools/lsp/pyright/enable");
    expect(opts.method).toBe("POST");
  });

  it("URL-encodes scoped/odd names", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    await enableTool.dispatch({ section: "lsp", name: "kotlin-language-server" });
    const [url] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools/lsp/kotlin-language-server/enable");
  });
});

describe("tools.delete", () => {
  it("DELETEs without body when force is undefined", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ disabled: ["binary.gh"] }), { status: 200 }),
    );
    await deleteTool.dispatch({ section: "binary", name: "gh" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools/binary/gh");
    expect(opts.method).toBe("DELETE");
    expect(opts.body).toBeUndefined();
  });

  it("sends force:true in body when cascading", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ disabled: [] }), { status: 200 }));
    await deleteTool.dispatch({ section: "runtimes", name: "node", force: true });
    const body = JSON.parse(mockFetch.mock.calls[0]![1].body as string);
    expect(body.force).toBe(true);
  });
});

describe("tools.patch", () => {
  it("PATCHes auto_update", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ auto_update: false }), { status: 200 }),
    );
    await patchTool.dispatch({ section: "binary", name: "gh", auto_update: false });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools/binary/gh");
    expect(opts.method).toBe("PATCH");
    expect(JSON.parse(opts.body as string).auto_update).toBe(false);
  });
});

describe("tools.status", () => {
  it("GETs /api/tools/status and dedupes", async () => {
    mockFetch.mockImplementation(
      () =>
        new Promise((r) =>
          setTimeout(() => {
            r(new Response(JSON.stringify({ npx: false, gh: true }), { status: 200 }));
          }, 50),
        ),
    );
    const p1 = getToolsStatus.dispatch(undefined);
    const p2 = getToolsStatus.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(50);
    const [r1] = await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(r1).toEqual({ npx: false, gh: true });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools/status");
    expect(opts.method).toBe("GET");
  });
});
