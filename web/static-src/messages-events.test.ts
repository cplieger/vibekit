// @vitest-environment happy-dom
import { describe, it, expect, vi } from "vitest";

// Set up minimal DOM that transitive imports need.
document.body.innerHTML = '<div id="messages"></div>';

// Mock heavy DOM-dependent modules imported transitively by messages-events.ts.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
vi.mock("./tool-group.js", () => ({
  breakToolGroup: vi.fn(),
  trackInProgress: vi.fn(),
}));
vi.mock("./tool-card.js", () => ({
  buildToolCard: vi.fn(() => document.createElement("div")),
}));
vi.mock("./transport.js", () => ({
  send: vi.fn(),
}));
vi.mock("./store.js", () => ({
  state: { messages: [], chatID: "" },
}));

import { EVENT_RENDER_MAP } from "./messages-events.js";

describe("EVENT_RENDER_MAP exhaustiveness", () => {
  it("every boundary entry has non-empty icon and defaultLabel", () => {
    for (const [kind, strategy] of Object.entries(EVENT_RENDER_MAP)) {
      if (strategy.kind === "boundary") {
        expect(strategy.icon, `${kind}.icon`).toBeTruthy();
        expect(strategy.defaultLabel, `${kind}.defaultLabel`).toBeTruthy();
      }
    }
  });

  it("no entry has undefined/null values for required fields", () => {
    for (const [kind, strategy] of Object.entries(EVENT_RENDER_MAP)) {
      expect(strategy.kind, `${kind}.kind`).toBeDefined();
      if (strategy.kind === "boundary") {
        expect(strategy.boundary, `${kind}.boundary`).toBeDefined();
        expect(typeof strategy.icon, `${kind}.icon type`).toBe("string");
        expect(typeof strategy.defaultLabel, `${kind}.defaultLabel type`).toBe("string");
      }
    }
  });

  it("inline render functions do not throw on minimal Message", () => {
    const minimalMessage = {
      id: "test-id",
      role: "event" as const,
      content: "test content",
      ts: Date.now(),
    };

    for (const [kind, strategy] of Object.entries(EVENT_RENDER_MAP)) {
      if (strategy.kind === "inline") {
        expect(() => strategy.render(minimalMessage as any), `${kind} threw`).not.toThrow();
      }
    }
  });

  it("inline render functions return HTMLElement or null", () => {
    const minimalMessage = {
      id: "test-id",
      role: "event" as const,
      content: "test content",
      ts: Date.now(),
    };

    for (const [kind, strategy] of Object.entries(EVENT_RENDER_MAP)) {
      if (strategy.kind === "inline") {
        const result = strategy.render(minimalMessage as any);
        if (result !== null) {
          expect(result, `${kind} result`).toBeInstanceOf(HTMLElement);
        }
      }
    }
  });

  it("boundary metadata snapshot", () => {
    const boundaries = Object.entries(EVENT_RENDER_MAP)
      .filter(([, s]) => s.kind === "boundary")
      .map(([kind, s]) => {
        if (s.kind !== "boundary") throw new Error("unreachable");
        return { kind, icon: s.icon, defaultLabel: s.defaultLabel };
      });

    expect(boundaries.length).toBeGreaterThan(0);
    expect(boundaries.map((b) => b.kind).sort()).toMatchSnapshot();
  });
});
