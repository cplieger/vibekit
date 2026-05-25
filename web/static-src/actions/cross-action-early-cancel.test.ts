// @vitest-environment happy-dom
// Cycle 14 Stage 1 Batch 2: Cross-action early-cancel race improvement.
// Validates that cancelled scope-queued dispatches resolve their dispatch
// promise immediately (without waiting for the previous entry to finish),
// and that callbacks fire eagerly before the promise resolves.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine, _internalsForTest } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

// ===========================================================================
// 1. Cancelled scope-queued dispatch resolves immediately
// ===========================================================================

describe("cancelled scope-queued dispatch resolves immediately", () => {
  it("dispatch promise resolves before prev finishes", async () => {
    let resolveA!: (v: string) => void;
    let bResolved = false;

    const actionA = defineAction<void, string>({
      name: "test.early_A",
      scope: "early",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.early_B",
      scope: "early",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();

    // Cancel B while A is still running
    actionB.cancel();

    // B's dispatch promise should resolve immediately (null) without
    // waiting for A to finish.
    const rB = await pB;
    bResolved = true;
    expect(rB).toBeNull();
    expect(bResolved).toBe(true);

    // A is still running
    expect(actionA.isInflight).toBe(true);

    // Now resolve A
    resolveA!("A-done");
    const rA = await pA;
    expect(rA).toBe("A-done");
  });

  it("onCancel fires before dispatch promise resolves", async () => {
    let resolveA!: (v: string) => void;
    const callOrder: string[] = [];

    const actionA = defineAction<void, string>({
      name: "test.early_cb_A",
      scope: "early-cb",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.early_cb_B",
      scope: "early-cb",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch(undefined, {
      onCancel: () => { callOrder.push("onCancel"); },
      onSettled: () => { callOrder.push("onSettled"); },
    });

    actionB.cancel();

    // Callbacks should have fired synchronously during cancel()
    expect(callOrder).toEqual(["onCancel", "onSettled"]);

    const rB = await pB;
    expect(rB).toBeNull();

    resolveA!("A");
    await pA;
  });

  it("registry records cancelled status eagerly", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.early_reg_A",
      scope: "early-reg",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.early_reg_B",
      scope: "early-reg",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    actionB.dispatch();
    actionB.cancel();

    // Registry should already have the cancelled entry
    const log = recentLog();
    const bEntry = log.find((e) => e.name === "test.early_reg_B");
    expect(bEntry).toBeDefined();
    expect(bEntry!.status).toBe("cancelled");

    resolveA!("A");
    await pA;
  });
});

// ===========================================================================
// 2. Multiple cancelled scope-queued dispatches all resolve immediately
// ===========================================================================

describe("multiple cancelled scope-queued dispatches resolve immediately", () => {
  it("all cancelled dispatches resolve before prev finishes", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.multi_early_A",
      scope: "multi-early",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<number, string>({
      name: "test.multi_early_B",
      scope: "multi-early",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();

    const pB1 = actionB.dispatch(1);
    const pB2 = actionB.dispatch(2);
    const pB3 = actionB.dispatch(3);

    actionB.cancel();

    // All should resolve immediately
    const [rB1, rB2, rB3] = await Promise.all([pB1, pB2, pB3]);
    expect(rB1).toBeNull();
    expect(rB2).toBeNull();
    expect(rB3).toBeNull();

    // A is still running
    expect(actionA.isInflight).toBe(true);

    resolveA!("A");
    await pA;
  });

  it("scope chain drains after all early-cancelled", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.drain_early_A",
      scope: "drain-early",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.drain_early_B",
      scope: "drain-early",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    actionB.cancel();
    await pB;

    resolveA!("A");
    await pA;
    // Extra ticks: tail resolution is deferred through prev to preserve
    // scope serialization (cross-action race fix).
    for (let i = 0; i < 5; i++) await Promise.resolve();

    const { scopeChains } = _internalsForTest();
    expect(scopeChains).toBe(0);
  });
});

// ===========================================================================
// 3. Early-cancel does not double-record or double-fire callbacks
// ===========================================================================

describe("early-cancel does not double-record", () => {
  it("registry has exactly one entry per cancelled dispatch", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.no_double_A",
      scope: "no-double",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.no_double_B",
      scope: "no-double",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    actionB.cancel();

    // Resolve A so runOnce eventually runs for B (should no-op)
    resolveA!("A");
    await Promise.all([pA, pB]);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    const log = recentLog();
    const bEntries = log.filter((e) => e.name === "test.no_double_B");
    expect(bEntries).toHaveLength(1);
    expect(bEntries[0]!.status).toBe("cancelled");
  });

  it("onCancel fires exactly once", async () => {
    let resolveA!: (v: string) => void;
    const onCancel = vi.fn();
    const onSettled = vi.fn();

    const actionA = defineAction<void, string>({
      name: "test.once_cb_A",
      scope: "once-cb",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.once_cb_B",
      scope: "once-cb",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch(undefined, { onCancel, onSettled });
    actionB.cancel();

    resolveA!("A");
    await Promise.all([pA, pB]);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onSettled).toHaveBeenCalledTimes(1);
  });
});

// ===========================================================================
// 4. Early-cancel + subsequent dispatch: scope chain remains functional
// ===========================================================================

describe("early-cancel + subsequent dispatch", () => {
  it("dispatch after early-cancel runs correctly in scope", async () => {
    let resolveA!: (v: string) => void;
    const order: string[] = [];

    const actionA = defineAction<void, string>({
      name: "test.post_early_A",
      scope: "post-early",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.post_early_B",
      scope: "post-early",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        order.push("B");
        return Promise.resolve("B");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.post_early_C",
      scope: "post-early",
      run: () => { order.push("C"); return Promise.resolve("C"); },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    actionB.cancel();

    // B resolved immediately via early-cancel
    await pB;

    // Dispatch C into the same scope — should queue behind A
    const pC = actionC.dispatch();

    resolveA!("A");
    const [rA, rC] = await Promise.all([pA, pC]);

    expect(rA).toBe("A");
    expect(rC).toBe("C");
    expect(order).toEqual(["C"]);
  });

  it("re-dispatch same action after early-cancel works", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.redispatch_A",
      scope: "redispatch",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<number, string>({
      name: "test.redispatch_B",
      scope: "redispatch",
      error: false,
      run: (n, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve(`B-${n}`);
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();

    // First dispatch of B — cancel it
    const pB1 = actionB.dispatch(1);
    actionB.cancel();
    const rB1 = await pB1;
    expect(rB1).toBeNull();

    // Re-dispatch B — should work normally
    const pB2 = actionB.dispatch(2);

    resolveA!("A");
    const [rA, rB2] = await Promise.all([pA, pB2]);
    expect(rA).toBe("A");
    expect(rB2).toBe("B-2");
  });
});

// ===========================================================================
// 5. Early-cancel + dedupe interaction
// ===========================================================================

describe("early-cancel + dedupe interaction", () => {
  it("deduped dispatch after early-cancel starts fresh", async () => {
    let resolveA!: (v: string) => void;
    let runCount = 0;

    const actionA = defineAction<void, string>({
      name: "test.dedupe_early_A",
      scope: "dedupe-early",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<string, string>({
      name: "test.dedupe_early_B",
      scope: "dedupe-early",
      dedupe: true,
      error: false,
      run: (_args, signal) => {
        runCount++;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve(`B-${runCount}`);
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();

    const pB1 = actionB.dispatch("k");
    actionB.cancel();
    const rB1 = await pB1;
    expect(rB1).toBeNull();

    // Re-dispatch with same dedupe key — should start fresh
    const pB2 = actionB.dispatch("k");

    resolveA!("A");
    const [rA, rB2] = await Promise.all([pA, pB2]);
    expect(rA).toBe("A");
    expect(rB2).toBe("B-1"); // runCount=1 because first run was skipped
    expect(runCount).toBe(1);
  });
});
