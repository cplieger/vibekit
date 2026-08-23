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

import { EVENT_RENDER_MAP, buildEvent } from "./messages-events.js";
import type { Message } from "./types.js";

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

  it("boundary metadata snapshot", () => {
    const boundaries = Object.entries(EVENT_RENDER_MAP)
      .filter(([, s]) => s.kind === "boundary")
      .map(([kind, s]) => {
        if (s.kind !== "boundary") {
          throw new Error("unreachable");
        }
        return { kind, icon: s.icon, defaultLabel: s.defaultLabel };
      });

    expect(boundaries.length).toBeGreaterThan(0);
    expect(boundaries.map((b) => b.kind).sort()).toMatchSnapshot();
  });
});

describe("infra_safety_blocked event (Kiro Infrastructure-Safety enforce block)", () => {
  it("renders a red blocked boundary carrying the violated properties", () => {
    const node = buildEvent({
      id: "m1",
      role: "event",
      event_kind: "infra_safety_blocked",
      content: "no public S3 buckets; encrypt at rest",
      ts: 0,
    } as Message);
    expect(node).not.toBeNull();
    expect(node?.className).toContain("boundary-blocked");
    const text = node?.textContent ?? "";
    expect(text).toContain("Infrastructure Safety blocked");
    expect(text).toContain("no public S3 buckets; encrypt at rest");
  });

  it("falls back to a default label when the block carries no reason", () => {
    const node = buildEvent({
      id: "m2",
      role: "event",
      event_kind: "infra_safety_blocked",
      ts: 0,
    } as Message);
    expect(node?.textContent ?? "").toContain("Infrastructure Safety blocked a change");
  });
});

describe("interrupted event (turn cut short)", () => {
  it("renders the server's own reason, not a generic label", () => {
    const node = buildEvent({
      id: "i1",
      role: "event",
      event_kind: "interrupted",
      content: "Session refreshed, retrying",
      ts: 0,
    } as Message);
    expect(node).not.toBeNull();
    expect(node?.className).toContain("boundary");
    // The reason travelled on the wire and used to be discarded here, so the
    // divider blamed a server restart whatever had actually happened.
    expect(node?.textContent ?? "").toContain("Session refreshed, retrying");
  });

  it("falls back to a generic label when the event carries no content", () => {
    const node = buildEvent({
      id: "i2",
      role: "event",
      event_kind: "interrupted",
      ts: 0,
    } as Message);
    expect(node).not.toBeNull();
    expect(node?.textContent ?? "").toContain("Turn interrupted");
  });

  // Nothing in the tree detects a restart: the `.partial` sidecar that claim
  // came from is deleted. Pinned so the string cannot come back.
  it("never blames a server restart", () => {
    const node = buildEvent({
      id: "i3",
      role: "event",
      event_kind: "interrupted",
      ts: 0,
    } as Message);
    expect(node?.textContent ?? "").not.toContain("restart");
  });
});

describe("cancelled event", () => {
  it("stays invisible (expected user action, no badge)", () => {
    const node = buildEvent({ id: "c1", role: "event", event_kind: "cancelled", ts: 0 } as Message);
    expect(node).toBeNull();
  });
});

describe("compacted event summary", () => {
  it("renders the conversation summary in a collapsible disclosure", () => {
    const node = buildEvent({
      id: "cp1",
      role: "event",
      event_kind: "compacted",
      content: "The user asked to refactor the auth module; we split it into three files.",
      ts: 0,
    } as Message);
    expect(node).not.toBeNull();
    // The boundary marker is present...
    expect(node?.querySelector(".boundary")).not.toBeNull();
    // ...and the summary is surfaced (not dropped) in an expandable details.
    const details = node?.querySelector("details");
    expect(details).not.toBeNull();
    expect(details?.textContent ?? "").toContain("Conversation summary");
    expect(details?.textContent ?? "").toContain("split it into three files");
  });

  it("renders just the marker (no disclosure) when there is no summary", () => {
    const node = buildEvent({
      id: "cp2",
      role: "event",
      event_kind: "compacted",
      ts: 0,
    } as Message);
    expect(node).not.toBeNull();
    expect(node?.className).toContain("boundary-compacted");
    expect(node?.querySelector("details")).toBeNull();
    expect(node?.textContent ?? "").toContain("Conversation compacted");
  });
});
