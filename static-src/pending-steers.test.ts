// @vitest-environment happy-dom
//
// The chip row is a pure projection of `session.steers`, so these tests drive the
// store the way the SSE handlers do and read the DOM the way a person does.
import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";
import { flushSync } from "@cplieger/reactive";

// The Discard control dispatches an action; the row's rendering is what is under
// test, and the real action would pull the framework and the transport in behind
// it.
vi.mock("./actions/chat.js", () => ({ clearSteers: { dispatch: vi.fn() } }));

import { setSessions, setActive, recordSteerQueued, markSteerInjected } from "./store.js";
import { initPendingSteers } from "./pending-steers.js";
import type { Session } from "./types.js";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: 0,
    messages: [],
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

function chips(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>("#queued-row .queued-prompt"));
}

function firstChip(): HTMLElement {
  const el = chips()[0];
  if (el === undefined) {
    throw new Error("no chip rendered");
  }
  return el;
}

describe("pending-steers chips", () => {
  // The row element is captured once, by the module's own idempotent init, so it
  // has to outlive every case: replacing it per test would leave the effect
  // painting into a detached node.
  beforeAll(() => {
    document.body.innerHTML = `<ul id="queued-row" class="queued-row hidden"></ul>`;
    initPendingSteers();
  });

  beforeEach(() => {
    // A fresh session has no steers, so the render empties the row: the store is
    // the only input, which is what makes the reset one line.
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
    flushSync();
    expect(chips()).toHaveLength(0);
  });

  it("shows a waiting steer with no verdict on it", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead" });
    flushSync();

    const chip = firstChip();
    expect(chip.classList.contains("steer-read")).toBe(false);
    expect(chip.querySelector(".queued-ack")).toBeNull();
    expect(chip.getAttribute("aria-label")).toBe("Waiting for the agent: use tabs instead");
  });

  // The read state on its own is what the row showed before: a check glyph and
  // nothing about the outcome. It stays correct for an agent that emits no
  // acknowledgement marker.
  it("shows read with no ack when the agent said nothing about it", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead" });
    markSteerInjected("chat-1", "steer-1", "use tabs instead");
    flushSync();

    const chip = firstChip();
    expect(chip.classList.contains("steer-read")).toBe(true);
    expect(chip.querySelector(".queued-ack")).toBeNull();
    expect(chip.getAttribute("aria-label")).toBe("Read by the agent: use tabs instead");
  });

  // The packet's whole purpose: the agent's own statement of what it did reaches
  // the chip rather than being discarded with the marker.
  it("carries what the agent did once the acknowledgement lands", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main" });
    markSteerInjected("chat-1", "steer-1", "actually target main");
    markSteerInjected("chat-1", "steer-1", "", "rebased onto main instead");
    flushSync();

    const chip = firstChip();
    expect(chip.querySelector(".queued-ack")?.textContent).toBe("rebased onto main instead");
    // The steer's own text is still the label: the chip has to stay
    // identifiable as the message the user sent.
    expect(chip.querySelector(".queued-text")?.textContent).toBe("actually target main");
    expect(chip.getAttribute("aria-label")).toBe(
      "Read by the agent: actually target main. The agent did: rebased onto main instead",
    );
    // Both halves in full on the tooltip, because both visible strings are
    // truncated.
    expect(chip.getAttribute("title")).toBe("actually target main\n\nrebased onto main instead");
  });

  // The repaint key has to include the ack. The computed dedups by string value,
  // so an ack arriving on an already-read steer changes nothing else about the
  // session and would otherwise paint nothing.
  it("repaints when only the ack changed", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    markSteerInjected("chat-1", "steer-1", "one");
    flushSync();
    expect(firstChip().querySelector(".queued-ack")).toBeNull();

    markSteerInjected("chat-1", "steer-1", "", "did the thing");
    flushSync();
    expect(firstChip().querySelector(".queued-ack")?.textContent).toBe("did the thing");
  });

  it("truncates a long acknowledgement and keeps the whole of it on the tooltip", () => {
    const long = "reworked the entire module boundary and moved the parser into its own package";
    recordSteerQueued("chat-1", { id: "steer-1", text: "short" });
    markSteerInjected("chat-1", "steer-1", "short");
    markSteerInjected("chat-1", "steer-1", "", long);
    flushSync();

    const shown = firstChip().querySelector(".queued-ack")?.textContent ?? "";
    expect(shown.length).toBeLessThan(long.length);
    expect(shown.endsWith("\u2026")).toBe(true);
    expect(firstChip().getAttribute("title")).toContain(long);
  });

  // Truncation is a layout answer to a fixed-width chip, and the accessible
  // name has no width. Sending the preview left a screen-reader user with only
  // the 60-character summaries while the full text lived solely in a hover
  // tooltip, which is not an accessible alternative to anything.
  it("carries BOTH strings in full on the accessible name when the chip truncates", () => {
    const longSteer =
      "stop rewriting the parser and instead widen the existing front-matter struct with the missing field";
    const longAck =
      "reworked the entire module boundary and moved the parser into its own package as asked";
    recordSteerQueued("chat-1", { id: "steer-1", text: longSteer });
    markSteerInjected("chat-1", "steer-1", longSteer);
    markSteerInjected("chat-1", "steer-1", "", longAck);
    flushSync();

    const chip = firstChip();
    // Both visible spans are still cut, so this is the truncating case.
    expect((chip.querySelector(".queued-text")?.textContent ?? "").length).toBeLessThan(
      longSteer.length,
    );
    expect((chip.querySelector(".queued-ack")?.textContent ?? "").length).toBeLessThan(
      longAck.length,
    );

    const label = chip.getAttribute("aria-label") ?? "";
    expect(label).toContain(longSteer);
    expect(label).toContain(longAck);
    expect(label).not.toContain("\u2026");
    // The state stays in words: the glyph is the only other channel.
    expect(label.startsWith("Read by the agent:")).toBe(true);
  });

  // A waiting chip has one string, and it is subject to the same rule.
  it("carries a long waiting steer in full on the accessible name", () => {
    const longSteer =
      "actually target the release branch rather than main, and leave the changelog alone for now";
    recordSteerQueued("chat-1", { id: "steer-1", text: longSteer });
    flushSync();

    const label = firstChip().getAttribute("aria-label") ?? "";
    expect(label).toBe(`Waiting for the agent: ${longSteer}`);
  });

  // Collapsed, not cut: a multi-line steer is announced as one string.
  it("collapses whitespace in the accessible name without shortening it", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first line\n\n   second line" });
    flushSync();

    expect(firstChip().getAttribute("aria-label")).toBe(
      "Waiting for the agent: first line second line",
    );
  });

  // Each steer's verdict belongs to that steer. Two answered in one response is
  // the case where a shared render would put one agent's answer on the other's
  // message.
  it("keeps each steer's acknowledgement on its own chip", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first ask" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second ask" });
    markSteerInjected("chat-1", "steer-1", "first ask", "answered the first");
    markSteerInjected("chat-1", "steer-2", "second ask", "answered the second");
    flushSync();

    const rendered = chips().map((c) => [
      c.querySelector(".queued-text")?.textContent,
      c.querySelector(".queued-ack")?.textContent,
    ]);
    expect(rendered).toEqual([
      ["first ask", "answered the first"],
      ["second ask", "answered the second"],
    ]);
  });
});
