// @vitest-environment happy-dom
// Per-action coverage gaps batch 2: git.pull, git.commit, git.discard,
// forge.signOut, editor.saveFile, editor.resolvePendingPartial,
// editor.fetchAgentLines, settings.saveSteering, settings.logout,
// git.refreshPRs.
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

vi.mock("../git-prs-tab.js", () => ({
  refreshPRs: vi.fn(),
}));

import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import * as toast from "../toast.js";
import { send as transportSend } from "../transport.js";

const mockFetch = vi.fn();
const mockSend = vi.mocked(transportSend);

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

// ===========================================================================
// git.pull — retryable, scoped, success toast
// ===========================================================================

describe("git.pull", () => {
  it("retries on network error and toasts success with repo name", async () => {
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    const { pull } = await import("./git-changes.js");
    const p = pull.dispatch({ repo: "myrepo" });
    await vi.advanceTimersByTimeAsync(300);
    await p;
    expect(mockFetch).toHaveBeenCalledTimes(2);
    expect(toast.success).toHaveBeenCalledWith("Pulled myrepo");
    expect(recentLog()[0]?.status).toBe("success");
  });

  it("serializes via scope (same repo)", async () => {
    const log: number[] = [];
    mockFetch.mockImplementation(async () => {
      log.push(Date.now());
      await new Promise<void>((r) => setTimeout(r, 50));
      return new Response("{}", { status: 200 });
    });
    const { pull, push } = await import("./git-changes.js");
    const p1 = pull.dispatch({ repo: "r1" });
    const p2 = push.dispatch({ repo: "r1" });
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(log[1]! - log[0]!).toBeGreaterThanOrEqual(50);
  });
});

// ===========================================================================
// git.commit — not retryable, scoped, custom error toast
// ===========================================================================

describe("git.commit", () => {
  it("POSTs to /api/git/commit with repo and message", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { commit } = await import("./git-changes.js");
    await commit.dispatch({ repo: "main", message: "feat: add feature" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/commit");
    expect(JSON.parse(opts.body as string)).toEqual({ repo: "main", message: "feat: add feature" });
    expect(toast.success).toHaveBeenCalledWith("Committed");
  });

  it("is not retryable (no retry on failure)", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "nothing to commit" }), { status: 500 }));
    const { commit } = await import("./git-changes.js");
    await commit.dispatch({ repo: "", message: "fix: bug" });
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Commit failed"), undefined);
  });

  it("truncates long commit message in error toast", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "err" }), { status: 500 }));
    const { commit } = await import("./git-changes.js");
    const longMsg = "a".repeat(50) + "\nsecond line";
    await commit.dispatch({ repo: "", message: longMsg });
    const errCall = vi.mocked(toast.error).mock.calls[0]![0] as string;
    expect(errCall).toContain("\u2026");
    expect(errCall.length).toBeLessThan(100);
  });
});

// ===========================================================================
// git.discard — not retryable, scoped, custom error toast
// ===========================================================================

describe("git.discard", () => {
  it("POSTs to /api/git/discard with repo and files", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { discard } = await import("./git-changes.js");
    await discard.dispatch({ repo: "main", files: ["src/a.ts"] });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/discard");
    expect(JSON.parse(opts.body as string)).toEqual({ repo: "main", files: ["src/a.ts"] });
  });

  it("is not retryable (destructive operation)", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const { discard } = await import("./git-changes.js");
    await discard.dispatch({ repo: "", files: ["x.ts"] });
    expect(mockFetch).toHaveBeenCalledTimes(1);
    // Error toast includes file name for single file
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("x.ts"), undefined);
  });

  it("error toast shows count for multi-file discard", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const { discard } = await import("./git-changes.js");
    await discard.dispatch({ repo: "", files: ["a.ts", "b.ts", "c.ts"] });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("3 files"), undefined);
  });
});

// ===========================================================================
// forge.signOut — not retryable, DELETE
// ===========================================================================

