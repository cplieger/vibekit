// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for prompt-queue.ts — the single owner of the drain-on-idle prompt
// queue. Drives the REAL store (so queue state is observable) with the send
// primitive, banner, and attachment row mocked at the module boundary.
//
// Covers the hardening cases from the task:
//   #1 FIFO ordering + drain one-per-turn
//   #2 attachments travel with their own prompt (inlined; no positional drift)
//   #3 drop-on-error / re-409: a drained prompt is never silently lost
//   #4 409 lands after turn_ended already fired → idle re-check drains it
//   cancel affordance
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

// Declared via vi.hoisted so they exist when the hoisted vi.mock factories run.
const { mockSendPromptTo, mockShowBanner, mockTakeAttachments, mockAddAttachment } = vi.hoisted(
  () => ({
    mockSendPromptTo: vi.fn(),
    mockShowBanner: vi.fn(),
    mockTakeAttachments: vi.fn(() => [] as unknown[]),
    mockAddAttachment: vi.fn(),
  }),
);

vi.mock("./chat-commands.js", () => ({ sendPromptTo: mockSendPromptTo }));
vi.mock("./banner-stack.js", () => ({ showBanner: mockShowBanner }));
vi.mock("./attachments.js", () => ({
  takeAttachments: mockTakeAttachments,
  addAttachment: mockAddAttachment,
}));

import { submitPrompt, drainNext, maybeDrainIfIdle, cancelQueuedPrompt } from "./prompt-queue.js";
import {
  setSessions,
  setActive,
  setThinking,
  enqueuePrompt,
  get,
  queueLength,
  peekPrompt,
} from "./store.js";
import type { Session } from "./types.js";

/** Flush microtasks + timers so a fire-and-forget drain's promise chain
 *  (sendPromptTo → .then → .finally) fully settles before assertions. */
function flush(): Promise<void> {
  return new Promise((r) => setTimeout(r, 0));
}

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
    pending_changes: [],
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

beforeEach(() => {
  vi.clearAllMocks();
  mockTakeAttachments.mockReturnValue([]);
});

describe("submitPrompt", () => {
  it("sends immediately when idle with an empty queue", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValueOnce("sent");
    const r = await submitPrompt("c1", "hello");
    await flush();
    expect(r).toBe("sent");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    expect(queueLength("c1")).toBe(0);
  });

  it("enqueues on 409 (a turn is in flight)", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockSendPromptTo.mockResolvedValueOnce("queued");
    const r = await submitPrompt("c1", "hello");
    await flush();
    expect(r).toBe("queued");
    expect(queueLength("c1")).toBe(1);
    expect(peekPrompt("c1")?.text).toBe("hello");
    // Still thinking → the idle re-check must NOT drain (no second send).
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
  });

  it("#4 race: a 409 that lands after turn_ended (chat idle) drains immediately", async () => {
    resetStore("c1");
    // thinking=false models the race: turn_ended already cleared it and drained
    // an empty queue before this prompt's 409 came back.
    mockSendPromptTo.mockResolvedValueOnce("queued").mockResolvedValueOnce("sent");
    const r = await submitPrompt("c1", "stranded?");
    await flush();
    expect(r).toBe("queued");
    // The idle re-check re-sent it instead of leaving it stranded forever.
    expect(mockSendPromptTo).toHaveBeenCalledTimes(2);
    expect(queueLength("c1")).toBe(0);
  });

  it("queues behind an existing entry instead of racing ahead (FIFO)", async () => {
    resetStore("c1");
    setThinking("c1", true);
    enqueuePrompt("c1", "first", "m-test");
    const r = await submitPrompt("c1", "second");
    await flush();
    expect(r).toBe("queued");
    // Did not send directly; appended behind "first".
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(get("c1")?.prompt_queue?.map((e) => e.text)).toEqual(["first", "second"]);
  });

  it("restores attachments to the input on a hard failure", async () => {
    resetStore("c1");
    mockTakeAttachments.mockReturnValueOnce([{ path: "a.ts", name: "a.ts" }]);
    mockSendPromptTo.mockResolvedValueOnce("failed");
    const r = await submitPrompt("c1", "hello");
    await flush();
    expect(r).toBe("failed");
    expect(mockAddAttachment).toHaveBeenCalledWith("a.ts");
    expect(queueLength("c1")).toBe(0);
  });

  it("carries captured attachments into the queued entry on 409", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockTakeAttachments.mockReturnValueOnce([{ path: "img.png", name: "img.png" }]);
    mockSendPromptTo.mockResolvedValueOnce("queued");
    await submitPrompt("c1", "look at this");
    await flush();
    expect(peekPrompt("c1")).toEqual({
      text: "look at this",
      attachments: [{ path: "img.png", name: "img.png" }],
      // Minted by submitPrompt so the drain re-sends under the SAME id
      // (server-side append-by-id idempotency absorbs the retry).
      messageId: expect.any(String) as unknown as string,
    });
  });

  it("rejects empty chat id / empty text", async () => {
    resetStore("c1");
    expect(await submitPrompt("", "hi")).toBe("failed");
    expect(await submitPrompt("c1", "")).toBe("failed");
    expect(mockSendPromptTo).not.toHaveBeenCalled();
  });
});

