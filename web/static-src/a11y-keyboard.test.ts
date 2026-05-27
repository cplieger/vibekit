// @vitest-environment happy-dom
// Accessibility tests: tool-group keyboard nav, file-picker labels, upload progress.
import { describe, it, expect, vi } from "vitest";

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

describe("a11y: tool-group header keyboard and aria", () => {
  it("header has role=button, tabindex=0, and aria-expanded=true", async () => {
    const { getOrCreateToolGroup, breakToolGroup } = await import("./tool-group.js");
    breakToolGroup();
    const group = getOrCreateToolGroup((el) => {
      document.body.appendChild(el);
    });
    const header = group.querySelector(".tool-group-header") as HTMLElement;
    expect(header.getAttribute("role")).toBe("button");
    expect(header.getAttribute("tabindex")).toBe("0");
    expect(header.getAttribute("aria-expanded")).toBe("true");
    document.body.removeChild(group);
  });

  it("header toggles aria-expanded on Enter key", async () => {
    const { getOrCreateToolGroup, breakToolGroup } = await import("./tool-group.js");
    breakToolGroup();
    const group = getOrCreateToolGroup((el) => {
      document.body.appendChild(el);
    });
    const header = group.querySelector(".tool-group-header") as HTMLElement;

    header.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(header.getAttribute("aria-expanded")).toBe("false");
    expect(group.classList.contains("tool-group-collapsed")).toBe(true);

    header.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(header.getAttribute("aria-expanded")).toBe("true");
    expect(group.classList.contains("tool-group-collapsed")).toBe(false);

    document.body.removeChild(group);
  });

  it("header toggles aria-expanded on Space key", async () => {
    const { getOrCreateToolGroup, breakToolGroup } = await import("./tool-group.js");
    breakToolGroup();
    const group = getOrCreateToolGroup((el) => {
      document.body.appendChild(el);
    });
    const header = group.querySelector(".tool-group-header") as HTMLElement;

    header.dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
    expect(header.getAttribute("aria-expanded")).toBe("false");

    document.body.removeChild(group);
  });

  it("maybeCollapseGroup sets aria-expanded=false", async () => {
    const { getOrCreateToolGroup, breakToolGroup, maybeCollapseGroup } =
      await import("./tool-group.js");
    breakToolGroup();
    const group = getOrCreateToolGroup((el) => {
      document.body.appendChild(el);
    });

    // Add 3 completed tool-call children (no data-start-ms)
    for (let i = 0; i < 3; i++) {
      const call = document.createElement("div");
      call.className = "tool-call";
      call.dataset["kind"] = "read";
      call.dataset["title"] = `read${i}`;
      call.dataset["filename"] = `f${i}.ts`;
      call.dataset["mcpServer"] = "";
      group.appendChild(call);
    }

    const header = group.querySelector(".tool-group-header") as HTMLElement;
    expect(header.getAttribute("aria-expanded")).toBe("true");

    maybeCollapseGroup(group.querySelector(".tool-call")!);
    expect(group.classList.contains("tool-group-auto-collapsed")).toBe(true);
    expect(header.getAttribute("aria-expanded")).toBe("false");

    document.body.removeChild(group);
  });
});
