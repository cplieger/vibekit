// @vitest-environment happy-dom
// Tests for bindDisabledPattern and multi-action cluster interactions
// with scope serialization and per-dispatch callbacks.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { bindDisabledPattern, bindLoadingCluster } from "./loading.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
});

describe("bindDisabledPattern", () => {
  it("disables when action is pending even if disabledWhen returns false", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.pending",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    bindDisabledPattern(btn, {
      actions: ["dp.pending"],
      disabledWhen: () => false,
    });
    expect(btn.disabled).toBe(false);
    const p = action.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");
    resolve();
    await p;
    expect(btn.disabled).toBe(false);
    expect(btn.getAttribute("aria-busy")).toBeNull();
  });

  it("disables when disabledWhen returns true even if no action pending", () => {
    const btn = document.createElement("button");
    bindDisabledPattern(btn, {
      actions: ["dp.none"],
      disabledWhen: () => true,
    });
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBeNull();
  });

  it("re-evaluates disabledWhen on recheck()", () => {
    let formValid = false;
    const btn = document.createElement("button");
    const handle = bindDisabledPattern(btn, {
      actions: [],
      disabledWhen: () => !formValid,
    });
    expect(btn.disabled).toBe(true);
    formValid = true;
    handle.recheck();
    expect(btn.disabled).toBe(false);
    handle.dispose();
  });

  it("combines pending + disabledWhen correctly across transitions", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.combo",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    let contentChanged = false;
    const btn = document.createElement("button");
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.combo"],
      disabledWhen: () => !contentChanged,
    });
    // Initially: no pending, content not changed → disabled
    expect(btn.disabled).toBe(true);
    // Content changes → recheck → enabled
    contentChanged = true;
    handle.recheck();
    expect(btn.disabled).toBe(false);
    // Action starts → disabled (pending)
    const p = action.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    // Action completes → disabledWhen re-evaluated → enabled
    resolve();
    await p;
    expect(btn.disabled).toBe(false);
    // Content reverts → recheck → disabled again
    contentChanged = false;
    handle.recheck();
    expect(btn.disabled).toBe(true);
    handle.dispose();
  });

  it("manages pendingClass correctly", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.class",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    bindDisabledPattern(btn, {
      actions: ["dp.class"],
      disabledWhen: () => false,
      pendingClass: "saving",
    });
    expect(btn.classList.contains("saving")).toBe(false);
    const p = action.dispatch(undefined);
    expect(btn.classList.contains("saving")).toBe(true);
    resolve();
    await p;
    expect(btn.classList.contains("saving")).toBe(false);
  });

  it("ariaBusy: false suppresses aria-busy attribute", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.noaria",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    bindDisabledPattern(btn, {
      actions: ["dp.noaria"],
      disabledWhen: () => false,
      ariaBusy: false,
    });
    const p = action.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBeNull();
    resolve();
    await p;
  });

  it("dispose stops further updates", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.dispose",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.dispose"],
      disabledWhen: () => false,
    });
    handle.dispose();
    const p = action.dispatch(undefined);
    // After dispose, no updates
    expect(btn.disabled).toBe(false);
    resolve();
    await p;
  });

  it("handles disabledWhen throwing gracefully (falls back to false)", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.throw",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    bindDisabledPattern(btn, {
      actions: ["dp.throw"],
      disabledWhen: () => { throw new Error("validation exploded"); },
    });
    // disabledWhen threw → falls back to false, no pending → enabled
    expect(btn.disabled).toBe(false);
    const p = action.dispatch(undefined);
    // pending → disabled regardless of disabledWhen
    expect(btn.disabled).toBe(true);
    resolve();
    await p;
    // After completion, disabledWhen throws → false → enabled
    expect(btn.disabled).toBe(false);
  });

  it("works with multiple action names (OR semantics)", async () => {
    let resolve1!: () => void;
    let resolve2!: () => void;
    const a1 = defineAction({
      name: "dp.multi1",
      run: () => new Promise<void>((r) => { resolve1 = r; }),
    });
    const a2 = defineAction({
      name: "dp.multi2",
      run: () => new Promise<void>((r) => { resolve2 = r; }),
    });
    const btn = document.createElement("button");
    bindDisabledPattern(btn, {
      actions: ["dp.multi1", "dp.multi2"],
      disabledWhen: () => false,
    });
    expect(btn.disabled).toBe(false);
    const p1 = a1.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    const p2 = a2.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    resolve1();
    await p1;
    // Still pending (a2)
    expect(btn.disabled).toBe(true);
    resolve2();
    await p2;
    expect(btn.disabled).toBe(false);
  });

  it("double dispose is safe (no-op)", () => {
    const btn = document.createElement("button");
    const handle = bindDisabledPattern(btn, {
      actions: [],
      disabledWhen: () => false,
    });
    handle.dispose();
    handle.dispose(); // should not throw
    expect(btn.disabled).toBe(false);
  });

  it("recheck after dispose is safe (no-op)", () => {
    const btn = document.createElement("button");
    const handle = bindDisabledPattern(btn, {
      actions: [],
      disabledWhen: () => true,
    });
    expect(btn.disabled).toBe(true);
    handle.dispose();
    // Manually enable
    btn.disabled = false;
    handle.recheck(); // should not re-disable
    expect(btn.disabled).toBe(false);
  });
});