describe("forge.sign_out", () => {
  it("DELETEs /api/forges/:id", async () => {
    mockFetch.mockResolvedValue(new Response("", { status: 204 }));
    const { signOut } = await import("./forge.js");
    await signOut.dispatch({ forgeId: "github:github.com" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/forges/github%3Agithub.com");
    expect(opts.method).toBe("DELETE");
    expect(recentLog()[0]?.status).toBe("success");
  });

  it("is not retryable and toasts error on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "not found" }), { status: 404 }));
    const { signOut } = await import("./forge.js");
    await signOut.dispatch({ forgeId: "gh1" });
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Couldn't sign out"), undefined);
  });
});

// ===========================================================================
// editor.saveFile — scoped per path, retryable (manual only)
// ===========================================================================

describe("editor.save_file", () => {
  it("PUTs to the file write URL with content", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { saveFile } = await import("./editor.js");
    await saveFile.dispatch({ path: "src/main.ts", content: "const x = 1;" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toContain("/api/file");
    expect(url).toContain("src%2Fmain.ts");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body as string)).toEqual({ content: "const x = 1;" });
  });

  it("serializes saves to the same file path", async () => {
    const log: number[] = [];
    mockFetch.mockImplementation(async () => {
      log.push(Date.now());
      await new Promise<void>((r) => setTimeout(r, 50));
      return new Response(JSON.stringify({ ok: true }), { status: 200 });
    });
    const { saveFile } = await import("./editor.js");
    const p1 = saveFile.dispatch({ path: "a.ts", content: "v1" });
    const p2 = saveFile.dispatch({ path: "a.ts", content: "v2" });
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(log[1]! - log[0]!).toBeGreaterThanOrEqual(50);
  });

  it("suppresses error toast (inline error surface)", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "disk full" }), { status: 500 }));
    const { saveFile } = await import("./editor.js");
    await saveFile.dispatch({ path: "x.ts", content: "" });
    expect(toast.error).not.toHaveBeenCalled();
  });
});

// ===========================================================================
// editor.resolvePendingPartial — transport, scoped, retryable
// ===========================================================================

describe("editor.resolve_partial", () => {
  it("sends resolve_pending_change_partial via transport", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { resolvePendingPartial } = await import("./editor.js");
    await resolvePendingPartial.dispatch({ chatID: "c1", toolCallID: "tc1", mergedText: "merged" });
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "resolve_pending_change_partial",
        chat_id: "c1",
        payload: expect.objectContaining({ tool_call_id: "tc1", merged_text: "merged" }),
      }),
      expect.anything(),
    );
  });

  it("retries on network error", async () => {
    mockSend
      .mockResolvedValueOnce({ ok: false, status: 0, error: "net", code: "network" })
      .mockResolvedValueOnce({ ok: true, status: 200 });
    const { resolvePendingPartial } = await import("./editor.js");
    const p = resolvePendingPartial.dispatch({ chatID: "c1", toolCallID: "tc1", mergedText: "m" });
    await vi.advanceTimersByTimeAsync(300);
    await p;
    expect(mockSend).toHaveBeenCalledTimes(2);
    expect(recentLog()[0]?.status).toBe("success");
  });
});

// ===========================================================================
// editor.fetchAgentLines — deduped, retryable
// ===========================================================================

