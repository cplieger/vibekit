// Tests for store-load.ts loadMessages pagination — specifically the id-dedupe
// when prepending an older page (a timestamp cursor can re-return a boundary
// message whose ms ts is shared, which must not render/insert twice).

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Message, Session } from "./types.js";

const { sessions, liveIDs, mockApiGetTyped, mockSetSessions } = vi.hoisted(() => ({
  sessions: new Map<string, Session>(),
  liveIDs: new Map<string, string>(),
  mockApiGetTyped: vi.fn(),
  mockSetSessions: vi.fn(),
}));

vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));
vi.mock("./api-client.js", () => ({ apiGetTyped: mockApiGetTyped }));
vi.mock("./store.js", () => ({
  get: (id: string) => sessions.get(id),
  getSessions: () => [...sessions.values()],
  setSessions: mockSetSessions,
  rebuildMsgIndex: vi.fn(),
  emitMessages: vi.fn(),
  // Identity here — the block-synthesis path is covered by store.test.ts; these
  // tests assert pagination/dedupe by id.
  normalizeMessage: (m: Message) => m,
  // The store's in-flight marker: which message id the chat's current turn is
  // streaming into, and therefore which one the chat file cannot carry yet.
  liveTurnMessage: (id: string) => liveIDs.get(id),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  getActive: vi.fn(() => undefined),
  tabStatusFor: vi.fn(() => ""),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGet: vi.fn(),
}));

import { loadMessages, loadList } from "./store-load.js";

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
  liveIDs.clear();
});

describe("loadList pruning", () => {
  // The unacknowledged-chat exemption is GONE, and this is the case that used to
  // need it, asserted from the other side. A chat minted client-side was absent
  // from /api/chats by definition, so `loadList` pruned its row on every SSE
  // `connected` and nothing could bring it back. Server-minted ids remove the
  // state: a chat with a store row is a chat the server has, so absence from the
  // listing means DELETED and pruning is the correct answer.
  it("prunes a chat the server does not list, with no unacknowledged exemption", async () => {
    seedSession("real", []);
    sessions.set("c-untracked", {
      id: "c-untracked",
      messages: [],
      message_count: 0,
    } as unknown as Session);
    mockApiGetTyped.mockResolvedValue({
      chats: [{ id: "real", name: "Real", message_count: 0, usage: {} }],
    });

    const ok = await loadList();
    expect(ok).toBe(true);
    const passed = (mockSetSessions.mock.calls.at(-1)?.[0] ?? []) as Session[];
    expect(passed.map((s) => s.id)).toEqual(["real"]);
  });

  // The direction the rescue above does NOT cover, kept so the prune cannot be
  // read as "never prune": a chat the server really has forgotten still goes.
  it("still prunes an acknowledged chat the server no longer lists", async () => {
    seedSession("gone", []);
    mockApiGetTyped.mockResolvedValue({
      chats: [{ id: "kept", name: "Kept", message_count: 0, usage: {} }],
    });

    await loadList();
    const passed = (mockSetSessions.mock.calls.at(-1)?.[0] ?? []) as Session[];
    expect(passed.map((s) => s.id)).toEqual(["kept"]);
  });
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

  // The newest page REPLACES the persisted transcript but keeps the in-flight
  // turn: the server accumulates it in an in-memory buffer and appends it to the
  // chat file once, at turn_ended, so it is absent from this page while
  // `turn_state` has already put it in the store. A blind whole-array replace
  // therefore DELETED the reply the reader was watching, every time this ran
  // mid-turn.
  it("replaces the persisted page and keeps the in-flight turn", async () => {
    seedSession("c1", [msg("streaming", 9)]);
    liveIDs.set("c1", "streaming");
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      messages: [msg("a", 1), msg("b", 2)],
      has_more: false,
    });

    await loadMessages("c1");
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    expect(ids).toEqual(["a", "b", "streaming"]);
  });

  // The reported bug, and the reason the boundary is a NAMED message rather than
  // a position. The agent persists messages DURING a turn — HandlePlan appends
  // one per plan update, and compaction, an infra-safety block and a cancel each
  // append an event — so the newest id the page carries is routinely NEWER than
  // the streaming reply. The old rule kept "everything after that id", which was
  // nothing, and the replace dropped the reply: the reader switched tabs, came
  // back to their own prompt above an empty turn body, and only a reload brought
  // the output back, by which time the buffer had flushed to the file.
  it("keeps the in-flight turn when the page carries a message persisted after it", async () => {
    seedSession("c1", [msg("user", 1), msg("streaming", 2), msg("plan", 3)]);
    liveIDs.set("c1", "streaming");
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      // What the chat file holds mid-turn: the prompt and the plan, not the reply.
      messages: [msg("user", 1), msg("plan", 3)],
      has_more: false,
    });

    await loadMessages("c1");
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    expect(ids).toEqual(["user", "plan", "streaming"]);
  });

  // The other side of the same marker: once the turn ends the server persists the
  // reply, `message_appended` clears the marker, and the page is authoritative
  // for it. A second copy kept here would double-render the turn.
  it("drops the local copy once the page carries the finished turn", async () => {
    seedSession("c1", [msg("user", 1), msg("reply", 2)]);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      messages: [msg("user", 1), msg("reply", 2)],
      has_more: false,
    });

    await loadMessages("c1");
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    expect(ids).toEqual(["user", "reply"]);
  });

  // A message the server persisted and broadcast while this request was in
  // flight is newer than the answer being applied, so the answer cannot drop it.
  // Nothing refetches on its own, so without this it would be missing from the
  // transcript until the next tab switch.
  it("keeps a message that arrived while the request was in flight", async () => {
    seedSession("c1", [msg("a", 1)]);
    mockApiGetTyped.mockImplementation(() => {
      sessions.get("c1")?.messages.push(msg("raced", 2));
      return Promise.resolve({
        chat: { message_count: 1 },
        messages: [msg("a", 1)],
        has_more: false,
      });
    });

    await loadMessages("c1");
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    expect(ids).toEqual(["a", "raced"]);
  });

  // The rule is deliberately blind to WHY the page omits the in-flight message,
  // because from here the two reasons look identical: it may be the turn the
  // server has not flushed yet, or a turn a rewind removed. What this pins is
  // that a message present in BOTH is the page's business and is never
  // duplicated.
  it("never duplicates a message the page also carries", async () => {
    seedSession("c1", [msg("a", 1), msg("b", 2)]);
    liveIDs.set("c1", "b");
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
    // The local array holds an older page ahead of the newest one. Neither older
    // message is the in-flight turn and both were present before the request, so
    // they are dropped with the replace — which is what keeps the order right,
    // and what pagination re-fetches.
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
