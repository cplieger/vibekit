// The received-event watchdog: the SECOND liveness signal, independent of
// `readyState`.
//
// The server's transport keepalive is an SSE COMMENT, which the EventSource parser
// discards — so an idle healthy stream produces no observable frame in the browser
// and byte recency was unmeasurable here. The server now publishes a NAMED heartbeat
// beside it, and this watchdog reconnects when no event of either kind has arrived
// for the whole silence window.
//
// What it must NOT do is read `readyState`: `sseIsDead` has to treat a mid-handshake
// stream as alive (`pageshow` fires on every cold load after `init` opened the
// source, so a bare `!== OPEN` there tears the stream down and reopens it on every
// page load), and this signal exists precisely because iOS answers OPEN for a stream
// the OS already tore down. The last case in this file fails if the watchdog ever
// starts consulting it.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("./failure-notice.js", () => ({
  reportFailure: vi.fn(),
  clearFailure: vi.fn(),
  _resetForTest: vi.fn(),
}));

// Spied rather than replaced, so the real ramp still runs and the assertion reads its
// own output: what is under test is that the WATCHDOG reconnects through it.
vi.mock("./lib/backoff.js", { spy: true });

import * as backoffMod from "./lib/backoff.js";
import { init, BACKOFF_CAP_MS } from "./transport.js";

const TICK_MS = 15_000;
const SILENCE_MS = 75_000;

/** An EventSource the test drives, including the named-event door the heartbeat
 *  listener registers on — `onmessage` never sees a named event, which is the whole
 *  reason that door exists. */
class FakeSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  /** Every instance ever constructed, so a reconnect is countable. */
  static opened: FakeSource[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  /** An ACCESSOR pair rather than a plain field, so the throwing subclass in the
   *  last case can legally override it — TypeScript refuses to override a property
   *  with an accessor (TS2611). */
  private state = FakeSource.CONNECTING;
  get readyState(): number {
    return this.state;
  }
  set readyState(v: number) {
    this.state = v;
  }
  readonly named = new Map<string, ((e: MessageEvent) => void)[]>();
  constructor(readonly url: string) {
    FakeSource.opened.push(this);
  }
  close(): void {
    this.readyState = FakeSource.CLOSED;
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void): void {
    const list = this.named.get(type) ?? [];
    list.push(fn);
    this.named.set(type, list);
  }
  /** One unnamed frame, the shape that reaches `onmessage`. */
  deliver(id: string, data: string): void {
    this.onmessage?.(new MessageEvent("message", { data, lastEventId: id }));
  }
  /** One NAMED frame, the shape only a registered listener reaches. */
  deliverNamed(type: string, id: string, data: string): void {
    for (const fn of this.named.get(type) ?? []) {
      fn(new MessageEvent(type, { data, lastEventId: id }));
    }
  }
}

const OriginalES = globalThis.EventSource;

/** `init` with nothing observed: every case here reads the constructed-source list. */
function boot(): FakeSource {
  init(
    () => {
      /* frames unobserved */
    },
    () => {
      /* status unobserved */
    },
  );
  const source = FakeSource.opened.at(-1);
  expect(source).toBeDefined();
  return source as FakeSource;
}

/** Advance past the silence threshold, then past the reconnect's own backoff, so a
 *  reconnect that WAS scheduled has actually opened its source.
 *
 *  Past the CAP, not past the first rung: the transport is a module singleton, so the
 *  ramp a previous case in this file escalated is still standing, and every delay it
 *  can draw is inside [0, BACKOFF_CAP_MS). Two separate advances rather than one, so
 *  the reconnect timer created during the first is fired by the second. */
function elapseIntoSilence(): void {
  vi.advanceTimersByTime(SILENCE_MS + TICK_MS);
  vi.advanceTimersByTime(BACKOFF_CAP_MS);
}

