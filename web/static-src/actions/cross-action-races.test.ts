// @vitest-environment happy-dom
// Cycle 12 Stage 1 Batch 2: Cross-action race conditions.
// Validates that isInflight reflects cancellation immediately for
// scope-queued dispatches, and that cancel() during scope-queue
// does not leave stale state.
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
// 1. isInflight race: cancel scope-queued action reflects immediately
// ===========================================================================

describe("isInflight reflects cancellation of scope-queued dispatches", () => {
  it("isInflight becomes false immediately after cancel() for scope-queued action", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.inflight_race_A",
      scope: "inflight-race",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.inflight_race_B",
      scope: "inflight-race",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B-done");
      },
    });

    // A starts running, B queues behind A in the scope chain
    const pA = actionA.dispatch();
    await Promise.resolve(); // let A's runOnce start
    const pB = actionB.dispatch();

    // B is scope-queued (A hasn't resolved yet)
    expect(actionB.isInflight).toBe(true);

    // Cancel B while it's still queued
    actionB.cancel();

    // isInflight should reflect cancellation IMMEDIATELY
    expect(actionB.isInflight).toBe(false);

    // Resolve A so the scope chain advances
    resolveA!("A-done");
    const [rA, rB] = await Promise.all([pA, pB]);

    expect(rA).toBe("A-done");
    expect(rB).toBeNull(); // B was cancelled
  });

  it("cancel of scope-queued action does not affect running action in same scope", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.no_cross_cancel_A",
      scope: "no-cross",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.no_cross_cancel_B",
      scope: "no-cross",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();

    // Cancel B — should NOT affect A
    actionB.cancel();
    expect(actionA.isInflight).toBe(true);

    resolveA!("A-ok");
    const rA = await pA;
    const rB = await pB;

    expect(rA).toBe("A-ok");
    expect(rB).toBeNull();
  });

  it("multiple scope-queued actions: cancel middle, isInflight correct for all", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.multi_queue_A",
      scope: "multi-q",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.multi_queue_B",
      scope: "multi-q",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.multi_queue_C",
      scope: "multi-q",
      run: () => Promise.resolve("C"),
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    const pC = actionC.dispatch();

    expect(actionA.isInflight).toBe(true);
    expect(actionB.isInflight).toBe(true);
    expect(actionC.isInflight).toBe(true);

    // Cancel B (middle of queue)
    actionB.cancel();

    expect(actionA.isInflight).toBe(true);  // still running
    expect(actionB.isInflight).toBe(false); // eagerly cleared
    expect(actionC.isInflight).toBe(true);  // still queued (not cancelled)

    resolveA!("A");
    const [rA, rB, rC] = await Promise.all([pA, pB, pC]);

    expect(rA).toBe("A");
    expect(rB).toBeNull();
    expect(rC).toBe("C");
  });
});

// ===========================================================================
// 2. Scope chain integrity after eager cancel cleanup
// ===========================================================================

describe("scope chain integrity after eager cancel", () => {
  it("scope chain drains correctly after cancelled-before-start dispatch", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.drain_A",
      scope: "drain",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.drain_B",
      scope: "drain",
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
    await Promise.resolve();
    await Promise.resolve();

    const { scopeChains } = _internalsForTest();
    expect(scopeChains).toBe(0);
  });

  it("action dispatched after cancel into same scope runs correctly", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.post_cancel_A",
      scope: "post-cancel",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.post_cancel_B",
      scope: "post-cancel",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.post_cancel_C",
      scope: "post-cancel",
      run: () => Promise.resolve("C"),
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    actionB.cancel();

    // Dispatch C AFTER B was cancelled — should still queue correctly
    const pC = actionC.dispatch();

    resolveA!("A");
    const [rA, rB, rC] = await Promise.all([pA, pB, pC]);

    expect(rA).toBe("A");
    expect(rB).toBeNull();
    expect(rC).toBe("C");
  });
});

// ===========================================================================
// 3. Registry records correct status for eagerly-cancelled scope-queued
// ===========================================================================

describe("registry records for eagerly-cancelled scope-queued dispatches", () => {
  it("cancelled scope-queued dispatch records status=cancelled in registry", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.reg_eager_A",
      scope: "reg-eager",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.reg_eager_B",
      scope: "reg-eager",
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
    const bEntry = log.find((e) => e.name === "test.reg_eager_B");
    expect(bEntry).toBeDefined();
    expect(bEntry!.status).toBe("cancelled");
  });

  it("onSettled still fires for eagerly-cancelled scope-queued dispatch", async () => {
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.settled_eager_A",
      scope: "settled-eager",
      run: () => new Promise<string>((r) => { resolveA = r; }),
    });

    const actionB = defineAction<void, string>({
      name: "test.settled_eager_B",
      scope: "settled-eager",
      error: false,
      run: (_args, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const onSettled = vi.fn();
    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch(undefined, { onSettled });
    actionB.cancel();

    resolveA!("A");
    await Promise.all([pA, pB]);

    expect(onSettled).toHaveBeenCalledTimes(1);
  });
});

// ===========================================================================
// 4. Rapid cancel + re-dispatch: isInflight transitions correctly
// ===========================================================================

describe("rapid cancel + re-dispatch isInflight transitions", () => {
  it("re-dispatch after cancel shows isInflight=true again", async () => {
    let resolveFirst!: (v: string) => void;

    const action = defineAction<number, string>({
      name: "test.rapid_inflight",
      scope: "rapid-inflight",
      error: false,
      run: (n, signal) => {
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        if (n === 1) return new Promise<string>((r) => { resolveFirst = r; });
        return Promise.resolve(`done-${String(n)}`);
      },
    });

    const p1 = action.dispatch(1);
    await Promise.resolve();
    expect(action.isInflight).toBe(true);

    action.cancel();
    // After cancel, isInflight should be false (action was running,
    // but cancel aborts it — the finally block runs on next microtask)
    // Note: for a RUNNING action, isInflight stays true until runOnce's
    // finally block fires (it's in the started set). This is correct
    // because the action is still settling.
    resolveFirst!("cancelled-but-resolved");
    await p1;
    expect(action.isInflight).toBe(false);

    // Re-dispatch
    const p2 = action.dispatch(2);
    expect(action.isInflight).toBe(true);
    await p2;
    expect(action.isInflight).toBe(false);
  });
});
