// ---------------------------------------------------------------------------
// Tests for submit.ts — what Send MEANS, which depends on whether a turn is
// already running. Drives the REAL store (so the steer projection is
// observable) with the send primitive, the steer action and the attachment row
// mocked at the module boundary.
//
// The cases that matter here are the ones where the old queue was wrong:
//   - a message typed mid-turn reaches the RUNNING turn, not the next one
//   - a plain 409 (a turn starting underneath the send) steers rather than
//     buffering — and a 409-starting does NOT steer, because that holder
//     cannot receive one (it renders the busy face instead)
//   - nothing appears on screen until the server's own frame says so
//   - attachments degrade to path references, because a steer is a plain string
//   - a failed send is RECOVERABLE in place: the stale error clears on the next
//     attempt and the text goes back in the box
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

// Declared via vi.hoisted so they exist when the hoisted vi.mock factories run.
const {
  mockSendPromptTo,
  mockSteer,
  mockTakeAttachments,
  mockAddAttachmentTo,
  mockAttachmentGeneration,
  mockTypedCommand,
  mockClearAgentDown,
  mockReportSendRefused,
  mockRestoreFailedSend,
} = vi.hoisted(() => ({
  mockSendPromptTo: vi.fn(),
  mockSteer: vi.fn(),
  mockTakeAttachments: vi.fn(() => [] as unknown[]),
  mockAddAttachmentTo: vi.fn(),
  mockAttachmentGeneration: vi.fn(() => 0),
  mockTypedCommand: vi.fn(() => false),
  mockClearAgentDown: vi.fn(),
  mockReportSendRefused: vi.fn(),
  mockRestoreFailedSend: vi.fn(),
}));

vi.mock("./chat-commands.js", () => ({ sendPromptTo: mockSendPromptTo }));
vi.mock("./actions/chat.js", () => ({ steerChat: { dispatch: mockSteer } }));
vi.mock("./typed-commands.js", () => ({ handleTypedCommand: mockTypedCommand }));
vi.mock("./attachments.js", () => ({
  takeAttachments: mockTakeAttachments,
  addAttachmentTo: mockAddAttachmentTo,
  attachmentGeneration: mockAttachmentGeneration,
}));
// Both are mocked for the same reason the others are: they own real DOM, and
// send-state's module-level effect paints the send button on import.
vi.mock("./send-state.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  setAgentDown: undefined,
  setSSEStatus: undefined,
  clearAgentDown: mockClearAgentDown,
  reportSendRefused: mockReportSendRefused,
}));
vi.mock("./composer-state.js", () => ({
  restoreFailedSend: mockRestoreFailedSend,
  // Present-but-inert so real-ESM linking succeeds: the tab projection reaches
  // this module now (the close rollback), so every export has to resolve.
  retargetComposer: vi.fn(),
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  flushComposerDraft: vi.fn(),
  dropComposerState: vi.fn(),
  seedComposerState: vi.fn(),
  adoptRemoteComposerState: vi.fn(),
  noteComposerText: vi.fn(),
  initComposerState: vi.fn(),
  _resetComposerStateForTest: vi.fn(),
}));

import { submitPrompt } from "./submit.js";
import { setSessions, setActive, setThinking, steerCount, appendMessage } from "./store.js";
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