describe("editor.fetch_agent_lines", () => {
  it("GETs /api/file-changes with chatID and path", async () => {
    const result = { changes: [{ start_line: 1, end_line: 5 }] };
    mockFetch.mockResolvedValue(new Response(JSON.stringify(result), { status: 200 }));
    const { fetchAgentLines } = await import("./editor.js");
    const r = await fetchAgentLines.dispatch({ chatID: "c1", path: "src/a.ts" });
    expect(r).toEqual(result);
    const [url] = mockFetch.mock.calls[0]!;
    expect(url).toContain("/api/file-changes");
    expect(url).toContain("chat_id=c1");
    expect(url).toContain("path=src%2Fa.ts");
  });

  it("dedupes concurrent dispatches for same chatID+path", async () => {
    mockFetch.mockImplementation(() =>
      new Promise((r) => setTimeout(() => r(new Response(JSON.stringify({ changes: [] }), { status: 200 })), 50)),
    );
    const { fetchAgentLines } = await import("./editor.js");
    const p1 = fetchAgentLines.dispatch({ chatID: "c1", path: "a.ts" });
    const p2 = fetchAgentLines.dispatch({ chatID: "c1", path: "a.ts" });
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("does not dedupe different paths", async () => {
    mockFetch.mockImplementation(() =>
      new Promise((r) => setTimeout(() => r(new Response(JSON.stringify({ changes: [] }), { status: 200 })), 50)),
    );
    const { fetchAgentLines } = await import("./editor.js");
    const p1 = fetchAgentLines.dispatch({ chatID: "c1", path: "a.ts" });
    const p2 = fetchAgentLines.dispatch({ chatID: "c1", path: "b.ts" });
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });
});

// ===========================================================================
// settings.save_steering — scoped, retryable
// ===========================================================================

describe("settings.save_steering", () => {
  it("PUTs to /api/steering with content", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { saveSteering } = await import("./settings.js");
    await saveSteering.dispatch({ content: "# My steering" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/steering");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body as string)).toEqual({ content: "# My steering" });
  });

  it("retries on network error", async () => {
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    const { saveSteering } = await import("./settings.js");
    const p = saveSteering.dispatch({ content: "x" });
    await vi.advanceTimersByTimeAsync(300);
    await p;
    expect(mockFetch).toHaveBeenCalledTimes(2);
    expect(recentLog()[0]?.status).toBe("success");
  });

  it("serializes via settings scope with other settings actions", async () => {
    const log: number[] = [];
    mockFetch.mockImplementation(async () => {
      log.push(Date.now());
      await new Promise<void>((r) => setTimeout(r, 50));
      return new Response("{}", { status: 200 });
    });
    const { saveSteering } = await import("./settings.js");
    const p1 = saveSteering.dispatch({ content: "a" });
    const p2 = saveSteering.dispatch({ content: "b" });
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    // Second starts after first finishes (serialized via scope "settings")
    expect(log[1]! - log[0]!).toBeGreaterThanOrEqual(50);
  });
});

// ===========================================================================
// settings.logout — optimistic + rollback with DOM
// ===========================================================================

describe("settings.logout", () => {
  it("POSTs to /api/logout and clears email optimistically", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const emailEl = document.createElement("span");
    emailEl.textContent = "user@example.com";
    const stAuthEl = document.createElement("span");
    stAuthEl.textContent = "signed in";
    const { logout } = await import("./settings.js");
    await logout.dispatch({ emailEl, stAuthEl });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/logout");
    expect(opts.method).toBe("POST");
    // Optimistic: cleared immediately
    expect(emailEl.textContent).toBe("");
    expect(stAuthEl.textContent).toBe("not signed in");
  });

  it("rolls back on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const emailEl = document.createElement("span");
    emailEl.textContent = "user@example.com";
    const stAuthEl = document.createElement("span");
    stAuthEl.textContent = "signed in";
    const { logout } = await import("./settings.js");
    await logout.dispatch({ emailEl, stAuthEl });
    // Rollback restores email and sets auth status based on non-empty email
    expect(emailEl.textContent).toBe("user@example.com");
    expect(stAuthEl.textContent).toBe("signed in");
  });
});

// ===========================================================================
// git.refresh_prs — deduped, retryable
// ===========================================================================

describe("git.refresh_prs", () => {
  it("calls refreshPRs from git-prs-tab and dedupes", async () => {
    const { refreshPRs: mockRefresh } = await import("../git-prs-tab.js");
    vi.mocked(mockRefresh).mockResolvedValue(undefined);
    const { refreshPRs } = await import("./git-prs.js");
    const p1 = refreshPRs.dispatch(undefined);
    const p2 = refreshPRs.dispatch(undefined);
    await Promise.all([p1, p2]);
    expect(vi.mocked(mockRefresh)).toHaveBeenCalledTimes(1);
  });

  it("retries on network error", async () => {
    const { refreshPRs: mockRefresh } = await import("../git-prs-tab.js");
    vi.mocked(mockRefresh)
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(undefined);
    const { refreshPRs } = await import("./git-prs.js");
    const p = refreshPRs.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(300);
    await p;
    expect(vi.mocked(mockRefresh)).toHaveBeenCalledTimes(2);
    expect(recentLog()[0]?.status).toBe("success");
  });

  it("records error when refreshPRs throws non-network error", async () => {
    const { refreshPRs: mockRefresh } = await import("../git-prs-tab.js");
    vi.mocked(mockRefresh).mockRejectedValue(new Error("parse error"));
    const { refreshPRs } = await import("./git-prs.js");
    const p = refreshPRs.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await vi.advanceTimersByTimeAsync(1200);
    await p;
    expect(recentLog()[0]?.status).toBe("error");
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Couldn't refresh PRs"), expect.anything());
  });
});
