// Tests for store-load.ts loadMessages pagination — specifically the id-dedupe
// when prepending an older page (a timestamp cursor can re-return a boundary
// message whose ms ts is shared, which must not render/insert twice).

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Message, Session } from "./types.js";

const {
  sessions,
  liveIDs,
  mockApiGetTyped,
  mockSetSessions,
  mockBumpMessages,
  mockRelatch,
  epoch,
} = vi.hoisted(() => ({
  sessions: new Map<string, Session>(),
  liveIDs: new Map<string, string>(),
  mockApiGetTyped: vi.fn(),
  mockSetSessions: vi.fn(),
  mockBumpMessages: vi.fn(),
  mockRelatch: vi.fn(),
  // The store's transport sync epoch, controllable so a case can land a "gap"
  // at an exact point in the fetch lifecycle.
  epoch: { n: 0 },
}));

vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));
vi.mock("./api-client.js", () => ({ apiGetTyped: mockApiGetTyped }));
vi.mock("./store.js", () => ({
  get: (id: string) => sessions.get(id),
  getSessions: () => [...sessions.values()],
  setSessions: mockSetSessions,
  rebuildMsgIndex: vi.fn(),
  bumpMessages: mockBumpMessages,
  // The outcome relatch loadMessages owes a newest-page load. A fn so the
  // wiring cases below can assert the call and its ordering against bump.
  relatchTurnVerdict: mockRelatch,
  syncEpoch: () => epoch.n,
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
  epoch.n = 0;
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

  it("keeps a scrolled-up window in order, ahead of the page", async () => {
    // The local array holds an older page ahead of the newest one. Both older
    // messages sit BEFORE the page's oldest, so the page says nothing about them
    // and they stay where they are — this used to drop them, which was lossless
    // only while a page was every real conversation whole.
    seedSession("c1", [msg("old1", 1), msg("old2", 2), msg("c", 3)]);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 1 },
      messages: [msg("c", 3)],
      has_more: true,
    });

    await loadMessages("c1");
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    expect(ids).toEqual(["old1", "old2", "c"]);
  });
});

describe("residency", () => {
  // Only a successful NEWEST-page load may claim `loaded`: it is what the
  // activation refetch gate trusts, so nothing weaker (background ingest, an
  // older-page prepend, a failed fetch) can be allowed to set it.
  it("marks the chat loaded on a successful newest-page load", async () => {
    seedSession("c1", []);
    sessions.get("c1")!.residency = "evicted";
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 1 },
      messages: [msg("a", 1)],
      has_more: false,
    });

    const ok = await loadMessages("c1");
    expect(ok).toBe(true);
    expect(sessions.get("c1")?.residency).toBe("loaded");
  });

  it("an older-page prepend asserts nothing about residency", async () => {
    seedSession("c1", [msg("m2", 2)]);
    sessions.get("c1")!.residency = "partial";
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      messages: [msg("m1", 1)],
      has_more: false,
    });

    await loadMessages("c1", "m2");
    expect(sessions.get("c1")?.residency).toBe("partial");
  });

  it("a failed newest-page load claims nothing", async () => {
    seedSession("c1", []);
    sessions.get("c1")!.residency = "evicted";
    mockApiGetTyped.mockResolvedValue(null);

    const ok = await loadMessages("c1");
    expect(ok).toBe(false);
    expect(sessions.get("c1")?.residency).toBe("evicted");
  });

  it("loadList carries residency across the header rebuild", async () => {
    // The header list rebuilds Session objects from the server's headers, and
    // residency is a client-only fact about the carried-over window: dropping
    // it would make every reconnect read a loaded chat as never-loaded.
    seedSession("c1", [msg("a", 1)]);
    sessions.get("c1")!.residency = "loaded";
    mockApiGetTyped.mockResolvedValue({
      chats: [{ id: "c1", name: "One", message_count: 1, usage: {} }],
    });

    await loadList();
    const passed = (mockSetSessions.mock.calls.at(-1)?.[0] ?? []) as Session[];
    expect(passed[0]?.residency).toBe("loaded");
  });
});

