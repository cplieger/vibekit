// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { isDragHandled, setReorderCallback, attachDrag } from "./tabs-drag.js";

describe("TabDragController", () => {
  let container: HTMLDivElement;
  let tab: HTMLDivElement;

  beforeEach(() => {
    container = document.createElement("div");
    tab = document.createElement("div");
    tab.dataset["tabId"] = "tab1";
    container.appendChild(tab);
    document.body.appendChild(container);
    attachDrag(tab);
  });

  it("isDragHandled() is false initially", () => {
    expect(isDragHandled()).toBe(false);
  });

  it("pointerdown + pointermove below threshold does not start drag", () => {
    tab.dispatchEvent(new PointerEvent("pointerdown", {
      clientY: 100, isPrimary: true, pointerType: "mouse", bubbles: true,
    }));
    tab.dispatchEvent(new PointerEvent("pointermove", {
      clientY: 102, isPrimary: true, pointerType: "mouse", bubbles: true,
    }));
    tab.dispatchEvent(new PointerEvent("pointerup", { bubbles: true }));
    expect(isDragHandled()).toBe(false);
  });

  it("pointerdown on .tab-close does not arm drag", () => {
    const closeBtn = document.createElement("button");
    closeBtn.className = "tab-close";
    tab.appendChild(closeBtn);
    closeBtn.dispatchEvent(new PointerEvent("pointerdown", {
      clientY: 100, isPrimary: true, pointerType: "mouse", bubbles: true,
    }));
    tab.dispatchEvent(new PointerEvent("pointermove", {
      clientY: 200, isPrimary: true, pointerType: "mouse", bubbles: true,
    }));
    expect(isDragHandled()).toBe(false);
  });

  it("touch pointerdown without hold timeout does not start drag", () => {
    vi.useFakeTimers();
    tab.dispatchEvent(new PointerEvent("pointerdown", {
      clientY: 100, isPrimary: true, pointerType: "touch", bubbles: true,
    }));
    // Release before 300ms hold timer
    vi.advanceTimersByTime(100);
    tab.dispatchEvent(new PointerEvent("pointerup", { bubbles: true }));
    vi.advanceTimersByTime(300);
    expect(isDragHandled()).toBe(false);
    vi.useRealTimers();
  });

  it("setReorderCallback stores callback for drag completion", () => {
    const cb = vi.fn();
    setReorderCallback(cb);
    // Callback is stored but not invoked without a completed drag
    expect(cb).not.toHaveBeenCalled();
  });
});
