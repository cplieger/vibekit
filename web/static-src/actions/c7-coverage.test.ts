// @vitest-environment happy-dom
// Cycle 7 coverage: tests for the 5 most impactful gaps identified:
//   1. switchModelAction — optimistic+rollback (untested)
//   2. crew.sendMessage — scope+idempotencyKey+retry (no test at all)
//   3. git-changes stage/pull — retry+scope+success toast (no test at all)
//   4. settings.logoutAction — optimistic+rollback (no test at all)
//   5. mcp.toggleServer retry — auto-retry on network error (only optimistic tested)
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../transport.js", () => ({ send: vi.fn() }));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

vi.mock("../store.js", () => ({
  get: (id: string) => id === "c1" ? { id: "c1", model: "gpt-4" } : undefined,
  setModel: vi.fn(),
  setThinking: vi.fn(),
  enqueuePrompt: vi.fn(),
  setSupervisedMode: vi.fn(),
  setAutoApproveCrew: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: () => 0,
  setFrozen: vi.fn(),
}));

vi.mock("../mcp-state.js", () => ({
  updateConfiguredEntry: vi.fn(),
  removeConfiguredEntry: vi.fn(),
  insertConfiguredEntry: vi.fn(),
}));

import { send as transportSend } from "../transport.js";
import * as toast from "../toast.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { updateConfiguredEntry } from "../mcp-state.js";
import type { Server } from "../mcp-state.js";

const mockSend = vi.mocked(transportSend);
const mockFetch = vi.fn();
const mockUpdate = vi.mocked(updateConfiguredEntry);

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

// ===========================================================================
// 1. switchModelAction — optimistic sets model, rollback restores on failure
// ===========================================================================

describe("switchModelAction optimistic + rollback", () => {
  it("sets model optimistically and keeps it on success", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { switchModelAction } = await import("./chat.js");
    const { setModel } = await import("../store.js");
    await switchModelAction.dispatch({ chatID: "c1", model: "claude-3" });
    // setModel was called with the new model
    expect(vi.mocked(setModel)).toHaveBeenCalledWith("c1", "claude-3");
  });

  it("rolls back model on transport failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "server error" });
    const { switchModelAction } = await import("./chat.js");
    const { setModel } = await import("../store.js");
    await switchModelAction.dispatch({ chatID: "c1", model: "claude-3" });
    // First call: optimistic (set to claude-3), second call: rollback (restore gpt-4)
    const calls = vi.mocked(setModel).mock.calls;
    expect(calls.length).toBeGreaterThanOrEqual(2);
    expect(calls[calls.length - 1]).toEqual(["c1", "gpt-4"]);
  });

  it("error toast fires on transport failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "server error" });
    const { switchModelAction } = await import("./chat.js");
    await switchModelAction.dispatch({ chatID: "c1", model: "claude-3" });
    expect(toast.error).toHaveBeenCalledTimes(1);
    const msg = vi.mocked(toast.error).mock.calls[0]![0];
    expect(msg).toContain("switch model");
  });
});

// ===========================================================================
// 2. crew.sendMessage — scope + idempotencyKey + retry
// ===========================================================================

