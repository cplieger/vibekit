// @vitest-environment happy-dom
// Race condition test: cancel + immediate re-dispatch with dedupe
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

describe("dedupe + cancel + immediate re-dispatch race", () => {
  it("re-dispatch after cancel should start a fresh run, not collapse onto cancelled", async () => {
    let runCount = 0;
    const action = defineAction<string, string>({
      name: "test.dedupe_cancel_redispatch",
      dedupe: true,
      error: false,
      run: (_args, signal) => {
        runCount++;
        const myRun = runCount;
        return new Promise<string>((resolve, reject) => {
          if (signal.aborted) {
            reject(new DOMException("aborted", "AbortError"));
            return;
          }
          signal.addEventListener("abort", () => { reject(new DOMException("aborted", "AbortError")); });
          setTimeout(() => { resolve(`result-${myRun}`); }, 10);
        });
      },
    });

    // Dispatch #1
    const p1 = action.dispatch("x");

    // Cancel immediately (synchronous)
    action.cancel();

    // Re-dispatch with same args in same synchronous block
    const p2 = action.dispatch("x");

    const [r1, r2] = await Promise.all([p1, p2]);

    expect(r1).toBeNull(); // cancelled
    // If this fails with r2 === null, the re-dispatch collapsed onto the cancelled promise
    expect(r2).toBe("result-2"); // should start fresh
    expect(runCount).toBe(2);
  });

  it("re-dispatch after cancel with scope should start fresh", async () => {
    let runCount = 0;
    const action = defineAction<string, string>({
      name: "test.scope_dedupe_cancel_redispatch",
      dedupe: true,
      scope: "s",
      error: false,
      run: (_args, signal) => {
        runCount++;
        const myRun = runCount;
        return new Promise<string>((resolve, reject) => {
          if (signal.aborted) {
            reject(new DOMException("aborted", "AbortError"));
            return;
          }
          signal.addEventListener("abort", () => { reject(new DOMException("aborted", "AbortError")); });
          setTimeout(() => { resolve(`result-${myRun}`); }, 10);
        });
      },
    });

    const p1 = action.dispatch("x");
    action.cancel();
    const p2 = action.dispatch("x");

    const [r1, r2] = await Promise.all([p1, p2]);

    expect(r1).toBeNull();
    // run() is only called once (for p2) since p1 was cancelled before
    // entering run(). So runCount=1 → "result-1".
    expect(r2).toBe("result-1");
    expect(runCount).toBe(1);
  });

  it("second cancel after re-dispatch still works correctly", async () => {
    let runCount = 0;
    const action = defineAction<string, string>({
      name: "test.dedupe_double_cancel",
      dedupe: true,
      error: false,
      run: (_args, signal) => {
        runCount++;
        return new Promise<string>((resolve, reject) => {
          if (signal.aborted) {
            reject(new DOMException("aborted", "AbortError"));
            return;
          }
          signal.addEventListener("abort", () => { reject(new DOMException("aborted", "AbortError")); });
          setTimeout(() => { resolve(`result-${runCount}`); }, 10);
        });
      },
    });

    // First dispatch + cancel
    const p1 = action.dispatch("x");
    action.cancel();
    await p1;

    // Let .finally() cleanup run
    await Promise.resolve();
    await Promise.resolve();

    // Second dispatch + cancel — should still work
    const p2 = action.dispatch("x");
    action.cancel();
    const r2 = await p2;
    expect(r2).toBeNull();

    // Third dispatch — should start fresh
    const p3 = action.dispatch("x");
    const r3 = await p3;
    expect(r3).toBe(`result-${runCount}`);
    expect(runCount).toBe(3); // 1st cancelled mid-run, 2nd cancelled mid-run, 3rd succeeds
  });
});
