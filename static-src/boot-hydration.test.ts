// ---------------------------------------------------------------------------
// A manual page refresh must not lose the server's connect replay, and must not
// reopen chat tabs the user closed.
//
// Both defects came out of boot ORDERING. The EventSource is opened
// synchronously in transport.init, and the server answers immediately with its
// whole connect replay — the handshake, every unanswered permission, and ONE
// `turn_state` per BUSY chat. That last one is the only channel carrying an
// in-flight turn to a new client and is never re-broadcast. The chat store is
// empty until GET /api/chats resolves several awaits later, and every consumer
// of a chat-scoped frame correctly bails when it cannot find the chat it names,
// so on a refresh the whole replay was dropped: every tab dot read `idle`, the
// streaming transcript came back blank, and the composer offered Send over a
// live turn (which turns a stale draft into a mid-turn steer, because a Send
// that meets 409 steers).
//
// A refresh was therefore a WEAKER recovery than a dropped connection, since a
// reconnect fires transport:gap and runs the full reconcile.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import type * as TransportModule from "./transport.js";

/** A minimal EventSource stand-in the test drives frame by frame. */
class FakeEventSource {
  static last: FakeEventSource | null = null;
  onopen: (() => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  readyState = 0;
  closed = false;
  constructor(readonly url: string) {
    FakeEventSource.last = this;
  }
  close(): void {
    this.closed = true;
  }
  /** Deliver one server frame. */
  emit(type: string, payload: unknown, chatID = "", id = ""): void {
    this.onmessage?.({
      data: JSON.stringify({ type, chat_id: chatID, payload }),
      lastEventId: id,
    } as MessageEvent);
  }
}

const OriginalES = globalThis.EventSource;

describe("the transport holds frames until the chat store is hydrated", () => {
  let transport: typeof TransportModule;

  beforeEach(async () => {
    vi.resetModules();
    (globalThis as { EventSource: unknown }).EventSource = FakeEventSource;
    (FakeEventSource as unknown as { CLOSED: number }).CLOSED = 2;
    FakeEventSource.last = null;
    transport = await import("./transport.js");
  });

  afterEach(() => {
    (globalThis as { EventSource: unknown }).EventSource = OriginalES;
    vi.useRealTimers();
  });

  it("delivers nothing before markHydrated, then everything in arrival order", () => {
    const seen: string[] = [];
    transport.init(
      (evt) => {
        seen.push(evt.type);
      },
      () => undefined,
    );
    const es = FakeEventSource.last;
    expect(es).not.toBeNull();

    es?.emit("connected", { floor: 0, head: 0 });
    es?.emit("turn_state", { chunk_seq: 0 }, "chat-1");
    es?.emit("permission_needed", { request_id: 1 }, "chat-1");
    // Nothing has reached the store yet: this is the whole point.
    expect(seen).toEqual([]);

    transport.markHydrated();
    // Order is preserved, and order is load-bearing: a message_chunk that raced
    // the snapshot is made idempotent by the watermark the snapshot installs, so
    // a chunk released before its turn_state would be double-appended.
    expect(seen).toEqual(["connected", "turn_state", "permission_needed"]);
  });

  it("passes frames straight through once hydrated", () => {
    const seen: string[] = [];
    transport.init(
      (evt) => {
        seen.push(evt.type);
      },
      () => undefined,
    );
    transport.markHydrated();
    FakeEventSource.last?.emit("message_chunk", { message_id: "m1", delta: "x" }, "chat-1");
    expect(seen).toEqual(["message_chunk"]);
  });

  it("markHydrated is idempotent and does not re-deliver", () => {
    const seen: string[] = [];
    transport.init(
      (evt) => {
        seen.push(evt.type);
      },
      () => undefined,
    );
    FakeEventSource.last?.emit("turn_state", { chunk_seq: 0 }, "chat-1");
    transport.markHydrated();
    transport.markHydrated();
    transport.markHydrated();
    expect(seen).toEqual(["turn_state"]);
  });

  it("releases what it held if hydration never reports in", () => {
    vi.useFakeTimers();
    const seen: string[] = [];
    transport.init(
      (evt) => {
        seen.push(evt.type);
      },
      () => undefined,
    );
    FakeEventSource.last?.emit("turn_state", { chunk_seq: 0 }, "chat-1");
    expect(seen).toEqual([]);

    // The gate is an ordering aid, not a correctness requirement: a hydration
    // that never lands (an auth bounce, a dead /api/chats) must not wedge the
    // stream, because the store's own missing-session guards still hold under it.
    vi.advanceTimersByTime(25_000);
    expect(seen).toEqual(["turn_state"]);
  });
});