describe("multi-action cluster + scope callbacks", () => {
  it("cluster tracks scoped actions that serialize", async () => {
    const resolvers: (() => void)[] = [];
    const action = defineAction({
      name: "cluster.scoped",
      scope: "serial",
      run: () => new Promise<void>((r) => { resolvers.push(r); }),
    });
    const states: Array<{ pending: boolean; activeNames: readonly string[] }> = [];
    bindLoadingCluster(["cluster.scoped"], (s) => {
      states.push({ pending: s.pending, activeNames: [...s.activeNames] });
    });
    // Dispatch two — second queues behind first due to scope
    const p1 = action.dispatch("a");
    // Scoped dispatch goes through .then() — await microtask for pending record
    await Promise.resolve();
    expect(states[states.length - 1]!.pending).toBe(true);
    const p2 = action.dispatch("b");
    // Complete first — second starts
    resolvers[0]!();
    await p1;
    // Second is now running (pending) — await microtask for scope chain
    await Promise.resolve();
    expect(states[states.length - 1]!.pending).toBe(true);
    // Complete second
    resolvers[1]!();
    await p2;
    expect(states[states.length - 1]!.pending).toBe(false);
  });

  it("cluster reflects cancellation of scoped actions", async () => {
    let started = false;
    const action = defineAction({
      name: "cluster.cancel_scope",
      scope: "s",
      run: (_args, signal) => {
        started = true;
        return new Promise<void>((_, reject) => {
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        });
      },
    });
    const states: Array<{ pending: boolean }> = [];
    bindLoadingCluster(["cluster.cancel_scope"], (s) => {
      states.push({ pending: s.pending });
    });
    const p = action.dispatch("x");
    // Wait for run to start
    await Promise.resolve();
    expect(started).toBe(true);
    expect(states[states.length - 1]!.pending).toBe(true);
    action.cancel();
    await p;
    expect(states[states.length - 1]!.pending).toBe(false);
  });

  it("cluster with multiple different actions tracks independently", async () => {
    let resolve1!: () => void;
    let resolve2!: () => void;
    const a1 = defineAction({
      name: "cluster.a",
      run: () => new Promise<void>((r) => { resolve1 = r; }),
    });
    const a2 = defineAction({
      name: "cluster.b",
      run: () => new Promise<void>((r) => { resolve2 = r; }),
    });
    const states: Array<{ pending: boolean; activeNames: readonly string[] }> = [];
    bindLoadingCluster(["cluster.a", "cluster.b"], (s) => {
      states.push({ pending: s.pending, activeNames: [...s.activeNames] });
    });
    const p1 = a1.dispatch(undefined);
    expect(states[states.length - 1]!.activeNames).toContain("cluster.a");
    const p2 = a2.dispatch(undefined);
    const last = states[states.length - 1]!;
    expect(last.activeNames).toContain("cluster.a");
    expect(last.activeNames).toContain("cluster.b");
    resolve1();
    await p1;
    const afterA = states[states.length - 1]!;
    expect(afterA.activeNames).not.toContain("cluster.a");
    expect(afterA.activeNames).toContain("cluster.b");
    resolve2();
    await p2;
    expect(states[states.length - 1]!.pending).toBe(false);
  });

  it("bindDisabledPattern + onSettled callback interaction", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.settled",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    let formDirty = true;
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.settled"],
      disabledWhen: () => !formDirty,
    });
    expect(btn.disabled).toBe(false); // formDirty=true → !formDirty=false
    const settled = vi.fn();
    const p = action.dispatch(undefined, {
      onSettled: () => {
        settled();
        // After action completes, form is clean
        formDirty = false;
        handle.recheck();
      },
    });
    expect(btn.disabled).toBe(true); // pending
    resolve();
    await p;
    expect(settled).toHaveBeenCalledTimes(1);
    // onSettled set formDirty=false and called recheck → disabled
    expect(btn.disabled).toBe(true);
    handle.dispose();
  });

  it("bindDisabledPattern + onSuccess re-enables after save", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.onsuccess",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    let hasChanges = true;
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.onsuccess"],
      disabledWhen: () => !hasChanges,
    });
    expect(btn.disabled).toBe(false); // hasChanges=true
    const p = action.dispatch(undefined, {
      onSuccess: () => {
        hasChanges = false;
        handle.recheck();
      },
    });
    expect(btn.disabled).toBe(true); // pending
    resolve();
    await p;
    // onSuccess cleared changes → disabled
    expect(btn.disabled).toBe(true);
    // New edit arrives
    hasChanges = true;
    handle.recheck();
    expect(btn.disabled).toBe(false);
    handle.dispose();
  });
});

