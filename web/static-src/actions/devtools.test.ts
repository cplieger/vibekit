// @vitest-environment happy-dom
import { describe, it, expect, beforeEach } from "vitest";

import { _resetForTest as resetRegistry, record } from "./registry.js";
import {
  initDevtoolsOverlay,
  toggle,
  _resetForTest as resetDevtools,
} from "./devtools.js";
import type { ActionInstance } from "./types.js";

beforeEach(() => {
  resetRegistry();
  resetDevtools();
  document.body.innerHTML = "";
});

function makeInstance(over: Partial<ActionInstance> = {}): ActionInstance {
  return {
    id: over.id ?? "id-" + Math.random().toString(36).slice(2, 8),
    name: over.name ?? "test.action",
    status: over.status ?? "success",
    args: over.args ?? {},
    startedAt: over.startedAt ?? Date.now(),
    completedAt: over.completedAt ?? Date.now() + 5,
    ...(over.result !== undefined ? { result: over.result } : {}),
    ...(over.error !== undefined ? { error: over.error } : {}),
  } as ActionInstance;
}

describe("devtools overlay", () => {
  it("does not render until toggled", () => {
    initDevtoolsOverlay();
    expect(document.querySelector(".vk-devtools-overlay")).toBeNull();
  });

  it("toggle() mounts the panel + toggle() again unmounts", () => {
    toggle();
    expect(document.querySelector(".vk-devtools-overlay")).not.toBeNull();
    toggle();
    expect(document.querySelector(".vk-devtools-overlay")).toBeNull();
  });

  it("renders existing log entries on mount (newest first)", () => {
    record(makeInstance({ id: "a", name: "first.action", status: "success" }));
    record(makeInstance({ id: "b", name: "second.action", status: "success" }));
    toggle();
    const rows = document.querySelectorAll(".vk-devtools-name");
    expect(rows.length).toBe(2);
    expect((rows[0] as HTMLElement).textContent).toBe("second.action");
    expect((rows[1] as HTMLElement).textContent).toBe("first.action");
  });

  it("subscribes live: new actions appear after mount", () => {
    toggle();
    record(makeInstance({ id: "live", name: "live.action", status: "success" }));
    const names = Array.from(
      document.querySelectorAll(".vk-devtools-name"),
    ).map((el) => (el as HTMLElement).textContent);
    expect(names).toContain("live.action");
  });

  it("renders error message in red detail row", () => {
    record(
      makeInstance({
        id: "err",
        name: "fail.action",
        status: "error",
        error: { message: "boom" },
      }),
    );
    toggle();
    const detail = document.querySelector(".vk-devtools-detail") as HTMLElement | null;
    expect(detail?.textContent).toBe("boom");
    expect(detail?.style.color).toBe("var(--c-red)");
  });

  it("close button removes the panel + cleans up subscription", () => {
    toggle();
    const closeBtn = document.querySelector(".vk-devtools-close") as HTMLButtonElement;
    closeBtn.click();
    expect(document.querySelector(".vk-devtools-overlay")).toBeNull();
    // Recording while unmounted shouldn't blow up.
    record(makeInstance({ name: "after-close" }));
  });

  it("Escape closes the panel", () => {
    toggle();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(document.querySelector(".vk-devtools-overlay")).toBeNull();
  });

  it("Ctrl+Shift+A toggles the overlay (after init)", () => {
    initDevtoolsOverlay();
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "a", ctrlKey: true, shiftKey: true }),
    );
    expect(document.querySelector(".vk-devtools-overlay")).not.toBeNull();
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "a", ctrlKey: true, shiftKey: true }),
    );
    expect(document.querySelector(".vk-devtools-overlay")).toBeNull();
  });

  it("Ctrl+Shift+A is ignored when focus is in an input", () => {
    initDevtoolsOverlay();
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();
    input.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "a",
        ctrlKey: true,
        shiftKey: true,
        bubbles: true,
      }),
    );
    expect(document.querySelector(".vk-devtools-overlay")).toBeNull();
  });

  it("truncates large args in the detail line", () => {
    const big = { msg: "x".repeat(200) };
    record(makeInstance({ name: "big.action", args: big }));
    toggle();
    const detail = document.querySelector(".vk-devtools-detail") as HTMLElement | null;
    expect(detail?.textContent?.length).toBeLessThanOrEqual(80);
    expect(detail?.textContent?.endsWith("...")).toBe(true);
  });

  it("handles DOM elements in args without crashing", () => {
    const el = document.createElement("div");
    record(makeInstance({ name: "dom.action", args: { row: el } }));
    expect(() => toggle()).not.toThrow();
    const detail = document.querySelector(".vk-devtools-detail") as HTMLElement | null;
    expect(detail?.textContent).toContain("<div>");
  });
});
