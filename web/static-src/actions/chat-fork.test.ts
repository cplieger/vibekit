// @vitest-environment happy-dom
// Tests for forkChat optimistic freeze + rollback.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../transport.js", () => ({ send: vi.fn() }));

import { send as transportSend } from "../transport.js";
import { setSessions, get, setActive } from "../store.js";
import { forkChat } from "./chat.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import type { Session } from "../types.js";

const mockSend = vi.mocked(transportSend);

function makeSession(id: string): Session {
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
  };
}

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
  setSessions([makeSession("parent-1")]);
  setActive("parent-1");
});

describe("forkChat optimistic + rollback", () => {
  it("sets frozen=true optimistically on dispatch", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await forkChat.dispatch({ chatID: "parent-1", tangentID: "t-1" });
    expect(get("parent-1")!.frozen).toBe(true);
  });

  it("rolls back frozen on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "server error" });
    await forkChat.dispatch({ chatID: "parent-1", tangentID: "t-1" });
    expect(get("parent-1")!.frozen).toBeUndefined();
  });
});
