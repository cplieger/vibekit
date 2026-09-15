// ---------------------------------------------------------------------------
// A manual page refresh must not lose the server's connect hook, and must not
// reopen chat tabs the user closed.
//
// Both defects came out of boot ORDERING. The stream is opened synchronously in
// the adapter's init, and the server answers immediately with its connect hook —
// the handshake and the two aggregate snapshots (every unanswered ask, every
// retained waiting status). Those are sent once per connection and never
// re-broadcast. The chat store is empty until GET /api/chats resolves several
// awaits later, and every consumer of a chat-scoped frame correctly bails when it
// cannot find the chat it names, so on a refresh the whole hook was dropped:
// every tab dot read `idle` and the composer offered Send over a live turn.
//
// The library's own hold is for revalidation and does not replace this gate:
// starting the stream only after hydration would lose every frame published
// between the chat-list response and the stream open, because a fresh hello has
// no replay.
// ---------------------------------------------------------------------------

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { init, markHydrated, _resetForTest } from "./sse-adapter.js";
import { createScriptedFetch, type ScriptedFetch, until } from "./__test-helpers__/sse-fetch.js";

vi.mock("./store-load.js", () => ({ loadList: vi.fn(), loadMessages: vi.fn() }));
vi.mock("./tabs-sync.js", () => ({ listTabs: vi.fn() }));
vi.mock("./run-store.js", () => ({ rebuildLiveRuns: vi.fn(), invalidateCachedRuns: vi.fn() }));
vi.mock("./session-catalog.js", () => ({ fetchCatalog: vi.fn() }));
vi.mock("./send-state.js", () => ({ setSSEStatus: vi.fn() }));
vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));

/** Let the written bytes cross the stream reader: a frame that is still in flight
 *  cannot prove it was held. */
function settle(): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, 20);
  });
}

describe("the adapter holds frames until the chat store is hydrated", () => {
  let scripted: ScriptedFetch;
  let seen: string[];

  beforeEach(() => {
    scripted = createScriptedFetch();
    vi.stubGlobal("fetch", scripted.fetch);
    seen = [];
  });

  function boot(): void {
    init(
      (evt) => {
        seen.push(evt.type);
      },
      () => undefined,
    );
  }

  afterEach(() => {
    _resetForTest();
    vi.useRealTimers();
  });

  async function opened(): Promise<NonNullable<ScriptedFetch["connections"][number]>> {
    await until(() => scripted.connections.length === 1);
    const conn = scripted.connections[0];
    if (conn === undefined) {
      throw new Error("no connection");
    }
    conn.hello();
    return conn;
  }

  it("delivers nothing before markHydrated, then everything in arrival order", async () => {
    boot();
    const conn = await opened();
    conn.frame({ type: "connected", payload: { busy_stated: false, live_runs_stated: false } });
    conn.frame({ type: "pending_snapshot", payload: { items: [] } });
    conn.frame({ type: "permission_needed", chat_id: "chat-1", payload: { request_id: 1 } });
    await settle();
    // Nothing has reached the store yet: this is the whole point.
    expect(seen).toEqual([]);

    markHydrated();
    // Order is preserved, and order is load-bearing: a message_chunk released before
    // the message_created it extends is orphaned.
    expect(seen).toEqual(["connected", "pending_snapshot", "permission_needed"]);
  });

  it("passes frames straight through once hydrated", async () => {
    boot();
    const conn = await opened();
    markHydrated();
    conn.frame({
      type: "message_chunk",
      chat_id: "chat-1",
      payload: { message_id: "m1", delta: "x" },
    });
    await until(() => seen.length === 1);
    expect(seen).toEqual(["message_chunk"]);
  });

  it("markHydrated is idempotent and does not re-deliver", async () => {
    boot();
    const conn = await opened();
    conn.frame({ type: "pending_snapshot", payload: { items: [] } });
    await settle();
    markHydrated();
    markHydrated();
    markHydrated();
    expect(seen).toEqual(["pending_snapshot"]);
  });

  it("releases what it held if hydration never reports in", async () => {
    // Fake timers that still advance with the clock: the gate's watchdog is armed at
    // init and has to be jumpable, while the stream's bytes still need real ticks to
    // cross the reader.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    boot();
    const conn = await opened();
    conn.frame({ type: "pending_snapshot", payload: { items: [] } });
    await settle();
    expect(seen).toEqual([]);

    // The gate is an ordering aid, not a correctness requirement: a hydration that
    // never lands (an auth bounce, a dead /api/chats) must not wedge the stream,
    // because the store's own missing-session guards still hold under it.
    vi.advanceTimersByTime(25_000);
    expect(seen).toEqual(["pending_snapshot"]);
  });
});
