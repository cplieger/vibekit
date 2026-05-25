// @vitest-environment happy-dom
// Per-action coverage gaps: stash, stashPop, unstage, loadSettings,
// sendPlan, loadDiff, openEdit, saveServer, searchRegistry, deleteLocal.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  apiGet: vi.fn(),
  apiPost: vi.fn(),
}));

vi.mock("../transport.js", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../transport.js")>();
  return { ...orig, send: vi.fn() };
});

vi.mock("../editor-types.js", () => ({
  routeForPath: (path: string) => ({ writeURL: `/api/file?path=${encodeURIComponent(path)}` }),
}));

vi.mock("../plan-actions.js", () => ({
  writePlanDraft: vi.fn(),
  runPlan: vi.fn(),
}));

vi.mock("../mcp-state.js", () => ({
  updateConfiguredEntry: vi.fn(),
  removeConfiguredEntry: vi.fn(),
  insertConfiguredEntry: vi.fn(),
}));

import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import * as toast from "../toast.js";
import { apiGet } from "../api-client.js";

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

// ===========================================================================
// git-changes: stash, stashPop, unstage
// ===========================================================================

describe("git.stash", () => {
  it("POSTs to /api/git/stash with repo", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { stash } = await import("./git-changes.js");
    await stash.dispatch({ repo: "myrepo" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/stash");
    expect(JSON.parse(opts.body as string)).toEqual({ repo: "myrepo" });
  });

  it("is not retryable (no server-side idempotency)", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const { stash } = await import("./git-changes.js");
    await stash.dispatch({ repo: "" });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Stash failed"), undefined);
  });
});

describe("git.stash_pop", () => {
  it("POSTs to /api/git/stash-pop with repo", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { stashPop } = await import("./git-changes.js");
    await stashPop.dispatch({ repo: "main" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/stash-pop");
    expect(JSON.parse(opts.body as string)).toEqual({ repo: "main" });
  });

  it("is not retryable", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "no stash" }), { status: 400 }));
    const { stashPop } = await import("./git-changes.js");
    await stashPop.dispatch({ repo: "" });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Stash pop failed"), undefined);
  });
});

describe("git.unstage", () => {
  it("POSTs to /api/git/unstage with repo and files", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { unstage } = await import("./git-changes.js");
    await unstage.dispatch({ repo: "r", files: ["a.ts"] });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/unstage");
    expect(JSON.parse(opts.body as string)).toEqual({ repo: "r", files: ["a.ts"] });
  });

  it("toasts with file name on single-file failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "err" }), { status: 500 }));
    const { unstage } = await import("./git-changes.js");
    await unstage.dispatch({ repo: "", files: ["index.ts"] });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("index.ts"), undefined);
  });

  it("toasts with count on multi-file failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "err" }), { status: 500 }));
    const { unstage } = await import("./git-changes.js");
    await unstage.dispatch({ repo: "", files: ["a.ts", "b.ts"] });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("2 files"), undefined);
  });
});

// ===========================================================================
// settings: loadSettings dedupe
// ===========================================================================

describe("settings.load", () => {
  it("GETs /api/settings", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ theme: "dark" }), { status: 200 }));
    const { loadSettings } = await import("./settings.js");
    const r = await loadSettings.dispatch(undefined);
    expect(r).toEqual({ theme: "dark" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/settings");
    expect(opts.method).toBe("GET");
  });

  it("dedupes concurrent dispatches", async () => {
    vi.useFakeTimers();
    mockFetch.mockImplementation(() =>
      new Promise((r) => setTimeout(() => r(new Response(JSON.stringify({}), { status: 200 })), 50)),
    );
    const { loadSettings } = await import("./settings.js");
    const p1 = loadSettings.dispatch(undefined);
    const p2 = loadSettings.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});

// ===========================================================================
// editor: sendPlan, loadDiff
// ===========================================================================

describe("editor.send_plan", () => {
  it("calls writePlanDraft then runPlan on success", async () => {
    const { writePlanDraft, runPlan } = await import("../plan-actions.js");
    vi.mocked(writePlanDraft).mockResolvedValue(true);
    vi.mocked(runPlan).mockResolvedValue(true);
    const { sendPlan } = await import("./editor.js");
    await sendPlan.dispatch({ chatID: "c1", content: "plan text" });
    expect(writePlanDraft).toHaveBeenCalledWith("c1", "plan text");
    expect(runPlan).toHaveBeenCalledWith("c1", "plan text");
  });

  it("errors when writePlanDraft returns false", async () => {
    const { writePlanDraft } = await import("../plan-actions.js");
    vi.mocked(writePlanDraft).mockResolvedValue(false);
    const { sendPlan } = await import("./editor.js");
    const r = await sendPlan.dispatch({ chatID: "c1", content: "x" });
    expect(r).toBeNull();
    expect(recentLog()[0]?.error?.code).toBe("draft_failed");
  });

  it("errors when runPlan returns false", async () => {
    const { writePlanDraft, runPlan } = await import("../plan-actions.js");
    vi.mocked(writePlanDraft).mockResolvedValue(true);
    vi.mocked(runPlan).mockResolvedValue(false);
    const { sendPlan } = await import("./editor.js");
    const r = await sendPlan.dispatch({ chatID: "c1", content: "x" });
    expect(r).toBeNull();
    expect(recentLog()[0]?.error?.code).toBe("run_plan_failed");
  });
});

