// The resume kick, and the readyState it has to read to fire.
//
// A tab coming back to the foreground kicks an immediate reconnect when the
// stream is not alive, to pre-empt the browser's own retry timer (3 s in
// Chromium, and the server emits no `retry:` field to shorten it). The check
// read the CONTROLLER's phase, and `onerror` only demotes that phase when
// `readyState` is CLOSED — so the case the kick exists for, a retryable drop the
// browser is handling internally, left the phase at `connected` and the kick
// never fired. Measured downstream: 32 of 40 `SSE connected` lines carried
// `last_event_id=""`, i.e. recovery was a cold stream rather than a resume.
//
// The second half is the trap in fixing it. `pageshow` fires on every cold load,
// AFTER `init` has opened the source, so a bare `readyState !== OPEN` test reports
// a mid-handshake stream as dead and tears down the connection the boot just
// opened, on every single load.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as transport from "./transport.js";
import { onBus, BUS_TRANSPORT_GAP } from "./bus.js";

/** An EventSource whose readyState the test drives. */
class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  /** Every instance ever constructed, so a reconnect is countable. */
  static opened: FakeEventSource[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  readyState = FakeEventSource.CONNECTING;
  constructor(readonly url: string) {
    FakeEventSource.opened.push(this);
  }
  close(): void {
    this.readyState = FakeEventSource.CLOSED;
  }
  /** Named-event listeners. A named SSE event never reaches `onmessage`, so the
   *  transport registers its heartbeat listener here. Recorded rather than
   *  dropped: a fake that discards a registration cannot be driven from it. */
  readonly named: ((e: MessageEvent) => void)[] = [];
  addEventListener(_type: string, fn: (e: MessageEvent) => void): void {
    this.named.push(fn);
  }
  /** The handshake completing. */
  open(): void {
    this.readyState = FakeEventSource.OPEN;
    this.onopen?.();
  }
  /** One frame, as the browser delivers it. The transport stamps its liveness
   *  clock from this, so it is what proves the pipe carried bytes. */
  deliver(id: string, data: string): void {
    this.onmessage?.(new MessageEvent("message", { data, lastEventId: id }));
  }
  /** A drop the BROWSER will retry on its own: `readyState` goes back to
   *  CONNECTING and `onerror` fires without ever reaching CLOSED, which is why the
   *  controller's phase stays `connected`. */
  retryableDrop(): void {
    this.readyState = FakeEventSource.CONNECTING;
    this.onerror?.();
  }
}

const OriginalES = globalThis.EventSource;

