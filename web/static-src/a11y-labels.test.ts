// @vitest-environment happy-dom
// Accessibility tests: verify missing labels, focus management, and keyboard nav fixes.
import { describe, it, expect } from "vitest";
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
