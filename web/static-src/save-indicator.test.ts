// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const { state } = vi.hoisted(() => {
  const state: {
    cb: ((i: any) => void) | null;
    pending: boolean;
  } = { cb: null, pending: false };
  return { state };
});

vi.mock("./actions/index.js", () => ({
  subscribeToActions: (fn: (i: any) => void) => { state.cb = fn; return () => {}; },
  pendingForAny: () => state.pending,
}));

vi.mock("./dom.js", () => ({
  $: { settingsSaveStatus: document.createElement("div") },
}));

vi.mock("./icons.js", () => ({
  iconEl: (path: string) => {
    const el = document.createElement("span");
    el.dataset["icon"] = path;
    return el;
  },
  ICON_SAVE_OK: "ok",
  ICON_SAVE_FAIL: "fail",
}));

import { showSaving } from "./save-indicator.js";
import { $ } from "./dom.js";

describe("save-indicator subscription", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    ($ as any).settingsSaveStatus = document.createElement("div");
    state.pending = false;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("500ms guard only blocks pending, not completion", () => {
    const el = $.settingsSaveStatus;

    showSaving();
    expect(el.querySelector(".spinner-sm")).not.toBeNull();

    // Within 500ms, a completion event should still fire
    vi.advanceTimersByTime(100);
    state.pending = false;
    state.cb!({ name: "settings.patch", status: "success" });

    expect(el.querySelector("[data-icon='ok']")).not.toBeNull();
  });

  it("500ms guard blocks pending branch", () => {
    const el = $.settingsSaveStatus;

    showSaving();
    vi.advanceTimersByTime(100);

    state.pending = true;
    state.cb!({ name: "settings.patch", status: "pending" });

    // Still the original spinner — guard blocked the re-set
    expect(el.querySelector(".spinner-sm")).not.toBeNull();
  });

  it("does not fire showSaved if other settings actions are still pending", () => {
    const el = $.settingsSaveStatus;

    vi.advanceTimersByTime(1000);

    state.pending = true;
    state.cb!({ name: "settings.patch", status: "success" });

    // Should NOT show saved icon — others still pending
    expect(el.querySelector("[data-icon='ok']")).toBeNull();
  });

  it("fires showSaved only when all settings actions are settled", () => {
    const el = $.settingsSaveStatus;

    vi.advanceTimersByTime(1000);

    state.pending = false;
    state.cb!({ name: "settings.save_steering", status: "success" });

    expect(el.querySelector("[data-icon='ok']")).not.toBeNull();
  });

  it("fires showError when action errors and nothing pending", () => {
    const el = $.settingsSaveStatus;

    vi.advanceTimersByTime(1000);

    state.pending = false;
    state.cb!({ name: "settings.patch", status: "error" });

    expect(el.querySelector("[data-icon='fail']")).not.toBeNull();
  });

  it("ignores non-settings actions", () => {
    const el = $.settingsSaveStatus;

    vi.advanceTimersByTime(1000);
    state.pending = false;
    state.cb!({ name: "unrelated.action", status: "success" });

    expect(el.children.length).toBe(0);
  });
});
