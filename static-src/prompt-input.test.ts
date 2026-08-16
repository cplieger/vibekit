// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Prompt-input history cycling: the draft round trip.
//
// ArrowUp saves whatever the user had typed and starts walking back through
// their own prompts; ArrowDown off the newest one and Escape both have to hand
// that text back. The defect these pin (issue #956): ArrowDown read the saved
// draft AFTER exitCycling() had zeroed it, so returning to the draft emptied
// the box while Escape, which saved to a local first, worked. Both exits now go
// through restoreDraft(), and the two assertions below are what stops one of
// them drifting away from the other again.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";
import { initPromptInput } from "./prompt-input.js";
import { setSessions, setActive } from "./store.js";
import type { Session } from "./types.js";

const CHAT = "c1";

function makeSession(prompts: string[]): Session {
  return {
    id: CHAT,
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
    message_count: prompts.length,
    messages: prompts.map((content, i) => ({
      id: `m${String(i)}`,
      role: "user",
      content,
      ts: i,
    })),
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** The prompt bar is a module singleton bound to these three elements at init,
 *  so the DOM is staged once and every test reuses it. Replacing the nodes
 *  between tests would leave the keydown listener on the departed textarea. */
beforeAll(() => {
  document.body.innerHTML = `
    <form id="prompt-form">
      <textarea id="prompt-input"></textarea>
      <button id="send-btn" type="submit"></button>
    </form>`;
  initPromptInput(vi.fn(), vi.fn());
});

function input(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}

/** Type into the box the way a user does: text, a caret at its end, and the
 *  `input` event a keystroke fires. That event is what ends cycling, so a
 *  helper that skips it leaves the controller's index pointing into history and
 *  the next test starts mid-cycle. */
function type(text: string): void {
  const el = input();
  el.value = text;
  el.setSelectionRange(text.length, text.length);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

function press(key: string): void {
  input().dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
}

beforeEach(() => {
  // Newest prompt first is what userPrompts() produces, so ArrowUp reaches
  // "newest" on the first press and "oldest" on the second.
  setSessions([makeSession(["oldest", "newest"])]);
  setActive(CHAT);
  type("");
});

describe("prompt history cycling", () => {
  it("gives the draft back on ArrowDown off the newest prompt", () => {
    type("half-written thought");
    press("ArrowUp");
    expect(input().value).toBe("newest");

    press("ArrowDown");
    expect(input().value).toBe("half-written thought");
  });

  it("gives the draft back on Escape", () => {
    type("half-written thought");
    press("ArrowUp");
    press("ArrowUp");
    expect(input().value).toBe("oldest");

    press("Escape");
    expect(input().value).toBe("half-written thought");
  });

  it("steps to the newer prompt when ArrowDown is not at the newest", () => {
    type("half-written thought");
    press("ArrowUp");
    press("ArrowUp");
    press("ArrowDown");
    expect(input().value).toBe("newest");
  });

  it("restores an empty draft without falling back to a prompt", () => {
    press("ArrowUp");
    expect(input().value).toBe("newest");

    press("ArrowDown");
    expect(input().value).toBe("");
  });

  it("re-captures the draft on a second cycle", () => {
    type("first draft");
    press("ArrowUp");
    press("ArrowDown");
    expect(input().value).toBe("first draft");

    type("second draft");
    press("ArrowUp");
    press("ArrowDown");
    expect(input().value).toBe("second draft");
  });
});
