// Tests for store-load.ts loadMessages pagination — specifically the id-dedupe
// when prepending an older page (a timestamp cursor can re-return a boundary
// message whose ms ts is shared, which must not render/insert twice).

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Message, Session } from "./types.js";

const { sessions, mockApiGetTyped } = vi.hoisted(() => ({
  sessions: new Map<string, Session>(),
  mockApiGetTyped: vi.fn(),
}));

vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));
vi.mock("./api-client.js", () => ({ apiGetTyped: mockApiGetTyped }));
vi.mock("./store.js", () => ({
  get: (id: string) => sessions.get(id),
  getSessions: () => [...sessions.values()],
  setSessions: vi.fn(),
  rebuildMsgIndex: vi.fn(),
  emitMessages: vi.fn(),
  // Identity here — the block-synthesis path is covered by store.test.ts; these
  // tests assert pagination/dedupe by id.
  normalizeMessage: (m: Message) => m,
}));

import { loadMessages } from "./store-load.js";

function msg(id: string, ts: number): Message {
  return { id, role: "assistant", ts } as Message;
}

function seedSession(id: string, messages: Message[]): void {
  sessions.set(id, {
    id,
    messages,
    message_count: messages.length,
    has_more: true,
  } as unknown as Session);
}

beforeEach(() => {
  vi.clearAllMocks();
  sessions.clear();
});

describe("loadMessages pagination dedupe", () => {
  it("dedupes a boundary message by id when prepending an older page", async () => {
    seedSession("c1", [msg("m2", 2), msg("m3", 3)]);
    // The older page overlaps at m2. With the id cursor the server no longer
    // re-returns a boundary message the way the old millisecond cursor could, but
    // the client's id filter still has to make an overlapping or re-issued page
    // harmless rather than a double render that also corrupts the msg index.
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 3 },
      messages: [msg("m1", 1), msg("m2", 2)],
      has_more: false,
    });

    const ok = await loadMessages("c1", "m3");
    expect(ok).toBe(true);
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    // m2 appears once, not twice.
    expect(ids).toEqual(["m1", "m2", "m3"]);
  });

  it("replaces messages wholesale on a first (no-cursor) load", async () => {
    seedSession("c1", [msg("stale", 9)]);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      messages: [msg("a", 1), msg("b", 2)],
      has_more: false,
    });

    await loadMessages("c1");
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    expect(ids).toEqual(["a", "b"]);
  });
});