describe("crew.sendMessage scope + idempotencyKey + retry", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("sends idempotency_key in command payload", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { sendMessage } = await import("./crew.js");
    await sendMessage.dispatch({ chatID: "c1", subSessionID: "sub1", text: "hi" });
    expect(mockSend).toHaveBeenCalledTimes(1);
    const cmd = mockSend.mock.calls[0]![0] as { payload?: { idempotency_key?: string } };
    expect(cmd.payload?.idempotency_key).toEqual(expect.any(String));
  });

  it("auto-retries on network error with same idempotency key", async () => {
    let attempt = 0;
    mockSend.mockImplementation(() => {
      attempt++;
      if (attempt < 3) return Promise.resolve({ ok: false, status: 0, error: "net", code: "network" });
      return Promise.resolve({ ok: true, status: 200 });
    });
    const { sendMessage } = await import("./crew.js");
    const p = sendMessage.dispatch({ chatID: "c1", subSessionID: "sub1", text: "hi" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(attempt).toBe(3);
    // Same idempotency key across retries
    const key1 = (mockSend.mock.calls[0]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    const key2 = (mockSend.mock.calls[1]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    const key3 = (mockSend.mock.calls[2]![0] as { payload?: { idempotency_key?: string } }).payload?.idempotency_key;
    expect(key1).toBe(key2);
    expect(key2).toBe(key3);
  });

  it("scope serializes messages to the same subagent", async () => {
    let callCount = 0;
    let resolveFirst: (() => void) | null = null;
    mockSend.mockImplementation(() => {
      callCount++;
      if (callCount === 1) return new Promise((r) => { resolveFirst = () => r({ ok: true, status: 200 }); });
      return Promise.resolve({ ok: true, status: 200 });
    });
    const { sendMessage } = await import("./crew.js");
    const p1 = sendMessage.dispatch({ chatID: "c1", subSessionID: "sub1", text: "a" });
    const p2 = sendMessage.dispatch({ chatID: "c1", subSessionID: "sub1", text: "b" });
    await Promise.resolve();
    expect(callCount).toBe(1);
    resolveFirst!();
    await p1;
    await vi.advanceTimersByTimeAsync(0);
    await p2;
    expect(callCount).toBe(2);
  });
});

// ===========================================================================
// 3. git-changes stage/pull — retry + scope + success toast
// ===========================================================================

describe("git-changes stage retry + scope + pull success toast", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("stage auto-retries on network error", async () => {
    let attempt = 0;
    mockFetch.mockImplementation(() => {
      attempt++;
      if (attempt < 3) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    const { stage } = await import("./git-changes.js");
    const p = stage.dispatch({ repo: "myrepo", files: ["a.ts"] });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    const result = await p;
    expect(attempt).toBe(3);
    expect(result).toBeDefined();
  });

  it("pull emits success toast with repo name", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { pull } = await import("./git-changes.js");
    await pull.dispatch({ repo: "myrepo" });
    expect(toast.success).toHaveBeenCalledWith("Pulled myrepo");
  });

  it("stage scope serializes dispatches for the same repo", async () => {
    let callCount = 0;
    let resolveFirst: (() => void) | null = null;
    mockFetch.mockImplementation(() => {
      callCount++;
      if (callCount === 1) return new Promise((r) => { resolveFirst = () => r(new Response("{}", { status: 200 })); });
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    const { stage } = await import("./git-changes.js");
    const p1 = stage.dispatch({ repo: "r", files: ["a.ts"] });
    const p2 = stage.dispatch({ repo: "r", files: ["b.ts"] });
    await Promise.resolve();
    expect(callCount).toBe(1);
    resolveFirst!();
    await p1;
    await Promise.resolve();
    await p2;
    expect(callCount).toBe(2);
  });

  it("stage error toast includes file name", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const { stage } = await import("./git-changes.js");
    await stage.dispatch({ repo: "r", files: ["important.ts"] });
    expect(toast.error).toHaveBeenCalledTimes(1);
    const msg = vi.mocked(toast.error).mock.calls[0]![0];
    expect(msg).toContain("important.ts");
  });
});

// ===========================================================================
// 4. settings.logoutAction — optimistic + rollback
// ===========================================================================

describe("settings.logoutAction optimistic + rollback", () => {
  it("clears email/status optimistically", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const emailEl = document.createElement("span");
    emailEl.textContent = "user@example.com";
    const stAuthEl = document.createElement("span");
    stAuthEl.textContent = "signed in";

    const { logoutAction } = await import("./settings.js");
    await logoutAction.dispatch({ emailEl, stAuthEl });
    expect(emailEl.textContent).toBe("");
    expect(stAuthEl.textContent).toBe("not signed in");
  });

  it("restores email/status on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const emailEl = document.createElement("span");
    emailEl.textContent = "user@example.com";
    const stAuthEl = document.createElement("span");
    stAuthEl.textContent = "signed in";

    const { logoutAction } = await import("./settings.js");
    await logoutAction.dispatch({ emailEl, stAuthEl });
    expect(emailEl.textContent).toBe("user@example.com");
    expect(stAuthEl.textContent).toBe("signed in");
  });

  it("error toast fires on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "session expired" }), { status: 401 }));
    const emailEl = document.createElement("span");
    emailEl.textContent = "u@e.com";
    const stAuthEl = document.createElement("span");
    stAuthEl.textContent = "signed in";

    const { logoutAction } = await import("./settings.js");
    await logoutAction.dispatch({ emailEl, stAuthEl });
    expect(toast.error).toHaveBeenCalledTimes(1);
    const msg = vi.mocked(toast.error).mock.calls[0]![0];
    expect(msg).toContain("session expired");
  });
});

// ===========================================================================
// 5. mcp.toggleServer retry — auto-retry on network error
// ===========================================================================

describe("mcp.toggleServer auto-retry on network error", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  function makeServer(id: string, enabled = true): Server {
    return { id, name: `srv-${id}`, transport: "stdio", enabled, created_at: 1000, updated_at: 1000 };
  }

  it("auto-retries on network error and succeeds", async () => {
    let attempt = 0;
    mockUpdate.mockReturnValue(makeServer("a", true));
    mockFetch.mockImplementation(() => {
      attempt++;
      if (attempt < 3) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve(new Response("", { status: 200 }));
    });
    const { toggleServer } = await import("./mcp.js");
    const p = toggleServer.dispatch({ id: "a", enabled: false });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(attempt).toBe(3);
    // No rollback on success — updateConfiguredEntry called once (optimistic only)
    expect(mockUpdate).toHaveBeenCalledTimes(1);
  });

  it("rolls back after all retries exhausted", async () => {
    mockUpdate.mockReturnValue(makeServer("a", true));
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"));
    const { toggleServer } = await import("./mcp.js");
    const p = toggleServer.dispatch({ id: "a", enabled: false });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    // Rollback restores previous enabled state
    expect(mockUpdate).toHaveBeenLastCalledWith("a", { enabled: true });
  });

  it("error toast fires after retries exhausted", async () => {
    mockUpdate.mockReturnValue(makeServer("a", true));
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"));
    const { toggleServer } = await import("./mcp.js");
    const p = toggleServer.dispatch({ id: "a", enabled: false });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(toast.error).toHaveBeenCalledTimes(1);
    const msg = vi.mocked(toast.error).mock.calls[0]![0];
    expect(msg).toContain("toggle integration");
  });
});
