// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

const mockActive = {
  available_commands: [
    { name: "/compact", description: "Compact history" },
    { name: "/context", description: "Add context" },
    { name: "/help", description: "Show help" },
  ],
  available_prompts: [],
};
vi.mock("./store.js", () => ({
  getActive: () => mockActive,
  getActiveId: () => "chat1",
}));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(async () => null) }));
vi.mock("./dom.js", () => {
  const input = document.createElement("textarea");
  return {
    $: new Proxy({ promptInput: input }, {
      get: (t, p) => (p in t ? (t as Record<string, unknown>)[p as string] : document.createElement("div")),
    }),
  };
});

import { $ } from "./dom.js";

// Import after mocks
const { initCommandsMenu } = await import("./commands-menu.js");

describe("CommandsMenuController", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    initCommandsMenu();
  });

  it("'/' triggers popover with all commands shown", () => {
    const input = $.promptInput as HTMLTextAreaElement;
    input.value = "/";
    input.dispatchEvent(new Event("input"));
    const popover = document.querySelector(".commands-popover");
    expect(popover).not.toBeNull();
    expect(popover!.classList.contains("hidden")).toBe(false);
    const rows = popover!.querySelectorAll(".commands-popover-row");
    expect(rows.length).toBe(3);
  });

  it("'/co' filters to commands starting with 'co'", () => {
    const input = $.promptInput as HTMLTextAreaElement;
    input.value = "/co";
    input.dispatchEvent(new Event("input"));
    const popover = document.querySelector(".commands-popover");
    expect(popover).not.toBeNull();
    const rows = popover!.querySelectorAll(".commands-popover-row");
    expect(rows.length).toBe(2); // compact, context
  });

  it("input without '/' closes popover", () => {
    const input = $.promptInput as HTMLTextAreaElement;
    input.value = "/";
    input.dispatchEvent(new Event("input"));
    input.value = "hello";
    input.dispatchEvent(new Event("input"));
    const popover = document.querySelector(".commands-popover");
    expect(popover!.classList.contains("hidden")).toBe(true);
  });

  it("'/unknown' shows empty list (popover closed)", () => {
    const input = $.promptInput as HTMLTextAreaElement;
    input.value = "/unknown";
    input.dispatchEvent(new Event("input"));
    const popover = document.querySelector(".commands-popover");
    expect(popover!.classList.contains("hidden")).toBe(true);
  });
});
