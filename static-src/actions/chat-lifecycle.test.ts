// Tests for chat.ts actions: setSupervised, switchModel, resolvePendingChange,
// respondPermission, restoreCheckpoint.
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
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import type { Session } from "../types.js";

const mockSend = vi.mocked(transportSend);
const mockFetch = vi.fn();

function makeSession(id: string, extra?: Partial<Session>): Session {
  return {
    id,
    name: "test",
    model: "claude-4",
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
    ...extra,
  };
}

beforeEach(() => {
  resetActionFramework();
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

describe("chat.load_sessions", () => {
  it("GETs /api/sessions and dedupes concurrent calls", async () => {
    vi.useFakeTimers();
    mockFetch.mockImplementation(
      () =>
        new Promise((r) =>
          setTimeout(() => {
            r(new Response(JSON.stringify({ sessions: [], runs: [] }), { status: 200 }));
          }, 50),
        ),
    );
    const { loadSessions } = await import("./chat.js");
    const p1 = loadSessions.dispatch(undefined);
    const p2 = loadSessions.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(50);
    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1).toEqual({ sessions: [], runs: [] });
    expect(r1).toEqual(r2);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});

describe("chat.resume_session", () => {
  it("sends resume_session with the session id and title", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { resumeSession } = await import("./chat.js");
    await resumeSession.dispatch({
      chatID: "c-new",
      sessionID: "sess_abc-123",
      name: "Earlier work",
    });
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "resume_session",
        chat_id: "c-new",
        payload: { session_id: "sess_abc-123", name: "Earlier work" },
      }),
      expect.anything(),
    );
  });
});

// There is no chat.resolve_pending_change test because there is no such
// action: a turn's writes are approved through chat.respond_permission below,
// which is the same reply KAS uses for every other permission.
describe("chat.exports", () => {
  it("exposes no pending-change resolver", async () => {
    const mod = await import("./chat.js");
    for (const name of [
      "resolvePendingChange",
      "resolveAllPending",
      "trustPending",
      "clearPendingTrust",
    ]) {
      expect(mod).not.toHaveProperty(name);
    }
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
