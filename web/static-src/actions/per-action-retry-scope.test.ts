// @vitest-environment happy-dom
// Per-action coverage: retry, scope serialization, dedupe, idempotency,
// and callback behavior for files, forge, chat, and git-changes actions.
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

vi.mock("../files-shared.js", () => ({
  joinPath: (dir: string, name: string) => dir ? `${dir}/${name}` : name,
}));

vi.mock("../upload.js", () => ({
  uploadFiles: vi.fn(),
}));

vi.mock("../store.js", () => ({
  get: vi.fn(),
  setThinking: vi.fn(),
  setSupervisedMode: vi.fn(),
  setAutoApproveCrew: vi.fn(),
  setModel: vi.fn(),
  setFrozen: vi.fn(),
  enqueuePrompt: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: vi.fn(),
  getSessions: vi.fn(() => []),
}));

vi.mock("../chat-commands.js", () => ({
  sendPromptTo: vi.fn(),
}));

import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
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
// files: createFile retry on network error
// ===========================================================================

describe("files.create_file — retry on network error", () => {
  it("retries on TypeError (network) and succeeds", async () => {
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response(JSON.stringify({}), { status: 200 }));
    const { createFile } = await import("./files.js");
    const p = createFile.dispatch({ dir: "/src", name: "new.ts" });
    await vi.advanceTimersByTimeAsync(300);
    const r = await p;
    expect(r).not.toBeNull();
    expect(mockFetch).toHaveBeenCalledTimes(2);
    expect(recentLog()[0]?.status).toBe("success");
  });

  it("serializes via scope (same dir)", async () => {
    const log: number[] = [];
    mockFetch.mockImplementation(async () => {
      log.push(Date.now());
      await new Promise<void>((r) => setTimeout(r, 50));
      return new Response(JSON.stringify({}), { status: 200 });
    });
    const { createFile, createFolder } = await import("./files.js");
    const p1 = createFile.dispatch({ dir: "/src", name: "a.ts" });
    const p2 = createFolder.dispatch({ dir: "/src", name: "lib" });
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    // Second starts after first finishes (serialized via scope "dir:/src")
    expect(log[1]! - log[0]!).toBeGreaterThanOrEqual(50);
  });
});

// ===========================================================================
// files: renameFile idempotency key
// ===========================================================================

describe("files.rename — idempotency key", () => {
  it("includes Idempotency-Key header on rename", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { renameFile } = await import("./files.js");
    await renameFile.dispatch({ dir: "/src", original: "old.ts", newName: "new.ts" });
    const headers = mockFetch.mock.calls[0]![1].headers as Record<string, string>;
    expect(headers["Idempotency-Key"]).toEqual(expect.any(String));
  });

  it("retries with same idempotency key on network error", async () => {
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response(JSON.stringify({}), { status: 200 }));
    const { renameFile } = await import("./files.js");
    const p = renameFile.dispatch({ dir: "/src", original: "a.ts", newName: "b.ts" });
    await vi.advanceTimersByTimeAsync(300);
    await p;
    const key1 = (mockFetch.mock.calls[0]![1].headers as Record<string, string>)["Idempotency-Key"];
    const key2 = (mockFetch.mock.calls[1]![1].headers as Record<string, string>)["Idempotency-Key"];
    expect(key1).toBe(key2);
  });
});

// ===========================================================================
// forge: startDeviceFlow dedupe
// ===========================================================================

describe("forge.start_device_flow — dedupe", () => {
  it("dedupes concurrent dispatches", async () => {
    mockFetch.mockImplementation(() =>
      new Promise((r) => setTimeout(() => r(new Response(JSON.stringify({ device_code: "abc" }), { status: 200 })), 50)),
    );
    const { startDeviceFlow } = await import("./forge.js");
    const p1 = startDeviceFlow.dispatch(undefined);
    const p2 = startDeviceFlow.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });
});

// ===========================================================================
// forge: cloneRepo retry + idempotency
// ===========================================================================

