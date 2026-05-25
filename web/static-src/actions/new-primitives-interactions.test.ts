// @vitest-environment happy-dom
// Tests for interaction scenarios between dedupe, cancel, retry, scope,
// debouncedDispatch leading+flush, transportAction idempotency_key, and
// per-call callbacks on deduped dispatches.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../transport.js", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../transport.js")>();
  return { ...orig, send: vi.fn() };
});

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { debouncedDispatch } from "./debounce.js";
import { transportAction } from "./transport.js";
import { send as transportSend } from "../transport.js";
import { ActionError } from "./error.js";

const mockSend = vi.mocked(transportSend);

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

// ===========================================================================
// 1. dedupe + cancel interaction (R1, R11)
// ===========================================================================

describe("dedupe + cancel interaction", () => {
  it("two concurrent dispatches with dedupe, then cancel — both resolve null and dedupe map clears", async () => {
    let runCalls = 0;
    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe_cancel",
      dedupe: true,
      run: (_args, signal) => {
        runCalls++;
        return new Promise<string>((resolve, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        });
      },
    });

    const p1 = action.dispatch({ id: "a" });
    const p2 = action.dispatch({ id: "a" });
    // Dedupe collapses: only one run() call
    expect(runCalls).toBe(1);

    action.cancel();
    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1).toBeNull();
    expect(r2).toBeNull();

    // After cancellation, dedupe map clears — next dispatch starts fresh
    runCalls = 0;
    const p3 = action.dispatch({ id: "a" });
    expect(runCalls).toBe(1);
    action.cancel();
    await p3;
  });
});

// ===========================================================================
// 2. dedupe + retry interaction (R10, R11)
// ===========================================================================

describe("dedupe + retry interaction", () => {
  it("concurrent dispatches with dedupe + retry: first attempt fails, retry succeeds, both callers get success", async () => {
    vi.useFakeTimers();
    let attempt = 0;
    const action = defineAction<{ q: string }, string>({
      name: "test.dedupe_retry",
      dedupe: true,
      retryable: "network",
      retry: { count: 1, delay: 50 },
      run: () => {
        attempt++;
        if (attempt === 1) throw new ActionError("network fail", { code: "network" });
        return Promise.resolve("success");
      },
    });

    const p1 = action.dispatch({ q: "x" });
    const p2 = action.dispatch({ q: "x" });

    // Advance past retry delay
    await vi.advanceTimersByTimeAsync(50);
    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1).toBe("success");
    expect(r2).toBe("success");
    // Only one run sequence (first attempt + retry = 2 attempts total)
    expect(attempt).toBe(2);
    vi.useRealTimers();
  });
});

// ===========================================================================
// 3. scope + dedupe combined (R10, R11)
// ===========================================================================

describe("scope + dedupe combined", () => {
  it("dedupe wins over scope — only one runOnce call for same args", async () => {
    let runCalls = 0;
    const action = defineAction<{ q: string }, string>({
      name: "test.scope_dedupe",
      scope: "q",
      dedupe: true,
      run: () => {
        runCalls++;
        return Promise.resolve("done");
      },
    });

    const p1 = action.dispatch({ q: "a" });
    const p2 = action.dispatch({ q: "a" });

    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1).toBe("done");
    expect(r2).toBe("done");
    // Dedupe collapses: only one run() call (scope serialization bypassed)
    expect(runCalls).toBe(1);
  });
});

// ===========================================================================
// 4. scope undefined (R11)
// ===========================================================================

describe("scope with undefined arg value", () => {
  it("scope key 'chat:undefined' serializes dispatches with chatID=undefined together", async () => {
    // Document: 'chat:' + undefined === 'chat:undefined'
    expect("chat:" + undefined).toBe("chat:undefined");

    let callCount = 0;
    let resolveFirst: (() => void) | null = null;

    const action = defineAction<{ chatID: string | undefined }, string>({
      name: "test.scope_undefined",
      scope: (args) => "chat:" + args.chatID,
      run: () => {
        callCount++;
        if (callCount === 1) {
          return new Promise<string>((r) => { resolveFirst = () => r("first"); });
        }
        return Promise.resolve("second");
      },
    });

    const p1 = action.dispatch({ chatID: undefined });
    // Allow microtask for scope chain setup
    await Promise.resolve();
    const p2 = action.dispatch({ chatID: undefined });

    // First dispatch started, second is queued behind it (scope serialization)
    expect(callCount).toBe(1);

    resolveFirst!();
    const [r1, r2] = await Promise.all([p1, p2]);
    // Both ran sequentially under the same scope key 'chat:undefined'
    expect(callCount).toBe(2);
    expect(r1).toBe("first");
    expect(r2).toBe("second");
  });
});

// ===========================================================================
// 5. Deduped dispatch fires per-call callbacks (F1 fix)
// ===========================================================================

describe("deduped dispatch fires per-call callbacks", () => {
  it("both onSettled callbacks fire for two concurrent deduped dispatches", async () => {
    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe_callbacks",
      dedupe: true,
      run: () => Promise.resolve("ok"),
    });

    const settled1 = vi.fn();
    const settled2 = vi.fn();
    const onSuccess1 = vi.fn();
    const onSuccess2 = vi.fn();

    const p1 = action.dispatch({ id: "a" }, { onSettled: settled1, onSuccess: onSuccess1 });
    const p2 = action.dispatch({ id: "a" }, { onSettled: settled2, onSuccess: onSuccess2 });

    await Promise.all([p1, p2]);

    // F1 fix: both callers' callbacks fire
    expect(settled1).toHaveBeenCalledTimes(1);
    expect(settled2).toHaveBeenCalledTimes(1);
    expect(onSuccess1).toHaveBeenCalledTimes(1);
    expect(onSuccess2).toHaveBeenCalledTimes(1);
  });
});

