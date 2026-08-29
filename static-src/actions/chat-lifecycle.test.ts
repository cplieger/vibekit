// Tests for chat.ts actions: setSupervised, switchModel, resolvePendingChange,
// respondPermission, restoreCheckpoint.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../transport.js", () => ({
  send: vi.fn(),
  // Reached through tabs.ts, which mints one `op_id` per tab mutation at the
  // DISPATCH site. Nothing here mutates a tab; the name has to exist for
  // real-ESM linking.
  newOpID: vi.fn(() => "op-test"),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
  // Reached through tabs.ts -> tabs-sync.ts, whose `GET /api/tabs` is the only
  // read in the projection. No case here lists tabs; the name has to exist for
  // real-ESM linking.
  apiGetTyped: vi.fn(),
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

// --- chat.create ---
// The command that mints. Three facts are the wire contract of stage 1b and each
// one is invisible from the outside if it regresses: the envelope carries no chat
// id, the op id travels in the payload, and the reply's chat is what the caller
// opens.
describe("chat.create", () => {
  const header = {
    id: "c-minted",
    name: "New conversation",
    model: "claude-opus-5",
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    created_at: 0,
    updated_at: 0,
    message_count: 0,
  };

  it("sends create_chat with the op id and NO chat id", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { createChat } = await import("./chat.js");
    await createChat.dispatch({ opID: "op-1", model: "claude-opus-5" });

    const sent = mockSend.mock.calls.at(-1)?.[0] as { chat_id?: string; payload: unknown };
    expect(sent).toMatchObject({
      type: "create_chat",
      payload: { op_id: "op-1", model: "claude-opus-5" },
    });
    expect(sent.chat_id).toBeUndefined();
  });

  // The framework generates ONE idempotency key per dispatch and threads it through
  // every retry attempt, so honouring it is what makes a retry inside the server's
  // 5-minute cache dedupe. The op id covers the fall-through past that TTL; both
  // halves have to travel or the create is idempotent over neither window.
  it("carries the framework's idempotency key alongside the op id", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { createChat } = await import("./chat.js");
    const { IDEMPOTENCY_COMMAND_FIELD } = await import("./index.js");
    await createChat.dispatch({ opID: "op-1" });

    const sent = mockSend.mock.calls.at(-1)?.[0] as Record<string, unknown>;
    expect(typeof sent[IDEMPOTENCY_COMMAND_FIELD]).toBe("string");
  });

  it("returns the chat the server minted, with the tab it opened and the version", async () => {
    mockSend.mockResolvedValue({
      ok: true,
      status: 200,
      body: {
        ok: true,
        chat: header,
        subject: {
          id: "tb_1",
          kind: "chat",
          ref: "c-minted",
          parent: "",
          pinned: false,
          owns: true,
        },
        version: 7,
      },
    });
    const { createChat } = await import("./chat.js");
    const got = await createChat.dispatch({ opID: "op-1" });

    expect(got?.chat.id).toBe("c-minted");
    expect(got?.chat.model).toBe("claude-opus-5");
    // The reply-widening contract: the subject and the committed version reach
    // the caller, which is what the adoption path paints and correlates from.
    expect(got?.subject?.id).toBe("tb_1");
    expect(got?.version).toBe(7);
  });

  it("carries no subject when the reply omits it", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { createChat } = await import("./chat.js");
    const got = await createChat.dispatch({ opID: "op-1" });
    expect(got?.chat.id).toBe("c-minted");
    expect(got?.subject).toBeUndefined();
  });

  // A 200 the client cannot read a chat out of is a FAILURE, not a null the caller
  // re-judges: there is nothing to open, and opening a tab for an id nobody has is
  // the exact window this change removes.
  it("fails when the reply names no chat", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true } });
    const { createChat } = await import("./chat.js");
    expect(await createChat.dispatch({ opID: "op-1" })).toBeNull();
  });

  it("fails when the command itself fails", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" });
    const { createChat } = await import("./chat.js");
    expect(await createChat.dispatch({ opID: "op-1" })).toBeNull();
  });

  // Omitted rather than sent empty: the server defaults the name to "New
  // conversation" and treats an empty model as unset, and sending "" for either
  // would make the client's silence look like a choice.
  it("omits an unset name and model rather than sending empty strings", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { createChat } = await import("./chat.js");
    await createChat.dispatch({ opID: "op-1", name: "", model: "" });

    const sent = mockSend.mock.calls.at(-1)?.[0] as { payload: Record<string, unknown> };
    expect(sent.payload).toEqual({ op_id: "op-1" });
  });
});

// resume_session no longer takes the new chat's id: the server mints it and
// returns it. This suite pins both halves, because the action is DORMANT (the
// history UI resolves a chat id server-side and opens it directly), so nothing
// else would notice a regression here.
describe("chat.resume_session", () => {
  const header = {
    id: "c-minted",
    name: "Earlier work",
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    created_at: 0,
    updated_at: 0,
    message_count: 0,
  };

  it("sends the session id, the title and an op id, and NO chat id", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { resumeSession } = await import("./chat.js");
    await resumeSession.dispatch({
      opID: "op-1",
      sessionID: "sess_abc-123",
      name: "Earlier work",
    });
    const sent = mockSend.mock.calls.at(-1)?.[0] as { chat_id?: string; payload: unknown };
    expect(sent).toMatchObject({
      type: "resume_session",
      payload: { session_id: "sess_abc-123", name: "Earlier work", op_id: "op-1" },
    });
    expect(sent.chat_id).toBeUndefined();
  });

  it("returns the chat the server created, so the caller can open it", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { resumeSession } = await import("./chat.js");
    const got = await resumeSession.dispatch({
      opID: "op-2",
      sessionID: "sess_abc-123",
      name: "Earlier work",
    });
    expect(got?.chat.id).toBe("c-minted");
  });

  // A 200 the client cannot read a chat out of is a FAILURE, not a null the caller
  // has to re-judge: it has adopted a session into a chat it cannot address.
  it("fails when the reply names no chat", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true } });
    const { resumeSession } = await import("./chat.js");
    const got = await resumeSession.dispatch({
      opID: "op-3",
      sessionID: "sess_abc-123",
      name: "Earlier work",
    });
    expect(got).toBeNull();
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
