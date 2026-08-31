// Accessibility tests: tool-group keyboard nav, file-picker labels, upload progress.
import { describe, it, expect, vi } from "vitest";

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

describe("a11y: tool-group header keyboard and aria", () => {
  it("header has role=button, tabindex=0, and aria-expanded=true", async () => {
    const { buildToolGroupShell } = await import("./tool-group.js");
    const group = buildToolGroupShell();
    document.body.appendChild(group);
    const header = group.querySelector(".tool-group-header")!;
    expect(header.getAttribute("role")).toBe("button");
    expect(header.getAttribute("tabindex")).toBe("0");
    expect(header.getAttribute("aria-expanded")).toBe("true");
    document.body.removeChild(group);
  });

  it("header toggles aria-expanded on Enter key", async () => {
    const { buildToolGroupShell } = await import("./tool-group.js");
    const group = buildToolGroupShell();
    document.body.appendChild(group);
    const header = group.querySelector(".tool-group-header")!;

    header.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(header.getAttribute("aria-expanded")).toBe("false");
    expect(group.classList.contains("tool-group-collapsed")).toBe(true);

    header.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(header.getAttribute("aria-expanded")).toBe("true");
    expect(group.classList.contains("tool-group-collapsed")).toBe(false);

    document.body.removeChild(group);
  });

  it("header toggles aria-expanded on Space key", async () => {
    const { buildToolGroupShell } = await import("./tool-group.js");
    const group = buildToolGroupShell();
    document.body.appendChild(group);
    const header = group.querySelector(".tool-group-header")!;

    header.dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
    expect(header.getAttribute("aria-expanded")).toBe("false");

    document.body.removeChild(group);
  });

  it("autoCollapseGroup sets aria-expanded=false", async () => {
    const { buildToolGroupShell, autoCollapseGroup } = await import("./tool-group.js");
    const group = buildToolGroupShell();
    document.body.appendChild(group);

    // Add 3 completed tool-call children (no data-start-ms) into the body
    // region, like the production mount path (messages-blocks mountToolCard).
    const { groupBody } = await import("./tool-group.js");
    for (let i = 0; i < 3; i++) {
      const call = document.createElement("div");
      call.className = "tool-call";
      call.dataset["kind"] = "read";
      call.dataset["title"] = `read${i}`;
      call.dataset["filename"] = `f${i}.ts`;
      call.dataset["mcpServer"] = "";
      groupBody(group).appendChild(call);
    }

    const header = group.querySelector(".tool-group-header")!;
    expect(header.getAttribute("aria-expanded")).toBe("true");

    autoCollapseGroup(group);
    expect(group.classList.contains("tool-group-auto-collapsed")).toBe(true);
    expect(header.getAttribute("aria-expanded")).toBe("false");

    document.body.removeChild(group);
  });
});
