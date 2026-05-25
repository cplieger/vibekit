// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { actionStatus, _resetForTest as resetActionStatus } from "./action-status.js";
import { ActionError } from "./error.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  resetActionStatus();
});

describe("actionStatus", () => {
  it("returns a snapshot with pending=0 for an unknown action", () => {
    const s = actionStatus("never.dispatched");
    expect(s.pending).toBe(0);
    expect(s.lastDispatchedAt).toBe(0);
    expect(s.lastSettledAt).toBe(0);
    expect(s.lastError).toBeUndefined();
    expect(s.lastSuccess).toBeUndefined();
  });

  it("increments pending on dispatch and decrements on success", async () => {
    const action = defineAction({ name: "test.status", run: async () => "ok" });
    const s = actionStatus("test.status");
    expect(s.pending).toBe(0);
    await action.dispatch({});
    expect(s.pending).toBe(0); // completed
    expect(s.lastSuccess).toBe("ok");
    expect(s.lastSettledAt).toBeGreaterThan(0);
  });

  it("tracks lastError on failure", async () => {
    const action = defineAction({
      name: "test.err",
      run: async () => { throw new ActionError("boom", { status: 500 }); },
    });
    const s = actionStatus("test.err");
    await action.dispatch({});
    expect(s.pending).toBe(0);
    expect(s.lastError?.message).toBe("boom");
    expect(s.lastError?.status).toBe(500);
  });

  it("pending count reflects multiple in-flight instances", async () => {
    let resolve1!: () => void;
    let resolve2!: () => void;
    const action = defineAction({
      name: "test.multi",
      run: () => new Promise<void>((r) => {
        if (!resolve1) resolve1 = r;
        else resolve2 = r;
      }),
    });
    const s = actionStatus("test.multi");
    const p1 = action.dispatch({});
    const p2 = action.dispatch({});
    expect(s.pending).toBe(2);
    resolve1();
    await p1;
    expect(s.pending).toBe(1);
    resolve2();
    await p2;
    expect(s.pending).toBe(0);
  });

  it("returns the same reference on repeated calls", () => {
    const s1 = actionStatus("test.ref");
    const s2 = actionStatus("test.ref");
    expect(s1).toBe(s2);
  });

  it("decrements pending on cancellation", async () => {
    const action = defineAction({
      name: "test.cancel_status",
      run: (_args, signal) =>
        new Promise<void>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    });
    const s = actionStatus("test.cancel_status");
    const p = action.dispatch({});
    expect(s.pending).toBe(1);
    action.cancel();
    await p;
    expect(s.pending).toBe(0);
    expect(s.lastSettledAt).toBeGreaterThan(0);
  });

  it("lastDispatchedAt updates on each dispatch", async () => {
    const action = defineAction({ name: "test.ts", run: async () => "x" });
    const s = actionStatus("test.ts");
    expect(s.lastDispatchedAt).toBe(0);
    await action.dispatch({});
    expect(s.lastDispatchedAt).toBeGreaterThan(0);
  });

  it("seeds pending count from registry when called after dispatch is in-flight", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "test.late_subscribe",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    // Dispatch BEFORE calling actionStatus — the listener isn't installed yet
    const p = action.dispatch({});
    // Now call actionStatus — should see pending=1 from registry seed
    const s = actionStatus("test.late_subscribe");
    expect(s.pending).toBe(1);
    resolve();
    await p;
    expect(s.pending).toBe(0);
  });

  it("seeds lastDispatchedAt from pending instances", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "test.late_ts",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const p = action.dispatch({});
    const s = actionStatus("test.late_ts");
    // lastDispatchedAt should be seeded from the pending instance
    expect(s.lastDispatchedAt).toBeGreaterThan(0);
    resolve();
    await p;
  });

  it("does not double-count pending when actionStatus is called from within a listener", async () => {
    // Simulate: another listener calls actionStatus during a pending notification
    let resolve!: () => void;
    const action = defineAction({
      name: "test.double_count",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    // First, install actionStatus for a different name to get the global listener installed
    actionStatus("test.other_name");
    // Now dispatch — the global listener fires but snapshots.get("test.double_count") is undefined
    const p = action.dispatch({});
    // Now call actionStatus mid-flight — seeds from pendingFor
    const s = actionStatus("test.double_count");
    expect(s.pending).toBe(1); // not 2
    resolve();
    await p;
    expect(s.pending).toBe(0);
  });
});
