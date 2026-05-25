// @vitest-environment happy-dom
// Cycle 8 coverage: tests for the 5 most impactful per-action gaps:
//   1. deleteFilesBatch — optimistic+rollback, scope (ZERO tests existed)
//   2. editor.resolve_partial — scope+retry+retryable (ZERO tests existed)
//   3. sendPrompt — rollback on transport failure (only 409 path tested)
//   4. forkChat — idempotencyKey+optimistic+rollback+scope (ZERO tests)
//   5. files.create_file — scope+retryable (ZERO tests existed)
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
  get: (id: string) => {
    if (id === "c1") return { id: "c1", model: "gpt-4", frozen: false };
    return undefined;
  },
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

vi.mock("../files-shared.js", () => ({
  joinPath: (dir: string, name: string) => `${dir}/${name}`,
}));

vi.mock("../upload.js", () => ({ uploadFiles: vi.fn() }));

import { send as transportSend } from "../transport.js";
import { setThinking, setFrozen } from "../store.js";
import * as toast from "../toast.js";
import { _resetForTest as resetDefine } from "./define.js";
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
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

// ===========================================================================
// 1. deleteFilesBatch — optimistic+rollback, scope
// ===========================================================================

describe("deleteFilesBatch optimistic + rollback + scope", () => {
  function makeListEl(names: string[]): HTMLElement {
    const el = document.createElement("div");
    for (const n of names) {
      const row = document.createElement("div");
      row.dataset["name"] = n;
      el.appendChild(row);
    }
    return el;
  }

  it("optimistic adds fb-row-exiting class, keeps on success", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { deleteFilesBatch } = await import("./files.js");
    const listEl = makeListEl(["a.ts", "b.ts"]);
    await deleteFilesBatch.dispatch({ dir: "/src", names: ["a.ts"], listEl });
    const row = listEl.children[0] as HTMLElement;
    expect(row.classList.contains("fb-row-exiting")).toBe(true);
  });

  it("rollback removes fb-row-exiting class on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "perm denied" }), { status: 403 }));
    const { deleteFilesBatch } = await import("./files.js");
    const listEl = makeListEl(["a.ts"]);
    await deleteFilesBatch.dispatch({ dir: "/src", names: ["a.ts"], listEl });
    const row = listEl.children[0] as HTMLElement;
    expect(row.classList.contains("fb-row-exiting")).toBe(false);
  });

  it("error toast fires with file name on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "not found" }), { status: 404 }));
    const { deleteFilesBatch } = await import("./files.js");
    const listEl = makeListEl(["important.ts"]);
    await deleteFilesBatch.dispatch({ dir: "/src", names: ["important.ts"], listEl });
    expect(toast.error).toHaveBeenCalledTimes(1);
    const msg = vi.mocked(toast.error).mock.calls[0]![0];
    expect(msg).toContain("important.ts");
  });

  it("scope serializes deletes in the same directory", async () => {
    let callCount = 0;
    let resolveFirst: (() => void) | null = null;
    mockFetch.mockImplementation(() => {
      callCount++;
      if (callCount === 1) return new Promise((r) => { resolveFirst = () => r(new Response("{}", { status: 200 })); });
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    const { deleteFilesBatch } = await import("./files.js");
    const listEl = makeListEl(["a.ts", "b.ts"]);
    const p1 = deleteFilesBatch.dispatch({ dir: "/src", names: ["a.ts"], listEl });
    const p2 = deleteFilesBatch.dispatch({ dir: "/src", names: ["b.ts"], listEl });
    // Allow microtasks to settle so scope chain queues the second dispatch
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    expect(callCount).toBe(1);
    resolveFirst!();
    await p1;
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    await p2;
    expect(callCount).toBe(2);
  });
});

// ===========================================================================
// 2. editor.resolve_partial — scope + retry + retryable
// ===========================================================================

describe("editor.resolve_partial scope + retry", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("auto-retries on network error and succeeds", async () => {
    let attempt = 0;
    mockSend.mockImplementation(() => {
      attempt++;
      if (attempt < 3) return Promise.resolve({ ok: false, status: 0, error: "net", code: "network" });
      return Promise.resolve({ ok: true, status: 200 });
    });
    const { resolvePendingPartial } = await import("./editor.js");
    const p = resolvePendingPartial.dispatch({ chatID: "c1", toolCallID: "tc1", mergedText: "merged" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(attempt).toBe(3);
  });

  it("error toast fires after retries exhausted", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "net", code: "network" });
    const { resolvePendingPartial } = await import("./editor.js");
    const p = resolvePendingPartial.dispatch({ chatID: "c1", toolCallID: "tc1", mergedText: "x" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(toast.error).toHaveBeenCalledTimes(1);
    const msg = vi.mocked(toast.error).mock.calls[0]![0];
    expect(msg).toContain("partial change");
  });

  it("scope serializes dispatches for the same chat", async () => {
    let callCount = 0;
    let resolveFirst: (() => void) | null = null;
    mockSend.mockImplementation(() => {
      callCount++;
      if (callCount === 1) return new Promise((r) => { resolveFirst = () => r({ ok: true, status: 200 }); });
      return Promise.resolve({ ok: true, status: 200 });
    });
    const { resolvePendingPartial } = await import("./editor.js");
    const p1 = resolvePendingPartial.dispatch({ chatID: "c1", toolCallID: "tc1", mergedText: "a" });
    const p2 = resolvePendingPartial.dispatch({ chatID: "c1", toolCallID: "tc2", mergedText: "b" });
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
// 3. sendPrompt — rollback clears thinking on transport failure
// ===========================================================================

describe("sendPrompt rollback on failure", () => {
  const promptArgs = {
    chatID: "c1", text: "hi", messageID: "m1", agent: "default",
    model: "gpt-4", activeFile: "", openFiles: [] as string[],
  };

  it("optimistic sets thinking, rollback clears on transport error", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "server error" });
    const { sendPrompt } = await import("./chat.js");
    await sendPrompt.dispatch(promptArgs);
    // setThinking(c1, true) then setThinking(c1, false) via rollback
    const calls = vi.mocked(setThinking).mock.calls;
    expect(calls[0]).toEqual(["c1", true]);
    expect(calls[calls.length - 1]).toEqual(["c1", false]);
  });

  it("idempotencyKey is sent in the command payload", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { sendPrompt } = await import("./chat.js");
    await sendPrompt.dispatch(promptArgs);
    const cmd = mockSend.mock.calls[0]![0] as { payload?: { idempotency_key?: string } };
    expect(cmd.payload?.idempotency_key).toEqual(expect.any(String));
  });

  it("scope serializes prompts to the same chat", async () => {
    let callCount = 0;
    let resolveFirst: (() => void) | null = null;
    mockSend.mockImplementation(() => {
      callCount++;
      if (callCount === 1) return new Promise((r) => { resolveFirst = () => r({ ok: true, status: 200 }); });
      return Promise.resolve({ ok: true, status: 200 });
    });
    const { sendPrompt } = await import("./chat.js");
    const p1 = sendPrompt.dispatch(promptArgs);
    const p2 = sendPrompt.dispatch({ ...promptArgs, text: "second" });
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    expect(callCount).toBe(1);
    resolveFirst!();
    await p1;
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    await p2;
    expect(callCount).toBe(2);
  });
});