describe("editor.load_diff", () => {
  it("fetches old and new content in parallel", async () => {
    vi.mocked(apiGet).mockImplementation(async (url: string) => {
      if (url.includes("/api/git/show")) return { content: "old" };
      if (url.includes("/api/file")) return { content: "new" };
      return null;
    });
    const { loadDiff } = await import("./editor.js");
    const r = await loadDiff.dispatch({ path: "src/a.ts", repo: "main", ref: "HEAD~1" });
    expect(r).toEqual({ oldContent: "old", newContent: "new", error: "" });
  });

  it("returns null on failure when both fetches return null", async () => {
    vi.mocked(apiGet).mockResolvedValue(null);
    const { loadDiff } = await import("./editor.js");
    const r = await loadDiff.dispatch({ path: "x.ts", repo: "", ref: "HEAD" });
    expect(r).toBeNull();
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
  });
});

// ===========================================================================
// mcp: openEdit, saveServer, searchRegistry
// ===========================================================================

describe("mcp.open_edit", () => {
  it("GETs /api/mcp/:id", async () => {
    const server = { id: "srv1", name: "test", transport: "stdio", enabled: true };
    mockFetch.mockResolvedValue(new Response(JSON.stringify(server), { status: 200 }));
    const { openEdit } = await import("./mcp.js");
    const r = await openEdit.dispatch("srv1");
    expect(r).toEqual(server);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/mcp/srv1");
    expect(opts.method).toBe("GET");
  });

  it("dedupes concurrent dispatches for same id", async () => {
    vi.useFakeTimers();
    mockFetch.mockImplementation(() =>
      new Promise((r) => setTimeout(() => r(new Response(JSON.stringify({}), { status: 200 })), 50)),
    );
    const { openEdit } = await import("./mcp.js");
    const p1 = openEdit.dispatch("srv1");
    const p2 = openEdit.dispatch("srv1");
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});

describe("mcp.save_server", () => {
  it("POSTs to /api/mcp for new server (empty id)", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ id: "new1" }), { status: 200 }));
    const { saveServer } = await import("./mcp.js");
    await saveServer.dispatch({ id: "", body: { name: "new-srv" } });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/mcp");
    expect(opts.method).toBe("POST");
  });

  it("PUTs to /api/mcp/:id for existing server", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ id: "srv1" }), { status: 200 }));
    const { saveServer } = await import("./mcp.js");
    await saveServer.dispatch({ id: "srv1", body: { name: "updated" } });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/mcp/srv1");
    expect(opts.method).toBe("PUT");
  });

  it("includes Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { saveServer } = await import("./mcp.js");
    await saveServer.dispatch({ id: "", body: {} });
    const headers = mockFetch.mock.calls[0]![1].headers as Record<string, string>;
    expect(headers["Idempotency-Key"]).toEqual(expect.any(String));
  });
});

describe("mcp.search_registry", () => {
  it("GETs /api/mcp/registry/search with query", async () => {
    const result = { servers: [{ name: "test-srv" }] };
    mockFetch.mockResolvedValue(new Response(JSON.stringify(result), { status: 200 }));
    const { searchRegistry } = await import("./mcp.js");
    const r = await searchRegistry.dispatch({ q: "test" });
    expect(r).toEqual(result);
    const [url] = mockFetch.mock.calls[0]!;
    expect(url).toContain("/api/mcp/registry/search");
    expect(url).toContain("q=test");
  });

  it("dedupes concurrent searches with same query", async () => {
    vi.useFakeTimers();
    mockFetch.mockImplementation(() =>
      new Promise((r) => setTimeout(() => r(new Response(JSON.stringify({ servers: [] }), { status: 200 })), 50)),
    );
    const { searchRegistry } = await import("./mcp.js");
    const p1 = searchRegistry.dispatch({ q: "abc" });
    const p2 = searchRegistry.dispatch({ q: "abc" });
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});

// ===========================================================================
// forge: deleteLocal
// ===========================================================================

describe("forge.delete_local", () => {
  it("POSTs to /api/git/remove with repo name", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ status: "ok" }), { status: 200 }));
    const { deleteLocal } = await import("./forge.js");
    await deleteLocal.dispatch({ repoName: "my-project" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/remove");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ repo: "my-project" });
  });

  it("is not retryable and suppresses error toast", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "not found" }), { status: 404 }));
    const { deleteLocal } = await import("./forge.js");
    await deleteLocal.dispatch({ repoName: "gone" });
    expect(toast.error).not.toHaveBeenCalled();
    expect(recentLog()[0]?.status).toBe("error");
  });
});
