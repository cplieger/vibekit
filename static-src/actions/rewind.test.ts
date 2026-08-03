// @vitest-environment happy-dom
// Tests for actions/rewind.ts. ONE action: rewind reverts the chat it is in.
// The create/promote/discard trio went with the branch it existed to resolve.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../transport.js", () => ({ send: vi.fn() }));

import { send as transportSend } from "../transport.js";
import * as toast from "../toast.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

const mockSend = vi.mocked(transportSend);

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
});

describe("rewind.revert", () => {
  it("sends a rewind_chat command addressing a MESSAGE, not a turn index", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { rewindChat } = await import("./rewind.js");

    await rewindChat.dispatch({ chatID: "c-1", messageID: "m-abc" });

    expect(mockSend).toHaveBeenCalledTimes(1);
    const cmd = mockSend.mock.calls[0]?.[0] as {
      type: string;
      chat_id: string;
      payload: { message_id: string };
    };
    expect(cmd.type).toBe("rewind_chat");
    expect(cmd.chat_id).toBe("c-1");
    // A message id, because that is what KAS's revert verb addresses — and it
    // must name a user message, whose id space is shared only because vibekit
    // sends it on session/prompt.
    expect(cmd.payload.message_id).toBe("m-abc");
    expect(cmd.payload).not.toHaveProperty("turn_index");
  });

  it("toasts an error when the server rejects", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" });
    const { rewindChat } = await import("./rewind.js");

    await rewindChat.dispatch({ chatID: "c-1", messageID: "m-abc" });

    // The server's reason is appended, so a refusal KAS explained in-band ("not
    // a user message", "Cannot revert while the agent is still running") reaches
    // the user instead of a generic failure.
    expect(toast.error).toHaveBeenCalledWith("Couldn't rewind chat: boom", undefined);
  });

  // A retry that reverts twice would cut a SECOND time from an
  // already-truncated transcript and take real turns with it, which is why the
  // idempotency key matters more here than it did for the old create action
  // (where a duplicate merely made a spare branch).
  it("carries an idempotency key", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { rewindChat } = await import("./rewind.js");

    await rewindChat.dispatch({ chatID: "c-1", messageID: "m-abc" });

    const cmd = mockSend.mock.calls[0]?.[0] as Record<string, unknown>;
    expect(cmd["request_id"]).toBeTypeOf("string");
  });

  it("exports no promote or discard action", async () => {
    const mod = await import("./rewind.js");
    expect(Object.keys(mod)).toEqual(["rewindChat"]);
  });
});
