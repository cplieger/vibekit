// Tests for store-load.ts loadMessages pagination — specifically the id-dedupe
// when prepending an older page (a timestamp cursor can re-return a boundary
// message whose ms ts is shared, which must not render/insert twice).

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Message, Session } from "./types.js";
// The module's own shape, for the fresh-instance loader at the foot of this file:
// `chatListLoaded` is module state, so its cases re-evaluate the module and need a
// type for what the dynamic import hands back.
import type * as StoreLoad from "./store-load.js";

const {
  sessions,
  liveIDs,
  mockApiGetTyped,
  mockApiGetTypedOrError,
  mockSetSessions,
  mockUpsertHeader,
  mockBumpMessages,
  mockRelatch,
  mockLatchFields,
  epoch,
} = vi.hoisted(() => ({
  sessions: new Map<string, Session>(),
  liveIDs: new Map<string, string>(),
  mockApiGetTyped: vi.fn(),
  // The status-bearing GET. `confirmChatExists` reads the STATUS rather than a
  // collapsed null, so its fixture is the whole `ApiResult` envelope.
  mockApiGetTypedOrError: vi.fn(),
  mockSetSessions: vi.fn(),
  // The single adoption door for a chat header. A spy so the confirm cases can
  // assert that a chat the server DOES know lands in the store.
  mockUpsertHeader: vi.fn(),
  mockBumpMessages: vi.fn(),
  mockRelatch: vi.fn(),
  // The header-derived latch seed. An INERT stub here on purpose: this file
  // asserts the WIRING (that loadList consults it, with the existing row and the
  // header), while what it returns and how that reaches the dot is asserted
  // against the real store in tab-dot.test.ts. A stub that reproduced the real
  // mapping would be a second copy of the logic under test.
  // Params are declared so the call tuple is typed and the assertions below can
  // read `calls[n][0]` (the existing row) and `calls[n][1]` (the header).
  mockLatchFields: vi.fn((_existing: unknown, _header: unknown) => ({})),
  // The store's transport sync epoch, controllable so a case can land a "gap"
  // at an exact point in the fetch lifecycle.
  epoch: { n: 0 },
}));

vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));
vi.mock("./api-client.js", () => ({
  apiGetTyped: mockApiGetTyped,
  apiGetTypedOrError: mockApiGetTypedOrError,
}));
vi.mock("./store.js", () => ({
  get: (id: string) => sessions.get(id),
  getSessions: () => [...sessions.values()],
  setSessions: mockSetSessions,
  upsertHeader: mockUpsertHeader,
  rebuildMsgIndex: vi.fn(),
  bumpMessages: mockBumpMessages,
  // The outcome relatch loadMessages owes a newest-page load. A fn so the
  // wiring cases below can assert the call and its ordering against bump.
  relatchTurnVerdict: mockRelatch,
  latchFieldsFor: mockLatchFields,
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

import { loadMessages, loadList, confirmChatExists } from "./store-load.js";

function msg(id: string, ts: number): Message {
  return { id, role: "assistant", ts } as Message;
}

/** A turn's plan row: RoleAssistant, so only its shape tells it from a reply. */
function planRow(id: string, ts: number): Message {
  return { id, role: "assistant", ts, plan: [] } as unknown as Message;
}

/** A user row, the one shape that OPENS a turn and so belongs after the reply. */
function userRow(id: string, ts: number): Message {
  return { id, role: "user", ts } as Message;
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

  // Same rule the live ingest path applies, in the one window it cannot reach: a
  // row persisted DURING the fetch goes before the unflushed reply, and a row that
  // OPENS a turn goes after it. Without this the re-adoption appended everything,
  // so a plan row ingested mid-fetch landed below the reply — contradicting where
  // the same row goes when no fetch is racing it, and where the file has it.
  it("re-adopts a row ingested mid-fetch ahead of the in-flight turn", async () => {
    seedSession("c1", [msg("live", 5)]);
    liveIDs.set("c1", "live");
    mockApiGetTyped.mockImplementation(() => {
      // Both land while the request is in flight, and both land AFTER the live
      // message locally — which is where appending them left them.
      sessions.get("c1")?.messages.push(planRow("plan", 6), userRow("u-2", 7));
      return Promise.resolve({
        chat: { message_count: 1 },
        messages: [msg("page", 1)],
        has_more: false,
      });
    });

    await loadMessages("c1");
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    expect(ids).toEqual(["page", "plan", "live", "u-2"]);
  });

  // Inert with no in-flight turn among the kept rows: with nothing to insert
  // against, local order is already the answer and re-ordering would invent one.
  it("leaves the kept rows in local order when no turn is in flight", async () => {
    seedSession("c1", []);
    mockApiGetTyped.mockImplementation(() => {
      sessions.get("c1")?.messages.push(userRow("u-2", 6), planRow("plan", 7));
      return Promise.resolve({
        chat: { message_count: 1 },
        messages: [msg("page", 1)],
        has_more: false,
      });
    });

    await loadMessages("c1");
    const ids = (sessions.get("c1")?.messages ?? []).map((m) => m.id);
    expect(ids).toEqual(["page", "u-2", "plan"]);
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

// ---------------------------------------------------------------------------
// The header-derived latch seed.
//
// `loadList` runs at boot AND on every SSE `connected`, so it is the one door
// that covers a fresh page, a brand-new browser session and a reconnect after
// hours. It rebuilds every Session from a ChatHeader, and the two outcome latches
// used to be a pure carry-over from an EXISTING in-memory session — so a client
// that had just connected had nothing to carry and every tab fell to the hollow
// `idle` ring however its last turn had really ended.
//
// These cases pin the WIRING: that the seed is consulted at all, and that it is
// handed the two inputs its rules need. The mapping itself is store.test.ts's,
// and the user-visible dot is tab-dot.test.ts's.
// ---------------------------------------------------------------------------

describe("loadList seeds the outcome latches from the header", () => {
  it("consults the seed once per listed chat, with the header that carries the outcome", async () => {
    mockApiGetTyped.mockResolvedValue({
      chats: [
        { id: "c1", name: "One", message_count: 0, usage: {}, last_turn_outcome: "completed" },
        { id: "c2", name: "Two", message_count: 0, usage: {} },
      ],
    });

    expect(await loadList()).toBe(true);
    expect(mockLatchFields).toHaveBeenCalledTimes(2);
    expect(mockLatchFields.mock.calls[0]?.[1]).toMatchObject({
      id: "c1",
      last_turn_outcome: "completed",
    });
    expect(mockLatchFields.mock.calls[1]?.[1]).toMatchObject({ id: "c2" });
  });

  it("passes the EXISTING row so the seed can see a local latch and a live turn", async () => {
    // Both of the seed's first two rules read the existing session, so handing it
    // undefined would silently make the local verdict lose to the header's.
    seedSession("c1", [msg("m1", 1)]);
    mockApiGetTyped.mockResolvedValue({
      chats: [{ id: "c1", name: "One", message_count: 1, usage: {} }],
    });

    await loadList();
    expect(mockLatchFields.mock.calls[0]?.[0]).toMatchObject({ id: "c1" });
  });

  it("passes undefined for a chat this client has never seen", async () => {
    mockApiGetTyped.mockResolvedValue({
      chats: [{ id: "fresh", name: "Fresh", message_count: 0, usage: {} }],
    });

    await loadList();
    expect(mockLatchFields.mock.calls[0]?.[0]).toBeUndefined();
  });

  it("spreads whatever the seed returns onto the rebuilt session", async () => {
    mockLatchFields.mockReturnValue({ turn_done: true } as never);
    mockApiGetTyped.mockResolvedValue({
      chats: [{ id: "c1", name: "One", message_count: 0, usage: {} }],
    });

    await loadList();
    const passed = (mockSetSessions.mock.calls.at(-1)?.[0] ?? []) as Session[];
    expect(passed[0]?.turn_done).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// `turn_open`: the server's liveness statement, riding the transcript response.
//
// The in-flight reply is in the server's in-memory buffer, so a turn in flight has
// no carrier in this payload — and the client used to read that silence as "nothing
// closed this turn" and derive `unknown`, a terminal verdict during a window in
// which nothing can know one. Shipping the liveness in the SAME response is what
// removes the window: there is no gap between the transcript painting and the
// verdict arriving, because they are one payload.
// ---------------------------------------------------------------------------

describe("loadMessages turn_open", () => {
  it("stores the server's statement from a newest-page load", async () => {
    seedSession("c1", []);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 1 },
      messages: [userRow("u1", 1)],
      has_more: false,
      turn_open: true,
    });

    await loadMessages("c1");
    expect(sessions.get("c1")?.turn_open).toBe(true);
  });

  it("never reads NOT LIVE as live when the field is ABSENT", async () => {
    // Optional-tolerant for the same reason `draft` is: a server that predates the
    // field, or a proxy that strips it, must not fail the whole chat load. Client
    // and server ship in one image, so this is a guard rather than a path.
    //
    // The assertion is `not true` rather than `false` because this suite mocks
    // `apiGetTyped`, so the raw object is handed straight to the loader and
    // `decodeChatGetResponseLocal` — which is where `o["turn_open"] === true`
    // coerces absent to false — never runs. What the loader owes either way is the
    // property `turnLive` reads (`turn_open === true`), and an absent field must not
    // satisfy it: reading a missing statement as live would claim a turn is running
    // on every chat an older server serves.
    seedSession("c1", []);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 1 },
      messages: [userRow("u1", 1)],
      has_more: false,
    });

    await loadMessages("c1");
    expect(sessions.get("c1")?.turn_open).not.toBe(true);
  });

  it("does not write it on an OLDER-page fetch", async () => {
    // A scroll-up asserts nothing about whether a turn is running NOW, so it must
    // not restate liveness — same rule the draft already follows. A stale `false`
    // written here would put the projection back on `thinking` alone mid-turn.
    seedSession("c1", [msg("m2", 2)]);
    sessions.set("c1", { ...(sessions.get("c1") as Session), turn_open: true });
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      messages: [msg("m1", 1)],
      has_more: false,
      turn_open: false,
    });

    await loadMessages("c1", "m2");
    expect(sessions.get("c1")?.turn_open).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// The render cause a fetched window is announced with.
//
// A fetched page is a REPLAY, and the paint has no way to know it from the array:
// `messages.ts` marks the rows that arrived since its last pass and gives only
// those the entry animation and the live-edge pin, and a cold open paints on
// `setActive` BEFORE this fetch resolves — so the paint this drives is not a chat
// switch, and its predecessor recorded no tail to append past. Announced as
// `shape`, that state is indistinguishable from a first prompt, and a reopened
// conversation's whole window read as arrivals: every row animated in unison, and
// every user turn in it asked the scroller for the live edge.
// ---------------------------------------------------------------------------

describe("loadMessages announces a fetched window as a replay", () => {
  it("bumps the newest page with the load cause", async () => {
    seedSession("c1", []);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      messages: [userRow("u1", 1), msg("a1", 2)],
      has_more: false,
    });

    await loadMessages("c1");
    expect(mockBumpMessages).toHaveBeenCalledExactlyOnceWith("c1", "load");
  });

  it("bumps an older-page prepend with it too", async () => {
    // The same statement from the other branch. The tail arithmetic already read a
    // prepend's rows as silent, so this is what makes that a claim the loader makes
    // rather than a coincidence of where the tail sits.
    seedSession("c1", [msg("m2", 2)]);
    mockApiGetTyped.mockResolvedValue({
      chat: { message_count: 2 },
      messages: [msg("m1", 1)],
      has_more: false,
    });

    await loadMessages("c1", "m2");
    expect(mockBumpMessages).toHaveBeenCalledExactlyOnceWith("c1", "load");
  });
});

// ---------------------------------------------------------------------------
// `chatListLoaded`: whether the store is ENTITLED to say a chat does not exist.
//
// An empty store has two meanings and they want opposite answers — the server said
// there are no such chats, or the server could not be reached — and `app.ts` reaches
// the second one on every boot whose chat fetch failed: it toasts, creates a fresh
// chat, and then applies the URL's route anyway. So without this predicate a reload
// of any `/chat/<id>` against a restarting server rewrote the URL and claimed the
// conversation no longer exists, seconds after saying the chats could not be loaded.
// A terminal verdict derived from absent data, which is the defect class `turn_open`
// removes one surface over.
//
// Each case takes a FRESH module instance, because the latch is module state and a
// successful load anywhere in this file would otherwise decide the answer for the
// rest of it.
// ---------------------------------------------------------------------------

let bootSeq = 0;

/** A fresh `store-load` instance, so the latch starts where a page load starts.
 *
 *  The busted specifier is what makes it fresh: the browser's module map is
 *  URL-keyed, so `vi.resetModules()` alone hands back the cached instance. The
 *  `.ts` extension is mandatory — written `.js` the suite stays green while v8
 *  attributes every evaluation to a file that does not exist. */
async function freshLoader(): Promise<typeof StoreLoad> {
  vi.resetModules();
  bootSeq++;
  return (await import(/* @vite-ignore */ `./store-load.ts?boot=${bootSeq}`)) as typeof StoreLoad;
}

describe("chatListLoaded", () => {
  it("is false before any list has been read", async () => {
    const loader = await freshLoader();
    expect(loader.chatListLoaded()).toBe(false);
  });

  it("is true once a list has landed", async () => {
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue({
      chats: [{ id: "c1", name: "One", message_count: 0, usage: {} }],
    });

    expect(await loader.loadList()).toBe(true);
    expect(loader.chatListLoaded()).toBe(true);
  });

  it("is true for a list that landed EMPTY, which is a real answer", async () => {
    // The distinction the predicate exists for, from the side that is easy to get
    // wrong: a server with no chats HAS answered, so a deep link naming one is
    // genuinely dead and the router is entitled to say so.
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue({ chats: [] });

    expect(await loader.loadList()).toBe(true);
    expect(loader.chatListLoaded()).toBe(true);
  });

  it("stays false when the fetch failed", async () => {
    const loader = await freshLoader();
    // What `apiGetTyped` answers for an unreachable server or an undecodable body.
    mockApiGetTyped.mockResolvedValue(null);

    expect(await loader.loadList()).toBe(false);
    expect(loader.chatListLoaded()).toBe(false);
  });

  it("stays true after a LATER failed refetch", async () => {
    // Latched rather than a snapshot of the last attempt: once a list has landed the
    // store holds a row per chat, and a failed refetch does not un-know them. It
    // also self-heals in the other direction, because `loadList` runs on every SSE
    // `connected`.
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue({
      chats: [{ id: "c1", name: "One", message_count: 0, usage: {} }],
    });
    await loader.loadList();

    mockApiGetTyped.mockResolvedValue(null);
    expect(await loader.loadList()).toBe(false);
    expect(loader.chatListLoaded()).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// `serverMayAnswer`: whether asking the server about ONE id can be answered.
//
// The gate on the confirmation round trip, and it exists because `chatListLoaded`
// answered a DIFFERENT question in that role and the difference cost a population.
// The short-circuit argued that a client whose list never loaded would be answered
// `unresolved` one request later, so the trip buys nothing — true when the server
// is down, false when the boot load was ABORTED, which `loadList`'s own first line
// (`listController?.abort()`) makes routine: a `connected`-driven refetch overtakes
// the boot load and it returns false exactly like a load against a dead server. In
// that population the chat exists, the server would answer 200, and the deep link
// dead-ended anyway.
//
// So an abort is recorded as a fact about a REQUEST and nothing else, and only a
// load that resolved and produced no list is evidence about the server.
//
// Fresh module per case, for `chatListLoaded`'s reason: both values are module state.
// ---------------------------------------------------------------------------

describe("serverMayAnswer", () => {
  it("is true before any list has been read", async () => {
    // Nothing has been established, so there is no evidence asking would fail — and
    // a 404 is authoritative whether or not a list ever landed.
    const loader = await freshLoader();
    expect(loader.serverMayAnswer()).toBe(true);
  });

  it("is true once a list has landed", async () => {
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue({ chats: [] });

    expect(await loader.loadList()).toBe(true);
    expect(loader.serverMayAnswer()).toBe(true);
  });

  it("is FALSE when the load resolved and produced no list", async () => {
    // Round 3's population, and the behaviour that must not regress: a reload of any
    // `/chat/<id>` against a restarting server holds the URL and stays quiet, because
    // boot has already said the chats could not be loaded.
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue(null);

    expect(await loader.loadList()).toBe(false);
    expect(loader.serverMayAnswer()).toBe(false);
  });

  it("stays TRUE when an ABORT is the last thing that completed", async () => {
    // The recovered population, and the state the router actually meets. `loadList`
    // aborts whatever is in flight before it starts, so a `connected`-driven refetch
    // overtakes the boot load; the boot load then resolves aborted, boot toasts and
    // creates a fallback chat, and `applyInitialRoute` runs — all while the refetch
    // is still on the wire. So the only COMPLETED attempt at that moment is an abort,
    // and the old gate read it as "do not ask" against a healthy server.
    //
    // The second load deliberately never resolves, which is what makes this the abort
    // state rather than the recovery below: a successful load would overwrite the
    // reach and the assertion would pass whatever the abort recorded.
    const loader = await freshLoader();
    let releaseFirst: (v: unknown) => void = () => undefined;
    mockApiGetTyped.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          releaseFirst = resolve;
        }),
    );
    mockApiGetTyped.mockImplementationOnce(() => new Promise(() => undefined));

    const first = loader.loadList();
    void loader.loadList();
    releaseFirst({ chats: [] });

    expect(await first).toBe(false);
    expect(loader.chatListLoaded()).toBe(false);
    expect(loader.serverMayAnswer()).toBe(true);
  });

  it("stays TRUE after an abort that a later load recovered", async () => {
    // The self-heal, kept beside it: `loadList` runs on every SSE `connected`, so the
    // superseding load is normally the one that lands.
    const loader = await freshLoader();
    let releaseFirst: (v: unknown) => void = () => undefined;
    mockApiGetTyped.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          releaseFirst = resolve;
        }),
    );
    mockApiGetTyped.mockResolvedValue({ chats: [] });

    const first = loader.loadList();
    const second = loader.loadList();
    releaseFirst({ chats: [] });

    expect(await first).toBe(false);
    expect(await second).toBe(true);
    expect(loader.serverMayAnswer()).toBe(true);
  });

  it("is true after a failed refetch that FOLLOWED a successful load", async () => {
    // `listLoaded` is latched and the reach is not, and this is why both are read:
    // the store still holds rows, and only BOOT toasts — so a reader here has been
    // told nothing and a deep link is worth one request plus a retry rather than
    // silence.
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue({
      chats: [{ id: "c1", name: "One", message_count: 0, usage: {} }],
    });
    await loader.loadList();

    mockApiGetTyped.mockResolvedValue(null);
    expect(await loader.loadList()).toBe(false);
    expect(loader.serverMayAnswer()).toBe(true);
  });

  it("goes false again on a fresh page whose FIRST load fails", async () => {
    // The latch is per page load, so the recovery above cannot leak into the next
    // boot and re-open the silent-dead-end the false arm exists to keep closed.
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue(null);

    await loader.loadList();
    expect(loader.serverMayAnswer()).toBe(false);
    expect(loader.chatListLoaded()).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// `confirmChatExists`: the SERVER's answer about an id the store holds no row for.
//
// `chatListLoaded` above answers "has a list ever landed", and the router used to
// treat that as licence to say a conversation no longer exists. It is one claim too
// far: a list is authoritative at the instant it lands and stale from then on, so a
// chat created on another device while this client's SSE was down is absent from a
// store that is otherwise entitled to speak — and the reader got a terminal verdict
// about a conversation that exists. That is the same class as reading an empty store
// as proof of deletion, one population narrower.
//
// So the verdict comes from the server, and BOTH directions are pinned here: what
// licenses the terminal claim (a 404, and a 400 for an id that is not a chat id at
// all) and what refuses it (a 5xx, a dead network, an aborted request, an
// undecodable body). @cplieger/fetch reports the whole no-answer family with either
// status 0 or the real 2xx status plus `code: "decode"`, so none of them can be
// mistaken for a 404.
// ---------------------------------------------------------------------------

/** A chat header the way `/api/chats/{id}` sends one. */
function confirmHeader(id: string): unknown {
  return { id, name: "Made elsewhere", message_count: 3, usage: {} };
}

describe("confirmChatExists", () => {
  it("asks the single-chat endpoint for the named id, and for no transcript", async () => {
    mockApiGetTypedOrError.mockResolvedValue({ ok: false, status: 404, data: null, error: "" });

    await confirmChatExists("c-somewhere");

    // `limit=1` rather than the endpoint's 50-message default: a verdict reads the
    // header, so a page of transcript would be paid for and thrown away.
    expect(mockApiGetTypedOrError.mock.calls[0]?.[0]).toBe("/api/chats/c-somewhere?limit=1");
  });

  it("percent-encodes the id it is handed", async () => {
    mockApiGetTypedOrError.mockResolvedValue({ ok: false, status: 400, data: null, error: "" });

    await confirmChatExists("c-a/b?c");

    expect(mockApiGetTypedOrError.mock.calls[0]?.[0]).toBe("/api/chats/c-a%2Fb%3Fc?limit=1");
  });

  it("answers `exists` and ADOPTS the header for a chat the server knows", async () => {
    // The direction that makes the round trip worth making: the deep link goes on to
    // open rather than dead-ending, and the header lands through the same door the
    // `chat_created` frame this client missed would have used.
    mockApiGetTypedOrError.mockResolvedValue({
      ok: true,
      status: 200,
      data: { chat: confirmHeader("c-elsewhere") },
      error: "",
    });

    expect(await confirmChatExists("c-elsewhere")).toBe("exists");
    expect(mockUpsertHeader).toHaveBeenCalledExactlyOnceWith(confirmHeader("c-elsewhere"));
  });

  it("answers `gone` for a 404, which is the server having read its own store", async () => {
    mockApiGetTypedOrError.mockResolvedValue({
      ok: false,
      status: 404,
      data: null,
      error: "chat not found",
    });

    expect(await confirmChatExists("c-deleted")).toBe("gone");
    expect(mockUpsertHeader).not.toHaveBeenCalled();
  });

  it("answers `gone` for a 400 on an id that is NOT SHAPED like a chat id", async () => {
    // The server's own id-validity rule refused it, so there is no such chat and
    // there never can be. Reading this as unresolved would hold the URL forever on
    // the empty-state hero, which is the silent dead end the toast exists to close.
    mockApiGetTypedOrError.mockResolvedValue({
      ok: false,
      status: 400,
      data: null,
      error: "invalid chat id",
    });

    expect(await confirmChatExists("not a chat id")).toBe("gone");
  });

  it("REFUSES the claim for a 400 on a WELL-SHAPED id", async () => {
    // The narrowing, and the class it closes. A 400 is only evidence about a chat
    // when something ties it to the chat, and the only thing that can is the id
    // itself: measured against the route as it stands every 400 source IS id-shaped,
    // so this changes no verdict today. What it stops is a middleware answering 400
    // later for a request-level reason — a stale CSRF header, a host check, a body
    // limit — being rendered as "that conversation no longer exists", which is the
    // exact false-terminal-claim class this whole path exists to eliminate.
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    mockApiGetTypedOrError.mockResolvedValue({
      ok: false,
      status: 400,
      data: null,
      error: "invalid origin",
    });

    expect(await confirmChatExists("c-0123456789abcdef")).toBe("unresolved");
    expect(mockUpsertHeader).not.toHaveBeenCalled();
  });

  it("keeps a 404 authoritative for a well-shaped id", async () => {
    // The other half of the narrowing: it touches the 400 arm ONLY. A 404 is the
    // server having read its own store, so the id's shape is irrelevant to it, and
    // narrowing that arm too would leave the ordinary deleted-chat case unresolvable.
    mockApiGetTypedOrError.mockResolvedValue({
      ok: false,
      status: 404,
      data: null,
      error: "chat not found",
    });

    expect(await confirmChatExists("c-0123456789abcdef")).toBe("gone");
  });

  it("treats every off-charset id as explainable, mirroring the server's gate", async () => {
    // The shape rule is the server's (`ids.ValidChatID`: non-empty, at most 128
    // bytes, nothing outside `[A-Za-z0-9_-]`), and this pins the boundary from the
    // side that matters — an id the server WOULD refuse must still read as
    // explainable, or a real malformed-id 400 becomes a silent held URL.
    for (const id of [
      "c-a/b",
      "c-a.b",
      "c-a b",
      "../etc/passwd",
      "c-\u00e9",
      "c".repeat(129),
      "..",
    ]) {
      mockApiGetTypedOrError.mockResolvedValue({
        ok: false,
        status: 400,
        data: null,
        error: "invalid chat id",
      });
      expect(await confirmChatExists(id), id).toBe("gone");
    }
  });

  it("treats every ON-charset id as unexplainable, so the 400 stays non-terminal", async () => {
    // The permissive direction the drift argument rests on. An id this client cannot
    // fault is one whose 400 it cannot attribute, so the claim is refused.
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    for (const id of [
      "c-0123456789abcdef",
      "c-1730000000-abc",
      "legacy_id",
      "A-1",
      "c".repeat(128),
    ]) {
      mockApiGetTypedOrError.mockResolvedValue({
        ok: false,
        status: 400,
        data: null,
        error: "invalid origin",
      });
      expect(await confirmChatExists(id), id).toBe("unresolved");
    }
  });

  it("REFUSES the claim on a 500 — the server failed, it did not answer", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    mockApiGetTypedOrError.mockResolvedValue({
      ok: false,
      status: 500,
      data: null,
      error: "internal error",
    });

    expect(await confirmChatExists("c-real")).toBe("unresolved");
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("REFUSES the claim when nothing reached the network", async () => {
    // Status 0 is @cplieger/fetch's whole no-response family: a dead network, a
    // timeout, a caller abort, a request it could not even build. Deriving a
    // terminal verdict from any of them is the defect one layer down from the one
    // this function exists to fix.
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    mockApiGetTypedOrError.mockResolvedValue({
      ok: false,
      status: 0,
      data: null,
      error: "network error",
    });

    expect(await confirmChatExists("c-real")).toBe("unresolved");
  });

  it("REFUSES the claim for a 200 whose body did not decode", async () => {
    // A rejected decoder lands on the failure side carrying the real 2xx status, so
    // the answer arrived and could not be read — which says nothing about the chat.
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    mockApiGetTypedOrError.mockResolvedValue({
      ok: false,
      status: 200,
      data: null,
      error: "$.chat_confirm: not an object",
    });

    expect(await confirmChatExists("c-real")).toBe("unresolved");
    expect(mockUpsertHeader).not.toHaveBeenCalled();
  });

  it("REFUSES the claim for a 2xx that carried no body at all", async () => {
    // An empty 2xx collapses to `data: null`, and an absent body is not a statement
    // about the chat either. Falls through to the status test, which 200 fails.
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    mockApiGetTypedOrError.mockResolvedValue({ ok: true, status: 200, data: null, error: "" });

    expect(await confirmChatExists("c-real")).toBe("unresolved");
    expect(mockUpsertHeader).not.toHaveBeenCalled();
  });
});
