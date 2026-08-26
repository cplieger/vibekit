// Tests for tools.ts action configuration: scope, retry, dedupe,
// idempotencyKey, and the v2 wire shapes (202 + job envelopes, the
// delete 409 pass-through, the ensure create->install fallback).
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

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

import {
  loadTools,
  createTool,
  installTool,
  updateTools,
  patchTool,
  deleteTool,
  ensureTool,
  searchTools,
  getToolsJobs,
  cancelToolJob,
  runDiagnostics,
  seedMcp,
  getToolsStatus,
} from "./tools.js";
import { resetActionFramework, headerValue } from "./__test-helpers__/action-test-setup.js";

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

const jobBody = JSON.stringify({ job: { id: "tj-1", kind: "install", state: "queued" } });

describe("tools.load", () => {
  it("GETs /api/tools and dedupes", async () => {
    mockFetch.mockImplementation(
      () =>
        new Promise((r) =>
          setTimeout(() => {
            r(new Response(JSON.stringify({ tools: [], system: [] }), { status: 200 }));
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

describe("tools.create", () => {
  it("POSTs to /api/tools and returns the 202 job envelope", async () => {
    mockFetch.mockResolvedValue(new Response(jobBody, { status: 202 }));
    expect(createTool.name).toBe("tools.create");
    const d = await createTool.dispatch({ name: "ripgrep" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string).name).toBe("ripgrep");
    expect(d?.job?.id).toBe("tj-1");
  });

  it("includes Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response(jobBody, { status: 202 }));
    await createTool.dispatch({ name: "x" });
    expect(headerValue(mockFetch.mock.calls[0]![1], "idempotency-key")).toEqual(expect.any(String));
  });
});

describe("tools.install", () => {
  it("POSTs to the per-tool install path with URL encoding", async () => {
    mockFetch.mockResolvedValue(new Response(jobBody, { status: 202 }));
    await installTool.dispatch({ name: "@scope/pkg" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools/%40scope%2Fpkg/install");
    expect(opts.method).toBe("POST");
  });
});

describe("tools.update", () => {
  it("POSTs to /api/tools/update with empty body by default", async () => {
    mockFetch.mockResolvedValue(new Response(jobBody, { status: 202 }));
    await updateTools.dispatch(undefined);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools/update");
    expect(JSON.parse(opts.body as string)).toEqual({});
  });

  it("passes explicit names through", async () => {
    mockFetch.mockResolvedValue(new Response(jobBody, { status: 202 }));
    await updateTools.dispatch({ names: ["gh"] });
    expect(JSON.parse(mockFetch.mock.calls[0]![1].body as string).names).toEqual(["gh"]);
  });
});

describe("tools.patch", () => {
  it("PATCHes pin without the name in the body", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    await patchTool.dispatch({ name: "gh", pin: true });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools/gh");
    expect(opts.method).toBe("PATCH");
    expect(JSON.parse(opts.body as string)).toEqual({ pin: true });
  });
});

describe("tools.delete", () => {
  it("DELETEs and returns the body on 200", async () => {
    mockFetch.mockResolvedValue(new Response(jobBody, { status: 202 }));
    const d = await deleteTool.dispatch({ name: "gh" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools/gh");
    expect(opts.method).toBe("DELETE");
    expect(d?.job?.id).toBe("tj-1");
  });

  it("returns the 409 has_dependents envelope instead of failing", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ code: "has_dependents", dependents: ["jdtls"] }), {
        status: 409,
      }),
    );
    const d = await deleteTool.dispatch({ name: "java" });
    expect(d?.code).toBe("has_dependents");
    expect(d?.dependents).toEqual(["jdtls"]);
  });

  it("sends force as a query param when cascading", async () => {
    mockFetch.mockResolvedValue(new Response(jobBody, { status: 202 }));
    await deleteTool.dispatch({ name: "java", force: true });
    expect(mockFetch.mock.calls[0]![0]).toBe("/api/tools/java?force=1");
  });
});

describe("tools.ensure", () => {
  it("falls back to per-tool install when create says the tool exists", async () => {
    mockFetch
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: 'tool "gh" already exists' }), { status: 400 }),
      )
      .mockResolvedValueOnce(new Response(jobBody, { status: 202 }));
    const d = await ensureTool.dispatch({ name: "gh" });
    expect(mockFetch).toHaveBeenCalledTimes(2);
    expect(mockFetch.mock.calls[0]![0]).toBe("/api/tools");
    expect(mockFetch.mock.calls[1]![0]).toBe("/api/tools/gh/install");
    expect(d?.job?.id).toBe("tj-1");
  });

  it("returns the job directly when create succeeds", async () => {
    mockFetch.mockResolvedValue(new Response(jobBody, { status: 202 }));
    const d = await ensureTool.dispatch({ name: "gh" });
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(d?.job?.id).toBe("tj-1");
  });
});

describe("tools.search", () => {
  it("GETs with the query encoded and dedupes", async () => {
    mockFetch.mockImplementation(
      () =>
        new Promise((r) =>
          setTimeout(() => {
            r(new Response(JSON.stringify({ results: [] }), { status: 200 }));
          }, 50),
        ),
    );
    const p1 = searchTools.dispatch({ q: "rip grep" });
    const p2 = searchTools.dispatch({ q: "rip grep" });
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(mockFetch.mock.calls[0]![0]).toBe("/api/tools/search?q=rip%20grep");
  });
});

describe("tools.jobs", () => {
  it("GETs /api/tools/jobs", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ recent: [] }), { status: 200 }));
    await getToolsJobs.dispatch(undefined);
    expect(mockFetch.mock.calls[0]![0]).toBe("/api/tools/jobs");
  });
});

describe("tools.cancel_job", () => {
  it("POSTs to the job cancel path", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    await cancelToolJob.dispatch({ id: "tj-9" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/tools/jobs/tj-9/cancel");
    expect(opts.method).toBe("POST");
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
      return new Response(jobBody, { status: 202 });
    });

    const p1 = createTool.dispatch({ name: "x" });
    const p2 = seedMcp.dispatch({ name: "x" });
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    // Second call starts after first finishes (serialized via scope "tools")
    expect(log[1]! - log[0]!).toBeGreaterThanOrEqual(50);
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
