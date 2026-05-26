// @vitest-environment happy-dom
// Cycle 15 Stage 1 Batch 2: Cross-action scope serialization race fix.
// Validates that cancelling a scope-queued entry does NOT unblock
// subsequent entries before the running entry finishes.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
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

describe("cross-action scope serialization after cancel", () => {
  it("C waits for A when B is cancelled (middle entry)", async () => {
    const order: string[] = [];
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.serial_A",
      scope: "serial",
      run: () => {
        order.push("A-start");
        return new Promise<string>((r) => { resolveA = r; });
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.serial_B",
      scope: "serial",
      error: false,
      run: (_args, signal) => {
        order.push("B-start");
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const actionC = defineAction<void, string>({
      name: "test.serial_C",
      scope: "serial",
      run: () => {
        order.push("C-start");
        return Promise.resolve("C");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    expect(order).toEqual(["A-start"]);

    const pB = actionB.dispatch();
    const pC = actionC.dispatch();

    actionB.cancel();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    // C must NOT start before A finishes
    expect(order).toEqual(["A-start"]);

    resolveA!("A-done");
    const [rA, rB, rC] = await Promise.all([pA, pB, pC]);

    expect(rA).toBe("A-done");
    expect(rB).toBeNull();
    expect(rC).toBe("C");
    expect(order).toEqual(["A-start", "C-start"]);
  });

  it("D waits for A when B (last entry) is cancelled then D dispatches", async () => {
    const order: string[] = [];
    let resolveA!: (v: string) => void;

    const actionA = defineAction<void, string>({
      name: "test.last_A",
      scope: "last-cancel",
      run: () => {
        order.push("A-start");
        return new Promise<string>((r) => { resolveA = r; });
      },
    });

    const actionB = defineAction<void, string>({
      name: "test.last_B",
      scope: "last-cancel",
      error: false,
      run: (_args, signal) => {
        order.push("B-start");
        if (signal.aborted) throw new DOMException("aborted", "AbortError");
        return Promise.resolve("B");
      },
    });

    const actionD = defineAction<void, string>({
      name: "test.last_D",
      scope: "last-cancel",
      run: () => {
        order.push("D-start");
        return Promise.resolve("D");
      },
    });

    const pA = actionA.dispatch();
    await Promise.resolve();
    const pB = actionB.dispatch();
    actionB.cancel();

    // Dispatch D after B was cancelled
    const pD = actionD.dispatch();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    // D must NOT start before A finishes
    expect(order).toEqual(["A-start"]);

    resolveA!("A-done");
    const [rA, rB, rD] = await Promise.all([pA, pB, pD]);

    expect(rA).toBe("A-done");
    expect(rB).toBeNull();
    expect(rD).toBe("D");
    expect(order).toEqual(["A-start", "D-start"]);
  });
});
