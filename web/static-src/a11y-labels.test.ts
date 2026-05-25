// @vitest-environment happy-dom
// Accessibility tests: verify missing labels, focus management, and keyboard nav fixes.
import { describe, it, expect, vi } from "vitest";
import { domRenderer } from "./smd-renderer.js";
import { CHECKBOX } from "./smd-parser-types.js";

describe("a11y: missing labels", () => {
  it("smd-renderer CHECKBOX has aria-label", () => {
    const container = document.createElement("div");
    const renderer = domRenderer(container, false);
    renderer.add_token(renderer.data, CHECKBOX);
    const cb = container.querySelector("input[type=checkbox]") as HTMLInputElement;
    expect(cb).not.toBeNull();
    expect(cb.getAttribute("aria-label")).toBe("Task item");
  });

  it("toast element has aria-label with level and message", async () => {
    const { showToast } = await import("./toast.js");
    const dismiss = showToast("File saved", "success", 5000);
    const toast = document.querySelector(".vk-toast-success") as HTMLDivElement;
    expect(toast).not.toBeNull();
    expect(toast.getAttribute("aria-label")).toBe("success notification: File saved. Click to dismiss.");
    dismiss();
  });

  it("banner stack container has aria-label and aria-live", async () => {
    // Setup minimal DOM for banner-stack
    const container = document.createElement("div");
    container.id = "banner-stack";
    document.body.appendChild(container);

    const { renderStack } = await import("./banner-stack.js");
    renderStack();

    expect(container.getAttribute("aria-label")).toBe("Notifications");
    expect(container.getAttribute("aria-live")).toBe("polite");
    document.body.removeChild(container);
  });

  it("settings tab bar gets role=tablist and buttons get role=tab", async () => {
    // Setup minimal DOM for settings-tabs
    const bar = document.createElement("div");
    bar.id = "settings-tab-bar";
    const select = document.createElement("select");
    select.id = "settings-tab-select";
    const tabs = ["general", "tools", "permissions", "instructions", "git"];
    for (const t of tabs) {
      const btn = document.createElement("button");
      btn.setAttribute("data-settings-tab", t);
      bar.appendChild(btn);
      const opt = document.createElement("option");
      opt.value = t;
      select.appendChild(opt);
    }
    document.body.append(bar, select);

    const { initSettingsTabs } = await import("./settings-tabs.js");
    initSettingsTabs();

    expect(bar.getAttribute("role")).toBe("tablist");
    expect(bar.getAttribute("aria-label")).toBe("Settings sections");
    const generalBtn = bar.querySelector('[data-settings-tab="general"]') as HTMLButtonElement;
    expect(generalBtn.getAttribute("role")).toBe("tab");
    expect(generalBtn.getAttribute("aria-label")).toBe("General");

    document.body.removeChild(bar);
    document.body.removeChild(select);
  });
});

describe("a11y: keyboard navigation on picker grid", () => {
  it("wireArrowNav makes items focusable via tabindex", async () => {
    const { wireArrowNav } = await import("./arrow-nav.js");
    const container = document.createElement("div");
    const btn1 = document.createElement("button");
    btn1.className = "picker-btn";
    btn1.textContent = "Model A";
    const btn2 = document.createElement("button");
    btn2.className = "picker-btn";
    btn2.textContent = "Model B";
    container.append(btn1, btn2);
    document.body.appendChild(container);

    wireArrowNav(container, ".picker-btn", { orientation: "horizontal" });

    expect(btn1.getAttribute("tabindex")).toBe("0");
    expect(btn2.getAttribute("tabindex")).toBe("-1");

    document.body.removeChild(container);
  });
});

describe("a11y: focus management", () => {
  it("overflow menu returns focus to trigger on close", async () => {
    const { openOverflowMenu, closeOverflowMenu } = await import("./overflow-menu.js");
    const trigger = document.createElement("button");
    trigger.textContent = "Menu";
    document.body.appendChild(trigger);
    trigger.focus();

    openOverflowMenu(trigger, [
      { id: "a", label: "Action A", onSelect: vi.fn() },
    ]);

    // Focus moved to menu item
    const menuItem = document.querySelector(".overflow-menu-item") as HTMLButtonElement;
    expect(menuItem).not.toBeNull();

    closeOverflowMenu();

    expect(document.activeElement).toBe(trigger);
    document.body.removeChild(trigger);
  });
});