/** The message id the send primitive was called with on the Nth dispatch. */
function sentMessageID(call = 0): string {
  return (
    (mockSendPromptTo.mock.calls[call]?.[2] as { messageID: string } | undefined)?.messageID ?? ""
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockTakeAttachments.mockReturnValue([]);
  mockAttachmentGeneration.mockReturnValue(0);
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

  it("restores the text and the attachments to the input when the send hard-fails", async () => {
    resetStore("c1");
    mockTakeAttachments.mockReturnValue([{ path: "a.ts" }, { path: "b.ts" }]);
    mockAttachmentGeneration.mockReturnValue(7);
    mockSendPromptTo.mockResolvedValue("failed");

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    // Chat-scoped, not "the composer on screen": the send is asynchronous, so by
    // the time a failure lands the reader may be in another conversation.
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "hello");
    // The generation the send took with it rides the restore, so a chat closed
    // while the request was in flight can refuse it.
    expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "a.ts", 7);
    expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "b.ts", 7);
  });

  // The user-visible bug: a prompt that was ACCEPTED and then lost its turn came
  // back into the composer, which is indistinguishable from an Enter that never
  // cleared the box. `CmdPrompt` persists and broadcasts the user row before the
  // ACP call and nothing rolls it back, so the echo is already in the store by the
  // time the turn dies and the POST answers 500.
  describe("a prompt the server already accepted", () => {
    /** Fail the send, but echo the user row first, the way the server does. */
    function failAfterEcho(): void {
      mockSendPromptTo.mockImplementation(
        async (chatID: string, text: string, opts: { messageID: string }) => {
          appendMessage(chatID, { id: opts.messageID, role: "user", content: text, ts: 1 });
          return "failed";
        },
      );
    }

    it("is not handed back to the composer", async () => {
      resetStore("c1");
      failAfterEcho();

      expect(await submitPrompt("c1", "hello")).toBe("failed");
      expect(mockRestoreFailedSend).not.toHaveBeenCalled();
    });

    it("does not get its attachments back either", async () => {
      // They rode the accepted prompt, and the server records them on the message.
      resetStore("c1");
      mockTakeAttachments.mockReturnValue([{ path: "a.ts" }]);
      failAfterEcho();

      await submitPrompt("c1", "hello");
      expect(mockAddAttachmentTo).not.toHaveBeenCalled();
    });

    it("still reuses its message id, so a retype lands on the row it persisted", async () => {
      resetStore("c1");
      failAfterEcho();
      await submitPrompt("c1", "hello");
      const first = sentMessageID(0);

      await submitPrompt("c1", "hello");
      expect(sentMessageID(1)).toBe(first);
    });
  });

  it("hands back the generation read BEFORE the send, not the one after it", async () => {
    // A close during the request is exactly what bumps it, so reading the token
    // again on the failure path would compare the new state against itself and
    // restore into a chat that had just forgotten its files.
    resetStore("c1");
    mockTakeAttachments.mockReturnValue([{ path: "a.ts" }]);
    mockAttachmentGeneration.mockReturnValue(3);
    mockSendPromptTo.mockImplementation(async () => {
      mockAttachmentGeneration.mockReturnValue(4); // the chat was closed meanwhile
      return "failed";
    });

    await submitPrompt("c1", "hello");
    expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "a.ts", 3);
  });

  it("keeps the input untouched when the send succeeds", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("sent");

    await submitPrompt("c1", "hello");
    expect(mockRestoreFailedSend).not.toHaveBeenCalled();
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

  it("reports failure and restores the text and attachments when the steer is refused", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockTakeAttachments.mockReturnValue([{ path: "a.ts" }]);
    mockSteer.mockResolvedValue(undefined);

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "hello");
    expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "a.ts", 0);
  });
});

// The third 409 class: reason "starting". The admission slot is held by a cold
// spawn, a shell or a prime — none of which can receive a steer — so this is
// neither the steer conversion nor a pre-persist failure. The user row is
// already persisted and rendered (persist precedes reservation server-side),
// which is what the echo-first fake below reproduces.
describe("submitPrompt on a 409-starting refusal", () => {
  /** Refuse with "starting", but echo the user row first, the way the server
   *  does: CmdPrompt persists and broadcasts the row BEFORE the admission
   *  check, so by the time the refusal lands the echo is in the store. */
  function startingAfterEcho(): void {
    mockSendPromptTo.mockImplementation(
      async (chatID: string, text: string, opts: { messageID: string }) => {
        appendMessage(chatID, { id: opts.messageID, role: "user", content: text, ts: 1 });
        return "starting";
      },
    );
  }

  it("reports failure and never attempts a steer", async () => {
    resetStore("c1");
    startingAfterEcho();

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    expect(mockSteer).not.toHaveBeenCalled();
  });

  it("renders the holder-neutral busy face through send-state's error surface", async () => {
    resetStore("c1");
    startingAfterEcho();

    await submitPrompt("c1", "hello");
    expect(mockReportSendRefused).toHaveBeenCalledWith(
      "The chat is busy right now — send again to retry",
    );
  });

  it("does NOT restore the text — the row is persisted and on screen", async () => {
    resetStore("c1");
    mockTakeAttachments.mockReturnValue([{ path: "a.ts" }]);
    startingAfterEcho();

    await submitPrompt("c1", "hello");
    expect(mockRestoreFailedSend).not.toHaveBeenCalled();
    // The attachments rode the persisted row for the same reason.
    expect(mockAddAttachmentTo).not.toHaveBeenCalled();
  });

  it("re-sends the same text under the same id, so the server dedupes the append", async () => {
    resetStore("c1");
    startingAfterEcho();
    await submitPrompt("c1", "hello");
    const first = sentMessageID(0);
    expect(first).not.toBe("");

    // The retry is an ordinary send (the face said "send again to retry").
    mockSendPromptTo.mockResolvedValue("sent");
    await submitPrompt("c1", "hello");
    expect(sentMessageID(1)).toBe(first);
  });

  it("retries as a PROMPT, not a steer", async () => {
    // The action's starting arm retracts the optimistic thinking (pinned in
    // actions/chat-prompt.test.ts), so the chat reads idle here and the retry
    // goes down the send path — the busy face said "send again", and a steer
    // would be undeliverable against the same holder.
    resetStore("c1");
    startingAfterEcho();
    await submitPrompt("c1", "hello");

    mockSendPromptTo.mockResolvedValue("sent");
    expect(await submitPrompt("c1", "hello")).toBe("sent");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(2);
    expect(mockSteer).not.toHaveBeenCalled();
  });
});