describe("drainNext", () => {
  it("removes the entry only after the server accepts it (sent)", async () => {
    resetStore("c1");
    enqueuePrompt("c1", "queued prompt", "m-test");
    mockSendPromptTo.mockResolvedValueOnce("sent");
    drainNext("c1");
    await flush();
    expect(mockSendPromptTo).toHaveBeenCalledWith("c1", "queued prompt", expect.any(Object));
    expect(queueLength("c1")).toBe(0);
  });

  it("#3 keeps the entry + surfaces a banner when the re-send fails (never lost)", async () => {
    resetStore("c1");
    enqueuePrompt("c1", "important", "m-test");
    mockSendPromptTo.mockResolvedValueOnce("failed");
    drainNext("c1");
    await flush();
    expect(queueLength("c1")).toBe(1);
    expect(peekPrompt("c1")?.text).toBe("important");
    expect(mockShowBanner).toHaveBeenCalledWith(
      "c1",
      "prompt_queue_send_failed",
      expect.any(String),
      "warning",
      true,
    );
  });

  it("#3 keeps the entry (no duplicate) when the re-send 409s again", async () => {
    resetStore("c1");
    enqueuePrompt("c1", "retry me", "m-test");
    mockSendPromptTo.mockResolvedValueOnce("queued");
    drainNext("c1");
    await flush();
    // Still exactly one entry — no double-enqueue, no loss.
    expect(queueLength("c1")).toBe(1);
    expect(mockShowBanner).not.toHaveBeenCalled();
  });

  it("forwards the current session model + the entry's attachments", async () => {
    resetStore("c1");
    enqueuePrompt("c1", "text", "m-test", [{ path: "f.ts", name: "f.ts" }]);
    mockSendPromptTo.mockResolvedValueOnce("sent");
    drainNext("c1");
    await flush();
    expect(mockSendPromptTo).toHaveBeenCalledWith("c1", "text", {
      messageID: "m-test",
      model: "claude",
      attachments: [{ path: "f.ts", name: "f.ts" }],
    });
  });

  it("no-ops on an empty queue", async () => {
    resetStore("c1");
    drainNext("c1");
    await flush();
    expect(mockSendPromptTo).not.toHaveBeenCalled();
  });

  it("guards against a concurrent second drain before the first send resolves", async () => {
    resetStore("c1");
    enqueuePrompt("c1", "once", "m-test");
    mockSendPromptTo.mockResolvedValue("sent");
    drainNext("c1");
    drainNext("c1"); // second call while the first is in flight → guarded
    await flush();
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
  });

  it("#1 drains FIFO across successive turn ends (one prompt per turn)", async () => {
    resetStore("c1");
    enqueuePrompt("c1", "P1", "m-test");
    enqueuePrompt("c1", "P2", "m-test");
    mockSendPromptTo.mockResolvedValue("sent");

    drainNext("c1"); // first turn_ended
    await flush();
    expect(queueLength("c1")).toBe(1);

    drainNext("c1"); // next turn_ended
    await flush();
    expect(queueLength("c1")).toBe(0);

    const order = mockSendPromptTo.mock.calls.map((c) => c[1]);
    expect(order).toEqual(["P1", "P2"]);
  });
});

describe("maybeDrainIfIdle", () => {
  it("drains when the chat is idle with a pending queue", async () => {
    resetStore("c1");
    setThinking("c1", false);
    enqueuePrompt("c1", "go", "m-test");
    mockSendPromptTo.mockResolvedValueOnce("sent");
    maybeDrainIfIdle("c1");
    await flush();
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    expect(queueLength("c1")).toBe(0);
  });

  it("does nothing while a turn is in flight (thinking)", async () => {
    resetStore("c1");
    setThinking("c1", true);
    enqueuePrompt("c1", "wait", "m-test");
    maybeDrainIfIdle("c1");
    await flush();
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(queueLength("c1")).toBe(1);
  });

  it("does nothing when the queue is empty", async () => {
    resetStore("c1");
    setThinking("c1", false);
    maybeDrainIfIdle("c1");
    await flush();
    expect(mockSendPromptTo).not.toHaveBeenCalled();
  });
});

describe("cancelQueuedPrompt", () => {
  it("removes and returns the entry at the given index", () => {
    resetStore("c1");
    enqueuePrompt("c1", "a", "m-test");
    enqueuePrompt("c1", "b", "m-test", [{ path: "x.ts", name: "x.ts" }]);
    enqueuePrompt("c1", "c", "m-test");
    const removed = cancelQueuedPrompt("c1", 1);
    expect(removed).toEqual({
      text: "b",
      attachments: [{ path: "x.ts", name: "x.ts" }],
      messageId: "m-test",
    });
    expect(get("c1")?.prompt_queue?.map((e) => e.text)).toEqual(["a", "c"]);
  });

  it("returns undefined for an out-of-range index", () => {
    resetStore("c1");
    enqueuePrompt("c1", "a", "m-test");
    expect(cancelQueuedPrompt("c1", 9)).toBeUndefined();
    expect(queueLength("c1")).toBe(1);
  });
});
