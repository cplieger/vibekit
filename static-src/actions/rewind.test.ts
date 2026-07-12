// @vitest-environment happy-dom
// Tests for actions/rewind.ts: rewindChat (create branch, return branch id).

import { describe, it, expect, vi, beforeEach } from "vitest";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import * as toast from "../toast.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("rewind.create", () => {
  it("POSTs a rewind_chat command envelope and returns the branch id", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ ok: true, rewind_id: "c-branch" }), { status: 200 }),
    );
    const { rewindChat } = await import("./rewind.js");

    const r = await rewindChat.dispatch({ chatID: "c-parent", turnIndex: 3 });

    expect(r?.rewind_id).toBe("c-branch");
    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url, init] = mockFetch.mock.calls[0]!;
    expect(url).toContain("/api/command");
    const body = JSON.parse((init as RequestInit).body as string) as {
      type: string;
      chat_id: string;
      payload: { turn_index: number };
    };
    expect(body.type).toBe("rewind_chat");
    expect(body.chat_id).toBe("c-parent");
    expect(body.payload.turn_index).toBe(3);
  });

  it("toasts an error when the server rejects", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "boom" }), { status: 500 }));
    const { rewindChat } = await import("./rewind.js");

    const r = await rewindChat.dispatch({ chatID: "c-parent", turnIndex: 0 });

    expect(r).toBeNull();
    expect(toast.error).toHaveBeenCalledWith(
      expect.stringContaining("Couldn't rewind chat"),
      undefined,
    );
  });
});
