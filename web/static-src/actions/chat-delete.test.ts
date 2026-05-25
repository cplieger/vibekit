// @vitest-environment happy-dom
// Tests for deleteChat, archiveChat, discardTangent optimistic + rollback.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../transport.js", () => ({ send: vi.fn() }));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

import { send as transportSend } from "../transport.js";
import { setSessions, get, setActive, getSessions, setFrozen } from "../store.js";
import { deleteChat, archiveChat, discardTangent } from "./chat.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import type { Session } from "../types.js";

const mockSend = vi.mocked(transportSend);
const mockFetch = vi.fn();

function makeSession(id: string, extra?: Partial<Session>): Session {
  return {
    id, name: "test", agent: "", model: "",
    acp_session_id: "", current_mode_id: "",
    available_modes: [], available_models: [],
    available_commands: [], available_prompts: [],
    auto_approve_crew: false, supervised_mode: false,
    pending_changes: [],
    usage: { context_pct: 0, context_size: 0, credits: 0, turn_count: 0, last_turn_ms: 0, has_real_data: false },
    message_count: 0, messages: [], has_more: false,
    thinking: false, working_label: "Thinking",
    ...extra,
  };
}

beforeEach(() => {
  resetDefine();
  resetRegistry();
  vi.clearAllMocks();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
  setSessions([makeSession("s1"), makeSession("s2"), makeSession("s3")]);
  setActive("s1");
});

describe("deleteChat optimistic + rollback", () => {
  it("removes session optimistically on dispatch", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await deleteChat.dispatch("s2");
    expect(get("s2")).toBeUndefined();
  });

  it("reinserts session at original index on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    await deleteChat.dispatch("s2");
    expect(get("s2")).toBeDefined();
    expect(getSessions().findIndex((s) => s.id === "s2")).toBe(1);
  });

  it("does not rollback on success", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await deleteChat.dispatch("s1");
    expect(get("s1")).toBeUndefined();
  });
});

describe("archiveChat optimistic + rollback", () => {
  it("removes session optimistically", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await archiveChat.dispatch("s2");
    expect(get("s2")).toBeUndefined();
  });

  it("reinserts session on HTTP error", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await archiveChat.dispatch("s2");
    expect(get("s2")).toBeDefined();
    expect(getSessions().findIndex((s) => s.id === "s2")).toBe(1);
  });
});

describe("discardTangent optimistic + rollback", () => {
  it("removes tangent and unfreezes parent optimistically", async () => {
    setSessions([makeSession("parent", { frozen: true }), makeSession("tangent", { parent_chat_id: "parent", is_tangent: true })]);
    setActive("tangent");
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await discardTangent.dispatch("tangent");
    expect(get("tangent")).toBeUndefined();
    expect(get("parent")?.frozen).toBeUndefined();
  });

  it("reinserts tangent and re-freezes parent on failure", async () => {
    setSessions([makeSession("parent", { frozen: true }), makeSession("tangent", { parent_chat_id: "parent", is_tangent: true })]);
    setActive("tangent");
    setFrozen("parent", true);
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    await discardTangent.dispatch("tangent");
    expect(get("tangent")).toBeDefined();
    expect(get("parent")?.frozen).toBe(true);
  });

  it("handles missing parent gracefully", async () => {
    setSessions([makeSession("orphan")]);
    setActive("orphan");
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    await discardTangent.dispatch("orphan");
    expect(get("orphan")).toBeDefined();
  });
});
