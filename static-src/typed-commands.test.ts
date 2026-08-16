// @vitest-environment happy-dom
// The typed-command table. Small on purpose, and what matters is the BOUNDARY:
// everything the table does not claim must fall through untouched, or vibekit
// starts silently swallowing text KAS itself parses.
import { describe, it, expect, vi, beforeEach } from "vitest";

const dispatch = vi.fn();
vi.mock("./actions/chat.js", () => ({
  compactChat: {
    dispatch: (args: unknown) => {
      dispatch(args);
    },
  },
}));

const toasts: string[] = [];
vi.mock("./toast.js", () => ({
  showToast: (msg: string) => {
    toasts.push(msg);
    return () => undefined;
  },
}));

const { handleTypedCommand } = await import("./typed-commands.js");
const store = await import("./store.js");

beforeEach(() => {
  dispatch.mockReset();
  toasts.length = 0;
});

describe("handleTypedCommand", () => {
  it("claims /compact and dispatches the native verb", () => {
    expect(handleTypedCommand("c1", "/compact")).toBe(true);
    expect(dispatch).toHaveBeenCalledWith({ chatID: "c1" });
  });

  it("tolerates surrounding whitespace and case", () => {
    expect(handleTypedCommand("c1", "  /COMPACT  ")).toBe(true);
    expect(dispatch).toHaveBeenCalledTimes(1);
  });

  // `/compact` is a command; `/compact this file` is a sentence that begins with
  // one, and guessing which was meant is how a table like this starts lying.
  it("does not claim a verb with arguments", () => {
    expect(handleTypedCommand("c1", "/compact this file")).toBe(false);
    expect(dispatch).not.toHaveBeenCalled();
  });

  it.each([
    ["ordinary prose", "please compact the context"],
    ["a bare slash", "/"],
    ["an unknown verb", "/help"],
    ["a path that looks like a command", "/usr/bin/env"],
    ["empty", ""],
  ])("falls through for %s", (_desc, text) => {
    expect(handleTypedCommand("c1", text)).toBe(false);
    expect(dispatch).not.toHaveBeenCalled();
  });

  // Slash text KAS parses itself must reach KAS. vibekit deliberately does not
  // enumerate that list — it is KAS's and it moves.
  it("leaves other slash commands to KAS", () => {
    expect(handleTypedCommand("c1", "/goal")).toBe(false);
    expect(dispatch).not.toHaveBeenCalled();
  });

  it("refuses without a chat", () => {
    expect(handleTypedCommand("", "/compact")).toBe(false);
    expect(dispatch).not.toHaveBeenCalled();
  });
});

// `/drop` is the one verb that asks the engine for nothing. That is its whole
// contract, so the tests assert BOTH halves: the composer leaves steer mode, and
// no command travels.
describe("handleTypedCommand /drop", () => {
  function seedThinking(...ids: string[]): void {
    store.setSessions(
      ids.map((id) => ({
        id,
        name: "test",
        model: "",
        acp_session_id: "",
        current_mode_id: "",
        available_modes: [],
        available_models: [],
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
      })),
    );
    store.setActive(ids[0] ?? "");
    for (const id of ids) {
      store.setThinking(id, true);
    }
  }

  it("returns the composer to prompt mode by clearing thinking", () => {
    seedThinking("c1");
    expect(store.isThinking("c1")).toBe(true);
    expect(handleTypedCommand("c1", "/drop")).toBe(true);
    // This is what makes the next Send a PROMPT rather than a steer: submit.ts
    // branches on exactly this read.
    expect(store.isThinking("c1")).toBe(false);
  });

  it("dispatches no command, so it needs nothing from the engine", () => {
    seedThinking("c1");
    handleTypedCommand("c1", "/drop");
    expect(dispatch).not.toHaveBeenCalled();
  });

  it("says the agent was not told to stop", () => {
    seedThinking("c1");
    handleTypedCommand("c1", "/drop");
    expect(toasts.at(-1)).toContain("not told to stop");
  });

  it("claims the verb on an idle chat rather than sending it to the model", () => {
    seedThinking("c1");
    store.setThinking("c1", false);
    expect(handleTypedCommand("c1", "/drop")).toBe(true);
    expect(toasts.at(-1)).toContain("No turn is running");
  });

  it("touches only the named chat", () => {
    seedThinking("c1", "c2");
    handleTypedCommand("c2", "/drop");
    expect(store.isThinking("c1")).toBe(true);
    expect(store.isThinking("c2")).toBe(false);
  });

  it("does not claim /drop with arguments", () => {
    seedThinking("c1");
    expect(handleTypedCommand("c1", "/drop this turn")).toBe(false);
    expect(store.isThinking("c1")).toBe(true);
  });
});
