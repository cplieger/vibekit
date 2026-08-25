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

  // The newest page REPLACES the persisted transcript but keeps the local TAIL:
  // anything after the newest message the page carries, that the page does not
  // have. The in-flight turn lives in the server's in-memory assistant buffer
  // and is flushed to the chat file once, at turn_ended — so it is absent from
  // this page while `turn_state` has already put it in the store. A blind
  // whole-array replace therefore DELETED the reply the reader was watching,
  // every time this ran mid-turn.
  it("replaces the persisted page and keeps a local message the page does not carry", async () => {
    seedSession("c1", [msg("streaming", 9)]);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      messages: [msg("a", 1), msg("b", 2)],
      has_more: false,
    });

    await loadMessages("c1");
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    expect(ids).toEqual(["a", "b", "streaming"]);
  });

  // The tail rule is deliberately blind to WHY a local message is missing from
  // the page, because from here the two reasons look identical: it may be the
  // in-flight turn the server has not flushed yet, or a turn a rewind removed.
  // Rewind does not depend on this path to notice — CmdRewindChat truncates the
  // client's own array when it lands, so by the time a page is fetched the
  // message is already gone locally. What this pins is that a message present in
  // BOTH is the page's business and is never duplicated.
  it("never duplicates a message the page also carries", async () => {
    seedSession("c1", [msg("a", 1), msg("b", 2)]);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      messages: [msg("a", 1), msg("b", 2)],
      has_more: false,
    });

    await loadMessages("c1");
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    expect(ids).toEqual(["a", "b"]);
  });

  it("keeps a scrolled-up window in order rather than re-appending it", async () => {
    // The local array holds an older page ahead of the newest one. The anchor is
    // the newest message the page carries ("c"), so nothing before it is treated
    // as a tail and the older messages are simply dropped with the replace —
    // which is what the pre-existing behaviour did and what keeps the order right.
    seedSession("c1", [msg("old1", 1), msg("old2", 2), msg("c", 3)]);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 1 },
      messages: [msg("c", 3)],
      has_more: true,
    });

    await loadMessages("c1");
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    expect(ids).toEqual(["c"]);
  });
});