// ===========================================================================
// 4. forkChat — idempotencyKey + optimistic + rollback + scope
// ===========================================================================

describe("forkChat idempotencyKey + optimistic + rollback", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("optimistic sets frozen, rollback restores on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    const { forkChat } = await import("./chat.js");
    await forkChat.dispatch({ chatID: "c1", tangentID: "t1" });
    const calls = vi.mocked(setFrozen).mock.calls;
    expect(calls[0]).toEqual(["c1", true]);
    expect(calls[calls.length - 1]).toEqual(["c1", false]);
  });

  it("sends idempotency_key in command payload", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { forkChat } = await import("./chat.js");
    await forkChat.dispatch({ chatID: "c1", tangentID: "t1" });
    const cmd = mockSend.mock.calls[0]![0] as { payload?: { idempotency_key?: string } };
    expect(cmd.payload?.idempotency_key).toEqual(expect.any(String));
  });

  it("scope serializes forks for the same chat", async () => {
    let callCount = 0;
    let resolveFirst: (() => void) | null = null;
    mockSend.mockImplementation(() => {
      callCount++;
      if (callCount === 1) return new Promise((r) => { resolveFirst = () => r({ ok: true, status: 200 }); });
      return Promise.resolve({ ok: true, status: 200 });
    });
    const { forkChat } = await import("./chat.js");
    const p1 = forkChat.dispatch({ chatID: "c1", tangentID: "t1" });
    const p2 = forkChat.dispatch({ chatID: "c1", tangentID: "t2" });
    await Promise.resolve();
    expect(callCount).toBe(1);
    resolveFirst!();
    await p1;
    await vi.advanceTimersByTimeAsync(0);
    await p2;
    expect(callCount).toBe(2);
  });

  it("retryable: network shows Retry button on network error", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "net", code: "network" });
    const { forkChat } = await import("./chat.js");
    await forkChat.dispatch({ chatID: "c1", tangentID: "t1" });
    expect(toast.error).toHaveBeenCalledTimes(1);
    const retryArg = vi.mocked(toast.error).mock.calls[0]![1];
    expect(retryArg).toBeDefined();
    expect(typeof retryArg?.onClick).toBe("function");
  });
});

// ===========================================================================
// 5. files.create_file — scope + retryable (network retry button)
// ===========================================================================

describe("files.create_file scope + retryable", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("succeeds on 200", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const { createFile } = await import("./files.js");
    const result = await createFile.dispatch({ dir: "/src", name: "new.ts" });
    expect(result).toEqual({});
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("error toast fires on server error", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "exists" }), { status: 409 }));
    const { createFile } = await import("./files.js");
    await createFile.dispatch({ dir: "/src", name: "dup.ts" });
    expect(toast.error).toHaveBeenCalledTimes(1);
    const msg = vi.mocked(toast.error).mock.calls[0]![0];
    expect(msg).toContain("exists");
  });

  it("retryable: network shows Retry button on network error", async () => {
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"));
    const { createFile } = await import("./files.js");
    const p = createFile.dispatch({ dir: "/src", name: "new.ts" });
    // Advance through retry backoff: 300ms + 600ms
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(toast.error).toHaveBeenCalledTimes(1);
    const retryArg = vi.mocked(toast.error).mock.calls[0]![1];
    expect(retryArg).toBeDefined();
    expect(typeof retryArg?.onClick).toBe("function");
  });

  it("scope serializes creates in the same directory", async () => {
    let callCount = 0;
    let resolveFirst: (() => void) | null = null;
    mockFetch.mockImplementation(() => {
      callCount++;
      if (callCount === 1) return new Promise((r) => { resolveFirst = () => r(new Response("{}", { status: 200 })); });
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    const { createFile } = await import("./files.js");
    const p1 = createFile.dispatch({ dir: "/src", name: "a.ts" });
    const p2 = createFile.dispatch({ dir: "/src", name: "b.ts" });
    await vi.advanceTimersByTimeAsync(0);
    expect(callCount).toBe(1);
    resolveFirst!();
    await p1;
    await vi.advanceTimersByTimeAsync(0);
    await p2;
    expect(callCount).toBe(2);
  });
});
