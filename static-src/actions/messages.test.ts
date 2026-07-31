// @vitest-environment happy-dom
// Tests for actions/messages.ts: copyClipboard, explainError, undoEdit, runPlan.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
}));
vi.mock("../transport.js", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const orig = await importOriginal<typeof import("../transport.js")>();
  return { ...orig, send: vi.fn() };
});

vi.mock("../prompt-queue.js", () => ({
  submitPrompt: vi.fn(),
}));

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { getActionLog as recentLog } from "./index.js";
import * as toast from "../toast.js";
import { send as transportSend } from "../transport.js";
import { submitPrompt } from "../prompt-queue.js";
import { join } from "@cplieger/keyenc";

const mockFetch = vi.fn();
const mockSend = vi.mocked(transportSend);
const mockSubmitPrompt = vi.mocked(submitPrompt);

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("ui.copy_clipboard", () => {
  it("copies text and toasts success", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    const { copyClipboard } = await import("./messages.js");
    await copyClipboard.dispatch("hello world");
    expect(writeText).toHaveBeenCalledWith("hello world");
    expect(toast.success).toHaveBeenCalledWith("Copied");
  });

  it("toasts error when clipboard unavailable", async () => {
    vi.stubGlobal("navigator", {
      clipboard: { writeText: vi.fn().mockRejectedValue(new Error("denied")) },
    });
    const { copyClipboard } = await import("./messages.js");
    const r = await copyClipboard.dispatch("x");
    expect(r).toBeNull();
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Couldn't copy"), undefined);
    expect(recentLog()[0]?.error?.code).toBe("clipboard");
  });
});

describe("messages.explain_error", () => {
  it("POSTs to /api/utility/explain-error with truncated error text", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ output: "explanation" }), { status: 200 }),
    );
    const { explainError } = await import("./messages.js");
    const r = await explainError.dispatch({
      errorText: "TypeError: x is not a function",
      context: "tool call",
    });
    expect(r).toEqual({ output: "explanation" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/utility/explain-error");
    expect(opts.method).toBe("POST");
    const body = JSON.parse(opts.body as string);
    expect(body.error).toBe("TypeError: x is not a function");
    expect(body.context).toBe("tool call");
  });
});

describe("messages.undo_edit", () => {
  it("sends undo_edit via transport and toasts success with filename", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { undoEdit } = await import("./messages.js");
    await undoEdit.dispatch({ chatID: "c1", tag: "t1", filePath: "src/utils/helper.ts" });
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "undo_edit",
        chat_id: "c1",
        payload: expect.objectContaining({ tag: "t1", file_path: "src/utils/helper.ts" }),
      }),
      expect.anything(),
    );
    expect(toast.success).toHaveBeenCalledWith(expect.stringContaining("helper.ts"));
  });
});

describe("plan.run", () => {
  it("sends plan content via submitPrompt (queue-aware)", async () => {
    mockSubmitPrompt.mockResolvedValue("sent");
    const { runPlan } = await import("./messages.js");
    await runPlan.dispatch({ chatID: "c1", content: "Step 1\nStep 2" });
    expect(mockSubmitPrompt).toHaveBeenCalledWith("c1", expect.stringContaining("Step 1"));
  });

  it("treats a queued plan handoff as success (drains later)", async () => {
    mockSubmitPrompt.mockResolvedValue("queued");
    const { runPlan } = await import("./messages.js");
    const r = await runPlan.dispatch({ chatID: "c1", content: "plan" });
    expect(r).not.toBeNull();
  });

  it("records error when submitPrompt returns 'failed'", async () => {
    mockSubmitPrompt.mockResolvedValue("failed");
    const { runPlan } = await import("./messages.js");
    const r = await runPlan.dispatch({ chatID: "c1", content: "plan" });
    expect(r).toBeNull();
    expect(recentLog()[0]?.error?.code).toBe("send_failed");
  });
});

// ---------------------------------------------------------------------------
// Composite idempotency keys (keyenc `join`).
//
// Consumption, verified against the server rather than assumed:
//   - undo_edit is a transportAction, so its key IS forwarded, as the
//     `idempotency_key` field of the /api/command envelope. Go's
//     api.ClientCommand declares no such field and command dedup keys on
//     `request_id`, so nothing reads it today.
//   - plan.run is a plain defineAction whose run() ignores the third `ctx`
//     argument, so its key is computed by the framework and discarded.
// Neither key can therefore cause a live collision; both are joined so they
// are correct if either path ever starts honouring them.
// ---------------------------------------------------------------------------

describe("messages.undo_edit idempotency key", () => {
  async function keyFor(tag: string, filePath: string): Promise<string> {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { undoEdit } = await import("./messages.js");
    await undoEdit.dispatch({ chatID: "c1", tag, filePath });
    const cmd = mockSend.mock.calls[0]![0] as Record<string, unknown>;
    mockSend.mockReset();
    resetActionFramework();
    return String(cmd["idempotency_key"] ?? "");
  }

  it("forwards the key on the command envelope", async () => {
    // Pins the field name the framework uses, so the claim above stays checkable
    // against the Go struct.
    expect(await keyFor("t1", "src/utils/helper.ts")).toBe(
      "messages.undo_edit:t1:src/utils/helper.ts",
    );
  });

  it("distinguishes a tag/path pair the old template collapsed", async () => {
    // ":" is legal in a filesystem path, so `${tag}:${filePath}` let the path
    // absorb the tag boundary.
    const oldKey = (tag: string, p: string): string => `messages.undo_edit:${tag}:${p}`;
    expect(oldKey("t1", "a:b.ts")).toBe(oldKey("t1:a", "b.ts"));

    expect(await keyFor("t1", "a:b.ts")).not.toBe(await keyFor("t1:a", "b.ts"));
  });
});

describe("plan.run idempotency key", () => {
  it("is computed but never reaches the wire", async () => {
    // The send goes through submitPrompt, which carries no idempotency key —
    // documenting that this key has no consumer today.
    mockSubmitPrompt.mockResolvedValue("sent");
    const { runPlan } = await import("./messages.js");
    await runPlan.dispatch({ chatID: "c1", content: "Step 1" });
    expect(mockSubmitPrompt).toHaveBeenCalledTimes(1);
    expect(mockSubmitPrompt.mock.calls[0]).toHaveLength(2);
    expect(mockSend).not.toHaveBeenCalled();
  });

  it("joins chat id and content so the content cannot forge the boundary", async () => {
    // The key is unreachable from the wire, so assert the encoding directly
    // against the same library call the action makes.
    const chatID = "c1";
    const content = ":forged\nplan";
    expect(join("plan.run", chatID, content.slice(0, 40))).not.toBe(
      `plan.run:${chatID}:${content.slice(0, 40)}`,
    );
    expect(join("plan.run", chatID, content.slice(0, 40))).toBe("plan.run:c1:\\:forged\nplan");
  });
});