describe("forge.clone_repo — retry + idempotency", () => {
  it("retries on network error with same idempotency key", async () => {
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response(JSON.stringify({ output: "done" }), { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    const p = cloneRepo.dispatch({ url: "https://github.com/org/repo" });
    await vi.advanceTimersByTimeAsync(300);
    const r = await p;
    expect(r).toEqual({ output: "done" });
    expect(mockFetch).toHaveBeenCalledTimes(2);
    const key1 = (mockFetch.mock.calls[0]![1].headers as Record<string, string>)["Idempotency-Key"];
    const key2 = (mockFetch.mock.calls[1]![1].headers as Record<string, string>)["Idempotency-Key"];
    expect(key1).toBe(key2);
  });
});

// ===========================================================================
// forge: connectPAT retry + idempotency
// ===========================================================================

describe("forge.connect_pat — retry + idempotency", () => {
  it("retries on 503 with same idempotency key", async () => {
    mockFetch
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: "unavailable" }), { status: 503 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ status: "ok" }), { status: 200 }));
    const { connectPAT } = await import("./forge.js");
    const p = connectPAT.dispatch({ kind: "github" as any, host: "github.com", token: "ghp_xxx" });
    await vi.advanceTimersByTimeAsync(300);
    const r = await p;
    expect(r).toEqual({ status: "ok" });
    expect(mockFetch).toHaveBeenCalledTimes(2);
    const key1 = (mockFetch.mock.calls[0]![1].headers as Record<string, string>)["Idempotency-Key"];
    const key2 = (mockFetch.mock.calls[1]![1].headers as Record<string, string>)["Idempotency-Key"];
    expect(key1).toBe(key2);
  });
});

// ===========================================================================
// chat: setSupervised retry + scope + rollback
// ===========================================================================

describe("chat.set_supervised — retry + scope + rollback", () => {
  it("retries on network error via transport", async () => {
    const { get: storeGet } = await import("../store.js");
    vi.mocked(storeGet).mockReturnValue({ id: "c1", supervised_mode: false } as any);
    mockSend
      .mockResolvedValueOnce({ ok: false, status: 0, error: "net", code: "network" })
      .mockResolvedValueOnce({ ok: false, status: 0, error: "net", code: "network" })
      .mockResolvedValueOnce({ ok: true, status: 200 });
    const { setSupervised } = await import("./chat.js");
    const p = setSupervised.dispatch({ chatID: "c1", enabled: true });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(mockSend).toHaveBeenCalledTimes(3);
    expect(recentLog()[0]?.status).toBe("success");
  });

  it("rolls back on final failure", async () => {
    const { get: storeGet, setSupervisedMode } = await import("../store.js");
    vi.mocked(storeGet).mockReturnValue({ id: "c1", supervised_mode: false } as any);
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "server error" });
    const { setSupervised } = await import("./chat.js");
    const p = setSupervised.dispatch({ chatID: "c1", enabled: true });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await vi.advanceTimersByTimeAsync(1200);
    await p;
    // Rollback restores previous value
    expect(vi.mocked(setSupervisedMode)).toHaveBeenLastCalledWith("c1", false);
  });
});

// ===========================================================================
// chat: cancelTurn retry
// ===========================================================================

describe("chat.cancel_turn — retry on network error", () => {
  it("retries and succeeds", async () => {
    mockSend
      .mockResolvedValueOnce({ ok: false, status: 0, error: "net", code: "network" })
      .mockResolvedValueOnce({ ok: true, status: 200 });
    const { cancelTurn } = await import("./chat.js");
    const p = cancelTurn.dispatch("c1");
    await vi.advanceTimersByTimeAsync(300);
    await p;
    expect(mockSend).toHaveBeenCalledTimes(2);
    expect(recentLog()[0]?.status).toBe("success");
  });
});

// ===========================================================================
// chat: loadHistory dedupe + retry
// ===========================================================================

