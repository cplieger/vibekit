// @vitest-environment happy-dom
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
import { bindLoadingState } from "./loading.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

describe("bindLoadingState — single name", () => {
  it("toggles disabled while action is pending", async () => {
    let resolveRun: (value: string) => void;
    const action = defineAction({
      name: "test.bind1",
      run: () =>
        new Promise<string>((r) => {
          resolveRun = r;
        }),
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
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
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
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
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
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
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
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    btn.disabled = true; // disabled for some unrelated reason (e.g. validation)
    bindLoadingState("test.bind5", btn, { preserveDisabled: true });
    expect(btn.disabled).toBe(true); // still disabled
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true); // still disabled (now from pending)
    resolveRun!();
    await p;
    expect(btn.disabled).toBe(true); // STAYS disabled (original state preserved)
  });

  it("ignores transitions of other actions", async () => {
    let resolveRun: () => void;
    const target = defineAction({
      name: "test.bind6.target",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
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
      run: () =>
        new Promise<void>((r) => {
          resolvers.push(r);
        }),
    });
    const btn = document.createElement("button");
    bindLoadingState("test.bind7", btn);
    const p1 = action.dispatch({});
    const p2 = action.dispatch({});
    expect(btn.disabled).toBe(true);
    resolvers[0]!();
    await p1;
    expect(btn.disabled).toBe(true); // still pending: instance #2
    resolvers[1]!();
    await p2;
    expect(btn.disabled).toBe(false);
  });

  it("returns an unsubscribe that stops further updates", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind8",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
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
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
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
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
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

  it("preserveAriaBusy: true does not manage aria-busy", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind11",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    // External code sets aria-busy before bind.
    btn.setAttribute("aria-busy", "true");
    bindLoadingState("test.bind11", btn, { preserveAriaBusy: true });
    const p = action.dispatch({});
    // aria-busy should remain untouched (external code owns it).
    expect(btn.getAttribute("aria-busy")).toBe("true");
    expect(btn.disabled).toBe(true);
    resolveRun!();
    await p;
    // After completion, aria-busy is still the external value.
    expect(btn.getAttribute("aria-busy")).toBe("true");
    expect(btn.disabled).toBe(false);
  });

  it("preserveDisabled: external mutation DURING pending is overwritten on completion (documented limitation)", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind12",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    // Initially enabled.
    bindLoadingState("test.bind12", btn, { preserveDisabled: true });
    expect(btn.disabled).toBe(false);
    // Start action — snapshots baseDisabled = false.
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    // External code mutates disabled DURING pending phase.
    // This mutation will be lost — documented limitation.
    btn.disabled = true;
    resolveRun!();
    await p;
    // Restores to the snapshot taken at pending edge (false), NOT the
    // external mutation (true). This is the documented contract.
    expect(btn.disabled).toBe(false);
  });

  it("disposed listener does not fire after unbind (B3)", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind13",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    const unbind = bindLoadingState("test.bind13", btn);
    // Unbind before any action starts.
    unbind();
    // Now dispatch — the disposed listener should not re-enable/disable.
    const p = action.dispatch({});
    expect(btn.disabled).toBe(false); // not affected
    resolveRun!();
    await p;
    expect(btn.disabled).toBe(false); // still not affected
  });

  it("auto-disposes when element is removed from DOM mid-pending", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.bind_autodispose",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    const unbind = bindLoadingState("test.bind_autodispose", btn);
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    expect(btn.isConnected).toBe(true);
    // Remove element from DOM
    btn.remove();
    expect(btn.isConnected).toBe(false);
    // Complete the action — the binding should auto-dispose on the
    // success transition (sees el disconnected) and NOT restore disabled.
    resolveRun!();
    await p;
    // After auto-dispose, the element stays disabled (no restore).
    // Note: unbind() is a no-op after auto-dispose.
    expect(btn.disabled).toBe(true);
    unbind(); // cleanup
  });
});

