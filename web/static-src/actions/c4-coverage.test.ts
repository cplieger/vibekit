// @vitest-environment happy-dom
// Cycle 4 coverage audit: tests for action defs missing per-feature coverage.
// Covers: optimistic+rollback, retry count, dedupe collapse, idempotencyKey sent.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../transport.js", () => ({ send: vi.fn() }));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  apiGet: vi.fn().mockResolvedValue(null),
  apiPost: vi.fn().mockResolvedValue(null),
}));

vi.mock("../store.js", () => ({
  get: vi.fn((id: string) => ({
    id, name: "test", model: "m1", supervised_mode: false,
    auto_approve_crew: false, frozen: false,
  })),
  setThinking: vi.fn(),
  setSupervisedMode: vi.fn(),
  setAutoApproveCrew: vi.fn(),
  setModel: vi.fn(),
  setFrozen: vi.fn(),
  enqueuePrompt: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: () => 0,
}));

vi.mock("../mcp-state.js", () => ({
  updateConfiguredEntry: vi.fn(),
  removeConfiguredEntry: vi.fn(),
  insertConfiguredEntry: vi.fn(),
}));

vi.mock("../push-util.js", () => ({
  urlBase64ToUint8Array: () => new Uint8Array(65),
}));

import { send as transportSend } from "../transport.js";
import { setSupervisedMode, setAutoApproveCrew, setModel } from "../store.js";
import { _resetForTest as resetDefine, IDEMPOTENCY_HEADER } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

const mockSend = vi.mocked(transportSend);
const mockFetch = vi.fn();

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
  mockFetch.mockReset();
  mockSend.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

// ===========================================================================
// chat.set_supervised — optimistic + rollback + retry count
// ===========================================================================

describe("chat.set_supervised", () => {
  it("optimistic: calls setSupervisedMode immediately", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { setSupervised } = await import("./chat.js");
    await setSupervised.dispatch({ chatID: "c1", enabled: true });
    expect(setSupervisedMode).toHaveBeenCalledWith("c1", true);
  });

  it("rollback: restores previous value on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    const { setSupervised } = await import("./chat.js");
    await setSupervised.dispatch({ chatID: "c1", enabled: true });
    // rollback restores prev (false)
    const calls = vi.mocked(setSupervisedMode).mock.calls;
    expect(calls.length).toBeGreaterThanOrEqual(2);
    expect(calls[calls.length - 1]).toEqual(["c1", false]);
  });

  it("retries up to 2 times on network error", async () => {
    vi.useFakeTimers();
    let attempts = 0;
    mockSend.mockImplementation(() => {
      attempts++;
      if (attempts < 3) return Promise.resolve({ ok: false, status: 0, error: "net", code: "network" });
      return Promise.resolve({ ok: true, status: 200 });
    });
    const { setSupervised } = await import("./chat.js");
    const p = setSupervised.dispatch({ chatID: "c1", enabled: true });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(attempts).toBe(3);
  });
});

// ===========================================================================
// chat.set_auto_approve_crew — optimistic + rollback + retry count
// ===========================================================================

describe("chat.set_auto_approve_crew", () => {
  it("optimistic: calls setAutoApproveCrew immediately", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { setAutoApproveCrewAction } = await import("./chat.js");
    await setAutoApproveCrewAction.dispatch({ chatID: "c1", enabled: true });
    expect(setAutoApproveCrew).toHaveBeenCalledWith("c1", true);
  });

  it("rollback: restores previous value on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    const { setAutoApproveCrewAction } = await import("./chat.js");
    await setAutoApproveCrewAction.dispatch({ chatID: "c1", enabled: true });
    const calls = vi.mocked(setAutoApproveCrew).mock.calls;
    expect(calls.length).toBeGreaterThanOrEqual(2);
    expect(calls[calls.length - 1]).toEqual(["c1", false]);
  });

  it("retries up to 2 times on network error", async () => {
    vi.useFakeTimers();
    let attempts = 0;
    mockSend.mockImplementation(() => {
      attempts++;
      if (attempts < 3) return Promise.resolve({ ok: false, status: 0, error: "net", code: "network" });
      return Promise.resolve({ ok: true, status: 200 });
    });
    const { setAutoApproveCrewAction } = await import("./chat.js");
    const p = setAutoApproveCrewAction.dispatch({ chatID: "c1", enabled: true });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(attempts).toBe(3);
  });
});

