// @vitest-environment happy-dom
// Cancel handling edge cases — Batch 2 fix verification:
// Verifies that evictDedupeSlot properly cleans activeDedupeKeys,
// preventing memory leaks when scope-queued deduped dispatches are cancelled.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine, _internalsForTest } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

describe("scope + dedupe cancel: no activeDedupes leak", () => {
  it("cancelling a scope-queued deduped dispatch cleans activeDedupes", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const blocker = defineAction<void, string>({
      name: "test.dedupe_scope_leak_blocker",
      scope: "ds",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<string, string>({
      name: "test.dedupe_scope_leak",
      scope: "ds",
      dedupe: true,
      run: async (arg) => `ok-${arg}`,
    });

    const pBlock = blocker.dispatch();
    const p = action.dispatch("x");

    // Cancel while scope-queued — early-cancel path fires
    action.cancel();
    resolve1();
    await pBlock;
    await p;
    // Allow scope chain .finally() to fire
    await Promise.resolve();

    // activeDedupes should be fully cleaned (no leak)
    const internals = _internalsForTest();
    expect(internals.activeDedupes).toBe(0);
  });

  it("re-dispatch after cancel of scope-queued deduped action starts fresh", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });

    const blocker = defineAction<void, string>({
      name: "test.dedupe_scope_redispatch_blocker",
      scope: "ds2",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<string, string>({
      name: "test.dedupe_scope_redispatch",
      scope: "ds2",
      dedupe: true,
      run: async (arg) => `result-${arg}`,
    });

    const pBlock = blocker.dispatch();
    const p1 = action.dispatch("a");

    action.cancel();
    resolve1();
    await pBlock;
    await p1;

    // Re-dispatch should start a fresh run (not collapse onto cancelled)
    const r2 = await action.dispatch("a");
    expect(r2).toBe("result-a");
  });

  it("deduped caller of scope-queued cancelled dispatch gets onCancel", async () => {
    let resolve1!: () => void;
    const gate = new Promise<void>((r) => { resolve1 = r; });
    const onCancel1 = vi.fn();
    const onCancel2 = vi.fn();

    const blocker = defineAction<void, string>({
      name: "test.dedupe_scope_cb_blocker",
      scope: "ds3",
      run: async () => { await gate; return "block"; },
    });

    const action = defineAction<string, string>({
      name: "test.dedupe_scope_cb",
      scope: "ds3",
      dedupe: true,
      run: async (arg) => `ok-${arg}`,
    });

    const pBlock = blocker.dispatch();
    const p1 = action.dispatch("z", { onCancel: onCancel1 });
    const p2 = action.dispatch("z", { onCancel: onCancel2 });

    action.cancel();
    resolve1();
    await pBlock;
    await Promise.all([p1, p2]);

    expect(onCancel1).toHaveBeenCalledTimes(1);
    expect(onCancel2).toHaveBeenCalledTimes(1);
  });
});

describe("cancel + evictDedupeSlot consistency after success", () => {
  it("evictDedupeSlot on success path leaves no stale activeDedupes", async () => {
    const action = defineAction<string, string>({
      name: "test.evict_success_clean",
      dedupe: true,
      run: async (arg) => `ok-${arg}`,
    });

    await action.dispatch("x");

    const internals = _internalsForTest();
    expect(internals.activeDedupes).toBe(0);
  });

  it("evictDedupeSlot on error path leaves no stale activeDedupes", async () => {
    const action = defineAction<string, string>({
      name: "test.evict_error_clean",
      dedupe: true,
      error: false,
      run: async () => { throw new Error("fail"); },
    });

    await action.dispatch("x");

    const internals = _internalsForTest();
    expect(internals.activeDedupes).toBe(0);
  });

  it("evictDedupeSlot on cancel path leaves no stale activeDedupes", async () => {
    const action = defineAction<string, string>({
      name: "test.evict_cancel_clean",
      dedupe: true,
      run: (_args, signal) =>
        new Promise<string>((_, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    });

    const p = action.dispatch("x");
    action.cancel();
    await p;

    const internals = _internalsForTest();
    expect(internals.activeDedupes).toBe(0);
  });
});