describe("bindLoadingState — multi-name", () => {
  it("disables while ANY named action is pending", async () => {
    let resolve1!: () => void;
    let resolve2!: () => void;
    const a1 = defineAction({
      name: "test.multi1",
      run: () =>
        new Promise<void>((r) => {
          resolve1 = r;
        }),
    });
    const a2 = defineAction({
      name: "test.multi2",
      run: () =>
        new Promise<void>((r) => {
          resolve2 = r;
        }),
    });
    const btn = document.createElement("button");
    bindLoadingState(["test.multi1", "test.multi2"], btn);
    expect(btn.disabled).toBe(false);
    const p1 = a1.dispatch({});
    expect(btn.disabled).toBe(true);
    const p2 = a2.dispatch({});
    expect(btn.disabled).toBe(true);
    resolve1();
    await p1;
    // Still disabled because a2 is pending
    expect(btn.disabled).toBe(true);
    resolve2();
    await p2;
    expect(btn.disabled).toBe(false);
  });

  it("returns no-op for empty actionNames", () => {
    const btn = document.createElement("button");
    const unbind = bindLoadingState([], btn);
    expect(typeof unbind).toBe("function");
    unbind(); // should not throw
  });

  it("delegates to bindLoadingState for single-name array", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "test.multi_single",
      run: () =>
        new Promise<void>((r) => {
          resolve = r;
        }),
    });
    const btn = document.createElement("button");
    bindLoadingState(["test.multi_single"], btn);
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    resolve();
    await p;
    expect(btn.disabled).toBe(false);
  });

  it("auto-disposes when element is removed from DOM", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "test.multi_dispose",
      run: () =>
        new Promise<void>((r) => {
          resolve = r;
        }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    bindLoadingState(["test.multi_dispose", "test.multi_other"], btn);
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    btn.remove();
    resolve();
    await p;
    // Auto-disposed: element stays disabled (no restore)
    expect(btn.disabled).toBe(true);
  });

  it("unsubscribe mid-pending restores element state", async () => {
    let resolve!: () => void;
    const action = defineAction({
      name: "test.multi_unsub",
      run: () =>
        new Promise<void>((r) => {
          resolve = r;
        }),
    });
    const btn = document.createElement("button");
    const unbind = bindLoadingState(["test.multi_unsub"], btn);
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    unbind();
    expect(btn.disabled).toBe(false);
    resolve();
    await p;
  });
});

describe("bindLoadingState — focus restore", () => {
  it("restores focus to button when user has not moved focus elsewhere", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.focus_restore",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    bindLoadingState("test.focus_restore", btn);
    btn.focus();
    expect(document.activeElement).toBe(btn);
    const p = action.dispatch({});
    // Button disabled → focus moves to body
    expect(btn.disabled).toBe(true);
    resolveRun!();
    await p;
    // Focus restored because user didn't move it elsewhere
    expect(document.activeElement).toBe(btn);
    btn.remove();
  });

  it("does NOT steal focus back when user moved focus to another element", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.focus_no_steal",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    const other = document.createElement("input");
    document.body.appendChild(btn);
    document.body.appendChild(other);
    bindLoadingState("test.focus_no_steal", btn);
    btn.focus();
    expect(document.activeElement).toBe(btn);
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    // User explicitly moves focus to another element during pending
    other.focus();
    expect(document.activeElement).toBe(other);
    resolveRun!();
    await p;
    // Focus should NOT be stolen back to btn
    expect(document.activeElement).toBe(other);
    btn.remove();
    other.remove();
  });
});

describe("bindLoadingState — focus restore edge cases", () => {
  it("does NOT restore focus when preserveDisabled keeps element disabled (baseDisabled=true)", async () => {
    // Scenario: button is disabled by validation at the time the action
    // starts. hadFocus is false because a disabled button can't receive
    // focus in real browsers. After action completes, preserveDisabled
    // restores disabled=true and focus is NOT touched.
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.focus_preserve_disabled",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    const other = document.createElement("input");
    document.body.appendChild(btn);
    document.body.appendChild(other);
    btn.disabled = true;
    bindLoadingState("test.focus_preserve_disabled", btn, { preserveDisabled: true });
    other.focus();
    expect(document.activeElement).toBe(other);
    // Dispatch while button is disabled — hadFocus should be false
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    resolveRun!();
    await p;
    // Button stays disabled (preserveDisabled restores baseDisabled=true)
    expect(btn.disabled).toBe(true);
    // Focus stays on other — not stolen to btn
    expect(document.activeElement).toBe(other);
    btn.remove();
    other.remove();
  });

  it("restores focus after error (not just success)", async () => {
    const action = defineAction({
      name: "test.focus_on_error",
      run: async () => {
        throw new Error("boom");
      },
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    bindLoadingState("test.focus_on_error", btn);
    btn.focus();
    expect(document.activeElement).toBe(btn);
    await action.dispatch({});
    // Focus restored even though action errored
    expect(document.activeElement).toBe(btn);
    btn.remove();
  });
});

describe("bindLoadingState — multi-name focus restore", () => {
  it("restores focus when all actions complete", async () => {
    let resolve1!: () => void;
    let resolve2!: () => void;
    const a1 = defineAction({
      name: "test.multi_focus1",
      run: () =>
        new Promise<void>((r) => {
          resolve1 = r;
        }),
    });
    const a2 = defineAction({
      name: "test.multi_focus2",
      run: () =>
        new Promise<void>((r) => {
          resolve2 = r;
        }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    bindLoadingState(["test.multi_focus1", "test.multi_focus2"], btn);
    btn.focus();
    expect(document.activeElement).toBe(btn);
    const p1 = a1.dispatch({});
    const p2 = a2.dispatch({});
    expect(btn.disabled).toBe(true);
    resolve1();
    await p1;
    // Still pending (a2), focus not yet restored
    expect(btn.disabled).toBe(true);
    resolve2();
    await p2;
    // All done — focus restored
    expect(document.activeElement).toBe(btn);
    btn.remove();
  });

  it("does NOT steal focus when user moved elsewhere", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.multi_focus_no_steal",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    const other = document.createElement("input");
    document.body.appendChild(btn);
    document.body.appendChild(other);
    bindLoadingState(["test.multi_focus_no_steal"], btn);
    btn.focus();
    const p = action.dispatch({});
    other.focus();
    resolveRun!();
    await p;
    expect(document.activeElement).toBe(other);
    btn.remove();
    other.remove();
  });
});

describe("bindLoadingState — disabledFn", () => {
  it("uses disabledFn to resolve disabled state on completion", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.disabledfn1",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    let formValid = true;
    const btn = document.createElement("button");
    bindLoadingState("test.disabledfn1", btn, { disabledFn: () => !formValid });
    expect(btn.disabled).toBe(false);
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true); // pending
    // External validation changes during pending
    formValid = false;
    resolveRun!();
    await p;
    // disabledFn re-evaluated: !formValid = true → stays disabled
    expect(btn.disabled).toBe(true);
  });

  it("disabledFn returning false enables button after action completes", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.disabledfn2",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    bindLoadingState("test.disabledfn2", btn, { disabledFn: () => false });
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    resolveRun!();
    await p;
    expect(btn.disabled).toBe(false);
  });

  it("disabledFn takes precedence over preserveDisabled", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.disabledfn3",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    btn.disabled = true;
    // Both set: disabledFn wins
    bindLoadingState("test.disabledfn3", btn, {
      preserveDisabled: true,
      disabledFn: () => false,
    });
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true); // pending
    resolveRun!();
    await p;
    // disabledFn returns false → enabled (ignores preserveDisabled snapshot)
    expect(btn.disabled).toBe(false);
  });

  it("disabledFn works with bindLoadingState", async () => {
    let resolve1!: () => void;
    const a1 = defineAction({
      name: "test.multi_dfn1",
      run: () =>
        new Promise<void>((r) => {
          resolve1 = r;
        }),
    });
    let externalDisabled = false;
    const btn = document.createElement("button");
    bindLoadingState(["test.multi_dfn1", "test.multi_dfn2"], btn, {
      disabledFn: () => externalDisabled,
    });
    expect(btn.disabled).toBe(false);
    const p = a1.dispatch({});
    expect(btn.disabled).toBe(true);
    externalDisabled = true;
    resolve1();
    await p;
    // disabledFn returns true → stays disabled
    expect(btn.disabled).toBe(true);
  });
});

