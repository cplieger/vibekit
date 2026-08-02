// @vitest-environment happy-dom
// Tests for actions/messages.ts: copyClipboard, explainError, undoEdit.

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

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { getActionLog as recentLog } from "./index.js";
import * as toast from "../toast.js";
import { send as transportSend } from "../transport.js";

const mockFetch = vi.fn();
const mockSend = vi.mocked(transportSend);

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
