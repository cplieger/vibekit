// @vitest-environment happy-dom
// Tests for the five UX primitives added on top of the action
// framework: idempotency keys, pendingCount,
// request deduplication, debouncedDispatch.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { retryNetwork } from "./error.js";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { defineAction, IDEMPOTENCY_HEADER, _resetForTest as resetDefine } from "./define.js";
import { apiAction } from "./api.js";
import { _resetForTest as resetRegistry, pendingCount } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { debouncedDispatch } from "./debounce.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

// ===========================================================================
// Idempotency keys
// ===========================================================================

describe("idempotencyKey", () => {
  it("apiAction sends Idempotency-Key header when configured: true", async () => {
    const fetchSpy = vi.fn<typeof fetch>(() =>
      Promise.resolve(new Response("{}", { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchSpy);

    const action = apiAction<{ id: string }>({
      name: "test.idem.true",
      idempotencyKey: true,
      request: ({ id }) => ({ method: "POST", path: `/api/x/${id}`, body: {} }),
    });
    await action.dispatch({ id: "abc" });
    // eslint-disable-next-line @typescript-eslint/no-non-null-asserted-optional-chain
    const init = fetchSpy.mock.calls[0]?.[1]!;
    const headers = init.headers as Record<string, string>;
    expect(headers[IDEMPOTENCY_HEADER]).toBeDefined();
    expect(typeof headers[IDEMPOTENCY_HEADER]).toBe("string");
    expect((headers[IDEMPOTENCY_HEADER]!).length).toBeGreaterThan(5);

    vi.unstubAllGlobals();
  });

  it("apiAction sends caller-supplied key from function form", async () => {
    const fetchSpy = vi.fn<typeof fetch>(() =>
      Promise.resolve(new Response("{}", { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchSpy);

    const action = apiAction<{ id: string }>({
      name: "test.idem.fn",
      idempotencyKey: (args) => `fixed-${args.id}`,
      request: () => ({ method: "POST", path: "/api/x", body: {} }),
    });
    await action.dispatch({ id: "abc" });
    // eslint-disable-next-line @typescript-eslint/no-non-null-asserted-optional-chain
    const init = fetchSpy.mock.calls[0]?.[1]!;
    const headers = init.headers as Record<string, string>;
    expect(headers[IDEMPOTENCY_HEADER]).toBe("fixed-abc");

    vi.unstubAllGlobals();
  });

  it("retries reuse the same idempotency key across attempts", async () => {
    let attempt = 0;
    const fetchSpy = vi.fn<typeof fetch>(() => {
      attempt++;
      if (attempt < 3) {
        return Promise.reject(new TypeError("Failed to fetch"));
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchSpy);
    vi.useFakeTimers();

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = apiAction<void>({
      name: "test.idem.retry",
      idempotencyKey: true,
      retryable: retryNetwork,
      retry: { count: 2, delay: 50 },
      request: () => ({ method: "POST", path: "/api/x", body: {} }),
    });
    const p = action.dispatch();
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(100);
    await p;

    expect(fetchSpy).toHaveBeenCalledTimes(3);
    // eslint-disable-next-line @typescript-eslint/no-non-null-asserted-optional-chain, no-unsafe-optional-chaining
    const k1 = (fetchSpy.mock.calls[0]?.[1]!).headers as Record<string, string>;
    // eslint-disable-next-line @typescript-eslint/no-non-null-asserted-optional-chain, no-unsafe-optional-chaining
    const k2 = (fetchSpy.mock.calls[1]?.[1]!).headers as Record<string, string>;
    // eslint-disable-next-line @typescript-eslint/no-non-null-asserted-optional-chain, no-unsafe-optional-chaining
    const k3 = (fetchSpy.mock.calls[2]?.[1]!).headers as Record<string, string>;
    expect(k1[IDEMPOTENCY_HEADER]).toBe(k2[IDEMPOTENCY_HEADER]);
    expect(k2[IDEMPOTENCY_HEADER]).toBe(k3[IDEMPOTENCY_HEADER]);

    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("no Idempotency-Key when idempotencyKey is undefined", async () => {
    const fetchSpy = vi.fn<typeof fetch>(() =>
      Promise.resolve(new Response("{}", { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchSpy);

    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = apiAction<void>({
      name: "test.idem.none",
      request: () => ({ method: "POST", path: "/api/x", body: {} }),
    });
    await action.dispatch();
    // eslint-disable-next-line @typescript-eslint/no-non-null-asserted-optional-chain
    const init = fetchSpy.mock.calls[0]?.[1]!;
    const headers = (init.headers ?? {}) as Record<string, string>;
    expect(headers[IDEMPOTENCY_HEADER]).toBeUndefined();

    vi.unstubAllGlobals();
  });

  it("custom defineAction can read idempotencyKey from ctx", async () => {
    const seen: string[] = [];
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = defineAction<void, void>({
      name: "test.idem.ctx",
      idempotencyKey: true,
      run: (_args, _signal, ctx) => {
        if (ctx?.idempotencyKey !== undefined) {seen.push(ctx.idempotencyKey);}
        return Promise.resolve();
      },
    });
    await action.dispatch();
    await action.dispatch();
    expect(seen).toHaveLength(2);
    expect(seen[0]).not.toBe(seen[1]);
  });
});

// ===========================================================================
// pendingCount (with and without name array)
// ===========================================================================

describe("pendingCount", () => {
  it("sums across all action names without arguments", async () => {
    // eslint-disable-next-line @typescript-eslint/no-empty-function
    let resolveA: () => void = () => {};
    // eslint-disable-next-line @typescript-eslint/no-empty-function
    let resolveB: () => void = () => {};
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const a = defineAction<void, void>({
      name: "test.pc.a",
      run: () =>
        new Promise<void>((r) => {
          resolveA = r;
        }),
    });
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const b = defineAction<void, void>({
      name: "test.pc.b",
      run: () =>
        new Promise<void>((r) => {
          resolveB = r;
        }),
    });
    expect(pendingCount()).toBe(0);
    const pa = a.dispatch();
    const pb = b.dispatch();
    expect(pendingCount()).toBe(2);
    resolveA();
    await pa;
    expect(pendingCount()).toBe(1);
    resolveB();
    await pb;
    expect(pendingCount()).toBe(0);
  });

  it("with names array returns count for those actions", async () => {
    // eslint-disable-next-line @typescript-eslint/no-empty-function
    let resolveA: () => void = () => {};
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const a = defineAction<void, void>({
      name: "test.pfa.a",
      run: () =>
        new Promise<void>((r) => {
          resolveA = r;
        }),
    });
    expect(pendingCount(["test.pfa.a", "test.pfa.b"])).toBe(0);
    const pa = a.dispatch();
    expect(pendingCount(["test.pfa.a"])).toBe(1);
    expect(pendingCount(["test.pfa.b"])).toBe(0);
    expect(pendingCount(["test.pfa.a", "test.pfa.b"])).toBe(1);
    resolveA();
    await pa;
    expect(pendingCount(["test.pfa.a"])).toBe(0);
  });

  it("with empty names array returns 0", () => {
    expect(pendingCount([])).toBe(0);
  });
});

// ===========================================================================
// Dedupe
// ===========================================================================

describe("dedupe", () => {
  it("two concurrent dispatches with matching args share one in-flight promise", async () => {
    let resolveRun: ((v: string) => void) | undefined;
    let runCalls = 0;
    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe",
      dedupe: true,
      run: () => {
        runCalls++;
        return new Promise<string>((r) => {
          resolveRun = r;
        });
      },
    });
    const p1 = action.dispatch({ id: "a" });
    const p2 = action.dispatch({ id: "a" });
    expect(runCalls).toBe(1); // only the first dispatch's run() fired
    resolveRun?.("ok");
    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1).toBe("ok");
    expect(r2).toBe("ok");
  });

  it("different args do NOT collapse", async () => {
    let runCalls = 0;
    const action = defineAction<{ id: string }, string>({
      name: "test.dedupe.different",
      dedupe: true,
      run: (args) => {
        runCalls++;
        return Promise.resolve(args.id);
      },
    });
    await Promise.all([action.dispatch({ id: "a" }), action.dispatch({ id: "b" })]);
    expect(runCalls).toBe(2);
  });

  it("dedupe entry clears after resolution; subsequent dispatch starts fresh", async () => {
    let runCalls = 0;
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = defineAction<{ id: string }, void>({
      name: "test.dedupe.clear",
      dedupe: true,
      run: () => {
        runCalls++;
        return Promise.resolve();
      },
    });
    await action.dispatch({ id: "a" });
    await action.dispatch({ id: "a" });
    expect(runCalls).toBe(2);
  });

  it("dedupe with custom function key", async () => {
    let runCalls = 0;
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = defineAction<{ user: string; tag: string }, void>({
      name: "test.dedupe.fn",
      dedupe: (args) => `user:${args.user}`, // ignore tag
      run: () => {
        runCalls++;
        return new Promise<void>((r) => setTimeout(r, 10));
      },
    });
    const p1 = action.dispatch({ user: "alice", tag: "x" });
    const p2 = action.dispatch({ user: "alice", tag: "y" }); // different tag, same user
    await Promise.all([p1, p2]);
    expect(runCalls).toBe(1);
  });
});

// ===========================================================================
// debouncedDispatch
// ===========================================================================

describe("debouncedDispatch", () => {
  it("coalesces rapid calls into a single dispatch with the latest args", async () => {
    vi.useFakeTimers();
    const runArgs: string[] = [];
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = defineAction<string, void>({
      name: "test.debounce.basic",
      run: (args) => {
        runArgs.push(args);
        return Promise.resolve();
      },
    });
    const dbg = debouncedDispatch(action, { wait: 100 });
    dbg("a");
    dbg("b");
    dbg("c");
    expect(runArgs).toEqual([]);
    await vi.advanceTimersByTimeAsync(100);
    expect(runArgs).toEqual(["c"]);
    vi.useRealTimers();
  });

  it("flush() fires immediately with most-recent args", async () => {
    vi.useFakeTimers();
    const runArgs: string[] = [];
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = defineAction<string, void>({
      name: "test.debounce.flush",
      run: (args) => {
        runArgs.push(args);
        return Promise.resolve();
      },
    });
    const dbg = debouncedDispatch(action, { wait: 1000 });
    dbg("a");
    dbg("b");
     
    dbg.flush();
    await vi.advanceTimersByTimeAsync(0);
    expect(runArgs).toEqual(["b"]);
    // No additional fire when timer would have elapsed.
    await vi.advanceTimersByTimeAsync(1000);
    expect(runArgs).toEqual(["b"]);
    vi.useRealTimers();
  });

  it("cancel() drops pending dispatch", async () => {
    vi.useFakeTimers();
    const runArgs: string[] = [];
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = defineAction<string, void>({
      name: "test.debounce.cancel",
      run: (args) => {
        runArgs.push(args);
        return Promise.resolve();
      },
    });
    const dbg = debouncedDispatch(action, { wait: 100 });
    dbg("a");
    dbg.cancel();
    await vi.advanceTimersByTimeAsync(200);
    expect(runArgs).toEqual([]);
    vi.useRealTimers();
  });

  it("leading: true fires immediately + suppresses trailing fires within wait, but trailing-fires queued args after cooldown", async () => {
    vi.useFakeTimers();
    const runArgs: string[] = [];
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = defineAction<string, void>({
      name: "test.debounce.leading",
      run: (args) => {
        runArgs.push(args);
        return Promise.resolve();
      },
    });
    const dbg = debouncedDispatch(action, { wait: 100, leading: true });
    dbg("a");
    dbg("b");
    dbg("c");
    // Leading edge fires "a" immediately; "b" and "c" are suppressed.
    expect(runArgs).toEqual(["a"]);
    // After cooldown, the trailing timer fires the most-recent
    // suppressed args ("c"). This is leading+trailing semantics.
    await vi.advanceTimersByTimeAsync(100);
    expect(runArgs).toEqual(["a", "c"]);
    // Trailing fire started a new cooldown; immediate dbg("d") suppressed.
    dbg("d");
    expect(runArgs).toEqual(["a", "c"]);
    // After the post-trailing cooldown, "d" fires.
    await vi.advanceTimersByTimeAsync(100);
    expect(runArgs).toEqual(["a", "c", "d"]);
    vi.useRealTimers();
  });

  it("isPending() reflects timer state", async () => {
    vi.useFakeTimers();
    // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument
    const action = defineAction<string, void>({
      name: "test.debounce.is_pending",
      run: () => Promise.resolve(),
    });
    const dbg = debouncedDispatch(action, { wait: 100 });
    expect(dbg.isPending()).toBe(false);
    dbg("a");
    expect(dbg.isPending()).toBe(true);
    await vi.advanceTimersByTimeAsync(100);
    expect(dbg.isPending()).toBe(false);
    vi.useRealTimers();
  });
});