describe("bindLoadingState — disabledFn edge cases", () => {
  it("recovers gracefully when disabledFn throws on completion", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.disabledfn_throw",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    bindLoadingState("test.disabledfn_throw", btn, {
      pendingClass: "loading",
      disabledFn: () => {
        throw new Error("validation exploded");
      },
    });
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    expect(btn.classList.contains("loading")).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");
    resolveRun!();
    await p;
    // disabledFn threw → falls back to false (element re-enabled)
    expect(btn.disabled).toBe(false);
    // aria-busy and pendingClass still cleaned up despite the throw
    expect(btn.getAttribute("aria-busy")).toBeNull();
    expect(btn.classList.contains("loading")).toBe(false);
    btn.remove();
  });

  it("disabledFn throwing does not prevent focus restore", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.disabledfn_throw_focus",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    let shouldThrow = false;
    bindLoadingState("test.disabledfn_throw_focus", btn, {
      disabledFn: () => {
        if (shouldThrow) {throw new Error("boom");}
        return false;
      },
    });
    btn.focus();
    expect(document.activeElement).toBe(btn);
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    shouldThrow = true;
    resolveRun!();
    await p;
    // Falls back to false → button enabled → focus restored
    expect(btn.disabled).toBe(false);
    expect(document.activeElement).toBe(btn);
    btn.remove();
  });

  it("disabledFn returning true suppresses focus restore", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.disabledfn_no_focus",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    bindLoadingState("test.disabledfn_no_focus", btn, {
      disabledFn: () => true,
    });
    btn.focus();
    const focusSpy = vi.spyOn(btn, "focus");
    const p = action.dispatch({});
    resolveRun!();
    await p;
    // disabledFn returns true → button stays disabled → focus NOT restored
    expect(btn.disabled).toBe(true);
    expect(focusSpy).not.toHaveBeenCalled();
    btn.remove();
  });

  it("disabledFn throwing in bindLoadingState recovers gracefully", async () => {
    let resolveRun: () => void;
    const action = defineAction({
      name: "test.multi_dfn_throw",
      run: () =>
        new Promise<void>((r) => {
          resolveRun = r;
        }),
    });
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    bindLoadingState(["test.multi_dfn_throw", "test.multi_dfn_other"], btn, {
      pendingClass: "spin",
      disabledFn: () => {
        throw new Error("kaboom");
      },
    });
    const p = action.dispatch({});
    expect(btn.disabled).toBe(true);
    expect(btn.classList.contains("spin")).toBe(true);
    resolveRun!();
    await p;
    // Falls back to false on throw
    expect(btn.disabled).toBe(false);
    expect(btn.classList.contains("spin")).toBe(false);
    expect(btn.getAttribute("aria-busy")).toBeNull();
    btn.remove();
  });
});