// ===========================================================================
// chat.switch_model — optimistic + rollback
// ===========================================================================

describe("chat.switch_model", () => {
  it("optimistic: calls setModel immediately", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { switchModelAction } = await import("./chat.js");
    await switchModelAction.dispatch({ chatID: "c1", model: "new-model" });
    expect(setModel).toHaveBeenCalledWith("c1", "new-model");
  });

  it("rollback: restores previous model on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    const { switchModelAction } = await import("./chat.js");
    await switchModelAction.dispatch({ chatID: "c1", model: "new-model" });
    const calls = vi.mocked(setModel).mock.calls;
    expect(calls.length).toBeGreaterThanOrEqual(2);
    expect(calls[calls.length - 1]).toEqual(["c1", "m1"]);
  });
});

// ===========================================================================
// chat.load_history — dedupe + retry
// ===========================================================================

describe("chat.load_history", () => {
  it("dedupe: collapses concurrent dispatches", async () => {
    let runCount = 0;
    mockFetch.mockImplementation(() => {
      runCount++;
      return Promise.resolve(new Response(JSON.stringify({ chats: [] }), { status: 200 }));
    });
    const { loadHistory } = await import("./chat.js");
    const p1 = loadHistory.dispatch();
    const p2 = loadHistory.dispatch();
    await Promise.all([p1, p2]);
    expect(runCount).toBe(1);
  });

  it("retry: retries up to 2 times on network error", async () => {
    vi.useFakeTimers();
    let attempts = 0;
    mockFetch.mockImplementation(() => {
      attempts++;
      if (attempts < 3) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve(new Response(JSON.stringify({ chats: [] }), { status: 200 }));
    });
    const { loadHistory } = await import("./chat.js");
    const p = loadHistory.dispatch();
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(attempts).toBe(3);
  });
});

// ===========================================================================
// files.rename — idempotencyKey + retry
// ===========================================================================

