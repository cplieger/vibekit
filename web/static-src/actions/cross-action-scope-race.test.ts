// @vitest-environment happy-dom
// Cycle 13 Stage 1 Batch 2: Cross-action scope race improvements.
// Validates that cancelled scope-queued dispatches unblock subsequent
// entries immediately (via tail skip) rather than forcing them to wait
// for the cancelled entry's runOnce to process.
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
// 1. Cancelled scope-queued dispatch unblocks next entry via tail skip
// ===========================================================================

describe("cancelled scope-queued dispatch unblocks next entry", () => {
  it("C runs after A without waiting for cancelled B's slot", async () => {
    const order: string[] = [];
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.scope_race_A",
      scope: "race-unblock",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.scope_race_B",
      scope: "race-unblock",
      error: false,
      run: (_args, signal) => {
        order.push("B-run");
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.scope_race_C",
      scope: "race-unblock",
      run: () => {
        order.push("C-run");
        return Promise.resolve("C");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    const pC = actionC.dispatch();

    // Cancel B — its tail resolves immediately, so C proceeds after A
    actionB.cancel();

    resolveA!("A-done");
    const [rA, rB, rC] = await Promise.all([pA, pB, pC]);

    expect(rA).toBe("A-done");
    expect(rB).toBeNull();
    expect(rC).toBe("C");
    // B's run should NOT have been called (cancelled before start)
    expect(order).toEqual(["C-run"]);
  });

  it("cancelled middle entry does not corrupt scope chain ordering", async () => {
    const order: string[] = [];
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.scope_order_A",
      scope: "order-check",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.scope_order_B",
      scope: "order-check",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        order.push("B");
        return Promise.resolve("B");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.scope_order_C",
      scope: "order-check",
      run: () => { order.push("C"); return Promise.resolve("C"); },
    });

    const actionD = defineAction<void, string>({
      name: "test.scope_order_D",
      scope: "order-check",
      run: () => { order.push("D"); return Promise.resolve("D"); },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    const pC = actionC.dispatch();
    const pD = actionD.dispatch();

    actionB.cancel();

    resolveA!("A");
    const [rA, rB, rC, rD] = await Promise.all([pA, pB, pC, pD]);

    expect(rA).toBe("A");
    expect(rB).toBeNull();
    expect(rC).toBe("C");
    expect(rD).toBe("D");
    expect(order).toEqual(["C", "D"]);
  });

  it("scope chain drains fully after cancel-unblock", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.drain_race_A",
      scope: "drain-race",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.drain_race_B",
      scope: "drain-race",
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

    resolveA!("A");
    await Promise.all([pA, pB]);
    // Allow .finally() cleanup to run
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    const { scopeChains } = _internalsForTest();
    expect(scopeChains).toBe(0);
  });
});

// ===========================================================================
// 2. Tail skip resolves scope chain immediately on cancel
// ===========================================================================

describe("tail skip resolves scope chain immediately on cancel", () => {
  it("subsequent entry starts without waiting for cancelled entry's runOnce", async () => {
    let resolveA!: (v: string) => void;
    let cStarted = false;

    const actionA = defineAction<void, string>({
      name: "test.tail_skip_A",
      scope: "tail-skip",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.tail_skip_B",
      scope: "tail-skip",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.tail_skip_C",
      scope: "tail-skip",
      run: () => { cStarted = true; return Promise.resolve("C"); },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    const pC = actionC.dispatch();

    // Cancel B — tail resolves immediately
    actionB.cancel();

    // C's prev (B's tail) has resolved, so C can start once A finishes
    resolveA!("A");
    await Promise.all([pA, pB, pC]);

    expect(cStarted).toBe(true);
  });

  it("callbacks fire correctly for cancelled scope-queued dispatch", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.tail_cb_A",
      scope: "tail-cb",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.tail_cb_B",
      scope: "tail-cb",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const onCancel = vi.fn();
    const onSettled = vi.fn();

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch(undefined, { onCancel, onSettled });
    actionB.cancel();

    resolveA!("A");
    await Promise.all([pA, pB]);

    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onSettled).toHaveBeenCalledTimes(1);
  });

  it("registry records cancelled status", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.tail_reg_A",
      scope: "tail-reg",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.tail_reg_B",
      scope: "tail-reg",
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

    resolveA!("A");
    await Promise.all([pA, pB]);

    const log = recentLog();
    const bEntry = log.find((e) => e.name === "test.tail_reg_B");
    expect(bEntry).toBeDefined();
    expect(bEntry!.status).toBe("cancelled");
  });
});

// ===========================================================================
// 3. Multiple cancels in scope chain: all unblock independently
// ===========================================================================

