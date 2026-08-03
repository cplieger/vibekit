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

const { handleTypedCommand } = await import("./typed-commands.js");

beforeEach(() => {
  dispatch.mockReset();
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
