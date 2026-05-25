import { describe, it, expect, vi, beforeEach } from "vitest";
vi.mock("../toast.js", () => ({ info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn() }));
import { defineAction, dispatchWithResult } from "./define.js";
import { ActionError, isActionError, toActionError } from "./error.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, pendingCount } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
beforeEach(() => { resetDefine(); resetRegistry(); resetCleanup(); });

describe("dispatchWithResult", () => {
  it("returns ok: true on success", async () => {
    const a = defineAction({ name: "t.dwr1", run: async () => "hi" });
    const r = await dispatchWithResult(a, undefined);
    expect(r.ok).toBe(true);
    if (r.ok) expect(r.value).toBe("hi");
  });
  it("returns ok: false on error", async () => {
    const a = defineAction({ name: "t.dwr2", error: false, run: async () => { throw new ActionError("x", { status: 500 }); } });
    const r = await dispatchWithResult(a, undefined);
    expect(r.ok).toBe(false);
    if (!r.ok) { expect(r.error.message).toBe("x"); expect(r.cancelled).toBe(false); }
  });
  it("returns cancelled on cancel", async () => {
    const a = defineAction({ name: "t.dwr3", run: (_x, s) => new Promise<string>((_, rej) => { s.addEventListener("abort", () => rej(new Error("a"))); }) });
    const p = dispatchWithResult(a, undefined);
    a.cancel();
    const r = await p;
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.cancelled).toBe(true);
  });
  it("fires user onSuccess", async () => {
    const a = defineAction({ name: "t.dwr4", run: async () => 1 });
    const cb = vi.fn();
    await dispatchWithResult(a, undefined, { onSuccess: cb });
    expect(cb).toHaveBeenCalledWith(1, undefined);
  });
});

describe("isActionError", () => {
  it("true for ActionError", () => { expect(isActionError(new ActionError("x"))).toBe(true); });
  it("false for Error", () => { expect(isActionError(new Error("x"))).toBe(false); });
  it("false for null", () => { expect(isActionError(null)).toBe(false); });
});

describe("pendingCount", () => {
  it("tracks pending", async () => {
    let r!: () => void;
    const g = new Promise<void>(res => { r = res; });
    const a = defineAction({ name: "t.pc", run: async () => { await g; return 1; } });
    expect(pendingCount()).toBe(0);
    const p = a.dispatch(undefined);
    expect(pendingCount()).toBe(1);
    r();
    await p;
    expect(pendingCount()).toBe(0);
  });
});

describe("toActionError edge cases", () => {
  it("empty string", () => { expect(toActionError("").message).toBe("Unknown error (empty value thrown)"); });
  it("Symbol", () => { expect(toActionError(Symbol("x")).message).toContain("Symbol"); });
  it("number 0", () => { expect(toActionError(0).message).toBe("0"); });
});
