// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for submit.ts — what Send MEANS, which depends on whether a turn is
// already running. Drives the REAL store (so the steer projection is
// observable) with the send primitive, the steer action and the attachment row
// mocked at the module boundary.
//
// The cases that matter here are the ones where the old queue was wrong:
//   - a message typed mid-turn reaches the RUNNING turn, not the next one
//   - a 409 (a turn starting underneath the send) steers rather than buffering
//   - nothing appears on screen until the server's own frame says so
//   - attachments degrade to path references, because a steer is a plain string
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

// Declared via vi.hoisted so they exist when the hoisted vi.mock factories run.
const { mockSendPromptTo, mockSteer, mockTakeAttachments, mockAddAttachment, mockTypedCommand } =
  vi.hoisted(() => ({
    mockSendPromptTo: vi.fn(),
    mockSteer: vi.fn(),
    mockTakeAttachments: vi.fn(() => [] as unknown[]),
    mockAddAttachment: vi.fn(),
    mockTypedCommand: vi.fn(() => false),
  }));

vi.mock("./chat-commands.js", () => ({ sendPromptTo: mockSendPromptTo }));
vi.mock("./actions/chat.js", () => ({ steerChat: { dispatch: mockSteer } }));
vi.mock("./typed-commands.js", () => ({ handleTypedCommand: mockTypedCommand }));
vi.mock("./attachments.js", () => ({
  takeAttachments: mockTakeAttachments,
  addAttachment: mockAddAttachment,
}));

import { submitPrompt } from "./submit.js";
import { setSessions, setActive, setThinking, steerCount } from "./store.js";
import type { Session } from "./types.js";

function makeSession(id: string): Session {
  return {
    id,
    name: "test",
    model: "claude",
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

function resetStore(id: string): void {
  setSessions([makeSession(id)]);
  setActive(id);
}

/** The steer text passed to the action on the Nth dispatch. */
function steeredText(call = 0): string {
  return (mockSteer.mock.calls[call]?.[0] as { text: string } | undefined)?.text ?? "";
}

beforeEach(() => {
  vi.clearAllMocks();
  mockTakeAttachments.mockReturnValue([]);
  mockTypedCommand.mockReturnValue(false);
  mockSteer.mockResolvedValue({});
});

describe("submitPrompt on an idle chat", () => {
  it("sends a prompt and never steers", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("sent");

    expect(await submitPrompt("c1", "hello")).toBe("sent");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    expect(mockSteer).not.toHaveBeenCalled();
  });

  it("restores attachments to the input when the send hard-fails", async () => {
    resetStore("c1");
    mockTakeAttachments.mockReturnValue([{ path: "a.ts" }, { path: "b.ts" }]);
    mockSendPromptTo.mockResolvedValue("failed");

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    expect(mockAddAttachment).toHaveBeenCalledWith("a.ts");
    expect(mockAddAttachment).toHaveBeenCalledWith("b.ts");
  });

  it("lets a typed command through without minting a send or a steer", async () => {
    resetStore("c1");
    mockTypedCommand.mockReturnValue(true);

    expect(await submitPrompt("c1", "/compact")).toBe("sent");
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(mockSteer).not.toHaveBeenCalled();
    // A command is not a prompt, so it must not consume the attachment row.
    expect(mockTakeAttachments).not.toHaveBeenCalled();
  });
});

describe("submitPrompt during a turn", () => {
  // The whole reason this module changed: the message has to reach the turn
  // that is running, not the one after it.
  it("steers instead of sending a prompt", async () => {
    resetStore("c1");
    setThinking("c1", true);

    expect(await submitPrompt("c1", "actually use tabs")).toBe("steered");
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(mockSteer).toHaveBeenCalledTimes(1);
    expect(steeredText()).toBe("actually use tabs");
  });

  // A turn can start between reading `thinking` and the POST landing; the server
  // answers that with 409. The old code buffered it until the turn ended, which
  // is precisely the case a steer exists for.
  it("steers when the server reports 409 busy", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("queued");

    expect(await submitPrompt("c1", "wait, stop")).toBe("steered");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    expect(steeredText()).toBe("wait, stop");
  });

  // No optimism: the chip is written by the server's own steer_queued frame, so
  // a refused steer leaves nothing on screen to explain away.
  it("records nothing locally", async () => {
    resetStore("c1");
    setThinking("c1", true);

    await submitPrompt("c1", "hello");
    expect(steerCount("c1")).toBe(0);
  });

  it("reports failure and restores attachments when the steer is refused", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockTakeAttachments.mockReturnValue([{ path: "a.ts" }]);
    mockSteer.mockResolvedValue(undefined);

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    expect(mockAddAttachment).toHaveBeenCalledWith("a.ts");
  });
});

describe("steer attachments", () => {
  // `_session/steer` takes a plain string, so a file cannot ride along as a
  // content block. Degrading to the path reference the server already uses for
  // an unsupported document keeps ONE convention for "here is a file".
  it("folds attachment paths into the text using the server's own wording", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockTakeAttachments.mockReturnValue([{ path: "src/a.ts" }, { path: "src/b.ts" }]);

    await submitPrompt("c1", "look at these");
    expect(steeredText()).toBe("look at these\n\nAttached file: src/a.ts\nAttached file: src/b.ts");
  });

  it("leaves the text alone when there are no attachments", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockTakeAttachments.mockReturnValue([]);

    await submitPrompt("c1", "plain");
    expect(steeredText()).toBe("plain");
  });

  it("skips an attachment with no usable path rather than emitting a blank line", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockTakeAttachments.mockReturnValue([{ path: "" }, { path: "ok.ts" }, {}]);

    await submitPrompt("c1", "one good file");
    expect(steeredText()).toBe("one good file\n\nAttached file: ok.ts");
  });
});

describe("submitPrompt guards", () => {
  it("refuses an empty chat id or empty text without touching the wire", async () => {
    resetStore("c1");
    expect(await submitPrompt("", "hello")).toBe("failed");
    expect(await submitPrompt("c1", "")).toBe("failed");
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(mockSteer).not.toHaveBeenCalled();
  });
});
