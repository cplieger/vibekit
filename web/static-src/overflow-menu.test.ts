// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

import { openOverflowMenu, closeOverflowMenu, _isOverflowMenuOpen } from "./overflow-menu.js";

function setupTriggerDOM(): HTMLButtonElement {
  document.body.innerHTML = `<button id="trigger">⋯</button>`;
  return document.getElementById("trigger") as HTMLButtonElement;
}

function findMenu(): HTMLElement | null {
  return document.querySelector(".overflow-menu");
}

describe("overflow-menu", () => {
  beforeEach(() => {
    closeOverflowMenu();
    vi.useFakeTimers();
  });

  it("opens with the supplied items", () => {
    const trigger = setupTriggerDOM();
    const a = vi.fn();
    const b = vi.fn();
    openOverflowMenu(trigger, [
      { id: "a", label: "Action A", onSelect: a },
      { id: "b", label: "Action B", onSelect: b },
    ]);
    const menu = findMenu();
    expect(menu).not.toBeNull();
    expect(_isOverflowMenuOpen()).toBe(true);
    const items = menu!.querySelectorAll<HTMLButtonElement>("[data-overflow-item]");
    expect(items.length).toBe(2);
    expect(items[0]!.textContent).toBe("Action A");
    expect(items[1]!.textContent).toBe("Action B");
  });

  it("clicking an item invokes its handler and closes the menu", () => {
    const trigger = setupTriggerDOM();
    const a = vi.fn();
    openOverflowMenu(trigger, [{ id: "a", label: "Action A", onSelect: a }]);
    const item = findMenu()!.querySelector<HTMLButtonElement>("[data-overflow-item='a']")!;
    item.click();
    expect(a).toHaveBeenCalledTimes(1);
    expect(findMenu()).toBeNull();
    expect(_isOverflowMenuOpen()).toBe(false);
  });

  it("disabled items render disabled and don't fire onSelect", () => {
    const trigger = setupTriggerDOM();
    const a = vi.fn();
    openOverflowMenu(trigger, [{ id: "a", label: "Action A", onSelect: a, disabled: true }]);
    const item = findMenu()!.querySelector<HTMLButtonElement>("[data-overflow-item='a']")!;
    expect(item.disabled).toBe(true);
    expect(item.getAttribute("aria-disabled")).toBe("true");
    item.click();
    expect(a).not.toHaveBeenCalled();
  });

  it("danger items get the danger class", () => {
    const trigger = setupTriggerDOM();
    openOverflowMenu(trigger, [
      { id: "x", label: "Delete", onSelect: () => undefined, danger: true },
    ]);
    const item = findMenu()!.querySelector<HTMLButtonElement>("[data-overflow-item='x']")!;
    expect(item.classList.contains("overflow-menu-item-danger")).toBe(true);
  });

  it("Escape closes the menu", () => {
    const trigger = setupTriggerDOM();
    openOverflowMenu(trigger, [{ id: "a", label: "Action A", onSelect: () => undefined }]);
    // Defer attach happens via setTimeout(0) — flush it.
    vi.advanceTimersByTime(1);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(findMenu()).toBeNull();
    expect(_isOverflowMenuOpen()).toBe(false);
  });

  it("outside click closes the menu", () => {
    const trigger = setupTriggerDOM();
    document.body.insertAdjacentHTML("beforeend", `<div id="elsewhere"></div>`);
    openOverflowMenu(trigger, [{ id: "a", label: "Action A", onSelect: () => undefined }]);
    vi.advanceTimersByTime(1);
    const elsewhere = document.getElementById("elsewhere") as HTMLElement;
    elsewhere.click();
    expect(findMenu()).toBeNull();
  });

  it("clicking inside the menu does not close it (until item is selected)", () => {
    const trigger = setupTriggerDOM();
    openOverflowMenu(trigger, [{ id: "a", label: "Action A", onSelect: () => undefined }]);
    vi.advanceTimersByTime(1);
    const menu = findMenu()!;
    // Click the menu container (not an item) — handler should not fire,
    // menu should stay open.
    menu.click();
    expect(findMenu()).not.toBeNull();
  });

  it("opening a second menu closes the first one", () => {
    const trigger = setupTriggerDOM();
    openOverflowMenu(trigger, [{ id: "a", label: "Action A", onSelect: () => undefined }]);
    expect(document.querySelectorAll(".overflow-menu").length).toBe(1);
    openOverflowMenu(trigger, [{ id: "b", label: "Action B", onSelect: () => undefined }]);
    expect(document.querySelectorAll(".overflow-menu").length).toBe(1);
    expect(findMenu()!.textContent).toContain("Action B");
  });

  it("closeOverflowMenu is idempotent when no menu is open", () => {
    expect(() => closeOverflowMenu()).not.toThrow();
    expect(_isOverflowMenuOpen()).toBe(false);
  });
});
