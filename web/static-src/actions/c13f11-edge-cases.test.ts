// @vitest-environment happy-dom
// C13F11 edge cases: cancel marks dedupe cancelled before delete,
// bindDisabledPattern dispose restore (focus + state).
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { bindDisabledPattern } from "./loading.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

describe("cancel marks dedupe cancelled before delete — edge cases", () => {
  it("deduped caller's onCancel fires even when cancel() clears the map first", async () => {
    const action = defineAction<string, string>({
      name: "test.dedupe_cancel_order",
      dedupe: true,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const onCancel1 = vi.fn();
    const onCancel2 = vi.fn();
    const onError2 = vi.fn();

    const p1 = action.dispatch("x", { onCancel: onCancel1 });
    const p2 = action.dispatch("x", { onCancel: onCancel2, onError: onError2 });

    action.cancel();
    await Promise.all([p1, p2]);

    expect(onCancel1).toHaveBeenCalledTimes(1);
    expect(onCancel2).toHaveBeenCalledTimes(1);
    expect(onError2).not.toHaveBeenCalled();
  });

  it("re-dispatch after cancel with same dedupe key does not see stale cancelled flag", async () => {
    let runCount = 0;
    const action = defineAction<string, string>({
      name: "test.dedupe_cancel_redispatch_fresh",
      dedupe: true,
      run: async (_args, signal) => {
        runCount++;
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return `run-${runCount}`;
      },
    });

    const p1 = action.dispatch("a");
    action.cancel();
    await p1;

    // Re-dispatch with same key — should start fresh, not see cancelled
    const result = await action.dispatch("a");
    expect(result).toBe("run-2");
    expect(runCount).toBe(2);
  });

  it("multiple deduped callers all receive onCancel when original is cancelled", async () => {
    const action = defineAction<string, string>({
      name: "test.dedupe_multi_cancel",
      dedupe: true,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const cancels = [vi.fn(), vi.fn(), vi.fn()];
    const p1 = action.dispatch("k", { onCancel: cancels[0]! });
    const p2 = action.dispatch("k", { onCancel: cancels[1]! });
    const p3 = action.dispatch("k", { onCancel: cancels[2]! });

    action.cancel();
    await Promise.all([p1, p2, p3]);

    for (const fn of cancels) {
      expect(fn).toHaveBeenCalledTimes(1);
    }
  });

  it("cancel after dedupe entry promise resolves (race): no stale cancelled flag", async () => {
    let resolve!: (v: string) => void;
    const action = defineAction<string, string>({
      name: "test.dedupe_cancel_after_resolve",
      dedupe: true,
      run: () => new Promise<string>((r) => { resolve = r; }),
    });

    const onSuccess = vi.fn();
    const onCancel = vi.fn();
    const p1 = action.dispatch("z", { onSuccess, onCancel });
    const p2 = action.dispatch("z", { onSuccess, onCancel });

    // Resolve before cancel — success path wins
    resolve("done");
    await Promise.all([p1, p2]);

    // cancel() after settlement is a no-op (inFlight already empty)
    action.cancel();

    expect(onSuccess).toHaveBeenCalledTimes(2);
    expect(onCancel).not.toHaveBeenCalled();
  });
});

describe("bindDisabledPattern dispose restore — edge cases", () => {
  it("dispose mid-pending restores focus when button had focus", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.dispose_focus",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.dispose_focus"],
      disabledWhen: () => false,
    });
    btn.focus();
    expect(document.activeElement).toBe(btn);
    const p = action.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    // Dispose mid-pending — should restore focus
    handle.dispose();
    expect(btn.disabled).toBe(false);
    expect(document.activeElement).toBe(btn);
    resolve();
    await p;
    document.body.removeChild(btn);
  });

  it("dispose mid-pending does NOT restore focus when disabledWhen keeps it disabled", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.dispose_focus_disabled",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.dispose_focus_disabled"],
      disabledWhen: () => true, // always disabled
    });
    btn.focus();
    const p = action.dispatch(undefined);
    // Dispose mid-pending — disabledWhen=true so no focus restore
    handle.dispose();
    expect(btn.disabled).toBe(true);
    resolve();
    await p;
    document.body.removeChild(btn);
  });

  it("dispose when not pending does not alter element state", () => {
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    btn.disabled = false;
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.idle_dispose"],
      disabledWhen: () => false,
    });
    handle.dispose();
    expect(btn.disabled).toBe(false);
    expect(btn.getAttribute("aria-busy")).toBeNull();
    document.body.removeChild(btn);
  });

  it("dispose mid-pending clears pendingClass and aria-busy", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.dispose_class_aria",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.dispose_class_aria"],
      disabledWhen: () => false,
      pendingClass: "spin",
      ariaBusy: true,
    });
    const p = action.dispatch(undefined);
    expect(btn.classList.contains("spin")).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");
    handle.dispose();
    expect(btn.classList.contains("spin")).toBe(false);
    expect(btn.getAttribute("aria-busy")).toBeNull();
    expect(btn.disabled).toBe(false);
    resolve();
    await p;
    document.body.removeChild(btn);
  });

  it("dispose mid-pending when disabledWhen throws falls back to false", async () => {
    let resolve!: () => void;
    let shouldThrow = false;
    const action = defineAction({
      name: "dp.dispose_throw",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.dispose_throw"],
      disabledWhen: () => { if (shouldThrow) throw new Error("boom"); return false; },
    });
    const p = action.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    shouldThrow = true;
    // dispose should not throw even if disabledWhen throws
    handle.dispose();
    expect(btn.disabled).toBe(false); // fallback to false
    resolve();
    await p;
    document.body.removeChild(btn);
  });

  it("action completing after dispose does not re-disable element", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "dp.complete_after_dispose",
      run: () => new Promise<void>((r) => { resolve = r; }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    const handle = bindDisabledPattern(btn, {
      actions: ["dp.complete_after_dispose"],
      disabledWhen: () => false,
    });
    const p = action.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    handle.dispose();
    expect(btn.disabled).toBe(false);
    // Action completes after dispose — should not touch element
    resolve();
    await p;
    expect(btn.disabled).toBe(false);
  });
});
