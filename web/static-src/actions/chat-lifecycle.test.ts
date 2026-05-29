// @vitest-environment happy-dom
// Tests for chat.ts actions: setSupervised, setAutoApproveCrew, switchModel,
// resolvePendingChange, respondPermission, restoreCheckpoint.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../transport.js", () => ({ send: vi.fn() }));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
}));
import { send as transportSend } from "../transport.js";
import { setSessions, get, setActive } from "../store.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
// eslint-disable-next-line @typescript-eslint/no-unused-vars
import * as toast from "../toast.js";
import type { Session } from "../types.js";

const mockSend = vi.mocked(transportSend);
const mockFetch = vi.fn();

function makeSession(id: string, extra?: Partial<Session>): Session {
  return {
    id,
    name: "test",
    agent: "",
    model: "claude-4",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    auto_approve_crew: false,
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
    ...extra,
  };
}

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
  setSessions([makeSession("c1"), makeSession("c2")]);
  setActive("c1");
});

describe("chat.set_supervised", () => {
  it("sends set_supervised_mode command and applies optimistic update", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { setSupervised } = await import("./chat.js");
    await setSupervised.dispatch({ chatID: "c1", enabled: true });
    expect(get("c1")!.supervised_mode).toBe(true);
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({ type: "set_supervised_mode", chat_id: "c1" }),
      expect.anything(),
    );
  });

  it("rolls back on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    const { setSupervised } = await import("./chat.js");
    await setSupervised.dispatch({ chatID: "c1", enabled: true });
    expect(get("c1")!.supervised_mode).toBe(false);
  });
});

describe("chat.set_auto_approve_crew", () => {
  it("applies optimistic update and rolls back on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    const { setAutoApproveCrew } = await import("./chat.js");
    await setAutoApproveCrew.dispatch({ chatID: "c1", enabled: true });
    // Rolled back to original value
    expect(get("c1")!.auto_approve_crew).toBe(false);
  });
});

describe("chat.switch_model", () => {
  it("applies optimistic model change and sends via transport", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { switchModel } = await import("./chat.js");
    const r = await switchModel.dispatch({ chatID: "c1", model: "opus" });
    expect(r).toBe(true);
    expect(get("c1")!.model).toBe("opus");
  });

  it("rolls back model on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    const { switchModel } = await import("./chat.js");
    await switchModel.dispatch({ chatID: "c1", model: "opus" });
    expect(get("c1")!.model).toBe("claude-4");
  });
});

describe("chat.cancel_turn", () => {
  it("sends cancel command via transport", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { cancelTurn } = await import("./chat.js");
    await cancelTurn.dispatch("c1");
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({ type: "cancel", chat_id: "c1" }),
      expect.anything(),
    );
  });
});

describe("chat.restore", () => {
  it("POSTs to /api/chats/archived with id", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { restoreChat } = await import("./chat.js");
    const r = await restoreChat.dispatch("archived-1");
    expect(r).toEqual({ ok: true });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/chats/archived");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ id: "archived-1" });
  });
});

describe("chat.delete_archived", () => {
  it("DELETEs /api/chats/archived/:id", async () => {
    mockFetch.mockResolvedValue(new Response("", { status: 204 }));
    const { deleteArchivedChat } = await import("./chat.js");
    await deleteArchivedChat.dispatch("old-chat");
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/chats/archived/old-chat");
    expect(opts.method).toBe("DELETE");
  });

  it("is not retryable", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "gone" }), { status: 404 }));
    const { deleteArchivedChat } = await import("./chat.js");
    await deleteArchivedChat.dispatch("x");
    expect(recentLog()[0]?.status).toBe("error");
  });
});

describe("chat.load_history", () => {
  it("GETs /api/chats/archived and dedupes", async () => {
    vi.useFakeTimers();
    mockFetch.mockImplementation(
      () =>
        new Promise((r) =>
          setTimeout(() => {
            r(new Response(JSON.stringify({ chats: [] }), { status: 200 }));
          }, 50),
        ),
    );
    const { loadHistory } = await import("./chat.js");
    const p1 = loadHistory.dispatch(undefined);
    const p2 = loadHistory.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(50);
    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1).toEqual({ chats: [] });
    expect(r1).toEqual(r2);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});

describe("chat.resolve_pending_change", () => {
  it("sends resolve_pending_change with tool_call_id and action", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { resolvePendingChange } = await import("./chat.js");
    await resolvePendingChange.dispatch({ chatID: "c1", toolCallID: "tc-1", action: "accept" });
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "resolve_pending_change",
        chat_id: "c1",
        payload: expect.objectContaining({ tool_call_id: "tc-1", action: "accept" }),
      }),
      expect.anything(),
    );
  });
});

describe("chat.respond_permission", () => {
  it("sends permission_response with request_id and option_id", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { respondPermission } = await import("./chat.js");
    await respondPermission.dispatch({ chatID: "c1", requestID: 42, optionID: "allow_once" });
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "permission_response",
        chat_id: "c1",
        payload: expect.objectContaining({ request_id: 42, option_id: "allow_once" }),
      }),
      expect.anything(),
    );
  });
});

describe("chat.restore_checkpoint", () => {
  it("sends restore_checkpoint with tag", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { restoreCheckpoint } = await import("./chat.js");
    await restoreCheckpoint.dispatch({ chatID: "c1", tag: "cp-abc" });
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "restore_checkpoint",
        chat_id: "c1",
        payload: expect.objectContaining({ tag: "cp-abc" }),
      }),
      expect.anything(),
    );
  });
});