describe("multiple cancels in scope chain", () => {
  it("cancelling B and C leaves D to run after A", async () => {
    const order: string[] = [];
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.multi_cancel_A",
      scope: "multi-cancel",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.multi_cancel_B",
      scope: "multi-cancel",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        order.push("B");
        return Promise.resolve("B");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.multi_cancel_C",
      scope: "multi-cancel",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        order.push("C");
        return Promise.resolve("C");
      },
    });

    const actionD = defineAction<void, string>({
      name: "test.multi_cancel_D",
      scope: "multi-cancel",
      run: () => { order.push("D"); return Promise.resolve("D"); },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    const pC = actionC.dispatch();
    const pD = actionD.dispatch();

    actionB.cancel();
    actionC.cancel();

    resolveA!("A");
    const [rA, rB, rC, rD] = await Promise.all([pA, pB, pC, pD]);

    expect(rA).toBe("A");
    expect(rB).toBeNull();
    expect(rC).toBeNull();
    expect(rD).toBe("D");
    expect(order).toEqual(["D"]);
  });

  it("cancel all queued entries: scope chain drains cleanly", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.cancel_all_A",
      scope: "cancel-all",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.cancel_all_B",
      scope: "cancel-all",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.cancel_all_C",
      scope: "cancel-all",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("C");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    const pC = actionC.dispatch();

    actionB.cancel();
    actionC.cancel();

    resolveA!("A");
    await Promise.all([pA, pB, pC]);
    // Allow .finally() cleanup
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    const { scopeChains } = _internalsForTest();
    expect(scopeChains).toBe(0);
  });
});

// ===========================================================================
// 4. Dedupe + scope + cancel race: re-dispatch after cancel starts fresh
// ===========================================================================

describe("dedupe + scope + cancel race", () => {
  it("re-dispatch with same dedupe key after cancel starts fresh run in scope", async () => {
    let runCount = 0;
    let resolveFirst!: (v: string) => void;

    const action = defineAction<string, string>({
      name: "test.dedupe_scope_cancel",
      dedupe: true,
      scope: "dsc",
      error: false,
      run: (args, signal) => {
        runCount++;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        if (runCount === 1) return new Promise<string>((r) => { resolveFirst = r; });
        return Promise.resolve(`fresh-${args}`);
      },
    });

    const p1 = action.dispatch("k");
    await Promise.resolve();
    expect(runCount).toBe(1);

    // Cancel first dispatch
    action.cancel();
    resolveFirst!("ignored");
    await p1;

    // Re-dispatch with same key — should start a fresh run
    const p2 = action.dispatch("k");
    const r2 = await p2;

    expect(runCount).toBe(2);
    expect(r2).toBe("fresh-k");
  });

  it("deduped dispatch queued in scope: cancel cleans dedupe map", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.dedupe_scope_unblock_A",
      scope: "dsu",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<string, string>({
      name: "test.dedupe_scope_unblock_B",
      dedupe: true,
      scope: "dsu",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch("x");
    actionB.cancel();

    resolveA!("A");
    await Promise.all([pA, pB]);

    // Dedupe map should be clean after settling
    await Promise.resolve();
    await Promise.resolve();
    const { dedupeInflight } = _internalsForTest();
    expect(dedupeInflight).toBe(0);
  });
});

// ===========================================================================
// 5. Cross-action interleave: A and B alternate in shared scope
// ===========================================================================

describe("cross-action interleave in shared scope", () => {
  it("alternating dispatches of A and B serialize correctly", async () => {
    const order: string[] = [];

    const actionA = defineAction<number, string>({
      name: "test.interleave_A",
      scope: "interleave",
      run: (n) => { order.push(`A-${n}`); return Promise.resolve(`A-${n}`); },
    });

    const actionB = defineAction<number, string>({
      name: "test.interleave_B",
      scope: "interleave",
      run: (n) => { order.push(`B-${n}`); return Promise.resolve(`B-${n}`); },
    });

    const p1 = actionA.dispatch(1);
    const p2 = actionB.dispatch(1);
    const p3 = actionA.dispatch(2);
    const p4 = actionB.dispatch(2);

    const [r1, r2, r3, r4] = await Promise.all([p1, p2, p3, p4]);

    expect(r1).toBe("A-1");
    expect(r2).toBe("B-1");
    expect(r3).toBe("A-2");
    expect(r4).toBe("B-2");
    expect(order).toEqual(["A-1", "B-1", "A-2", "B-2"]);
  });

  it("cancel one action does not affect other action in same scope", async () => {
    const order: string[] = [];

    const actionA = defineAction<void, string>({
      name: "test.interleave_iso_A",
      scope: "iso-interleave",
      error: false,
      run: (_args, signal) =>
        new Promise<string>((_resolve, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const actionB = defineAction<void, string>({
      name: "test.interleave_iso_B",
      scope: "iso-interleave",
      run: () => { order.push("B"); return Promise.resolve("B"); },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();

    // Cancel A — B should still run after A's slot resolves
    actionA.cancel();

    const [rA, rB] = await Promise.all([pA, pB]);
    expect(rA).toBeNull();
    expect(rB).toBe("B");
    expect(order).toEqual(["B"]);
  });
});
