// Cycle 16 Stage 1: safeStringify bigint/symbol fix, missing primitive
// exports (subscribeByName, pendingFor, RetryConfig, ActionLifecycleStatus,
// RegistryListener), and optimisticRan compile error fix.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, subscribeByName, pendingFor } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { ActionError } from "./error.js";
import type { RetryConfig, ActionLifecycleStatus, RegistryListener } from "./types.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
});

describe("safeStringify — bigint and symbol dedupe keys", () => {
  it("two different bigint args produce different dedupe keys", async () => {
    let runCount = 0;
    const action = defineAction<bigint, string>({
      name: "test.bigint_dedupe",
      dedupe: true,
      run: async (args) => {
        runCount++;
        return `result-${args}`;
      },
    });
    const [r1, r2] = await Promise.all([
      action.dispatch(1n),
      action.dispatch(2n),
    ]);
    // Different bigint args → different dedupe keys → both run
    expect(runCount).toBe(2);
    expect(r1).toBe("result-1");
    expect(r2).toBe("result-2");
  });

  it("same bigint args collapse via dedupe", async () => {
    let runCount = 0;
    let resolve!: () => void;
    const gate = new Promise<void>((r) => { resolve = r; });
    const action = defineAction<bigint, string>({
      name: "test.bigint_dedupe_same",
      dedupe: true,
      run: async (args) => {
        runCount++;
        await gate;
        return `result-${args}`;
      },
    });
    const p1 = action.dispatch(42n);
    const p2 = action.dispatch(42n);
    resolve();
    const [r1, r2] = await Promise.all([p1, p2]);
    expect(runCount).toBe(1);
    expect(r1).toBe("result-42");
    expect(r2).toBe("result-42");
  });

  it("two different symbol args produce different dedupe keys", async () => {
    let runCount = 0;
    const sym1 = Symbol("a");
    const sym2 = Symbol("b");
    const action = defineAction<symbol, string>({
      name: "test.symbol_dedupe",
      dedupe: true,
      run: async (args) => {
        runCount++;
        return `result-${String(args)}`;
      },
    });
    const [r1, r2] = await Promise.all([
      action.dispatch(sym1),
      action.dispatch(sym2),
    ]);
    expect(runCount).toBe(2);
    expect(r1).toBe("result-Symbol(a)");
    expect(r2).toBe("result-Symbol(b)");
  });

  it("two symbols with SAME description produce different dedupe keys", async () => {
    let runCount = 0;
    const sym1 = Symbol("dup");
    const sym2 = Symbol("dup");
    const action = defineAction<symbol, string>({
      name: "test.symbol_same_desc",
      dedupe: true,
      run: async (args) => {
        runCount++;
        return `result-${String(args)}`;
      },
    });
    const [r1, r2] = await Promise.all([
      action.dispatch(sym1),
      action.dispatch(sym2),
    ]);
    // Same description but different symbols → different dedupe keys → both run
    expect(runCount).toBe(2);
    expect(r1).toBe("result-Symbol(dup)");
    expect(r2).toBe("result-Symbol(dup)");
  });
});

describe("subscribeByName — exported primitive", () => {
  it("fires only for matching action name", async () => {
    const events: string[] = [];
    const unsub = subscribeByName("test.sub_by_name", (inst) => {
      events.push(`${inst.name}:${inst.status}`);
    });
    const a = defineAction({ name: "test.sub_by_name", run: async () => "ok" });
    const b = defineAction({ name: "test.other", run: async () => "ok" });
    await a.dispatch(undefined);
    await b.dispatch(undefined);
    unsub();
    expect(events).toEqual([
      "test.sub_by_name:pending",
      "test.sub_by_name:success",
    ]);
  });
});

describe("pendingFor — exported primitive", () => {
  it("returns pending instances for a named action", async () => {
    let resolve!: () => void;
    const gate = new Promise<void>((r) => { resolve = r; });
    const action = defineAction({
      name: "test.pending_for",
      run: async () => { await gate; return "done"; },
    });
    const p = action.dispatch(undefined);
    const pending = pendingFor("test.pending_for");
    expect(pending.length).toBe(1);
    expect(pending[0]!.name).toBe("test.pending_for");
    expect(pending[0]!.status).toBe("pending");
    resolve();
    await p;
    expect(pendingFor("test.pending_for").length).toBe(0);
  });
});

describe("onRollback fires correctly after optimisticRan fix", () => {
  it("fires onRollback on error when optimistic is defined", async () => {
    const onRollback = vi.fn();
    const action = defineAction<string, string, string>({
      name: "test.rollback_fires",
      optimistic: () => "snapshot",
      rollback: () => {},
      error: false,
      run: async () => { throw new ActionError("fail", { status: 500 }); },
    });
    await action.dispatch("x", { onRollback });
    expect(onRollback).toHaveBeenCalledTimes(1);
    expect(onRollback.mock.calls[0]![0].message).toBe("fail");
  });

  it("fires onRollback on cancellation when optimistic is defined", async () => {
    const onRollback = vi.fn();
    const action = defineAction<string, string, string>({
      name: "test.rollback_cancel",
      optimistic: () => "snapshot",
      rollback: () => {},
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const p = action.dispatch("x", { onRollback });
    action.cancel();
    await p;
    expect(onRollback).toHaveBeenCalledTimes(1);
    expect(onRollback.mock.calls[0]![0].message).toBe("cancelled");
  });

  it("does NOT fire onRollback when no optimistic is defined", async () => {
    const onRollback = vi.fn();
    const action = defineAction<string, string>({
      name: "test.no_rollback_no_opt",
      rollback: () => {},
      error: false,
      run: async () => { throw new ActionError("fail", { status: 500 }); },
    });
    await action.dispatch("x", { onRollback });
    // rollback() fires (it's defined), but onRollback should NOT fire
    // because optimistic is not defined — there's nothing to "undo"
    // from the caller's perspective.
    expect(onRollback).not.toHaveBeenCalled();
  });
});

describe("type exports compile correctly", () => {
  it("RetryConfig type is usable", () => {
    const cfg: RetryConfig = { count: 3, delay: 200, factor: 1.5 };
    expect(cfg.count).toBe(3);
  });

  it("ActionLifecycleStatus type is usable", () => {
    const status: ActionLifecycleStatus = "success";
    expect(status).toBe("success");
  });

  it("RegistryListener type is usable", () => {
    const listener: RegistryListener = () => {};
    expect(typeof listener).toBe("function");
  });
});