describe("loadedEpoch", () => {
  // The freshness stamp is the epoch captured BEFORE the request went out, so a
  // window assembled from an answer that never raced a gap claims the current
  // epoch and reads fresh.
  it("stamps the pre-request epoch on a successful newest-page load", async () => {
    seedSession("c1", []);
    epoch.n = 3;
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 1 },
      messages: [msg("a", 1)],
      has_more: false,
    });

    const ok = await loadMessages("c1");
    expect(ok).toBe(true);
    expect(sessions.get("c1")?.loadedEpoch).toBe(3);
  });

  // Race order one: the gap lands while the fetch is IN FLIGHT. The answer may
  // predate events the gap dropped, so the stamp must be the pre-gap number —
  // never equal to the bumped epoch — and the window stays stale.
  it("a fetch that raced a gap stores a stamp that already reads stale", async () => {
    seedSession("c1", []);
    epoch.n = 3;
    mockApiGetTyped.mockImplementation(() => {
      // The gap arrives after the request went out, before the answer lands.
      epoch.n = 4;
      return Promise.resolve({
        chat: { message_count: 1 },
        messages: [msg("a", 1)],
        has_more: false,
      });
    });

    await loadMessages("c1");
    expect(sessions.get("c1")?.loadedEpoch).toBe(3);
    expect(sessions.get("c1")?.loadedEpoch).not.toBe(epoch.n);
  });

  // Race order two: the gap lands AFTER the load completed. The stamp is a
  // capture, not a live read, so it keeps naming the epoch the window was
  // fetched under and the comparison flips stale on its own.
  it("a gap after completion leaves the stored stamp behind the epoch", async () => {
    seedSession("c1", []);
    epoch.n = 3;
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 1 },
      messages: [msg("a", 1)],
      has_more: false,
    });

    await loadMessages("c1");
    expect(sessions.get("c1")?.loadedEpoch).toBe(3);

    epoch.n = 4;
    expect(sessions.get("c1")?.loadedEpoch).toBe(3);
  });

  it("an older-page prepend stamps nothing", async () => {
    seedSession("c1", [msg("m2", 2)]);
    sessions.get("c1")!.loadedEpoch = 1;
    epoch.n = 2;
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      messages: [msg("m1", 1)],
      has_more: false,
    });

    await loadMessages("c1", "m2");
    expect(sessions.get("c1")?.loadedEpoch).toBe(1);
  });

  it("a failed newest-page load stamps nothing", async () => {
    seedSession("c1", []);
    epoch.n = 2;
    mockApiGetTyped.mockResolvedValue(null);

    const ok = await loadMessages("c1");
    expect(ok).toBe(false);
    expect(sessions.get("c1")?.loadedEpoch).toBeUndefined();
  });

  it("loadList carries loadedEpoch across the header rebuild", async () => {
    // Same reason residency travels: the stamp describes the carried-over
    // window, and dropping it would read every loaded chat as stale after any
    // reconnect's header refresh.
    seedSession("c1", [msg("a", 1)]);
    sessions.get("c1")!.residency = "loaded";
    sessions.get("c1")!.loadedEpoch = 2;
    mockApiGetTyped.mockResolvedValue({
      chats: [{ id: "c1", name: "One", message_count: 1, usage: {} }],
    });

    await loadList();
    const passed = (mockSetSessions.mock.calls.at(-1)?.[0] ?? []) as Session[];
    expect(passed[0]?.loadedEpoch).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// The outcome relatch: a newest-page load re-derives the turn latches from the
// persisted record it just applied. The latches are client memory, dropped by
// every gap and absent on every fresh page, while turn_outcome is durable —
// this call is the heal path that stopped a finished turn's green dot falling
// to the hollow idle ring whenever the connection blinked.
// ---------------------------------------------------------------------------

describe("loadMessages outcome relatch", () => {
  it("relatches after a newest-page load, once the window is settled", async () => {
    seedSession("c1", []);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 1 },
      messages: [msg("m1", 1)],
      has_more: false,
    });

    await loadMessages("c1");
    expect(mockRelatch).toHaveBeenCalledExactlyOnceWith("c1");
    // After bumpMessages: the repaint and the dot must read one settled window.
    const bumpOrder = mockBumpMessages.mock.invocationCallOrder[0] ?? Infinity;
    const relatchOrder = mockRelatch.mock.invocationCallOrder[0] ?? 0;
    expect(relatchOrder).toBeGreaterThan(bumpOrder);
  });

  it("does not relatch on an older-page prepend", async () => {
    // A scroll-up extends the window; it says nothing new about how the last
    // turn ended, and relatching from mid-history would be wrong anyway.
    seedSession("c1", [msg("m2", 2)]);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      messages: [msg("m1", 1)],
      has_more: false,
    });

    await loadMessages("c1", "m2");
    expect(mockRelatch).not.toHaveBeenCalled();
  });

  it("does not relatch on a failed load", async () => {
    seedSession("c1", []);
    mockApiGetTyped.mockResolvedValue(null);

    await loadMessages("c1");
    expect(mockRelatch).not.toHaveBeenCalled();
  });
});

