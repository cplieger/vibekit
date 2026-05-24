// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { bindLoadingState } from "./loading.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

describe("bindLoadingState", () => {
  it("toggles disabled while action is pending", async () => {
    let resolveRun: (value: string) => void;
    const action = defineAction({
      name: "test.bind1",
      run: () => new Promise<string>((r) => { resolveRun = r; }),
    });
    const btn = document.createElement("button");
    bindLoadingState("test.bind1", btn);
    expect(btn.disabled).toBe(false);
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    resolveRun!("ok");
    await p;
    expect(btn.disabled).toBe(false);
  });

  it("sets aria-busy by default + clears on completion", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind2",
      run: () => new Promise<void>((r) => { resolveRun = r; }),
    });
    const btn = document.createElement("button");
    bindLoadingState("test.bind2", btn);
    expect(btn.getAttribute("aria-busy")).toBeNull();
    const p = action.dispatch({});
    expect(btn.getAttribute("aria-busy")).toBe("true");
    resolveRun!();
    await p;
    expect(btn.getAttribute("aria-busy")).toBeNull();
  });

  it("ariaBusy: false suppresses the attribute", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind3",
      run: () => new Promise<void>((r) => { resolveRun = r; }),
    });
    const btn = document.createElement("button");
    bindLoadingState("test.bind3", btn, { ariaBusy: false });
    const p = action.dispatch({});
    expect(btn.getAttribute("aria-busy")).toBeNull();
    resolveRun!();
    await p;
  });

  it("pendingClass adds + removes a CSS class", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind4",
      run: () => new Promise<void>((r) => { resolveRun = r; }),
    });
    const btn = document.createElement("button");
    bindLoadingState("test.bind4", btn, { pendingClass: "btn-loading" });
    expect(btn.classList.contains("btn-loading")).toBe(false);
    const p = action.dispatch({});
    expect(btn.classList.contains("btn-loading")).toBe(true);
    resolveRun!();
    await p;
    expect(btn.classList.contains("btn-loading")).toBe(false);
  });

  it("preserveDisabled: true ORs original disabled with pending state", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind5",
      run: () => new Promise<void>((r) => { resolveRun = r; }),
    });
    const btn = document.createElement("button");
    btn.disabled = true;  // disabled for some unrelated reason (e.g. validation)
    bindLoadingState("test.bind5", btn, { preserveDisabled: true });
    expect(btn.disabled).toBe(true);  // still disabled
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);  // still disabled (now from pending)
    resolveRun!();
    await p;
    expect(btn.disabled).toBe(true);  // STAYS disabled (original state preserved)
  });

  it("ignores transitions of other actions", async () => {
    let resolveRun: () => void;
    const target = defineAction({
      name: "test.bind6.target",
      run: () => new Promise<void>((r) => { resolveRun = r; }),
    });
    const noise = defineAction({
      name: "test.bind6.noise",
      run: async () => undefined,
    });
    const btn = document.createElement("button");
    bindLoadingState("test.bind6.target", btn);
    // Fire and complete a noise action — should not affect btn.
    await noise.dispatch({});
    expect(btn.disabled).toBe(false);
    const p = target.dispatch({});
    expect(btn.disabled).toBe(true);
    resolveRun!();
    await p;
  });

  it("multiple in-flight instances keep btn disabled until ALL complete", async () => {
    const resolvers: (() => void)[] = [];
    const action = defineAction({
      name: "test.bind7",
      run: () => new Promise<void>((r) => { resolvers.push(r); }),
    });
    const btn = document.createElement("button");
    bindLoadingState("test.bind7", btn);
    const p1 = action.dispatch({});
    const p2 = action.dispatch({});
    expect(btn.disabled).toBe(true);
    resolvers[0]!();
    await p1;
    expect(btn.disabled).toBe(true);  // still pending: instance #2
    resolvers[1]!();
    await p2;
    expect(btn.disabled).toBe(false);
  });

  it("returns an unsubscribe that stops further updates", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind8",
      run: () => new Promise<void>((r) => { resolveRun = r; }),
    });
    const btn = document.createElement("button");
    const unbind = bindLoadingState("test.bind8", btn);
    unbind();
    const p = action.dispatch({});
    // No subscription → btn state never updates from pending.
    expect(btn.disabled).toBe(false);
    resolveRun!();
    await p;
  });

  it("preserveDisabled: post-bind external mutation is respected on restore", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind9",
      run: () => new Promise<void>((r) => { resolveRun = r; }),
    });
    const btn = document.createElement("button");
    // Initially enabled.
    bindLoadingState("test.bind9", btn, { preserveDisabled: true });
    expect(btn.disabled).toBe(false);
    // External code disables the button AFTER bind (e.g. validation).
    btn.disabled = true;
    // Now an action starts — should snapshot the live disabled=true.
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    resolveRun!();
    await p;
    // After action completes, restores to the pre-pending value (true).
    expect(btn.disabled).toBe(true);
  });

  it("unsubscribe mid-pending restores element state", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind10",
      run: () => new Promise<void>((r) => { resolveRun = r; }),
    });
    const btn = document.createElement("button");
    btn.classList.add("other");
    const unbind = bindLoadingState("test.bind10", btn, { pendingClass: "btn-loading" });
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");
    expect(btn.classList.contains("btn-loading")).toBe(true);
    // Unsubscribe while still pending — should restore.
    unbind();
    expect(btn.disabled).toBe(false);
    expect(btn.getAttribute("aria-busy")).toBeNull();
    expect(btn.classList.contains("btn-loading")).toBe(false);
    resolveRun!();
    await p;
  });
});
