// Per-setting save indicators: only the slot naming the written key animates.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("./icon-el.js", () => ({
  iconEl: (path: string) => {
    const node = document.createElement("span");
    node.dataset["icon"] = path;
    return node;
  },
}));

vi.mock("./icons.js", () => ({
  ICON_SAVE_OK: "ok",
  ICON_SAVE_FAIL: "fail",
}));

import { showSaving, showSaved, showError, _resetForTest } from "./save-indicator.js";

/** Mount one slot per key; the returned lookup throws on a key it did not mount,
 *  so a renamed key in a test fails there rather than silently asserting on
 *  nothing. */
function mountSlots(...keys: string[]): (key: string) => HTMLElement {
  const byKey = new Map<string, HTMLElement>();
  for (const key of keys) {
    const slot = document.createElement("span");
    slot.className = "settings-save-status hidden";
    slot.dataset["saveStatus"] = key;
    document.body.append(slot);
    byKey.set(key, slot);
  }
  return (key) => {
    const slot = byKey.get(key);
    if (slot === undefined) {
      throw new Error(`no slot mounted for ${key}`);
    }
    return slot;
  };
}

const spinner = (slot: HTMLElement): Element | null => slot.querySelector(".spinner-sm");
const ok = (slot: HTMLElement): Element | null => slot.querySelector("[data-icon='ok']");
const fail = (slot: HTMLElement): Element | null => slot.querySelector("[data-icon='fail']");

describe("save-indicator", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    document.body.replaceChildren();
    _resetForTest();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("spins only the slot whose key was written", () => {
    const slot = mountSlots("debug_logs", "memory_enabled");

    showSaving("debug_logs");

    expect(spinner(slot("debug_logs"))).not.toBeNull();
    expect(slot("memory_enabled").children.length).toBe(0);
  });

  it("spins every slot a multi-key write carried", () => {
    const slot = mountSlots("notifications_enabled", "notify_agent_finished", "debug_logs");

    showSaving(["notifications_enabled", "notify_agent_finished"]);

    expect(spinner(slot("notifications_enabled"))).not.toBeNull();
    expect(spinner(slot("notify_agent_finished"))).not.toBeNull();
    expect(slot("debug_logs").children.length).toBe(0);
  });

  it("serves one slot that names several keys", () => {
    const shared = document.createElement("span");
    shared.className = "settings-save-status hidden";
    shared.dataset["saveStatus"] = "chat_retention_days chat_retention_forever";
    document.body.append(shared);

    showSaving("chat_retention_forever");

    expect(spinner(shared)).not.toBeNull();
  });

  it("does nothing for a key no slot names", () => {
    const slot = mountSlots("debug_logs");

    showSaving("last_model");
    showSaved("last_model");

    expect(slot("debug_logs").children.length).toBe(0);
  });

  it("settles the written slot and leaves its neighbour untouched", () => {
    const slot = mountSlots("debug_logs", "memory_enabled");

    showSaving("debug_logs");
    showSaved("debug_logs");

    expect(ok(slot("debug_logs"))).not.toBeNull();
    expect(slot("memory_enabled").children.length).toBe(0);
  });

  it("shows the failure face on the slot that failed", () => {
    const slot = mountSlots("debug_logs");

    showSaving("debug_logs");
    showError("debug_logs");

    expect(fail(slot("debug_logs"))).not.toBeNull();
  });

  it("fades a settled face out and then hides it", () => {
    const slot = mountSlots("debug_logs")("debug_logs");

    showSaving("debug_logs");
    showSaved("debug_logs");
    expect(slot.classList.contains("hidden")).toBe(false);

    vi.advanceTimersByTime(1200);
    expect(slot.classList.contains("fade-out")).toBe(true);
    expect(slot.classList.contains("hidden")).toBe(false);

    vi.advanceTimersByTime(400);
    expect(slot.classList.contains("hidden")).toBe(true);
  });

  it("holds the failure face longer than the success face", () => {
    const slot = mountSlots("a", "b");

    showSaved("a");
    showError("b");

    vi.advanceTimersByTime(1200);
    expect(slot("a").classList.contains("fade-out")).toBe(true);
    expect(slot("b").classList.contains("fade-out")).toBe(false);

    vi.advanceTimersByTime(1200);
    expect(slot("b").classList.contains("fade-out")).toBe(true);
  });

  it("delays a success that would blink an error away, per slot", () => {
    const slot = mountSlots("debug_logs")("debug_logs");

    showError("debug_logs");
    expect(fail(slot)).not.toBeNull();

    // A retry answers 300ms later: the ✗ still owes 1200ms of visibility.
    vi.advanceTimersByTime(300);
    showSaved("debug_logs");
    expect(fail(slot)).not.toBeNull();
    expect(ok(slot)).toBeNull();

    vi.advanceTimersByTime(1200);
    expect(ok(slot)).not.toBeNull();
  });

  it("does not delay a success on a slot that never errored", () => {
    const slot = mountSlots("a", "b");

    showError("a");
    vi.advanceTimersByTime(300);
    showSaved("b");

    expect(ok(slot("b"))).not.toBeNull();
    expect(fail(slot("a"))).not.toBeNull();
  });

  it("a new write clears the error credit, so its success is immediate", () => {
    const slot = mountSlots("debug_logs")("debug_logs");

    showError("debug_logs");
    vi.advanceTimersByTime(300);
    // The user changed the setting again: the spinner already replaced the ✗.
    showSaving("debug_logs");
    showSaved("debug_logs");

    expect(ok(slot)).not.toBeNull();
  });

  it("re-showing a settled slot brings it back from hidden", () => {
    const slot = mountSlots("debug_logs")("debug_logs");

    showSaved("debug_logs");
    vi.advanceTimersByTime(1600);
    expect(slot.classList.contains("hidden")).toBe(true);

    showSaving("debug_logs");
    expect(slot.classList.contains("hidden")).toBe(false);
    expect(slot.classList.contains("fade-out")).toBe(false);
    expect(spinner(slot)).not.toBeNull();
  });

  it("a spinner does not fade on its own", () => {
    const slot = mountSlots("debug_logs")("debug_logs");

    showSaving("debug_logs");
    vi.advanceTimersByTime(10000);

    expect(spinner(slot)).not.toBeNull();
    expect(slot.classList.contains("hidden")).toBe(false);
  });
});
