// @vitest-environment happy-dom
// Tests for agent-terminal.ts — drives the real module via the SSE bus.
// Covers: per-terminal ANSI rendering + isolation, output capping (tail kept),
// exit code/signal surfacing, and terminal-card a11y.

import { describe, it, expect, beforeEach, vi } from "vitest";

// Mock the shared announce live region so we can assert announcements without
// dealing with its internal setTimeout gap.
const { announceMock } = vi.hoisted(() => ({ announceMock: vi.fn() }));
vi.mock("@cplieger/ui-primitives/announce", () => ({ announce: announceMock }));

import { dispatch } from "./bus.js";
import { initAgentTerminals, MAX_TERMINAL_OUTPUT_CHARS } from "./agent-terminal.js";

// initAgentTerminals subscribes to the bus once and does its DOM lookups at
// event time, so set the shell DOM up first, then init a single time.
document.body.innerHTML = `
  <div id="shell-panel">
    <button id="shell-fullscreen-btn"></button>
    <div id="shell-tabs"></div>
    <div id="shell-terminal"></div>
    <div id="agent-terminals" class="hidden"></div>
  </div>
`;
initAgentTerminals();

const ESC = "\u001b";

function created(id: string, command: string, args?: string[]): void {
  dispatch({ type: "terminal_created", chat_id: "", payload: { terminal_id: id, command, args } });
}
function output(id: string, data: string): void {
  dispatch({ type: "terminal_output", chat_id: "", payload: { terminal_id: id, data } });
}
function exited(id: string, exitCode?: number, signal?: string): void {
  dispatch({
    type: "terminal_exited",
    chat_id: "",
    payload: { terminal_id: id, exit_code: exitCode, signal },
  });
}

function pane(id: string): HTMLElement | null {
  return document.querySelector(`[data-term-id="${id}"]`);
}
function outputEl(id: string): HTMLPreElement | null {
  return (pane(id)?.querySelector("pre.agent-term-output") as HTMLPreElement | null) ?? null;
}
function statusEl(id: string): HTMLElement | null {
  return (pane(id)?.querySelector(".agent-term-status") as HTMLElement | null) ?? null;
}
function tabEl(id: string): HTMLElement | null {
  return document.querySelector(`[data-shell-tab="${id}"]`);
}

beforeEach(() => {
  announceMock.mockClear();
});

describe("terminal_created", () => {
  it("creates a labelled tab and card, unhides the container, and announces", () => {
    created("t-create", "npm", ["test"]);
    const card = pane("t-create");
    expect(card).not.toBeNull();
    expect(card!.getAttribute("role")).toBe("group");
    expect(card!.getAttribute("aria-label")).toContain("npm test");

    const region = outputEl("t-create");
    expect(region).not.toBeNull();
    expect(region!.getAttribute("role")).toBe("log");

    expect(tabEl("t-create")).not.toBeNull();
    expect(document.getElementById("agent-terminals")!.classList.contains("hidden")).toBe(false);
    expect(announceMock).toHaveBeenCalledWith(expect.stringContaining("npm test"));
  });
});

describe("terminal_output", () => {
  it("renders output with ANSI stripped into the <pre>", () => {
    created("t-out", "echo");
    output("t-out", `${ESC}[32mhello${ESC}[0m world`);
    expect(outputEl("t-out")!.textContent).toBe("hello world");
  });

  it("ignores output for an unknown terminal", () => {
    expect(() => output("t-unknown", "x")).not.toThrow();
  });

  it("caps rendered output, keeping the tail and dropping the front", () => {
    created("t-cap", "yes");
    output("t-cap", "A".repeat(MAX_TERMINAL_OUTPUT_CHARS));
    output("t-cap", "B".repeat(1000));
    const tail = "TAIL_MARKER_END";
    output("t-cap", tail);

    const text = outputEl("t-cap")!.textContent ?? "";
    expect(text.length).toBeLessThanOrEqual(MAX_TERMINAL_OUTPUT_CHARS);
    expect(text.endsWith(tail)).toBe(true);
    // The oldest filler ("A"…) must have been dropped from the front.
    expect(text.startsWith("A")).toBe(false);
  });
});

describe("terminal_exited", () => {
  it("marks a clean exit (code 0) with an ok footer and tab class", () => {
    created("t-ok", "true");
    exited("t-ok", 0);

    const status = statusEl("t-ok");
    expect(status).not.toBeNull();
    expect(status!.textContent).toContain("exited (code 0)");
    expect(status!.classList.contains("is-ok")).toBe(true);
    expect(tabEl("t-ok")!.classList.contains("term-exited-ok")).toBe(true);
    expect(announceMock).toHaveBeenCalledWith(expect.stringContaining("exited (code 0)"));
  });

  it("marks a non-zero exit code as an error", () => {
    created("t-err", "false");
    exited("t-err", 1);

    const status = statusEl("t-err")!;
    expect(status.textContent).toContain("exited (code 1)");
    expect(status.classList.contains("is-err")).toBe(true);
    expect(tabEl("t-err")!.classList.contains("term-exited-err")).toBe(true);
  });

  it("shows a signal death as killed (SIGNAL)", () => {
    created("t-sig", "sleep", ["100"]);
    exited("t-sig", undefined, "SIGTERM");

    const status = statusEl("t-sig")!;
    expect(status.textContent).toContain("killed (SIGTERM)");
    expect(status.classList.contains("is-err")).toBe(true);
    expect(announceMock).toHaveBeenCalledWith(expect.stringContaining("killed (SIGTERM)"));
  });

  it("ignores a duplicate exit for the same terminal", () => {
    created("t-dup", "cmd");
    exited("t-dup", 0);
    exited("t-dup", 1);

    // Only the first exit is recorded; no second footer is appended.
    const statuses = pane("t-dup")!.querySelectorAll(".agent-term-status");
    expect(statuses.length).toBe(1);
    expect(statuses[0]!.textContent).toContain("exited (code 0)");
  });
});

describe("per-terminal isolation", () => {
  it("keeps each terminal's output in its own pane", () => {
    created("t-iso-a", "cmdA");
    created("t-iso-b", "cmdB");
    output("t-iso-a", "alpha");
    output("t-iso-b", "beta");
    output("t-iso-a", "-again");

    expect(outputEl("t-iso-a")!.textContent).toBe("alpha-again");
    expect(outputEl("t-iso-b")!.textContent).toBe("beta");
  });
});

describe("fullscreen toggle", () => {
  it("mirrors the panel class onto aria-pressed (initialized false at wiring)", () => {
    const btn = document.getElementById("shell-fullscreen-btn") as HTMLButtonElement;
    const panel = document.getElementById("shell-panel")!;
    expect(btn.getAttribute("aria-pressed")).toBe("false");

    btn.click();
    expect(panel.classList.contains("shell-fullscreen")).toBe(true);
    expect(btn.getAttribute("aria-pressed")).toBe("true");

    btn.click();
    expect(panel.classList.contains("shell-fullscreen")).toBe(false);
    expect(btn.getAttribute("aria-pressed")).toBe("false");
  });
});