describe("the resume kick", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.opened = [];
    (globalThis as { EventSource: unknown }).EventSource = FakeEventSource;
    // Not vi.resetModules(): the browser's module map is URL-keyed, so a
    // re-import hands back the same singleton and leaves the previous
    // controller's document listeners live.
    transport._resetForTest();
  });

  afterEach(() => {
    transport._resetForTest();
    (globalThis as { EventSource: unknown }).EventSource = OriginalES;
    vi.useRealTimers();
  });

  function boot(): FakeEventSource {
    transport.init(
      () => {
        /* frames unobserved */
      },
      () => {
        /* status unobserved */
      },
    );
    const source = FakeEventSource.opened.at(-1);
    if (source === undefined) {
      throw new Error("init must open a stream");
    }
    return source;
  }

  it("reconnects when the browser is retrying under a still-connected phase", () => {
    const source = boot();
    source.open();
    expect(FakeEventSource.opened).toHaveLength(1);

    source.retryableDrop();
    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: false }));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(2);
    expect(source.readyState).toBe(FakeEventSource.CLOSED);
  });

  it("leaves a stream that is still open alone", () => {
    const source = boot();
    source.open();

    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: false }));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(1);
  });

  it("leaves the first handshake alone, because pageshow races it on every load", () => {
    // Not opened yet: readyState is CONNECTING and nothing has gone wrong. A bare
    // `!== OPEN` test would tear this down and reopen it on every cold load.
    boot();

    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: false }));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(1);
  });

  it("reconnects when the tab returns to a stream the browser gave up on", () => {
    const source = boot();
    source.open();
    source.readyState = FakeEventSource.CLOSED;

    document.dispatchEvent(new Event("visibilitychange"));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(2);
  });

  const ONE_FRAME = JSON.stringify({ type: "chat_updated", chat_id: "c1" });

  /** A SUSPENSION: the wall clock jumps while this page's own timers do not run.
   *  `setSystemTime` moves `Date.now()` and fires nothing, which is exactly what
   *  the OS does to a backgrounded tab. */
  function suspend(ms: number): void {
    vi.setSystemTime(Date.now() + ms);
  }

  /** Time PASSING with the page running: the tick keeps up, so the suspension
   *  detector reads no gap and only the last-frame clock ages. */
  function idle(ms: number): void {
    vi.advanceTimersByTime(ms);
  }

  function setVisibility(state: DocumentVisibilityState): void {
    vi.spyOn(document, "visibilityState", "get").mockReturnValue(state);
  }

  it("reconnects a suspended stream that still reports OPEN", () => {
    const source = boot();
    source.open();
    source.deliver("5", ONE_FRAME);
    expect(FakeEventSource.opened).toHaveLength(1);

    suspend(600_000);
    // THE PREMISE, and the whole defect: iOS answers OPEN for a stream the OS
    // tore down, so `readyState` cannot decide this. Asserted rather than assumed
    // — a CLOSED source here would make the case about the easy half.
    expect(source.readyState).toBe(FakeEventSource.OPEN);

    document.dispatchEvent(new Event("visibilitychange"));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(2);
  });

  it("carries the replay cursor into the resume's reconnect", () => {
    const source = boot();
    source.open();
    source.deliver("7", ONE_FRAME);

    suspend(600_000);
    document.dispatchEvent(new Event("visibilitychange"));
    vi.advanceTimersByTime(0);

    // Without this the reconnect is a cold stream: the server replays from the
    // cursor, and the cursor only travels as a query parameter.
    expect(FakeEventSource.opened.at(-1)?.url).toBe("/api/events?last_event_id=7");
  });

  it("runs the server's gap verdict on the resume, which is what marks the store stale", () => {
    const gaps = vi.fn();
    const unsub = onBus(BUS_TRANSPORT_GAP, gaps);
    try {
      const source = boot();
      source.open();
      source.deliver("7", ONE_FRAME);

      suspend(600_000);
      document.dispatchEvent(new Event("visibilitychange"));
      vi.advanceTimersByTime(0);

      const reopened = FakeEventSource.opened.at(-1);
      expect(reopened).not.toBe(source);
      reopened?.open();
      // The ring wrapped past the cursor during the suspension, so the frames
      // this client missed are gone and only a full reconcile recovers them.
      // `transport:gap` is the ONE trigger of bumpSyncEpoch, and the epoch is
      // what makes a tab switch refetch instead of rendering the pre-sleep store.
      reopened?.deliver(
        "200",
        JSON.stringify({ type: "connected", payload: { floor: 100, head: 200 } }),
      );

      expect(gaps).toHaveBeenCalledWith({ lastSeen: 7, floor: 100, head: 200 });
    } finally {
      unsub();
    }
  });

  it("leaves the stream alone when a frame arrived inside the liveness window", () => {
    const source = boot();
    source.open();
    suspend(600_000);
    // A frame AFTER the clock jump: the pipe is carrying bytes right now, so the
    // timer gap describes a suspension this connection already survived.
    source.deliver("5", ONE_FRAME);

    document.dispatchEvent(new Event("visibilitychange"));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(1);
  });

  it("leaves an idle stream alone after a hide too short to be a suspension", () => {
    const source = boot();
    source.open();
    // No frames for 16s, so the liveness window cannot answer; the tick ran, so
    // the detector reads no gap. The cheap case: nothing is reconnected.
    idle(16_000);
    suspend(3_000);

    document.dispatchEvent(new Event("visibilitychange"));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(1);
  });

  it("reconnects an idle stream when the network comes back", () => {
    const source = boot();
    source.open();
    idle(16_000);

    window.dispatchEvent(new Event("online"));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(2);
  });

  it("reconnects after a freeze however short the timer gap", () => {
    const source = boot();
    source.open();
    idle(16_000);

    // The platform reported its own suspension, so nothing has to be inferred.
    document.dispatchEvent(new Event("freeze"));
    document.dispatchEvent(new Event("visibilitychange"));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(2);
  });

  it("reconnects on the page-lifecycle resume event", () => {
    const source = boot();
    source.open();
    idle(16_000);

    document.dispatchEvent(new Event("resume"));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(2);
  });

  it("reconnects a still-OPEN stream restored from the back/forward cache", () => {
    const source = boot();
    source.open();
    idle(16_000);

    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(2);
  });

  it("opens exactly one stream for a rapid visible-hidden-visible sequence", () => {
    const source = boot();
    source.open();
    source.deliver("5", ONE_FRAME);
    suspend(600_000);

    document.dispatchEvent(new Event("visibilitychange"));
    // The reconnect lands, so a second decision would be observable as a third
    // source rather than as a replaced timer.
    vi.advanceTimersByTime(0);
    expect(FakeEventSource.opened).toHaveLength(2);

    // Each signal 300ms after the last, so the assertion is about the 1000ms
    // window rather than about one millisecond: `advanceTimersByTime(0)` does not
    // move `Date.now()`, so an unspaced sequence passes for any window above 0.
    suspend(300);
    setVisibility("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    suspend(300);
    setVisibility("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    suspend(300);
    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(2);
  });

  it("does not carry a freeze past the resume that met a dead stream", () => {
    const source = boot();
    source.open();
    // Dead already, so this resume answers on `readyState` alone and never
    // consults the freeze — which must still consume it, or the NEXT resume
    // decides on evidence belonging to a connection that is gone.
    source.readyState = FakeEventSource.CLOSED;
    document.dispatchEvent(new Event("freeze"));
    document.dispatchEvent(new Event("visibilitychange"));
    vi.advanceTimersByTime(0);
    expect(FakeEventSource.opened).toHaveLength(2);
    FakeEventSource.opened.at(-1)?.open();

    // Past the coalesce window with the page RUNNING: the tick kept up, so the
    // detector reads no gap, and this signal has no evidence of its own.
    idle(16_000);
    document.dispatchEvent(new Event("visibilitychange"));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(2);
  });

  it("resumes again once the coalesce window has passed", () => {
    const source = boot();
    source.open();
    source.deliver("5", ONE_FRAME);
    suspend(600_000);

    document.dispatchEvent(new Event("visibilitychange"));
    vi.advanceTimersByTime(0);
    expect(FakeEventSource.opened).toHaveLength(2);

    // The gate is a COALESCER, not a once-per-load latch. Without this case a
    // permanent gate passes every swallowing assertion above while making resume
    // fire exactly once per page load — the feature broken in the way a reader
    // would not notice, because the first resume works.
    suspend(1_500);
    document.dispatchEvent(new Event("visibilitychange"));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(3);
  });
});