// A failure sets a signal nothing on the failure path ever clears (no
// turn_ended is emitted for a prompt that died at session/prompt), so without
// this the stale reason decorates every later send in the thread.
describe("retrying in the same thread", () => {
  it("clears the previous failure before each new attempt", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "first try");
    expect(mockClearAgentDown).toHaveBeenCalledTimes(1);

    mockSendPromptTo.mockResolvedValue("sent");
    expect(await submitPrompt("c1", "second try")).toBe("sent");
    expect(mockClearAgentDown).toHaveBeenCalledTimes(2);
    // The retry is an ordinary send: no special path, no cancel, no new chat.
    expect(mockSendPromptTo).toHaveBeenCalledTimes(2);
  });

  it("clears it for a typed command too, which is also an attempt", async () => {
    resetStore("c1");
    mockTypedCommand.mockReturnValue(true);

    await submitPrompt("c1", "/compact");
    expect(mockClearAgentDown).toHaveBeenCalledTimes(1);
  });

  it("does not clear it when the guards refuse the submit outright", async () => {
    resetStore("c1");
    await submitPrompt("", "hello");
    await submitPrompt("c1", "");
    expect(mockClearAgentDown).not.toHaveBeenCalled();
  });

  // The server persists the user row BEFORE the ACP call and never rolls it
  // back, so a retry under a fresh id appends a second identical row and hands
  // KAS a second messageId. This is what keeps one failed prompt to one row.
  it("retries a failed send under the id that attempt already used", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "same text");
    const first = sentMessageID(0);
    expect(first).not.toBe("");

    mockSendPromptTo.mockResolvedValue("sent");
    await submitPrompt("c1", "same text");
    expect(sentMessageID(1)).toBe(first);
  });

  it("keeps the id stable across repeated retries of the same text", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "same text");
    await submitPrompt("c1", "same text");
    await submitPrompt("c1", "same text");
    expect(sentMessageID(1)).toBe(sentMessageID(0));
    expect(sentMessageID(2)).toBe(sentMessageID(0));
  });

  it("mints a fresh id once the text is edited — a different message earns a different row", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "first wording");
    await submitPrompt("c1", "second wording");
    expect(sentMessageID(1)).not.toBe(sentMessageID(0));
  });

  it("mints a fresh id for the same text on a different chat", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "same text");
    await submitPrompt("c2", "same text");
    expect(sentMessageID(1)).not.toBe(sentMessageID(0));
  });

  it("forgets the failed id after a send succeeds", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "same text");
    mockSendPromptTo.mockResolvedValue("sent");
    await submitPrompt("c1", "same text");
    // A third send of identical text is a NEW message, not a retry of the one
    // that already landed.
    await submitPrompt("c1", "same text");
    expect(sentMessageID(2)).not.toBe(sentMessageID(1));
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