// ===========================================================================
// 6. debouncedDispatch leading flush (F2 fix)
// ===========================================================================

describe("debouncedDispatch leading flush", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("leading mode: first fires immediately, flush() fires the most-recently-suppressed args", async () => {
    const runArgs: string[] = [];
    const action = defineAction<string, void>({
      name: "test.debounce_leading_flush",
      run: (args) => { runArgs.push(args); return Promise.resolve(); },
    });

    const dbg = debouncedDispatch(action, { wait: 100, leading: true });

    // First call fires immediately (leading edge)
    dbg("a");
    expect(runArgs).toEqual(["a"]);

    // Second call is suppressed during the leading window but stored
    // so flush() can fire it (F2 fix: previously flush was a no-op).
    dbg("b");
    expect(runArgs).toEqual(["a"]);

    // flush() during leading mode now fires the most-recent suppressed args
    dbg.flush();
    expect(runArgs).toEqual(["a", "b"]);

    // After wait expires, the cooldown resets
    await vi.advanceTimersByTimeAsync(100);

    // New call after window fires immediately again
    dbg("c");
    expect(runArgs).toEqual(["a", "b", "c"]);
  });

  it("flush with explicit args fires even in leading mode", async () => {
    const runArgs: string[] = [];
    const action = defineAction<string, void>({
      name: "test.debounce_leading_flush_explicit",
      run: (args) => { runArgs.push(args); return Promise.resolve(); },
    });

    const dbg = debouncedDispatch(action, { wait: 100, leading: true });
    dbg("a"); // fires immediately
    dbg("b"); // suppressed

    // flush with explicit args always fires
    dbg.flush("z");
    expect(runArgs).toContain("z");
  });
});

// ===========================================================================
// 7. transportAction idempotency_key in payload (F3 fix)
// ===========================================================================

describe("transportAction idempotency_key in payload", () => {
  it("sends idempotency key via ctx when idempotencyKey: true", async () => {
    const ctxKeys: (string | undefined)[] = [];
    const action = defineAction<{ chatID: string }, void>({
      name: "test.transport_idem_ctx",
      idempotencyKey: true,
      run: (_args, _signal, ctx) => {
        ctxKeys.push(ctx?.idempotencyKey);
        return Promise.resolve();
      },
    });

    await action.dispatch({ chatID: "c1" });
    expect(ctxKeys[0]).toBeDefined();
    expect(typeof ctxKeys[0]).toBe("string");
    expect(ctxKeys[0]!.length).toBeGreaterThan(5);
  });

  it("transportAction with idempotencyKey passes key to transport.send command", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });

    const action = transportAction<{ chatID: string }>({
      name: "test.transport_idem_payload",
      idempotencyKey: true,
      command: ({ chatID }) => ({ type: "cancel", chat_id: chatID }),
    });

    await action.dispatch({ chatID: "c1" });

    expect(mockSend).toHaveBeenCalledTimes(1);
    const cmd = mockSend.mock.calls[0]![0] as { type: string; chat_id: string; payload?: { idempotency_key?: string } };
    expect(cmd.type).toBe("cancel");
    expect(cmd.chat_id).toBe("c1");
    // F3 fix verification: the idempotency_key MUST be in the payload.
    expect(cmd.payload).toBeDefined();
    expect(cmd.payload!.idempotency_key).toEqual(expect.any(String));
    expect(cmd.payload!.idempotency_key!.length).toBeGreaterThan(5);
  });
});

// ===========================================================================
// 8. dedupe shared promise — both callers receive same result (R11)
// ===========================================================================

describe("dedupe shared promise behavior", () => {
  it("deduped dispatches resolve to the same value with only one run() call", async () => {
    let runCalls = 0;
    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe_shared",
      dedupe: true,
      run: () => {
        runCalls++;
        return new Promise<string>((r) => setTimeout(() => r("shared-result"), 10));
      },
    });

    const p1 = action.dispatch({ id: "x" });
    const p2 = action.dispatch({ id: "x" });
    // Only one run() call — dedupe collapsed
    expect(runCalls).toBe(1);

    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1).toBe("shared-result");
    expect(r2).toBe("shared-result");

    // Different args produce separate dispatches
    const p3 = action.dispatch({ id: "y" });
    expect(runCalls).toBe(2);
    await p3;
  });

  it("after resolution, next dispatch creates a fresh run() call", async () => {
    let runCalls = 0;
    let resolveRun: ((v: string) => void) | null = null;
    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe_shared_clear",
      dedupe: true,
      run: () => {
        runCalls++;
        return new Promise<string>((r) => { resolveRun = r; });
      },
    });

    const p1 = action.dispatch({ id: "a" });
    const p2 = action.dispatch({ id: "a" });
    expect(runCalls).toBe(1);

    resolveRun!("done");
    await Promise.all([p1, p2]);

    // New dispatch after resolution starts fresh
    const p3 = action.dispatch({ id: "a" });
    expect(runCalls).toBe(2);
    resolveRun!("done2");
    const r3 = await p3;
    expect(r3).toBe("done2");
  });
});
