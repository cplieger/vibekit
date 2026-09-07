// Tests for actions/rewind.ts. ONE action: rewind reverts the chat it is in.
// The create/promote/discard trio went with the branch it existed to resolve.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../transport.js", () => ({ send: vi.fn() }));

// The refetch is the point of the success path, so the loader is a spy rather
// than a real fetch: what matters is that the action asks for the page again.
vi.mock("../store-load.js", () => ({ loadMessages: vi.fn(() => Promise.resolve(true)) }));

import { send as transportSend } from "../transport.js";
import { loadMessages } from "../store-load.js";
import * as toast from "../toast.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

const mockSend = vi.mocked(transportSend);
const mockLoadMessages = vi.mocked(loadMessages);

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  mockLoadMessages.mockClear();
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
  //
  // The key must be at the command's TOP level under the framework's field
  // name, because that is the one place transport.send looks when it builds the
  // Idempotency-Key header. This assertion used to read `request_id`, a field
  // the action set by hand and transport.send discarded when it built the body
  // — so it passed while nothing reached the server.
  it("carries a framework idempotency key at the top level", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { rewindChat } = await import("./rewind.js");
    const { IDEMPOTENCY_COMMAND_FIELD } = await import("./index.js");

    await rewindChat.dispatch({ chatID: "c-1", messageID: "m-abc" });

    const cmd = mockSend.mock.calls[0]?.[0] as Record<string, unknown>;
    expect(cmd[IDEMPOTENCY_COMMAND_FIELD]).toBeTypeOf("string");
    // And NOT the hand-minted envelope field it used to carry: the server's
    // envelope has no request_id any more.
    expect(cmd["request_id"]).toBeUndefined();
  });

  it("exports no promote or discard action", async () => {
    const mod = await import("./rewind.js");
    expect(Object.keys(mod)).toEqual(["rewindChat"]);
  });

  // Nothing on the wire tells a client that messages were REMOVED: chat_updated
  // carries only the header, and upsertHeader merges the count as Math.max, so a
  // shrink is discarded. Without this refetch a successful rewind rolled the
  // files back and truncated the record while the reader kept looking at the
  // dropped turns until a reload.
  it("refetches the chat's messages after a successful rewind", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { rewindChat } = await import("./rewind.js");

    await rewindChat.dispatch({ chatID: "c-1", messageID: "m-abc" });

    expect(mockLoadMessages).toHaveBeenCalledTimes(1);
    expect(mockLoadMessages).toHaveBeenCalledWith("c-1");
  });

  // A refusal left the record untouched server-side, so refetching would replace
  // the reader's window with a byte-identical copy for nothing — and on the 409
  // the transcript they are looking at is still the truth.
  it("does not refetch when the rewind was refused", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "no bridge" });
    const { rewindChat } = await import("./rewind.js");

    await rewindChat.dispatch({ chatID: "c-1", messageID: "m-abc" });

    expect(mockLoadMessages).not.toHaveBeenCalled();
  });
});
