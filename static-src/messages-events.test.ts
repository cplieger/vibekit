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
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  newOpID: vi.fn(() => "op-test"),
}));
vi.mock("./store.js", async () => ({
  ...(await import("./__test-helpers__/store-mock.js")).storeMock,
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
  // THE OWNERSHIP RULE, from the divider's side. Both of these cases used to
  // assert the inverse — that the divider rendered the server's own reason — and
  // that is exactly what put the sentence on screen TWICE about 50px apart:
  // `turnFailureText`'s first source reads THIS row's content into the card-level
  // `.turn-notice`, and the notice is the surface present in both fold states, so
  // it is the one that keeps the prose. The divider marks the boundary and names
  // its kind.
  //
  // Nothing is lost, and `turns.node.test.ts` is where that half is pinned: the
  // notice still returns this row's content verbatim.
  it("marks the boundary and does NOT repeat the notice's sentence", () => {
    const node = buildEvent({
      id: "i1",
      role: "event",
      event_kind: "interrupted",
      content: "Session refreshed, retrying",
      ts: 0,
    } as Message);
    expect(node).not.toBeNull();
    expect(node?.className).toContain("boundary");
    expect(node?.textContent ?? "").toContain("Turn interrupted");
    expect(node?.textContent ?? "").not.toContain("Session refreshed, retrying");
  });

  // The 2026-08 case — a throttled or capacity-refused turn — asserted the same
  // way round. The reason is long, which is the second argument for one home: two
  // copies of a 180-character upstream sentence in one card is a wall of it.
  it("does not repeat a long model-backend failure reason either", () => {
    const node = buildEvent({
      id: "i0",
      role: "event",
      event_kind: "interrupted",
      content:
        "Too many requests, please wait before trying again. kiro-cli already " +
        "retried; waiting a moment before resending is the only thing that helps. (request req-9)",
      ts: 0,
    } as Message);
    expect(node?.textContent ?? "").not.toContain("Too many requests");
    expect(node?.textContent ?? "").not.toContain("(request req-9)");
    expect(node?.textContent ?? "").toContain("Turn interrupted");
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
  const compacted = (content: string): HTMLDetailsElement =>
    buildEvent({
      id: "cp1",
      role: "event",
      event_kind: "compacted",
      content,
      ts: 0,
    } as Message) as HTMLDetailsElement;

  it("makes the marker itself the summary's trigger", () => {
    const node = compacted("The user asked to refactor auth; we split it into three files.");
    expect(node.tagName).toBe("DETAILS");
    expect(node.querySelector(".boundary")).toBeNull();
    const head = node.querySelector("summary.compaction-head");
    expect(head?.textContent ?? "").toContain("Conversation compacted");
    // The app's own joiner, as plain text (status.ts's effort pill is the shape).
    expect(head?.textContent ?? "").toContain("· summary");
  });

  // The row sets `list-style: none`, so this glyph is the only thing on screen
  // saying it opens.
  it("carries the app's disclosure chevron", () => {
    const node = compacted("A summary.");
    expect(node.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    expect(node.querySelector("summary.compaction-head > .disclosure-chevron")).not.toBeNull();
  });

  it("renders the summary as markdown on first open, and not before", async () => {
    const node = compacted("## Goal\n\nSplit `auth` into three files.\n");
    const body = node.querySelector(".compaction-body");
    expect(body).not.toBeNull();
    expect(body?.textContent).toBe("");

    node.open = true;
    // `toggle` is queued, not dispatched synchronously.
    await vi.waitFor(() => {
      expect(body?.querySelector("h2")?.textContent).toBe("Goal");
    });
    expect(body?.querySelector("code")?.textContent).toBe("auth");
    expect(body?.textContent ?? "").not.toContain("##");
    expect(body?.textContent ?? "").not.toContain("`");
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

// A workflow step's `send_message` notification. The dock holds the question in
// MEMORY, so this row is the only durable copy of it — which is why the content is
// the label rather than being dropped for a fixed sentence, the way a workflow
// progress row is.
describe("step_notice event (a workflow step spoke to the reader)", () => {
  it("renders the step's own words rather than a fixed sentence", () => {
    const node = buildEvent({
      id: "n1",
      role: "event",
      event_kind: "step_notice",
      content: "Ship it?",
      ts: 0,
    } as Message);
    expect(node).not.toBeNull();
    // A boundary rather than a bubble: the author is neither side of the
    // conversation, so it takes the neutral face the other markers use.
    expect(node?.className).toContain("boundary-switched");
    expect(node?.querySelector(".boundary-label")?.textContent).toBe("Step: Ship it?");
  });

  it("falls back to a label when the frame carried no text", () => {
    const node = buildEvent({
      id: "n2",
      role: "event",
      event_kind: "step_notice",
      ts: 0,
    } as Message);
    expect(node).not.toBeNull();
    expect(node?.textContent ?? "").toContain("A workflow step sent a message");
  });
});
