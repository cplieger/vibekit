// @vitest-environment happy-dom
// Tests for actions/editor.ts: saveFile, resolvePendingPartial, fetchAgentLines, suggestResolution.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

vi.mock("../transport.js", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../transport.js")>();
  return { ...orig, send: vi.fn() };
});

vi.mock("../editor-types.js", () => ({
  routeForPath: (path: string) => ({ writeURL: `/api/file?path=${encodeURIComponent(path)}` }),
}));

import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { send as transportSend } from "../transport.js";

const mockFetch = vi.fn();
const mockSend = vi.mocked(transportSend);

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("editor.save_file", () => {
  it("PUTs file content to the write URL", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { saveFile } = await import("./editor.js");
    const r = await saveFile.dispatch({ path: "src/main.ts", content: "console.log('hi')" });
    expect(r).toEqual({ ok: true });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/file?path=src%2Fmain.ts");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body as string)).toEqual({ content: "console.log('hi')" });
  });

  it("suppresses error toast (error: false)", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "disk full" }), { status: 500 }));
    const { saveFile } = await import("./editor.js");
    const { error: toastError } = await import("../toast.js");
    await saveFile.dispatch({ path: "x.ts", content: "" });
    expect(toastError).not.toHaveBeenCalled();
  });
});

describe("editor.resolve_partial", () => {
  it("sends resolve_pending_change_partial via transport", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { resolvePendingPartial } = await import("./editor.js");
    await resolvePendingPartial.dispatch({ chatID: "c1", toolCallID: "tc1", mergedText: "merged" });
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "resolve_pending_change_partial",
        chat_id: "c1",
        payload: expect.objectContaining({ tool_call_id: "tc1", merged_text: "merged" }),
      }),
      expect.anything(),
    );
  });

  it("records error on transport failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "internal" });
    const { resolvePendingPartial } = await import("./editor.js");
    const r = await resolvePendingPartial.dispatch({ chatID: "c1", toolCallID: "tc1", mergedText: "" });
    expect(r).toBeNull();
    expect(recentLog()[0]?.status).toBe("error");
  });
});

describe("editor.fetch_agent_lines", () => {
  it("GETs file changes with chat_id and path params", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ changes: [{ start_line: 1, end_line: 5 }] }), { status: 200 }));
    const { fetchAgentLines } = await import("./editor.js");
    const r = await fetchAgentLines.dispatch({ chatID: "c1", path: "src/a.ts" });
    expect(r).toEqual({ changes: [{ start_line: 1, end_line: 5 }] });
    const [url] = mockFetch.mock.calls[0]!;
    expect(url).toContain("/api/file-changes");
    expect(url).toContain("chat_id=c1");
    expect(url).toContain("path=src%2Fa.ts");
  });
});

describe("editor.suggest_resolution", () => {
  it("POSTs to /api/utility/resolve-conflict", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ output: "resolved" }), { status: 200 }));
    const { suggestResolution } = await import("./editor.js");
    const r = await suggestResolution.dispatch({ ours: "a", theirs: "b", context: "merge" });
    expect(r).toEqual({ output: "resolved" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/utility/resolve-conflict");
    expect(opts.method).toBe("POST");
  });
});
