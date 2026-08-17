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
import { initPromptInput, sendComposer } from "./prompt-input.js";
import { setSessions, setActive } from "./store.js";
import type { Session } from "./types.js";

const CHAT = "c1";

const submitted: string[] = [];

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
  // The action attribute is the real markup's backstop against a native form
  // submission, and it is staged here so a regression shows up as a navigation
  // attempt in this suite rather than only as a browser console violation.
  document.body.innerHTML = `
    <form id="prompt-form" action="javascript:void 0">
      <textarea id="prompt-input"></textarea>
      <button id="send-btn" type="submit"></button>
    </form>`;
  initPromptInput((text: string) => {
    submitted.push(text);
  }, vi.fn());
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
  submitted.length = 0;
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

// ---------------------------------------------------------------------------
// The send path. Three gestures (the form's submit, Enter, the keyboard
// shortcut) reach ONE function, and nothing fakes a DOM event to get there.
//
// The defect these pin: Enter and the shortcut ran
// `form.dispatchEvent(new Event("submit"))`, and `new Event()` is
// `cancelable: false`, so the submit handler's preventDefault() was a no-op and
// the browser performed the form's NATIVE submission on every send. What kept
// that from being a page navigation was the `action="javascript:void 0"` backstop
// plus CSP `form-action 'self'`, which downgraded it to a console violation — the
// exact failure that backstop's comment predicts.
// ---------------------------------------------------------------------------
describe("send", () => {
  it("sends the trimmed text on Enter", () => {
    expect.assertions(1);
    type("  hello  ");
    press("Enter");
    expect(submitted).toEqual(["hello"]);
  });

  it("clears the box after sending", () => {
    expect.assertions(1);
    type("hello");
    press("Enter");
    expect(input().value).toBe("");
  });

  it("sends once per Enter, not once per gesture-plus-event", () => {
    // A synthetic submit event dispatched from the keydown handler ran the send
    // AND then let the native submission proceed; one keystroke has to mean one
    // send and nothing else.
    expect.assertions(1);
    type("hello");
    press("Enter");
    expect(submitted).toHaveLength(1);
  });

  it("refuses an empty or whitespace-only box", () => {
    expect.assertions(1);
    type("   ");
    press("Enter");
    expect(submitted).toEqual([]);
  });

  it("does not dispatch a non-cancelable submit event at the form", () => {
    // The cause, asserted directly: a `cancelable: false` submit event is one
    // whose preventDefault cannot work, so the native submission follows it.
    expect.assertions(1);
    const uncancelable = vi.fn();
    const form = document.getElementById("prompt-form") as HTMLFormElement;
    const listener = (e: Event): void => {
      if (!e.cancelable) {
        uncancelable();
      }
    };
    form.addEventListener("submit", listener);
    type("hello");
    press("Enter");
    form.removeEventListener("submit", listener);
    expect(uncancelable).not.toHaveBeenCalled();
  });

  it("attempts no native form submission on Enter", () => {
    // The consequence, asserted through the DOM's own submit machinery:
    // requestSubmit/submit is what a native submission would reach.
    expect.assertions(1);
    const form = document.getElementById("prompt-form") as HTMLFormElement;
    const native = vi.fn();
    const realSubmit = form.submit.bind(form);
    form.submit = native;
    try {
      type("hello");
      press("Enter");
    } finally {
      form.submit = realSubmit;
    }
    expect(native).not.toHaveBeenCalled();
  });

  it("sends through the form's own submit event too", () => {
    // The send button is type=submit, so a click arrives this way.
    expect.assertions(1);
    type("hello");
    const form = document.getElementById("prompt-form") as HTMLFormElement;
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    expect(submitted).toEqual(["hello"]);
  });

  it("sends through the exported shortcut entry point", () => {
    expect.assertions(1);
    type("hello");
    sendComposer();
    expect(submitted).toEqual(["hello"]);
  });

  it("ends history cycling when it sends", () => {
    expect.assertions(3);
    type("draft");
    press("ArrowUp");
    expect(input().value).toBe("newest");
    press("Enter");
    expect(submitted).toEqual(["newest"]);
    // Cycling ended, so ArrowUp is a fresh walk from the top rather than a step
    // deeper into the previous cycle.
    press("ArrowUp");
    expect(input().value).toBe("newest");
  });
});