describe("files.rename", () => {
  it("sends Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { renameFile } = await import("./files.js");
    await renameFile.dispatch({ dir: "/src", original: "a.ts", newName: "b.ts" });
    const init = mockFetch.mock.calls[0]?.[1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers[IDEMPOTENCY_HEADER]).toBeDefined();
    expect(headers[IDEMPOTENCY_HEADER]!.length).toBeGreaterThan(5);
  });

  it("retries up to 2 times on network error", async () => {
    vi.useFakeTimers();
    let attempts = 0;
    mockFetch.mockImplementation(() => {
      attempts++;
      if (attempts < 3) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    const { renameFile } = await import("./files.js");
    const p = renameFile.dispatch({ dir: "/src", original: "a.ts", newName: "b.ts" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(attempts).toBe(3);
  });
});

// ===========================================================================
// git.stage — retry count
// ===========================================================================

describe("git.stage", () => {
  it("retries up to 2 times on network error", async () => {
    vi.useFakeTimers();
    let attempts = 0;
    mockFetch.mockImplementation(() => {
      attempts++;
      if (attempts < 3) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    const { stage } = await import("./git-changes.js");
    const p = stage.dispatch({ repo: "r1", files: ["a.ts"] });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(attempts).toBe(3);
  });
});

// ===========================================================================
// git.generate_message — dedupe + retry
// ===========================================================================

describe("git.generate_message", () => {
  it("dedupe: collapses concurrent dispatches for same repo", async () => {
    let runCount = 0;
    mockFetch.mockImplementation(() => {
      runCount++;
      return Promise.resolve(new Response(JSON.stringify({ message: "feat: x" }), { status: 200 }));
    });
    const { generateCommitMessage } = await import("./git-changes.js");
    const p1 = generateCommitMessage.dispatch({ repo: "r1" });
    const p2 = generateCommitMessage.dispatch({ repo: "r1" });
    await Promise.all([p1, p2]);
    expect(runCount).toBe(1);
  });
});

// ===========================================================================
// git.checkout_branch — retry count
// ===========================================================================

describe("git.checkout_branch", () => {
  it("retries up to 2 times on network error", async () => {
    vi.useFakeTimers();
    let attempts = 0;
    mockFetch.mockImplementation(() => {
      attempts++;
      if (attempts < 3) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    const { checkoutBranch } = await import("./git-branch.js");
    const p = checkoutBranch.dispatch({ repo: "r1", branch: "feat", create: false });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(attempts).toBe(3);
  });
});

// ===========================================================================
// git.close_pr — idempotencyKey
// ===========================================================================

describe("git.close_pr", () => {
  it("sends Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    // Need to mock git-prs-state for closePR
    vi.doMock("../git-prs-state.js", () => ({
      removePRFromGroups: vi.fn(),
      reinsertPRInGroups: vi.fn(),
    }));
    const { closePR } = await import("./git-prs.js");
    await closePR.dispatch({ forge_id: "gh1", owner: "org", name: "repo", pr_number: 5 });
    const init = mockFetch.mock.calls[0]?.[1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers[IDEMPOTENCY_HEADER]).toBeDefined();
  });
});

// ===========================================================================
// mcp.save_server — idempotencyKey + retry
// ===========================================================================

describe("mcp.save_server", () => {
  it("sends Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ id: "new" }), { status: 200 }));
    const { saveServer } = await import("./mcp.js");
    await saveServer.dispatch({ id: "", body: { name: "test" } });
    const init = mockFetch.mock.calls[0]?.[1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers[IDEMPOTENCY_HEADER]).toBeDefined();
  });

  it("retries up to 2 times on network error", async () => {
    vi.useFakeTimers();
    let attempts = 0;
    mockFetch.mockImplementation(() => {
      attempts++;
      if (attempts < 3) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve(new Response(JSON.stringify({ id: "new" }), { status: 200 }));
    });
    const { saveServer } = await import("./mcp.js");
    const p = saveServer.dispatch({ id: "", body: { name: "test" } });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(attempts).toBe(3);
  });
});

// ===========================================================================
// mcp.search_registry — dedupe
// ===========================================================================

describe("mcp.search_registry", () => {
  it("dedupe: collapses concurrent dispatches for same query", async () => {
    let runCount = 0;
    mockFetch.mockImplementation(() => {
      runCount++;
      return Promise.resolve(new Response(JSON.stringify({ servers: [] }), { status: 200 }));
    });
    const { searchRegistry } = await import("./mcp.js");
    const p1 = searchRegistry.dispatch({ q: "test" });
    const p2 = searchRegistry.dispatch({ q: "test" });
    await Promise.all([p1, p2]);
    expect(runCount).toBe(1);
  });
});

// ===========================================================================
// tools.install — idempotencyKey
// ===========================================================================

describe("tools.install", () => {
  it("sends Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { installTools } = await import("./tools.js");
    await installTools.dispatch();
    const init = mockFetch.mock.calls[0]?.[1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers[IDEMPOTENCY_HEADER]).toBeDefined();
  });
});

// ===========================================================================
// tools.run_diagnostics — dedupe + retry
// ===========================================================================

describe("tools.run_diagnostics", () => {
  it("dedupe: collapses concurrent dispatches", async () => {
    let runCount = 0;
    mockFetch.mockImplementation(() => {
      runCount++;
      return Promise.resolve(new Response(JSON.stringify({ report: "ok" }), { status: 200 }));
    });
    const { runDiagnostics } = await import("./tools.js");
    const p1 = runDiagnostics.dispatch();
    const p2 = runDiagnostics.dispatch();
    await Promise.all([p1, p2]);
    expect(runCount).toBe(1);
  });
});

// ===========================================================================
// forge.start_device_flow — dedupe
// ===========================================================================

describe("forge.start_device_flow", () => {
  it("dedupe: collapses concurrent dispatches", async () => {
    let runCount = 0;
    mockFetch.mockImplementation(() => {
      runCount++;
      return Promise.resolve(new Response(JSON.stringify({ device_code: "x", user_code: "y", verification_uri: "z", expires_in: 900, interval: 5 }), { status: 200 }));
    });
    const { startDeviceFlow } = await import("./forge.js");
    const p1 = startDeviceFlow.dispatch(undefined);
    const p2 = startDeviceFlow.dispatch(undefined);
    await Promise.all([p1, p2]);
    expect(runCount).toBe(1);
  });
});

// ===========================================================================
// chat.send_prompt — idempotencyKey sent in payload
// ===========================================================================

describe("chat.send_prompt idempotencyKey", () => {
  it("includes idempotency_key in transport payload", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { sendPromptAction } = await import("./chat.js");
    await sendPromptAction.dispatch({
      chatID: "c1", text: "hi", messageID: "m1",
      agent: "default", model: "m1", activeFile: "", openFiles: [],
    });
    const cmd = mockSend.mock.calls[0]?.[0] as Record<string, unknown>;
    const payload = cmd["payload"] as Record<string, unknown>;
    expect(payload["idempotency_key"]).toBeDefined();
    expect(typeof payload["idempotency_key"]).toBe("string");
  });
});

// ===========================================================================
// conflicts.open_diff — dedupe + retry
// ===========================================================================

describe("conflicts.open_diff", () => {
  it("dedupe: collapses concurrent dispatches", async () => {
    let runCount = 0;
    mockFetch.mockImplementation(() => {
      runCount++;
      return Promise.resolve(new Response("content", { status: 200 }));
    });
    vi.doMock("../editor-openers.js", () => ({
      openFileDiff: vi.fn(),
    }));
    const { openConflictDiff } = await import("./conflicts.js");
    const args = { chatID: "c1", path: "a.ts", expectedSha: "s1", actualSha: "s2", otherChat: "c2" };
    const p1 = openConflictDiff.dispatch(args);
    const p2 = openConflictDiff.dispatch(args);
    await Promise.all([p1, p2]);
    // 2 fetches per dispatch (expected + actual blobs), but only 1 dispatch runs
    expect(runCount).toBe(2);
  });
});

// ===========================================================================
// messages.explain_error — dedupe
// ===========================================================================

describe("messages.explain_error", () => {
  it("dedupe: collapses concurrent dispatches for same error text", async () => {
    let runCount = 0;
    mockFetch.mockImplementation(() => {
      runCount++;
      return Promise.resolve(new Response(JSON.stringify({ output: "explanation" }), { status: 200 }));
    });
    const { explainError } = await import("./messages.js");
    const p1 = explainError.dispatch({ errorText: "some error", context: "ctx" });
    const p2 = explainError.dispatch({ errorText: "some error", context: "ctx" });
    await Promise.all([p1, p2]);
    expect(runCount).toBe(1);
  });
});

// ===========================================================================
// settings.save_steering — retry
// ===========================================================================

describe("settings.save_steering", () => {
  it("retries up to 2 times on network error", async () => {
    vi.useFakeTimers();
    let attempts = 0;
    mockFetch.mockImplementation(() => {
      attempts++;
      if (attempts < 3) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    const { saveSteering } = await import("./settings.js");
    const p = saveSteering.dispatch({ content: "# steering" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(attempts).toBe(3);
  });
});

// ===========================================================================
// permissions.add_rule — idempotencyKey
// ===========================================================================

describe("permissions.add_rule idempotencyKey", () => {
  it("sends Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { addRule } = await import("./permissions.js");
    const rules = [{ pattern: "npm *", mode: "allow" as const, priority: 1, created_at: 1000 }];
    await addRule.dispatch({
      pattern: "git *", mode: "allow", priority: 2,
      rules, setRules: vi.fn(), getCurrentRules: () => rules,
    });
    const init = mockFetch.mock.calls[0]?.[1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers[IDEMPOTENCY_HEADER]).toBeDefined();
  });
});