describe("the received-event watchdog", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeSource.opened = [];
    (globalThis as { EventSource: unknown }).EventSource = FakeSource;
  });

  afterEach(() => {
    vi.useRealTimers();
    (globalThis as { EventSource: unknown }).EventSource = OriginalES;
  });

  it("tears the stream down and reconnects after silence past the threshold", () => {
    const first = boot();

    elapseIntoSilence();

    expect(FakeSource.opened).toHaveLength(2);
    // Torn down, not merely abandoned: an unclosed source keeps its `onmessage`
    // bound and would deliver frames beside its own replacement, moving the cursor.
    expect(first.readyState).toBe(FakeSource.CLOSED);
  });

  it("leaves a stream alone while it is still inside the window", () => {
    boot();

    // One tick short of the threshold. The window has to clear 2x the heartbeat
    // interval, so a single missed beat is not silence.
    vi.advanceTimersByTime(SILENCE_MS - TICK_MS);

    expect(FakeSource.opened).toHaveLength(1);
  });

  it("resets the clock on an ordinary onmessage frame", () => {
    const first = boot();

    vi.advanceTimersByTime(SILENCE_MS - TICK_MS);
    first.deliver("7", JSON.stringify({ type: "chat_updated", chat_id: "c1" }));
    // Another near-full window from the frame. Without the reset the accumulated
    // silence would be well past the threshold by now.
    vi.advanceTimersByTime(SILENCE_MS - TICK_MS);

    expect(FakeSource.opened).toHaveLength(1);
  });

  it("resets the clock on a named heartbeat event", () => {
    const first = boot();

    vi.advanceTimersByTime(SILENCE_MS - TICK_MS);
    first.deliverNamed("heartbeat", "8", JSON.stringify({ seq: 1 }));
    vi.advanceTimersByTime(SILENCE_MS - TICK_MS);

    expect(FakeSource.opened).toHaveLength(1);
  });

  it("carries the heartbeat's id into the next connect's cursor", () => {
    // The browser advances its OWN Last-Event-ID from a named event, and this
    // module's cursor has to keep step or a reconnect asks for a replay it already
    // has. The cursor rides the URL, since a self-driven reconnect closes the source
    // and so never sends the header.
    const first = boot();
    first.deliverNamed("heartbeat", "4242", JSON.stringify({ seq: 3 }));

    elapseIntoSilence();

    const second = FakeSource.opened.at(-1);
    expect(second).toBeDefined();
    const q = new URL(second?.url ?? "", "https://example.test").searchParams;
    expect(q.get("last_event_id")).toBe("4242");
  });

  it("escalates the delay when the silence repeats", () => {
    // Observed on the WATCHDOG's own path, not by calling the ramp directly: a test
    // that computes the ramp itself proves `lib/backoff` works and stays green when
    // the watchdog reconnects at delay 0 instead of going through it.
    const ramp = vi.mocked(backoffMod.computeBackoff);
    boot();
    ramp.mockClear();

    elapseIntoSilence();
    elapseIntoSilence();

    expect(FakeSource.opened).toHaveLength(3);
    const ms = ramp.mock.results.map((r) =>
      r.type === "return" ? (r.value as { backoffMs: number }).backoffMs : -1,
    );
    expect(ms).toHaveLength(2);
    // Asserted on backoffMs GROWTH, never on a sampled delay: `computeBackoff`
    // jitters the delay inside [0, backoffMs), so two samples can legitimately come
    // back in either order while the ramp is escalating correctly. The premise below
    // fails loudly if a previous case in this file already saturated the singleton's
    // ramp at its cap, where growth is unobservable rather than absent.
    expect(ms[0]).toBeLessThan(BACKOFF_CAP_MS);
    expect(ms[1]).toBeGreaterThan(ms[0] ?? 0);
    // And the ramp is fed by its OWN previous value, which is what carrying the
    // escalation across two silences means.
    expect(ramp.mock.calls[1]?.[0]).toBe(ms[0]);
  });

  it("does not fire while the document is hidden", () => {
    // A backgrounded tab has throttled timers, iOS kills the stream anyway, and the
    // existing visibilitychange/pageshow kick covers the return immediately — so
    // reconnecting here is work nobody is waiting for.
    boot();
    // visibilityState is an accessor on Document.prototype, so shadow it on the
    // instance and restore in finally.
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden" as DocumentVisibilityState,
    });
    try {
      elapseIntoSilence();
      expect(FakeSource.opened).toHaveLength(1);
    } finally {
      Reflect.deleteProperty(document, "visibilityState");
    }
  });

  it("never consults readyState", () => {
    // The killing assertion. A source whose `readyState` getter THROWS makes any read
    // loud, so this fails the moment the watchdog starts reinterpreting `readyState`
    // instead of standing beside it as an independent signal.
    class ThrowingSource extends FakeSource {
      override get readyState(): number {
        throw new Error("the watchdog read readyState");
      }
      override set readyState(_v: number) {
        // Swallowed: the inherited `close()` assigns through here, and the trap is
        // the GETTER — a setter that threw would make teardown itself the failure.
      }
    }
    (globalThis as { EventSource: unknown }).EventSource = ThrowingSource;

    boot();
    expect(() => {
      elapseIntoSilence();
    }).not.toThrow();
    // And it did the work: a reconnect happened without anything reading the getter.
    expect(FakeSource.opened).toHaveLength(2);
  });
});