describe("the no-cursor reload keeps the older pages already resident", () => {
  // The byte budget changed what a newest page IS. While `limit = 50` messages
  // returned every real conversation whole, "replace with the window" was
  // lossless. Under the budget the newest page is frequently ONE message, and the
  // reachable caller with no cursor is the gap heal — so a blind replace threw a
  // paged-up reader's history away, and their scroll position with it.
  it("re-adopts the messages older than the page's oldest", async () => {
    seedSession("c1", [msg("m1", 1), msg("m2", 2), msg("m3", 3), msg("m4", 4)]);
    // The page is the newest two, which is what a 1 MiB budget answers for a chat
    // whose recent messages are large.
    mockApiGetTyped.mockResolvedValue({
      chat: { id: "c1", message_count: 4 },
      messages: [msg("m3", 3), msg("m4", 4)],
      has_more: true,
      draft: "",
    });

    await loadMessages("c1");

    expect(sessions.get("c1")?.messages.map((m) => m.id)).toEqual(["m1", "m2", "m3", "m4"]);
  });

  // `has_more` answers "is there anything older than the OLDEST MESSAGE HELD".
  // Re-adopting does not move that, so the page's own answer — which is about the
  // PAGE's start — must not overwrite it.
  it("leaves has_more alone when it re-adopted, because the client's oldest did not move", async () => {
    seedSession("c1", [msg("m1", 1), msg("m2", 2), msg("m3", 3)]);
    sessions.get("c1")!.has_more = false;
    mockApiGetTyped.mockResolvedValue({
      chat: { id: "c1", message_count: 3 },
      messages: [msg("m3", 3)],
      has_more: true,
      draft: "",
    });

    await loadMessages("c1");

    expect(sessions.get("c1")?.has_more).toBe(false);
  });

  it("adopts the page's has_more when nothing was re-adopted", async () => {
    seedSession("c1", [msg("m3", 3)]);
    sessions.get("c1")!.has_more = false;
    mockApiGetTyped.mockResolvedValue({
      chat: { id: "c1", message_count: 9 },
      messages: [msg("m3", 3)],
      has_more: true,
      draft: "",
    });

    await loadMessages("c1");

    expect(sessions.get("c1")?.has_more).toBe(true);
  });

  // No overlap means the window moved out from under what is held, so the page
  // replaces. Anchoring on the page's oldest id is what makes that decidable
  // without a count or a timestamp.
  it("replaces when the page shares no message with what is held", async () => {
    seedSession("c1", [msg("old1", 1), msg("old2", 2)]);
    mockApiGetTyped.mockResolvedValue({
      chat: { id: "c1", message_count: 2 },
      messages: [msg("new1", 8), msg("new2", 9)],
      has_more: true,
      draft: "",
    });

    await loadMessages("c1");

    expect(sessions.get("c1")?.messages.map((m) => m.id)).toEqual(["new1", "new2"]);
  });

  // The in-flight turn still has to survive, and it still goes at the END: the
  // server accumulates it in memory and appends it to the chat file once, at
  // turn_ended, so no page can carry it.
  it("keeps the in-flight turn at the end while re-adopting the older pages", async () => {
    seedSession("c1", [msg("m1", 1), msg("m2", 2), msg("live", 3)]);
    liveIDs.set("c1", "live");
    mockApiGetTyped.mockResolvedValue({
      chat: { id: "c1", message_count: 2 },
      messages: [msg("m2", 2)],
      has_more: true,
      draft: "",
    });

    await loadMessages("c1");

    expect(sessions.get("c1")?.messages.map((m) => m.id)).toEqual(["m1", "m2", "live"]);
  });
});
