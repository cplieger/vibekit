// The add-integration tab bar: one tab per mode, and switching mode never hides
// a tab.
//
// `data-mcp-mode` marks two different populations — one PANEL and one TAB BUTTON
// per mode — so a selector over the bare attribute reaches both. `setMode`'s
// panel loop was written that way, so it hid every tab button whose mode was not
// current; and because the default mode (`search`) had no tab button at all,
// opening the modal hid ALL of them. The bar was left as a 5px strip of its own
// padding and border, and nothing on screen could reopen it — which took the
// Remote URL and npm forms with it, the two the registry-search failure message
// tells the user to switch to.
//
// Both halves are pinned here: the markup covers all four modes, and the runtime
// leaves the four buttons visible whichever mode is active.

import { describe, it, expect, vi } from "vitest";
import indexHtml from "../static/index.html?raw";

// The panels' own initialisers reach modules with real network + signal wiring;
// this file is about which elements carry `hidden`, so they are stubbed out.
vi.mock("./actions/tools.js", () => ({ getToolsStatus: { dispatch: async () => null } }));
vi.mock("./tools.js", () => ({ installToolAndWait: async () => ({ ok: true }) }));

function modalMarkup(): HTMLElement {
  const start = indexHtml.indexOf('<div id="mcp-modal"');
  const end = indexHtml.indexOf("<!-- Git output popup -->", start);
  expect(start, "#mcp-modal not found").toBeGreaterThan(-1);
  expect(end, "the marker after the MCP modal not found").toBeGreaterThan(start);
  const host = document.createElement("div");
  host.innerHTML = indexHtml.slice(start, end);
  return host;
}

const MODES = ["search", "remote", "npm", "raw"] as const;

describe("MCP add-integration tab bar (static/index.html)", () => {
  it("declares one tab per panel mode", () => {
    const host = modalMarkup();
    const bar = host.querySelector<HTMLElement>("#mcp-modal-tabs");
    expect(bar, "#mcp-modal-tabs must exist").not.toBeNull();

    const tabbed = Array.from(bar?.querySelectorAll<HTMLElement>(".mcp-modal-tab") ?? []).map(
      (b) => b.dataset["mcpMode"],
    );
    const panelled = Array.from(host.querySelectorAll<HTMLElement>(".mcp-mode-panel")).map(
      (p) => p.dataset["mcpMode"],
    );

    // A mode with a panel and no tab is unreachable; a tab with no panel shows
    // an empty modal. Both sets are the same set.
    expect([...tabbed].sort()).toEqual([...MODES].sort());
    expect([...panelled].sort()).toEqual([...MODES].sort());
  });

  it("marks exactly one tab selected, and it is the mode the modal opens on", () => {
    const bar = modalMarkup().querySelector<HTMLElement>("#mcp-modal-tabs");
    const selected = Array.from(bar?.querySelectorAll<HTMLElement>(".mcp-modal-tab") ?? []).filter(
      (b) => b.getAttribute("aria-selected") === "true",
    );
    expect(selected).toHaveLength(1);
    expect(selected[0]?.dataset["mcpMode"]).toBe("search");
  });
});

describe("switching mode hides panels only", () => {
  it("leaves every tab button visible in all four modes", async () => {
    document.body.replaceChildren(...Array.from(modalMarkup().childNodes));
    const { setEditing, initModal } = await import("./mcp-panels.js");

    for (const mode of MODES) {
      setEditing({ id: "" });
      initModal({ mode, server: null });

      const hiddenTabs = Array.from(
        document.querySelectorAll<HTMLElement>(".mcp-modal-tab"),
      ).filter((b) => b.classList.contains("hidden"));
      expect(
        hiddenTabs.map((b) => b.dataset["mcpMode"]),
        `mode=${mode}`,
      ).toEqual([]);

      const shownPanels = Array.from(
        document.querySelectorAll<HTMLElement>(".mcp-mode-panel"),
      ).filter((p) => !p.classList.contains("hidden"));
      expect(
        shownPanels.map((p) => p.dataset["mcpMode"]),
        `mode=${mode}`,
      ).toEqual([mode]);

      const active = Array.from(document.querySelectorAll<HTMLElement>(".mcp-modal-tab")).filter(
        (b) => b.classList.contains("active"),
      );
      expect(
        active.map((b) => b.dataset["mcpMode"]),
        `mode=${mode}`,
      ).toEqual([mode]);
    }
  });
});
