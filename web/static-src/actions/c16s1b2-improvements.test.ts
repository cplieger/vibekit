// Cycle 16 Stage 1 Batch 2 + Stage 2: actionStatus.lastCancelledAt,
// onceSettled AbortSignal, debouncedDispatch flush() returns promise,
// bindLoadingCluster ClusterHandle.pending getter.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, onceSettled } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { actionStatus, _resetForTest as resetActionStatus } from "./action-status.js";
import { debouncedDispatch } from "./debounce.js";
import { bindLoadingCluster } from "./loading.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  resetActionStatus();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

// ===========================================================================
// actionStatus.lastCancelledAt
// ===========================================================================

describe("actionStatus.lastCancelledAt", () => {
  it("starts at 0", () => {
    const s = actionStatus("test.cancel_ts");
    expect(s.lastCancelledAt).toBe(0);
  });

  it("updates on cancellation", async () => {
    const action = defineAction({
      name: "test.cancel_ts",
      run: (_args, signal) =>
        new Promise<void>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const s = actionStatus("test.cancel_ts");
    const p = action.dispatch(undefined);
    action.cancel();
    await p;
    expect(s.lastCancelledAt).toBeGreaterThan(0);
    expect(s.lastSettledAt).toBe(s.lastCancelledAt);
  });

  it("does not update on success", async () => {
    const action = defineAction({
      name: "test.cancel_ts_success",
      run: async () => "ok",
    });
    const s = actionStatus("test.cancel_ts_success");
    await action.dispatch(undefined);
    expect(s.lastCancelledAt).toBe(0);
    expect(s.lastSettledAt).toBeGreaterThan(0);
  });

  it("does not update on error", async () => {
    const action = defineAction({
      name: "test.cancel_ts_error",
      run: async () => { throw new Error("boom"); },
      error: false,
    });
    const s = actionStatus("test.cancel_ts_error");
    await action.dispatch(undefined);
    expect(s.lastCancelledAt).toBe(0);
    expect(s.lastSettledAt).toBeGreaterThan(0);
  });

  it("tracks the most recent cancellation timestamp", async () => {
    const action = defineAction({
      name: "test.cancel_ts_multi",
      run: (_args, signal) =>
        new Promise<void>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const s = actionStatus("test.cancel_ts_multi");
    const p1 = action.dispatch(undefined);
    action.cancel();
    await p1;
    const first = s.lastCancelledAt;
    expect(first).toBeGreaterThan(0);

    vi.advanceTimersByTime(100);
    const p2 = action.dispatch(undefined);
    action.cancel();
    await p2;
    expect(s.lastCancelledAt).toBeGreaterThanOrEqual(first);
  });
});

// ===========================================================================
// onceSettled with AbortSignal
// ===========================================================================

describe("onceSettled — AbortSignal support", () => {
  it("rejects with AbortError when signal is already aborted", async () => {
    const ac = new AbortController();
    ac.abort();
    await expect(onceSettled("test.never", ac.signal)).rejects.toThrow("aborted");
  });

  it("rejects with AbortError when signal aborts before action settles", async () => {
    const ac = new AbortController();
    const p = onceSettled("test.abort_mid", ac.signal);
    ac.abort();
    await expect(p).rejects.toThrow("aborted");
  });

  it("resolves normally when action settles before signal aborts", async () => {
    const ac = new AbortController();
    const action = defineAction({
      name: "test.settle_first",
      run: async () => "done",
    });
    const p = onceSettled("test.settle_first", ac.signal);
    await action.dispatch(undefined);
    const inst = await p;
    expect(inst.status).toBe("success");
    // Aborting after resolve is a no-op
    ac.abort();
  });

  it("works without signal (backward compatible)", async () => {
    const action = defineAction({
      name: "test.no_signal",
      run: async () => "ok",
    });
    const p = onceSettled("test.no_signal");
    await action.dispatch(undefined);
    const inst = await p;
    expect(inst.status).toBe("success");
  });

  it("cleans up listener on normal resolve", async () => {
    const ac = new AbortController();
    const action = defineAction({
      name: "test.cleanup_resolve",
      run: async () => "ok",
    });
    const p = onceSettled("test.cleanup_resolve", ac.signal);
    await action.dispatch(undefined);
    const inst = await p;
    expect(inst.status).toBe("success");
    // Aborting after resolve should not cause unhandled rejection
    ac.abort();
  });
});

// ===========================================================================
// debouncedDispatch flush() returns promise
// ===========================================================================

describe("debouncedDispatch — flush returns promise", () => {
  it("flush() returns the dispatch promise", async () => {
    const action = defineAction<string, string>({
      name: "test.flush_promise",
      run: async (args) => `result:${args}`,
    });
    const debounced = debouncedDispatch(action, { wait: 100 });
    debounced("hello");
    const p = debounced.flush();
    expect(p).toBeInstanceOf(Promise);
    const result = await p;
    expect(result).toBe("result:hello");
  });

  it("flush() returns undefined when nothing is pending", () => {
    const action = defineAction<string, string>({
      name: "test.flush_empty",
      run: async (args) => args,
    });
    const debounced = debouncedDispatch(action, { wait: 100 });
    const result = debounced.flush();
    expect(result).toBeUndefined();
  });

  it("flush(args) dispatches with provided args and returns promise", async () => {
    const action = defineAction<string, string>({
      name: "test.flush_args",
      run: async (args) => `got:${args}`,
    });
    const debounced = debouncedDispatch(action, { wait: 100 });
    const p = debounced.flush("direct");
    expect(p).toBeInstanceOf(Promise);
    const result = await p;
    expect(result).toBe("got:direct");
  });

  it("flush() in leading mode returns promise", async () => {
    const action = defineAction<string, string>({
      name: "test.flush_leading",
      run: async (args) => `lead:${args}`,
    });
    const debounced = debouncedDispatch(action, { wait: 100, leading: true });
    debounced("first"); // fires immediately (leading)
    vi.advanceTimersByTime(50);
    debounced("second"); // suppressed
    const p = debounced.flush();
    expect(p).toBeInstanceOf(Promise);
    const result = await p;
    expect(result).toBe("lead:second");
  });
});

// ===========================================================================
// bindLoadingCluster — ClusterHandle.pending getter
// ===========================================================================

describe("bindLoadingCluster — ClusterHandle", () => {
  it("pending getter reflects current state", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "test.cluster_handle",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const handle = bindLoadingCluster(["test.cluster_handle"], () => {});
    expect(handle.pending).toBe(false);
    const p = action.dispatch(undefined);
    expect(handle.pending).toBe(true);
    resolve();
    await p;
    expect(handle.pending).toBe(false);
  });

  it("pending is false for empty action names", () => {
    const handle = bindLoadingCluster([], () => {});
    expect(handle.pending).toBe(false);
  });

  it("dispose stops updates and pending stays at last value", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "test.cluster_dispose_pending",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const handle = bindLoadingCluster(["test.cluster_dispose_pending"], () => {});
    const p = action.dispatch(undefined);
    expect(handle.pending).toBe(true);
    handle.dispose();
    resolve();
    await p;
    // After dispose, pending is frozen at last computed value
    // (the onChange won't fire to update it)
    expect(handle.pending).toBe(true);
  });

  it("pending reflects OR across multiple actions", async () => {
    let resolve1!: () => void;
    let resolve2!: () => void;
    const a1 = defineAction({
      name: "test.cluster_or1",
      run: () => new Promise<void>((r) => { resolve1 = r; }),
    });
    const a2 = defineAction({
      name: "test.cluster_or2",
      run: () => new Promise<void>((r) => { resolve2 = r; }),
    });
    const handle = bindLoadingCluster(["test.cluster_or1", "test.cluster_or2"], () => {});
    expect(handle.pending).toBe(false);
    const p1 = a1.dispatch(undefined);
    expect(handle.pending).toBe(true);
    resolve1();
    await p1;
    expect(handle.pending).toBe(false);
    const p2 = a2.dispatch(undefined);
    expect(handle.pending).toBe(true);
    resolve2();
    await p2;
    expect(handle.pending).toBe(false);
  });
});
