// @vitest-environment happy-dom
//
// The picker's visibility is an EFFECT over store state, so these tests drive
// the store and read the `hidden` class the way a person reads the screen.
//
// Why this file exists: the picker is a full-bleed overlay, and its visibility
// used to be two imperative calls in chat.ts. Every sender that did not route
// through one of them left it covering the transcript, which is what `/goal` in
// a fresh chat did. Each case below is one of those paths.
import { describe, it, expect, beforeAll, beforeEach } from "vitest";
import { flushSync } from "@cplieger/reactive";

import { setSessions, setActive, setThinking } from "./store.js";
import { initModelPicker, setPickerModels } from "./picker.js";
import type { Session, Message } from "./types.js";

function makeSession(overrides: Partial<Session> = {}): Session {
  return {
    id: "chat-1",
    name: "test",
    model: "claude-opus",
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
    ...overrides,
  };
}

function userMessage(text: string): Message {
  return { id: "m-1", role: "user", content: text, ts: 0 } as Message;
}

function hidden(): boolean {
  const el = document.getElementById("model-picker");
  if (el === null) {
    throw new Error("no #model-picker in the fixture");
  }
  return el.classList.contains("hidden");
}

describe("model picker visibility", () => {
  // The module captures its host through the DOM registry on first use and the
  // effect is installed once, so the fixture has to outlive every case.
  beforeAll(() => {
    document.body.innerHTML = `
      <div id="model-picker" class="hidden">
        <div class="picker-label"></div>
        <div class="picker-grid"></div>
      </div>`;
    setPickerModels([
      { model_id: "claude-opus", model_name: "Claude Opus", rate_multiplier: 1 },
      { model_id: "claude-sonnet", model_name: "Claude Sonnet", rate_multiplier: 1 },
    ]);
    initModelPicker(() => {
      // Selection is model-switcher's job; this file only tests visibility.
    });
  });

  beforeEach(() => {
    setSessions([makeSession()]);
    setActive("chat-1");
    flushSync();
  });

  it("shows for an empty idle chat", () => {
    expect(hidden()).toBe(false);
  });

  // The overlay must not sit over a conversation. `message_count` is the
  // server's count and `messages` the paginated window; either one being
  // non-empty means there is something to read underneath.
  it("hides once the chat holds a message", () => {
    setSessions([makeSession({ messages: [userMessage("hello")] })]);
    setActive("chat-1");
    flushSync();
    expect(hidden()).toBe(true);
  });

  it("hides for a chat the server already counts messages for", () => {
    setSessions([makeSession({ message_count: 4 })]);
    setActive("chat-1");
    flushSync();
    expect(hidden()).toBe(true);
  });

  // THE REGRESSION. A send sets `thinking` synchronously, well before the
  // server's message_appended echo arrives, so this is what closes the overlay
  // for a sender that never calls a hide function: the goal row, the tangent
  // row, and anything added later.
  it("hides as soon as a turn starts, before any message lands", () => {
    setThinking("chat-1", true);
    flushSync();
    expect(hidden()).toBe(true);
  });

  // A failed send leaves the server idle and the chat promptable, and by then
  // the user message is persisted — so the picker must not come back and cover
  // the transcript when thinking clears.
  it("stays hidden when a turn ends on a chat that now has a message", () => {
    setSessions([makeSession({ messages: [userMessage("/goal do the thing")] })]);
    setActive("chat-1");
    setThinking("chat-1", true);
    flushSync();
    setThinking("chat-1", false);
    flushSync();
    expect(hidden()).toBe(true);
  });

  // `isEmptyChat(undefined)` is true by its own contract, so the absent case
  // needs its own guard: a chat that does not exist has no model to choose, and
  // the pre-session surface is the model pill's inline list.
  it("hides when no chat is active", () => {
    setActive("");
    flushSync();
    expect(hidden()).toBe(true);
  });
});