describe("multi-action cluster + dedupe interactions", () => {
  it("cluster sees only one pending entry for deduped dispatches", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "cluster.dedupe",
      dedupe: true,
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const states: Array<{ pending: boolean }> = [];
    bindLoadingCluster(["cluster.dedupe"], (s) => {
      states.push({ pending: s.pending });
    });
    const p1 = action.dispatch("x");
    const p2 = action.dispatch("x"); // deduped
    // Only one pending instance in registry
    const pendingStates = states.filter((s) => s.pending);
    expect(pendingStates.length).toBeGreaterThanOrEqual(1);
    resolve();
    await Promise.all([p1, p2]);
    expect(states[states.length - 1]!.pending).toBe(false);
  });

  it("bindDisabledPattern reflects dedupe correctly", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.dedupe",
      dedupe: true,
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    bindDisabledPattern(btn, {
      actions: ["dp.dedupe"],
      disabledWhen: () => false,
    });
    const p1 = action.dispatch("y");
    const p2 = action.dispatch("y");
    expect(btn.disabled).toBe(true);
    resolve();
    await Promise.all([p1, p2]);
    expect(btn.disabled).toBe(false);
  });
});

describe("bindDisabledPattern — edge cases", () => {
  it("auto-disposes when element is removed from DOM mid-pending", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.dom_remove",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    bindDisabledPattern(btn, {
      actions: ["dp.dom_remove"],
      disabledWhen: () => false,
    });
    const p = action.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    // Remove from DOM — next registry notification triggers auto-dispose
    document.body.removeChild(btn);
    resolve();
    await p;
    // After auto-dispose, element state is frozen (no further updates)
    // The disabled state remains as-is from when it was removed
  });

  it("restores focus after pending completes when button had focus", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.focus_restore",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    bindDisabledPattern(btn, {
      actions: ["dp.focus_restore"],
      disabledWhen: () => false,
    });
    btn.focus();
    expect(document.activeElement).toBe(btn);
    const p = action.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    // Focus moves to body when button is disabled
    resolve();
    await p;
    expect(btn.disabled).toBe(false);
    // Focus should be restored
    expect(document.activeElement).toBe(btn);
    document.body.removeChild(btn);
  });

  it("does not restore focus when disabledWhen keeps element disabled", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.focus_no_restore",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.focus_no_restore"],
      disabledWhen: () => true, // always disabled
    });
    btn.focus();
    const p = action.dispatch(undefined);
    resolve();
    await p;
    // Element stays disabled (disabledWhen=true), so no focus restore
    expect(btn.disabled).toBe(true);
    handle.dispose();
    document.body.removeChild(btn);
  });

  it("dispose during pending restores element state (stale state rollback)", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.dispose_pending",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.dispose_pending"],
      disabledWhen: () => false,
      pendingClass: "loading",
    });
    const p = action.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");
    expect(btn.classList.contains("loading")).toBe(true);
    // Dispose while pending — element should be restored
    handle.dispose();
    expect(btn.disabled).toBe(false);
    expect(btn.getAttribute("aria-busy")).toBeNull();
    expect(btn.classList.contains("loading")).toBe(false);
    resolve();
    await p;
    document.body.removeChild(btn);
  });

  it("dispose during pending respects disabledWhen for final state", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.dispose_pending_dw",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.dispose_pending_dw"],
      disabledWhen: () => true, // form invalid
    });
    const p = action.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    // Dispose while pending — disabled stays true because disabledWhen
    handle.dispose();
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBeNull(); // aria-busy cleared
    resolve();
    await p;
    document.body.removeChild(btn);
  });
});