describe("chat.load_history — dedupe + retry", () => {
  it("dedupes concurrent dispatches", async () => {
    mockFetch.mockImplementation(() =>
      new Promise((r) => setTimeout(() => r(new Response(JSON.stringify({ chats: [] }), { status: 200 })), 50)),
    );
    const { loadHistory } = await import("./chat.js");
    const p1 = loadHistory.dispatch(undefined);
    const p2 = loadHistory.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("retries on network error", async () => {
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response(JSON.stringify({ chats: [{ id: "c1" }] }), { status: 200 }));
    const { loadHistory } = await import("./chat.js");
    const p = loadHistory.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(300);
    const r = await p;
    expect(r).toEqual({ chats: [{ id: "c1" }] });
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });
});

// ===========================================================================
// chat: switchModel scope serialization + rollback
// ===========================================================================

describe("chat.switch_model — scope + rollback", () => {
  it("rolls back model on transport failure", async () => {
    const { get: storeGet, setModel } = await import("../store.js");
    vi.mocked(storeGet).mockReturnValue({ id: "c1", model: "claude-3" } as any);
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    const { switchModel } = await import("./chat.js");
    const p = switchModel.dispatch({ chatID: "c1", model: "gpt-4" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await vi.advanceTimersByTimeAsync(1200);
    await p;
    // Rollback restores previous model
    expect(vi.mocked(setModel)).toHaveBeenLastCalledWith("c1", "claude-3");
  });
});

// ===========================================================================
// git-changes: stage scope serialization
// ===========================================================================

describe("git.stage — scope serialization", () => {
  it("serializes dispatches for same repo", async () => {
    const log: number[] = [];
    mockFetch.mockImplementation(async () => {
      log.push(Date.now());
      await new Promise<void>((r) => setTimeout(r, 50));
      return new Response(JSON.stringify({}), { status: 200 });
    });
    const { stage, unstage } = await import("./git-changes.js");
    const p1 = stage.dispatch({ repo: "main", files: ["a.ts"] });
    const p2 = unstage.dispatch({ repo: "main", files: ["b.ts"] });
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(log[1]! - log[0]!).toBeGreaterThanOrEqual(50);
  });

  it("runs in parallel for different repos", async () => {
    const log: string[] = [];
    mockFetch.mockImplementation(async (_url: string, opts: RequestInit) => {
      const body = JSON.parse(opts.body as string) as { repo: string };
      log.push(`start:${body.repo}`);
      await new Promise<void>((r) => setTimeout(r, 50));
      log.push(`end:${body.repo}`);
      return new Response(JSON.stringify({}), { status: 200 });
    });
    const { stage } = await import("./git-changes.js");
    const p1 = stage.dispatch({ repo: "repoA", files: ["a.ts"] });
    const p2 = stage.dispatch({ repo: "repoB", files: ["b.ts"] });
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    // Both start before either ends (parallel)
    expect(log.indexOf("start:repoB")).toBeLessThan(log.indexOf("end:repoA"));
  });
});

// ===========================================================================
// git-changes: generateCommitMessage dedupe
// ===========================================================================

describe("git.generate_message — dedupe", () => {
  it("dedupes concurrent dispatches for same repo", async () => {
    mockFetch.mockImplementation(() =>
      new Promise((r) => setTimeout(() => r(new Response(JSON.stringify({ message: "feat: x" }), { status: 200 })), 50)),
    );
    const { generateCommitMessage } = await import("./git-changes.js");
    const p1 = generateCommitMessage.dispatch({ repo: "main" });
    const p2 = generateCommitMessage.dispatch({ repo: "main" });
    await vi.advanceTimersByTimeAsync(50);
    const [r1, r2] = await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(r1).toEqual({ message: "feat: x" });
    expect(r2).toEqual({ message: "feat: x" });
  });

  it("does not dedupe different repos", async () => {
    mockFetch.mockImplementation(() =>
      new Promise((r) => setTimeout(() => r(new Response(JSON.stringify({ message: "m" }), { status: 200 })), 50)),
    );
    const { generateCommitMessage } = await import("./git-changes.js");
    const p1 = generateCommitMessage.dispatch({ repo: "repoA" });
    const p2 = generateCommitMessage.dispatch({ repo: "repoB" });
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });
});

// ===========================================================================
// chat: onSuccess / onError / onSettled callbacks
// ===========================================================================

describe("chat.cancel_turn — dispatch callbacks", () => {
  it("fires onSuccess callback on success", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const onSuccess = vi.fn();
    const onSettled = vi.fn();
    const { cancelTurn } = await import("./chat.js");
    await cancelTurn.dispatch("c1", { onSuccess, onSettled });
    expect(onSuccess).toHaveBeenCalledTimes(1);
    expect(onSettled).toHaveBeenCalledWith("c1");
  });

  it("fires onError callback on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    const onError = vi.fn();
    const onSettled = vi.fn();
    const { cancelTurn } = await import("./chat.js");
    const p = cancelTurn.dispatch("c1", { onError, onSettled });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await vi.advanceTimersByTimeAsync(1200);
    await p;
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ message: "fail" }), "c1");
    expect(onSettled).toHaveBeenCalledWith("c1");
  });

  it("fires onCancel callback on cancellation", async () => {
    mockSend.mockImplementation((_cmd, opts) =>
      new Promise((_resolve, reject) => {
        opts!.signal!.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      }),
    );
    const onCancel = vi.fn();
    const onSettled = vi.fn();
    const { cancelTurn } = await import("./chat.js");
    const p = cancelTurn.dispatch("c1", { onCancel, onSettled });
    cancelTurn.cancel();
    await p;
    expect(onCancel).toHaveBeenCalledWith("c1");
    expect(onSettled).toHaveBeenCalledWith("c1");
  });
});

// ===========================================================================
// files: downloadFiles abort handling
// ===========================================================================

describe("files.download — abort handling", () => {
  it("returns null on cancellation", async () => {
    mockFetch.mockImplementation((_url: string, opts: RequestInit) =>
      new Promise((_resolve, reject) => {
        opts.signal!.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      }),
    );
    const { downloadFiles } = await import("./files.js");
    const p = downloadFiles.dispatch({ paths: ["/a.ts"] });
    downloadFiles.cancel();
    const r = await p;
    expect(r).toBeNull();
    expect(recentLog()[0]?.status).toBe("cancelled");
  });
});
